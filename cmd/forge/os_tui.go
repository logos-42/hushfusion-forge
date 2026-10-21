package main

// forge os tui —— 基于 bubbletea v2（charm.land/bubbletea/v2）+ lipgloss/v2 + bubbles/v2
// 的终端 UI。表现层完全独立于 osview/oskernel/aios（这三个库不动），只通过它们的
// 公共 API 读写：osview 读状态、oskernel 经 doPropose/doOpp/doDecide 写、aios 跑 AI。
//
// 状态机：面板(panels) / 表单(form) / AI思考中(thinking,带 spinner) / 报告(report)。
// 焦点态：面板态焦点=所选面板；表单态焦点=当前字段。AI 调用以 tea.Cmd 异步执行，
// 期间 spinner 持续转动；结果以消息回传，再切到报告屏。

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/logos-42/hushfusion-forge/internal/aios"
	"github.com/logos-42/hushfusion-forge/internal/oskernel"
	"github.com/logos-42/hushfusion-forge/internal/osview"
)

const (
	ansiReset = "\033[0m"
	green     = "\033[32m"
	yellow    = "\033[33m"
)

type appState int

const (
	statePanels appState = iota
	stateForm
	stateThinking
	stateReport
)

type panel struct {
	name     string
	lines    func() []string
	expanded bool
	scroll   int
}

// ---------------------------------------------------------------------------
// 模型
// ---------------------------------------------------------------------------

type osModel struct {
	base   string
	snap   *osview.Snapshot
	panels []panel
	sel    int
	state  appState

	form    *formModel
	spinner spinner.Model

	thinkingGoal string
	report       *aios.Report
	reportErr    error

	status   string // 瞬时底部消息
	showHelp bool   // ? 切换的按键帮助覆盖层

	width  int
	height int
	styles uiStyles
}

type uiStyles struct {
	appTitle    lipgloss.Style
	title       lipgloss.Style
	label       lipgloss.Style
	value       lipgloss.Style
	panel       lipgloss.Style
	panelActive lipgloss.Style
	footer      lipgloss.Style
	hint        lipgloss.Style
	spinner     lipgloss.Style
	err         lipgloss.Style
	ok          lipgloss.Style
	input       lipgloss.Style
}

type formModel struct {
	kind   string
	title  string
	labels []string
	inputs []textinput.Model
	focus  int
}

type aiResultMsg struct {
	rep *aios.Report
	err error
}

// keyMap 用 bubbles/key 声明式绑定（学 Crush keys.go），帮助栏由绑定自动生成。
type keyMap struct {
	quit   key.Binding
	reload key.Binding
	check  key.Binding
	up     key.Binding
	down   key.Binding
	expand key.Binding
	cancel key.Binding
	task   key.Binding
	opp    key.Binding
	decide key.Binding
	ai     key.Binding
	help   key.Binding
}

func defaultKeyMap() keyMap {
	return keyMap{
		quit:   key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q/ctrl+c", "退出")),
		reload: key.NewBinding(key.WithKeys("L", "l"), key.WithHelp("L", "重载 OS 视图")),
		check:  key.NewBinding(key.WithKeys("r", "R"), key.WithHelp("r", "跑 oscheck 门")),
		up:     key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "上移选择")),
		down:   key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "下移选择")),
		expand: key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "展开/提交")),
		cancel: key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "取消录入")),
		task:   key.NewBinding(key.WithKeys("n", "N"), key.WithHelp("n", "新任务")),
		opp:    key.NewBinding(key.WithKeys("o", "O"), key.WithHelp("o", "新机会")),
		decide: key.NewBinding(key.WithKeys("d", "D"), key.WithHelp("d", "新决策")),
		ai:     key.NewBinding(key.WithKeys("a", "A"), key.WithHelp("a", "AI 目标")),
		help:   key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "按键帮助")),
	}
}

var km = defaultKeyMap()

