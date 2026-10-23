package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	key "charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/logos-42/hushfusion-forge/internal/aios"
	"github.com/logos-42/hushfusion-forge/internal/osview"
)

func keyRune(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r} }
func keyCode(c rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: c} }

// keyText 模拟真实终端的可打印键（Code + Text 都有；textinput 按 Text 插入）。
func keyText(r rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: r, Text: string(r)} }

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

// 状态机：面板 → (a) AI 表单 → 提交 → 内联思考（busy=true，仍在面板态）→
// 结果内联落展示框 → q 退出。不再切「思考中 / 报告」两个独立弹窗。
func TestTUIStateMachine(t *testing.T) {
	base := setupTempOS(t)
	m := newOSModel(base, mustLoad(t, base))

	m.openForm("ai")
	if m.state != stateForm || m.form.kind != "ai" {
		t.Fatalf("openForm(ai) 应打开 AI 表单, got state=%v kind=%v", m.state, m.form.kind)
	}

	// 填目标并回车提交 → 内联思考：仍在主控制台，busy=true + 异步 tea.Cmd。
	m.form.inputs[0].SetValue("整理机会池")
	m.form.focus = len(m.form.inputs) - 1 // 跳到最后一字段，一次回车即提交
	nm, cmd := m.Update(keyCode(rune(tea.KeyEnter)))
	m = nm.(*osModel)
	if m.state != statePanels {
		t.Fatalf("提交 AI 目标后应留在主控制台（内联）, got %v", m.state)
	}
	if !m.busy {
		t.Fatal("提交 AI 后 busy 应为 true（内联思考中）")
	}
	if cmd == nil {
		t.Fatal("AI 提交应返回非 nil tea.Cmd（异步执行）")
	}

	// 收到结果 → 仍是面板态（结果内联落展示框），busy 解除。
	nm, _ = m.Update(aiResultMsg{rep: &aios.Report{Goal: "g"}, err: nil})
	m = nm.(*osModel)
	if m.state != statePanels {
		t.Fatalf("结果应内联在控制台（不切报告弹窗）, got %v", m.state)
	}
	if m.busy {
		t.Fatal("收到结果后 busy 应解除")
	}

	// 面板只读后 q 不再退出；用 /quit 退出。
	m.cmdInput.SetValue("/quit")
	_, cmd = m.Update(keyCode(rune(tea.KeyEnter)))
	if cmd == nil {
		t.Fatal("/quit 应返回 tea.Quit")
	}
}

