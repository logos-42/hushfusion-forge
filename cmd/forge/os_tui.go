package main

// forge os tui —— 基于 bubbletea v2（charm.land/bubbletea/v2）+ lipgloss/v2 + bubbles/v2
// 的终端 UI。表现层完全独立于 osview/oskernel/aios（这三个库不动），只通过它们的
// 公共 API 读写：osview 读状态、oskernel 经 doPropose/doOpp/doDecide 写、aios 跑 AI。
//
// 布局（agent 控制台，Hermes/Crush 风格）：
//
//	┌ 艺术字 logo ────────────────────────────────────────────┐
//	│ header: ◉FORGE · model:x/y · cache:hit · ⏱ 12s · sess  │
//	│ ┌ 输出/工具调用（展示框，PgUp/PgDn 滚动）──────────────┐ │
//	│ │  ⚙ tasks.propose ✓ …                                │ │
//	│ └──────────────────────────────────────────────────────┘ │
//	│ 上下文加载 [████░░] 62%   （加载时显示）                 │
//	│ ▌ 仪表盘（并排网格，↑↓←→ 选择，Enter 详情）              │
//	│ ❯ 输入目标回车=AI 推进；/task /opp /decide /check /help  │
//	│ ─────────────────────────────────────────────────────── │
//	└ 状态栏（模式 · 状态 · Ctrl+C×2 退出）────────────────────┘
//
// 能力：展示框(输出/工具调用) · 上下文加载进度条 · 缓存命中 · 运行时间 ·
// session 摘要 · 退出整理落盘(os/sessions) · interrupt(ctx 取消) · queue(排队) ·
// Ctrl+C 一次中断/两次退出 · 模型型号 · 工具调用展示。AI 以 tea.Cmd 异步执行。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/logos-42/hushfusion-forge/internal/agent"
	"github.com/logos-42/hushfusion-forge/internal/ai"
	"github.com/logos-42/hushfusion-forge/internal/aios"
	"github.com/logos-42/hushfusion-forge/internal/oskernel"
	"github.com/logos-42/hushfusion-forge/internal/osview"
)

const (
	ansiReset = "\033[0m"
	green     = "\033[32m"
	yellow    = "\033[33m"
)

// logoBanner 是顶部艺术字（ANSI Shadow 体的 FORGE）+ 简介。
const logoBanner = `███████╗ ██████╗ ██████╗  ██████╗ ███████╗
██╔════╝██╔═══██╗██╔══██╗██╔════╝ ██╔════╝
█████╗  ██║   ██║██████╔╝██║  ███╗█████╗
██╔══╝  ██║   ██║██╔══██╗██║   ██║██╔══╝
██║     ╚██████╔╝██║  ██║╚██████╔╝███████╗
╚═╝      ╚═════╝ ╚═╝  ╚═╝ ╚═════╝ ╚══════╝`

// kaomoji 是 AI 思考时每秒轮换的颜文字表情。
var kaomoji = []string{
	"(´･_･`)", "(◕‿◕)", "(╯°□°）╯︵ ┻━┻", "(｡•́︿•̀｡)", "(¬‿¬)", "(•_•)?",
	"( ˘ω˘ )", "(๑•̀ㅂ•́)و ✧", "(╥﹏╥)", "(￣▽￣)", "(・_・?)", "(ﾉ◕ヮ◕)ﾉ*:･ﾟ✧",
}

// panelCols 是仪表盘网格列数（↑↓ 按行跳转的 2D 导航）。
const panelCols = 3

type appState int

const (
	statePanels appState = iota
	stateDetail
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
	base string
	snap *osview.Snapshot

	panels []panel
	sel    int
	detail int
	state  appState

	form     *formModel
	spinner  spinner.Model
	cmdInput textinput.Model

	thinkingGoal string
	report       *aios.Report
	reportErr    error

	// 展示框（输出 / 工具调用日志）
	out       []string
	outScroll int

	// 上下文加载进度条
	loading bool
	progPct float64

	// 会话 / 头部元信息
	sessionID string
	startTime time.Time
	provider  string
	model     string
	cacheHit  string

	// 真实用量（provider 上报才有）：会话累计账本 + provider 实际回报的模型名。
	ledger    ai.UsageLedger
	modelUsed string

	// mem 是持久记忆（--persist 时落盘，否则纯内存）。每轮 AI 把它注入 system。
	mem *agent.MemStore

	// 视图/会话增强（学 Hermes /help 分类，落到 OS/内核架构）
	title      string // 会话标题（/title）
	lastGoal   string // 最近 AI 目标（/retry）
	showStatus bool   // 状态栏开关（/statusbar）
	focus      bool   // 极简模式（/focus）
	verbose    int    // 工具调用详细度 0/1/2（/verbose）
	indicator  string // 思考指示器 kaomoji/spinner/ascii（/indicator）
	status     string
	showHelp   bool

	// / 命令快速选择（命令面板）
	slashActive bool
	slashSel    int
	slashMatch  []slashCmd

	// AI 运行 / 队列 / 中断 / Ctrl+C 两次退出
	busy       bool
	queue      []string
	aiStart    time.Time
	aiCancel   context.CancelFunc
	ctrlCArm   bool
	lastCtrlC  time.Time
	thinkFrame int

	// 会话统计（退出摘要用）
	goalCount int
	toolCalls int
	toolOK    int
	toolFail  int

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
	logo        lipgloss.Style
	subtitle    lipgloss.Style
	dialog      lipgloss.Style
	dialogTitle lipgloss.Style
	header      lipgloss.Style
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

type thinkTickMsg struct{}

func thinkTick() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return thinkTickMsg{} })
}

type secondTickMsg struct{}

func secondTick() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return secondTickMsg{} })
}

type loadTickMsg struct{}

func loadTick() tea.Cmd {
	return tea.Tick(60*time.Millisecond, func(time.Time) tea.Msg { return loadTickMsg{} })
}

// keyMap 用 bubbles/key 声明式绑定（学 Crush keys.go），帮助栏由绑定自动生成。
type keyMap struct {
	quit   key.Binding
	reload key.Binding
	check  key.Binding
	up     key.Binding
	down   key.Binding
	left   key.Binding
	right  key.Binding
	open   key.Binding
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
		left:   key.NewBinding(key.WithKeys("left"), key.WithHelp("←", "左移选择")),
		right:  key.NewBinding(key.WithKeys("right"), key.WithHelp("→", "右移选择")),
		open:   key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "展开为对话框")),
		cancel: key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "取消/清空")),
		task:   key.NewBinding(key.WithKeys("n", "N"), key.WithHelp("n", "新任务")),
		opp:    key.NewBinding(key.WithKeys("o", "O"), key.WithHelp("o", "新机会")),
		decide: key.NewBinding(key.WithKeys("d", "D"), key.WithHelp("d", "新决策")),
		ai:     key.NewBinding(key.WithKeys("a", "A"), key.WithHelp("a", "AI 目标表单")),
		help:   key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "按键帮助")),
	}
}

var km = defaultKeyMap()

// slashCmd 描述一个 / 命令（命令面板与帮助共用同一份真源）。
type slashCmd struct {
	Name string
	Desc string
	Arg  string // 参数提示（空=无需参数，Enter 直接执行）
}

