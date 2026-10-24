package agent

// tools_ext_test.go —— 内置工具（记忆/任务/技能）的注册与执行纪律测试。
// 零网络：直接走 Session 状态（MemStore/TaskStore）+ 假 SkillsDir。

import (
	"context"
	"strings"
	"testing"

	"github.com/logos-42/hushfusion-forge/internal/ai"
)

// newBuiltinSession 构造一个装配了内置工具的会话。
// allowWrites 控制哪些写工具进白名单（nil = 全只读）。
func newBuiltinSession(t *testing.T, allowWrites map[string]bool) *Session {
	t.Helper()
	s := NewSession(DefaultConfig(), alwaysFinal("ok"), NewRegistry(), "sys")
	if err := s.RegisterBuiltinTools(nil); err != nil {
		t.Fatalf("RegisterBuiltinTools: %v", err)
	}
	if allowWrites != nil {
		for k, v := range allowWrites {
			s.AllowWrites[k] = v
		}
	}
	return s
}

// execTool 直接执行一次工具调用（跳过模型；等价于 loop 里的 registry.Execute）。
func execTool(t *testing.T, s *Session, name, argsJSON string) (string, error) {
	t.Helper()
	call := ai.ToolCall{ID: "t1", Type: "function", Function: struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}{Name: name, Arguments: argsJSON}}
	return s.registry.Execute(context.Background(), call, s.AllowWrites)
}

// 记忆工具：只读 mem.get 无需白名单；写 mem.set 需白名单，未放行 fail-closed。
func TestMemoryToolsWhitelist(t *testing.T) {
	// 只读会话：mem.set 必须被拒，mem.get 可用。
	s := newBuiltinSession(t, nil)
	if _, err := execTool(t, s, "mem.set", `{"key":"k","value":"v"}`); err == nil || !strings.Contains(err.Error(), "白名单") {
		t.Fatalf("未放行写工具应拒绝, got err=%v", err)
	}
	out, err := execTool(t, s, "mem.get", `{"key":"k"}`)
	if err != nil {
		t.Fatalf("mem.get: %v", err)
	}
	if !strings.Contains(out, "不存在") {
		t.Fatalf("空记忆应报不存在, got %q", out)
	}

	// 放行后：set 成功，get 读回。
	s2 := newBuiltinSession(t, map[string]bool{"mem.set": true})
	if out, err := execTool(t, s2, "mem.set", `{"key":"k","value":"v"}`); err != nil || !strings.Contains(out, "已记忆") {
		t.Fatalf("放行后 mem.set 应成功, out=%q err=%v", out, err)
	}
	out, _ = execTool(t, s2, "mem.get", `{"key":"k"}`)
	if !strings.Contains(out, "k=v") {
		t.Fatalf("mem.get 应读回 k=v, got %q", out)
	}

	// 模型侧使用安全名 mem_set；执行层必须映射回内部 mem.set。
	if out, err := execTool(t, s2, "mem_set", `{"key":"safe","value":"ok"}`); err != nil || !strings.Contains(out, "已记忆") {
		t.Fatalf("安全名 mem_set 应执行成功, out=%q err=%v", out, err)
	}
	out, _ = execTool(t, s2, "mem_get", `{"key":"safe"}`)
	if !strings.Contains(out, "safe=ok") {
		t.Fatalf("安全名 mem_get 应读回 safe=ok, got %q", out)
	}
}

// 任务工具：tasks.add 需白名单；tasks.list 只读；set_state 转 done 必须附证据。
func TestTaskToolsStateMachine(t *testing.T) {
	s := newBuiltinSession(t, map[string]bool{
		"tasks.add":       true,
		"tasks.set_state": true,
		"tasks.list":      true,
	})
	// 新增一条。
	out, err := execTool(t, s, "tasks.add", `{"title":"跑一次实验","goal":"G19"}`)
	if err != nil || !strings.Contains(out, "A-001") {
		t.Fatalf("tasks.add: out=%q err=%v", out, err)
	}
	// 转 active（无证据可转）。
	if out, err := execTool(t, s, "tasks.set_state", `{"id":"A-001","state":"active"}`); err != nil || !strings.Contains(out, "→ active") {
		t.Fatalf("转 active: out=%q err=%v", out, err)
	}
	// 转 done 无证据 → 拒（完成只能被证明）。
	if _, err := execTool(t, s, "tasks.set_state", `{"id":"A-001","state":"done"}`); err == nil || !strings.Contains(err.Error(), "证据") {
		t.Fatalf("done 无证据应拒绝, got err=%v", err)
	}
	// 转 done 带证据 → 过。
	if _, err := execTool(t, s, "tasks.set_state", `{"id":"A-001","state":"done","evidence":"runs/x/report.md"}`); err != nil {
		t.Fatalf("done 带证据应过, err=%v", err)
	}
	// 终态不可再转。
	if _, err := execTool(t, s, "tasks.set_state", `{"id":"A-001","state":"blocked"}`); err == nil {
		t.Fatal("终态不可转回")
	}
	// list 应含任务与证据。
	out, _ = execTool(t, s, "tasks.list", `{}`)
	if !strings.Contains(out, "A-001") || !strings.Contains(out, "done") {
		t.Fatalf("tasks.list 应含 A-001 done, got %q", out)
	}
}