func newOSModel(base string, snap *osview.Snapshot) *osModel {
	m := &osModel{
		base:   base,
		snap:   snap,
		panels: buildPanels(snap),
		state:  statePanels,
		width:  80,
		height: 24,
	}
	m.initStyles()
	m.spinner = spinner.New(
		spinner.WithSpinner(spinner.Dot),
		spinner.WithStyle(m.styles.spinner),
	)
	return m
}

func (m *osModel) initStyles() {
	m.styles = uiStyles{
		appTitle:    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("231")),
		title:       lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("51")),
		label:       lipgloss.NewStyle().Foreground(lipgloss.Color("39")),
		value:       lipgloss.NewStyle().Foreground(lipgloss.Color("252")),
		panel:       lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("241")).Padding(0, 1),
		panelActive: lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("51")).Padding(0, 1),
		footer:      lipgloss.NewStyle().Reverse(true).Foreground(lipgloss.Color("252")),
		hint:        lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("244")),
		spinner:     lipgloss.NewStyle().Foreground(lipgloss.Color("51")),
		err:         lipgloss.NewStyle().Foreground(lipgloss.Color("203")),
		ok:          lipgloss.NewStyle().Foreground(lipgloss.Color("42")),
		input:       lipgloss.NewStyle(),
	}
}

// ---------------------------------------------------------------------------
// bubbletea 接口
// ---------------------------------------------------------------------------

func (m *osModel) Init() tea.Cmd { return nil }

func (m *osModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case spinner.TickMsg:
		if m.state == stateThinking {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}
		return m, nil
	case aiResultMsg:
		m.state = stateReport
		m.report = msg.rep
		m.reportErr = msg.err
		if msg.err != nil {
			m.status = m.styles.err.Render("AI 运行失败（见报告屏）")
		}
		m.reload()
		return m, nil
	case tea.KeyPressMsg:
		switch m.state {
		case stateThinking:
			if key.Matches(msg, km.quit) {
				return m, tea.Quit
			}
			return m, nil
		case stateReport:
			if key.Matches(msg, km.quit) {
				return m, tea.Quit
			}
			// 任意其它键 → 返回面板
			m.state = statePanels
			m.reload()
			m.status = ""
			return m, nil
		case stateForm:
			if key.Matches(msg, km.cancel) {
				m.state = statePanels
				m.status = m.styles.err.Render("已取消录入")
				m.form = nil
				return m, nil
			}
			if key.Matches(msg, km.quit) {
				return m, tea.Quit
			}
			return m.updateForm(msg)
		default: // statePanels
			switch {
			case key.Matches(msg, km.quit):
				return m, tea.Quit
			case key.Matches(msg, km.help):
				m.showHelp = !m.showHelp
				return m, nil
			case key.Matches(msg, km.reload):
				m.reload()
				m.status = m.styles.ok.Render("已重新加载")
			case key.Matches(msg, km.check):
				m.status = runCheckInline()
				m.reload()
			case key.Matches(msg, km.up):
				if m.panels[m.sel].expanded {
					m.panels[m.sel].scroll--
				} else if m.sel > 0 {
					m.sel--
				}
			case key.Matches(msg, km.down):
				if m.panels[m.sel].expanded {
					m.panels[m.sel].scroll++
				} else if m.sel < len(m.panels)-1 {
					m.sel++
				}
			case key.Matches(msg, km.expand):
				m.panels[m.sel].expanded = !m.panels[m.sel].expanded
				m.panels[m.sel].scroll = 0
			case key.Matches(msg, km.task):
				m.openForm("task")
			case key.Matches(msg, km.opp):
				m.openForm("opp")
			case key.Matches(msg, km.decide):
				m.openForm("decide")
			case key.Matches(msg, km.ai):
				m.openForm("ai")
			}
			return m, nil
		}
	}
	return m, nil
}