// slashCommands 是全部 / 命令（命令面板 + /help 共用）。
var slashCommands = []slashCmd{
	{"model", "切换/查看 AI 模型", "<provider> [model]"},
	{"task", "新任务表单", ""},
	{"opp", "新机会表单", ""},
	{"decide", "新决策表单", ""},
	{"ai", "直接跑 AI 目标", "<目标>"},
	{"check", "跑 oscheck 门", ""},
	{"reload", "重载 OS 视图", ""},
	{"open", "打开面板详情", "<N>"},
	{"clear", "清空展示框", ""},
	{"new", "开新会话", ""},
	{"save", "立即落盘会话", ""},
	{"summary", "打印会话摘要", ""},
	{"sessions", "列出历史会话", ""},
	{"interrupt", "中断当前 AI", ""},
	{"queue", "查看队列", ""},
	{"help", "按键帮助", ""},
	{"quit", "退出(落盘)", ""},
	{"exit", "退出(=/quit)", ""},
	{"reset", "开新会话(=/new)", ""},
	{"title", "设置会话标题", "[name]"},
	{"retry", "重跑最近目标", ""},
	{"statusbar", "开关状态栏", "[on|off]"},
	{"focus", "极简模式", "[on|off]"},
	{"verbose", "工具调用详细度", "[low|normal|high]"},
	{"indicator", "思考指示器", "[kaomoji|spinner|ascii]"},
	{"redraw", "强制重绘", ""},
	{"diff", "看 os/ git 改动", ""},
	{"palette", "打开命令面板", ""},
	{"remember", "写一条持久记忆", "<key> <value>"},
	{"forget", "删一条持久记忆", "<key>"},
	{"memory", "查看持久记忆", ""},
}

// refreshSlash 依据输入框内容刷新命令面板（以 "/" 开头即激活，按已输命令名过滤）。
func (m *osModel) refreshSlash() {
	v := m.cmdInput.Value()
	if !strings.HasPrefix(v, "/") {
		m.slashActive = false
		m.slashMatch = nil
		return
	}
	q := strings.ToLower(strings.TrimPrefix(v, "/"))
	name := q
	if i := strings.IndexAny(q, " "); i >= 0 {
		name = q[:i]
	}
	m.slashMatch = m.slashMatch[:0]
	for _, c := range slashCommands {
		if name == "" || strings.HasPrefix(c.Name, name) {
			m.slashMatch = append(m.slashMatch, c)
		}
	}
	m.slashActive = len(m.slashMatch) > 0
	if m.slashSel >= len(m.slashMatch) {
		m.slashSel = 0
	}
}

// runSlashByName 执行一个命令名（供命令面板 Enter 直接触发无参命令）。
func (m *osModel) runSlashByName(name string) (tea.Model, tea.Cmd) {
	m.cmdInput.SetValue("")
	m.slashActive = false
	return m.runSlash(name)
}

func newOSModel(base string, snap *osview.Snapshot) *osModel {
	m := &osModel{
		base:      base,
		snap:      snap,
		panels:    buildPanels(snap),
		state:     statePanels,
		width:     100,
		height:    30,
		sessionID: fmt.Sprintf("sess-%s-%04d", time.Now().Format("0102-150405"), time.Now().UnixNano()%10000),
		startTime: time.Now(),
		provider:  envOr("HUSHFUSION_AI_PROVIDER", "zai"),
		model:     envOr("HUSHFUSION_AI_MODEL", "default"),
		cacheHit:  "–",
	}
	m.initStyles()
	m.spinner = spinner.New(
		spinner.WithSpinner(spinner.Dot),
		spinner.WithStyle(m.styles.spinner),
	)
	ci := textinput.New()
	ci.Placeholder = "输入目标回车让 AI 推进；或 /model /task /opp /decide /check /help（↑↓←→ 选面板，PgUp/PgDn 滚输出）"
	ci.SetWidth(80)
	m.cmdInput = ci
	m.cmdInput.Focus()
	m.showStatus = true
	m.verbose = 1
	m.indicator = "kaomoji"
	m.mem = agent.NewMemStore() // 默认纯内存；cmdOSTUI --persist 会换成文件后备
	return m
}

// memBlock 返回注入 system 的记忆块（空 = 无记忆，不占预算）。
func (m *osModel) memBlock() string {
	if m.mem == nil {
		return ""
	}
	if m.mem.Len() == 0 {
		return ""
	}
	return "—— 持久记忆（/remember 写入，跨会话保留）——\n" + strings.Join(m.mem.Snapshot(), "\n")
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
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
		logo:        lipgloss.NewStyle().Foreground(lipgloss.Color("51")),
		subtitle:    lipgloss.NewStyle().Foreground(lipgloss.Color("245")),
		dialog:      lipgloss.NewStyle().Border(lipgloss.DoubleBorder()).BorderForeground(lipgloss.Color("51")).Padding(1, 2),
		dialogTitle: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("231")),
		header:      lipgloss.NewStyle().Foreground(lipgloss.Color("250")),
	}
}

// ---------------------------------------------------------------------------
// bubbletea 接口
// ---------------------------------------------------------------------------

func (m *osModel) Init() tea.Cmd {
	return tea.Batch(secondTick(), m.startLoad())
}

func (m *osModel) startLoad() tea.Cmd {
	m.loading = true
	m.progPct = 0
	return loadTick()
}

func (m *osModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		if w := msg.Width - 4; w > 20 {
			m.cmdInput.SetWidth(w)
		}
		return m, nil
	case spinner.TickMsg:
		if m.state == stateThinking {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}
		return m, nil
	case secondTickMsg:
		return m, secondTick() // 驱动 header 运行时间每秒刷新
	case loadTickMsg:
		m.progPct += 20
		if m.progPct >= 100 {
			m.progPct = 100
			m.loading = false
			m.appendDashboard()
			return m, nil
		}
		return m, loadTick()
	case thinkTickMsg:
		if m.state == stateThinking {
			m.thinkFrame++
			return m, thinkTick()
		}
		return m, nil
	case aiResultMsg:
		return m.onAIResult(msg)
	case tea.KeyPressMsg:
		return m.updateKey(msg)
	}
	return m, nil
}

// onAIResult 收 AI 结果：落工具调用进展示框、解除 busy、按队列续跑。
func (m *osModel) onAIResult(msg aiResultMsg) (tea.Model, tea.Cmd) {
	m.busy = false
	m.aiCancel = nil
	m.state = stateReport
	m.report = msg.rep
	m.reportErr = msg.err
	if msg.err != nil {
		m.out = append(m.out, m.styles.err.Render("✗ "+msg.err.Error()))
		if strings.Contains(msg.err.Error(), "API key") {
			m.out = append(m.out, m.styles.hint.Render("💡 未配 key：export OPENROUTER_API_KEY=… 或 GLM_API_KEY=…；或 export HUSHFUSION_AI_PROVIDER=openrouter；按 a 用表单指定 provider/model"))
		}
		if strings.Contains(msg.err.Error(), "context") {
			m.out = append(m.out, m.styles.hint.Render("⏹ AI 已中断"))
		}
	} else if msg.rep != nil {
		m.appendToolCalls(msg.rep)
	}
	// 真实用量入账（provider 上报才有；假路径为 0）。缓存命中率只在"上报过"时展示。
	if msg.rep != nil {
		m.ledger = m.ledger.Merged(msg.rep.Usage)
		m.modelUsed = firstNonEmptyStr(msg.rep.Model, m.modelUsed)
	}
	m.cacheHit = m.cacheLabel()
	m.reload()
	if len(m.queue) > 0 {
		next := m.queue[0]
		m.queue = m.queue[1:]
		_, cmd := m.startThinking(next, "", "")
		return m, cmd
	}
	return m, nil
}

