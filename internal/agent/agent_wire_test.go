package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sessionWith 造一个带指定 system 的会话（复用 agent_test.go 的 alwaysFinal/newTestSession）。
func sessionWith(system string) *Session {
	s := newTestSession(alwaysFinal("（占位回复）"), NewRegistry())
	s.System = system
	return s
}

// ---------------------------------------------------------------------------
// persona 注入
// ---------------------------------------------------------------------------

// persona 必须拼在 System 之前（人格 > 指令），且只出现一次 system 消息。
func TestAssemblePrependsPersona(t *testing.T) {
	s := sessionWith("指令：先读证据")
	s.Persona = "# Agent Bootstrap\n## SOUL.md（persona）\n我是审慎的工程 agent"

	msgs := s.assemble()
	if len(msgs) == 0 || msgs[0].Role != "system" {
		t.Fatalf("首条应为 system, got %+v", msgs)
	}
	sys := msgs[0].Content
	pi := strings.Index(sys, "我是审慎的工程 agent")
	si := strings.Index(sys, "指令：先读证据")
	if pi < 0 || si < 0 {
		t.Fatalf("system 应同时含 persona 与指令: %q", sys)
	}
	if pi > si {
		t.Fatalf("persona 应拼在指令之前: persona@%d 指令@%d", pi, si)
	}
	n := 0
	for _, m := range msgs {
		if m.Role == "system" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("应只有 1 条 system, got %d", n)
	}
}

func TestSystemTextCombinations(t *testing.T) {
	s := sessionWith("")
	if got := s.systemText(); got != "" {
		t.Fatalf("两者皆空应为空, got %q", got)
	}
	s.Persona = "P"
	if got := s.systemText(); got != "P" {
		t.Fatalf("仅 persona 时应为 P, got %q", got)
	}
	s.System = "S"
	if got := s.systemText(); got != "P\n\nS" {
		t.Fatalf("两者皆有时应为 P\\n\\nS, got %q", got)
	}
}

