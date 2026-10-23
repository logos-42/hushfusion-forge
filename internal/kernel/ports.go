package kernel

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/logos-42/hushfusion-forge/internal/proc"
	"github.com/logos-42/hushfusion-forge/internal/sandbox"
)

// errUnknownPort 表示调用方注入了一个内核不认识的命名端口 (fail-closed)。
func errUnknownPort(name string, avail []string) error {
	return fmt.Errorf("未知命名端口: %s (可用: %s)", name, strings.Join(avail, ", "))
}

// Conn 是 ModelRuntime 的一条连接 (由 openConnection 端口注入实现)。
type Conn interface {
	Call(req map[string]any) (map[string]any, error)
	Close() error
}

// Ports 是解析后的端口函数集合 (由命名端口注入)。nil 字段 = 该端口未注入。
type Ports struct {
	StartRun       func(payload any) (any, error)
	RecordStep     func(runID string, payload any) (any, error)
	FinishRun      func(runID string, payload any) (any, error)
	SaveCheckpoint func(runID string, payload any) (any, error)

	Send      func(peerID, kind string, payload any) (any, error)
	Broadcast func(kind string, payload any) (any, error)
	Peers     func() (any, error)

	OpenConnection func(snapshot map[string]any) (Conn, error)

	// Exec 执行一次外部进程调用 (bolloon CLI); 返回 {ok, code, stdout, stderr}。
	// 带 ctx：取消即强杀进程组（含孙子），不等自然结束。
	Exec func(ctx context.Context, args []string) (map[string]any, error)
}

// namedPort 是「名字 → 装配函数」。名字形如 "<端口>.<实现>" (如 recordStep.echo)。
// 接口层传不了真实函数, 故端口注入用名字; 这里解析成 Go 函数 (对齐原 server.mjs)。
type namedPort struct {
	name   string
	assign func(p *Ports)
}

func namedPorts() []namedPort {
	return []namedPort{
		{"recordStep.echo", func(p *Ports) {
			p.RecordStep = func(_ string, _ any) (any, error) { return map[string]any{"ok": true}, nil }
		}},
		{"send.echo", func(p *Ports) {
			p.Send = func(_, _ string, _ any) (any, error) { return map[string]any{"ok": true}, nil }
		}},
		{"openConnection.fake", func(p *Ports) {
			p.OpenConnection = func(_ map[string]any) (Conn, error) { return &fakeConn{}, nil }
		}},
		{"exec.bolloon", func(p *Ports) {
			p.Exec = execBolloon
		}},
	}
}

// resolvePorts 把调用方给的 {端口名: 命名端口} 解析成 *Ports。未知名字 ⇒ 报错 (fail-closed)。
func resolvePorts(in map[string]any) (*Ports, error) {
	reg := namedPorts()
	avail := make([]string, 0, len(reg))
	byName := make(map[string]namedPort, len(reg))
	for _, np := range reg {
		avail = append(avail, np.name)
		byName[np.name] = np
	}
	p := &Ports{}
	for key, val := range in {
		name := fmt.Sprintf("%s.%s", key, asString(val))
		np, ok := byName[name]
		if !ok {
			return nil, errUnknownPort(name, avail)
		}
		np.assign(p)
	}
	return p, nil
}

// fakeConn 是 openConnection.fake 注入的连接: call 恒 ok。
type fakeConn struct{}

func (c *fakeConn) Call(_ map[string]any) (map[string]any, error) {
	return map[string]any{"ok": true}, nil
}
func (c *fakeConn) Close() error { return nil }