func (m *osModel) updateKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// Ctrl+C 全局语义：忙→中断；空闲→两次退出（第二次落盘并退出）。
	if msg.String() == "ctrl+c" {
		if m.busy && m.aiCancel != nil {
			m.aiCancel() // 一次：中断当前 AI（ctx 取消）
		}
		if m.ctrlCArm && time.Since(m.lastCtrlC) < 2*time.Second {
			return m.doQuit() // 两次：整理会话并退出
		}
		m.ctrlCArm = true
		m.lastCtrlC = time.Now()
		if m.busy {
			m.status = m.styles.err.Render("⏹ 已中断 · 再按 Ctrl+C 退出")
		} else {
			m.status = "再按一次 Ctrl+C 退出（会话将落盘）"
		}
		return m, nil
	}
	switch m.state {
	case stateThinking:
		if key.Matches(msg, km.quit) && msg.String() == "q" {
			return m.doQuit()
		}
		return m, nil
	case stateDetail:
		switch {
		case key.Matches(msg, km.quit) && msg.String() == "q":
			return m.doQuit()
		case key.Matches(msg, km.cancel), key.Matches(msg, km.open):
			m.state = statePanels
			return m, nil
		case key.Matches(msg, km.up):
			m.panels[m.detail].scroll--
		case key.Matches(msg, km.down):
			m.panels[m.detail].scroll++
		}
		return m, nil
	case stateForm:
		if key.Matches(msg, km.cancel) {
			m.state = statePanels
			m.status = m.styles.err.Render("已取消录入")
			m.form = nil
			return m, nil
		}
		if key.Matches(msg, km.quit) && msg.String() == "q" {
			return m.doQuit()
		}
		return m.updateForm(msg)
	case stateReport:
		if key.Matches(msg, km.quit) && msg.String() == "q" {
			return m.doQuit()
		}
		m.state = statePanels
		m.reload()
		m.status = ""
		return m, nil
	default: // statePanels（控制台）
		return m.updatePanels(msg)
	}
}

func (m *osModel) updatePanels(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.showHelp {
		if key.Matches(msg, km.quit) && msg.String() == "q" {
			return m.doQuit()
		}
		m.showHelp = false
		return m, nil
	}
	m.ctrlCArm = false // 其它键解除“Ctrl+C 退出”待确认
	// 输出框虚拟滚动：PgUp/PgDn 翻页，Home 到顶，End 回到底部（吸底）
	switch msg.String() {
	case "pgup":
		m.outScroll += m.pageSize()
		return m, nil
	case "pgdown":
		m.outScroll -= m.pageSize()
		if m.outScroll < 0 {
			m.outScroll = 0
		}
		return m, nil
	case "home":
		m.outScroll = 1 << 30 // 渲染时按 maxScroll 收敛到顶部
		return m, nil
	case "end":
		m.outScroll = 0
		return m, nil
	}
	if msg.String() == "ctrl+p" {
		m.cmdInput.SetValue("/")
		m.refreshSlash()
		m.cmdInput.Focus()
		return m, nil
	}
	// / 命令面板激活时：↑↓ 选择、Enter/Tab 补全或执行、Esc 取消、其它键继续输入并过滤
	if m.slashActive {
		switch msg.String() {
		case "up", "k":
			if m.slashSel > 0 {
				m.slashSel--
			}
			return m, nil
		case "down", "j":
			if m.slashSel < len(m.slashMatch)-1 {
				m.slashSel++
			}
			return m, nil
		case "enter":
			v := m.cmdInput.Value()
			if strings.Contains(strings.TrimPrefix(v, "/"), " ") {
				return m.submitCommand(strings.TrimSpace(v)) // 已在输参数 → 提交
			}
			if m.slashSel < 0 || m.slashSel >= len(m.slashMatch) {
				return m, nil
			}
			c := m.slashMatch[m.slashSel]
			if c.Arg == "" {
				return m.runSlashByName(c.Name)
			}
			m.cmdInput.SetValue("/" + c.Name + " ")
			m.refreshSlash()
			return m, nil
		case "tab":
			if m.slashSel >= 0 && m.slashSel < len(m.slashMatch) {
				m.cmdInput.SetValue("/" + m.slashMatch[m.slashSel].Name + " ")
				m.refreshSlash()
			}
			return m, nil
		case "esc":
			m.slashActive = false
			return m, nil
		default:
			var cmd tea.Cmd
			m.cmdInput, cmd = m.cmdInput.Update(msg)
			m.refreshSlash()
			return m, cmd
		}
	}
	// 方向键：仪表盘 2D 选择
	switch {
	case key.Matches(msg, km.up):
		m.moveSel(-panelCols)
		return m, nil
	case key.Matches(msg, km.down):
		m.moveSel(panelCols)
		return m, nil
	case key.Matches(msg, km.left):
		m.moveSel(-1)
		return m, nil
	case key.Matches(msg, km.right):
		m.moveSel(1)
		return m, nil
	}
	// 输入框为空时：单键快捷键
	if strings.TrimSpace(m.cmdInput.Value()) == "" {
		switch {
		case key.Matches(msg, km.help):
			m.showHelp = true
			return m, nil
		case key.Matches(msg, km.reload):
			m.reload()
			m.appendDashboard()
			m.status = m.styles.ok.Render("已重新加载")
			return m, nil
		case key.Matches(msg, km.check):
			m.status = runCheckInline()
			m.out = append(m.out, stripANSI(m.status))
			m.reload()
			return m, nil
		case key.Matches(msg, km.task):
			m.openForm("task")
			return m, nil
		case key.Matches(msg, km.opp):
			m.openForm("opp")
			return m, nil
		case key.Matches(msg, km.decide):
			m.openForm("decide")
			return m, nil
		case key.Matches(msg, km.ai):
			m.openForm("ai")
			return m, nil
		case key.Matches(msg, km.quit):
			return m.doQuit()
		}
	}
	// 其余键进输入框
	switch msg.String() {
	case "esc":
		m.cmdInput.SetValue("")
		return m, nil
	case "enter":
		if t := strings.TrimSpace(m.cmdInput.Value()); t != "" {
			return m.submitCommand(t)
		}
		if m.sel >= 0 && m.sel < len(m.panels) {
			m.detail = m.sel
			m.panels[m.detail].scroll = 0
			m.state = stateDetail
		}
		return m, nil
	default:
		var cmd tea.Cmd
		m.cmdInput, cmd = m.cmdInput.Update(msg)
		m.refreshSlash()
		return m, cmd
	}
}

func (m *osModel) moveSel(delta int) {
	n := len(m.panels)
	if n == 0 {
		return
	}
	m.sel += delta
	if m.sel < 0 {
		m.sel = 0
	}
	if m.sel > n-1 {
		m.sel = n - 1
	}
}

// submitCommand：忙则入队；否则立即跑（/ 命令 或 AI 目标）。
func (m *osModel) submitCommand(text string) (tea.Model, tea.Cmd) {
	m.cmdInput.SetValue("")
	if text == "" {
		return m, nil
	}
	if m.busy {
		m.queue = append(m.queue, text)
		m.out = append(m.out, m.styles.hint.Render(fmt.Sprintf("⏳ 已入队 (%d): %s", len(m.queue), text)))
		m.status = fmt.Sprintf("已入队，当前队列 %d", len(m.queue))
		return m, nil
	}
	if strings.HasPrefix(text, "/") {
		return m.runSlash(strings.TrimPrefix(text, "/"))
	}
	m.goalCount++
	m.out = append(m.out, m.styles.label.Render("❯ ")+text)
	return m.startThinking(text, "", "")
}