func (m *osModel) View() tea.View {
	w := m.width
	if w < 40 {
		w = 80
	}
	var b strings.Builder
	b.WriteString(m.styles.appTitle.Render(" HUSHFUSION OS · 宏内核 TUI ") + "\n\n")
	switch m.state {
	case stateForm:
		b.WriteString(m.renderForm())
	case stateThinking:
		b.WriteString(m.renderThinking())
	case stateReport:
		b.WriteString(m.renderReport())
	default:
		if m.showHelp {
			b.WriteString(m.renderHelp())
		} else {
			b.WriteString(m.renderPanels())
		}
	}
	v := tea.NewView(b.String())
	v.AltScreen = true
	return v
}

// ---------------------------------------------------------------------------
// 表单
// ---------------------------------------------------------------------------

func (m *osModel) openForm(kind string) {
	fm := &formModel{kind: kind}
	switch kind {
	case "task":
		fm.title = "新任务（proposed）"
		fm.labels = []string{"ID (T-XX)", "标题", "I (1-5)", "E (1-5)", "R (1-5)", "C (1-5)", "依赖/阻塞 (可空)"}
	case "opp":
		fm.title = "新机会"
		fm.labels = []string{"机会", "来源", "研究方向匹配", "下一步"}
	case "decide":
		fm.title = "新决策"
		fm.labels = []string{"决策", "依据（证据路径，禁止「我觉得」）"}
	case "ai":
		fm.title = "AI 目标（经内核受控系统调用提议）"
		fm.labels = []string{"目标", "provider（默认 zai）", "model（空=provider 默认）"}
	}
	fm.inputs = make([]textinput.Model, len(fm.labels))
	for i := range fm.inputs {
		ti := textinput.New()
		ti.Placeholder = fm.labels[i]
		fm.inputs[i] = ti
	}
	if kind == "ai" {
		p := os.Getenv("HUSHFUSION_AI_PROVIDER")
		if p == "" {
			p = "zai"
		}
		fm.inputs[1].SetValue(p)
	}
	fm.inputs[0].Focus()
	m.form = fm
	m.state = stateForm
}

func (m *osModel) updateForm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch key {
	case "esc":
		m.state = statePanels
		m.status = m.styles.err.Render("已取消录入")
		m.form = nil
		return m, nil
	case "enter":
		if m.form.focus < len(m.form.inputs)-1 {
			m.form.inputs[m.form.focus].Blur()
			m.form.focus++
			m.form.inputs[m.form.focus].Focus()
			return m, nil
		}
		return m.submitForm()
	case "tab":
		m.form.inputs[m.form.focus].Blur()
		m.form.focus = (m.form.focus + 1) % len(m.form.inputs)
		m.form.inputs[m.form.focus].Focus()
		return m, nil
	case "shift+tab":
		m.form.inputs[m.form.focus].Blur()
		m.form.focus = (m.form.focus - 1 + len(m.form.inputs)) % len(m.form.inputs)
		m.form.inputs[m.form.focus].Focus()
		return m, nil
	default:
		var cmd tea.Cmd
		m.form.inputs[m.form.focus], cmd = m.form.inputs[m.form.focus].Update(msg)
		return m, cmd
	}
}