// execBolloon 经进程管理器 + 沙箱围栏真正 spawn 本机 bolloon 二进制
// (去中心化智能体 CLI), 追加 --json。
//
// 两条线都不省：
//   - proc：登记 + 并发闸 + ctx 取消传播 + 超时杀进程组 + waiter 回收（不留僵尸）；
//   - sandbox：目录 jail + env 白名单 + 独立进程组（否则杀不掉孙子）。
//
// 二进制路径由 BOLLOON_BIN 覆盖 (测试/非常规安装用); 内核只负责执行, 不判业务。
// 围栏策略全部来自 env（见 execPolicyFromEnv），默认保守。
func execBolloon(ctx context.Context, args []string) (map[string]any, error) {
	bin := os.Getenv("BOLLOON_BIN")
	if bin == "" {
		bin = "bolloon"
	}
	argv := append([]string{}, args...)
	if !containsStr(argv, "--json") {
		argv = append(argv, "--json")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// 并发闸（进程级资源约束；满了 fail-closed，不排队吞掉）。
	if max := execMaxConcurrent(); max > 0 && bolloonRunning() >= max {
		return map[string]any{
			"ok": false, "code": -1, "stdout": "",
			"stderr": fmt.Sprintf("bolloon exec 并发已达上限 %d（fail-closed）", max),
		}, nil
	}
	policy := execPolicyFromEnv()
	var stdout, stderr strings.Builder
	id := fmt.Sprintf("%s-%d", bolloonProcName, time.Now().UnixNano())
	p, err := kernelProc.Start(ctx, id, bin, argv, proc.Options{
		Stdout:  &stdout,
		Stderr:  &stderr,
		Timeout: policy.Timeout,
		Fence: func(c *exec.Cmd) error {
			_, ferr := sandbox.Apply(c, policy)
			return ferr
		},
	})
	if err != nil {
		// 二进制缺失 / 围栏拒绝 / 启动失败：如实反映为失败，不假装成功。
		return map[string]any{"ok": false, "code": -1, "stdout": "", "stderr": err.Error()}, nil
	}
	_ = kernelProc.Wait(p.ID)
	info, _ := kernelProc.Get(p.ID)
	kernelProc.Forget(p.ID) // 结束即注销，长驻进程里登记表不无限增长
	if info.TimedOut {
		return map[string]any{
			"ok": false, "code": info.ExitCode,
			"stdout": strings.TrimSpace(stdout.String()),
			"stderr": strings.TrimSpace(stderr.String()) + "（沙箱超时强杀）",
		}, nil
	}
	return map[string]any{
		"ok":     info.State == proc.StateDone,
		"code":   info.ExitCode,
		"stdout": strings.TrimSpace(stdout.String()),
		"stderr": strings.TrimSpace(stderr.String()),
	}, nil
}

const bolloonProcName = "bolloon"

// kernelProc 是进程级外部进程管理器。"最多同时几个 bolloon" 是进程级资源约束
// （不是每会话的），所以放包级；并发上限在调用时读 env（测试可 t.Setenv）。
var kernelProc = proc.NewManager(0)

// bolloonRunning 当前运行中的 bolloon exec 数。
// 按 ID 前缀识别（Name 是二进制绝对路径，不适合做标识；ID 由 execBolloon 统一命名）。
func bolloonRunning() int {
	n := 0
	for _, in := range kernelProc.List() {
		if strings.HasPrefix(in.ID, bolloonProcName+"-") && in.State == proc.StateRunning {
			n++
		}
	}
	return n
}

// execPolicyFromEnv 从环境变量构造 exec 围栏（默认保守，可显式覆盖）：
//
//	BOLLOON_EXEC_ROOT           jail 根（空/未设 = 不切 cwd）
//	BOLLOON_EXEC_TIMEOUT        Go duration（未设/0 = 不限；到点强杀进程组）
//	BOLLOON_EXEC_ENV_ALLOW      逗号分隔白名单；未设 = 默认最小集 PATH,HOME,BOLLOON_BIN；
//	                            显式设空串 = 不给任何环境变量（最严）
//	BOLLOON_EXEC_MAX_CONCURRENT 并发上限（0 = 不限；满了 fail-closed）
func execPolicyFromEnv() sandbox.Policy {
	p := sandbox.Policy{EnvSet: map[string]string{}}
	if v := os.Getenv("BOLLOON_EXEC_ROOT"); v != "" {
		p.Root = v
	}
	if v := os.Getenv("BOLLOON_EXEC_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			p.Timeout = d
		}
	}
	if raw, ok := os.LookupEnv("BOLLOON_EXEC_ENV_ALLOW"); ok {
		p.EnvAllow = splitCSV(raw) // 显式设空串 ⇒ 空白名单 ⇒ 空环境（最严）
	} else {
		p.EnvAllow = []string{"PATH", "HOME", "BOLLOON_BIN"} // 默认最小集
	}
	// 资源上限（需宿主 prlimit；缺失则启动失败 —— fail-closed，不静默放开）。
	if v := os.Getenv("BOLLOON_EXEC_CPU_SECONDS"); v != "" {
		if s, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && s > 0 {
			p.Limits.CPU = time.Duration(s) * time.Second
		}
	}
	if v := os.Getenv("BOLLOON_EXEC_AS_BYTES"); v != "" {
		if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil && n > 0 {
			p.Limits.AS = n
		}
	}
	if v := os.Getenv("BOLLOON_EXEC_NOFILE"); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
			p.Limits.NOFILE = n
		}
	}
	if v := os.Getenv("BOLLOON_EXEC_NPROC"); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
			p.Limits.NPROC = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("BOLLOON_EXEC_NET_ISOLATE")); v == "1" || strings.EqualFold(v, "true") {
		p.NetIsolate = true
	}
	return p
}

// execMaxConcurrent 读并发上限（非法值按 0 = 不限）。
func execMaxConcurrent() int {
	n := 0
	if v := os.Getenv("BOLLOON_EXEC_MAX_CONCURRENT"); v != "" {
		if x, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && x > 0 {
			n = x
		}
	}
	return n
}

func splitCSV(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if t := strings.TrimSpace(part); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func containsStr(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
