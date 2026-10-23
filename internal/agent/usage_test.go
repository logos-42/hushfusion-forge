package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/logos-42/hushfusion-forge/internal/ai"
)

// 真调用路径：provider 回报的 usage 应入账本（LastUsage + 会话累计）。
func TestSessionRecordsUsage(t *testing.T) {
	usage := ai.Usage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120, CachedTokens: 40, CacheReported: true}
	chat := func(_ context.Context, _ ai.ChatRequest) (*ai.ChatResponse, error) {
		return &ai.ChatResponse{Content: "ok", Usage: usage}, nil
	}
	s := newTestSession(chat, NewRegistry())
	if _, err := s.Turn(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	if s.LastUsage.PromptTokens != 100 {
		t.Fatalf("LastUsage 应记录真实用量: %+v", s.LastUsage)
	}
	l := s.Usage()
	if l.Calls != 1 || l.Prompt != 100 || l.Completion != 20 || l.Cached != 40 || !l.CacheReported {
		t.Fatalf("账本累加错: %+v", l)
	}
	if got := l.CacheHitRate(); got < 0.39 || got > 0.41 {
		t.Fatalf("命中率应≈0.4, got %v", got)
	}
}

// 未上报 usage 的 provider（全 0）⇒ 不应污染校准系数（scale 保持 1）。
func TestTokenScaleStaysNeutralWithoutUsage(t *testing.T) {
	s := newTestSession(alwaysFinal("x"), NewRegistry())
	s.messages = []Message{{Role: RoleUser, Content: strings.Repeat("a", 400)}}
	s.lastEstimate = 100
	if got := s.tokenScale(); got != 1 {
		t.Fatalf("无真实用量时 scale 应为 1, got %v", got)
	}
}

// 中文被 len/4 低估：校准后同样的上下文应触发压缩（这正是"压缩时机不准"的根因）。
func TestTokenScaleCalibratesCompaction(t *testing.T) {
	s := newTestSession(alwaysFinal("x"), NewRegistry())
	s.cfg.MaxBudgetTokens = 100
	s.messages = []Message{
		{Role: RoleUser, Content: strings.Repeat("低温超导", 25)}, // 300 字节 /4 = 75 估算
		{Role: RoleAssistant, Content: "a"},
		{Role: RoleUser, Content: "b"},
	}
	// 未校准：估算 75 < 预算 100 ⇒ 不压缩
	if n := s.compact(context.Background()); n != 0 {
		t.Fatalf("未校准时不该压缩, got %d", n)
	}
	// 校准：真实 500 / 估算 27 ≈ 18.5 倍（中文 UTF-8 的典型低估幅度）
	s.LastUsage = ai.Usage{PromptTokens: 500}
	s.lastEstimate = 27
	if scale := s.tokenScale(); scale < 10 {
		t.Fatalf("scale 应≈18.5, got %v", scale)
	}
	if n := s.compact(context.Background()); n == 0 {
		t.Fatal("校准后应触发压缩")
	}
	if !s.CompactedReported() {
		t.Fatal("压缩发生过应留痕")
	}
}

// 荒谬比例不采信（防脏数据把预算拉爆）。
func TestTokenScaleRejectsAbsurdRatio(t *testing.T) {
	s := newTestSession(alwaysFinal("x"), NewRegistry())
	s.lastEstimate = 1
	s.LastUsage = ai.Usage{PromptTokens: 1_000_000} // 比例 100 万 ⇒ 不可信
	if got := s.tokenScale(); got != 1 {
		t.Fatalf("荒谬比例应回退 1, got %v", got)
	}
}

// OnUsage 回调（TUI/serve 实时显示用）。
func TestSessionOnUsageCallback(t *testing.T) {
	var got []ai.Usage
	chat := func(_ context.Context, _ ai.ChatRequest) (*ai.ChatResponse, error) {
		return &ai.ChatResponse{Content: "ok", Usage: ai.Usage{PromptTokens: 7, CompletionTokens: 3}}, nil
	}
	s := newTestSession(chat, NewRegistry())
	s.OnUsage = func(u ai.Usage) { got = append(got, u) }
	if _, err := s.Turn(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].PromptTokens != 7 {
		t.Fatalf("OnUsage 应收到一次真实用量: %+v", got)
	}
}
