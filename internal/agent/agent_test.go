package agent

// agent_test.go —— runtime 核心测试，全部零网络（假 ChatFunc / 假执行器）。
// 覆盖：loop 收敛 / 工具往返回填 / fail-closed 三闸（未知工具、参数非法、
// 写越权）/ 上下文压缩 / skills 匹配注入 / frontmatter 解析 / serve 单行协议。

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/logos-42/hushfusion-forge/internal/ai"
)

// fakeChat 是可编程的假模型：按调用序号返回预设响应。
// 每项：respContent / toolCalls（nil=最终回复）。
type fakeTurn struct {
	content   string
	toolCalls []ai.ToolCall
}

func fakeChat(turns []fakeTurn) ChatFunc {
	i := 0
	return func(_ context.Context, req ai.ChatRequest) (*ai.ChatResponse, error) {
		if i >= len(turns) {
			return nil, errors.New("假模型被调用超过预设轮数")
		}
		t := turns[i]
		i++
		return &ai.ChatResponse{Content: t.content, ToolCalls: t.toolCalls}, nil
	}
}

// alwaysFinal 是永远直接给最终回复的假模型（不限制调用次数）。
func alwaysFinal(content string) ChatFunc {
	return func(_ context.Context, _ ai.ChatRequest) (*ai.ChatResponse, error) {
		return &ai.ChatResponse{Content: content}, nil
	}
}

// echoTool 是一个假工具：返回 args 的 JSON 摘要。
func echoTool(name string) Tool {
	return Tool{
		Spec: ai.ToolSpec{Type: "function", Function: struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Parameters  any    `json:"parameters,omitempty"`
		}{Name: name, Description: "echo 工具"}},
		Executor: func(_ context.Context, _ string, args map[string]any) (string, error) {
			b, _ := json.Marshal(args)
			return "echo:" + string(b), nil
		},
	}
}

func newTestSession(chat ChatFunc, reg *ToolRegistry) *Session {
	return NewSession(DefaultConfig(), chat, reg, "test system")
}

// 最终回复（无工具）：模型一次调用就给出答案，Turn 落栈返回。
func TestTurnFinalReply(t *testing.T) {
	s := newTestSession(fakeChat([]fakeTurn{{content: "你好"}}), NewRegistry())
	out, err := s.Turn(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if out != "你好" {
		t.Fatalf("回复 = %q, want 你好", out)
	}
	if s.Len() != 2 { // user + assistant
		t.Fatalf("消息数 = %d, want 2", s.Len())
	}
}

// 工具往返：模型先请求工具，执行结果回填为 tool 消息，第二轮产出最终回复。
// 验证 assistant(tool_calls) → tool 配对顺序（OpenAI 协议）。
func TestTurnToolRoundtrip(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register(echoTool("echo")); err != nil {
		t.Fatal(err)
	}
	chat := fakeChat([]fakeTurn{
		{content: "", toolCalls: []ai.ToolCall{{
			ID: "call_1", Type: "function",
			Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Name: "echo", Arguments: `{"x":1}`},
		}}},
		{content: "好了"},
	})
	s := newTestSession(chat, reg)
	out, err := s.Turn(context.Background(), "调工具")
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if out != "好了" {
		t.Fatalf("回复 = %q", out)
	}
	hist := s.History()
	// 顺序：user → assistant(tool_calls) → tool → assistant(最终)
	if len(hist) != 4 {
		t.Fatalf("消息数 = %d, want 4（user/assistant/tool/assistant）", len(hist))
	}
	if hist[1].Role != RoleAssistant || len(hist[1].ToolCalls) != 1 {
		t.Fatalf("第 2 条应为 assistant+tool_calls, got %+v", hist[1])
	}
	if hist[2].Role != RoleTool || hist[2].ToolCallID != "call_1" {
		t.Fatalf("第 3 条应为 tool 回填(call_1), got %+v", hist[2])
	}
	if !strings.Contains(hist[2].Content, `"x":1`) {
		t.Fatalf("tool 回填应含参数, got %q", hist[2].Content)
	}
}

