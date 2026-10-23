package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/logos-42/hushfusion-forge/internal/ai"
)

func TestBudgetForModel(t *testing.T) {
	cases := []struct {
		model string
		min   int
	}{
		{"claude-sonnet-4", 100_000},
		{"gemini-2.5-pro", 500_000},
		{"glm-5.3-flash", 60_000},
		{"gpt-4o", 60_000},
		{"unknown-model", 60_000},
	}
	for _, c := range cases {
		if got := BudgetForModel(c.model, 0.6); got < c.min {
			t.Fatalf("%s 预算 %d < %d", c.model, got, c.min)
		}
	}
	if ContextWindowForModel("gemini-2.5") != 1_000_000 {
		t.Fatal("gemini 上下文窗口应 1M")
	}
}

// compact 超预算时应调用模型生成摘要，而不是直接丢历史。
func TestCompactUsesSummary(t *testing.T) {
	chat := func(_ context.Context, req ai.ChatRequest) (*ai.ChatResponse, error) {
		if len(req.Messages) > 0 && strings.Contains(req.Messages[0].Content, "上下文压缩器") {
			return &ai.ChatResponse{Content: "SUMMARY：目标=聚变；决策=D9857；证据=score 0.8"}, nil
		}
		return &ai.ChatResponse{Content: "final"}, nil
	}
	cfg := DefaultConfig()
	cfg.MaxBudgetTokens = 50
	s := NewSession(cfg, chat, NewRegistry(), "sys")
	for i := 0; i < 6; i++ {
		s.messages = append(s.messages, Message{Role: RoleUser, Content: strings.Repeat("x", 120)})
	}
	removed := s.compact(context.Background())
	if removed == 0 {
		t.Fatal("应触发压缩")
	}
	joined := ""
	for _, m := range s.messages {
		joined += m.Content + "\n"
	}
	if !strings.Contains(joined, "压缩摘要") || !strings.Contains(joined, "SUMMARY") {
		t.Fatalf("应写入模型摘要: %q", joined)
	}
}

// 手动 /compress：保留最近 keep 条，其余摘要。
func TestManualCompress(t *testing.T) {
	chat := func(_ context.Context, req ai.ChatRequest) (*ai.ChatResponse, error) {
		if len(req.Messages) > 0 && strings.Contains(req.Messages[0].Content, "上下文压缩器") {
			return &ai.ChatResponse{Content: "MANUAL-SUMMARY"}, nil
		}
		return &ai.ChatResponse{Content: "final"}, nil
	}
	s := NewSession(DefaultConfig(), chat, NewRegistry(), "sys")
	for i := 0; i < 6; i++ {
		s.messages = append(s.messages, Message{Role: RoleUser, Content: "msg"})
	}
	n, summary, err := s.Compress(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 || !strings.Contains(summary, "MANUAL-SUMMARY") {
		t.Fatalf("n=%d summary=%q", n, summary)
	}
	if !s.CompactedReported() {
		t.Fatal("应标记 Compacted")
	}
}

// 摘要失败时回退为丢消息 + 通知，不返回错误、不假装无损。
func TestCompactSummaryFailureFallback(t *testing.T) {
	chat := func(_ context.Context, req ai.ChatRequest) (*ai.ChatResponse, error) {
		if len(req.Messages) > 0 && strings.Contains(req.Messages[0].Content, "上下文压缩器") {
			return nil, errors.New("missing key")
		}
		return &ai.ChatResponse{Content: "final"}, nil
	}
	cfg := DefaultConfig()
	cfg.MaxBudgetTokens = 50
	s := NewSession(cfg, chat, NewRegistry(), "sys")
	for i := 0; i < 6; i++ {
		s.messages = append(s.messages, Message{Role: RoleUser, Content: strings.Repeat("y", 120)})
	}
	if s.compact(context.Background()) == 0 {
		t.Fatal("应触发压缩")
	}
	joined := ""
	for _, m := range s.messages {
		joined += m.Content + "\n"
	}
	if !strings.Contains(joined, "压缩丢弃") || strings.Contains(joined, "压缩摘要") {
		t.Fatalf("摘要失败应回退到通知: %q", joined)
	}
}