func (m *osModel) runSlash(cmd string) (tea.Model, tea.Cmd) {
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return m, nil
	}
	switch fields[0] {
	case "task":
		m.openForm("task")
	case "opp":
		m.openForm("opp")
	case "decide":
		m.openForm("decide")
	case "ai":
		goal := strings.TrimSpace(strings.TrimPrefix(cmd, fields[0]))
		if goal != "" {
			m.goalCount++
			m.out = append(m.out, m.styles.label.Render("❯ ")+goal)
			return m.startThinking(goal, "", "")
		}
		m.openForm("ai")
	case "model":
		return m.slashModel(cmd, fields[0])
	case "clear":
		m.out = nil
		m.outScroll = 0
		m.status = m.styles.ok.Render("展示框已清空")
	case "new":
		m.sessionID = fmt.Sprintf("sess-%s-%04d", time.Now().Format("0102-150405"), time.Now().UnixNano()%10000)
		m.startTime = time.Now()
		m.out = nil
		m.outScroll = 0
		m.goalCount, m.toolCalls, m.toolOK, m.toolFail = 0, 0, 0, 0
		m.queue = nil
		m.appendDashboard()
		m.status = m.styles.ok.Render("新会话 " + m.sessionID)
	case "save":
		m.persistSession()
		m.status = m.styles.ok.Render("会话已落盘 os/sessions/" + m.sessionID + ".md")
	case "summary":
		m.appendSummary()
		m.status = m.styles.ok.Render("摘要见展示框")
	case "sessions":
		m.appendSessions()
	case "interrupt":
		if m.busy && m.aiCancel != nil {
			m.aiCancel()
			m.status = m.styles.err.Render("⏹ 已中断")
		} else {
			m.status = m.styles.hint.Render("当前没有运行中的 AI")
		}
	case "queue":
		if len(m.queue) == 0 {
			m.status = m.styles.hint.Render("队列为空")
		} else {
			m.appendQueue()
			m.status = fmt.Sprintf("队列 %d 条", len(m.queue))
		}
	case "remember":
		rest := strings.TrimSpace(strings.TrimPrefix(cmd, fields[0]))
		parts := strings.SplitN(rest, " ", 2)
		if len(parts) < 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
			m.status = m.styles.err.Render("用法: /remember <key> <value>")
			return m, nil
		}
		k, v := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		old, existed := m.mem.Set(k, v)
		m.out = append(m.out, m.styles.ok.Render("✓ 记忆已写入: "+k+"="+v))
		if m.mem.LastPersistErr != nil {
			// 落盘失败要说出来：否则用户以为"记住了"，重启就没了。
			m.out = append(m.out, m.styles.err.Render("⚠ 未落盘: "+m.mem.LastPersistErr.Error()))
			m.status = m.styles.err.Render("记忆仅在内存")
		} else if existed {
			m.status = m.styles.ok.Render("已更新 " + k + "（旧值: " + old + "）")
		} else {
			m.status = m.styles.ok.Render("已记住 " + k)
		}
		m.trimOut()
	case "forget":
		k := strings.TrimSpace(strings.TrimPrefix(cmd, fields[0]))
		if k == "" {
			m.status = m.styles.err.Render("用法: /forget <key>")
			return m, nil
		}
		if m.mem.Delete(k) {
			m.out = append(m.out, m.styles.ok.Render("✓ 已删除记忆: "+k))
			m.status = m.styles.ok.Render("已删除 " + k)
		} else {
			m.status = m.styles.hint.Render("没有这条记忆: " + k)
		}
		m.trimOut()
	case "memory":
		m.out = append(m.out, m.styles.label.Render("── 持久记忆 ──"))
		if m.mem.Len() == 0 {
			m.out = append(m.out, m.styles.hint.Render("  （无；用 /remember <key> <value> 写）"))
		} else {
			for _, kv := range m.mem.Snapshot() {
				m.out = append(m.out, "  "+kv)
			}
		}
		if p := m.mem.Path(); p != "" {
			m.out = append(m.out, m.styles.hint.Render("  文件: "+p))
		} else {
			m.out = append(m.out, m.styles.hint.Render("  内存模式（重启即失忆）；用 --persist 落盘"))
		}
		m.status = m.styles.ok.Render(fmt.Sprintf("记忆 %d 条", m.mem.Len()))
		m.trimOut()
	case "check":
		m.status = runCheckInline()
		m.out = append(m.out, stripANSI(m.status))
		m.reload()
		m.appendDashboard()
	case "reload":
		m.reload()
		m.appendDashboard()
		m.status = m.styles.ok.Render("已重载")
	case "help":
		m.showHelp = true
	case "open":
		if len(fields) > 1 {
			if n, err := strconv.Atoi(fields[1]); err == nil && n >= 1 && n <= len(m.panels) {
				m.detail = n - 1
				m.panels[m.detail].scroll = 0
				m.state = stateDetail
			} else {
				m.status = m.styles.err.Render("/open 需要 1.." + strconv.Itoa(len(m.panels)))
			}
		}
	case "reset":
		return m.runSlash("new")
	case "title":
		t := strings.TrimSpace(strings.TrimPrefix(cmd, fields[0]))
		m.title = t
		if t == "" {
			m.status = m.styles.ok.Render("已清除标题")
		} else {
			m.status = m.styles.ok.Render("标题: " + t)
		}
	case "retry":
		if m.lastGoal == "" {
			m.status = m.styles.hint.Render("没有可重跑的目标")
		} else {
			m.out = append(m.out, m.styles.label.Render("↻ 重跑: ")+m.lastGoal)
			return m.startThinking(m.lastGoal, "", "")
		}
	case "statusbar":
		m.showStatus = toggleOnOff(fields, m.showStatus)
		m.status = onOffLabel("状态栏", m.showStatus)
	case "focus":
		m.focus = toggleOnOff(fields, m.focus)
		m.status = onOffLabel("极简模式", m.focus)
	case "verbose":
		lv := []string{"low", "normal", "high"}
		if len(fields) > 1 {
			switch strings.ToLower(fields[1]) {
			case "low":
				m.verbose = 0
			case "normal":
				m.verbose = 1
			case "high":
				m.verbose = 2
			default:
				m.status = m.styles.err.Render("verbose: low|normal|high")
				return m, nil
			}
		} else {
			m.verbose = (m.verbose + 1) % len(lv)
		}
		m.status = m.styles.ok.Render("工具详细度: " + lv[m.verbose])
	case "indicator":
		opts := []string{"kaomoji", "spinner", "ascii"}
		if len(fields) > 1 && indexOfStr(opts, fields[1]) >= 0 {
			m.indicator = fields[1]
		} else if len(fields) > 1 {
			m.status = m.styles.err.Render("indicator: kaomoji|spinner|ascii")
			return m, nil
		} else {
			m.indicator = opts[(indexOfStr(opts, m.indicator)+1)%len(opts)]
		}
		m.status = m.styles.ok.Render("指示器: " + m.indicator)
	case "redraw":
		return m, tea.ClearScreen
	case "diff":
		m.appendGitDiff()
		m.status = m.styles.ok.Render("git diff 见展示框")
	case "palette":
		m.cmdInput.SetValue("/")
		m.refreshSlash()
		m.cmdInput.Focus()
		m.status = m.styles.ok.Render("命令面板已打开（↑↓ 选择）")
	case "quit", "exit":
		return m.doQuit()
	default:
		m.status = m.styles.err.Render("未知命令: /" + fields[0] + "（/help 看命令）")
	}
	return m, nil
}

// slashModel 处理 /model：无参=展示当前+可选列表；带参=/model <provider> [model] 切换。
func (m *osModel) slashModel(cmd, head string) (tea.Model, tea.Cmd) {
	rest := strings.TrimSpace(strings.TrimPrefix(cmd, head))
	if rest == "" {
		m.out = append(m.out, m.styles.label.Render(fmt.Sprintf("当前模型: %s / %s", m.provider, m.model)))
		m.out = append(m.out, m.styles.hint.Render("用法: /model <provider> [model]  ·  可用 provider:"))
		for _, p := range ai.Builtins() {
			line := fmt.Sprintf("  %-12s 默认 %s", p.Name, p.DefaultModel)
			if p.APIMode == ai.ModeAnthropicMessages {
				line += "  (未实现, fail-closed)"
			}
			m.out = append(m.out, m.styles.hint.Render(line))
		}
		m.status = m.styles.ok.Render("模型列表见展示框")
		return m, nil
	}
	parts := strings.Fields(rest)
	prof := findBuiltin(parts[0])
	if prof == nil {
		m.status = m.styles.err.Render("未知 provider: " + parts[0] + "（/model 看列表）")
		return m, nil
	}
	m.provider = prof.Name
	if len(parts) > 1 {
		m.model = parts[1]
	} else {
		m.model = prof.DefaultModel
	}
	m.out = append(m.out, m.styles.ok.Render(fmt.Sprintf("✓ 已切换模型 → %s / %s", m.provider, m.model)))
	if !modelKeySet(prof) {
		m.out = append(m.out, m.styles.err.Render(fmt.Sprintf("⚠ 该 provider 似未配 key（env: %s）", strings.Join(prof.EnvVars, " / "))))
	}
	m.status = m.styles.ok.Render(fmt.Sprintf("model → %s/%s", m.provider, m.model))
	return m, nil
}