// 未知工具 → fail-closed 拒绝，原因回灌给模型（模型下一轮可自愈）。
func TestUnknownToolFailClosed(t *testing.T) {
	reg := NewRegistry() // 空注册表：任何工具都未知
	chat := fakeChat([]fakeTurn{
		{content: "", toolCalls: []ai.ToolCall{{
			ID: "c1", Type: "function",
			Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Name: "nope", Arguments: `{}`},
		}}},
		{content: "没有这个工具，我换个方式"},
	})
	s := newTestSession(chat, reg)
	out, err := s.Turn(context.Background(), "调不存在的工具")
	if err != nil {
		t.Fatalf("Turn: %v", err)
	}
	if !strings.Contains(out, "换个方式") {
		t.Fatalf("模型应自愈, got %q", out)
	}
	hist := s.History()
	toolMsg := hist[len(hist)-2]
	if toolMsg.Role != RoleTool || !strings.Contains(toolMsg.Content, "未知工具") {
		t.Fatalf("工具结果应含拒绝原因, got %+v", toolMsg)
	}
}

// 写工具不在白名单 → 拒绝（AI 只能提议，不能自主写）。
func TestWriteToolRequiresWhitelist(t *testing.T) {
	reg := NewRegistry()
	writeTool := echoTool("os.propose")
	writeTool.Write = true
	if err := reg.Register(writeTool); err != nil {
		t.Fatal(err)
	}
	chat := fakeChat([]fakeTurn{
		{content: "", toolCalls: []ai.ToolCall{{
			ID: "c1", Type: "function",
			Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Name: "os.propose", Arguments: `{"id":"T-99"}`},
		}}},
		{content: "我不能自主写，先问人"},
	})
	s := newTestSession(chat, reg) // AllowWrites 缺省空
	if _, err := s.Turn(context.Background(), "写个任务"); err != nil {
		t.Fatalf("Turn: %v", err)
	}
	hist := s.History()
	toolMsg := hist[len(hist)-2]
	if !strings.Contains(toolMsg.Content, "白名单") {
		t.Fatalf("写工具应被白名单拒绝, got %q", toolMsg.Content)
	}
	// 加入白名单后应允许执行。
	s2 := newTestSession(fakeChat([]fakeTurn{
		{content: "", toolCalls: []ai.ToolCall{{
			ID: "c1", Type: "function",
			Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Name: "os.propose", Arguments: `{"id":"T-99"}`},
		}}},
		{content: "好"},
	}), reg)
	s2.AllowWrites["os.propose"] = true
	if _, err := s2.Turn(context.Background(), "写个任务"); err != nil {
		t.Fatalf("白名单内 Turn: %v", err)
	}
	hist = s2.History()
	if !strings.Contains(hist[len(hist)-2].Content, "echo:") {
		t.Fatalf("白名单内应执行成功, got %q", hist[len(hist)-2].Content)
	}
}

// 工具参数非法 JSON → fail-closed 拒绝（不 panic、不执行）。
func TestBadToolArgsFailClosed(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register(echoTool("echo")); err != nil {
		t.Fatal(err)
	}
	chat := fakeChat([]fakeTurn{
		{content: "", toolCalls: []ai.ToolCall{{
			ID: "c1", Type: "function",
			Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Name: "echo", Arguments: `{bad json`},
		}}},
		{content: "参数坏了"},
	})
	s := newTestSession(chat, reg)
	if _, err := s.Turn(context.Background(), "坏参数"); err != nil {
		t.Fatalf("Turn: %v", err)
	}
	hist := s.History()
	if !strings.Contains(hist[len(hist)-2].Content, "不是合法 JSON") {
		t.Fatalf("坏参数应拒绝, got %q", hist[len(hist)-2].Content)
	}
}

// 工具往返达到上限 → fail-closed 报错（不编造完成）。
func TestToolTurnLimitFailClosed(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register(echoTool("echo")); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.MaxToolTurns = 2
	s := NewSession(cfg, func(_ context.Context, _ ai.ChatRequest) (*ai.ChatResponse, error) {
		return &ai.ChatResponse{ToolCalls: []ai.ToolCall{{
			ID: "c", Type: "function",
			Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{Name: "echo", Arguments: `{}`},
		}}}, nil
	}, reg, "s")
	_, err := s.Turn(context.Background(), "无限工具")
	if err == nil || !strings.Contains(err.Error(), "上限") {
		t.Fatalf("应 fail-closed, got err=%v", err)
	}
}

