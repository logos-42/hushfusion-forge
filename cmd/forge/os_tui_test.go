package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/logos-42/hushfusion-forge/internal/aios"
	"github.com/logos-42/hushfusion-forge/internal/osview"
)

func keyRune(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r} }
func keyCode(c rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: c} }

// setupTempOS 复制一份 os/ 到临时目录并设为 HUSHFUSION_OS_DIR（沙箱写操作）。
func setupTempOS(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(repoRoot(), "os")
	if out, err := exec.Command("cp", "-r", src+"/.", dir).CombinedOutput(); err != nil {
		t.Fatalf("复制 os/ 失败: %v (%s)", err, out)
	}
	t.Setenv("HUSHFUSION_OS_DIR", dir)
	return dir
}

func mustLoad(t *testing.T, base string) *osview.Snapshot {
	t.Helper()
	s, err := osview.Load(base)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// 状态机：面板 → (a) AI 表单 → 提交 → 思考态(异步 Cmd) → 结果 → 报告态 → q 退出。
func TestTUIStateMachine(t *testing.T) {
	base := setupTempOS(t)
	m := newOSModel(base, mustLoad(t, base))

	nm, _ := m.Update(keyRune('a'))
	m = nm.(*osModel)
	if m.state != stateForm || m.form.kind != "ai" {
		t.Fatalf("a 应打开 AI 表单, got state=%v kind=%v", m.state, m.form.kind)
	}

	// 填目标并回车提交 → 思考态 + 异步 tea.Cmd（spinner 异步的核心）。
	m.form.inputs[0].SetValue("整理机会池")
	m.form.focus = len(m.form.inputs) - 1 // 跳到最后一字段，一次回车即提交
	nm, cmd := m.Update(keyCode(rune(tea.KeyEnter)))
	m = nm.(*osModel)
	if m.state != stateThinking {
		t.Fatalf("提交 AI 目标后应进入思考态, got %v", m.state)
	}
	if cmd == nil {
		t.Fatal("AI 提交应返回非 nil tea.Cmd（异步执行）")
	}

	// 收到结果 → 报告态。
	nm, _ = m.Update(aiResultMsg{rep: &aios.Report{Goal: "g"}, err: nil})
	m = nm.(*osModel)
	if m.state != stateReport {
		t.Fatalf("结果消息后应进入报告态, got %v", m.state)
	}

	// 报告态按 q → 退出 Cmd。
	_, cmd = m.Update(keyRune('q'))
	if cmd == nil {
		t.Fatal("q 应返回 tea.Quit")
	}
}

// 任务表单经模型写穿到内核（沙箱 TASKS.md 真的多一行）。
func TestTUITaskFormWrites(t *testing.T) {
	base := setupTempOS(t)
	m := newOSModel(base, mustLoad(t, base))
	m.Update(keyRune('n'))
	set := func(i int, s string) { m.form.inputs[i].SetValue(s) }
	set(0, "T-99")
	set(1, "测试任务")
	set(2, "3")
	set(3, "3")
	set(4, "3")
	set(5, "2")
	m.form.focus = len(m.form.inputs) - 1 // 跳到最后一字段，一次回车即提交
	nm, _ := m.Update(keyCode(rune(tea.KeyEnter)))
	m = nm.(*osModel)
	if m.state != statePanels {
		t.Fatalf("任务录入后应回面板, got %v (status=%s)", m.state, m.status)
	}
	if !strings.Contains(m.status, "已录入") {
		t.Fatalf("应提示已录入, got %s", m.status)
	}
	raw, _ := os.ReadFile(filepath.Join(base, "TASKS.md"))
	if !strings.Contains(string(raw), "T-99") {
		t.Fatal("T-99 应已写入沙箱 TASKS.md")
	}
}

// 各态 View() 不 panic。
func TestTUIViewsNoPanic(t *testing.T) {
	base := setupTempOS(t)
	m := newOSModel(base, mustLoad(t, base))
	if v := m.View(); !strings.Contains(v.Content, "HUSHFUSION OS") {
		t.Fatal("面板视图应含标题")
	}
	m.openForm("task")
	if v := m.View(); !strings.Contains(v.Content, "录入") {
		t.Fatal("表单视图应含录入标题")
	}
	m.state = stateThinking
	m.thinkingGoal = "x"
	if v := m.View(); !strings.Contains(v.Content, "思考中") {
		t.Fatal("思考视图应含 思考中")
	}
	m.state = stateReport
	m.report = &aios.Report{Goal: "g"}
	if v := m.View(); !strings.Contains(v.Content, "粘合报告") {
		t.Fatal("报告视图应含粘合报告")
	}
}
