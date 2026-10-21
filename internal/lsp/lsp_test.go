package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

// fakeServer 起一个内存版 LSP server（两个 io.Pipe 对连），handler 处理请求方法。
// 它演示「先 arm 再等就绪」：必须正确回应 initialize 才能继续；其余方法按 handler。
func fakeServer(t *testing.T, h func(method string, params json.RawMessage) (json.RawMessage, error)) (*Server, func()) {
	t.Helper()
	ar, aw := io.Pipe()
	br, bw := io.Pipe()
	s := newServer(ServerConfig{ID: "go", Allow: []string{"*"}}, &pipe{r: ar, w: bw})
	go s.loop() // 启动客户端读循环（真实 Start 也会启动；newServer 不自动启）
	go func() {
		brd := bufio.NewReader(br)
		for {
			raw, err := readMessage(brd)
			if err != nil {
				return
			}
			var m struct {
				ID     *int64          `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if json.Unmarshal(raw, &m) != nil {
				continue
			}
			if m.ID == nil {
				continue // 通知（initialized/shutdown/exit）无需回应
			}
			var result json.RawMessage
			switch m.Method {
			case "initialize":
				result = json.RawMessage(`{"capabilities":{"definitionProvider":true,"hoverProvider":true,"referencesProvider":true}}`)
			case "shutdown":
				result = json.RawMessage(`null`)
			default:
				r, herr := h(m.Method, m.Params)
				if herr != nil {
					resp, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": *m.ID, "error": map[string]any{"message": herr.Error()}})
					writeMessage(aw, resp)
					continue
				}
				result = r
			}
			resp, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": *m.ID, "result": result})
			writeMessage(aw, resp)
		}
	}()
	return s, func() { aw.Close(); bw.Close() }
}

func TestLSPInitAndDefinition(t *testing.T) {
	h := func(method string, params json.RawMessage) (json.RawMessage, error) {
		if method == "textDocument/definition" {
			return json.RawMessage(`[{"uri":"file:///x/y.go","range":{"start":{"line":0,"character":0},"end":{"line":0,"character":2}}}]`), nil
		}
		return nil, nil
	}
	s, cleanup := fakeServer(t, h)
	defer cleanup()
	ctx := context.Background()
	// init 门：必须先 WaitForInit 成功
	if err := s.WaitForInit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Open(ctx, "file:///x/y.go", "package x\n"); err != nil {
		t.Fatal(err)
	}
	locs, err := s.Definition(ctx, "file:///x/y.go", 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(locs) != 1 || locs[0].URI != "file:///x/y.go" {
		t.Fatalf("definition 结果错误: %+v", locs)
	}
}

func TestLSPHover(t *testing.T) {
	h := func(method string, params json.RawMessage) (json.RawMessage, error) {
		if method == "textDocument/hover" {
			return json.RawMessage(`{"contents":{"value":"// doc comment"}}`), nil
		}
		return nil, nil
	}
	s, cleanup := fakeServer(t, h)
	defer cleanup()
	ctx := context.Background()
	s.WaitForInit(ctx)
	s.Open(ctx, "file:///x/y.go", "package x\n")
	out, err := s.Hover(ctx, "file:///x/y.go", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if out != "// doc comment" {
		t.Fatalf("hover = %q", out)
	}
}

func TestLSPReferences(t *testing.T) {
	h := func(method string, params json.RawMessage) (json.RawMessage, error) {
		if method == "textDocument/references" {
			return json.RawMessage(`[{"uri":"file:///x/y.go","range":{"start":{"line":3,"character":4},"end":{"line":3,"character":5}}}]`), nil
		}
		return nil, nil
	}
	s, cleanup := fakeServer(t, h)
	defer cleanup()
	ctx := context.Background()
	s.WaitForInit(ctx)
	s.Open(ctx, "file:///x/y.go", "package x\n")
	locs, err := s.References(ctx, "file:///x/y.go", 1, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(locs) != 1 {
		t.Fatalf("references 应返回 1 个，got %d", len(locs))
	}
}

// 权限门：Allow 为空 → 全部 op 拒绝（fail-closed）。
func TestLSPPermissionFailClosed(t *testing.T) {
	h := func(method string, params json.RawMessage) (json.RawMessage, error) {
		return nil, nil
	}
	s, cleanup := fakeServer(t, h)
	defer cleanup()
	s.cfg.Allow = nil // 清空白名单
	ctx := context.Background()
	_, err := s.Definition(ctx, "file:///x/y.go", 0, 0)
	if err == nil || !strings.Contains(err.Error(), "fail-closed") {
		t.Fatalf("空 Allow 应 fail-closed 拒绝，got %v", err)
	}
}

// Manager 把每个 server 的 op 注册成 agent 工具（callback 注册），路由正确。
func TestLSPManagerAgentTools(t *testing.T) {
	h := func(method string, params json.RawMessage) (json.RawMessage, error) {
		switch method {
		case "textDocument/definition":
			return json.RawMessage(`[{"uri":"file:///a.go","range":{"start":{"line":1,"character":1},"end":{"line":1,"character":2}}}]`), nil
		case "textDocument/hover":
			return json.RawMessage(`{"contents":"hover text"}`), nil
		case "textDocument/references":
			return json.RawMessage(`[]`), nil
		}
		return nil, nil
	}
	s, cleanup := fakeServer(t, h)
	defer cleanup()
	mgr := NewManager()
	mgr.Attach(s)
	tools := mgr.AgentTools()
	if len(tools) != 3 {
		t.Fatalf("每 server 应 3 个 tool，got %d", len(tools))
	}
	var defTool Tool
	for _, tl := range tools {
		if tl.Name == "lsp:go:definition" {
			defTool = tl
		}
	}
	if defTool.Call == nil {
		t.Fatal("应有 lsp:go:definition 工具")
	}
	ctx := context.Background()
	s.WaitForInit(ctx)
	s.Open(ctx, "file:///x/y.go", "package x\n")
	out, err := defTool.Call(ctx, map[string]any{"uri": "file:///x/y.go", "line": 0, "character": 0})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "a.go") {
		t.Fatalf("definition 工具输出应含 a.go，got %q", out)
	}
}

// Caps 应在 initialize 后暴露 server 能力。
func TestLSPCaps(t *testing.T) {
	h := func(method string, params json.RawMessage) (json.RawMessage, error) {
		return nil, nil
	}
	s, cleanup := fakeServer(t, h)
	defer cleanup()
	ctx := context.Background()
	caps, err := s.Caps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if caps == nil {
		t.Fatal("caps 应非 nil")
	}
}
