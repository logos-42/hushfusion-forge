package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/logos-42/hushfusion-forge/internal/ai"
)

// streamingEcho 是带增量的假流式模型：两段 content，再给最终响应。
func streamingEcho() ChatStreamFunc {
	return func(_ context.Context, _ ai.ChatRequest, on func(ai.StreamEvent)) (*ai.ChatResponse, error) {
		if on != nil {
			on(ai.StreamEvent{Type: ai.StreamContent, Text: "Hello "})
			on(ai.StreamEvent{Type: ai.StreamContent, Text: "world"})
		}
		return &ai.ChatResponse{Content: "Hello world"}, nil
	}
}

func TestTurnStreamEmitsDeltas(t *testing.T) {
	s := newTestSession(alwaysFinal("ignored"), NewRegistry())
	s.SetStreamChat(streamingEcho())
	var got []string
	out, err := s.TurnStream(context.Background(), "hi", func(ev ai.StreamEvent) {
		if ev.Type == ai.StreamContent {
			got = append(got, ev.Text)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, "") != "Hello world" {
		t.Fatalf("增量应拼成 Hello world, got %q", strings.Join(got, ""))
	}
	if out != "Hello world" {
		t.Fatalf("最终内容应 Hello world, got %q", out)
	}
}

// 没注入流式端口时 TurnStream 退化为一次性：仍发一条最终 StreamContent。
func TestTurnStreamFallsBackToBatched(t *testing.T) {
	s := newTestSession(alwaysFinal("final text"), NewRegistry())
	var texts []string
	out, err := s.TurnStream(context.Background(), "hi", func(ev ai.StreamEvent) {
		if ev.Type == ai.StreamContent {
			texts = append(texts, ev.Text)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(texts) != 1 || texts[0] != "final text" {
		t.Fatalf("退化应只剩一条完整增量, got %v", texts)
	}
	if out != "final text" {
		t.Fatalf("内容应 final text, got %q", out)
	}
}

// 根会话含 delegate；委派返回子智能体最终文本，并带用量行。
func TestDelegateToolRunsSubSession(t *testing.T) {
	calls := 0
	// 子会话复用父的 chat（delegateExecute 用 s.chat），用消息内容区分父子：
	// 首调（父，仅 user 无历史）→ 返回 delegate 调用；子会话的 user=="sub" → 返回子结果；其余 → final。
	parentChat := func(_ context.Context, req ai.ChatRequest) (*ai.ChatResponse, error) {
		calls++
		if calls == 1 {
			tc := ai.ToolCall{ID: "d1", Type: "function"}
			tc.Function.Name = "delegate"
			tc.Function.Arguments = `{"task":"sub"}`
			return &ai.ChatResponse{ToolCalls: []ai.ToolCall{tc}}, nil
		}
		if len(req.Messages) > 0 && req.Messages[len(req.Messages)-1].Content == "sub" {
			return &ai.ChatResponse{Content: "sub reply"}, nil
		}
		return &ai.ChatResponse{Content: "final"}, nil
	}
	s := newTestSession(parentChat, NewRegistry())
	if err := s.RegisterBuiltinTools(nil); err != nil {
		t.Fatal(err)
	}
	out, err := s.Turn(context.Background(), "hi")
	if err != nil {
		t.Fatal(err)
	}
	if out != "final" {
		t.Fatalf("父最终应 final, got %q", out)
	}
	// 委派输出应进了消息栈（tool 结果）。
	var joined string
	for _, m := range s.History() {
		joined += m.Content
	}
	if !strings.Contains(joined, "sub reply") {
		t.Fatalf("委派子智能体输出应出现: %q", joined)
	}
	if !strings.Contains(joined, "[delegate") {
		t.Fatalf("委派应带回用量行: %q", joined)
	}
}

// 子会话（nesting>0）**不**注册 delegate：从结构上禁止无限委派。
func TestDelegateNotRegisteredForNested(t *testing.T) {
	child := newTestSession(alwaysFinal("x"), NewRegistry())
	child.SetNesting(1)
	if err := child.RegisterBuiltinTools(nil); err != nil {
		t.Fatal(err)
	}
	for _, n := range child.registry.Names() {
		if n == "delegate" {
			t.Fatal("嵌套会话不应含 delegate 工具")
		}
	}
}

// 防御性：即便手动给嵌套会话塞了 delegate 工具，执行时也拒绝（防自递归）。
func TestDelegateSelfRecursionBlocked(t *testing.T) {
	child := newTestSession(alwaysFinal("x"), NewRegistry())
	child.SetNesting(1)
	child.registerDelegateTool() // 强行注册以测执行期护栏
	tc := ai.ToolCall{}
	tc.Function.Name = "delegate"
	tc.Function.Arguments = `{"task":"x"}`
	res, err := child.registry.Execute(context.Background(), tc, map[string]bool{})
	if err == nil {
		t.Fatalf("嵌套委派应被拒，却返回: %q", res)
	}
	if !strings.Contains(err.Error(), "嵌套") {
		t.Fatalf("应报嵌套上限, got %v", err)
	}
}
