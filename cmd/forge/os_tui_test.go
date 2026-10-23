package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	key "charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/logos-42/hushfusion-forge/internal/agent"
	"github.com/logos-42/hushfusion-forge/internal/ai"
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
	// 真实工作区可能有本机 config（含 key），测试用副本必须剔除，避免污染环境变量。
	_ = os.Remove(filepath.Join(dir, "forge.config.json"))
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
	if m.state != statePanels || m.detail != 0 {
		t.Fatalf("方向键不应切换/打开面板, state=%v detail=%d", m.state, m.detail)
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
	m.out = append(m.out, "hello") // 有输出才渲染 forge 框

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
	if dash := m.renderDashboard(m.width); !strings.ContainsAny(dash, "╭╮╰╯") {
		t.Fatalf("仪表盘整体应套圆角框: %q", dash)
	}
	out := m.renderOutputBox(m.width, 10)
	if !strings.Contains(out, "forge") || !strings.ContainsAny(out, "╭╮╰╯") {
		t.Fatalf("输出区应是带 forge 的圆角框: %q", out)
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

// 用户发送内容在回复区用上下两条分隔线包住：── / ❯ hi / ──。
func TestUserEchoFormat(t *testing.T) {
	base := setupTempOS(t)
	m := newOSModel(base, mustLoad(t, base))
	m.width, m.height = 40, 40
	m.appendUserEcho("hi")
	if len(m.lastEcho) != 3 {
		t.Fatalf("回显应为三行, got %d", len(m.lastEcho))
	}
	if !strings.Contains(stripANSI(m.lastEcho[1]), "❯ hi") {
		t.Fatalf("中间应是 ❯ hi: %q", m.lastEcho[1])
	}
	if m.lastEcho[0] == "" || m.lastEcho[0] != m.lastEcho[2] || !strings.Contains(m.lastEcho[0], "─") {
		t.Fatalf("上下分隔线应一致且为横线: %q / %q", m.lastEcho[0], m.lastEcho[2])
	}
	if len(m.sentLog) != 3 {
		t.Fatalf("会话落盘应记录发送内容, got %d 行", len(m.sentLog))
	}
}

// 工具调用在 forge 框外逐行：图标 + 描述 + 耗时。
func TestToolLogOutsideForgeBox(t *testing.T) {
	base := setupTempOS(t)
	m := newOSModel(base, mustLoad(t, base))
	m.width, m.height = 120, 40
	m.out = append(m.out, "reply")
	m.appendToolCalls(&aios.Report{
		Plan: []aios.PlanStep{{Syscall: "read", Args: map[string]any{"path": "index.md"}, Allowed: true}},
		Results: []aios.ExecResult{{
			Syscall: "read", Args: map[string]any{"path": "index.md"},
			OK: true, Duration: 2700 * time.Millisecond,
		}},
	})
	if len(m.toolLog) != 1 || !strings.Contains(stripANSI(m.toolLog[0]), "📖 Read index.md") {
		t.Fatalf("工具行格式不对: %#v", m.toolLog)
	}
	c := stripANSI(m.renderConsole(m.width))
	boxTop := strings.LastIndex(c, "╭")
	toolIdx := strings.Index(c, "📖 Read index.md")
	if boxTop < 0 || toolIdx < 0 || toolIdx > boxTop {
		t.Fatalf("工具调用应在 forge 框外/上方（先工具后回复框）: boxTop=%d tool=%d", boxTop, toolIdx)
	}
}

func TestToolLineShellFormat(t *testing.T) {
	base := setupTempOS(t)
	m := newOSModel(base, mustLoad(t, base))
	line := stripANSI(m.toolLine(aios.ExecResult{
		Syscall: "shell", Args: map[string]any{"cmd": "python3 scripts/version_check.py"},
		OK: true, Duration: 5300 * time.Millisecond,
	}))
	if !strings.Contains(line, "💻 $ python3 scripts/version_check.py") || !strings.Contains(line, "5.30s") {
		t.Fatalf("shell 行格式不对: %q", line)
	}
}

// 实时工具事件应立刻追加一行，并继续等待下一条消息。
func TestToolEventMsgAppends(t *testing.T) {
	base := setupTempOS(t)
	m := newOSModel(base, mustLoad(t, base))
	m.width, m.height = 120, 40
	ch := make(chan tea.Msg, 1)
	nm, cmd := m.Update(toolEventMsg{
		ev: agent.ToolEvent{Name: "read", Args: map[string]any{"path": "index.md"}, OK: true, Duration: 2700 * time.Millisecond},
		ch: ch,
	})
	m = nm.(*osModel)
	if len(m.toolLog) != 1 || !strings.Contains(stripANSI(m.toolLog[0]), "📖 Read index.md") {
		t.Fatalf("工具事件未实时追加: %#v", m.toolLog)
	}
	if cmd == nil {
		t.Fatal("应继续等待下一条消息")
	}
}

// 输出框初始应为空：不再把仪表盘快照转储进去。
func TestOutputBoxStartsEmpty(t *testing.T) {
	base := setupTempOS(t)
	m := newOSModel(base, mustLoad(t, base))
	m.width, m.height = 120, 40
	out := stripANSI(m.renderOutputBox(m.width, 10))
	if out != "" {
		t.Fatalf("没有输入/输出前不应显示回复框, got %q", out)
	}
	if strings.Contains(out, "仪表盘快照") {
		t.Fatalf("输出框不应含仪表盘快照转储: %q", out)
	}
}

// 发送内容在上、forge 回复框在下（回复出现在发送内容下面）。
func TestUserEchoAboveForgeBox(t *testing.T) {
	base := setupTempOS(t)
	m := newOSModel(base, mustLoad(t, base))
	m.width, m.height = 120, 40
	m.out = append(m.out, "reply") // 有输出才渲染 forge 框
	m.appendUserEcho("hi")
	c := stripANSI(m.renderConsole(m.width))
	forgeIdx := strings.Index(c, "forge")
	echoIdx := strings.Index(c, "❯ hi")
	if forgeIdx < 0 || echoIdx < 0 || echoIdx > forgeIdx {
		t.Fatalf("发送内容应在 forge 回复框上方: forge=%d echo=%d", forgeIdx, echoIdx)
	}
}

// 规划器返回空计划 [] 时，runAICmd 应补问一次普通 chat，把文字回复带回来。
// agent 运行时：一次模型调用直接得到最终回复（不再走 aios 规划器 + 兜底）。
func TestRunAICmdAgentReply(t *testing.T) {
	base := setupTempOS(t)
	ai.RegisterBuiltins()

	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": "m",
			"choices": []map[string]any{
				{"message": map[string]any{"role": "assistant", "content": "你好！我是 HUSHFUSION Forge。"}},
			},
		})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("AI_CUSTOM_API_KEY", "test-key")
	t.Setenv("HUSHFUSION_AI_BASE_URL", srv.URL)

	msg := runAICmd(context.Background(), base, "你好", "custom", "m", "")()
	res, ok := msg.(aiResultMsg)
	if !ok {
		t.Fatalf("消息类型 = %T", msg)
	}
	if res.err != nil {
		t.Fatalf("agent 回合失败: %v", res.err)
	}
	if res.answer != "你好！我是 HUSHFUSION Forge。" {
		t.Fatalf("应带回最终回复, got %q", res.answer)
	}
	if calls != 1 {
		t.Fatalf("无工具调用时应只请求 1 次，got %d", calls)
	}
}