// findBuiltin 按名字或别名找内置 provider。
func findBuiltin(nameOrAlias string) *ai.Profile {
	q := strings.ToLower(strings.TrimSpace(nameOrAlias))
	for _, p := range ai.Builtins() {
		if p.Name == q {
			return p
		}
		for _, a := range p.Aliases {
			if a == q {
				return p
			}
		}
	}
	return nil
}

// modelKeySet 判断该 provider 是否已有可用 key（env 非空或显式 Keys）。
func modelKeySet(p *ai.Profile) bool {
	for _, e := range p.EnvVars {
		if os.Getenv(e) != "" {
			return true
		}
	}
	return len(p.Keys) > 0
}

// appendSummary 把当前会话摘要追加进展示框。
func (m *osModel) appendSummary() {
	m.out = append(m.out, m.styles.label.Render("── 会话摘要 ──"))
	m.out = append(m.out, fmt.Sprintf("  session: %s", m.sessionID))
	m.out = append(m.out, fmt.Sprintf("  时长: %s", time.Since(m.startTime).Round(time.Second)))
	m.out = append(m.out, fmt.Sprintf("  model: %s/%s", m.provider, m.model))
	m.out = append(m.out, fmt.Sprintf("  AI 目标: %d · 工具调用: %d (✓%d/✗%d) · 队列: %d",
		m.goalCount, m.toolCalls, m.toolOK, m.toolFail, len(m.queue)))
	m.trimOut()
}

// appendSessions 列出 os/sessions 下的历史会话（最近 10 个）。
func (m *osModel) appendSessions() {
	m.out = append(m.out, m.styles.label.Render("── 历史会话 (os/sessions) ──"))
	dir := filepath.Join(m.base, "sessions")
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		m.out = append(m.out, m.styles.hint.Render("  （无）"))
		m.trimOut()
		return
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() > entries[j].Name() })
	for i, e := range entries {
		if i >= 10 {
			break
		}
		m.out = append(m.out, "  "+e.Name())
	}
	m.trimOut()
}

// appendQueue 把当前队列追加进展示框。
func (m *osModel) appendQueue() {
	m.out = append(m.out, m.styles.label.Render("── 队列 ──"))
	for i, q := range m.queue {
		m.out = append(m.out, fmt.Sprintf("  %d. %s", i+1, q))
	}
	m.trimOut()
}

// startThinking 进入思考态：spinner + 颜文字 + 可中断 ctx + 异步 AI。
func (m *osModel) startThinking(goal, provider, model string) (tea.Model, tea.Cmd) {
	if provider == "" {
		provider = m.provider
	}
	if model == "" {
		model = m.model
	}
	m.provider = provider
	m.model = model
	m.state = stateThinking
	m.thinkingGoal = goal
	m.lastGoal = goal
	m.thinkFrame = 0
	m.report, m.reportErr = nil, nil
	m.status = ""
	m.busy = true
	m.aiStart = time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	m.aiCancel = cancel
	return m, tea.Batch(m.spinner.Tick, thinkTick(), runAICmd(ctx, m.base, goal, provider, model, m.memBlock()))
}

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
		ti.SetWidth(60)
		fm.inputs[i] = ti
	}
	if kind == "ai" {
		fm.inputs[1].SetValue(m.provider)
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
		m.out = append(m.out, m.styles.ok.Render("✓ tasks.propose "+id))
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
		m.out = append(m.out, m.styles.ok.Render("✓ opportunities.add "+v(0)))
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
		m.out = append(m.out, m.styles.ok.Render("✓ decisions.add "+v(0)))
	case "ai":
		goal, provider, model := v(0), v(1), v(2)
		if goal == "" {
			m.status = m.styles.err.Render("目标必填")
			return m, nil
		}
		m.goalCount++
		m.out = append(m.out, m.styles.label.Render("❯ ")+goal)
		return m.startThinking(goal, provider, model)
	}
	m.reload()
	m.appendDashboard()
	m.form = nil
	m.state = statePanels
	return m, nil
}

func runAICmd(ctx context.Context, base, goal, provider, model, extraSystem string) tea.Cmd {
	return func() tea.Msg {
		ag := aios.NewAgent(oskernel.New(base), provider, model, os.Getenv("HUSHFUSION_AI_BASE_URL"))
		ag.ExtraSystem = extraSystem // 持久记忆块（--persist 时非空）
		rep, err := ag.Run(ctx, goal, false)
		return aiResultMsg{rep: rep, err: err}
	}
}

func (m *osModel) reload() {
	if s, err := osview.Load(m.base); err == nil {
		m.snap = s
		m.panels = buildPanels(s)
		if m.detail >= len(m.panels) {
			m.detail = 0
		}
		if m.sel >= len(m.panels) {
			m.sel = len(m.panels) - 1
		}
	}
}

// appendDashboard 把当前仪表盘快照追加进展示框。
func (m *osModel) appendDashboard() {
	m.out = append(m.out, m.styles.label.Render("── 仪表盘快照 ──"))
	for _, p := range m.panels {
		ls := p.lines()
		head := fmt.Sprintf("  %s (%d)", p.name, len(ls))
		m.out = append(m.out, m.styles.value.Render(head))
		for i, l := range ls {
			if i >= 4 {
				m.out = append(m.out, m.styles.hint.Render("    …"))
				break
			}
			m.out = append(m.out, "    "+truncateLine(l, 60))
		}
	}
	m.trimOut()
}

// appendToolCalls 把一次 AI 回合的计划/执行/备注作为“工具调用”落进展示框。
func (m *osModel) appendToolCalls(rep *aios.Report) {
	allowed, denied := 0, 0
	for _, p := range rep.Plan {
		if p.Allowed {
			allowed++
		} else {
			denied++
		}
	}
	if m.verbose == 0 { // low：仅汇总
		m.out = append(m.out, m.styles.label.Render(fmt.Sprintf("── AI 回合 ── %d 步（✓%d/✗%d）", len(rep.Plan), allowed, denied)))
		m.trimOut()
		return
	}
	m.out = append(m.out, m.styles.label.Render("── AI 回合 ──"))
	for _, p := range rep.Plan {
		m.toolCalls++
		if p.Allowed {
			m.toolOK++
			line := "  ✓ " + p.Syscall
			if m.verbose >= 2 {
				line += " " + argsSummary(p.Args)
			}
			m.out = append(m.out, m.styles.ok.Render(line))
		} else {
			m.toolFail++
			m.out = append(m.out, m.styles.err.Render("  ✗ "+p.Syscall+" ("+p.Reason+")"))
		}
	}
	for _, r := range rep.Results {
		if r.OK {
			m.out = append(m.out, m.styles.ok.Render("    → "+r.Syscall+" "+r.Message))
		} else {
			m.out = append(m.out, m.styles.err.Render("    → "+r.Syscall+" "+r.Err))
		}
	}
	if m.verbose >= 2 {
		for _, n := range rep.Notes {
			m.out = append(m.out, m.styles.hint.Render("  · "+n))
		}
	}
	m.trimOut()
}

