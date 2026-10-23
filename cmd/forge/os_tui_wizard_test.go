package main

// os_tui_wizard_test.go —— /model 分步向导（provider → API key → 模型 → URL → 确认）。

import (
	"fmt"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/logos-42/hushfusion-forge/internal/ai"
)

// openWizard 是测试公共前置：起 TUI 模型并打开向导。
func openWizard(t *testing.T) *osModel {
	t.Helper()
	base := setupTempOS(t)
	m := newOSModel(base, mustLoad(t, base))
	nm, _ := m.openModelWizard()
	return nm.(*osModel)
}

func pressEnter(t *testing.T, m *osModel) *osModel {
	t.Helper()
	nm, _ := m.Update(keyCode(rune(tea.KeyEnter)))
	return nm.(*osModel)
}

func pressKey(t *testing.T, m *osModel, r rune) *osModel {
	t.Helper()
	nm, _ := m.Update(keyCode(r))
	return nm.(*osModel)
}

func pressEsc(t *testing.T, m *osModel) *osModel {
	t.Helper()
	nm, _ := m.Update(keyCode(rune(tea.KeyEscape)))
	return nm.(*osModel)
}

func TestModelWizardOpensAtProviderStep(t *testing.T) {
	m := openWizard(t)
	if m.state != stateModelWizard {
		t.Fatalf("应进入向导态, got %v", m.state)
	}
	if m.wiz == nil || m.wiz.step != wizStepProvider {
		t.Fatalf("应停在选 provider 步: %+v", m.wiz)
	}
	if len(m.wiz.providers) == 0 {
		t.Fatal("provider 列表不应为空")
	}
	// 只列已实现 chat_completions 的（空 APIMode 由 Validate 归一化为默认模式）
	for _, p := range m.wiz.providers {
		mode := p.APIMode
		if mode == "" {
			mode = ai.ModeChatCompletions
		}
		if mode != ai.ModeChatCompletions {
			t.Fatalf("%s 的模式 %s 未实现，不该出现在选项里", p.Name, p.APIMode)
		}
	}
}

// 完整流程：选 provider → API key(replace) → 选模型 → 填 URL → 确认 → 生效。
func TestModelWizardFullFlowAppliesKeyAndURL(t *testing.T) {
	m := openWizard(t)
	// 先登记还原（必须在向导写入 env 之前捕获原值）。
	restoreEnv := func(name string) {
		orig, ok := os.LookupEnv(name)
		t.Cleanup(func() {
			if ok {
				_ = os.Setenv(name, orig)
			} else {
				_ = os.Unsetenv(name)
			}
		})
	}
	restoreEnv("HUSHFUSION_AI_BASE_URL")

	// 上下键能移动光标（相对位移断言，不依赖列表长度）。
	before := m.wiz.cursor
	if len(m.wiz.providers) > 1 {
		m = pressKey(t, m, rune(tea.KeyDown))
		want := (before + 1) % len(m.wiz.providers)
		if m.wiz.cursor != want {
			t.Fatalf("↓ 应从 %d 移到 %d, got %d", before, want, m.wiz.cursor)
		}
		m = pressKey(t, m, rune(tea.KeyUp))
		if m.wiz.cursor != before {
			t.Fatalf("↑ 应回到 %d, got %d", before, m.wiz.cursor)
		}
	}

	// 选定 provider → key 步
	m = pressEnter(t, m)
	if m.wiz.step != wizStepKey {
		t.Fatalf("应进入 key 步（先填 API）, got %d", m.wiz.step)
	}
	prof := m.wiz.provider
	if prof == nil {
		t.Fatal("应已选定 provider")
	}
	if len(prof.EnvVars) > 0 {
		restoreEnv(prof.EnvVars[0]) // 写入前登记还原
	}

	// replace：输入新 key
	m = pressKey(t, m, 'r')
	if !m.wiz.keyEditing {
		t.Fatal("r 应进入 key 输入态")
	}
	const testKey = "sk-wizard-test-123456"
	m.wiz.key.SetValue(testKey)
	m = pressEnter(t, m) // key 输入确认 → model 步
	if m.wiz.step != wizStepModel {
		t.Fatalf("应进入 model 步, got %d", m.wiz.step)
	}
	if m.wiz.model != prof.DefaultModel {
		t.Fatalf("model 应默认选中 provider 默认值 %q, got %q", prof.DefaultModel, m.wiz.model)
	}
	// URL 在 provider 步预填
	if got := m.wiz.url.Value(); got != prof.BaseURL {
		t.Fatalf("url 应预填 %q, got %q", prof.BaseURL, got)
	}

	// model → url 步
	m = pressEnter(t, m)
	if m.wiz.step != wizStepURL {
		t.Fatalf("应进入 url 步, got %d", m.wiz.step)
	}
	const testURL = "http://127.0.0.1:9999/v1"
	m.wiz.url.SetValue(testURL)

	// url → 确认步
	m = pressEnter(t, m)
	if m.wiz.step != wizStepConfirm {
		t.Fatalf("应进入确认步, got %d", m.wiz.step)
	}
	body := m.wizardBody()
	if strings.Contains(body, testKey) {
		t.Fatalf("确认页不应出现明文 key: %s", body)
	}
	if !strings.Contains(body, "3456") { // 只露末 4 位供核对
		t.Fatalf("确认页应给出掩码尾部便于核对: %s", body)
	}

	// 确认 → 生效
	m = pressEnter(t, m)
	if m.state != statePanels {
		t.Fatalf("应用后应回控制台, got %v", m.state)
	}
	if m.wiz != nil {
		t.Fatal("应用后向导应清空")
	}
	if m.provider != prof.Name || m.model != prof.DefaultModel {
		t.Fatalf("provider/model 未生效: %s/%s", m.provider, m.model)
	}
	if len(prof.EnvVars) > 0 {
		if got := os.Getenv(prof.EnvVars[0]); got != testKey {
			t.Fatalf("key 应写入本进程环境 %s, got %q", prof.EnvVars[0], got)
		}
	}
	if got := os.Getenv("HUSHFUSION_AI_BASE_URL"); got != testURL {
		t.Fatalf("base url 应写入 HUSHFUSION_AI_BASE_URL, got %q", got)
	}
	// 展示框里绝不能出现明文 key（会话要落盘）
	for _, line := range m.out {
		if strings.Contains(line, testKey) {
			t.Fatalf("输出泄露明文 key: %q", line)
		}
	}
}