// submitForm 把表单映射成内核写入；AI 表单则进入异步思考态。
func (m *osModel) submitForm() (tea.Model, tea.Cmd) {
	v := func(i int) string { return strings.TrimSpace(m.form.inputs[i].Value()) }
	switch m.form.kind {
	case "task":
		id, title, deps := v(0), v(1), v(6)
		if id == "" || title == "" {
			m.status = m.styles.err.Render("ID 与标题必填")
			return m, nil
		}
		parse := func(s string) int { n, _ := strconv.Atoi(s); return n }
		i, e, r, c := parse(v(2)), parse(v(3)), parse(v(4)), parse(v(5))
		if i < 1 || i > 5 || e < 1 || e > 5 || r < 1 || r > 5 || c < 1 || c > 5 {
			m.status = m.styles.err.Render("I/E/R/C 必须 1..5")
			return m, nil
		}
		if _, err := doPropose(m.base, id, i, e, r, c, title, deps); err != nil {
			m.status = m.styles.err.Render("录入失败: " + err.Error())
			return m, nil
		}
		m.status = m.styles.ok.Render(fmt.Sprintf("任务 %s 已录入（P=%d）", id, i*e*r/c))
	case "opp":
		if v(0) == "" {
			m.status = m.styles.err.Render("机会必填")
			return m, nil
		}
		if err := doOpp(m.base, v(0), v(1), v(2), v(3), "proposed", time.Now().Format("2006-01-02")); err != nil {
			m.status = m.styles.err.Render("录入失败: " + err.Error())
			return m, nil
		}
		m.status = m.styles.ok.Render("机会已录入")
	case "decide":
		if v(0) == "" {
			m.status = m.styles.err.Render("决策必填")
			return m, nil
		}
		if _, err := doDecide(m.base, v(0), v(1), "—", "—", "active"); err != nil {
			m.status = m.styles.err.Render("录入失败: " + err.Error())
			return m, nil
		}
		m.status = m.styles.ok.Render("决策已录入")
	case "ai":
		goal, provider, model := v(0), v(1), v(2)
		if goal == "" {
			m.status = m.styles.err.Render("目标必填")
			return m, nil
		}
		if provider == "" {
			provider = os.Getenv("HUSHFUSION_AI_PROVIDER")
		}
		if provider == "" {
			provider = "zai"
		}
		m.state = stateThinking
		m.thinkingGoal = goal
		m.report, m.reportErr = nil, nil
		m.status = ""
		return m, tea.Batch(m.spinner.Tick, runAICmd(m.base, goal, provider, model))
	}
	m.reload()
	m.form = nil
	m.state = statePanels
	return m, nil
}

// runAICmd 以 tea.Cmd 异步跑 AI 粘合：bubbletea 在独立 goroutine 执行，期间 spinner
// 持续转动；结果以 aiResultMsg 回传。aios 库本身不动。
func runAICmd(base, goal, provider, model string) tea.Cmd {
	return func() tea.Msg {
		agent := aios.NewAgent(oskernel.New(base), provider, model, os.Getenv("HUSHFUSION_AI_BASE_URL"))
		rep, err := agent.Run(context.Background(), goal, false)
		return aiResultMsg{rep: rep, err: err}
	}
}

func (m *osModel) reload() {
	if s, err := osview.Load(m.base); err == nil {
		m.snap = s
		m.panels = buildPanels(s)
	}
}

// ---------------------------------------------------------------------------
// 渲染
// ---------------------------------------------------------------------------

func (m *osModel) renderPanels() string {
	w := m.width
	if w < 40 {
		w = 80
	}
	avail := m.height - 4
	if avail < 6 {
		avail = 6
	}
	expIdx := -1
	for i, p := range m.panels {
		if p.expanded {
			expIdx = i
		}
	}
	var blocks []string
	used := 0
	for i, p := range m.panels {
		ls := p.lines()
		var body []string
		if i == expIdx {
			room := avail - used - 2
			if room < 1 {
				room = 1
			}
			start := clamp(p.scroll, 0, max(0, len(ls)-room))
			for j := start; j < len(ls) && len(body) < room; j++ {
				body = append(body, ls[j])
			}
		} else if len(ls) > 0 {
			body = []string{ls[0]}
		} else {
			body = []string{"（空）"}
		}
		head := m.styles.label.Render("▌ " + p.name)
		content := append([]string{head}, body...)
		st := m.styles.panel
		if i == m.sel {
			st = m.styles.panelActive
		}
		rendered := st.Width(w - 2).Render(strings.Join(content, "\n"))
		blocks = append(blocks, rendered)
		used += strings.Count(rendered, "\n") + 1
		if used >= avail {
			break
		}
	}
	return lipgloss.JoinVertical(lipgloss.Left, blocks...) + "\n" + m.renderFooter()
}

