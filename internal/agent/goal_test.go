package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/logos-42/hushfusion-forge/internal/ai"
)

// 长期目标应作为 system 动态注入每次请求，且不写进 messages 历史。
func TestGoalInjectedIntoRequest(t *testing.T) {
	var captured ai.ChatRequest
	chat := func(_ context.Context, req ai.ChatRequest) (*ai.ChatResponse, error) {
		captured = req
		return &ai.ChatResponse{Content: "ok"}, nil
	}
	s := NewSession(DefaultConfig(), chat, NewRegistry(), "sys")
	s.SetGoal("完成 D9857 线圈优化")
	if _, err := s.Turn(context.Background(), "开始"); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range captured.Messages {
		if m.Role == "system" && strings.Contains(m.Content, "完成 D9857 线圈优化") {
			found = true
		}
	}
	if !found {
		t.Fatalf("请求里没有目标 system: %+v", captured.Messages)
	}
	for _, m := range s.messages {
		if strings.Contains(m.Content, "完成 D9857 线圈优化") {
			t.Fatal("目标不应写进 messages 历史")
		}
	}
}

func TestGoalClear(t *testing.T) {
	s := NewSession(DefaultConfig(), alwaysFinal("ok"), NewRegistry(), "")
	s.SetGoal("x")
	s.SetGoal("")
	if s.Goal != "" {
		t.Fatalf("清除后 Goal=%q", s.Goal)
	}
}
