// Package mcp 实现 MCP（Model Context Protocol）server 的 stdio 客户端，
// 并落实从 charmbracelet/crush 学来的两件事：
//
//  1. **init 门（initOnce / WaitForInit）**：client 就绪不是即时的——
//     要先 initialize → 发 notifications/initialized → tools/list 才能拿到工具。
//     用 sync.Once + ready channel 做"先 arm 再等就绪"的确定性初始化，避免竞态
//     （crush 的 internal/agent/tools/mcp 正是这个结构）。
//  2. **权限门（fail-closed）**：未显式列入 Allow 白名单的工具调用一律拒绝，
//     调用前先过权限检查（对应 crush 的 permission.Service）。
//
// 传输用 MCP 规定的 Content-Length 分帧 JSON-RPC 2.0。本模块不依赖外部 MCP SDK，
// 自包含、可用内存管道测试；真实接入时把 rw 换成子进程的 stdin/stdout 即可。
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// ServerConfig 描述一个要接入的 MCP server（stdio 方式拉起；rw 由调用方提供）。
type ServerConfig struct {
	Name    string   `json:"name"`
	Command string   `json:"command,omitempty"` // 仅作记录/未来 spawn 用
	Args    []string `json:"args,omitempty"`
	Env     []string `json:"env,omitempty"`
	// Allow 是该 server 上允许被调用的工具名白名单。空 = 全部拒绝（fail-closed）；
	// 含 "*" = 全部允许。权限门在每次 CallTool 前先过此检查。
	Allow []string `json:"allow,omitempty"`
}

// Tool 是 MCP server 暴露的一个工具（供模型选择）。
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

// Client 是一个 MCP server 的 stdio 客户端。
type Client struct {
	cfg ServerConfig
	rw  io.ReadWriteCloser

	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan json.RawMessage

	initOnce sync.Once
	ready    chan struct{} // 关闭表示 init 完成（成功或失败）
	initErr  error
	tools    []Tool
}

// pipe 把"读一端 + 写一端"拼成单条 ReadWriteCloser（子进程 stdin/stdout 即如此）。
type pipe struct {
	r io.Reader
	w io.WriteCloser
}

func (p *pipe) Read(b []byte) (int, error)  { return p.r.Read(b) }
func (p *pipe) Write(b []byte) (int, error) { return p.w.Write(b) }
func (p *pipe) Close() error                { return p.w.Close() }

// NewClient 创建客户端并启动后台读循环。
func NewClient(cfg ServerConfig, rw io.ReadWriteCloser) *Client {
	c := &Client{
		cfg:     cfg,
		rw:      rw,
		pending: map[int64]chan json.RawMessage{},
		ready:   make(chan struct{}),
	}
	go c.loop()
	return c
}

// WaitForInit 执行（且只执行一次）握手：initialize → initialized 通知 → tools/list。
// 这等价于 crush 的 initOnce + WaitForInit 门；调用方必须等它返回才能拿工具/调工具。
func (c *Client) WaitForInit(ctx context.Context) error {
	c.initOnce.Do(func() {
		defer close(c.ready)
		if _, err := c.request(ctx, "initialize", map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "hushfusion", "version": "0.1"},
		}); err != nil {
			c.initErr = err
			return
		}
		note, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
		if err := writeMessage(c.rw, note); err != nil {
			c.initErr = err
			return
		}
		res, err := c.request(ctx, "tools/list", map[string]any{})
		if err != nil {
			c.initErr = err
			return
		}
		var lr struct {
			Tools []Tool `json:"tools"`
		}
		if err := json.Unmarshal(res, &lr); err != nil {
			c.initErr = err
			return
		}
		c.tools = lr.Tools
	})
	select {
	case <-c.ready:
	case <-ctx.Done():
		return ctx.Err()
	}
	return c.initErr
}

// Tools 返回该 server 暴露的工具（调用前应 WaitForInit）。
func (c *Client) Tools(ctx context.Context) ([]Tool, error) {
	if err := c.WaitForInit(ctx); err != nil {
		return nil, err
	}
	return c.tools, nil
}

// CallTool 调用一个工具；调用前先过权限门（fail-closed）。
func (c *Client) CallTool(ctx context.Context, name string, args map[string]any) (string, error) {
	if !c.allowed(name) {
		return "", fmt.Errorf("mcp: 工具 %q 未在 Allow 白名单中（fail-closed 拒绝）", name)
	}
	if err := c.WaitForInit(ctx); err != nil {
		return "", err
	}
	res, err := c.request(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return "", err
	}
	var r struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(res, &r); err != nil {
		return "", err
	}
	var sb strings.Builder
	for _, c := range r.Content {
		sb.WriteString(c.Text)
	}
	return sb.String(), nil
}

// allowed 实现权限门：空 Allow = 全部拒绝；"*" = 全部允许；否则按名单。
func (c *Client) allowed(name string) bool {
	if len(c.cfg.Allow) == 0 {
		return false
	}
	for _, a := range c.cfg.Allow {
		if a == "*" || a == name {
			return true
		}
	}
	return false
}

// ---- 内部：JSON-RPC 收发 ----

func (c *Client) loop() {
	br := bufio.NewReader(c.rw)
	for {
		raw, err := readMessage(br)
		if err != nil {
			return
		}
		var hdr struct {
			ID     *int64 `json:"id"`
			Method string `json:"method"`
		}
		if json.Unmarshal(raw, &hdr) != nil {
			continue
		}
		if hdr.ID == nil {
			continue // 通知（如 initialized）无需回应
		}
		c.mu.Lock()
		ch, ok := c.pending[*hdr.ID]
		c.mu.Unlock()
		if ok {
			ch <- raw
		}
	}
}

func (c *Client) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := atomic.AddInt64(&c.nextID, 1)
	msg := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	ch := make(chan json.RawMessage, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()
	if err := writeMessage(c.rw, raw); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case raw := <-ch:
		var resp struct {
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			return nil, err
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("mcp %s: %s", method, resp.Error.Message)
		}
		return resp.Result, nil
	}
}

// readMessage 按 MCP 的 Content-Length 分帧读取一条 JSON-RPC 消息。
func readMessage(r *bufio.Reader) (json.RawMessage, error) {
	contentLength := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break // 头部结束
		}
		if strings.HasPrefix(strings.ToLower(line), "content-length:") {
			contentLength, _ = strconv.Atoi(strings.TrimSpace(line[len("content-length:"):]))
		}
	}
	if contentLength < 0 {
		return nil, fmt.Errorf("mcp: 缺 Content-Length 头")
	}
	buf := make([]byte, contentLength)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return json.RawMessage(buf), nil
}

func writeMessage(w io.Writer, msg json.RawMessage) error {
	payload := []byte(msg)
	if _, err := fmt.Fprintf(w, "Content-Length: %d\r\n\r\n", len(payload)); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}
