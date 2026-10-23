package main

// os_tui_wizard_test.go —— /model 分步向导（provider → model → key → URL → 确认）。

import (
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

// 完整流程：选 provider → model → 填 key → 填 URL → 确认 → 生效（key 进本进程 env）。
func TestModelWizardFullFlowAppliesKeyAndURL(t *testing.T) {
	m := openWizard(t)
	// 先登记还原（必须在向导写入 env 之前捕获原值，否则还原的是被污染的值）。
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

	// 上下键能移动光标（先下再上回到原位）。
	// 注意：光标初始停在「当前激活 provider」的索引，列表按名字排序、数量随
	// 内置目录增长而变，因此这里用相对位移断言，不硬编码绝对索引。
	before := m.wiz.cursor
	if len(m.wiz.providers) > 1 {
		nm, _ := m.Update(keyCode(rune(tea.KeyDown)))
		m = nm.(*osModel)
		want := (before + 1) % len(m.wiz.providers)
		if m.wiz.cursor != want {
			t.Fatalf("↓ 应从 %d 移到 %d, got %d", before, want, m.wiz.cursor)
		}
		nm, _ = m.Update(keyCode(rune(tea.KeyUp)))
		m = nm.(*osModel)
		if m.wiz.cursor != before {
			t.Fatalf("↑ 应回到 %d, got %d", before, m.wiz.cursor)
		}
	}

	// 选定 provider
	m = pressEnter(t, m)
	if m.wiz.step != wizStepModel {
		t.Fatalf("应进入 model 步, got %d", m.wiz.step)
	}
	prof := m.wiz.provider
	if prof == nil {
		t.Fatal("应已选定 provider")
	}
	if len(prof.EnvVars) > 0 {
		restoreEnv(prof.EnvVars[0]) // 同理：写入前登记还原
	}
	if got := m.wiz.model.Value(); got != prof.DefaultModel {
		t.Fatalf("model 应预填 provider 默认值 %q, got %q", prof.DefaultModel, got)
	}
	// URL 也应预填 profile 值（可改）
	if got := m.wiz.url.Value(); got != prof.BaseURL {
		t.Fatalf("url 应预填 %q, got %q", prof.BaseURL, got)
	}

	// model → key 步
	m = pressEnter(t, m)
	if m.wiz.step != wizStepKey {
		t.Fatalf("应进入 key 步, got %d", m.wiz.step)
	}
	const testKey = "sk-wizard-test-123456"
	m.wiz.key.SetValue(testKey)

	// key → url 步
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

// model 留空时自动匹配：清空 model 走完 key/url，到确认步应回填 provider 默认模型。
func TestModelWizardAutoMatchesEmptyModel(t *testing.T) {
	m := openWizard(t)
	m = pressEnter(t, m) // provider → model
	if m.wiz == nil || m.wiz.step != wizStepModel {
		t.Fatalf("应在 model 步, got %+v", m.wiz)
	}
	prof := m.wiz.provider
	if prof == nil || prof.DefaultModel == "" {
		t.Skip("当前 provider 无默认模型，跳过（避免触发真实 /models 网络请求）")
	}
	m.wiz.model.SetValue("") // 模拟用户不填 model
	m = pressEnter(t, m)     // model → key
	m = pressEnter(t, m)     // key → url
	m = pressEnter(t, m)     // url → confirm（URL 步 Enter 触发自动匹配）
	if got := m.wiz.model.Value(); got != prof.DefaultModel {
		t.Fatalf("空 model 应自动匹配为 %q, got %q", prof.DefaultModel, got)
	}
}

// Esc 在输入步是"上一步"，在 provider 步是"取消"。
func TestModelWizardEscGoesBackThenCancels(t *testing.T) {
	m := openWizard(t)
	m = pressEnter(t, m) // → model 步
	m = pressEnter(t, m) // → key 步
	if m.wiz.step != wizStepKey {
		t.Fatalf("应在 key 步, got %d", m.wiz.step)
	}
	nm, _ := m.Update(keyCode(rune(tea.KeyEscape)))
	m = nm.(*osModel)
	if m.wiz == nil || m.wiz.step != wizStepModel {
		t.Fatalf("Esc 应退回 model 步, got %+v", m.wiz)
	}
	nm, _ = m.Update(keyCode(rune(tea.KeyEscape)))
	m = nm.(*osModel)
	if m.wiz == nil || m.wiz.step != wizStepProvider {
		t.Fatalf("再 Esc 应退回 provider 步")
	}
	nm, _ = m.Update(keyCode(rune(tea.KeyEscape)))
	m = nm.(*osModel)
	if m.state != statePanels || m.wiz != nil {
		t.Fatalf("provider 步 Esc 应取消并回控制台: state=%v wiz=%v", m.state, m.wiz)
	}
}

// 空 key 时确认页应说明"沿用环境变量"，不做任何写入。
func TestModelWizardEmptyKeyKeepsEnv(t *testing.T) {
	orig, had := os.LookupEnv("HUSHFUSION_AI_BASE_URL")
	t.Cleanup(func() {
		if had {
			_ = os.Setenv("HUSHFUSION_AI_BASE_URL", orig)
		} else {
			_ = os.Unsetenv("HUSHFUSION_AI_BASE_URL")
		}
	})
	m := openWizard(t)
	m = pressEnter(t, m)   // model
	m = pressEnter(t, m)   // key（留空）
	m = pressEnter(t, m)   // url
	m.wiz.url.SetValue("") // 空 URL ⇒ 清除覆盖
	m = pressEnter(t, m)   // 确认
	m = pressEnter(t, m)   // 应用
	if got := os.Getenv("HUSHFUSION_AI_BASE_URL"); got != "" {
		t.Fatalf("空 URL 应清掉覆盖, got %q", got)
	}
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