func TestWorkspaceConfigRoundTrip(t *testing.T) {
	base := t.TempDir()
	want := workspaceConfig{Provider: "custom", Model: "glm-5.3-flash", BaseURL: "https://x/v1", APIKey: "sk-test"}
	if err := saveWorkspaceConfig(base, want); err != nil {
		t.Fatal(err)
	}
	if got := loadWorkspaceConfig(base); got != want {
		t.Fatalf("config 往返不一致: %+v", got)
	}
	info, err := os.Stat(workspaceConfigPath(base))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("config 权限应为 0600, got %o", perm)
	}
}

func TestApplyWorkspaceConfigInjectsEnv(t *testing.T) {
	base := t.TempDir()
	_ = saveWorkspaceConfig(base, workspaceConfig{
		Provider: "custom", Model: "glm-5.3-flash",
		BaseURL: "https://x/v1", APIKey: "sk-test",
	})
	t.Setenv("HUSHFUSION_AI_PROVIDER", "")
	t.Setenv("HUSHFUSION_AI_MODEL", "")
	t.Setenv("HUSHFUSION_AI_BASE_URL", "")
	t.Setenv("AI_CUSTOM_API_KEY", "")
	applyWorkspaceConfig(base)
	if got := os.Getenv("HUSHFUSION_AI_PROVIDER"); got != "custom" {
		t.Fatalf("provider 未注入: %q", got)
	}
	if got := os.Getenv("HUSHFUSION_AI_MODEL"); got != "glm-5.3-flash" {
		t.Fatalf("model 未注入: %q", got)
	}
	if got := os.Getenv("HUSHFUSION_AI_BASE_URL"); got != "https://x/v1" {
		t.Fatalf("base url 未注入: %q", got)
	}
	if got := os.Getenv("AI_CUSTOM_API_KEY"); got != "sk-test" {
		t.Fatalf("key 未注入: %q", got)
	}
}

