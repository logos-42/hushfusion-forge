package agent

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/logos-42/hushfusion-forge/internal/ai"
)

// 历史恢复只收 user/assistant/tool；system 属于构造期（Persona/System），不进栈。
func TestRestoreValidatesRoles(t *testing.T) {
	s := newTestSession(alwaysFinal("x"), NewRegistry())
	if err := s.Restore([]Message{{Role: RoleSystem, Content: "sys"}}); err == nil {
		t.Fatal("system 角色不该混进历史栈")
	}
	if err := s.Restore([]Message{
		{Role: RoleUser, Content: "u1"},
		{Role: RoleAssistant, Content: "a1"},
	}); err != nil {
		t.Fatal(err)
	}
	if s.Len() != 2 {
		t.Fatalf("应恢复 2 条, got %d", s.Len())
	}
	if h := s.History(); h[0].Content != "u1" || h[1].Content != "a1" {
		t.Fatalf("恢复内容不对: %+v", h)
	}
	// 再恢复一次应整体替换，不是追加
	if err := s.Restore([]Message{{Role: RoleUser, Content: "u2"}}); err != nil {
		t.Fatal(err)
	}
	if s.Len() != 1 {
		t.Fatalf("应整体替换为 1 条, got %d", s.Len())
	}
}

// AfterTurn 必须在"回合成功"和"重置"两种情况下都触发，否则重置后的空历史
// 不会落盘，重启后旧对话会复活。
func TestServeAfterTurnOnTurnAndReset(t *testing.T) {
	var got []string
	h := NewServeHandler(func(id string) *Session {
		return NewSession(DefaultConfig(), alwaysFinal("ok"), NewRegistry(), "s")
	})
	h.AfterTurn = func(id string, s *Session) {
		got = append(got, id+":"+strconv.Itoa(s.Len()))
	}
	if _, err := h.Handle(context.Background(), []byte(`{"msg":"hi","session":"a"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Handle(context.Background(), []byte(`{"reset":true,"session":"a"}`)); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("应回调 2 次, got %v", got)
	}
	if got[0] != "a:2" { // user + assistant
		t.Fatalf("回合后历史应 2 条, got %q", got[0])
	}
	if got[1] != "a:0" { // 重置后为空
		t.Fatalf("重置后历史应 0 条, got %q", got[1])
	}
}

// 失败的回合不应触发 AfterTurn（没有新历史可落盘）。
func TestServeAfterTurnSkippedOnError(t *testing.T) {
	calls := 0
	h := NewServeHandler(func(id string) *Session {
		chat := func(_ context.Context, _ ai.ChatRequest) (*ai.ChatResponse, error) {
			return nil, errors.New("boom")
		}
		return NewSession(DefaultConfig(), chat, NewRegistry(), "s")
	})
	h.AfterTurn = func(string, *Session) { calls++ }
	resp, err := h.Handle(context.Background(), []byte(`{"msg":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error == "" {
		t.Fatalf("应是错误响应: %+v", resp)
	}
	if calls != 0 {
		t.Fatalf("失败回合不该回调, got %d", calls)
	}
}