// 失败回滚：回合中途 fail 后，消息栈回滚到 user 结束位置（不带不完整轮次的
// assistant/tool 配对）；下一回合从干净状态重试，不把过期工具结果喂给模型。
func TestTurnFailRollsBackMessages(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register(echoTool("echo")); err != nil {
		t.Fatal(err)
	}
	// 第一回合：模型永远请求工具（导致工具往返上限 fail）；第二回合起正常回复。
	firstRound := true
	chat := func(_ context.Context, req ai.ChatRequest) (*ai.ChatResponse, error) {
		if firstRound { // MaxToolTurns=2 → 第 1、2 次都请求工具，循环耗尽
			return &ai.ChatResponse{ToolCalls: []ai.ToolCall{{
				ID: "c", Type: "function",
				Function: struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}{Name: "echo", Arguments: `{}`},
			}}}, nil
		}
		return &ai.ChatResponse{Content: "正常回复"}, nil
	}
	cfg := DefaultConfig()
	cfg.MaxToolTurns = 2
	s := NewSession(cfg, chat, reg, "s")

	// 第一回合失败：消息栈应回滚到只有 user（无 assistant/tool 残留）。
	if _, err := s.Turn(context.Background(), "第一轮"); err == nil {
		t.Fatal("第一轮应 fail-closed")
	}
	firstRound = false
	if s.Stage != StageError {
		t.Fatalf("Stage = %v, want error", s.Stage)
	}
	hist := s.History()
	if len(hist) != 1 || hist[0].Role != RoleUser {
		t.Fatalf("失败后消息栈应只剩 user, got %+v", hist)
	}

	// 第二回合：干净重试，成功，消息栈 = user1 + user2 + assistant。
	out, err := s.Turn(context.Background(), "第二轮")
	if err != nil {
		t.Fatalf("第二轮应成功, err=%v", err)
	}
	if out != "正常回复" {
		t.Fatalf("回复 = %q", out)
	}
	hist = s.History()
	if len(hist) != 3 {
		t.Fatalf("消息数 = %d, want 3（user1+user2+assistant）", len(hist))
	}
	if hist[2].Role != RoleAssistant {
		t.Fatalf("末条应为 assistant, got %+v", hist[2])
	}
}

// 模型调用失败（非超时）→ fail-closed 且回滚；重试预算消耗后不再重试。
func TestTurnModelErrorRollsBack(t *testing.T) {
	chatErr := errors.New("provider 挂了")
	s := NewSession(DefaultConfig(), func(_ context.Context, _ ai.ChatRequest) (*ai.ChatResponse, error) {
		return nil, chatErr
	}, NewRegistry(), "s")
	if _, err := s.Turn(context.Background(), "hi"); err == nil {
		t.Fatal("应 fail-closed")
	}
	if s.Stage != StageError {
		t.Fatalf("Stage = %v, want error", s.Stage)
	}
	// 消息栈回滚到 user（失败轮次没有 assistant 残留）。
	hist := s.History()
	if len(hist) != 1 || hist[0].Role != RoleUser {
		t.Fatalf("失败后应只剩 user, got %+v", hist)
	}
	if !strings.Contains(s.LastError, "provider 挂了") {
		t.Fatalf("LastError 应记录原因, got %q", s.LastError)
	}
}

// 上下文压缩：小预算下触发，Compacted 标记，且 model 看到的消息不超预算。
func TestContextCompaction(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register(echoTool("echo")); err != nil {
		t.Fatal(err)
	}
	chat := alwaysFinal("ok")
	cfg := DefaultConfig()
	cfg.MaxBudgetTokens = 40 // 极小预算：约 160 字符，多条消息必然触发压缩
	s := NewSession(cfg, chat, reg, "sys")
	// 塞几条长 user 消息触发压缩。
	for i := 0; i < 8; i++ {
		if _, err := s.Turn(context.Background(), strings.Repeat("x", 100)); err != nil {
			t.Fatalf("Turn %d: %v", i, err)
		}
	}
	if !s.CompactedReported() {
		t.Fatal("应标记发生过压缩")
	}
	// model 看到的 message 估算不超预算（压缩目标：估 token ≤ 预算 + 说明容差）。
	if got := estimateTokens(s.messages); got > cfg.MaxBudgetTokens+10 {
		t.Fatalf("压缩后估 token = %d > 预算 %d+10", got, cfg.MaxBudgetTokens)
	}
	// 通知存在（诚实边界：模型知道压缩发生过）。
	found := false
	for _, m := range s.messages {
		if strings.Contains(m.Content, "压缩丢弃") {
			found = true
		}
	}
	if !found {
		t.Fatal("应含压缩通知")
	}
}

