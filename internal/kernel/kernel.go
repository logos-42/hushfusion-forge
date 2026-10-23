package kernel

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Kernel 是「一个内核实例」的统一接口 (机制原语)。背后的实现是进程内的原生
// Go 内核 (本包), 也可由 DualKernel 递归组合成双核冗余。
type Kernel interface {
	ChannelSubmit(channelID, taskID, marker string, sleepMs int) (map[string]any, error)
	ChannelPeek(channelID string) (map[string]any, error)
	LifecycleSubmit(request, ports map[string]any) (map[string]any, error)
	TransportSubmit(request, ports map[string]any) (map[string]any, error)
	ModelAcquire(snapshot, ports map[string]any) (map[string]any, error)
	BolloonExec(args ...string) (map[string]any, error)
	Close() error
}

// 编译期断言: *Client 满足 Kernel。
var _ Kernel = (*Client)(nil)

// Client 是原生内核的一个会话 (进程内, 无子进程 / 无跨语言桥)。
// 每个 Client 持有独立的 actor 注册表与审计流水 —— 新建即干净, 天然隔离。
type Client struct {
	reg     *registry
	lcAudit auditLog
	trAudit auditLog
}

// New 构造一个原生内核客户端。进程内即用, 不需要 node / bridge。
func New() *Client {
	return &Client{reg: newRegistry()}
}

// Close 是原生内核的收尾 (无外部进程); 幂等。
func (c *Client) Close() error { return nil }

// RunLifecycleAudit / TransportAudit 暴露审计流水 (测试与运维可读)。
func (c *Client) RunLifecycleAudit() []auditEntry { return c.lcAudit.entries }
func (c *Client) TransportAudit() []auditEntry    { return c.trAudit.entries }

// ChannelSubmit 往一个 channel 投递串行任务。同 channel 内严格 FIFO,
// 不同 channel 互不阻塞。返回执行结果 (与原内核逐字一致的形状)。
func (c *Client) ChannelSubmit(channelID, taskID, marker string, sleepMs int) (map[string]any, error) {
	if channelID == "" {
		return nil, errors.New("params.channelId 必填")
	}
	if taskID == "" {
		taskID = fmt.Sprintf("task-%d", time.Now().UnixNano())
	}
	if marker == "" {
		marker = taskID
	}
	actor := c.reg.getOrCreate("bridge:"+channelID, map[string]any{"channelId": channelID, "kind": "bridge-task"})
	startedAt := time.Now().UnixMilli()
	result := actor.Submit(func(st ActorState) any {
		sleepFor(sleepMs)
		at := time.Now().UnixMilli()
		st["lastTask"] = map[string]any{"taskId": taskID, "marker": marker, "at": at}
		return map[string]any{"taskId": taskID, "marker": marker, "sleepMs": sleepMs, "at": at}
	})
	finishedAt := time.Now().UnixMilli()
	return map[string]any{
		"ok": true, "via": "kernel-channel-actor", "result": result,
		"startedAt": startedAt, "finishedAt": finishedAt,
	}, nil
}

// ChannelPeek 取一个 channel actor 的当前状态快照 (不存在 ⇒ fail-closed ok:false)。
func (c *Client) ChannelPeek(channelID string) (map[string]any, error) {
	if channelID == "" {
		return nil, errors.New("params.channelId 必填")
	}
	actor := c.reg.peek("bridge:" + channelID)
	if actor == nil {
		return map[string]any{"ok": false, "via": "kernel-channel-actor", "detail": "actor 不存在"}, nil
	}
	return map[string]any{"ok": true, "via": "kernel-channel-actor", "state": actor.snapshot()}, nil
}

// LifecycleSubmit 经 Run 生命周期端口写一条记录。ports 未注入 ⇒ 内核 fail-closed (ok:false)。
func (c *Client) LifecycleSubmit(request, ports map[string]any) (map[string]any, error) {
	p, err := resolvePorts(ports)
	if err != nil {
		return nil, err
	}
	return SubmitRunLifecycle(request, p, &c.lcAudit), nil
}

// TransportSubmit 经通信端口发一条消息。ports 未注入 ⇒ fail-closed。
func (c *Client) TransportSubmit(request, ports map[string]any) (map[string]any, error) {
	p, err := resolvePorts(ports)
	if err != nil {
		return nil, err
	}
	return SubmitTransport(request, p, &c.trAudit), nil
}

// ModelAcquire 取一次模型只读租约。openConnection 端口未注入 ⇒ 构造期报错 (fail-closed)。
func (c *Client) ModelAcquire(snapshot, ports map[string]any) (map[string]any, error) {
	p, err := resolvePorts(ports)
	if err != nil {
		return nil, err
	}
	rt, err := NewModelRuntime(p)
	if err != nil {
		return nil, err
	}
	lease, err := rt.Acquire(snapshot, nil)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "via": "kernel-model-runtime", "lease": lease, "runtime": rt}, nil
}

// BolloonExec 把一次 bolloon CLI 调用放进内核 ChannelActor 里串行/审计执行。
// 便捷入口: 固定注入 exec.bolloon 端口 (调用方无需给 ports)。
// 不可取消（用 context.Background()）；要可取消/可中断用 BolloonExecContext。
func (c *Client) BolloonExec(args ...string) (map[string]any, error) {
	return c.BolloonExecContext(context.Background(), args...)
}

// BolloonExecContext 同 BolloonExec，但接受 ctx：取消即强杀 bolloon 进程组
// （含孙子），不等自然结束。TUI 的 Ctrl+C 中断、上层超时看门狗都走这条。
func (c *Client) BolloonExecContext(ctx context.Context, args ...string) (map[string]any, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	return c.execThroughChannel(ctx, args, map[string]any{"exec": "bolloon"})
}

// ExecThroughChannel 暴露底层原语: exec 端口必须显式注入, 否则 fail-closed。
// (对齐原内核「不默认任何端口」的纪律; 便捷入口 BolloonExec 才固定注入。)
func (c *Client) ExecThroughChannel(args []string, ports map[string]any) (map[string]any, error) {
	return c.execThroughChannel(context.Background(), args, ports)
}

func (c *Client) execThroughChannel(ctx context.Context, args []string, ports map[string]any) (map[string]any, error) {
	if args == nil {
		return nil, errors.New("params.args 必填 (string[])")
	}
	p, err := resolvePorts(ports)
	if err != nil {
		return nil, err
	}
	if p.Exec == nil {
		return nil, errors.New("exec 端口未注入 (fail-closed): 调用方必须显式给 ports.exec")
	}
	const channelID = "bolloon"
	actor := c.reg.getOrCreate("bridge:"+channelID, map[string]any{"channelId": channelID, "kind": "bolloon-cli"})
	result := actor.Submit(func(st ActorState) any {
		out, execErr := p.Exec(ctx, args)
		if execErr != nil {
			// 端口自身失败（区别于 bolloon 退出码非 0 —— 那是 out.ok=false）。
			return map[string]any{"ok": false, "error": execErr.Error()}
		}
		st["lastBolloon"] = map[string]any{"args": args, "at": time.Now().UnixMilli()}
		return out
	})
	return map[string]any{"ok": true, "via": "kernel-channel-actor", "result": result}, nil
}
