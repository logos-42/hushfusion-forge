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
	"time"
)

//go:embed dchat-node.mjs
var nodeScript string

//go:embed node/package.json
var nodePkg string

// Channels 是受支持的频道名。
var Channels = []string{"iroh", "hyperswarm", "orbitdb"}

// Manager 持有 Node 助手进程（每个频道一个，懒启动）。
//
// 助手目录默认持久化在 ~/.forge/dchat（可用 DCHAT_NODE_DIR 覆盖）：
// 首次缺 npm 依赖时自动 `npm install` 一次并重试；目录不随 Close 删除，
// 装好的 node_modules 跨进程复用。DCHAT_NODE_DIR 指向临时目录时按临时处理。
type Manager struct {
	mu    sync.Mutex
	dir   string
	bin   string
	temp  bool
	chans map[string]*nodeChan
	// installed 标记"自动安装只试一次"，避免每次调用都空转 npm。
	installed bool
}

// NewManager 准备助手目录（持久）与脚本；不启动任何进程。
func NewManager() (*Manager, error) {
	bin := strings.TrimSpace(os.Getenv("DCHAT_NODE_BIN"))
	if bin == "" {
		bin = "node"
	}
	dir := strings.TrimSpace(os.Getenv("DCHAT_NODE_DIR"))
	temp := false
	if dir == "" {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			dir = filepath.Join(home, ".forge", "dchat")
		}
	}
	if dir == "" {
		var err error
		dir, err = os.MkdirTemp("", "forge-dchat-")
		if err != nil {
			return nil, err
		}
		temp = true
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	fail := func(err error) (*Manager, error) {
		if temp {
			_ = os.RemoveAll(dir)
		}
		return nil, err
	}
	script := filepath.Join(dir, "dchat-node.mjs")
	if cur, rerr := os.ReadFile(script); rerr != nil || string(cur) != nodeScript {
		if err := os.WriteFile(script, []byte(nodeScript), 0o600); err != nil {
			return fail(err)
		}
	}
	pkg := filepath.Join(dir, "package.json")
	if _, rerr := os.ReadFile(pkg); rerr != nil {
		if err := os.WriteFile(pkg, []byte(nodePkg), 0o600); err != nil {
			return fail(err)
		}
	}
	return &Manager{dir: dir, bin: bin, temp: temp, chans: map[string]*nodeChan{}}, nil
}

// Close 关掉所有助手进程；持久目录保留（node_modules 复用），临时目录删除。
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
	if m.temp && m.dir != "" {
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
	c, err := m.startWithDeps()
	if err != nil {
		return nil, err
	}
	m.chans[name] = c
	return c, nil
}

// needsDepInstall 判断错误是否为"Node 依赖缺失"（触发自动 npm install）。
func needsDepInstall(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "Cannot find package") ||
		strings.Contains(msg, "Cannot find module") ||
		strings.Contains(msg, "MODULE_NOT_FOUND")
}

// startWithDeps 启动助手并用 status 探测；缺依赖时自动 npm install 一次再重试。
func (m *Manager) startWithDeps() (*nodeChan, error) {
	c, err := startNodeChan(m.bin, filepath.Join(m.dir, "dchat-node.mjs"))
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	_, perr := c.call(ctx, map[string]any{"cmd": "status", "channel": "hyperswarm"})
	cancel()
	if perr == nil || !needsDepInstall(perr) {
		return c, nil // 探测通过，或错误与依赖无关（留给调用方如实处理）
	}
	c.close()
	if ierr := m.installDeps(); ierr != nil {
		return nil, fmt.Errorf("%w；自动安装依赖失败: %v", perr, ierr)
	}
	c2, err2 := startNodeChan(m.bin, filepath.Join(m.dir, "dchat-node.mjs"))
	if err2 != nil {
		return nil, err2
	}
	return c2, nil
}

// installDeps 在助手目录执行一次 npm install（声明依赖来自 package.json）。
func (m *Manager) installDeps() error {
	if m.installed {
		return fmt.Errorf("已自动安装过一次仍缺依赖；请手动执行: npm install --prefix %s", m.dir)
	}
	m.installed = true
	npm, err := exec.LookPath("npm")
	if err != nil {
		return fmt.Errorf("找不到 npm: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, npm, "install", "--no-audit", "--no-fund")
	cmd.Dir = m.dir
	out, err := cmd.CombinedOutput()
	if len(out) > 2*1024 {
		out = out[:2*1024]
	}
	if err != nil {
		return fmt.Errorf("npm install 失败: %v\n%s", err, out)
	}
	return nil
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
	cmd.Dir = filepath.Dir(script) // IPFS repo 等相对路径落在助手目录，不污染进程 cwd
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
		// 助手 stdout 可能混入库横幅（如 IPFS 初始化 "generating Ed25519
		// keypair..."），非 JSON 行跳过，只认 JSON 响应。
		for {
			line, err := c.stdout.ReadBytes('\n')
			if err != nil {
				done <- res{err: err}
				return
			}
			var v map[string]any
			if jsonErr := json.Unmarshal(line, &v); jsonErr != nil {
				fmt.Fprintf(os.Stderr, "dchat: 忽略助手非 JSON 行: %q\n", strings.TrimSpace(string(line)))
				continue
			}
			done <- res{v: v}
			return
		}
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