// 模型步默认选中 provider 的 DefaultModel（内置列表里置顶可选）。
func TestModelWizardDefaultsToProviderModel(t *testing.T) {
	m := openWizard(t)
	m = pressEnter(t, m) // provider → key
	m = pressEnter(t, m) // keep → model
	if m.wiz == nil || m.wiz.step != wizStepModel {
		t.Fatalf("应在 model 步, got %+v", m.wiz)
	}
	prof := m.wiz.provider
	if prof == nil || prof.DefaultModel == "" {
		t.Skip("当前 provider 无默认模型")
	}
	if m.wiz.model != prof.DefaultModel {
		t.Fatalf("默认应选中 %q, got %q", prof.DefaultModel, m.wiz.model)
	}
	if len(m.wiz.modelShown) == 0 {
		t.Fatal("模型候选不应为空")
	}
	if m.wiz.modelShown[m.wiz.modelSel] != prof.DefaultModel {
		t.Fatalf("光标应停在默认模型 %q, got %q", prof.DefaultModel, m.wiz.modelShown[m.wiz.modelSel])
	}
}

// 模型步支持关键词筛选；无匹配时 Enter 可把关键词当自定义模型 id。
func TestModelWizardModelFilter(t *testing.T) {
	m := openWizard(t)
	m = pressEnter(t, m) // provider → key
	m = pressEnter(t, m) // keep → model
	all := len(m.wiz.modelShown)

	m.wiz.filter.SetValue("zzz-no-such-model")
	m.applyModelFilter()
	if len(m.wiz.modelShown) != 0 {
		t.Fatalf("无匹配时应为空, got %v", m.wiz.modelShown)
	}
	// 无匹配时 Enter → 用关键词本身作为模型 id
	m = pressEnter(t, m)
	if m.wiz.step != wizStepURL || m.wiz.model != "zzz-no-such-model" {
		t.Fatalf("无匹配 Enter 应把关键词当模型 id, step=%d model=%q", m.wiz.step, m.wiz.model)
	}
	// 回退回来清空筛选 → 恢复全量
	_ = pressEsc(t, m)
	m.wiz.filter.SetValue("")
	m.applyModelFilter()
	if len(m.wiz.modelShown) != all {
		t.Fatalf("清空筛选应恢复 %d 条, got %d", all, len(m.wiz.modelShown))
	}
}

// keep：不改变现有环境变量。
func TestModelWizardKeepKeyPreservesEnv(t *testing.T) {
	m := openWizard(t)
	m = pressEnter(t, m) // provider → key
	prof := m.wiz.provider
	if prof == nil || len(prof.EnvVars) == 0 {
		t.Skip("provider 无 EnvVars")
	}
	env := prof.EnvVars[0]
	orig, had := os.LookupEnv(env)
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(env, orig)
		} else {
			_ = os.Unsetenv(env)
		}
	})
	_ = os.Setenv(env, "orig-key-value")

	m = pressEnter(t, m) // key 步 Enter = keep → model
	m = pressEnter(t, m) // model → url
	m = pressEnter(t, m) // url → confirm
	m = pressEnter(t, m) // apply
	if got := os.Getenv(env); got != "orig-key-value" {
		t.Fatalf("keep 应保持原值, got %q", got)
	}
}

// clear：清除该 provider 的 key。
func TestModelWizardClearKeyUnsetsEnv(t *testing.T) {
	m := openWizard(t)
	m = pressEnter(t, m) // provider → key
	prof := m.wiz.provider
	if prof == nil || len(prof.EnvVars) == 0 {
		t.Skip("provider 无 EnvVars")
	}
	env := prof.EnvVars[0]
	orig, had := os.LookupEnv(env)
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(env, orig)
		} else {
			_ = os.Unsetenv(env)
		}
	})
	_ = os.Setenv(env, "orig-key-value")

	m = pressKey(t, m, 'c') // clear → model
	m = pressEnter(t, m)    // model → url
	m = pressEnter(t, m)    // url → confirm
	m = pressEnter(t, m)    // apply
	if got := os.Getenv(env); got != "" {
		t.Fatalf("clear 应清除 key, got %q", got)
	}
}