func (m *osModel) renderFooter() string {
	hint := "↑↓ 选择 · Enter 展开 · n/o/d 录入 · a AI目标 · r check · L 重载 · ? 帮助 · q 退出"
	var s string
	if m.status != "" {
		s = m.status + "   " + m.styles.hint.Render(hint)
	} else {
		s = m.styles.hint.Render(hint)
	}
	return m.styles.footer.Width(m.width-2).Render(s) + "\n"
}

// renderHelp 由 keyMap 的声明式绑定自动生成（? 切换）。
func (m *osModel) renderHelp() string {
	w := m.width
	if w < 40 {
		w = 80
	}
	var b strings.Builder
	b.WriteString(m.styles.appTitle.Render(" 按键帮助 ") + "\n\n")
	for _, bd := range []key.Binding{km.up, km.down, km.expand, km.task, km.opp, km.decide, km.ai, km.check, km.reload, km.help, km.cancel, km.quit} {
		if !bd.Enabled() {
			continue
		}
		h := bd.Help()
		b.WriteString(fmt.Sprintf("  %-10s %s\n", h.Key, h.Desc))
	}
	b.WriteString("\n" + m.styles.hint.Render("表单内：方向键/字符输入，enter 提交或下一项，esc 取消。按 ? 返回。"))
	return b.String()
}

func (m *osModel) renderForm() string {
	var b strings.Builder
	b.WriteString(m.styles.title.Render("录入："+m.form.title) + "\n\n")
	for i, inp := range m.form.inputs {
		label := m.form.labels[i] + "："
		if i == m.form.focus {
			b.WriteString(m.styles.label.Render("▶ ") + label + "\n")
		} else {
			b.WriteString("  " + label + "\n")
		}
		b.WriteString(inp.View() + "\n")
	}
	b.WriteString("\n" + m.styles.hint.Render("Enter=下一字段/提交 · Tab 切换 · Esc=取消"))
	if m.status != "" {
		b.WriteString("\n" + m.status)
	}
	return b.String()
}

func (m *osModel) renderThinking() string {
	var b strings.Builder
	b.WriteString(m.styles.title.Render("AI 思考中…") + "\n\n")
	b.WriteString("  " + m.spinner.View() + " " + m.thinkingGoal + "\n\n")
	b.WriteString(m.styles.hint.Render("正在经内核受控系统调用执行（可 q 退出，报告随后呈现）"))
	return b.String()
}

func (m *osModel) renderReport() string {
	var b strings.Builder
	b.WriteString(m.styles.title.Render("AI 粘合报告") + "\n\n")
	if m.reportErr != nil {
		b.WriteString(m.styles.err.Render("运行失败: "+m.reportErr.Error()) + "\n\n")
		b.WriteString(m.styles.hint.Render("任意键返回 · q 退出"))
		return b.String()
	}
	if m.report != nil {
		b.WriteString("目标: " + m.report.Goal + "\n\n")
		b.WriteString("计划（准入判定）:\n")
		for i, p := range m.report.Plan {
			mark := m.styles.ok.Render("✓允许")
			if !p.Allowed {
				mark = m.styles.err.Render("✗拒绝: " + p.Reason)
			}
			b.WriteString(fmt.Sprintf("  %d. %s %s — %s\n", i+1, p.Syscall, mark, argsSummary(p.Args)))
		}
		if len(m.report.Results) > 0 {
			b.WriteString("\n执行结果（经内核）:\n")
			for _, r := range m.report.Results {
				if r.OK {
					b.WriteString(m.styles.ok.Render("  ✓ ") + r.Syscall + " " + r.Message + "\n")
				} else {
					b.WriteString(m.styles.err.Render("  ✗ ") + r.Syscall + " " + r.Err + "\n")
				}
			}
		}
		for _, n := range m.report.Notes {
			b.WriteString("\n> " + n + "\n")
		}
	}
	b.WriteString("\n" + m.styles.hint.Render("任意键返回 · q 退出"))
	return b.String()
}

