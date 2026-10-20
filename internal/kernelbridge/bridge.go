package kernelbridge

import (
	"encoding/json"
	"fmt"
)

// ── 桥接的方法面 (与 bridge/server.mjs 的 handlers 一一对应) ──────────────

// ChannelSubmit 往一个 channel 投递串行任务。同 channel 内严格 FIFO,
// 不同 channel 互不阻塞。marker 与 sleepMs 由 Go 侧给出, 返回执行结果。
func (c *Client) ChannelSubmit(channelID, taskID, marker string, sleepMs int) (map[string]any, error) {
	var out map[string]any
	if err := c.call("channel.submit", map[string]any{
		"channelId": channelID,
		"taskId":    taskID,
		"marker":    marker,
		"sleepMs":   sleepMs,
	}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ChannelPeek 取一个 channel actor 的当前状态快照。
func (c *Client) ChannelPeek(channelID string) (map[string]any, error) {
	var out map[string]any
	if err := c.call("channel.peek", map[string]any{"channelId": channelID}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// LifecycleSubmit 经 Run 生命周期端口写一条记录。ports 未注入 ⇒ 内核 fail-closed。
func (c *Client) LifecycleSubmit(request map[string]any, ports map[string]any) (map[string]any, error) {
	var out map[string]any
	if err := c.call("lifecycle.submit", map[string]any{
		"request": request,
		"ports":   ports,
	}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// TransportSubmit 经通信端口发一条消息。ports 未注入 ⇒ fail-closed。
func (c *Client) TransportSubmit(request map[string]any, ports map[string]any) (map[string]any, error) {
	var out map[string]any
	if err := c.call("transport.submit", map[string]any{
		"request": request,
		"ports":   ports,
	}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ModelAcquire 取一次模型只读租约 (K6: acquire 只读, 连接复用/超时/取消)。
func (c *Client) ModelAcquire(snapshot map[string]any, ports map[string]any) (map[string]any, error) {
	var out map[string]any
	if err := c.call("model.acquire", map[string]any{
		"snapshot": snapshot,
		"ports":    ports,
	}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// BolloonExec 把一次 bolloon CLI 调用放进内核 ChannelActor 里串行/审计执行
// （kernel @bolloon/bolloon-kernel 作为执行核心）。args 是传给 bolloon 的子命令
// 参数（如 "network" "join" "<link>"）。返回内核回传的执行结果，其中
// result.stdout / result.stderr / result.code 是 exec.bolloon 端口捕获的。
//
// 路径: Forge(Go) → JSON-RPC → bridge/server.mjs → ChannelActor.submit →
// exec.bolloon 端口 → 本机 bolloon 二进制。这是"核聚变"走内核编排去驱动
// BOLLOON 去中心化内核（外部多智能体交互）的正式入口；直接用 internal/bolloon
// 则是等价的强类型壳，不经内核调度。
func (c *Client) BolloonExec(args ...string) (map[string]any, error) {
	var out map[string]any
	if err := c.call("bolloon.exec", map[string]any{
		"args":  args,
		"ports": map[string]any{"exec": "bolloon"},
	}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// call 是 rpc 的泛型外壳: 把响应 JSON 解到 out。
func (c *Client) call(method string, params map[string]any, out any) error {
	raw, err := c.rpc(method, params)
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return fmt.Errorf("method %s: empty result", method)
	}
	return json.Unmarshal(raw, out)
}
