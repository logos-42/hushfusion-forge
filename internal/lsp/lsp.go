// Package lsp 实现 LSP（Language Server Protocol）server 的 stdio 客户端 +
// manager，作为「LSP 插件模型」的范本：
//
//   - Manager 拉起多个 LSP server（gopls / pylsp / tsserver …），各自过
//     "先 arm 再等就绪" 的 init 门（initialize → initialized → 才能发请求），
//     模式来自 internal/mcp 的 initOnce/WaitForInit。
//   - 每个 server 的 LSP 能力（definition / hover / references）封装成 agent
//     工具，由 Manager.AgentTools() 以「callback 注册」方式暴露——agent 调
//     tool 名 → 路由到对应 server 的 op，权限门（fail-closed）在每次调用前先过。
//
// 传输用 LSP 规定的 Content-Length 分帧 JSON-RPC 2.0（与 MCP 同构；这里独立实现
// 以避免与 mcp 包耦合——若日后出现第三个 JSON-RPC 客户端，再抽到 internal/jsonrpc）。
// 本模块不依赖任意具体 LSP server，可用内存管道测试；真实接入把 rw 换成子进程
// 的 stdin/stdout 即可（见 Start）。
package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// Op 是 LSP 暴露给 agent 的工具操作名（权限门按它判定）。
const (
	OpDefinition = "definition"
	OpHover      = "hover"
	OpReferences = "references"
)

// Location 是 LSP 的一个位置（文档 + 区间）。
type Location struct {
	URI   string `json:"uri"`
	Range Range  `json:"range"`
}

