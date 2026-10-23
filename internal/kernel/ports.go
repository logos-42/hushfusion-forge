package kernel

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
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
	Exec func(args []string) (map[string]any, error)
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

// execBolloon 真正 spawn 本机 bolloon 二进制 (去中心化智能体 CLI), 追加 --json。
// 二进制路径由 BOLLOON_BIN 覆盖 (测试/非常规安装用); 内核只负责执行, 不判业务。
func execBolloon(args []string) (map[string]any, error) {
	bin := os.Getenv("BOLLOON_BIN")
	if bin == "" {
		bin = "bolloon"
	}
	argv := append([]string{}, args...)
	if !containsStr(argv, "--json") {
		argv = append(argv, "--json")
	}
	cmd := exec.Command(bin, argv...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return map[string]any{
				"ok":     false,
				"code":   ee.ExitCode(),
				"stdout": strings.TrimSpace(stdout.String()),
				"stderr": strings.TrimSpace(stderr.String()),
			}, nil
		}
		// 二进制缺失等: 如实反映为失败 (ok:false), 不假装成功。
		return map[string]any{"ok": false, "code": -1, "stdout": "", "stderr": err.Error()}, nil
	}
	return map[string]any{
		"ok":     true,
		"code":   0,
		"stdout": strings.TrimSpace(stdout.String()),
		"stderr": strings.TrimSpace(stderr.String()),
	}, nil
}

func containsStr(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
