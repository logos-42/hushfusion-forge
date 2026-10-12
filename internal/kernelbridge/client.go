// Package kernelbridge 把 Bolloon Native Macro-Kernel (@bolloon/bolloon-kernel)
// 的机制原语桥接给纯 Go 的 Forge。
//
// 形态: 子进程 stdio JSON-RPC (ndjson)。Go 侧起 bridge/server.mjs (Node),
// 通过 stdin 发请求、从 stdout 读响应。这是跨语言接入 kernel 的唯一边界 ——
// Forge 的 Go 代码不 import 任何 TS, kernel 的机制经 JSON-RPC 暴露。
//
// 设计纪律 (kernel SDK index.ts 的出口纪律在这里延续):
//
//	· 只桥接「机制」, 不桥接业务判定。
//	· 端口 (I/O) 语义由调用方给; 未注入 ⇒ fail-closed (ok:false), 不假装成功。
package kernelbridge

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// processInterrupt 返回关闭 stdin 前先发的软信号 (让 server 自己走完 in-flight)。
func processInterrupt() syscall.Signal { return syscall.SIGTERM }

// waitTimeout 是 Close 里等待子进程退出的上限。
func waitTimeout() <-chan time.Time { return time.After(2 * time.Second) }

// Client 是一个 bridge 子进程的会话。Create 之后必须 Close。
type Client struct {
	cmd    *exec.Cmd
	stdin  *bufio.Writer
	mu     sync.Mutex // 写锁 (stdin 串行) 与 nextID
	nextID int

	readsStarted chan struct{} // 后台读循环已启动
	pending      sync.Map      // id(int) -> chan json.RawMessage
	readErr      error         // 后台读循环终止原因 (测试可查)
	closed       chan struct{}
}

// Create 启动 bridge server 子进程 (需要 node + bridge/server.mjs)。
func Create() (*Client, error) {
	return create("node", "bridge/server.mjs")
}

// CreateWithServer 允许指定 server 路径 (测试用; 默认 bridge/server.mjs)。
func create(nodeBin, serverPath string) (*Client, error) {
	cmd := exec.Command(nodeBin, serverPath)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start bridge: %w", err)
	}
	c := &Client{
		cmd:          cmd,
		stdin:        bufio.NewWriter(stdin),
		readsStarted: make(chan struct{}),
		closed:       make(chan struct{}),
	}
	go c.readLoop(bufio.NewScanner(stdout))
	<-c.readsStarted // 等读循环就位, 否则第一个响应可能先于 waiter 到达被丢
	return c, nil
}

// readLoop 独占 stdout 扫描: 按 id 把响应交给等待的调用方。
func (c *Client) readLoop(sc *bufio.Scanner) {
	close(c.readsStarted) // 启动即通知 create() —— defer 会在退出时才触发, create() 会永久阻塞
	for sc.Scan() {
		var resp struct {
			ID     any             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(sc.Bytes(), &resp); err != nil {
			c.failPending(fmt.Errorf("unmarshal resp: %w", err))
			return
		}
		id := int(toFloat(resp.ID))
		if ch, ok := c.pending.Load(id); ok {
			if resp.Error != nil {
				ch.(chan json.RawMessage) <- mustMarshal(map[string]any{
					"__bridge_error": fmt.Sprintf("%d: %s", resp.Error.Code, resp.Error.Message),
				})
			} else {
				ch.(chan json.RawMessage) <- resp.Result
			}
			c.pending.Delete(id)
		}
		// 未知 id (或 id=null 的协议错误): 丢弃, 不破坏读循环
	}
	if err := sc.Err(); err != nil {
		c.failPending(fmt.Errorf("read resp: %w", err))
		return
	}
	c.failPending(fmt.Errorf("bridge closed (stdin EOF)"))
}

// failPending 把错误广播给所有还在等的调用方 (进程死了以后没人能等到响应)。
func (c *Client) failPending(err error) {
	select {
	case <-c.closed:
		return // Close 已主动触发, 调用方会从自己的超时收尾
	default:
	}
	c.mu.Lock()
	c.readErr = err
	c.mu.Unlock()
	c.pending.Range(func(k, v any) bool {
		v.(chan json.RawMessage) <- mustMarshal(map[string]any{"__bridge_error": err.Error()})
		c.pending.Delete(k)
		return true
	})
}

func mustMarshal(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// Close 关闭子进程。未 Close 会泄漏进程。
func (c *Client) Close() error {
	if c.cmd.Process == nil {
		return nil
	}
	select {
	case <-c.closed:
		return nil
	default:
		close(c.closed)
	}
	// 关掉 stdin 让 server 自行退出; 超时再 kill。
	_ = c.cmd.Process.Signal(processInterrupt())
	done := make(chan error, 1)
	go func() { done <- c.cmd.Wait() }()
	select {
	case <-done:
		return nil
	case <-waitTimeout():
		_ = c.cmd.Process.Kill()
		<-done
		return nil
	}
}

// rpc 发一个请求并等响应。参数 id 自增由写锁保护; 读由后台循环按 id 分发 ——
// 同一管道上的并发请求安全 (跨 channel 隔离才能被测)。
func (c *Client) rpc(method string, params map[string]any) (json.RawMessage, error) {
	ch := make(chan json.RawMessage, 1)

	c.mu.Lock()
	c.nextID++
	id := c.nextID
	req := map[string]any{"id": id, "method": method, "params": params}
	payload, err := json.Marshal(req)
	if err != nil {
		c.mu.Unlock()
		return nil, fmt.Errorf("marshal req: %w", err)
	}
	payload = append(payload, '\n')
	// Store 必须在写请求**之前** —— 否则响应先到、readLoop 查 pending 为空 ⇒ 响应被丢 ⇒ rpc 永远等。
	c.pending.Store(id, ch)
	if _, err := c.stdin.Write(payload); err != nil {
		c.pending.Delete(id)
		c.mu.Unlock()
		return nil, fmt.Errorf("write req: %w", err)
	}
	if err := c.stdin.Flush(); err != nil {
		c.pending.Delete(id)
		c.mu.Unlock()
		return nil, fmt.Errorf("flush req: %w", err)
	}
	c.mu.Unlock()

	select {
	case raw := <-ch:
		if s := string(raw); len(s) > 0 && s[0] == '{' {
			var errBox struct {
				BridgeError string `json:"__bridge_error"`
			}
			if json.Unmarshal(raw, &errBox) == nil && errBox.BridgeError != "" {
				return nil, fmt.Errorf("bridge error: %s", errBox.BridgeError)
			}
		}
		return raw, nil
	case <-time.After(30 * time.Second):
		c.pending.Delete(id)
		return nil, fmt.Errorf("bridge timeout (id=%d)", id)
	}
}

func toFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case json.Number:
		f, _ := n.Float64()
		return f
	case int:
		return float64(n)
	}
	return -1
}