// 任务写工具未放行 → fail-closed；list 只读无需白名单。
func TestTaskToolsReadOnlyDenied(t *testing.T) {
	s := newBuiltinSession(t, nil)
	if _, err := execTool(t, s, "tasks.add", `{"title":"x"}`); err == nil || !strings.Contains(err.Error(), "白名单") {
		t.Fatalf("未放行 tasks.add 应拒绝, got err=%v", err)
	}
	if out, err := execTool(t, s, "tasks.list", `{}`); err != nil || !strings.Contains(out, "无任务") {
		t.Fatalf("tasks.list 只读应可用, out=%q err=%v", out, err)
	}
}

// skill.view：无技能目录时不注册（nil 返回空，工具可选不假装）；有目录时按名查看。
func TestSkillViewTool(t *testing.T) {
	// 无技能目录 → 注册为空，未知工具 fail-closed。
	s := newBuiltinSession(t, nil)
	if _, err := execTool(t, s, "skill.view", `{"name":"x"}`); err == nil || !strings.Contains(err.Error(), "未知工具") {
		t.Fatalf("无技能目录时 skill.view 应不存在, got err=%v", err)
	}

	// 有技能目录 → 注册，能按名取正文。
	dir := t.TempDir()
	writeSkill(t, dir, "loopskill", "循环说明", "完整正文：这里是循环的详细知识。")
	s2 := NewSession(DefaultConfig(), alwaysFinal("ok"), NewRegistry(), "sys")
	if err := s2.RegisterBuiltinTools(NewSkills([]string{dir}, nil)); err != nil {
		t.Fatalf("RegisterBuiltinTools(skills): %v", err)
	}
	out, err := execTool(t, s2, "skill.view", `{"name":"loopskill"}`)
	if err != nil {
		t.Fatalf("skill.view: %v", err)
	}
	if !strings.Contains(out, "详细知识") {
		t.Fatalf("应返回完整正文, got %q", out)
	}
	// 查不存在的技能 → 明确报错。
	if _, err := execTool(t, s2, "skill.view", `{"name":"nope"}`); err == nil || !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("查无此技能应报错, got err=%v", err)
	}
}

// 工具 schema 正确暴露给模型（Specs() 含且仅含注册工具）。
func TestBuiltinSpecsExposed(t *testing.T) {
	s := newBuiltinSession(t, nil)
	specs := s.registry.Specs()
	names := map[string]bool{}
	for _, sp := range specs {
		names[sp.Function.Name] = true
	}
	for _, want := range []string{"mem_get", "mem_set", "mem_reset", "tasks_list", "tasks_add", "tasks_set_state", "agent_paths"} {
		if !names[want] {
			t.Fatalf("Specs() 缺工具 %q（got %v）", want, names)
		}
	}
	for name := range names {
		if strings.Contains(name, ".") {
			t.Fatalf("模型可见工具名不能含点号: %q", name)
		}
	}
	// 写工具在 Specs 里不带 Write 标记（模型不可见权限面；权限在 execute() 层）。
	if len(specs) == 0 {
		t.Fatal("Specs() 不应为空")
	}
}

// ServeHandler 传入 RegisterBuiltinTools 的会话构造器：同一会话内记忆跨回合保留。
func TestServeSessionMemoryPersists(t *testing.T) {
	h := NewServeHandler(func(id string) *Session {
		s := NewSession(DefaultConfig(), alwaysFinal("ok"), NewRegistry(), "sys")
		if err := s.RegisterBuiltinTools(nil); err != nil {
			t.Fatalf("RegisterBuiltinTools: %v", err)
		}
		s.AllowWrites["mem.set"] = true
		return s
	})
	// 会话是**懒创建**的（get() 在首次 Handle 时才 NewSession），
	// 所以必须先 Handle 一次再取实例，否则拿到 nil。
	if _, err := h.Handle(context.Background(), []byte(`{"msg":"hi","session":"default"}`)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	s1 := h.sessions["default"]
	if s1 == nil {
		t.Fatal("首次 Handle 后应已创建会话")
	}
	s1.Memory.Set("k", "v")

	// 再次 Handle 必须复用同一会话实例，记忆跨请求保留。
	if _, err := h.Handle(context.Background(), []byte(`{"msg":"again","session":"default"}`)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	s2 := h.sessions["default"]
	if s2 != s1 {
		t.Fatal("同 session 应复用同一会话实例（不应重建）")
	}
	if v, ok := s2.Memory.Get("k"); !ok || v != "v" {
		t.Fatalf("同 session 记忆应跨请求保留, got %q ok=%v", v, ok)
	}
}