// WithPersona 从磁盘发现文档并注入；目录为空不是错误。
func TestWithPersonaLoadsFromDisk(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "SOUL.md"), []byte("人格：谨慎"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("协约：证据优先"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := sessionWith("指令")
	if err := s.WithPersona(dir, 4000); err != nil {
		t.Fatal(err)
	}
	sys := s.systemText()
	if !strings.Contains(sys, "人格：谨慎") || !strings.Contains(sys, "协约：证据优先") {
		t.Fatalf("persona 块应含两份文档: %q", sys)
	}
	if !strings.Contains(sys, "Agent Bootstrap") {
		t.Fatalf("应有 bootstrap 标题: %q", sys)
	}
}

func TestWithPersonaEmptyDirIsNoop(t *testing.T) {
	s := sessionWith("指令")
	if err := s.WithPersona(t.TempDir(), 4000); err != nil {
		t.Fatalf("空目录不应报错: %v", err)
	}
	if s.Persona != "" {
		t.Fatalf("无文档时 Persona 应为空, got %q", s.Persona)
	}
	if s.systemText() != "指令" {
		t.Fatalf("无 persona 时应原样用 System")
	}
}

// ---------------------------------------------------------------------------
// review 完成门
// ---------------------------------------------------------------------------

// 完成门未批准 ⇒ 转 done 被拒，且任务状态不变（不许部分成功）。
func TestTaskCompleteGateRejects(t *testing.T) {
	ts := NewTaskStoreWithGate(func(_ *Task) (bool, string, error) {
		return false, "二审说证据不足", nil
	})
	id := ts.Add("补低温数据", "goal-x")
	if err := ts.SetState(id, TaskActive, "ai", ""); err != nil {
		t.Fatal(err)
	}
	err := ts.SetState(id, TaskDone, "ai", "os/EXPERIMENTS.md")
	if err == nil || !strings.Contains(err.Error(), "证据不足") {
		t.Fatalf("门未批准应拒并带回理由: %v", err)
	}
	got, _ := ts.Get(id)
	if got.State != TaskActive {
		t.Fatalf("被拒后状态不应变成 done, got %s", got.State)
	}
}

// 门批准 ⇒ 可 done（证据仍然必须）。
func TestTaskCompleteGateApproves(t *testing.T) {
	ts := NewTaskStoreWithGate(func(_ *Task) (bool, string, error) { return true, "批准", nil })
	id := ts.Add("任务", "g")
	_ = ts.SetState(id, TaskActive, "ai", "")
	if err := ts.SetState(id, TaskDone, "ai", ""); err == nil {
		t.Fatal("无证据即使门批准也应拒")
	}
	if err := ts.SetState(id, TaskDone, "ai", "os/EXPERIMENTS.md"); err != nil {
		t.Fatalf("有证据 + 门批准应放行: %v", err)
	}
	got, _ := ts.Get(id)
	if got.State != TaskDone {
		t.Fatalf("应已 done, got %s", got.State)
	}
}

// 门自身出错 ⇒ 上抛（不吞、不放行）。
func TestTaskCompleteGateError(t *testing.T) {
	ts := NewTaskStoreWithGate(func(_ *Task) (bool, string, error) {
		return false, "", os.ErrDeadlineExceeded
	})
	id := ts.Add("任务", "g")
	_ = ts.SetState(id, TaskActive, "ai", "")
	err := ts.SetState(id, TaskDone, "ai", "证据")
	if err == nil || !strings.Contains(err.Error(), "完成门执行失败") {
		t.Fatalf("门错误应上抛: %v", err)
	}
}

// 无门时行为不变（只需证据）。
func TestTaskNoGateBehavesAsBefore(t *testing.T) {
	ts := NewTaskStore()
	id := ts.Add("任务", "g")
	_ = ts.SetState(id, TaskActive, "ai", "")
	if err := ts.SetState(id, TaskDone, "ai", "证据路径"); err != nil {
		t.Fatalf("无门时只需证据: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 记忆持久化（v0.2）
// ---------------------------------------------------------------------------

// 文件模式：写入后重开应读回（原子替换，不留 .tmp）。
func TestMemStoreFilePersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.json")
	ms, err := NewMemStoreFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if ms.Path() != path {
		t.Fatalf("Path 应回文件路径, got %q", ms.Path())
	}
	ms.Set("project", "低温数据补全")
	ms.Set("owner", "leo")
	if ms.LastPersistErr != nil {
		t.Fatalf("落盘不应报错: %v", ms.LastPersistErr)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("原子替换后不应留 .tmp")
	}

	ms2, err := NewMemStoreFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := ms2.Get("project"); !ok || v != "低温数据补全" {
		t.Fatalf("重开应读回记忆: %q ok=%v", v, ok)
	}
	if ms2.Len() != 2 {
		t.Fatalf("应 2 条, got %d", ms2.Len())
	}
}

// 损坏文件 ⇒ 构造期报错（不静默丢记忆）。
func TestMemStoreFileCorruptErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.json")
	if err := os.WriteFile(path, []byte("{这不是 JSON"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewMemStoreFile(path); err == nil {
		t.Fatal("损坏的记忆文件应报错（不静默从空开始）")
	}
}

// 文件不存在 ⇒ 空库，不是错误。
func TestMemStoreFileMissingIsEmpty(t *testing.T) {
	ms, err := NewMemStoreFile(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("首次运行不应报错: %v", err)
	}
	if ms.Len() != 0 {
		t.Fatalf("应为空库, got %d", ms.Len())
	}
	if _, err := NewMemStoreFile("  "); err == nil {
		t.Fatal("空路径应被拒")
	}
}

// 内存模式：不落任何文件，LastPersistErr 恒 nil。
func TestMemStoreInMemoryNoFile(t *testing.T) {
	dir := t.TempDir()
	ms := NewMemStore()
	ms.Set("a", "1")
	if ms.Path() != "" {
		t.Fatalf("内存模式 Path 应为空, got %q", ms.Path())
	}
	if ms.LastPersistErr != nil {
		t.Fatal("内存模式不应有落盘错误")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("内存模式不应写文件, got %v", entries)
	}
}

// Reset 在文件模式下连磁盘一起清空。
func TestMemStoreResetPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.json")
	ms, _ := NewMemStoreFile(path)
	ms.Set("a", "1")
	ms.Reset()
	if ms.Len() != 0 {
		t.Fatal("Reset 后应为空")
	}
	ms2, _ := NewMemStoreFile(path)
	if ms2.Len() != 0 {
		t.Fatalf("Reset 应连磁盘一起清空, got %d", ms2.Len())
	}
}

// Delete 也持久化。
func TestMemStoreDeletePersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.json")
	ms, _ := NewMemStoreFile(path)
	ms.Set("a", "1")
	if !ms.Delete("a") {
		t.Fatal("Delete 应返回 true")
	}
	if ms.Delete("ghost") {
		t.Fatal("删不存在应 false")
	}
	ms2, _ := NewMemStoreFile(path)
	if _, ok := ms2.Get("a"); ok {
		t.Fatal("Delete 应持久化")
	}
}

// 记忆文件内容是人可读的 JSON（可 git diff / 可审计）。
func TestMemStoreFileIsReadableJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.json")
	ms, _ := NewMemStoreFile(path)
	ms.Set("k", "v")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Version int               `json:"version"`
		Items   map[string]string `json:"items"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("应为合法 JSON: %v", err)
	}
	if f.Version != memFileVersion || f.Items["k"] != "v" {
		t.Fatalf("内容不符: %+v", f)
	}
}

// Summary 供 system 注入。
func TestMemStoreSummary(t *testing.T) {
	ms := NewMemStore()
	if ms.Summary() != "" {
		t.Fatal("空记忆应无摘要")
	}
	ms.Set("project", "低温")
	if got := ms.Summary(); !strings.Contains(got, "project=低温") || !strings.Contains(got, "会话记忆") {
		t.Fatalf("摘要应含条目: %q", got)
	}
}

// 会话默认存储：记忆是内存模式；写记忆/任务不污染消息栈。
func TestSessionDefaultStoresAreMemoryMode(t *testing.T) {
	s := newTestSession(alwaysFinal("x"), NewRegistry())
	if s.Memory.Path() != "" {
		t.Fatal("默认记忆应为内存模式")
	}
	s.Memory.Set("x", "1")
	s.Tasks.Add("任务", "g")
	if s.Len() != 0 {
		t.Fatal("写记忆/任务不应污染消息栈")
	}
}