// appendGitDiff 把 os/ 的 git 改动概要追加进展示框（只读）。
func (m *osModel) appendGitDiff() {
	cmd := exec.Command("git", "-C", repoRoot(), "diff", "--stat", "--", "os")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	m.out = append(m.out, m.styles.label.Render("── git diff --stat os/ ──"))
	if err := cmd.Run(); err != nil {
		m.out = append(m.out, m.styles.err.Render("  git diff 失败: "+firstNonEmptyLine(out.String())))
		m.trimOut()
		return
	}
	body := strings.TrimSpace(out.String())
	if body == "" {
		m.out = append(m.out, m.styles.hint.Render("  （os/ 无未提交改动）"))
	} else {
		for _, l := range strings.Split(body, "\n") {
			m.out = append(m.out, "  "+l)
		}
	}
	m.trimOut()
}

// doQuit 退出前整理会话并落盘（best-effort，失败不影响退出）。
func (m *osModel) doQuit() (tea.Model, tea.Cmd) {
	m.persistSession()
	return m, tea.Quit
}

func (m *osModel) persistSession() {
	dir := filepath.Join(m.base, "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	var b strings.Builder
	b.WriteString("# 会话 " + m.sessionID + "\n\n")
	if m.title != "" {
		b.WriteString(fmt.Sprintf("- 标题: %s\n", m.title))
	}
	b.WriteString(fmt.Sprintf("- 开始: %s\n", m.startTime.Format(time.RFC3339)))
	b.WriteString(fmt.Sprintf("- 结束: %s\n", time.Now().Format(time.RFC3339)))
	b.WriteString(fmt.Sprintf("- 时长: %s\n", time.Since(m.startTime).Round(time.Second)))
	b.WriteString(fmt.Sprintf("- provider/model: %s / %s\n", m.provider, m.model))
	b.WriteString(fmt.Sprintf("- AI 目标数: %d\n", m.goalCount))
	b.WriteString(fmt.Sprintf("- 工具调用: %d（✓ %d / ✗ %d）\n", m.toolCalls, m.toolOK, m.toolFail))
	b.WriteString(fmt.Sprintf("- 退出时未处理队列: %d\n", len(m.queue)))
	b.WriteString("\n## 输出日志\n\n```\n" + strings.Join(m.out, "\n") + "\n```\n")
	_ = os.WriteFile(filepath.Join(dir, m.sessionID+".md"), []byte(b.String()), 0o644)
	// 机器可读侧车：供 --resume 恢复（输出日志 + 计数）。
	data := sessionData{
		SessionID: m.sessionID,
		Title:     m.title,
		StartTime: m.startTime.Format(time.RFC3339),
		Provider:  m.provider,
		Model:     m.model,
		GoalCount: m.goalCount,
		ToolCalls: m.toolCalls,
		ToolOK:    m.toolOK,
		ToolFail:  m.toolFail,
		Out:       m.out,
		Usage:     m.ledger,
	}
	if jb, err := json.MarshalIndent(data, "", "  "); err == nil {
		_ = os.WriteFile(filepath.Join(dir, m.sessionID+".json"), jb, 0o644)
	}
}

// sessionData 是会话的机器可读取证（os/sessions/<id>.json），供 --resume 恢复。
type sessionData struct {
	SessionID string   `json:"session_id"`
	Title     string   `json:"title,omitempty"`
	StartTime string   `json:"start_time"`
	Provider  string   `json:"provider"`
	Model     string   `json:"model"`
	GoalCount int      `json:"goal_count"`
	ToolCalls int      `json:"tool_calls"`
	ToolOK    int      `json:"tool_ok"`
	ToolFail  int      `json:"tool_fail"`
	Out       []string `json:"out"`
	// Usage 是会话累计的真实 token 用量（provider 上报才有；--resume 后继续累计）。
	Usage ai.UsageLedger `json:"usage,omitempty"`
}

// loadSession 读取一个会话（sel 为空/latest=最新；否则按 id）。best-effort。
func loadSession(base, sel string) (*sessionData, error) {
	dir := filepath.Join(base, "sessions")
	id := strings.TrimSuffix(sel, ".json")
	if sel == "" || sel == "latest" {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		newest := ""
		for _, e := range entries {
			n := e.Name()
			if strings.HasSuffix(n, ".json") && n > newest {
				newest = n
			}
		}
		if newest == "" {
			return nil, fmt.Errorf("os/sessions 无会话可恢复")
		}
		id = strings.TrimSuffix(newest, ".json")
	}
	raw, err := os.ReadFile(filepath.Join(dir, id+".json"))
	if err != nil {
		return nil, err
	}
	var d sessionData
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

func firstNonEmptyStr(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// toggleOnOff 解析可选 [on|off] 参数；无参则在两者间切换。
func toggleOnOff(fields []string, cur bool) bool {
	if len(fields) > 1 {
		switch strings.ToLower(fields[1]) {
		case "on", "true", "1":
			return true
		case "off", "false", "0":
			return false
		}
	}
	return !cur
}

func onOffLabel(name string, on bool) string {
	if on {
		return name + ": 开"
	}
	return name + ": 关"
}

func indexOfStr(ss []string, x string) int {
	for i, s := range ss {
		if s == x {
			return i
		}
	}
	return -1
}

func asciiSpin(frame int) string {
	frames := []string{"-", "\\", "|", "/"}
	return frames[frame%len(frames)]
}

// ---------------------------------------------------------------------------
// View
// ---------------------------------------------------------------------------

func (m *osModel) View() tea.View {
	w := m.width
	if w < 40 {
		w = 80
	}
	var b strings.Builder
	b.WriteString(m.renderBanner())
	switch m.state {
	case stateDetail:
		b.WriteString(m.renderDialog(m.panels[m.detail].name+" · 详情", m.detailBody()))
	case stateForm:
		b.WriteString(m.renderDialog("录入："+m.form.title, m.formBody()))
	case stateThinking:
		b.WriteString(m.renderDialog("AI 思考中", m.thinkingBody()))
	case stateReport:
		b.WriteString(m.renderDialog("AI 粘合报告", m.reportBody()))
	default: // statePanels（控制台）
		if m.showHelp {
			b.WriteString(m.renderDialog("按键帮助", m.helpBody()))
		} else {
			b.WriteString(m.renderConsole(w))
		}
	}
	if !m.focus {
		b.WriteString(m.renderStatusBar(w))
	}
	v := tea.NewView(b.String())
	v.AltScreen = true
	return v
}

// ---------------------------------------------------------------------------
// 控制台布局：header + 展示框 + 进度条 + 仪表盘 + 输入框
// ---------------------------------------------------------------------------

func (m *osModel) renderConsole(w int) string {
	var b strings.Builder
	if !m.focus {
		b.WriteString(m.renderHeader(w))
	}
	popup := m.renderSlashPopup(w)
	popupH := 0
	if popup != "" {
		popupH = strings.Count(popup, "\n")
	}
	chrome := 20
	if m.focus {
		chrome = 8
	}
	outH := m.height - chrome - popupH
	if outH < 4 {
		outH = 4
	}
	b.WriteString(m.renderOutputBox(w, outH))
	if m.loading {
		b.WriteString(m.renderProgress(w))
	}
	if !m.focus {
		b.WriteString(m.renderDashboard(w))
	}
	b.WriteString(popup)
	b.WriteString(m.renderInput(w))
	return b.String()
}

func (m *osModel) renderHeader(w int) string {
	el := time.Since(m.startTime).Round(time.Second)
	left := m.styles.logo.Render("◉ FORGE")
	sess := m.sessionID
	if m.title != "" {
		sess = m.title
	}
	model := m.model
	if m.modelUsed != "" {
		model = m.modelUsed
	}
	meta := fmt.Sprintf("model:%s · tok:%s · cache:%s · %s · ⏱ %s · %s",
		model, m.tokenLabel(), m.cacheLabel(), m.costLabel(), el, sess)
	line := left + "  " + m.styles.header.Render(meta)
	return line + "\n"
}

// cacheLabel 渲染缓存命中率。provider **没上报过**缓存字段时显示 "–"
// （那是"无从得知"，不是 0% 命中 —— 拿 0% 冒充会误导优化方向）。
func (m *osModel) cacheLabel() string {
	if !m.ledger.CacheReported || m.ledger.Prompt == 0 {
		return "–"
	}
	return fmt.Sprintf("%.0f%%", m.ledger.CacheHitRate()*100)
}

// tokenLabel 渲染累计 token（prompt/completion）。
func (m *osModel) tokenLabel() string {
	if m.ledger.Calls == 0 {
		return "–"
	}
	return humanNum(m.ledger.Prompt) + "/" + humanNum(m.ledger.Completion)
}

// costLabel 渲染累计成本；价目未知显示 "—"（不显示 0，避免被当成免费）。
func (m *osModel) costLabel() string {
	model := m.modelUsed
	if model == "" {
		model = m.model
	}
	usd, known := m.ledger.Cost(model)
	if !known {
		return "—"
	}
	return fmt.Sprintf("$%.4f", usd)
}

// humanNum 把 token 数压成 1.2k / 3.4M 形式。
func humanNum(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	default:
		return strconv.Itoa(n)
	}
}

func (m *osModel) renderOutputBox(w, h int) string {
	bodyH := h - 3
	if bodyH < 1 {
		bodyH = 1
	}
	lines := m.out
	maxScroll := 0
	if len(lines) > bodyH {
		maxScroll = len(lines) - bodyH
	}
	if m.outScroll > maxScroll {
		m.outScroll = maxScroll
	}
	if m.outScroll < 0 {
		m.outScroll = 0
	}
	end := len(lines) - m.outScroll
	start := end - bodyH
	if start < 0 {
		start = 0
	}
	var show []string
	for i := start; i < end; i++ {
		show = append(show, truncateLine(lines[i], w-4))
	}
	if len(show) == 0 {
		show = []string{m.styles.hint.Render("  （等待上下文加载…）")}
	}
	title := m.styles.label.Render("▌ 输出 / 工具调用")
	if maxScroll > 0 {
		title += m.styles.hint.Render(fmt.Sprintf("  (%d/%d)", len(lines)-m.outScroll, len(lines)))
	}
	if m.outScroll > 0 {
		title += m.styles.err.Render(fmt.Sprintf("  ↓ %d 条新内容 · PgDn/End 回到底部", m.outScroll))
	}
	content := title + "\n" + strings.Join(show, "\n")
	return m.styles.panel.Width(w-2).Render(content) + "\n"
}

// pageSize 是展示框一页的行数（翻页步长）。
func (m *osModel) pageSize() int {
	p := m.height - 22
	if p < 3 {
		p = 3
	}
	return p
}

// outHardCap 是虚拟化展示框的硬上限（防失控；正常会话远达不到，等同于“无限”）。
const outHardCap = 200000

// trimOut 前端裁剪到硬上限，并同步滚动偏移保持视口稳定（虚拟化：只渲染可见行）。
func (m *osModel) trimOut() {
	if len(m.out) > outHardCap {
		drop := len(m.out) - outHardCap
		m.out = m.out[drop:]
		m.outScroll -= drop
		if m.outScroll < 0 {
			m.outScroll = 0
		}
	}
}

func (m *osModel) renderProgress(w int) string {
	bar := m.renderBar(int(m.progPct), w-22)
	return m.styles.hint.Render("上下文加载 ") + bar +
		m.styles.value.Render(fmt.Sprintf(" %3.0f%%", m.progPct)) + "\n"
}

func (m *osModel) renderBar(pct, width int) string {
	if width < 8 {
		width = 8
	}
	filled := pct * width / 100
	if filled < 0 {
		filled = 0
	}
	if filled > width {
		filled = width
	}
	return m.styles.ok.Render(strings.Repeat("█", filled)) +
		m.styles.hint.Render(strings.Repeat("░", width-filled))
}

func (m *osModel) renderDashboard(w int) string {
	return m.styles.label.Render("▌ 仪表盘") + "\n" + m.renderPanels(w)
}

func (m *osModel) renderPanels(w int) string {
	const cols = panelCols
	colW := (w - 2*(cols-1) - 2) / cols
	if colW < 18 {
		colW = 18
	}
	var rows []string
	for i := 0; i < len(m.panels); i += cols {
		var cells []string
		for j := 0; j < cols; j++ {
			idx := i + j
			if idx >= len(m.panels) {
				cells = append(cells, strings.Repeat(" ", colW))
				continue
			}
			cells = append(cells, m.panelCell(idx, colW))
		}
		rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, cells...))
	}
	return strings.Join(rows, "\n") + "\n"
}