// 纯对话结果（Answer，无动作）应显示在终端，且不再追加空「AI 回合」。
func TestAIResultShowsAnswer(t *testing.T) {
	base := setupTempOS(t)
	m := newOSModel(base, mustLoad(t, base))
	nm, _ := m.Update(aiResultMsg{rep: &aios.Report{Goal: "hi", Answer: "你好！我是 forge。"}})
	m = nm.(*osModel)
	joined := stripANSI(strings.Join(m.out, "\n"))
	if !strings.Contains(joined, "你好！我是 forge。") {
		t.Fatalf("AI 回复应出现在输出: %q", joined)
	}
	m.width = 120
	if out := stripANSI(m.renderOutputBox(m.width, 10)); !strings.Contains(out, "forge") {
		t.Fatalf("标题应在框上: %q", out)
	}
	if strings.Contains(joined, "forge · 回复") {
		t.Fatalf("框内不应再重复标题: %q", joined)
	}
	if strings.Contains(joined, "AI 回合") {
		t.Fatalf("纯对话不应再追加空 AI 回合: %q", joined)
	}
}

// 首次构建的冻结快照应包含 skills / 经验 / 长期记忆 / 持久记忆，且后续轮次不重建。
func TestFrozenContextSnapshot(t *testing.T) {
	base := setupTempOS(t)
	m := newOSModel(base, mustLoad(t, base))
	m.mem.Set("project", "低温数据补全")
	ctx := m.buildFrozenContext()
	for _, want := range []string{"冻结上下文快照", "Skills", "沉淀经验", "持久记忆", "project"} {
		if !strings.Contains(ctx, want) {
			t.Fatalf("快照缺少 %q: %q", want, ctx)
		}
	}

	m.startThinking("目标一", "zai", "m")
	first := m.frozenContext
	if first == "" {
		t.Fatal("首次 AI 调用前应构建快照")
	}
	m.startThinking("目标二", "zai", "m")
	if m.frozenContext != first {
		t.Fatal("后续轮次不应重建快照（冻结）")
	}
}

