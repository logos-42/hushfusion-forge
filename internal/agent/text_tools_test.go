package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/logos-42/hushfusion-forge/internal/ai"
)

func TestParseTextToolCalls(t *testing.T) {
	src := `先读一下。
<invoke name="mem.get">
<parameter name="key">session_progress</parameter>
</invoke>
<invoke name="mem.get">
<parameter name="key">session_progress</parameter>
</invoke>
<invoke name="unknown.tool">
<parameter name="x">y</parameter>
</invoke>`
	clean, calls := parseTextToolCalls(src, map[string]bool{"mem.get": true})
	if len(calls) != 1 {
		t.Fatalf("应只保留一个去重后的已知调用, got %+v", calls)
	}
	if calls[0].Function.Name != "mem.get" || calls[0].Function.Arguments != `{"key":"session_progress"}` {
		t.Fatalf("调用解析错误: %+v", calls[0])
	}
	if !strings.Contains(clean, "先读一下") || !strings.Contains(clean, "unknown.tool") {
		t.Fatalf("应保留正文与未知工具原文, got %q", clean)
	}
	if strings.Contains(clean, `name="mem.get"`) {
		t.Fatalf("已知调用块应被移除, got %q", clean)
	}
}

func TestTurnExecutesTextToolInvoke(t *testing.T) {
	round := 0
	chat := func(_ context.Context, req ai.ChatRequest) (*ai.ChatResponse, error) {
		round++
		if round == 1 {
			return &ai.ChatResponse{Content: `<invoke name="mem.set">
<parameter name="key">session_progress</parameter>
<parameter name="value">ok</parameter>
</invoke>`}, nil
		}
		seenTool := false
		for _, m := range req.Messages {
			if m.Role == "tool" && strings.Contains(m.Content, "已记忆") {
				seenTool = true
			}
		}
		if !seenTool {
			t.Fatalf("第二轮应看到 mem.set 的工具结果, messages=%+v", req.Messages)
		}
		return &ai.ChatResponse{Content: "done"}, nil
	}
	s := NewSession(DefaultConfig(), chat, NewRegistry(), "sys")
	if err := s.RegisterBuiltinTools(nil); err != nil {
		t.Fatal(err)
	}
	s.AllowWrites["mem.set"] = true
	out, err := s.Turn(context.Background(), "记住")
	if err != nil {
		t.Fatal(err)
	}
	if out != "done" || round != 2 {
		t.Fatalf("out=%q round=%d", out, round)
	}
	if got, _ := s.Memory.Get("session_progress"); got != "ok" {
		t.Fatalf("mem.set 未执行, got=%q", got)
	}
}

func TestTextToolStreamFilter(t *testing.T) {
	var out strings.Builder
	f := newTextToolStreamFilter(func(ev ai.StreamEvent) {
		out.WriteString(ev.Text)
	})
	for _, chunk := range []string{"你好", "<inv", `oke name="mem.get">`, "<parameter", ` name="key">k</parameter>`, "</invoke>", "结束"} {
		f.handle(ai.StreamEvent{Type: ai.StreamContent, Text: chunk})
	}
	f.flush()
	if got := out.String(); got != "你好结束" {
		t.Fatalf("流式过滤 = %q, want 你好结束", got)
	}
}