func (m *osModel) panelCell(idx, colW int) string {
	p := m.panels[idx]
	n := len(p.lines())
	head := marker(idx == m.sel) + " " + p.name + fmt.Sprintf(" (%d)", n)
	first := "（空）"
	if n > 0 {
		first = truncateLine(p.lines()[0], colW-4)
	}
	content := m.styles.label.Render(head) + "\n" + m.styles.value.Render(first)
	st := m.styles.panel
	if idx == m.sel {
		st = m.styles.panelActive
	}
	return st.Width(colW).Render(content)
}

func (m *osModel) renderInput(w int) string {
	prompt := m.styles.label.Render("❯ ")
	line := prompt + m.cmdInput.View()
	q := ""
	if len(m.queue) > 0 {
		q = m.styles.hint.Render(fmt.Sprintf("   [队列 %d]", len(m.queue)))
	}
	rule := lipgloss.NewStyle().Foreground(lipgloss.Color("240")).
		Render(strings.Repeat("─", max(0, w-1)))
	return "\n" + line + q + "\n" + rule + "\n"
}

// renderSlashPopup 渲染 / 命令快速选择面板（激活且匹配非空时，出现在输入框上方）。
func (m *osModel) renderSlashPopup(w int) string {
	if !m.slashActive || len(m.slashMatch) == 0 {
		return ""
	}
	const maxShow = 8
	var body strings.Builder
	body.WriteString(m.styles.label.Render("▌ 命令 · ↑↓ 选择 · Enter/Tab 补全或执行 · Esc 取消") + "\n")
	start := 0
	if m.slashSel >= maxShow {
		start = m.slashSel - maxShow + 1
	}
	for i := start; i < len(m.slashMatch) && i < start+maxShow; i++ {
		c := m.slashMatch[i]
		line := fmt.Sprintf("/%-9s %s", c.Name, c.Desc)
		if c.Arg != "" {
			line += "  " + c.Arg
		}
		if i == m.slashSel {
			body.WriteString(m.styles.title.Render("▶ "+line) + "\n")
		} else {
			body.WriteString(m.styles.hint.Render("  "+line) + "\n")
		}
	}
	return m.styles.panelActive.Width(min(w-2, 78)).Render(body.String()) + "\n"
}

func (m *osModel) renderStatusBar(w int) string {
	if !m.showStatus {
		return ""
	}
	// 干净的底部一行：模式 · (瞬时状态 或 静态提示) · 退出提示。≤3 段、分隔一致、
	// 不做反色满宽横幅（避免拖一条大空白），超宽按字符截断。
	parts := []string{modeLabel(m.state)}
	if m.status != "" {
		parts = append(parts, stripANSI(m.status))
	} else {
		parts = append(parts, "/help · PgUp/PgDn 滚屏")
	}
	parts = append(parts, "Ctrl+C×2 退出")
	line := strings.Join(parts, "  ·  ")
	if r := []rune(line); w > 1 && len(r) > w-1 {
		line = string(r[:w-1])
	}
	return m.styles.hint.Render(line) + "\n"
}

func modeLabel(s appState) string {
	switch s {
	case stateDetail:
		return "详情"
	case stateForm:
		return "录入"
	case stateThinking:
		return "AI…"
	case stateReport:
		return "报告"
	default:
		return "控制台"
	}
}

// ---------------------------------------------------------------------------
// 对话框（唯一模态）
// ---------------------------------------------------------------------------