// 冻结快照随会话落盘，--resume 后原样复用。
func TestFrozenContextPersisted(t *testing.T) {
	base := setupTempOS(t)
	m := newOSModel(base, mustLoad(t, base))
	m.frozenContext = "FROZEN-SNAPSHOT"
	m.persistSession()
	d, err := loadSession(base, m.sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Frozen != "FROZEN-SNAPSHOT" {
		t.Fatalf("冻结快照未落盘: %q", d.Frozen)
	}
}

// custom 启动即带出 provider 默认型号（不再写死 default）。
func TestNewOSModelUsesProviderDefaultModel(t *testing.T) {
	base := setupTempOS(t)
	t.Setenv("HUSHFUSION_AI_PROVIDER", "custom")
	t.Setenv("HUSHFUSION_AI_MODEL", "")
	m := newOSModel(base, mustLoad(t, base))
	if m.provider != "custom" {
		t.Fatalf("provider = %q", m.provider)
	}
	if m.model != "custom-model" {
		t.Fatalf("custom 未自动带出默认型号: %q", m.model)
	}
}

// custom/local 向导：输入框里的型号优先于列表选中项。
func TestPickWizardModelCustom(t *testing.T) {
	w := &modelWizard{provider: &ai.Profile{Name: "custom"}, step: wizStepModel, modelShown: []string{"custom-model"}, modelSel: 0}
	w.filter = newWizInput("搜索模型…")
	w.filter.SetValue("my-model")
	if got := pickWizardModel(w); got != "my-model" {
		t.Fatalf("custom 应接受输入的型号, got %q", got)
	}
	w.provider.Name = "zai"
	if got := pickWizardModel(w); got != "custom-model" {
		t.Fatalf("普通 provider 应以列表选中项为准, got %q", got)
	}
}

// 长期记忆：向量召回跨会话命中，且记录持久化到文件。
func TestLongTermMemoryRecall(t *testing.T) {
	base := t.TempDir()
	addMemRecord(base, "s1", "核聚变 线圈 设计", "D9857 的最优线圈配置与电磁场结构")
	addMemRecord(base, "s2", "前端 CSS 布局", "Flexbox 与 Grid 的对比")
	recs := recallMemories(base, "核聚变 磁体 线圈", 2)
	if len(recs) == 0 || !strings.Contains(recs[0].Goal, "核聚变") {
		t.Fatalf("召回结果不对: %+v", recs)
	}
	if got := loadMemRecords(base); len(got) != 2 {
		t.Fatalf("记录未持久化: %d", len(got))
	}
}

// 空闲心跳：做梦写 dreams.md、整理工作流写 memory/workflow.md 并写入记忆。
func TestIdleHeartbeatActions(t *testing.T) {
	base := setupTempOS(t)
	m := newOSModel(base, mustLoad(t, base))
	m.lastActivity = time.Now().Add(-time.Hour)
	m.lastIdleRun = time.Time{}
	m.lastGoal = "测试目标"
	m.toolLog = []string{"📖 Read index.md  0.01s"}
	m.out = append(m.out, "这是一段足够长的测试结论，用来做梦与整理工作流。")
	m.idlePulse()

	if _, err := os.Stat(filepath.Join(base, "dreams.md")); err != nil {
		t.Fatalf("dreams.md 未生成: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "memory", "workflow.md")); err != nil {
		t.Fatalf("workflow.md 未生成: %v", err)
	}
	if _, ok := m.mem.Get("workflow"); !ok {
		t.Fatal("workflow 未写入记忆")
	}
	if !strings.Contains(stripANSI(m.status), "空闲心跳") {
		t.Fatalf("状态未显示空闲心跳: %q", m.status)
	}
}

func TestIdleHeartbeatSkipsBusy(t *testing.T) {
	base := setupTempOS(t)
	m := newOSModel(base, mustLoad(t, base))
	m.busy = true
	m.lastActivity = time.Now().Add(-time.Hour)
	m.lastIdleRun = time.Time{}
	// setupTempOS 复制真实 os/，可能自带 dreams.md；先删掉再断言。
	_ = os.Remove(filepath.Join(base, "dreams.md"))
	m.idlePulse()
	if _, err := os.Stat(filepath.Join(base, "dreams.md")); err == nil {
		t.Fatal("忙时不应做梦")
	}
}

// 每轮沉淀经验：同一 skill 追加多条，且能被 agent 索引（下次启动复用）。
func TestEvolvedSkillAccumulates(t *testing.T) {
	base := setupTempOS(t)
	m := newOSModel(base, mustLoad(t, base))
	// setupTempOS 会复制真实 os/（可能已有经验），先清空再断言。
	_ = os.Remove(evolvedSkillPath(base))
	m.lastGoal = "测试目标"
	m.toolLog = []string{"📖 Read index.md  0.01s"}
	m.evolveSkill("第一条结论")
	m.evolveSkill("第二条结论")
	b, err := os.ReadFile(evolvedSkillPath(base))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, "name: evolved-experience") {
		t.Fatalf("frontmatter 缺失: %q", s)
	}
	if n := strings.Count(s, "\n## "); n != 2 {
		t.Fatalf("应有 2 条经验, got %d: %q", n, s)
	}
	if !strings.Contains(s, "第一条结论") || !strings.Contains(s, "第二条结论") {
		t.Fatalf("经验内容丢失: %q", s)
	}
	sk := agent.NewSkills([]string{filepath.Join(base, "skills")}, agent.NewKeywordMatcher())
	if _, ok := sk.Lookup("evolved-experience"); !ok {
		t.Fatal("经验 skill 未被 agent 索引")
	}
}

// 工具调用上限应显著高于 150（此前 6/12 太小）。
func TestAgentToolLimitsHigh(t *testing.T) {
	cfg := agent.DefaultConfig()
	if cfg.MaxToolTurns < 150 || cfg.MaxToolResults < 150 {
		t.Fatalf("工具调用上限应 ≥150, got turns=%d results=%d", cfg.MaxToolTurns, cfg.MaxToolResults)
	}
}

// 每条回复独立成框，不能与上一条合并。
func TestRepliesNotMerged(t *testing.T) {
	base := setupTempOS(t)
	m := newOSModel(base, mustLoad(t, base))
	m.width, m.height = 120, 40
	m.appendAnswerText("第一条回复")
	m.appendAnswerText("第二条回复")
	if len(m.replyBlocks) != 1 {
		t.Fatalf("第一条应进历史块, got %d", len(m.replyBlocks))
	}
	c := stripANSI(m.renderConsole(m.width))
	if got := strings.Count(c, "╭─ forge"); got != 2 {
		t.Fatalf("应有两个独立 forge 框, got %d: %q", got, c)
	}
	if strings.Index(c, "第一条回复") > strings.Index(c, "第二条回复") {
		t.Fatalf("回复顺序应保持时间序: %q", c)
	}
}

// 回复里的 Markdown 应渲染成样式文本，不再残留 ## / ** / ` / --- 等标记。
func TestMarkdownRendered(t *testing.T) {
	base := setupTempOS(t)
	m := newOSModel(base, mustLoad(t, base))
	m.width = 120
	m.appendAnswerText("## 标题\n**加粗**\n- 列表项\n`code`\n---")
	joined := stripANSI(strings.Join(m.out, "\n"))
	for _, raw := range []string{"##", "**", "`", "---"} {
		if strings.Contains(joined, raw) {
			t.Fatalf("不应残留 Markdown 标记 %q: %q", raw, joined)
		}
	}
	if !strings.Contains(joined, "标题") || !strings.Contains(joined, "加粗") || !strings.Contains(joined, "• 列表项") {
		t.Fatalf("Markdown 内容丢失: %q", joined)
	}
}

// 回显横线按框内宽生成，不能再被字节截断成 �…（`─` 是 3 字节）。
func TestUserEchoNoGarbledRule(t *testing.T) {
	base := setupTempOS(t)
	m := newOSModel(base, mustLoad(t, base))
	m.width, m.height = 120, 40
	m.appendUserEcho("hi")
	for _, s := range m.lastEcho {
		if strings.ContainsRune(s, '\uFFFD') {
			t.Fatalf("回显不应含非法 UTF-8: %q", s)
		}
		if strings.Contains(s, "…") {
			t.Fatalf("回显横线不应被裁剪: %q", s)
		}
	}
	if view := m.renderConsole(m.width); strings.ContainsRune(view, '\uFFFD') {
		t.Fatalf("渲染后不应出现 �: %q", view)
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
