package ai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeSSE 起一个 SSE 服务：两段 content + 一段被拆成两次的 tool_call + 末尾 usage。
func fakeSSE(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), `"stream":true`) == false {
			t.Errorf("请求未带 stream:true: %s", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		chunks := []string{
			`data: {"choices":[{"delta":{"content":"Hello "}}]}` + "\n\n",
			`data: {"choices":[{"delta":{"content":"world"}}]}` + "\n\n",
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"search","arguments":"{\"q\":"}}]}}]}` + "\n\n",
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"x\"}"}}]}}]}` + "\n\n",
			`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":7,"completion_tokens":2,"total_tokens":9}}` + "\n\n",
			`data: [DONE]` + "\n\n",
		}
		for _, c := range chunks {
			_, _ = w.Write([]byte(c))
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestChatStreamParsesContentAndTools(t *testing.T) {
	srv := fakeSSE(t)
	c, err := NewClient(&Profile{Name: "t", BaseURL: srv.URL, DefaultModel: "m"}, ClientOptions{APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	var toolDeltaCount int
	resp, err := c.ChatStream(context.Background(), ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}},
		func(ev StreamEvent) {
			switch ev.Type {
			case StreamContent:
				texts = append(texts, ev.Text)
			case StreamToolCall:
				toolDeltaCount++
			}
		})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(texts, "") != "Hello world" {
		t.Fatalf("texts 应拼成 Hello world, got %q", strings.Join(texts, ""))
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "c1" || resp.ToolCalls[0].Function.Name != "search" {
		t.Fatalf("应装配出一个 tool_call c1/search, got %+v", resp.ToolCalls)
	}
	if resp.ToolCalls[0].Function.Arguments != `{"q":"x"}` {
		t.Fatalf("参数应累积为 {\"q\":\"x\"}, got %q", resp.ToolCalls[0].Function.Arguments)
	}
	if resp.Usage.TotalTokens != 9 {
		t.Fatalf("usage 应来自末片=9, got %d", resp.Usage.TotalTokens)
	}
	if toolDeltaCount != 2 {
		t.Fatalf("工具增量应分两次到达, got %d", toolDeltaCount)
	}
}

// 已下发事件后服务端断开 ⇒ 返回错误（不静默、不重复 delta）。
func TestChatStreamFailsAfterDelivering(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"))
		// 不断开连接但发损坏数据，迫使解析/读取失败
		_, _ = w.Write([]byte("data: not-json\n\n"))
	}))
	defer srv.Close()
	c, _ := NewClient(&Profile{Name: "t", BaseURL: srv.URL, DefaultModel: "m"}, ClientOptions{APIKey: "k"})
	delivered := false
	_, err := c.ChatStream(context.Background(), ChatRequest{Model: "m"},
		func(StreamEvent) { delivered = true })
	if err == nil {
		t.Fatal("损坏流应报错")
	}
	if !delivered {
		t.Fatal("应在报错前已下发过事件")
	}
}