// 任务表单经模型写穿到内核（沙箱 TASKS.md 真的多一行）。
func TestTUITaskFormWrites(t *testing.T) {
	base := setupTempOS(t)
	m := newOSModel(base, mustLoad(t, base))
	m.openForm("task")
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

// ? 键切换帮助覆盖层，且帮助由 keyMap 声明式绑定自动生成。
func TestTUIHelpOverlay(t *testing.T) {
	base := setupTempOS(t)
	m := newOSModel(base, mustLoad(t, base))
	nm, _ := m.runSlash("help")
	m = nm.(*osModel)
	if !m.showHelp {
		t.Fatal("/help 应打开帮助覆盖层")
	}
	if !strings.Contains(m.View().Content, "按键帮助") {
		t.Fatal("帮助屏应含『按键帮助』")
	}
	nm, _ = m.Update(keyRune('x')) // 任意键关闭
	m = nm.(*osModel)
	if m.showHelp {
		t.Fatal("任意键应关闭帮助")
	}
}

// keyMap 声明式绑定：km.ai 匹配 a、km.open 匹配 enter、km.help 匹配 ?。
func TestKeyMapBindings(t *testing.T) {
	if !key.Matches(keyRune('a'), km.ai) {
		t.Fatal("km.ai 应匹配 a")
	}
	if !key.Matches(keyCode(rune(tea.KeyEnter)), km.open) {
		t.Fatal("km.open 应匹配 enter")
	}
	if !key.Matches(keyRune('?'), km.help) {
		t.Fatal("km.help 应匹配 ?")
	}
}

// 主界面只读：字母进输入框、方向键不切面板、空回车不开详情。
func TestTUIInputOnlyNoPanelShortcuts(t *testing.T) {
	base := setupTempOS(t)
	m := newOSModel(base, mustLoad(t, base))
	sel := m.sel

	nm, _ := m.Update(keyText('a'))
	m = nm.(*osModel)
	if m.state != statePanels {
		t.Fatalf("a 不应再打开表单, state=%v", m.state)
	}
	if got := m.cmdInput.Value(); got != "a" {
		t.Fatalf("a 应写入输入框, got %q", got)
	}

	nm, _ = m.Update(keyCode(rune(tea.KeyLeft)))
	m = nm.(*osModel)
	if m.sel != sel {
		t.Fatalf("方向键不应切换面板 (%d → %d)", sel, m.sel)
	}

	m.cmdInput.SetValue("")
	nm, _ = m.Update(keyCode(rune(tea.KeyEnter)))
	m = nm.(*osModel)
	if m.state != statePanels {
		t.Fatalf("空回车不应打开面板详情, state=%v", m.state)
	}
}

// 仪表盘快照在输出（forge 框）上方；输入框上下各一条分隔线；面板不再套框。
func TestConsoleLayout(t *testing.T) {
	base := setupTempOS(t)
	m := newOSModel(base, mustLoad(t, base))
	m.width, m.height = 120, 40

	c := m.renderConsole(m.width)
	iDash := strings.Index(c, "仪表盘")
	iForge := strings.Index(c, "forge")
	if iDash < 0 || iForge < 0 || iDash > iForge {
		t.Fatalf("仪表盘应在输出(forge)之前: dash=%d forge=%d", iDash, iForge)
	}

	lines := strings.Split(c, "\n")
	for i, ln := range lines {
		if !strings.Contains(ln, "❯") {
			continue
		}
		if i == 0 || !strings.Contains(lines[i-1], "─") {
			t.Fatalf("输入框上方应有分隔线")
		}
		if i+1 >= len(lines) || !strings.Contains(lines[i+1], "─") {
			t.Fatalf("输入框下方应有分隔线")
		}
		break
	}

	if pan := m.renderPanels(m.width); strings.ContainsAny(pan, "╭╮╰╯") {
		t.Fatalf("仪表盘面板不应再套框: %q", pan)
	}
	out := m.renderOutputBox(m.width, 10)
	if !strings.Contains(out, "forge") {
		t.Fatalf("输出区应有 forge 标题: %q", out)
	}
	if strings.ContainsAny(out, "╭╮╰╯") {
		t.Fatalf("输出区不应再套圆角框: %q", out)
	}
}

// ↑↓ 在输入框里回填以前提交过的文本；↓ 越过最新一条恢复草稿。
func TestTUIInputHistory(t *testing.T) {
	base := setupTempOS(t)
	m := newOSModel(base, mustLoad(t, base))
	m.pushHistory("first goal")
	m.pushHistory("/check")

	m.cmdInput.SetValue("draft")
	m = pressKey(t, m, rune(tea.KeyUp))
	if got := m.cmdInput.Value(); got != "/check" {
		t.Fatalf("↑ 应回填最近一条, got %q", got)
	}
	m = pressKey(t, m, rune(tea.KeyUp))
	if got := m.cmdInput.Value(); got != "first goal" {
		t.Fatalf("再 ↑ 应回填更早一条, got %q", got)
	}
	m = pressKey(t, m, rune(tea.KeyDown))
	if got := m.cmdInput.Value(); got != "/check" {
		t.Fatalf("↓ 应回到更新一条, got %q", got)
	}
	m = pressKey(t, m, rune(tea.KeyDown))
	if got := m.cmdInput.Value(); got != "draft" {
		t.Fatalf("↓ 越过最新一条应恢复草稿, got %q", got)
	}
}

// 主输入框也支持粘贴（bracketed paste → tea.PasteMsg）。
func TestTUICmdInputPaste(t *testing.T) {
	base := setupTempOS(t)
	m := newOSModel(base, mustLoad(t, base))
	nm, _ := m.Update(tea.PasteMsg{Content: "/model"})
	m = nm.(*osModel)
	if got := m.cmdInput.Value(); got != "/model" {
		t.Fatalf("粘贴应写入输入框, got %q", got)
	}
	if !m.slashActive {
		t.Fatal("粘贴 /model 后应激活命令面板")
	}
}