// Esc 在输入步是"上一步"，在 provider 步是"取消"。
func TestModelWizardEscGoesBackThenCancels(t *testing.T) {
	m := openWizard(t)
	m = pressEnter(t, m) // provider → key
	if m.wiz.step != wizStepKey {
		t.Fatalf("应在 key 步, got %d", m.wiz.step)
	}
	m = pressEnter(t, m) // keep → model
	if m.wiz.step != wizStepModel {
		t.Fatalf("应在 model 步, got %d", m.wiz.step)
	}
	m = pressEsc(t, m)
	if m.wiz == nil || m.wiz.step != wizStepKey {
		t.Fatalf("Esc 应退回 key 步, got %+v", m.wiz)
	}
	m = pressEsc(t, m)
	if m.wiz == nil || m.wiz.step != wizStepProvider {
		t.Fatalf("再 Esc 应退回 provider 步")
	}
	m = pressEsc(t, m)
	if m.state != statePanels || m.wiz != nil {
		t.Fatalf("provider 步 Esc 应取消并回控制台: state=%v wiz=%v", m.state, m.wiz)
	}
}

// 空 URL ⇒ 清除覆盖（keep 时不动 key）。
func TestModelWizardEmptyURLClearsOverride(t *testing.T) {
	orig, had := os.LookupEnv("HUSHFUSION_AI_BASE_URL")
	t.Cleanup(func() {
		if had {
			_ = os.Setenv("HUSHFUSION_AI_BASE_URL", orig)
		} else {
			_ = os.Unsetenv("HUSHFUSION_AI_BASE_URL")
		}
	})
	m := openWizard(t)
	m = pressEnter(t, m)   // provider → key
	m = pressEnter(t, m)   // keep → model
	m = pressEnter(t, m)   // model → url
	m.wiz.url.SetValue("") // 空 URL ⇒ 清除覆盖
	m = pressEnter(t, m)   // url → confirm
	m = pressEnter(t, m)   // apply
	if got := os.Getenv("HUSHFUSION_AI_BASE_URL"); got != "" {
		t.Fatalf("空 URL 应清掉覆盖, got %q", got)
	}
}

// 模型列表支持滚动：窗口跟随选择、PgUp/PgDn 翻页、Home/End 首尾。
func TestModelWizardModelScroll(t *testing.T) {
	m := openWizard(t)
	m = pressEnter(t, m) // provider → key
	m = pressEnter(t, m) // keep → model
	w := m.wiz
	// 造 200 条长列表验证窗口/翻页（真实 /models 常返回上百条）。
	w.modelShown = make([]string, 0, 200)
	for i := 0; i < 200; i++ {
		w.modelShown = append(w.modelShown, fmt.Sprintf("model-%03d", i))
	}
	w.modelSel = 0
	rows := m.wizListRows()
	if rows < 3 {
		t.Fatalf("列表行数过小: %d", rows)
	}
	assertVisible := func() {
		start, end := windowAround(w.modelSel, len(w.modelShown), rows)
		if w.modelSel < start || w.modelSel >= end {
			t.Fatalf("选中项 %d 不在可见窗口 [%d,%d)", w.modelSel, start, end)
		}
	}
	assertVisible()

	m = pressKey(t, m, rune(tea.KeyPgDown))
	if w.modelSel != rows {
		t.Fatalf("PgDn 应下移 %d 行, got %d", rows, w.modelSel)
	}
	assertVisible()

	m = pressKey(t, m, rune(tea.KeyEnd))
	if w.modelSel != len(w.modelShown)-1 {
		t.Fatalf("End 应到末尾, got %d", w.modelSel)
	}
	assertVisible()

	m = pressKey(t, m, rune(tea.KeyHome))
	if w.modelSel != 0 {
		t.Fatalf("Home 应到顶部, got %d", w.modelSel)
	}
	assertVisible()
}

func TestKeyPreviewNeverLeaksPlaintext(t *testing.T) {
	p := &ai.Profile{Name: "t", EnvVars: []string{"T_KEY"}}
	const secret = "sk-super-secret-value"
	got := keyPreview(secret, p)
	if strings.Contains(got, secret) {
		t.Fatalf("预览泄露明文: %q", got)
	}
	if !strings.HasSuffix(strings.SplitN(got, "（", 2)[0], "alue") {
		t.Fatalf("应保留尾部便于核对: %q", got)
	}
	// 空 key：有 env 说沿用，没 env 说会失败（都要诚实）
	if s := keyPreview("", &ai.Profile{Name: "t", EnvVars: []string{"NO_SUCH_KEY_X"}}); !strings.Contains(s, "调用会失败") {
		t.Fatalf("未配 key 应明说会失败: %q", s)
	}
}