// Range 是一个源码区间。
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// Position 是零基行列位置。
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// ServerConfig 描述要接入的 LSP server（stdio 拉起）。
type ServerConfig struct {
	ID      string   `json:"id"` // 逻辑 id，如 "go" / "python"
	Command string   `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
	RootURI string   `json:"root_uri,omitempty"`
	// Allow 是允许被 agent 调用的 op 白名单（definition/hover/references）。
	// 空 = 全部拒绝（fail-closed）；含 "*" = 全部允许。
	Allow []string `json:"allow,omitempty"`
}

// Server 是一个 LSP server 的 stdio 客户端（"先 arm 再等就绪"的 init 门）。
type Server struct {
	cfg ServerConfig
	rw  io.ReadWriteCloser

	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan json.RawMessage
	openVer map[string]int // uri -> version（去重 didOpen）

	once    sync.Once
	ready   chan struct{}
	initErr error
	caps    map[string]any
}

func newServer(cfg ServerConfig, rw io.ReadWriteCloser) *Server {
	return &Server{
		cfg:     cfg,
		rw:      rw,
		pending: map[int64]chan json.RawMessage{},
		ready:   make(chan struct{}),
		openVer: map[string]int{},
	}
}

// pipe 把"读一端 + 写一端"拼成单条 ReadWriteCloser（子进程 stdin/stdout 即如此）。
type pipe struct {
	r io.Reader
	w io.WriteCloser
}

func (p *pipe) Read(b []byte) (int, error)  { return p.r.Read(b) }
func (p *pipe) Write(b []byte) (int, error) { return p.w.Write(b) }
func (p *pipe) Close() error                { return p.w.Close() }

// Start 拉起真实 LSP server（exec.Command），返回客户端并启动后台读循环。
func Start(ctx context.Context, cfg ServerConfig) (*Server, error) {
	if cfg.Command == "" {
		return nil, fmt.Errorf("lsp: ServerConfig.Command 为空")
	}
	if cfg.RootURI == "" {
		if wd, err := os.Getwd(); err == nil {
			cfg.RootURI = "file://" + wd
		}
	}
	cmd := exec.CommandContext(ctx, cfg.Command, cfg.Args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	s := newServer(cfg, &pipe{r: stdout, w: stdin})
	go s.loop()
	return s, nil
}

// WaitForInit 执行（且只执行一次）握手：initialize → initialized 通知。
// 等价于 mcp 的 initOnce + WaitForInit 门；调用方必须等它返回才能发请求。
func (s *Server) WaitForInit(ctx context.Context) error {
	s.once.Do(func() {
		defer close(s.ready)
		res, err := s.request(ctx, "initialize", map[string]any{
			"processId": 0,
			"rootUri":   s.cfg.RootURI,
			"capabilities": map[string]any{
				"textDocument": map[string]any{
					"definition": map[string]any{},
					"hover":      map[string]any{},
					"references": map[string]any{},
				},
			},
			"clientInfo": map[string]any{"name": "hushfusion", "version": "0.1"},
		})
		if err != nil {
			s.initErr = err
			return
		}
		var ir struct {
			Capabilities map[string]any `json:"capabilities"`
		}
		if err := json.Unmarshal(res, &ir); err != nil {
			s.initErr = err
			return
		}
		s.caps = ir.Capabilities
		note, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "initialized"})
		if err := writeMessage(s.rw, note); err != nil {
			s.initErr = err
			return
		}
	})
	select {
	case <-s.ready:
	case <-ctx.Done():
		return ctx.Err()
	}
	return s.initErr
}

// Open 把一篇文档以 didOpen 推给 server（幂等：同 uri 只开一次）。
func (s *Server) Open(ctx context.Context, uri, text string) error {
	if err := s.WaitForInit(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	_, ok := s.openVer[uri]
	s.mu.Unlock()
	if ok {
		return nil
	}
	ver := 1
	if err := s.notify(ctx, "textDocument/didOpen", map[string]any{
		"textDocument": map[string]any{
			"uri":        uri,
			"languageId": langOf(uri),
			"version":    ver,
			"text":       text,
		},
	}); err != nil {
		return err
	}
	s.mu.Lock()
	s.openVer[uri] = ver
	s.mu.Unlock()
	return nil
}

// Definition 跳转到定义（权限门先过，再 openFromDisk，再发 textDocument/definition）。
func (s *Server) Definition(ctx context.Context, uri string, line, col int) ([]Location, error) {
	if !s.allowed(OpDefinition) {
		return nil, fmt.Errorf("lsp: op %q 未在 Allow 白名单（fail-closed）", OpDefinition)
	}
	if err := s.openFromDisk(ctx, uri); err != nil {
		return nil, err
	}
	res, err := s.request(ctx, "textDocument/definition", textDocPos(uri, line, col))
	if err != nil {
		return nil, err
	}
	return parseLocations(res)
}

// Hover 取符号悬停信息。
func (s *Server) Hover(ctx context.Context, uri string, line, col int) (string, error) {
	if !s.allowed(OpHover) {
		return "", fmt.Errorf("lsp: op %q 未在 Allow 白名单（fail-closed）", OpHover)
	}
	if err := s.openFromDisk(ctx, uri); err != nil {
		return "", err
	}
	res, err := s.request(ctx, "textDocument/hover", textDocPos(uri, line, col))
	if err != nil {
		return "", err
	}
	return parseHover(res)
}

// References 找符号引用（includeDeclaration 控制是否含声明本身）。
func (s *Server) References(ctx context.Context, uri string, line, col int, includeDecl bool) ([]Location, error) {
	if !s.allowed(OpReferences) {
		return nil, fmt.Errorf("lsp: op %q 未在 Allow 白名单（fail-closed）", OpReferences)
	}
	if err := s.openFromDisk(ctx, uri); err != nil {
		return nil, err
	}
	res, err := s.request(ctx, "textDocument/references", map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"position":     map[string]any{"line": line, "character": col},
		"context":      map[string]any{"includeDeclaration": includeDecl},
	})
	if err != nil {
		return nil, err
	}
	return parseLocations(res)
}

// Caps 返回 server 在 initialize 时汇报的能力（调用前应 WaitForInit）。
func (s *Server) Caps(ctx context.Context) (map[string]any, error) {
	if err := s.WaitForInit(ctx); err != nil {
		return nil, err
	}
	return s.caps, nil
}

// Close 优雅关闭：shutdown + exit（best effort）+ 关管道。
func (s *Server) Close() error {
	note, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "shutdown"})
	_ = writeMessage(s.rw, note)
	exit, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "exit"})
	_ = writeMessage(s.rw, exit)
	return s.rw.Close()
}

// openFromDisk 若文档未打开则从磁盘读取并 didOpen（uri 形如 file:///abs/path）。
func (s *Server) openFromDisk(ctx context.Context, uri string) error {
	s.mu.Lock()
	_, ok := s.openVer[uri]
	s.mu.Unlock()
	if ok {
		return nil
	}
	path := strings.TrimPrefix(uri, "file://")
	text, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("lsp: 读文档 %q 失败（请先 Open 或提供 file:// 路径）: %w", uri, err)
	}
	return s.Open(ctx, uri, string(text))
}

// allowed 实现权限门：空 Allow = 全部拒绝；"*" = 全部允许；否则按名单。
func (s *Server) allowed(op string) bool {
	if len(s.cfg.Allow) == 0 {
		return false
	}
	for _, a := range s.cfg.Allow {
		if a == "*" || a == op {
			return true
		}
	}
	return false
}

// ---- Manager：拉起多 server + 注册 agent 工具（callback 注册工具） ----

// Manager 管理多个 LSP server，并产出供 agent 使用的工具列表。
type Manager struct {
	mu      sync.Mutex
	servers map[string]*Server
}

// NewManager 创建空 manager。
func NewManager() *Manager {
	return &Manager{servers: map[string]*Server{}}
}

// Attach 接入一个已拉起的 server（测试或外部拉起时用）。
func (m *Manager) Attach(s *Server) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.servers[s.cfg.ID] = s
}

// Server 按 id 取已接入的 server（nil 表示未接入）。
func (m *Manager) Server(id string) *Server {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.servers[id]
}

// Tool 是注册给 agent 的一个可调用工具（callback 封装）。
type Tool struct {
	Name        string
	Description string
	Call        func(ctx context.Context, args map[string]any) (string, error)
}

// AgentTools 把每个 server 的 op 注册成 agent 工具（路由到对应 server）。
// 回调（Call）即"callback 注册工具"：agent 调 tool 名 → 路由到 server 的 op；
// 每次调用仍先过 server 的权限门（fail-closed）。
func (m *Manager) AgentTools() []Tool {
	m.mu.Lock()
	defer m.mu.Unlock()
	var tools []Tool
	for _, s := range m.servers {
		id := s.cfg.ID
		tools = append(tools,
			Tool{
				Name:        fmt.Sprintf("lsp:%s:definition", id),
				Description: fmt.Sprintf("在 %s 项目中跳转到符号定义（参数 uri/line/character）", id),
				Call: func(ctx context.Context, args map[string]any) (string, error) {
					uri, line, col := parsePosArgs(args)
					locs, err := s.Definition(ctx, uri, line, col)
					if err != nil {
						return "", err
					}
					return formatLocations(locs), nil
				},
			},
			Tool{
				Name:        fmt.Sprintf("lsp:%s:hover", id),
				Description: fmt.Sprintf("在 %s 项目中取符号悬停信息（参数 uri/line/character）", id),
				Call: func(ctx context.Context, args map[string]any) (string, error) {
					uri, line, col := parsePosArgs(args)
					return s.Hover(ctx, uri, line, col)
				},
			},
			Tool{
				Name:        fmt.Sprintf("lsp:%s:references", id),
				Description: fmt.Sprintf("在 %s 项目中找符号引用（参数 uri/line/character）", id),
				Call: func(ctx context.Context, args map[string]any) (string, error) {
					uri, line, col := parsePosArgs(args)
					locs, err := s.References(ctx, uri, line, col, true)
					if err != nil {
						return "", err
					}
					return formatLocations(locs), nil
				},
			},
		)
	}
	return tools
}

// ---- 内部：JSON-RPC 收发（与 mcp 同构，独立实现） ----

func (s *Server) loop() {
	br := bufio.NewReader(s.rw)
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
		s.mu.Lock()
		ch, ok := s.pending[*hdr.ID]
		s.mu.Unlock()
		if ok {
			ch <- raw
		}
	}
}

func (s *Server) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := atomic.AddInt64(&s.nextID, 1)
	msg := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		msg["params"] = params
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	ch := make(chan json.RawMessage, 1)
	s.mu.Lock()
	s.pending[id] = ch
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
	}()
	if err := writeMessage(s.rw, raw); err != nil {
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
			return nil, fmt.Errorf("lsp: 解析响应失败: %w", err)
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("lsp %s: %s", method, resp.Error.Message)
		}
		return resp.Result, nil
	}
}

func (s *Server) notify(ctx context.Context, method string, params any) error {
	msg := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		msg["params"] = params
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return writeMessage(s.rw, raw)
}

// readMessage 按 LSP 的 Content-Length 分帧读取一条 JSON-RPC 消息。
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
		return nil, fmt.Errorf("lsp: 缺 Content-Length 头")
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

// ---- 内部：参数/结果辅助 ----

func textDocPos(uri string, line, col int) map[string]any {
	return map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"position":     map[string]any{"line": line, "character": col},
	}
}

func parsePosArgs(args map[string]any) (string, int, int) {
	uri, _ := args["uri"].(string)
	line := toInt(args["line"])
	col := toInt(args["character"])
	if col == 0 {
		col = toInt(args["col"])
	}
	return uri, line, col
}

func toInt(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	case int64:
		return int(x)
	case string:
		n, _ := strconv.Atoi(x)
		return n
	}
	return 0
}

func langOf(uri string) string {
	switch strings.ToLower(filepath.Ext(uri)) {
	case ".go":
		return "go"
	case ".py":
		return "python"
	case ".ts", ".tsx":
		return "typescript"
	case ".js", ".jsx":
		return "javascript"
	default:
		return "plaintext"
	}
}

func parseLocations(raw json.RawMessage) ([]Location, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var single Location
	if err := json.Unmarshal(raw, &single); err == nil && single.URI != "" {
		return []Location{single}, nil
	}
	var many []Location
	if err := json.Unmarshal(raw, &many); err != nil {
		return nil, err
	}
	return many, nil
}

func parseHover(raw json.RawMessage) (string, error) {
	var h struct {
		Contents json.RawMessage `json:"contents"`
	}
	if err := json.Unmarshal(raw, &h); err != nil {
		return "", err
	}
	if len(h.Contents) == 0 {
		return "", nil
	}
	var asStr string
	if err := json.Unmarshal(h.Contents, &asStr); err == nil {
		return asStr, nil
	}
	var asObj struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(h.Contents, &asObj); err == nil {
		return asObj.Value, nil
	}
	return "", nil
}

func formatLocations(locs []Location) string {
	if len(locs) == 0 {
		return "（无结果）"
	}
	var b strings.Builder
	for _, l := range locs {
		fmt.Fprintf(&b, "%s:%d:%d\n", strings.TrimPrefix(l.URI, "file://"), l.Range.Start.Line+1, l.Range.Start.Character+1)
	}
	return strings.TrimRight(b.String(), "\n")
}
