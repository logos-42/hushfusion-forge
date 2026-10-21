package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"testing"
)

// fakeServer 是内存管道上的最小 MCP server，仅实现 initialize/tools/list/tools/call。
func fakeServer(t *testing.T, serverR io.Reader, serverW io.Writer) {
	t.Helper()
	br := bufio.NewReader(serverR)
	for {
		raw, err := readMessage(br)
		if err != nil {
			return
		}
		var m struct {
			ID     *int64 `json:"id"`
			Method string `json:"method"`
		}
		if json.Unmarshal(raw, &m) != nil {
			return
		}
		switch m.Method {
		case "initialize":
			resp, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0", "id": *m.ID,
				"result": map[string]any{
					"protocolVersion": "2024-11-05",
					"capabilities":    map[string]any{"tools": map[string]any{}},
					"serverInfo":      map[string]any{"name": "fake"},
				},
			})
			writeMessage(serverW, resp)
		case "notifications/initialized":
			// 通知无需回应
		case "tools/list":
			resp, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0", "id": *m.ID,
				"result": map[string]any{"tools": []map[string]any{
					{"name": "echo", "description": "回声", "inputSchema": map[string]any{"type": "object"}},
					{"name": "rm", "description": "危险", "inputSchema": map[string]any{"type": "object"}},
				}},
			})
			writeMessage(serverW, resp)
		case "tools/call":
			var p struct {
				Params struct {
					Name      string         `json:"name"`
					Arguments map[string]any `json:"arguments"`
				} `json:"params"`
			}
			json.Unmarshal(raw, &p)
			text := fmt.Sprintf("%v", p.Params.Arguments["text"])
			resp, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0", "id": *m.ID,
				"result": map[string]any{"content": []map[string]any{{"type": "text", "text": text}}},
			})
			writeMessage(serverW, resp)
		}
	}
}

func newPipePair() (clientRW, serverRW io.ReadWriteCloser) {
	cr, sw := io.Pipe() // server -> client
	sr, cw := io.Pipe() // client -> server
	clientRW = &pipe{r: cr, w: cw}
	serverRW = &pipe{r: sr, w: sw}
	return
}

// TestMCPInitAndPermission：握手拿工具 + 白名单内工具可调用 + 白名单外 fail-closed。
func TestMCPInitAndPermission(t *testing.T) {
	clientRW, serverRW := newPipePair()
	client := NewClient(ServerConfig{Name: "fake", Allow: []string{"echo"}}, clientRW)
	go fakeServer(t, serverRW, serverRW)

	ctx := context.Background()
	if err := client.WaitForInit(ctx); err != nil {
		t.Fatalf("WaitForInit: %v", err)
	}
	tools, err := client.Tools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 2 {
		t.Fatalf("应拿到 2 个工具，got %d: %+v", len(tools), tools)
	}

	// 白名单内：可调用
	out, err := client.CallTool(ctx, "echo", map[string]any{"text": "hi"})
	if err != nil || out != "hi" {
		t.Fatalf("echo 应返回 hi，got %q err=%v", out, err)
	}

	// 白名单外：权限门 fail-closed 拒绝
	if _, err := client.CallTool(ctx, "rm", map[string]any{}); err == nil {
		t.Fatal("rm 不在 Allow 中，应被权限门拒绝")
	}
}

// TestMCPFailClosedEmptyAllow：Allow 为空 → 全部拒绝（fail-closed）。
func TestMCPFailClosedEmptyAllow(t *testing.T) {
	clientRW, serverRW := newPipePair()
	client := NewClient(ServerConfig{Name: "fake"}, clientRW) // Allow 为空
	go fakeServer(t, serverRW, serverRW)

	ctx := context.Background()
	if err := client.WaitForInit(ctx); err != nil {
		t.Fatalf("WaitForInit: %v", err)
	}
	if _, err := client.CallTool(ctx, "echo", nil); err == nil {
		t.Fatal("空 Allow 应 fail-closed 拒绝一切调用")
	}
}
