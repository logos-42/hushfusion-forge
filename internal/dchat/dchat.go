// Package dchat 接入去中心化通信频道：iroh / hyperswarm / orbitdb。
//
// 设计：Go 侧只做“进程 + JSON 行协议”的薄客户端，真正的 P2P 栈放在 Node
// 助手（dchat-node.mjs）里动态加载。缺依赖/缺二进制时 fail-closed，并说明
// 缺什么，绝不假装已连通。
//
// 协议（stdin/stdout，每行一个 JSON）：
//
//	→ {"cmd":"status","channel":"hyperswarm"}
//	← {"ok":true,"data":{...}}
package dchat

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

//go:embed dchat-node.mjs
var nodeScript string

// Channels 是受支持的频道名。
var Channels = []string{"iroh", "hyperswarm", "orbitdb"}

// Manager 持有 Node 助手进程（每个频道一个，懒启动）。
type Manager struct {
	mu    sync.Mutex
	dir   string
	bin   string
	chans map[string]*nodeChan
}

// NewManager 准备脚本目录；不启动任何进程。
func NewManager() (*Manager, error) {
	dir, err := os.MkdirTemp("", "forge-dchat-")
	if err != nil {
		return nil, err
	}
	script := filepath.Join(dir, "dchat-node.mjs")
	if err := os.WriteFile(script, []byte(nodeScript), 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	bin := strings.TrimSpace(os.Getenv("DCHAT_NODE_BIN"))
	if bin == "" {
		bin = "node"
	}
	return &Manager{dir: dir, bin: bin, chans: map[string]*nodeChan{}}, nil
}

// Close 关掉所有助手进程并清临时目录。
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var firstErr error
	for name, c := range m.chans {
		if err := c.close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("%s: %w", name, err)
		}
	}
	m.chans = map[string]*nodeChan{}
	if m.dir != "" {
		if err := os.RemoveAll(m.dir); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (m *Manager) channel(name string) (*nodeChan, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return nil, fmt.Errorf("dchat: channel 为空（支持 %s）", strings.Join(Channels, "/"))
	}
	ok := false
	for _, c := range Channels {
		if c == name {
			ok = true
		}
	}
	if !ok {
		return nil, fmt.Errorf("dchat: 不支持的频道 %q（支持 %s）", name, strings.Join(Channels, "/"))
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if c := m.chans[name]; c != nil {
		return c, nil
	}
	c, err := startNodeChan(m.bin, filepath.Join(m.dir, "dchat-node.mjs"))
	if err != nil {
		return nil, err
	}
	m.chans[name] = c
	return c, nil
}

// Status 返回频道状态（可用性 / DID / peerId 等）。
func (m *Manager) Status(ctx context.Context, channel string) (map[string]any, error) {
	c, err := m.channel(channel)
	if err != nil {
		return nil, err
	}
	return c.call(ctx, map[string]any{"cmd": "status", "channel": strings.ToLower(channel)})
}

// Send 往频道 topic 发一条消息。
func (m *Manager) Send(ctx context.Context, channel, topic, body string) (map[string]any, error) {
	c, err := m.channel(channel)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(topic) == "" {
		return nil, fmt.Errorf("dchat: topic 为空")
	}
	if strings.TrimSpace(body) == "" {
		return nil, fmt.Errorf("dchat: body 为空")
	}
	return c.call(ctx, map[string]any{"cmd": "send", "channel": strings.ToLower(channel), "topic": topic, "body": body})
}

// Receive 收最近 limit 条消息。
func (m *Manager) Receive(ctx context.Context, channel, topic string, limit int) (map[string]any, error) {
	c, err := m.channel(channel)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	return c.call(ctx, map[string]any{"cmd": "recv", "channel": strings.ToLower(channel), "topic": topic, "limit": limit})
}

// nodeChan 是一个常驻 Node 助手进程。
type nodeChan struct {
	cmd    *exec.Cmd
	stdin  *bufio.Writer
	stdout *bufio.Reader
	mu     sync.Mutex
	broken bool
}

func startNodeChan(bin, script string) (*nodeChan, error) {
	if _, err := exec.LookPath(bin); err != nil {
		if bin != "node" {
			return nil, fmt.Errorf("dchat: Node 二进制 %q 不可用: %w", bin, err)
		}
		return nil, fmt.Errorf("dchat: 找不到 node（设置 DCHAT_NODE_BIN 指到 Node）: %w", err)
	}
	cmd := exec.Command(bin, script)
	cmd.Env = os.Environ()
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &nodeChan{cmd: cmd, stdin: bufio.NewWriter(in), stdout: bufio.NewReader(out)}, nil
}

func (c *nodeChan) call(ctx context.Context, req map[string]any) (map[string]any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.broken {
		return nil, fmt.Errorf("dchat: 助手进程已断开（重启后重试）")
	}
	b, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if _, err := c.stdin.Write(append(b, '\n')); err != nil {
		c.broken = true
		_ = c.cmd.Process.Kill()
		return nil, err
	}
	if err := c.stdin.Flush(); err != nil {
		c.broken = true
		_ = c.cmd.Process.Kill()
		return nil, err
	}
	type res struct {
		v   map[string]any
		err error
	}
	done := make(chan res, 1)
	go func() {
		line, err := c.stdout.ReadBytes('\n')
		if err != nil {
			done <- res{err: err}
			return
		}
		var v map[string]any
		if err := json.Unmarshal(line, &v); err != nil {
			done <- res{err: fmt.Errorf("dchat: 助手返回非 JSON: %q", string(line))}
			return
		}
		done <- res{v: v}
	}()
	select {
	case <-ctx.Done():
		c.broken = true
		_ = c.cmd.Process.Kill()
		return nil, ctx.Err()
	case r := <-done:
		if r.err != nil {
			c.broken = true
			_ = c.cmd.Process.Kill()
			return nil, r.err
		}
		if ok, _ := r.v["ok"].(bool); !ok {
			msg, _ := r.v["error"].(string)
			if msg == "" {
				msg = "未知错误"
			}
			return nil, fmt.Errorf("dchat: %s", msg)
		}
		data, _ := r.v["data"].(map[string]any)
		if data == nil {
			data = map[string]any{}
		}
		return data, nil
	}
}

func (c *nodeChan) close() error {
	if c == nil || c.cmd == nil || c.cmd.Process == nil {
		return nil
	}
	_ = c.cmd.Process.Kill()
	_, _ = c.cmd.Process.Wait()
	return nil
}