func (m *osModel) renderDialog(title, body string) string {
	w := m.width
	if w < 40 {
		w = 80
	}
	contentH := m.height - (strings.Count(logoBanner, "\n") + 1) - 4
	if contentH < 6 {
		contentH = 6
	}
	inner := m.styles.dialogTitle.Render(title) + "\n\n" + body
	box := m.styles.dialog.Width(min(w-6, 88)).Render(inner)
	return lipgloss.Place(w, contentH, lipgloss.Center, lipgloss.Center, box) + "\n"
}

func (m *osModel) renderBanner() string {
	return m.styles.logo.Render(logoBanner) + "\n" +
		m.styles.subtitle.Render("  ◉ HUSHFUSION OS · 宏内核 TUI  —  设计 → 物理 → 评分 → 搜索 → 注册 → 规则") + "\n\n"
}

func (m *osModel) detailBody() string {
	p := m.panels[m.detail]
	ls := p.lines()
	if len(ls) == 0 {
		return m.styles.hint.Render("（空）")
	}
	room := m.height - (strings.Count(logoBanner, "\n") + 1) - 8
	if room < 3 {
		room = 3
	}
	start := clamp(p.scroll, 0, max(0, len(ls)-room))
	var b strings.Builder
	for j := start; j < len(ls) && j < start+room; j++ {
		b.WriteString("  " + ls[j] + "\n")
	}
	b.WriteString("\n" + m.styles.hint.Render("↑↓ 滚动 · Esc/Enter 返回"))
	return b.String()
}

func (m *osModel) thinkingBody() string {
	el := time.Since(m.aiStart).Round(time.Second)
	var ind string
	switch m.indicator {
	case "spinner":
		ind = m.spinner.View()
	case "ascii":
		ind = m.styles.title.Render(asciiSpin(m.thinkFrame))
	default:
		ind = m.styles.title.Render(kaomoji[m.thinkFrame%len(kaomoji)])
	}
	return "  " + ind + "   " + m.thinkingGoal + "\n\n" +
		m.styles.hint.Render(fmt.Sprintf("经内核受控系统调用执行中 · ⏱ %s · %s 每秒切换 · Ctrl+C 中断", el, m.indicator))
}

func (m *osModel) formBody() string {
	var b strings.Builder
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

func (m *osModel) reportBody() string {
	if m.reportErr != nil {
		s := m.styles.err.Render("运行失败: "+m.reportErr.Error()) + "\n\n"
		if strings.Contains(m.reportErr.Error(), "API key") {
			s += m.keyHint() + "\n\n"
		}
		s += m.styles.hint.Render("任意键返回 · q 退出")
		return s
	}
	var b strings.Builder
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

func (m *osModel) keyHint() string {
	return m.styles.hint.Render(
		"未配置 API key。设置任一 provider 的 key 后重试：\n" +
			"  export OPENROUTER_API_KEY=sk-...   # openrouter (openai/gpt-5)\n" +
			"  export GLM_API_KEY=...            # zai/GLM (glm-5.3-flash)\n" +
			"  export DEEPSEEK_API_KEY=...       # deepseek (deepseek-chat)\n" +
			"切换默认：export HUSHFUSION_AI_PROVIDER=openrouter\n" +
			"或在输入框按 a 用 AI 表单指定 provider/model")
}

func (m *osModel) helpBody() string {
	var b strings.Builder
	for _, bd := range []key.Binding{km.up, km.down, km.left, km.right, km.open, km.task, km.opp, km.decide, km.ai, km.check, km.reload, km.help, km.cancel, km.quit} {
		if !bd.Enabled() {
			continue
		}
		h := bd.Help()
		b.WriteString(fmt.Sprintf("  %-10s %s\n", h.Key, h.Desc))
	}
	b.WriteString("\n" + m.styles.hint.Render("Ctrl+P 或 /palette 开命令面板；/title /retry /reset /statusbar /focus /verbose /indicator /diff /redraw"))
	b.WriteString("\n" + m.styles.hint.Render("PgUp/PgDn 滚展示框；输入框命令：/model /task /opp /decide /ai <目标> /check /reload /open N /save /summary /sessions /queue /help /quit"))
	b.WriteString("\n" + m.styles.hint.Render("Ctrl+C：忙=中断，空闲=按两次退出（退出整理会话并落盘 os/sessions）"))
	b.WriteString("\n" + m.styles.hint.Render("任意键关闭帮助"))
	return b.String()
}

// ---------------------------------------------------------------------------
// 入口（非 TTY 退化成仪表盘）
// ---------------------------------------------------------------------------

func cmdOSTUI(args []string) int {
	fs := newFlagSet("os tui", "forge os tui [--resume <id|latest>] [--continue <id|latest>] [--in DIR] [--persist] [--memory-file PATH]",
		"启动 TUI 控制台。--resume/--continue 恢复历史会话（latest=最近）；--in 指定 os/ 根目录；"+
			"--persist 把 agent 记忆落盘（/remember 写入，跨会话保留）。")
	resume := fs.String("resume", "", "恢复会话：session id 或 latest")
	cont := fs.String("continue", "", "恢复会话（同 --resume）")
	contShort := fs.String("c", "", "同 --continue")
	inDir := fs.String("in", "", "工作目录（设为 os/ 根，等同 HUSHFUSION_OS_DIR）")
	persist := fs.Bool("persist", false, "持久化 agent 记忆到 <os>/agent-memory.json（跨会话保留）")
	memFile := fs.String("memory-file", "", "记忆文件路径（隐含 --persist；默认 <os>/agent-memory.json）")
	if code := parseFlags(fs, args); code >= 0 {
		return code
	}
	base := osBaseDir()
	if *inDir != "" {
		base = *inDir
	}
	snap, err := osview.Load(base)
	if err != nil {
		return fail("%v", err)
	}
	if st, _ := os.Stdout.Stat(); (st.Mode() & os.ModeCharDevice) == 0 {
		fmt.Print(snap.Dashboard())
		return 0
	}
	m := newOSModel(base, snap)
	// 记忆持久化（--persist / --memory-file）。失败不挡启动，但必须说清楚已退化为内存模式
	// —— 静默"以为记住了"是最坏的结果。
	if *persist || *memFile != "" {
		path := *memFile
		if path == "" {
			path = filepath.Join(base, "agent-memory.json")
		}
		if st, merr := agent.NewMemStoreFile(path); merr == nil {
			m.mem = st
			m.out = append(m.out, m.styles.ok.Render("✓ 记忆持久化: "+path))
		} else {
			fmt.Fprintln(os.Stderr, "os tui: 记忆文件不可用（退回内存模式）:", merr)
			m.out = append(m.out, m.styles.err.Render("⚠ 记忆文件不可用，已退回内存模式: "+merr.Error()))
		}
	}
	if sel := firstNonEmptyStr(*resume, *cont, *contShort); sel != "" {
		if d, lerr := loadSession(base, sel); lerr == nil {
			m.sessionID = orDefault(d.SessionID, m.sessionID)
			m.title = d.Title
			m.provider = orDefault(d.Provider, m.provider)
			m.model = orDefault(d.Model, m.model)
			m.goalCount = d.GoalCount
			m.toolCalls, m.toolOK, m.toolFail = d.ToolCalls, d.ToolOK, d.ToolFail
			m.ledger = d.Usage // 用量随会话恢复（成本/命中率继续累计）
			m.out = append(m.out, m.styles.label.Render("── 恢复会话 "+m.sessionID+" ──"))
			m.out = append(m.out, d.Out...)
			m.startTime = time.Now() // 计时自恢复时刻起
		} else {
			fmt.Fprintln(os.Stderr, "os tui: 恢复会话失败（将开新会话）:", lerr)
		}
	}
	p := tea.NewProgram(m)
	if _, err := p.Run(); err != nil {
		return fail("os tui: %v", err)
	}
	return 0
}

// ---------------------------------------------------------------------------
// 复用工具
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

func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == '\033' {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			if i < len(s) {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