// ---- skills ----

func writeSkill(t *testing.T, dir, name, desc, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\ndescription: " + desc + "\n---\n\n" + body
	if err := os.WriteFile(filepath.Join(path, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSkillsIndexAndMatch(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "fusion-loop", "HUSHFUSION Forge 电磁设计搜索循环", "正文：forge run 的知识。")
	writeSkill(t, dir, "lean4", "Lean 4 定理形式化", "正文：lean 证明。")
	s := NewSkills([]string{dir}, nil)
	if len(s.All()) != 2 {
		t.Fatalf("索引技能数 = %d, want 2", len(s.All()))
	}
	hits := s.Match("我想跑 forge 搜索循环", 3)
	if len(hits) == 0 {
		t.Fatal("应命中 forge 技能")
	}
	if hits[0].Name != "fusion-loop" {
		t.Fatalf("第一命中 = %s, want fusion-loop", hits[0].Name)
	}
	sk, ok := s.Lookup("lean4")
	if !ok || !strings.Contains(sk.Body, "lean 证明") {
		t.Fatalf("Lookup lean4: ok=%v body=%q", ok, sk.Body)
	}
}

func TestSkillsInjectTruncation(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "longskill", "关于长技能注入截断的说明", strings.Repeat("长文", 1000))
	s := NewSkills([]string{dir}, nil)
	s.SkillInjectionBudget = 100
	sys := s.Inject("长技能截断", "")
	if !strings.Contains(sys, "<skill name=\"longskill\">") {
		t.Fatal("注入应含技能块")
	}
	if !strings.Contains(sys, "截断") {
		t.Fatal("超预算应标注截断")
	}
	if len(sys) > 1000 {
		t.Fatalf("注入过长: %d", len(sys))
	}
}

func TestParseFrontmatter(t *testing.T) {
	fm := parseFrontmatter("---\nname: x\ndescription: 描述\nother: 忽略\n---\n正文")
	if fm.Name != "x" || fm.Description != "描述" {
		t.Fatalf("fm = %+v", fm)
	}
	bad := parseFrontmatter("no frontmatter here")
	if bad.Name != "" {
		t.Fatalf("坏文件不应索引, got %+v", bad)
	}
}

// ---- serve ----

func TestServeHandleOneLine(t *testing.T) {
	h := NewServeHandler(func(id string) *Session {
		return NewSession(DefaultConfig(), alwaysFinal("回复:"+id), NewRegistry(), "s")
	})
	resp, err := h.Handle(context.Background(), []byte(`{"msg":"你好","session":"s1"}`))
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if resp.Session != "s1" || resp.Content != "回复:s1" {
		t.Fatalf("resp = %+v", resp)
	}
	// 同 session 第二次调用应有连续状态（同一 Session 实例）。
	resp2, err := h.Handle(context.Background(), []byte(`{"msg":"再来","session":"s1"}`))
	if err != nil {
		t.Fatalf("Handle2: %v", err)
	}
	if resp2.Content != "回复:s1" {
		t.Fatalf("同 session 应复用, got %+v", resp2)
	}
}

func TestServeQuitAndReset(t *testing.T) {
	h := NewServeHandler(func(id string) *Session {
		return NewSession(DefaultConfig(), fakeChat([]fakeTurn{{content: "x"}}), NewRegistry(), "s")
	})
	resp, _ := h.Handle(context.Background(), []byte(`{"quit":true}`))
	if !resp.Quit {
		t.Fatal("quit 应回 Quit")
	}
	resp, _ = h.Handle(context.Background(), []byte(`{"reset":true,"session":"a"}`))
	if !resp.Reset {
		t.Fatal("reset 应回 Reset")
	}
}