// ---------------------------------------------------------------------------
// 入口（非 TTY 退化成仪表盘）
// ---------------------------------------------------------------------------

func cmdOSTUI() int {
	base := osBaseDir()
	snap, err := osview.Load(base)
	if err != nil {
		return fail("%v", err)
	}
	if st, _ := os.Stdout.Stat(); (st.Mode() & os.ModeCharDevice) == 0 {
		fmt.Print(snap.Dashboard())
		return 0
	}
	m := newOSModel(base, snap)
	p := tea.NewProgram(m)
	if _, err := p.Run(); err != nil {
		return fail("os tui: %v", err)
	}
	return 0
}

// ---------------------------------------------------------------------------
// 复用的展示/工具（与旧版一致，供 buildPanels / runCheckInline 使用）
// ---------------------------------------------------------------------------

func buildPanels(snap *osview.Snapshot) []panel {
	return []panel{
		{name: "状态 / 北极星", lines: func() []string {
			out := []string{}
			if snap.CurrentState != "" {
				out = append(out, snap.CurrentState)
			}
			out = append(out, truncateLine(snap.NorthStar, 80))
			return out
		}},
		{name: "任务池", lines: func() []string {
			out := []string{}
			if snap.ActiveTask != nil {
				out = append(out, "[active] "+snap.ActiveTask.ID+" "+snap.ActiveTask.Title)
			}
			for _, t := range snap.Tasks {
				if t.State == "active" {
					continue
				}
				out = append(out, fmt.Sprintf("  %s P=%s %s — %s", t.ID, t.P, t.State, t.Title))
			}
			return out
		}},
		{name: "实验五态", lines: func() []string {
			out := []string{}
			for _, e := range snap.Experiments {
				depth := 0
				for i := 0; i < 5; i++ {
					if !blankCell(e.States[i]) {
						depth = i + 1
					}
				}
				out = append(out, fmt.Sprintf("  %s → 第%d态", e.Tag, depth))
			}
			return out
		}},
		{name: "机会池", lines: func() []string {
			out := []string{}
			if len(snap.Opportunities) == 0 {
				out = append(out, "  （空——待 T-02 首填）")
			}
			for _, o := range snap.Opportunities {
				out = append(out, fmt.Sprintf("  %s [%s]", o.Chance, o.Status))
			}
			return out
		}},
		{name: "决策日志", lines: func() []string {
			out := []string{}
			for _, d := range snap.Decisions {
				out = append(out, fmt.Sprintf("  %s %s [%s]", d.ID, truncateLine(d.Decision, 60), d.Status))
			}
			return out
		}},
		{name: "kernel 回执", lines: func() []string {
			out := []string{}
			if len(snap.Receipts) == 0 {
				out = append(out, "  （无回执）")
			}
			for _, r := range snap.Receipts {
				out = append(out, fmt.Sprintf("  %s → %s%s", r.Task, r.Status, ternary(r.Settled, " (settled)", "")))
			}
			return out
		}},
	}
}

func runCheckInline() string {
	cmd := exec.Command("go", "run", "./os/tools/oscheck")
	cmd.Dir = repoRoot()
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		first := firstNonEmptyLine(out.String())
		return yellow + "oscheck: 有违规 → " + first + ansiReset
	}
	return green + "oscheck: 全绿（T1–T8 / K1–K2 / E1 / A1）" + ansiReset
}

func blankCell(s string) bool {
	s = strings.TrimSpace(s)
	return s == "" || s == "—" || s == "-" || s == "–"
}

func marker(on bool) string {
	if on {
		return "▶"
	}
	return " "
}

func truncateLine(s string, n int) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func firstNonEmptyLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			return t
		}
	}
	return "(无输出)"
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func ternary(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}
