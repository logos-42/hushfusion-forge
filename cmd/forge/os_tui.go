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
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/charmbracelet/x/ansi"
	"github.com/logos-42/hushfusion-forge/internal/agent"
	"github.com/logos-42/hushfusion-forge/internal/ai"
	"github.com/logos-42/hushfusion-forge/internal/aios"
	"github.com/logos-42/hushfusion-forge/internal/envload"
	"github.com/logos-42/hushfusion-forge/internal/osview"
	"github.com/logos-42/hushfusion-forge/internal/persona"
	"github.com/logos-42/hushfusion-forge/internal/workspace"
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
	stateModelWizard
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

	// 面板只读：没有 sel/选中态，所有键盘与鼠标事件都只作用于输入框。
	panels []panel
	detail int
	state  appState

	form     *formModel
	wiz      *modelWizard
	spinner  spinner.Model
	cmdInput textinput.Model

	// 输入历史：↑↓ 在输入框里回填以前提交过的文本（仅会话内，不落盘）。
	inputHistory []string
	histPos      int    // 当前浏览位置；== len(inputHistory) 表示草稿
	histDraft    string // 开始浏览历史前输入框里的草稿
	histBrowsing bool   // 正在浏览历史：↑↓ 优先给历史而不是 / 命令面板

	thinkingGoal string
	report       *aios.Report
	reportErr    error

	// 展示框（输出 / 工具调用日志）
	out         []string
	outScroll   int
	replyBlocks [][]string // 历史回复：每条回复单独一个 forge 框，不合并

	// 用户发送内容：lastEcho 渲染在 forge 框上方；sentLog 用于会话落盘。
	lastEcho []string
	sentLog  []string
	// 工具调用：渲染在 forge 框外，逐行（💻/📖/📚…）。
	toolLog []string

	// 上下文加载进度条
	loading bool
	progPct float64

	// 会话 / 头部元信息
	sessionID string
	startTime time.Time

	// 空闲心跳：最后一次用户活动 / 最后一次空闲动作的时间。
	lastActivity time.Time
	lastIdleRun  time.Time
	provider     string
	model        string
	cacheHit     string

	// 真实用量（provider 上报才有）：会话累计账本 + provider 实际回报的模型名。
	ledger    ai.UsageLedger
	modelUsed string

	// mem 是持久记忆（--persist 时落盘，否则纯内存）。每轮 AI 把它注入 system。
	mem *agent.MemStore
	// sess 是跨轮复用的 agent 会话：只有它持有对话历史，/compress 才有东西可压。
	// /new、切换模型时置 nil，下一轮重建。
	sess *agent.Session
	// budgetTokens 是对话历史预算；默认按模型上下文窗口的 60% 自动匹配，
	// 可用 /budget N 或 os/forge.config.json 的 budget_tokens 覆盖。
	budgetTokens int
	// goal 是 /goal 设置的长期目标；loop* 是 /loop 自主推进状态。
	goal         string
	threadGoal   string // 普通对话的“对话主线”：第一条实质输入，无 /goal 时也注入，防漂移
	loopActive   bool
	loopMax      int
	loopIter     int
	loopInterval time.Duration
	loopWaiting  bool
	loopErr      int
	loopStopReq  bool
	// chatRetry* 是普通对话出错后的自动纠正反馈（区别于 /loop 的续跑）。
	chatRetry      int
	chatRetrying   bool
	chatStopReq    bool
	pendingChatCmd tea.Cmd
	pendingLoopCmd tea.Cmd
	// frozenContext 是会话首次 AI 调用前构建一次的冻结快照：
	// user/agent/soul 文件 + skills + 沉淀经验 + 长期记忆 + 记忆 + 工作流。
	// 之后每轮原样注入 system，不随文件变化重建（“每轮不能忘”）。
	frozenContext string

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
	// 向导专用：accent=加粗亮蓝（provider/模型名），bright=高亮正文，
	// sel=选中行（蓝底白字），searchBar=带下划线的搜索框，hintBox=亮眼的操作提示框。
	accent    lipgloss.Style
	bright    lipgloss.Style
	sel       lipgloss.Style
	searchBar lipgloss.Style
	hintBox   lipgloss.Style
	replyBox  lipgloss.Style // 输出区的圆角「forge」框
	dashBox   lipgloss.Style // 顶部仪表盘的圆角框（与回复框同款）
}

type formModel struct {
	kind   string
	title  string
	labels []string
	inputs []textinput.Model
	focus  int
}

// modelWizard 是 /model 的多步设置向导：选 provider → 填 model → 填 key → 填 URL → 确认。
//
// 为什么不能一步做完：真实场景里 key 往往还没配、BaseURL 要指向内网网关，
// 「一条命令一刀切」只会逼人退出 TUI 去 shell 里 export。分步让每一步都可核对、
// 可回退（Esc 上一步），且 key 只在内存里，不落盘、不进会话、不进日志。
type modelWizard struct {
	step      int // 见 wizStep* 常量
	providers []*ai.Profile
	cursor    int
	provider  *ai.Profile

	// API key 步：keep / replace / clear
	keyAction  string // "keep" | "replace" | "clear"
	keyEditing bool   // replace 子态：正在输入新 key
	key        textinput.Model

	// provider 步：搜索栏 + 上下选择（cursor 指 provShown 的下标）
	provFilter textinput.Model
	provShown  []*ai.Profile

	// 模型步：内置型号 + 关键词筛选 + 上下选择
	filter     textinput.Model
	modelAll   []string // 最近/常用/已解析/默认（去重保序）
	modelShown []string // 按 filter 过滤后的视图
	modelSel   int
	model      string   // 最终选定
	recent     []string // 最近使用（缓存，渲染标记用）
	resolved   []string // /models 异步拉回的模型
	modelsBusy bool

	url textinput.Model
}

const (
	wizStepProvider = iota // 上下选 provider
	wizStepKey             // API key：keep / replace / clear
	wizStepModel           // 上下选模型（可筛选）
	wizStepURL             // 填 Base URL
	wizStepConfirm         // 汇总确认
)

type wizModelsMsg struct {
	provider string
	models   []string
	err      error
}

type aiResultMsg struct {
	rep    *aios.Report
	answer string            // agent 运行时的最终文字回复
	tools  []agent.ToolEvent // 本轮工具调用（框外逐行展示）
	err    error
	// sess 是跨轮复用的会话；compacted 表示本轮自动压缩过。
	sess      *agent.Session
	compacted bool
}

// compressResultMsg 是 /compress 手动摘要的结果。
type compressResultMsg struct {
	removed int
	summary string
	err     error
}

// loopTickMsg 是 /loop 的定时器：间隔到点后推进下一轮。
type loopTickMsg struct{}

func loopTick(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return loopTickMsg{} })
}

// toolEventMsg 是一条实时工具调用事件（agent 执行完一个工具就投递一条）。
type toolEventMsg struct {
	ev agent.ToolEvent
	ch chan tea.Msg
}

// waitForMsg 继续等待同一个通道的下一条消息（Bubble Tea 实时投递的标准写法）。
func waitForMsg(ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
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
	{"model", "模型设置向导（provider→API key→模型→URL；key 支持 keep/replace/clear，模型可筛选）", ""},
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
	{"compress", "摘要压缩较早历史", ""},
	{"budget", "查看/设置上下文预算", "[tokens]"},
	{"goal", "设置/查看长期目标", "[目标|clear]"},
	{"loop", "按目标自主循环", "[N] [30s|5m|2h] | stop | status"},
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
	m.pushHistory("/" + name)
	return m.runSlash(name)
}

func newOSModel(base string, snap *osview.Snapshot) *osModel {
	// 先加载 workspace config 并注入环境变量，后面的 envOr 才能读到。
	applyWorkspaceConfig(base)
	// .env 环境加载与保护：不覆盖已有 env；失败要明说，不静默。
	var envErr error
	if _, err := envload.Load(repoRoot()); err != nil {
		envErr = err
	}
	migrateRuntimeData(base) // 旧 os/、./forge 数据搬到全局 ~/.forge
	// 型号不再写死 "default"：未显式设置时取 provider 的 DefaultModel
	// （custom → custom-model），保证自定义端点启动即带出正确型号。
	provName := envOr("HUSHFUSION_AI_PROVIDER", "zai")
	defModel := "default"
	if p := ai.Lookup(provName); p != nil {
		provName = p.Name
		if strings.TrimSpace(p.DefaultModel) != "" {
			defModel = p.DefaultModel
		}
	}
	modelName := envOr("HUSHFUSION_AI_MODEL", defModel)
	// 预算随模型自动匹配；workspace 显式配置优先。
	budgetTokens := loadWorkspaceConfig(base).BudgetTokens
	if budgetTokens <= 0 {
		budgetTokens = agent.BudgetForModel(modelName, 0.6)
	}
	m := &osModel{
		base:         base,
		snap:         snap,
		panels:       buildPanels(snap),
		state:        statePanels,
		width:        100,
		height:       30,
		sessionID:    fmt.Sprintf("sess-%s-%04d", time.Now().Format("0102-150405"), time.Now().UnixNano()%10000),
		startTime:    time.Now(),
		lastActivity: time.Now(),
		provider:     provName,
		model:        modelName,
		cacheHit:     "–",
		budgetTokens: budgetTokens,
	}
	m.initStyles()
	if envErr != nil {
		m.out = append(m.out, m.styles.err.Render("⚠ .env 加载失败: "+envErr.Error()))
	}
	m.spinner = spinner.New(
		spinner.WithSpinner(spinner.Dot),
		spinner.WithStyle(m.styles.spinner),
	)
	ci := textinput.New()
	ci.Placeholder = "输入目标回车让 AI 推进；或 /model /task /opp /decide /help（面板只读，PgUp/PgDn 滚输出）"
	ci.SetWidth(80)
	m.cmdInput = ci
	m.cmdInput.Focus()
	m.showStatus = true
	m.verbose = 1
	m.indicator = "kaomoji"
	m.mem = agent.NewMemStore() // 默认纯内存；cmdOSTUI --persist 会换成文件后备
	m.loadWorkflowMemory()      // 恢复上次空闲沉淀的工作流记忆
	// 检查工作区经验 skill：存在则加载（第二轮打开同一目录也能复用/继续迭代）。
	if b := ensureEvolvedSkill(base); b != "" {
		if n := strings.Count(b, "\n## "); n > 0 {
			m.status = m.styles.hint.Render(fmt.Sprintf("📚 已加载历史经验 %d 条（%s）", n, evolvedSkillDir(base)))
		}
	}
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

// buildFrozenContext 在会话首次 AI 调用前构建一次：
// user / agent / soul / 规则 / 记忆文件 + skills 索引 + 沉淀经验 + 长期记忆 + agent 记忆 + 工作流。
// 之后每轮把同一份字符串注入 system（冻结，不随文件变化重建）。
func (m *osModel) buildFrozenContext() string {
	var b strings.Builder
	b.WriteString("# 冻结上下文快照（本会话首次加载；每轮注入，勿忘）\n")
	b.WriteString("生成时间: " + time.Now().Format("2006-01-02 15:04") + "\n")

	// 1) SOUL.md / AGENTS.md / USER.md / CLAUDE.md / MEMORY.md
	if block, err := persona.Bootstrap(repoRoot(), 8000); err == nil && strings.TrimSpace(block) != "" {
		b.WriteString("\n" + block + "\n")
	}

	// 2) skills 索引（完整正文仍用 skill.view 按需拉取）
	skills := agent.NewSkills([]string{globalSkillsDir(m.base), projectSkillsDir(m.base)}, agent.NewKeywordMatcher())
	if all := skills.All(); len(all) > 0 {
		b.WriteString("\n## Skills（global + project；project 同名覆盖，可按名字用 skill.view 取全文）\n")
		for _, sk := range all {
			scope := "global"
			if strings.HasPrefix(sk.Path, projectSkillsDir(m.base)) {
				scope = "project"
			}
			b.WriteString("- [" + scope + "] " + sk.Name + "：" + clipSkillText(sk.Description, 160) + "\n")
		}
	}

	// 3) 每轮自动沉淀的经验
	if body := ensureEvolvedSkill(m.base); strings.TrimSpace(body) != "" {
		tail := tailRunes(body, 4000)
		b.WriteString("\n## 沉淀经验（本项目 skills/evolved/SKILL.md）\n")
		// 长文件尾部可能把规则截掉；确保提示词始终在快照里。
		if !strings.Contains(tail, "沉淀规则") {
			b.WriteString(skillDistillPrompt + "\n\n")
		}
		b.WriteString(tail + "\n")
	}

	// 4) 跨会话长期记忆（最近 20 条）
	if recs := loadMemRecords(m.base); len(recs) > 0 {
		start := len(recs) - 20
		if start < 0 {
			start = 0
		}
		b.WriteString("\n## 长期记忆（最近 20 条，跨会话）\n")
		for _, r := range recs[start:] {
			b.WriteString("- [" + r.Time + "] " + clipSkillText(r.Goal, 60) + " → " + clipSkillText(r.Text, 160) + "\n")
		}
	}

	// 5) agent 记忆（/remember + 空闲心跳沉淀的 workflow）
	if mb := m.memBlock(); mb != "" {
		b.WriteString("\n" + mb + "\n")
	}

	// 6) 工作流快照
	if wf, err := os.ReadFile(filepath.Join(runtimeMemoryDir(m.base), "workflow.md")); err == nil {
		b.WriteString("\n## 工作流快照\n" + tailRunes(string(wf), 1200) + "\n")
	}

	// 7) workspace 分类索引：项目文档 / 设计 / 验收 / 证据 / 回执 / 工具 等
	b.WriteString("\n" + workspace.Render(workspace.Scan(repoRoot()), 6))
	return b.String()
}

// tailRunes 按 rune 取尾部（保留最近内容，不破坏 CJK）。
func tailRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return "…" + string(r[len(r)-n:])
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
		panel:       lipgloss.NewStyle(), // 仪表盘面板不再套框（纯文本网格）
		panelActive: lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("51")).Padding(0, 1),
		replyBox:    lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("39")).Padding(0, 1),
		dashBox:     lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("39")).Padding(0, 1),
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
		accent:      lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39")),
		bright:      lipgloss.NewStyle().Foreground(lipgloss.Color("255")),
		sel:         lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("231")).Background(lipgloss.Color("24")),
		searchBar:   lipgloss.NewStyle().Border(lipgloss.NormalBorder(), false, false, true, false).BorderForeground(lipgloss.Color("39")).Padding(0, 1),
		hintBox:     lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("39")).Padding(0, 1).Bold(true).Foreground(lipgloss.Color("231")).Background(lipgloss.Color("24")),
	}
}

// ---------------------------------------------------------------------------
// bubbletea 接口
// ---------------------------------------------------------------------------

func (m *osModel) Init() tea.Cmd {
	cmds := []tea.Cmd{secondTick(), heartbeatTick(), m.startLoad()}
	if m.pendingChatCmd != nil {
		cmds = append(cmds, m.pendingChatCmd)
		m.pendingChatCmd = nil
	}
	if m.pendingLoopCmd != nil {
		cmds = append(cmds, m.pendingLoopCmd)
		m.pendingLoopCmd = nil
	}
	return tea.Batch(cmds...)
}

// 空闲心跳：无输入一段时间后，自动做梦 / 进化 skill / 整理工作流并写入记忆。
const (
	heartbeatInterval = 15 * time.Second
	idleThreshold     = 45 * time.Second
	idleCooldown      = 2 * time.Minute
)

type heartbeatMsg struct{}

func heartbeatTick() tea.Cmd {
	return tea.Tick(heartbeatInterval, func(time.Time) tea.Msg { return heartbeatMsg{} })
}

func (m *osModel) idlePulse() {
	if m.busy || m.loading {
		return
	}
	if time.Since(m.lastActivity) < idleThreshold {
		return
	}
	if time.Since(m.lastIdleRun) < idleCooldown {
		return
	}
	m.lastIdleRun = time.Now()
	var parts []string
	if m.dream() {
		parts = append(parts, "做梦")
	}
	if m.compactEvolvedSkill() {
		parts = append(parts, "进化 skill")
	}
	m.organizeWorkflow()
	parts = append(parts, "整理工作流")
	m.status = m.styles.hint.Render("💤 空闲心跳：" + strings.Join(parts, " + "))
}

// dream 把近期回复里的碎片重新拼成一条“梦”，追加到 os/dreams.md（不调模型）。
func (m *osModel) dream() bool {
	pool := append([]string(nil), m.out...)
	for _, blk := range m.replyBlocks {
		pool = append(pool, blk...)
	}
	var picks []string
	for i := len(pool) - 1; i >= 0 && len(picks) < 3; i-- {
		s := strings.TrimSpace(stripANSI(pool[i]))
		if len([]rune(s)) >= 12 {
			picks = append(picks, s)
		}
	}
	if len(picks) == 0 {
		return false
	}
	line := fmt.Sprintf("\n## %s · 空闲梦\n%s\n", time.Now().Format("2006-01-02 15:04"), strings.Join(picks, " … "))
	_ = os.MkdirAll(runtimeDreamsDir(m.base), 0o755)
	path := filepath.Join(runtimeDreamsDir(m.base), time.Now().Format("2006-01-02")+".md")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return false
	}
	defer f.Close()
	_, err = f.WriteString(line)
	return err == nil
}

// compactEvolvedSkill 去重并裁剪经验 skill（相同结论只留最新一条）。
func (m *osModel) compactEvolvedSkill() bool {
	b, err := os.ReadFile(evolvedSkillPath(m.base))
	if err != nil {
		return false
	}
	content := string(b)
	const marker = "\n# 历史经验\n"
	i := strings.Index(content, marker)
	if i < 0 {
		return false
	}
	head := content[:i+len(marker)]
	parts := strings.Split(content[i+len(marker):], "\n## ")
	seen := map[string]bool{}
	var kept []string
	changed := false
	for j := len(parts) - 1; j >= 0; j-- {
		p := parts[j]
		key := ""
		for _, ln := range strings.Split(p, "\n") {
			t := strings.TrimSpace(ln)
			if strings.HasPrefix(t, "- 结论:") {
				key = strings.TrimSpace(strings.TrimPrefix(t, "- 结论:"))
			}
		}
		if key != "" && seen[key] {
			changed = true
			continue
		}
		if key != "" {
			seen[key] = true
		}
		kept = append([]string{p}, kept...)
	}
	if len(kept) > 60 {
		kept = kept[len(kept)-60:]
		changed = true
	}
	if !changed {
		return false
	}
	return os.WriteFile(evolvedSkillPath(m.base), []byte(head+strings.Join(kept, "\n## ")), 0o644) == nil
}

// organizeWorkflow 把当前工作流沉淀到 os/memory/workflow.md，并写进 agent 记忆供下轮回调。
func (m *osModel) organizeWorkflow() {
	dir := runtimeMemoryDir(m.base)
	_ = os.MkdirAll(dir, 0o755)
	var b strings.Builder
	b.WriteString("# 工作流快照\n\n")
	b.WriteString("- 时间: " + time.Now().Format("2006-01-02 15:04") + "\n")
	b.WriteString("- 会话: " + m.sessionID + "\n")
	b.WriteString("- 最近目标: " + firstNonEmptyStr(m.lastGoal, "（无）") + "\n")
	b.WriteString(fmt.Sprintf("- 工具调用: %d（✓%d / ✗%d）\n", m.toolCalls, m.toolOK, m.toolFail))
	if len(m.toolLog) > 0 {
		b.WriteString("\n## 最近工具\n")
		start := len(m.toolLog) - 10
		if start < 0 {
			start = 0
		}
		for _, l := range m.toolLog[start:] {
			b.WriteString("- " + strings.TrimSpace(stripANSI(l)) + "\n")
		}
	}
	if len(m.panels) > 0 {
		b.WriteString("\n## 面板\n")
		for _, p := range m.panels {
			b.WriteString("### " + p.name + "\n")
			for i, l := range p.lines() {
				if i >= 3 {
					break
				}
				b.WriteString("- " + strings.TrimSpace(stripANSI(l)) + "\n")
			}
		}
	}
	body := b.String()
	_ = os.WriteFile(filepath.Join(dir, "workflow.md"), []byte(body), 0o644)
	if m.mem != nil {
		m.mem.Set("workflow", clipSkillText(body, 1200))
	}
}

// loadWorkflowMemory 启动时把上次沉淀的工作流恢复到 agent 记忆。
func (m *osModel) loadWorkflowMemory() {
	if m.mem == nil {
		return
	}
	if b, err := os.ReadFile(filepath.Join(runtimeMemoryDir(m.base), "workflow.md")); err == nil {
		m.mem.Set("workflow", clipSkillText(string(b), 1200))
	}
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
		if m.busy {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}
		return m, nil
	case secondTickMsg:
		return m, secondTick() // 驱动 header 运行时间每秒刷新
	case heartbeatMsg:
		m.idlePulse()
		return m, heartbeatTick()
	case loadTickMsg:
		m.progPct += 20
		if m.progPct >= 100 {
			m.progPct = 100
			m.loading = false
			return m, nil
		}
		return m, loadTick()
	case thinkTickMsg:
		if m.busy {
			m.thinkFrame++
			return m, thinkTick()
		}
		return m, nil
	case toolEventMsg:
		// 工具执行完立刻追加一行，并继续等下一条（回复随后到达）。
		m.appendToolEvents([]agent.ToolEvent{msg.ev})
		return m, waitForMsg(msg.ch)
	case aiResultMsg:
		return m.onAIResult(msg)
	case compressResultMsg:
		return m.onCompressResult(msg)
	case loopTickMsg:
		return m.onLoopTick()
	case chatRetryMsg:
		return m.onChatRetry()
	case wizModelsMsg:
		return m.onWizModels(msg)
	case tea.PasteMsg:
		m.lastActivity = time.Now()
		return m.handlePaste(msg)
	case tea.KeyPressMsg:
		m.lastActivity = time.Now()
		return m.updateKey(msg)
	}
	return m, nil
}

// handlePaste 把终端粘贴内容送进当前焦点输入框。
// bubbletea v2 的 bracketed paste 走 tea.PasteMsg（不是 KeyPressMsg），
// 若不在顶层路由就会静默丢掉 —— 表现就是向导里粘贴 API key 没反应。
func (m *osModel) handlePaste(msg tea.PasteMsg) (tea.Model, tea.Cmd) {
	if m.busy { // 思考中吞输入，与按键行为保持一致
		return m, nil
	}
	switch m.state {
	case stateModelWizard:
		w := m.wiz
		if w == nil {
			return m, nil
		}
		switch {
		case w.step == wizStepKey && w.keyEditing:
			var cmd tea.Cmd
			w.key, cmd = w.key.Update(msg)
			return m, cmd
		case w.step == wizStepModel:
			var cmd tea.Cmd
			w.filter, cmd = w.filter.Update(msg)
			m.applyModelFilter()
			return m, cmd
		case w.step == wizStepURL:
			var cmd tea.Cmd
			w.url, cmd = w.url.Update(msg)
			return m, cmd
		}
		return m, nil
	case stateForm:
		if m.form == nil || len(m.form.inputs) == 0 {
			return m, nil
		}
		var cmd tea.Cmd
		m.form.inputs[m.form.focus], cmd = m.form.inputs[m.form.focus].Update(msg)
		return m, cmd
	case statePanels:
		var cmd tea.Cmd
		m.cmdInput, cmd = m.cmdInput.Update(msg)
		m.refreshSlash()
		return m, cmd
	}
	return m, nil
}

// onAIResult 收 AI 结果：落工具调用进展示框、解除 busy、按队列续跑。
func (m *osModel) onAIResult(msg aiResultMsg) (tea.Model, tea.Cmd) {
	m.busy = false
	m.aiCancel = nil
	if msg.sess != nil {
		m.sess = msg.sess // 跨轮复用；下一轮继续带历史
	}
	if msg.err == nil {
		m.chatRetry = 0
		m.chatRetrying = false
		m.chatStopReq = false
		m.loopErr = 0
	}
	m.state = statePanels // 内联：结果直接落展示框，不切「AI 粘合报告」弹窗
	m.outScroll = 0       // 新回复吸底，避免用户上翻后看不到回复
	m.report = msg.rep
	m.reportErr = msg.err
	if msg.rep != nil {
		m.appendAnswer(msg.rep) // 没有动作时的自然语言回复
	}
	if strings.TrimSpace(msg.answer) != "" {
		m.appendAnswerText(msg.answer) // agent 运行时的最终回复
	}
	if len(msg.tools) > 0 {
		m.appendToolEvents(msg.tools) // 工具调用：框外逐行
	}
	// 每轮结束自动“进化”：把目标/工具/结论沉淀进工作区经验 skill，下次启动复用。
	if msg.err == nil {
		ans := msg.answer
		if ans == "" && msg.rep != nil {
			ans = msg.rep.Answer
		}
		m.evolveSkill(ans)
		if strings.TrimSpace(ans) != "" {
			// 跨会话长期记忆（向量库）：下次会话可召回。
			addMemRecord(m.base, m.sessionID, m.lastGoal, ans)
		}
	}
	if msg.err != nil {
		m.out = append(m.out, m.styles.err.Render("✗ "+msg.err.Error()))
		if strings.Contains(msg.err.Error(), "API key") {
			m.out = append(m.out, m.styles.hint.Render("💡 未配 key：export OPENROUTER_API_KEY=… 或 GLM_API_KEY=…；或 export HUSHFUSION_AI_PROVIDER=openrouter；按 a 用表单指定 provider/model"))
		}
		if strings.Contains(msg.err.Error(), "context") {
			m.out = append(m.out, m.styles.hint.Render("⏹ AI 已中断"))
		}
	} else if msg.rep != nil {
		// 纯对话（有 Answer、无动作）不再追加空的「AI 回合」。
		if len(msg.rep.Plan) > 0 || len(msg.rep.Results) > 0 || strings.TrimSpace(msg.rep.Answer) == "" {
			m.appendToolCalls(msg.rep)
		}
	}
	// 真实用量入账（provider 上报才有；假路径为 0）。缓存命中率只在"上报过"时展示。
	if msg.rep != nil {
		m.ledger = m.ledger.Merged(msg.rep.Usage)
		m.modelUsed = firstNonEmptyStr(msg.rep.Model, m.modelUsed)
	}
	m.cacheHit = m.cacheLabel()
	// 自主循环：未完成且未到上限 → 立即推进下一轮（用量先入账再续跑）。
	if m.loopActive {
		if msg.err != nil {
			if m.loopStopReq || m.loopErr >= 5 || strings.Contains(msg.err.Error(), "API key") {
				m.loopActive = false
				m.loopWaiting = false
				m.status = m.styles.err.Render("循环因错误停止: " + msg.err.Error())
			} else {
				m.loopErr++
				m.loopWaiting = true
				m.status = m.styles.err.Render(fmt.Sprintf(
					"循环第 %d 次出错，%s 后重试（最多 5 次）", m.loopErr, retryBackoff(m.loopErr)))
				return m, loopTick(retryBackoff(m.loopErr))
			}
		} else {
			m.loopIter++
			ans := msg.answer
			if ans == "" && msg.rep != nil {
				ans = msg.rep.Answer
			}
			done := isGoalDone(ans)
			if done {
				m.loopActive = false
				m.loopWaiting = false
				m.status = m.styles.ok.Render(fmt.Sprintf("✅ 目标完成（%d 轮）", m.loopIter))
			} else if m.loopMax > 0 && m.loopIter >= m.loopMax {
				m.loopActive = false
				m.loopWaiting = false
				m.status = m.styles.err.Render(fmt.Sprintf("⏹ 达到循环上限 %s 轮，目标未标记完成", loopMaxLabel(m.loopMax)))
			} else if m.loopInterval > 0 {
				m.loopWaiting = true
				m.status = m.styles.hint.Render(fmt.Sprintf(
					"⏳ 循环 %s/%s，%s 后开始下一轮", loopIterLabel(m.loopIter), loopMaxLabel(m.loopMax), loopIntervalLabel(m.loopInterval)))
				return m, loopTick(m.loopInterval)
			} else {
				m.status = m.styles.hint.Render(fmt.Sprintf("▶ 循环 %s/%s", loopIterLabel(m.loopIter), loopMaxLabel(m.loopMax)))
				_, cmd := m.startThinking(m.loopPrompt(), "", "")
				return m, cmd
			}
		}
	}
	if msg.compacted && msg.err == nil {
		m.status = m.styles.hint.Render(fmt.Sprintf("🗜 已自动压缩较早轮次（预算 %d tokens，/compress 可手动）", m.budgetTokens))
	}
	// 普通对话的自动纠正：出错且非用户中断时，原句重试，避免一次网络错误就断线。
	if msg.err != nil && !m.loopActive && !m.chatStopReq && m.chatRetry < 3 &&
		strings.TrimSpace(m.lastGoal) != "" && !strings.Contains(msg.err.Error(), "API key") {
		m.chatRetry++
		m.chatRetrying = true
		m.status = m.styles.err.Render(fmt.Sprintf(
			"↻ 网络/模型错误，%s 后自动重试上一句（%d/3）", retryBackoff(m.chatRetry), m.chatRetry))
		return m, chatRetryTick(retryBackoff(m.chatRetry))
	}
	m.reload()
	if len(m.queue) > 0 {
		next := m.queue[0]
		m.queue = m.queue[1:]
		_, cmd := m.startThinking(next, "", "")
		return m, cmd
	}
	return m, nil
}

// onCompressResult 收 /compress 结果：展示摘要预览，不打断当前展示框。
func (m *osModel) onCompressResult(msg compressResultMsg) (tea.Model, tea.Cmd) {
	m.busy = false
	m.aiCancel = nil
	if msg.err != nil {
		m.out = append(m.out, m.styles.err.Render("✗ 压缩失败: "+msg.err.Error()))
		m.status = m.styles.err.Render("压缩失败")
		return m, nil
	}
	if msg.removed == 0 {
		m.status = m.styles.hint.Render("没有可压缩的较早历史（至少先聊两轮）")
		return m, nil
	}
	m.out = append(m.out, m.styles.label.Render(fmt.Sprintf("🗜 已压缩 %d 条较早消息 → 摘要", msg.removed)))
	if s := strings.TrimSpace(msg.summary); s != "" {
		m.out = append(m.out, m.styles.hint.Render("摘要: "+clipSkillText(s, 300)))
	}
	m.status = m.styles.ok.Render(fmt.Sprintf("压缩完成（%d 条 → 摘要）", msg.removed))
	return m, nil
}

func (m *osModel) updateKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	// Ctrl+C 全局语义：忙→中断；空闲→两次退出（第二次落盘并退出）。
	if msg.String() == "ctrl+c" {
		if m.loopActive || m.loopWaiting {
			m.loopActive = false // 中断同时停止自主循环（含等待定时器）
			m.loopWaiting = false
			m.loopStopReq = true
		}
		if m.chatRetrying {
			m.chatRetrying = false // 用户主动中断时不自动重试
			m.chatStopReq = true
		}
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
	// 忙时（AI 思考中）：结果内联在主控制台，仍然只允许 Ctrl+C 中断（上面已处理）
	// 与 q 退出，其余键忽略 —— 与原来 stateThinking 态吞键行为一致。
	if m.busy {
		if key.Matches(msg, km.quit) && msg.String() == "q" {
			return m.doQuit()
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
	case stateModelWizard:
		if m.wiz == nil { // 防御：状态与数据不一致时直接回控制台，不卡死
			m.state = statePanels
			return m, nil
		}
		return m.updateWizard(msg)
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
	// / 命令面板激活时：↑↓ 选择、Enter/Tab 补全或执行、Esc 取消、其它键继续输入并过滤。
	// 正在浏览输入历史时让位给历史（否则历史里的 "/xxx" 会激活面板，↑↓ 就切不动了）。
	if m.slashActive && !m.histBrowsing {
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
	// ↑↓ 浏览输入历史（/ 命令面板激活时优先给面板，上面已 return）。
	switch msg.String() {
	case "up":
		m.historyUp()
		return m, nil
	case "down":
		m.historyDown()
		return m, nil
	}
	// 面板只读：不再用方向键 / 单键快捷键选择面板，所有可打印键都进输入框。
	// （之前输入框为空时按 a/n/o 等会直接开表单，方向键会切换选中面板，属误触源。）
	switch msg.String() {
	case "esc":
		m.cmdInput.SetValue("")
		m.histPos, m.histDraft, m.histBrowsing = len(m.inputHistory), "", false
		return m, nil
	case "enter":
		m.histBrowsing = false
		if t := strings.TrimSpace(m.cmdInput.Value()); t != "" {
			return m.submitCommand(t)
		}
		return m, nil // 空回车不再打开面板详情（避免误触）
	default:
		m.histBrowsing = false // 一旦开始编辑，↑↓ 重新交给 / 命令面板
		var cmd tea.Cmd
		m.cmdInput, cmd = m.cmdInput.Update(msg)
		m.refreshSlash()
		return m, cmd
	}
}

// ---------------------------------------------------------------------------
// 输入历史（↑↓ 回填，仅会话内，不落盘）
// ---------------------------------------------------------------------------

const maxInputHistory = 200

// pushHistory 记录一次提交（连续重复去重、最多 200 条），并重置浏览位置。
func (m *osModel) pushHistory(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if n := len(m.inputHistory); n > 0 && m.inputHistory[n-1] == text {
		m.histPos, m.histDraft, m.histBrowsing = len(m.inputHistory), "", false
		return
	}
	m.inputHistory = append(m.inputHistory, text)
	if len(m.inputHistory) > maxInputHistory {
		m.inputHistory = m.inputHistory[len(m.inputHistory)-maxInputHistory:]
	}
	m.histPos, m.histDraft, m.histBrowsing = len(m.inputHistory), "", false
}

// historyUp 回填更早一条；第一次向上时先存下当前草稿。
func (m *osModel) historyUp() {
	if len(m.inputHistory) == 0 {
		return
	}
	m.histBrowsing = true
	if m.histPos == len(m.inputHistory) {
		m.histDraft = m.cmdInput.Value()
	}
	if m.histPos > 0 {
		m.histPos--
	}
	m.cmdInput.SetValue(m.inputHistory[m.histPos])
	m.cmdInput.CursorEnd()
	m.refreshSlash()
}

// historyDown 回填更新一条；越过最新一条则恢复草稿。
func (m *osModel) historyDown() {
	if len(m.inputHistory) == 0 {
		return
	}
	m.histBrowsing = true
	if m.histPos < len(m.inputHistory) {
		m.histPos++
	}
	if m.histPos >= len(m.inputHistory) {
		m.histPos = len(m.inputHistory)
		m.cmdInput.SetValue(m.histDraft)
	} else {
		m.cmdInput.SetValue(m.inputHistory[m.histPos])
	}
	m.cmdInput.CursorEnd()
	m.refreshSlash()
}

// appendUserEcho 把用户发送的内容回显到回复区：上下两条分隔线包住，
//
//	─────────────
//	❯ hi
//	─────────────
func (m *osModel) appendUserEcho(text string) {
	w := m.width
	if w < 40 {
		w = 80
	}
	// 发送内容独立渲染在 forge 框下方（不在框内），横线用整宽。
	rule := strings.Repeat("─", max(0, w-2))
	m.lastEcho = []string{rule, m.styles.label.Render("❯ ") + text, rule}
	m.sentLog = append(m.sentLog, m.lastEcho...)
}

// submitCommand：忙则入队；否则立即跑（/ 命令 或 AI 目标）。
func (m *osModel) submitCommand(text string) (tea.Model, tea.Cmd) {
	m.cmdInput.SetValue("")
	if text == "" {
		return m, nil
	}
	m.pushHistory(text)
	if m.busy {
		m.queue = append(m.queue, text)
		m.out = append(m.out, m.styles.hint.Render(fmt.Sprintf("⏳ 已入队 (%d): %s", len(m.queue), text)))
		m.status = fmt.Sprintf("已入队，当前队列 %d", len(m.queue))
		return m, nil
	}
	if strings.HasPrefix(text, "/") {
		return m.runSlash(strings.TrimPrefix(text, "/"))
	}
	// 等待下一轮时用户插话：以用户输入为准，停止自主循环，避免两条线并发。
	if m.loopActive && m.loopWaiting {
		m.loopActive = false
		m.loopWaiting = false
		m.status = m.styles.hint.Render("检测到手动输入，自主循环已停止")
	}
	// 用户手动输入 = 纠正反馈：取消待重试，避免和自动重试打架。
	m.chatRetrying = false
	m.chatStopReq = false
	m.chatRetry = 0
	// 没有显式 /goal 时，第一条实质输入成为“对话主线”，每轮注入防漂移。
	if strings.TrimSpace(m.goal) == "" && strings.TrimSpace(m.threadGoal) == "" &&
		len([]rune(strings.TrimSpace(text))) >= 12 {
		m.threadGoal = strings.TrimSpace(text)
	}
	m.goalCount++
	m.appendUserEcho(text)
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
			m.appendUserEcho(goal)
			return m.startThinking(goal, "", "")
		}
		m.openForm("ai")
	case "model":
		return m.slashModel(cmd, fields[0])
	case "compress":
		if m.sess == nil {
			m.status = m.styles.hint.Render("还没有可压缩的会话；先对话一轮")
			break
		}
		m.busy = true
		m.aiStart = time.Now()
		ctx, cancel := context.WithCancel(context.Background())
		m.aiCancel = cancel
		m.status = m.styles.hint.Render("🗜 正在摘要较早轮次…")
		return m, tea.Batch(m.spinner.Tick, compressCmd(ctx, m.sess, 4))
	case "budget":
		if len(fields) > 1 {
			n, err := strconv.Atoi(fields[1])
			if err != nil || n < 1000 {
				m.status = m.styles.err.Render("预算需要 ≥ 1000 tokens")
				break
			}
			m.budgetTokens = n
			if m.sess != nil {
				m.sess.SetBudget(n)
			}
			wc := loadWorkspaceConfig(m.base)
			wc.BudgetTokens = n
			if err := saveWorkspaceConfig(m.base, wc); err != nil {
				m.status = m.styles.err.Render("预算保存失败: " + err.Error())
				break
			}
			m.status = m.styles.ok.Render(fmt.Sprintf("预算 → %d tokens（已写入 %s）", n, workspaceConfigPath(m.base)))
			break
		}
		m.status = m.styles.hint.Render(fmt.Sprintf(
			"预算 %d tokens；模型 %s 上下文 %d（/budget N 修改）",
			m.budgetTokens, m.model, agent.ContextWindowForModel(m.model)))
	case "goal":
		text := strings.TrimSpace(strings.TrimPrefix(cmd, fields[0]))
		if text == "" {
			if m.goal == "" {
				m.status = m.styles.hint.Render("当前没有长期目标；/goal <目标> 设置")
			} else {
				m.status = m.styles.hint.Render("当前目标: " + clipSkillText(m.goal, 120))
			}
			break
		}
		if text == "clear" || text == "none" {
			m.goal = ""
			m.threadGoal = "" // 同时清掉普通对话主线，避免仍按旧目标注入
			if m.sess != nil {
				m.sess.SetGoal("")
			}
			m.loopActive = false
			m.loopWaiting = false
			m.loopErr = 0
			m.status = m.styles.ok.Render("目标已清除")
			break
		}
		m.goal = text
		if m.sess != nil {
			m.sess.SetGoal(text)
		}
		m.status = m.styles.ok.Render("目标 → " + clipSkillText(text, 80))
	case "loop":
		arg := ""
		if len(fields) > 1 {
			arg = strings.ToLower(fields[1])
		}
		switch arg {
		case "stop", "off":
			m.loopActive = false
			m.loopWaiting = false
			m.loopErr = 0
			m.status = m.styles.ok.Render("自主循环已停止")
		case "status":
			if m.loopActive {
				where := fmt.Sprintf("循环中 %s/%s", loopIterLabel(m.loopIter), loopMaxLabel(m.loopMax))
				if m.loopWaiting {
					where = fmt.Sprintf("等待下一轮 %s/%s（间隔 %s）",
						loopIterLabel(m.loopIter+1), loopMaxLabel(m.loopMax), loopIntervalLabel(m.loopInterval))
				} else if m.loopInterval > 0 {
					where += fmt.Sprintf("（间隔 %s）", loopIntervalLabel(m.loopInterval))
				}
				m.status = m.styles.hint.Render(where)
			} else {
				m.status = m.styles.hint.Render("循环未运行（/loop [N] [5m|2h] 启动，/loop stop 停止）")
			}
		default:
			if strings.TrimSpace(m.goal) == "" {
				m.status = m.styles.err.Render("先 /goal <目标> 再 /loop")
				break
			}
			if m.busy {
				m.status = m.styles.err.Render("当前正忙；先 /interrupt 或等本轮结束")
				break
			}
			maxIter := 10
			var interval time.Duration
			for _, a := range fields[1:] {
				if n, err := strconv.Atoi(a); err == nil {
					maxIter = n
					continue
				}
				d, err := parseLoopInterval(a)
				if err != nil {
					m.status = m.styles.err.Render(err.Error())
					return m, nil
				}
				interval = d
			}
			if maxIter < 1 || maxIter > 150 {
				m.status = m.styles.err.Render("/loop 轮数需在 1–150")
				break
			}
			m.loopMax, m.loopIter = maxIter, 0
			m.loopInterval = interval
			m.loopWaiting = false
			m.loopActive = true
			suffix := ""
			if interval > 0 {
				suffix = fmt.Sprintf("，间隔 %s", interval)
			}
			m.status = m.styles.hint.Render(fmt.Sprintf("▶ 自主循环启动（最多 %s 轮%s）", loopMaxLabel(maxIter), suffix))
			return m.startThinking(m.loopPrompt(), "", "")
		}
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
		m.frozenContext = "" // 新会话重新构建快照
		m.sess = nil         // 丢弃旧会话历史
		m.loopActive = false
		m.loopWaiting = false
		m.loopErr = 0
		m.loopStopReq = false
		m.chatRetry = 0
		m.chatRetrying = false
		m.chatStopReq = false
		m.threadGoal = ""
		m.status = m.styles.ok.Render("新会话 " + m.sessionID)
	case "save":
		m.persistSession()
		m.status = m.styles.ok.Render("会话已落盘 " + filepath.Join(sessionsDir(m.base), m.sessionID+".md"))
	case "summary":
		m.appendSummary()
		m.status = m.styles.ok.Render("摘要见展示框")
	case "sessions":
		m.appendSessions()
	case "interrupt":
		if m.busy && m.aiCancel != nil {
			m.loopActive = false
			m.loopWaiting = false
			m.loopStopReq = true
			m.chatRetrying = false
			m.chatStopReq = true
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
	case "reload":
		m.reload()
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
	// TUI 启动路径不经 cmdAI，内置 provider 从未注册时 ai.Builtins() 为空 ——
	// 那样列表是空的、向导也没得选。这里幂等注册（后注册覆盖同名，内置只是兜底）。
	_ = ai.RegisterBuiltins()
	rest := strings.TrimSpace(strings.TrimPrefix(cmd, head))
	if rest == "" {
		// 无参：打开分步向导（provider → API key → 模型 → URL → 确认）。
		return m.openModelWizard()
	}
	if rest == "list" {
		m.out = append(m.out, m.styles.label.Render(fmt.Sprintf("当前模型: %s / %s", m.provider, m.model)))
		m.out = append(m.out, m.styles.hint.Render("用法: /model 打开分步向导 · /model <provider> [model] 快捷切换 · 可用 provider:"))
		for _, p := range ai.Builtins() {
			line := fmt.Sprintf("  %-12s 默认 %s", p.Name, p.DefaultModel)
			if p.APIMode == ai.ModeAnthropicMessages {
				line += "  (未实现, fail-closed)"
			}
			if modelKeySet(p) {
				line += "  key ✓"
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
	m.onModelChanged()
	m.out = append(m.out, m.styles.ok.Render(fmt.Sprintf("✓ 已切换模型 → %s / %s", m.provider, m.model)))
	if !modelKeySet(prof) {
		m.out = append(m.out, m.styles.err.Render(fmt.Sprintf("⚠ 该 provider 似未配 key（env: %s）", strings.Join(prof.EnvVars, " / "))))
	}
	m.status = m.styles.ok.Render(fmt.Sprintf("model → %s/%s", m.provider, m.model))
	return m, nil
}

// openModelWizard 打开分步设置向导（只列已实现 chat_completions 的 provider）。
// 顺序：provider → API key（keep/replace/clear）→ 模型（可筛选选择）→ URL → 确认。
func (m *osModel) openModelWizard() (tea.Model, tea.Cmd) {
	_ = ai.RegisterBuiltins() // 幂等兜底（直接调用本方法也不至于拿到空列表）
	w := &modelWizard{keyAction: "keep"}
	for _, p := range ai.Builtins() {
		// Builtins() 是原始字面量，空 APIMode 由 Validate() 补成 chat_completions ——
		// 这里必须先归一化再判断，否则会把「没写 APIMode」的 provider 全筛掉。
		mode := p.APIMode
		if mode == "" {
			mode = ai.ModeChatCompletions
		}
		if mode != ai.ModeChatCompletions {
			continue // 未实现的模式不上选项：选了也只会 fail-closed
		}
		w.providers = append(w.providers, p)
	}
	w.provFilter = newWizInput("搜索 provider…")
	w.provFilter.SetWidth(40)
	w.provFilter.Focus()
	w.provShown = w.providers
	for i, p := range w.provShown {
		if p.Name == m.provider {
			w.cursor = i
		}
	}
	w.key = newWizInput("输入新 API key（掩码，不落盘）")
	w.key.EchoMode = textinput.EchoPassword // 输入不显形
	w.filter = newWizInput("搜索模型…")
	w.filter.SetWidth(40)
	w.url = newWizInput("Base URL")
	m.wiz = w
	m.state = stateModelWizard
	m.status = m.styles.hint.Render("模型向导：输入关键词搜索 provider · ↑↓ 选择 · Enter 下一步 · Esc 取消")
	return m, nil
}

// applyProviderFilter 按搜索栏内容过滤 provider 列表（名称/显示名/默认模型）。
func (m *osModel) applyProviderFilter() {
	w := m.wiz
	q := strings.ToLower(strings.TrimSpace(w.provFilter.Value()))
	shown := make([]*ai.Profile, 0, len(w.providers))
	for _, p := range w.providers {
		if q == "" ||
			strings.Contains(strings.ToLower(p.Name), q) ||
			strings.Contains(strings.ToLower(p.DisplayName), q) ||
			strings.Contains(strings.ToLower(p.DefaultModel), q) {
			shown = append(shown, p)
		}
	}
	w.provShown = shown
	if w.cursor >= len(shown) {
		w.cursor = len(shown) - 1
	}
	if w.cursor < 0 {
		w.cursor = 0
	}
}

func newWizInput(placeholder string) textinput.Model {
	ti := textinput.New()
	ti.Placeholder = placeholder
	ti.SetWidth(64)
	// 亮色正文（默认主题在深色终端上偏暗，用户反馈「字体颜色太暗」）。
	st := textinput.DefaultDarkStyles()
	st.Focused.Text = lipgloss.NewStyle().Foreground(lipgloss.Color("255"))
	st.Focused.Placeholder = lipgloss.NewStyle().Foreground(lipgloss.Color("246"))
	st.Blurred.Text = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	st.Blurred.Placeholder = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	ti.SetStyles(st)
	return ti
}

// enterModelStep 进入模型步：组装列表（最近→常用→已解析→默认），默认选中
// DefaultModel，并异步拉 /models 补全「解析」分组。
func (m *osModel) enterModelStep() (tea.Model, tea.Cmd) {
	w := m.wiz
	w.step = wizStepModel
	w.filter.SetValue("")
	w.filter.Focus()
	w.modelSel = 0
	w.resolved = nil
	w.modelsBusy = true
	w.recent = loadRecentModels(m.base)[w.provider.Name]
	w.modelAll = buildWizardModelList(m.base, w.provider, nil)
	w.model = w.provider.DefaultModel
	if w.model != "" {
		for i, id := range w.modelAll {
			if id == w.provider.DefaultModel {
				w.modelSel = i
				break
			}
		}
	}
	m.applyModelFilter()
	return m, m.fetchModelsCmd(w.provider, m.wizardKeyValue(), w.url.Value())
}

// onWizModels 收 /models 异步结果：合并进「解析」并重排，不打断用户输入。
func (m *osModel) onWizModels(msg wizModelsMsg) (tea.Model, tea.Cmd) {
	if m.wiz == nil || m.wiz.provider == nil || m.wiz.step != wizStepModel || m.wiz.provider.Name != msg.provider {
		return m, nil
	}
	w := m.wiz
	prev := ""
	if w.modelSel >= 0 && w.modelSel < len(w.modelShown) {
		prev = w.modelShown[w.modelSel]
	}
	w.modelsBusy = false
	if len(msg.models) > 0 {
		w.resolved = msg.models
	}
	w.modelAll = buildWizardModelList(m.base, w.provider, w.resolved)
	m.applyModelFilter()
	// custom / local：若仍停在占位默认，真实 /models 加载后自动选第一个。
	if (w.provider.Name == "custom" || w.provider.Name == "local") &&
		len(w.resolved) > 0 && prev == w.provider.DefaultModel {
		w.model = w.resolved[0]
		for i, id := range w.modelShown {
			if id == w.model {
				w.modelSel = i
				break
			}
		}
		return m, nil
	}
	// 其余 provider：保持原选中项（列表重排后按 id 重新定位）。
	for i, id := range w.modelShown {
		if id == prev {
			w.modelSel = i
			break
		}
	}
	return m, nil
}

// applyModelFilter 按筛选框内容过滤 modelAll → modelShown，并收敛 selection。
func (m *osModel) applyModelFilter() {
	w := m.wiz
	q := strings.ToLower(strings.TrimSpace(w.filter.Value()))
	shown := make([]string, 0, len(w.modelAll))
	for _, id := range w.modelAll {
		if q == "" || strings.Contains(strings.ToLower(id), q) {
			shown = append(shown, id)
		}
	}
	w.modelShown = shown
	if w.modelSel >= len(shown) {
		w.modelSel = len(shown) - 1
	}
	if w.modelSel < 0 {
		w.modelSel = 0
	}
}

// pickWizardModel 决定模型步 Enter 选中的 id：
//   - custom/local：输入框内容即模型 id（列表只是建议，支持任意名字/型号）；
//   - 其它 provider：以列表选中项为准，无匹配时才把关键词当 id。
func pickWizardModel(w *modelWizard) string {
	if w == nil {
		return ""
	}
	f := strings.TrimSpace(w.filter.Value())
	if (w.provider != nil && (w.provider.Name == "custom" || w.provider.Name == "local")) && f != "" {
		return f
	}
	if len(w.modelShown) > 0 {
		return w.modelShown[w.modelSel]
	}
	return f
}

// wizardKeyValue 返回向导里「显式」的 key（仅 replace 且已输入时）；否则空串
// 交给 ListModels / 调用方按环境变量解析。
func (m *osModel) wizardKeyValue() string {
	if m.wiz == nil || m.wiz.keyAction != "replace" {
		return ""
	}
	return strings.TrimSpace(m.wiz.key.Value())
}

// fetchModelsCmd 异步拉 provider 的 /models（短超时，失败静默 —— 内置目录仍在）。
func (m *osModel) fetchModelsCmd(p *ai.Profile, key, url string) tea.Cmd {
	if p == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		ids, err := ai.ListModels(ctx, p.Name, ai.ClientOptions{APIKey: key, BaseURL: url, AllowNoKey: true})
		return wizModelsMsg{provider: p.Name, models: ids, err: err}
	}
}

// updateWizard 处理向导按键。
func (m *osModel) updateWizard(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	w := m.wiz
	switch w.step {
	case wizStepProvider:
		switch msg.String() {
		case "esc":
			m.wiz, m.state = nil, statePanels
			m.status = m.styles.err.Render("已取消模型设置")
		case "up":
			if w.cursor > 0 {
				w.cursor--
			}
		case "down":
			if w.cursor < len(w.provShown)-1 {
				w.cursor++
			}
		case "pgup":
			w.cursor -= m.wizListRows()
			if w.cursor < 0 {
				w.cursor = 0
			}
		case "pgdown":
			w.cursor += m.wizListRows()
			if w.cursor >= len(w.provShown) {
				w.cursor = len(w.provShown) - 1
			}
			if w.cursor < 0 {
				w.cursor = 0
			}
		case "home":
			w.cursor = 0
		case "end":
			w.cursor = len(w.provShown) - 1
			if w.cursor < 0 {
				w.cursor = 0
			}
		case "enter":
			if len(w.provShown) == 0 {
				m.status = m.styles.err.Render("没有匹配的 provider")
				return m, nil
			}
			w.provider = w.provShown[w.cursor]
			w.url.SetValue(w.provider.BaseURL) // 预填 profile 默认，可改成内网网关
			w.keyAction, w.keyEditing = "keep", false
			w.key.SetValue("")
			w.provFilter.Blur()
			w.step = wizStepKey // 先填 API，后选模型和 URL
		default:
			var cmd tea.Cmd
			w.provFilter, cmd = w.provFilter.Update(msg)
			m.applyProviderFilter()
			return m, cmd
		}
		return m, nil

	case wizStepKey:
		if w.keyEditing { // replace 子态：输入新 key
			switch msg.String() {
			case "esc":
				w.keyEditing = false
				w.key.Blur()
				w.key.SetValue("")
			case "enter":
				if strings.TrimSpace(w.key.Value()) == "" {
					m.status = m.styles.err.Render("key 为空：继续输入，或 Esc 返回选 keep/clear")
					return m, nil
				}
				w.keyEditing = false
				w.key.Blur()
				return m.enterModelStep()
			default:
				var cmd tea.Cmd
				w.key, cmd = w.key.Update(msg)
				return m, cmd
			}
			return m, nil
		}
		switch msg.String() {
		case "esc":
			w.step = wizStepProvider
		case "k", "enter":
			w.keyAction = "keep"
			return m.enterModelStep()
		case "r":
			w.keyAction = "replace"
			w.keyEditing = true
			w.key.SetValue("")
			w.key.Focus()
		case "c":
			w.keyAction = "clear"
			return m.enterModelStep()
		}
		return m, nil

	case wizStepModel:
		switch msg.String() {
		case "esc":
			w.filter.Blur()
			w.step = wizStepKey
			return m, nil
		case "up":
			if w.modelSel > 0 {
				w.modelSel--
			}
			return m, nil
		case "down":
			if w.modelSel < len(w.modelShown)-1 {
				w.modelSel++
			}
			return m, nil
		case "pgup":
			w.modelSel -= m.wizListRows()
			if w.modelSel < 0 {
				w.modelSel = 0
			}
			return m, nil
		case "pgdown":
			w.modelSel += m.wizListRows()
			if w.modelSel >= len(w.modelShown) {
				w.modelSel = len(w.modelShown) - 1
			}
			if w.modelSel < 0 {
				w.modelSel = 0
			}
			return m, nil
		case "home":
			w.modelSel = 0
			return m, nil
		case "end":
			w.modelSel = len(w.modelShown) - 1
			if w.modelSel < 0 {
				w.modelSel = 0
			}
			return m, nil
		case "enter":
			w.model = pickWizardModel(w)
			if w.model == "" {
				m.status = m.styles.err.Render("请先选择模型（↑↓ 或输入关键词）")
				return m, nil
			}
			w.filter.Blur()
			w.step = wizStepURL
			w.url.Focus()
			return m, nil
		default:
			var cmd tea.Cmd
			w.filter, cmd = w.filter.Update(msg)
			m.applyModelFilter()
			return m, cmd
		}

	case wizStepURL:
		switch msg.String() {
		case "esc":
			w.url.Blur()
			w.step = wizStepModel
			w.filter.Focus()
			return m, nil
		case "enter":
			w.url.Blur()
			w.step = wizStepConfirm
			return m, nil
		default:
			var cmd tea.Cmd
			w.url, cmd = w.url.Update(msg)
			return m, cmd
		}

	case wizStepConfirm:
		switch msg.String() {
		case "esc":
			w.step = wizStepURL
			w.url.Focus()
		case "enter":
			return m.applyWizard()
		}
	}
	return m, nil
}

// applyWizard 应用向导结果：切模型 + 按 keep/replace/clear 处理 key + 设 URL。
//
// 为什么写 env 而不是配置文件：本仓纪律是密钥只经环境变量/受控注入，不落盘、
// 不进日志、不进会话文件。代价是只对本进程有效 —— 这一点在输出里明说。
func (m *osModel) applyWizard() (tea.Model, tea.Cmd) {
	w := m.wiz
	prof := w.provider
	if prof == nil {
		m.wiz, m.state = nil, statePanels
		m.status = m.styles.err.Render("未选择 provider")
		return m, nil
	}
	model := orDefault(strings.TrimSpace(w.model), prof.DefaultModel)
	if model == "" {
		m.status = m.styles.err.Render("尚未选定模型；请回到 model 步选择")
		w.step = wizStepModel
		w.filter.Focus()
		return m, nil
	}
	url := strings.TrimSpace(w.url.Value())

	m.provider, m.model = prof.Name, model
	m.onModelChanged()
	keyNote := ""
	keyForConfig := ""
	switch w.keyAction {
	case "replace":
		k := strings.TrimSpace(w.key.Value())
		if k == "" {
			m.status = m.styles.err.Render("replace 但 key 为空；请重填或改选 keep")
			w.step = wizStepKey
			w.keyEditing = true
			w.key.Focus()
			return m, nil
		}
		keyForConfig = k
		if len(prof.EnvVars) > 0 {
			_ = os.Setenv(prof.EnvVars[0], k)
			keyNote = fmt.Sprintf("  key 已替换：写入本进程环境 + workspace config（%s，0600）", prof.EnvVars[0])
		}
	case "clear":
		for _, e := range prof.EnvVars {
			_ = os.Unsetenv(e)
		}
		keyForConfig = ""
		keyNote = fmt.Sprintf("  key 已清除（env + workspace config，%s）", strings.Join(prof.EnvVars, "/"))
	default: // keep：把当前生效 key 同步进 config，下次启动直接可用
		if c := loadWorkspaceConfig(m.base); c.APIKey != "" {
			keyForConfig = c.APIKey
		} else {
			keyForConfig = ai.ResolveAPIKey(prof)
		}
		if modelKeySet(prof) {
			keyNote = "  key 沿用现有环境变量（keep，已同步到 workspace config）"
		}
	}
	// Base URL 总是显式设定（空则清掉），避免沿用上一个 provider 的残留覆盖。
	if url != "" {
		_ = os.Setenv("HUSHFUSION_AI_BASE_URL", url)
	} else {
		_ = os.Unsetenv("HUSHFUSION_AI_BASE_URL")
	}
	// 持久化到 workspace config（含 key，0600、已 gitignore）；下次启动自动注入 env。
	wc := loadWorkspaceConfig(m.base) // 保留 lsp_servers / budget_tokens 等既有配置
	wc.Provider, wc.Model, wc.BaseURL, wc.APIKey = prof.Name, model, url, keyForConfig
	cfgErr := saveWorkspaceConfig(m.base, wc)
	recordRecentModel(m.base, prof.Name, model) // 最近使用落盘（best-effort）

	m.out = append(m.out, m.styles.ok.Render(fmt.Sprintf("✓ 模型 → %s / %s", m.provider, m.model)))
	if url != "" {
		m.out = append(m.out, m.styles.hint.Render("  base-url: "+url))
	}
	if keyNote != "" {
		m.out = append(m.out, m.styles.hint.Render(keyNote))
	}
	if cfgErr != nil {
		m.out = append(m.out, m.styles.err.Render("  ⚠ workspace config 写入失败: "+cfgErr.Error()))
	} else {
		m.out = append(m.out, m.styles.hint.Render("  config: "+workspaceConfigPath(m.base)+"（0600，gitignore）"))
	}
	if !modelKeySet(prof) {
		m.out = append(m.out, m.styles.err.Render(fmt.Sprintf("⚠ 该 provider 仍未配 key（env: %s）",
			strings.Join(prof.EnvVars, "/"))))
	}
	m.status = m.styles.ok.Render(fmt.Sprintf("model → %s/%s", m.provider, m.model))
	m.wiz, m.state = nil, statePanels
	return m, nil
}

// wizardBody 渲染向导当前步骤（不再显示「步骤 N/5」提示，聚焦当前步内容）。
func (m *osModel) wizardBody() string {
	w := m.wiz
	if w == nil {
		return ""
	}
	if w.step == wizStepProvider {
		return m.renderProviderStep()
	}
	var b strings.Builder
	// 已选 provider 的上下文（加粗亮蓝）。
	b.WriteString(m.styles.accent.Render(w.provider.Name) + m.styles.bright.Render("  "+w.provider.DisplayName) + "\n\n")

	switch w.step {
	case wizStepKey:
		b.WriteString(m.renderKeyStep())
	case wizStepModel:
		b.WriteString(m.renderModelStep())
	case wizStepURL:
		b.WriteString(m.styles.accent.Render("Base URL") + "\n" + w.url.View() + "\n\n")
		b.WriteString(m.renderHintBox("Enter 下一步 · Esc 上一步"))
	default: // wizStepConfirm
		b.WriteString(m.styles.accent.Render("确认后立即生效") + "\n\n")
		row := func(label, val string) {
			b.WriteString(m.styles.accent.Render(fmt.Sprintf("%-10s", label)) + m.styles.bright.Render(val) + "\n")
		}
		row("provider", w.provider.Name)
		row("model", orDefault(w.model, w.provider.DefaultModel))
		row("api key", w.keyActionLabel())
		row("base url", orDefault(strings.TrimSpace(w.url.Value()), "（空 = provider 默认）"))
		b.WriteString("\n" + m.renderHintBox("Enter 应用 · Esc 返回修改"))
	}
	return b.String()
}

// renderProviderStep 渲染 provider 步：搜索栏 + 加粗蓝名称列表（选中行蓝底白字）。
func (m *osModel) renderProviderStep() string {
	w := m.wiz
	var b strings.Builder
	b.WriteString(m.renderSearchBar(m.styles.accent.Render("搜索 provider")+"  ", w.provFilter))
	rows := m.wizListRows()
	b.WriteString(m.styles.bright.Render(fmt.Sprintf("  %d 个候选", len(w.provShown))) + "\n")
	if len(w.provShown) == 0 {
		b.WriteString(m.styles.bright.Render("  无匹配；换个关键词试试") + "\n")
	}
	start, end := windowAround(w.cursor, len(w.provShown), rows)
	for i := start; i < end; i++ {
		p := w.provShown[i]
		keyState := "无 key"
		if modelKeySet(p) {
			keyState = "key ✓"
		}
		name := clipRunes(p.Name, 12)
		disp := clipRunes(p.DisplayName, 18)
		def := clipRunes(p.DefaultModel, 15)
		if i == w.cursor {
			line := fmt.Sprintf("❯ %-12s %-20s 默认 %-16s %s", name, disp, def, keyState)
			b.WriteString(m.styles.sel.Render(clipWizLine(line, 68)) + "\n")
		} else {
			b.WriteString("  " + m.styles.accent.Render(fmt.Sprintf("%-12s", name)) +
				m.styles.bright.Render(fmt.Sprintf(" %-20s 默认 %-16s %s", disp, def, keyState)) + "\n")
		}
	}
	b.WriteString(m.scrollHint(w.cursor, len(w.provShown), rows))
	b.WriteString("\n" + m.renderHintBox("输入关键词搜索 · ↑↓ 选择 · Enter 下一步 · Esc 取消"))
	return b.String()
}

// renderSearchBar 渲染带蓝色下划线的搜索栏（亮色正文）。
func (m *osModel) renderSearchBar(label string, input textinput.Model) string {
	return m.styles.searchBar.Render("🔍 "+label+input.View()) + "\n"
}

// renderHintBox 把操作提示放进亮眼圆角框（蓝底白字加粗）。
func (m *osModel) renderHintBox(text string) string {
	return m.styles.hintBox.Render(text) + "\n"
}

// renderKeyStep 渲染 API key 步（keep / replace / clear 三选一，或 replace 输入态）。
func (m *osModel) renderKeyStep() string {
	w := m.wiz
	var b strings.Builder
	envs := strings.Join(w.provider.EnvVars, "/")
	state := "无 key"
	if modelKeySet(w.provider) {
		state = "已有 key（env: " + envs + "）"
	}
	b.WriteString(m.styles.bright.Render("当前: "+state) + "\n\n")
	if w.keyEditing {
		b.WriteString(m.styles.accent.Render("新 API key（掩码，不落盘）") + "\n" + w.key.View() + "\n\n")
		b.WriteString(m.renderHintBox("Enter 确认 · Esc 返回"))
		return b.String()
	}
	for _, o := range []struct{ k, label string }{
		{"k", "keep（沿用现有环境变量；无则保持不变）"},
		{"r", "replace（输入新 key）"},
		{"c", "clear（清除该 provider 的 key）"},
	} {
		b.WriteString("  " + m.styles.accent.Render("["+o.k+"]") + " " + m.styles.bright.Render(o.label) + "\n")
	}
	b.WriteString("\n" + m.renderHintBox("按 k / r / c 选择 · Esc 上一步"))
	return b.String()
}

// renderModelStep 渲染模型步：搜索栏 + 可滚动列表（名称蓝粗、选中蓝底白字）。
// 列表行数随终端高度自适应；↑↓ 逐条移动，PgUp/PgDn 翻页，Home/End 首尾。
func (m *osModel) renderModelStep() string {
	w := m.wiz
	var b strings.Builder
	b.WriteString(m.renderSearchBar(m.styles.accent.Render("搜索模型")+"  ", w.filter))
	busy := ""
	if w.modelsBusy {
		busy = "  · 解析 /models 中…"
	}
	rows := m.wizListRows()
	b.WriteString(m.styles.bright.Render(fmt.Sprintf("  %d 个候选%s", len(w.modelShown), busy)) + "\n")
	if len(w.modelShown) == 0 {
		if strings.TrimSpace(w.filter.Value()) != "" {
			b.WriteString(m.styles.bright.Render("  无匹配 —— Enter 可把关键词作为自定义模型 id") + "\n")
		} else {
			b.WriteString(m.styles.bright.Render("  无内置候选；输入关键词可直接指定模型 id") + "\n")
		}
	}
	start, end := windowAround(w.modelSel, len(w.modelShown), rows)
	for i := start; i < end; i++ {
		full := w.modelShown[i]
		tag := m.modelTag(full)
		id := clipRunes(full, 40)
		if i == w.modelSel {
			line := fmt.Sprintf("❯ %-40s %s", id, tag)
			b.WriteString(m.styles.sel.Render(clipWizLine(line, 72)) + "\n")
		} else {
			b.WriteString("  " + m.styles.accent.Render(fmt.Sprintf("%-40s", id)) +
				" " + m.styles.bright.Render(tag) + "\n")
		}
	}
	b.WriteString(m.scrollHint(w.modelSel, len(w.modelShown), rows))
	b.WriteString("\n" + m.renderHintBox("↑↓ 选择 · PgUp/PgDn 翻页 · 输入可筛选/自定义 id · Enter 确认 · Esc 返回"))
	return b.String()
}

// scrollHint 渲染「当前区间/总数」的滚动指示（只有溢出时才显示）。
func (m *osModel) scrollHint(sel, total, rows int) string {
	if total <= rows {
		return ""
	}
	start, end := windowAround(sel, total, rows)
	return m.styles.bright.Render(fmt.Sprintf("  …（%d–%d / %d，PgUp/PgDn 翻页）…\n", start+1, end, total))
}

// wizListRows 是向导列表可用的行数（随终端高度自适应，给弹窗留足空间）。
// 对话框样式 = DoubleBorder(2 行) + Padding(1,2)(2 行) + 标题/筛选/计数/滚动/提示
// 等固定行 —— 这里按 contentH - 14 预留，确保列表完整可见、不靠终端裁切。
func (m *osModel) wizListRows() int {
	contentH := m.height - (strings.Count(logoBanner, "\n") + 1) - 4
	if contentH < 6 {
		contentH = 6
	}
	rows := contentH - 14
	if rows < 3 {
		rows = 3
	}
	if rows > 40 {
		rows = 40
	}
	return rows
}

// modelTag 给模型打来源标记（最近 / 默认 / 常用 / 解析）。
func (m *osModel) modelTag(id string) string {
	w := m.wiz
	if w == nil || w.provider == nil {
		return ""
	}
	for _, x := range w.recent {
		if x == id {
			return "[最近]"
		}
	}
	if id == w.provider.DefaultModel {
		return "[默认]"
	}
	for _, x := range commonModels[w.provider.Name] {
		if x == id {
			return "[常用]"
		}
	}
	for _, x := range w.resolved {
		if x == id {
			return "[解析]"
		}
	}
	return ""
}

// keyActionLabel 是 key 步的小结文本（keep/replace/clear）。
func (w *modelWizard) keyActionLabel() string {
	switch w.keyAction {
	case "replace":
		return keyPreview(strings.TrimSpace(w.key.Value()), w.provider)
	case "clear":
		return "clear（已选清除）"
	default:
		if modelKeySet(w.provider) {
			return "keep（沿用环境变量）"
		}
		return "keep（当前无 key）"
	}
}

// windowAround 返回 [start,end) 窗口，长度约 size，尽量让 sel 居中。
func windowAround(sel, n, size int) (int, int) {
	if size <= 0 {
		size = 1
	}
	if n <= size {
		return 0, n
	}
	start := sel - size/2
	if start < 0 {
		start = 0
	}
	end := start + size
	if end > n {
		end = n
		start = end - size
	}
	if start < 0 {
		start = 0
	}
	return start, end
}

// clipWizLine 按 rune 裁剪到 n 列（保留前导缩进，不 TrimSpace）。
func clipWizLine(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// clipRunes 把字符串按 rune 截断到 n 个字符（不加省略号，供定宽列对齐）。
func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// keyPreview 把 key 变成可展示的摘要：永远不打印明文。
func keyPreview(key string, p *ai.Profile) string {
	envs := strings.Join(p.EnvVars, "/")
	if key == "" {
		if modelKeySet(p) {
			return "沿用环境变量（" + envs + "）"
		}
		return "未配置 —— 调用会失败（env: " + envs + "）"
	}
	tail := ""
	if len(key) > 4 {
		tail = key[len(key)-4:]
	}
	return strings.Repeat("•", 6) + tail + "（仅本进程环境，不落盘）"
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
	m.out = append(m.out, m.styles.label.Render("── 历史会话 ("+sessionsDir(m.base)+") ──"))
	dir := sessionsDir(m.base)
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
	oldProvider, oldModel := m.provider, m.model
	if provider == "" {
		provider = m.provider
	}
	if model == "" {
		model = m.model
	}
	// 会话绑定 provider/model；切换后必须重建，否则会用旧模型的历史和 chat。
	if m.sess != nil && (provider != oldProvider || model != oldModel) {
		m.sess = nil
	}
	m.provider = provider
	m.model = model
	m.state = statePanels // 内联：留在控制台，仅用 busy+spinner 内联指示「思考中」
	m.thinkingGoal = goal
	m.lastGoal = goal
	m.thinkFrame = 0
	m.report, m.reportErr = nil, nil
	m.toolLog = nil // 新一轮：工具调用列表重新开始
	m.status = ""
	m.busy = true
	m.aiStart = time.Now()
	// 首次 AI 调用前构建一次冻结快照；之后每轮原样注入，不重建。
	fresh := m.frozenContext == ""
	if fresh {
		m.frozenContext = m.buildFrozenContext()
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.aiCancel = cancel
	cmd := tea.Batch(m.spinner.Tick, thinkTick(),
		runAICmd(ctx, m.base, goal, provider, model, m.frozenContext, m.sess, m.budgetTokens, m.currentGoal()))
	if fresh {
		m.status = m.styles.hint.Render(fmt.Sprintf(
			"❄ 已冻结上下文快照（%d 字符）：soul/agent/user + skills + 经验 + 记忆", len([]rune(m.frozenContext))))
	}
	return m, cmd
}

// isGoalDone 识别模型是否在最终回复里声明目标完成。
func isGoalDone(answer string) bool {
	return strings.Contains(strings.ToUpper(answer), "GOAL_DONE")
}

// parseLoopInterval 解析 /loop 的间隔：30s、5m、2h（10s–24h）。
func parseLoopInterval(s string) (time.Duration, error) {
	d, err := time.ParseDuration(strings.TrimSpace(s))
	if err != nil {
		return 0, fmt.Errorf("间隔格式：30s / 5m / 2h")
	}
	if d < 10*time.Second || d > 24*time.Hour {
		return 0, fmt.Errorf("间隔需在 10s–24h")
	}
	return d, nil
}

// onLoopTick 定时器到点：没在忙就推进下一轮；忙则稍后再看，避免并发。
func (m *osModel) onLoopTick() (tea.Model, tea.Cmd) {
	if !m.loopActive {
		m.loopWaiting = false
		return m, nil
	}
	if m.busy {
		d := m.loopInterval
		if d <= 0 {
			d = time.Second
		}
		return m, loopTick(d)
	}
	m.loopWaiting = false
	return m.startThinking(m.loopPrompt(), "", "")
}

// loopPrompt 是自主循环每轮发给模型的推进指令（长期目标由 Session.Goal 注入）。
func (m *osModel) loopPrompt() string {
	if m.loopIter == 0 {
		return "开始执行当前长期目标。每轮只做可验证的一步；完成时在最终回复末尾单独一行输出 `GOAL_DONE: <证据>`。"
	}
	return fmt.Sprintf("继续推进当前长期目标（第 %d/%d 轮）。完成时输出 `GOAL_DONE: <证据>`；未完成则说明下一步并继续。", m.loopIter+1, m.loopMax)
}

// onModelChanged 切换模型后重建会话：旧会话的 chat/model 仍是旧 provider。
// 没有显式 /budget 时，预算随新模型上下文窗口自动重算。
func loopIterLabel(n int) string { return fmt.Sprintf("%d", n) }

func loopMaxLabel(n int) string {
	if n <= 0 {
		return "∞"
	}
	return fmt.Sprintf("%d", n)
}

func loopIntervalLabel(d time.Duration) string {
	if d <= 0 {
		return "无间隔"
	}
	return d.String()
}

// currentGoal 返回本轮应注入的目标：显式 /goal 优先，否则用对话主线。
func (m *osModel) currentGoal() string {
	if strings.TrimSpace(m.goal) != "" {
		return m.goal
	}
	return m.threadGoal
}

// chatRetryMsg 是普通对话出错后的自动重试定时器。
type chatRetryMsg struct{}

func chatRetryTick(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return chatRetryMsg{} })
}

// retryBackoff 出错重试的退避：5s、10s、20s…封顶 10m。
func retryBackoff(n int) time.Duration {
	if n < 1 {
		n = 1
	}
	d := time.Duration(1<<(n-1)) * 5 * time.Second
	if d > 10*time.Minute {
		d = 10 * time.Minute
	}
	return d
}

// onChatRetry 普通对话的自动纠正：不修改用户目标，原样重发上一句，让对话继续。
func (m *osModel) onChatRetry() (tea.Model, tea.Cmd) {
	if m.chatStopReq || !m.chatRetrying || m.busy {
		return m, nil
	}
	if strings.TrimSpace(m.lastGoal) == "" {
		m.chatRetrying = false
		return m, nil
	}
	m.chatRetrying = false
	m.out = append(m.out, m.styles.hint.Render(
		fmt.Sprintf("↻ 自动重试上一句（%d/3）：%s", m.chatRetry, clipSkillText(m.lastGoal, 80))))
	return m.startThinking(m.lastGoal, "", "")
}

// onModelChanged 切换模型后重建会话：旧会话的 chat/model 仍是旧 provider。
// 没有显式 /budget 时，预算随新模型上下文窗口自动重算。
func (m *osModel) onModelChanged() {
	m.sess = nil
	if loadWorkspaceConfig(m.base).BudgetTokens <= 0 {
		m.budgetTokens = agent.BudgetForModel(m.model, 0.6)
	}
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
		m.appendUserEcho(goal)
		return m.startThinking(goal, provider, model)
	}
	m.reload()
	m.form = nil
	m.state = statePanels
	return m, nil
}

func runAICmd(ctx context.Context, base, goal, provider, model, extraSystem string, sess *agent.Session, budget int, longGoal string) tea.Cmd {
	return func() tea.Msg {
		ch := make(chan tea.Msg, 256)
		go func() {
			baseURL := os.Getenv("HUSHFUSION_AI_BASE_URL")
			if sess == nil {
				cfg := agent.DefaultConfig()
				cfg.Provider, cfg.Model, cfg.BaseURL = provider, model, baseURL
				cfg.MaxBudgetTokens = budget
				cfg.Root = repoRoot()
				chat := func(ctx context.Context, req ai.ChatRequest) (*ai.ChatResponse, error) {
					return ai.Chat(ctx, provider, req, ai.ClientOptions{BaseURL: baseURL})
				}
				sys := agentSystemPrompt()
				if strings.TrimSpace(extraSystem) != "" {
					sys += "\n\n" + extraSystem
				}
				// 冻结快照已由 startThinking 构建并通过 extraSystem 传入；
				// 这里不再动态读取经验/记忆，保证“每轮看到的是同一份快照”。
				sess = agent.NewSession(cfg, chat, agent.NewRegistry(), sys)
				skills := agent.NewSkills([]string{globalSkillsDir(base), projectSkillsDir(base)}, agent.NewKeywordMatcher())
				sess.Skills = skills
				_ = sess.RegisterBuiltinTools(skills) // read / ls / shell / mem.* / tasks.* / skill.view / delegate
				registerProjectTools(sess, base)      // fusion.* / plant.* / plugin.* / mcp.* / acp.* / diap.*
				// shell 默认 fail-closed；只有显式 HUSHFUSION_ALLOW_SHELL=1 才放行。
				if os.Getenv("HUSHFUSION_ALLOW_SHELL") == "1" {
					sess.AllowWrites["shell"] = true
				}
				allowPlatformWrites(sess)
			} else {
				sess.SetBudget(budget) // /budget 改动下一轮生效
				allowPlatformWrites(sess)
			}
			sess.SetGoal(longGoal) // /goal 每轮动态注入，不占历史
			// 工具执行完立刻投递一行；最终回复最后到达。
			// 每轮换新通道（复用会话时旧的 OnTool 仍指向上一个已结束的通道）。
			sess.OnTool = func(ev agent.ToolEvent) {
				ch <- toolEventMsg{ev: ev, ch: ch}
			}
			wasCompacted := sess.CompactedReported()
			reply, err := sess.Turn(ctx, goal)
			ch <- aiResultMsg{
				answer: reply, err: err, sess: sess,
				compacted: !wasCompacted && sess.CompactedReported(),
			}
		}()
		return <-ch
	}
}

// compressCmd 手动把较早历史摘要掉（/compress）。
func compressCmd(ctx context.Context, sess *agent.Session, keep int) tea.Cmd {
	return func() tea.Msg {
		removed, summary, err := sess.Compress(ctx, keep)
		return compressResultMsg{removed: removed, summary: summary, err: err}
	}
}

func (m *osModel) reload() {
	if s, err := osview.Load(m.base); err == nil {
		m.snap = s
		m.panels = buildPanels(s)
		if m.detail >= len(m.panels) {
			m.detail = 0
		}
	}
}

// appendToolCalls 把一次 AI 回合的计划/执行/备注作为“工具调用”落进展示框。
// appendAnswer 渲染「没有可执行动作」时的自然语言回复（聊天式回显）。
func (m *osModel) appendAnswer(rep *aios.Report) {
	if rep == nil {
		return
	}
	m.appendAnswerText(rep.Answer)
}

// appendAnswerText 把最终文字回复放进 forge 框。
func (m *osModel) appendAnswerText(ans string) {
	ans = strings.TrimSpace(ans)
	if ans == "" {
		return
	}
	// 上一条回复收进历史：每条回复独立成框，绝不与下一条合并。
	if len(m.out) > 0 {
		m.replyBlocks = append(m.replyBlocks, m.out)
		if len(m.replyBlocks) > 50 {
			m.replyBlocks = m.replyBlocks[len(m.replyBlocks)-50:]
		}
		m.out = nil
	}
	w := m.width
	if w < 40 {
		w = 80
	}
	inner := w - 8 // 框内宽 = 总宽 - 边框 - 内边距 - 两格缩进
	if inner < 20 {
		inner = 20
	}
	// 框内只放回复正文；标题已嵌在框的上边框，不再重复。
	// Markdown 在此渲染成 ANSI，并按框内宽度换行（不靠 renderOutputBox 的截断）。
	for _, line := range mdLines(ans) {
		for _, wrapped := range strings.Split(ansi.Wordwrap(line, inner, " "), "\n") {
			m.out = append(m.out, "  "+wrapped)
		}
	}
	m.trimOut()
}

// 轻量 Markdown 渲染：标题 / 加粗 / 斜体 / 删除线 / 行内代码 / 列表 / 引用 / 分隔线。
// 不做完整 CommonMark，只覆盖模型回复里常见的语法；代码块内不做行内解析。
var (
	mdHeading = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	mdUL      = regexp.MustCompile(`^(\s*)[-*+]\s+(.*)$`)
	mdOL      = regexp.MustCompile(`^(\s*)(\d+)\.\s+(.*)$`)
	mdHR      = regexp.MustCompile(`^-{3,}$`)
	mdCode    = regexp.MustCompile("`([^`]+)`")
	mdBold    = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	mdItalic  = regexp.MustCompile(`\*([^*]+)\*`)
	mdStrike  = regexp.MustCompile(`~~([^~]+)~~`)
	mdLink    = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)

	mdBoldStyle   = lipgloss.NewStyle().Bold(true)
	mdItalicStyle = lipgloss.NewStyle().Italic(true)
	mdCodeStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("229")).Background(lipgloss.Color("236"))
	mdStrikeStyle = lipgloss.NewStyle().Strikethrough(true)
	mdH1Style     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("51"))
	mdH2Style     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	mdH3Style     = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("252"))
	mdDimStyle    = lipgloss.NewStyle().Faint(true)
)

func mdInline(s string) string {
	s = mdLink.ReplaceAllString(s, "$1")
	s = mdCode.ReplaceAllStringFunc(s, func(m string) string {
		return mdCodeStyle.Render(strings.Trim(m, "`"))
	})
	s = mdBold.ReplaceAllStringFunc(s, func(m string) string {
		return mdBoldStyle.Render(strings.Trim(m, "*"))
	})
	s = mdItalic.ReplaceAllStringFunc(s, func(m string) string {
		return mdItalicStyle.Render(strings.Trim(m, "*"))
	})
	s = mdStrike.ReplaceAllStringFunc(s, func(m string) string {
		return mdStrikeStyle.Render(strings.Trim(m, "~"))
	})
	return s
}

func mdLines(text string) []string {
	var out []string
	inFence := false
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimRight(raw, " \t")
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			out = append(out, mdCodeStyle.Render(line))
			continue
		}
		if trimmed == "" {
			out = append(out, "")
			continue
		}
		if m := mdHeading.FindStringSubmatch(trimmed); m != nil {
			style := mdH1Style
			switch len(m[1]) {
			case 2:
				style = mdH2Style
			case 3, 4, 5, 6:
				style = mdH3Style
			}
			out = append(out, style.Render(mdInline(m[2])))
			continue
		}
		if mdHR.MatchString(trimmed) {
			out = append(out, mdDimStyle.Render(strings.Repeat("─", 24)))
			continue
		}
		if m := mdUL.FindStringSubmatch(line); m != nil {
			out = append(out, m[1]+"• "+mdInline(m[2]))
			continue
		}
		if m := mdOL.FindStringSubmatch(line); m != nil {
			out = append(out, m[1]+m[2]+". "+mdInline(m[3]))
			continue
		}
		if strings.HasPrefix(trimmed, "> ") {
			out = append(out, mdDimStyle.Render("▎ ")+mdInline(strings.TrimPrefix(trimmed, "> ")))
			continue
		}
		out = append(out, mdInline(line))
	}
	return out
}

// ---- 工作区经验 skill：每轮结束自动沉淀，下次启动加载复用 ----

const evolvedSkillName = "evolved-experience"

// skillDistillPrompt 是给“沉淀经验”这一步的提示词：
// 约束它只写真正值得复用的东西，不要把每轮对话全文都塞进 skill。
const skillDistillPrompt = `> 沉淀规则（只沉淀真正值得沉淀的）：
> - 只记录可复用的方法、关键坑、决策依据、验证证据；
> - 不摘录整段对话、不复制用户原话、不写“你好/谢谢”这类寒暄；
> - 同一结论不重复记录；没有新教训时本轮不写；
> - 每条一句话，写成“做了什么 → 结果/证据”。`

// 运行时数据统一放全局目录（默认 ~/.forge），按类型分子目录：
//
//	~/.forge/dreams/   ~/.forge/memory/   ~/.forge/skills/   ~/.forge/sessions/<project>/
//
// 可用 HUSHFUSION_HOME（或 FORGE_HOME）覆盖；无 home 时才退回 <os>/../forge。
// 这样对话产生的文件不再落进项目目录。
func runtimeRoot(base string) string {
	if v := strings.TrimSpace(os.Getenv("HUSHFUSION_HOME")); v != "" {
		return filepath.Clean(v)
	}
	if v := strings.TrimSpace(os.Getenv("FORGE_HOME")); v != "" {
		return filepath.Clean(v)
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		return filepath.Join(home, ".forge")
	}
	return filepath.Clean(filepath.Join(base, "..", "forge"))
}
func runtimeDreamsDir(base string) string { return filepath.Join(runtimeRoot(base), "dreams") }
func runtimeMemoryDir(base string) string { return filepath.Join(runtimeRoot(base), "memory") }

// globalSkillsDir 是跨项目共享的 skills；projectSkillsDir 是本项目沉淀的 skills。
// 加载顺序为 global → project，同名时 project 覆盖 global。
func globalSkillsDir(base string) string { return filepath.Join(runtimeRoot(base), "skills") }
func projectSkillsDir(base string) string {
	return filepath.Join(runtimeRoot(base), "projects", projectKey(base), "skills")
}

// projectKey 用 os/ 绝对路径的短哈希区分不同项目（全局 sessions 不串会话）。
func projectKey(base string) string {
	abs, err := filepath.Abs(base)
	if err != nil {
		abs = base
	}
	sum := sha256.Sum256([]byte(abs))
	name := filepath.Base(filepath.Dir(abs))
	if name == "" || name == "." || name == string(filepath.Separator) {
		name = "project"
	}
	name = strings.ReplaceAll(name, string(filepath.Separator), "-")
	return fmt.Sprintf("%s-%x", name, sum[:4])
}

func sessionsDir(base string) string {
	return filepath.Join(runtimeRoot(base), "sessions", projectKey(base))
}

// migrateRuntimeData 把旧位置（<os>/... 与 <os>/../forge/...）的数据搬到全局目录，不覆盖已有。
// moveFileIfAbsent 把 src 移到 dst：dst 已存在时保留 src（不覆盖、不丢数据）。
func moveFileIfAbsent(src, dst string) bool {
	if _, err := os.Stat(src); err != nil {
		return false
	}
	if _, err := os.Stat(dst); err == nil {
		return false
	}
	_ = os.MkdirAll(filepath.Dir(dst), 0o755)
	if err := os.Rename(src, dst); err == nil {
		return true
	}
	// 跨文件系统 rename 失败 → 复制后删源。
	b, err := os.ReadFile(src)
	if err != nil {
		return false
	}
	if err := os.WriteFile(dst, b, 0o644); err != nil {
		return false
	}
	_ = os.Remove(src)
	return true
}

// mergeDreamsFile 把旧 dreams 合并进全局当天文件：全局没有就移动；
// 已存在则只追加旧文件里没有的内容，最后删除旧文件（项目内不再残留）。
func mergeDreamsFile(src, dst string) {
	sb, err := os.ReadFile(src)
	if err != nil {
		return
	}
	if _, err := os.Stat(dst); err != nil {
		moveFileIfAbsent(src, dst)
		return
	}
	db, _ := os.ReadFile(dst)
	body := bytes.TrimSpace(sb)
	if len(body) > 0 && !bytes.Contains(db, body) {
		f, ferr := os.OpenFile(dst, os.O_APPEND|os.O_WRONLY, 0o644)
		if ferr == nil {
			if len(db) > 0 && !bytes.HasSuffix(db, []byte("\n")) {
				_, _ = f.WriteString("\n")
			}
			_, _ = f.Write(append([]byte("\n"), sb...))
			_ = f.Close()
		}
	}
	_ = os.Remove(src)
}

// moveDirContentsIfAbsent 把 srcDir 下所有文件按相对路径移到 dstDir（不覆盖已有），
// 最后清理 srcDir 的空目录。
func moveDirContentsIfAbsent(srcDir, dstDir string) {
	_ = filepath.WalkDir(srcDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(srcDir, path)
		if relErr != nil {
			return nil
		}
		moveFileIfAbsent(path, filepath.Join(dstDir, rel))
		return nil
	})
	removeEmptyDirs(srcDir)
}

// removeEmptyDirs 自底向上删除空目录（非空目录不会被删）。
func removeEmptyDirs(root string) {
	var dirs []string
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			dirs = append(dirs, p)
		}
		return nil
	})
	for i := len(dirs) - 1; i >= 0; i-- {
		_ = os.Remove(dirs[i])
	}
}

// migrateRuntimeData 把旧位置（<os>/... 与 <os>/../forge/...）的数据**移动**到全局目录；
// 目标已存在时保留旧文件（不覆盖），最后清掉遗留的空目录。
func migrateRuntimeData(base string) {
	oldForge := filepath.Clean(filepath.Join(base, "..", "forge"))
	oldRoots := []string{base, oldForge}

	// 旧的项目内 skills（os/skills、./forge/skills）→ 当前项目 skills
	for _, root := range oldRoots {
		moveDirContentsIfAbsent(filepath.Join(root, "skills"), projectSkillsDir(base))
	}
	// 旧全局 evolved（此前所有项目共用一个）→ 当前项目 evolved（仅当本项目还没有）
	if _, err := os.Stat(evolvedSkillPath(base)); err != nil {
		moveFileIfAbsent(filepath.Join(globalSkillsDir(base), "evolved", "SKILL.md"), evolvedSkillPath(base))
	}
	_ = os.Remove(filepath.Join(globalSkillsDir(base), "evolved")) // 仅空目录

	// memory：longterm / workflow / agent-memory
	for _, name := range []string{"longterm.json", "workflow.md", "agent-memory.json"} {
		dst := filepath.Join(runtimeMemoryDir(base), name)
		for _, root := range oldRoots {
			src := filepath.Join(root, "memory", name)
			if name == "agent-memory.json" {
				src = filepath.Join(root, name) // 旧 --persist 默认在 <os>/agent-memory.json
			}
			moveFileIfAbsent(src, dst)
			// 全局已有同名文件时，旧位置也清掉（全局是权威快照）。
			if _, err := os.Stat(dst); err == nil {
				_ = os.Remove(src)
			}
		}
	}

	// dreams：旧 <os>/../forge/dreams/* 与 <os>/dreams.md → 全局 dreams/
	if entries, err := os.ReadDir(filepath.Join(oldForge, "dreams")); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			moveFileIfAbsent(filepath.Join(oldForge, "dreams", e.Name()), filepath.Join(runtimeDreamsDir(base), e.Name()))
		}
	}
	mergeDreamsFile(filepath.Join(base, "dreams.md"),
		filepath.Join(runtimeDreamsDir(base), time.Now().Format("2006-01-02")+".md"))

	// sessions：<os>/sessions/* 与 <os>/../forge/sessions/* → 全局 sessions/<project>/
	for _, dir := range []string{
		filepath.Join(base, "sessions"),
		filepath.Join(oldForge, "sessions"),
	} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			src, dst := filepath.Join(dir, e.Name()), filepath.Join(sessionsDir(base), e.Name())
			moveFileIfAbsent(src, dst)
			if _, err := os.Stat(dst); err == nil {
				_ = os.Remove(src) // 全局已有同名会话时清掉旧副本
			}
		}
	}

	// 清空遗留空目录（只删空目录；非空不动）。
	for _, d := range []string{
		filepath.Join(base, "skills", "evolved"),
		filepath.Join(base, "skills"),
		filepath.Join(base, "memory"),
		filepath.Join(base, "sessions"),
		filepath.Join(oldForge, "dreams"),
		filepath.Join(oldForge, "memory"),
		filepath.Join(oldForge, "skills", "evolved"),
		filepath.Join(oldForge, "skills"),
		filepath.Join(oldForge, "sessions"),
	} {
		_ = os.Remove(d)
	}
}

func evolvedSkillDir(base string) string  { return filepath.Join(projectSkillsDir(base), "evolved") }
func evolvedSkillPath(base string) string { return filepath.Join(evolvedSkillDir(base), "SKILL.md") }

// ensureEvolvedSkill 确保经验 skill 存在并返回全文（frontmatter 必须含 name）。
// 已有文件会补上“沉淀规则”提示词，避免历史文件缺少约束。
func ensureEvolvedSkill(base string) string {
	path := evolvedSkillPath(base)
	if b, err := os.ReadFile(path); err == nil {
		content := ensureDistillPrompt(string(b))
		if content != string(b) {
			_ = os.WriteFile(path, []byte(content), 0o644)
		}
		return content
	}
	_ = os.MkdirAll(evolvedSkillDir(base), 0o755)
	content := "---\nname: " + evolvedSkillName +
		"\ndescription: 本项目历次对话自动积累的可复用经验（方法 坑 决策 证据）\n---\n\n# 历史经验\n\n" +
		skillDistillPrompt + "\n"
	_ = os.WriteFile(path, []byte(content), 0o644)
	return content
}

// ensureDistillPrompt 在 “# 历史经验” 后插入沉淀规则（幂等）。
func ensureDistillPrompt(content string) string {
	if strings.Contains(content, "沉淀规则") {
		return content
	}
	const marker = "\n# 历史经验\n"
	i := strings.Index(content, marker)
	if i < 0 {
		return content
	}
	at := i + len(marker)
	return content[:at] + "\n" + skillDistillPrompt + "\n" + content[at:]
}

// evolveSkill 把本轮目标 / 工具 / 结论确定性沉淀进经验 skill（不额外调模型）。
func (m *osModel) evolveSkill(answer string) {
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return
	}
	var tools []string
	for _, l := range m.toolLog {
		if s := strings.TrimSpace(stripANSI(l)); s != "" {
			tools = append(tools, s)
		}
	}
	if len(tools) > 6 {
		tools = tools[len(tools)-6:]
	}
	// 先过质量门：寒暄/无工具的短闲聊不沉淀；再按 200 字压缩成一句结论。
	concl := clipSkillText(answer, 200)
	if !m.worthEvolving(concl, tools) {
		return
	}
	content := ensureEvolvedSkill(m.base)
	if last := lastEntryConclusion(content); last != "" && (last == concl || strings.Contains(last, concl)) {
		return // 同一结论不重复记录
	}
	goal := clipSkillText(m.lastGoal, 80)
	if goal == "" {
		goal = "（无目标）"
	}
	var b strings.Builder
	b.WriteString("\n## " + time.Now().Format("2006-01-02 15:04") + " · " + goal + "\n")
	if len(tools) > 0 {
		b.WriteString("- 工具: " + strings.Join(tools, ", ") + "\n")
	}
	b.WriteString("- 结论: " + concl + "\n")
	content += b.String()
	if err := os.WriteFile(evolvedSkillPath(m.base), []byte(trimEvolved(content, 60)), 0o644); err != nil {
		m.status = m.styles.err.Render("经验 skill 写入失败: " + err.Error())
	}
}

// worthEvolving 判断本轮是否值得沉淀（宁可少记，不把寒暄/闲聊塞进 skill）：
//   - 有工具调用/验证过程 → 值得；
//   - 纯寒暄（你好/谢谢/…）且无工具 → 不记；
//   - 无工具时，结论需足够长且含结论性词汇才记。
func (m *osModel) worthEvolving(concl string, tools []string) bool {
	if strings.TrimSpace(concl) == "" {
		return false
	}
	if len(tools) > 0 {
		return true
	}
	low := strings.ToLower(m.lastGoal)
	for _, greet := range []string{"你好", "您好", "hi", "hello", "谢谢", "在吗", "ok", "好的"} {
		if strings.Contains(low, greet) {
			return false
		}
	}
	if len([]rune(concl)) < 40 {
		return false
	}
	for _, kw := range []string{"结论", "原因", "根因", "修复", "验证", "证据", "失败", "成功", "应该", "必须", "注意"} {
		if strings.Contains(concl, kw) {
			return true
		}
	}
	return false
}

// lastEntryConclusion 取最后一条经验的结论（用于去重）。
func lastEntryConclusion(content string) string {
	idx := strings.LastIndex(content, "- 结论:")
	if idx < 0 {
		return ""
	}
	s := content[idx+len("- 结论:"):]
	if end := strings.Index(s, "\n"); end >= 0 {
		s = s[:end]
	}
	return strings.TrimSpace(s)
}

func clipSkillText(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// trimEvolved 只保留最近 maxEntries 条经验，避免文件无限膨胀。
func trimEvolved(content string, maxEntries int) string {
	const marker = "\n# 历史经验\n"
	i := strings.Index(content, marker)
	if i < 0 {
		return content
	}
	head := content[:i+len(marker)]
	parts := strings.Split(content[i+len(marker):], "\n## ")
	if len(parts) > maxEntries {
		parts = parts[len(parts)-maxEntries:]
	}
	return head + strings.Join(parts, "\n## ")
}

// borderTitle 把标题嵌进圆角框的上边框（避免标题作为内容第一行再出现一次）。
func borderTitle(box, title, color string) string {
	lines := strings.Split(box, "\n")
	if len(lines) == 0 {
		return box
	}
	total := lipgloss.Width(lines[0])
	if total < 6 {
		return box
	}
	tw := lipgloss.Width(title)
	if tw > total-6 {
		title = ansi.Truncate(title, total-6, "…")
		tw = lipgloss.Width(title)
	}
	n := total - tw - 5
	if n < 1 {
		n = 1
	}
	border := lipgloss.NewStyle().Foreground(lipgloss.Color(color))
	top := border.Render("╭─ ") + title + border.Render(" "+strings.Repeat("─", n)+"╮")
	if pad := total - lipgloss.Width(top); pad > 0 {
		top += border.Render(strings.Repeat(" ", pad))
	}
	lines[0] = top
	return strings.Join(lines, "\n")
}

// appendToolEvents 把 agent 运行时的工具调用逐行写进框外列表。
func (m *osModel) appendToolEvents(evs []agent.ToolEvent) {
	for _, ev := range evs {
		m.toolCalls++
		if ev.OK {
			m.toolOK++
		} else {
			m.toolFail++
		}
		m.toolLog = append(m.toolLog, m.toolEventLine(ev))
	}
	if len(m.toolLog) > 500 {
		m.toolLog = m.toolLog[len(m.toolLog)-500:]
	}
}

// toolEventLine 把一条 agent 工具事件渲染成框外一行。
func (m *osModel) toolEventLine(ev agent.ToolEvent) string {
	icon, desc := toolIconDesc(ev.Name, ev.Args)
	line := icon + " " + desc
	if d := fmtToolDur(ev.Duration); d != "" {
		line += "  " + m.styles.hint.Render(d)
	}
	if !ev.OK {
		line += "  " + m.styles.err.Render("✗ "+firstNonEmptyStr(ev.Err, "失败"))
	}
	return line
}

func (m *osModel) appendToolCalls(rep *aios.Report) {
	// 工具调用放框外逐行；AI 文字回复由 appendAnswer 放框内。
	if rep == nil || (len(rep.Plan) == 0 && len(rep.Results) == 0) {
		return
	}
	// 优先用实际执行结果（含 Args/Duration）；dry-run 无结果时退回计划。
	if len(rep.Results) > 0 {
		for _, r := range rep.Results {
			m.toolCalls++
			if r.OK {
				m.toolOK++
			} else {
				m.toolFail++
			}
			m.toolLog = append(m.toolLog, m.toolLine(r))
		}
	} else {
		for _, p := range rep.Plan {
			m.toolCalls++
			if p.Allowed {
				m.toolOK++
			} else {
				m.toolFail++
			}
			m.toolLog = append(m.toolLog, m.toolLine(aios.ExecResult{
				Syscall: p.Syscall, Args: p.Args, OK: p.Allowed, Err: p.Reason,
			}))
		}
	}
	if len(m.toolLog) > 500 {
		m.toolLog = m.toolLog[len(m.toolLog)-500:]
	}
}

// toolLine 把一次工具调用渲染成框外的一行（图标 + 描述 + 耗时）。
func (m *osModel) toolLine(r aios.ExecResult) string {
	icon, desc := toolIconDesc(r.Syscall, r.Args)
	line := icon + " " + desc
	if d := fmtToolDur(r.Duration); d != "" {
		line += "  " + m.styles.hint.Render(d)
	}
	if !r.OK {
		line += "  " + m.styles.err.Render("✗ "+firstNonEmptyStr(r.Err, "失败"))
	}
	return line
}

// toolIconDesc 把系统调用映射成「图标 + 人类可读描述」。
func toolIconDesc(syscall string, args map[string]any) (string, string) {
	get := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := args[k]; ok {
				if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
					return s
				}
			}
		}
		return ""
	}
	switch syscall {
	case "shell", "bash", "exec", "run", "command":
		cmd := get("cmd", "command", "script", "argv")
		if cmd == "" {
			cmd = argsSummary(args)
		}
		return "💻", "$ " + cmd
	case "skill", "skill.view", "skills.view":
		return "📚", "Skill " + firstNonEmptyStr(get("name", "skill", "id"), argsSummary(args))
	case "read", "file.read", "fs.read":
		return "📖", "Read " + firstNonEmptyStr(get("path", "file"), argsSummary(args))
	case "tasks.propose":
		return "📋", "Task " + firstNonEmptyStr(get("id", "title"), argsSummary(args))
	case "tasks.list":
		return "📋", "Task list"
	case "tasks.add":
		return "📋", "Task add " + firstNonEmptyStr(get("title"), argsSummary(args))
	case "tasks.set_state":
		return "📋", fmt.Sprintf("Task %s → %v", get("id"), args["state"])
	case "mem.get", "mem.set", "mem.reset":
		return "🧠", "Memory " + syscall
	case "ls", "list", "dir":
		return "📂", "List " + firstNonEmptyStr(get("path"), ".")
	case "delegate":
		return "🤝", "Delegate " + firstNonEmptyStr(get("goal", "prompt", "task"), argsSummary(args))
	case "opportunities.add":
		return "🎯", "Opportunity " + get("chance")
	case "decisions.add":
		return "⚖️", "Decision"
	case "experiments.new":
		return "🧪", "Experiment " + get("tag")
	case "experiments.advance":
		return "🧪", fmt.Sprintf("Experiment %s → 态%v", get("tag"), args["state"])
	default:
		return "🔧", syscall + " " + argsSummary(args)
	}
}

func fmtToolDur(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	// 统一按秒展示（工具调用时间按秒来算）。
	return fmt.Sprintf("%.2fs", d.Seconds())
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
	dir := sessionsDir(m.base)
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
	if len(m.sentLog) > 0 {
		b.WriteString("\n## 用户消息\n\n```\n" + strings.Join(m.sentLog, "\n") + "\n```\n")
	}
	b.WriteString("\n## 输出日志\n\n```\n" + strings.Join(m.out, "\n") + "\n```\n")
	_ = os.WriteFile(filepath.Join(dir, m.sessionID+".md"), []byte(b.String()), 0o644)
	// 机器可读侧车：供 --resume 恢复（输出日志 + 计数）。
	data := sessionData{
		SessionID:  m.sessionID,
		Title:      m.title,
		StartTime:  m.startTime.Format(time.RFC3339),
		Provider:   m.provider,
		Model:      m.model,
		GoalCount:  m.goalCount,
		ToolCalls:  m.toolCalls,
		ToolOK:     m.toolOK,
		ToolFail:   m.toolFail,
		Out:        m.allOutputLines(),
		Sent:       m.sentLog,
		Frozen:     m.frozenContext,
		Goal:       m.goal,
		ThreadGoal: m.threadGoal,
		ChatRetry:  m.chatRetry,
		LoopActive: m.loopActive,
		LoopMax:    m.loopMax,
		LoopIter:   m.loopIter,
		LoopInterval: func() string {
			if m.loopInterval > 0 {
				return m.loopInterval.String()
			}
			return ""
		}(),
		LoopWaiting: m.loopWaiting,
		LoopErr:     m.loopErr,
		Usage:       m.ledger,
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
	Sent      []string `json:"sent,omitempty"`
	// Frozen 是本次会话的冻结上下文快照（--resume 后原样复用，不重建）。
	Frozen string `json:"frozen,omitempty"`
	// Goal 是 /goal 的长期目标（--resume 后继续生效）。
	Goal string `json:"goal,omitempty"`
	// ThreadGoal 是普通对话主线（无 /goal 时注入，防漂移）。
	ThreadGoal string `json:"thread_goal,omitempty"`
	// ChatRetry 是普通对话出错自动重试计数（--resume 后续跑）。
	ChatRetry int `json:"chat_retry,omitempty"`
	// Loop* 是 /loop 自主循环状态（--resume 后续跑长周期任务）。
	LoopActive   bool   `json:"loop_active,omitempty"`
	LoopMax      int    `json:"loop_max,omitempty"`
	LoopIter     int    `json:"loop_iter,omitempty"`
	LoopInterval string `json:"loop_interval,omitempty"`
	LoopWaiting  bool   `json:"loop_waiting,omitempty"`
	LoopErr      int    `json:"loop_err,omitempty"`
	// Usage 是会话累计的真实 token 用量（provider 上报才有；--resume 后继续累计）。
	Usage ai.UsageLedger `json:"usage,omitempty"`
}

// loadSession 读取一个会话（sel 为空/latest=最新；否则按 id）。best-effort。
func loadSession(base, sel string) (*sessionData, error) {
	dir := sessionsDir(base)
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
			return nil, fmt.Errorf("全局 forge/sessions 无会话可恢复")
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
	case stateModelWizard:
		b.WriteString(m.renderDialog("模型设置", m.wizardBody()))
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
// 控制台布局：header + 仪表盘快照 + 输出（无框平铺） + 进度条 + 输入框
// ---------------------------------------------------------------------------

func (m *osModel) renderConsole(w int) string {
	var b strings.Builder
	if !m.focus {
		b.WriteString(m.renderHeader(w))
		// 仪表盘快照在最上面（只读、无边框）；输出在其下方。
		b.WriteString(m.renderDashboard(w))
	}
	popup := m.renderSlashPopup(w)
	popupH := 0
	if popup != "" {
		popupH = strings.Count(popup, "\n")
	}
	chrome := 20 // 含输入框上下两条分隔线（无前置空行）
	if m.focus {
		chrome = 8
	} else {
		chrome += 2 // 仪表盘圆角框（上下边框）
	}
	if m.busy {
		chrome++ // 内联「AI 思考中」行占一行
	}
	if len(m.lastEcho) > 0 {
		chrome += len(m.lastEcho) // 发送内容块在 forge 框上方
	}
	toolH := len(m.toolLog)
	if toolH > maxToolLines {
		toolH = maxToolLines
	}
	// 终端再高也放不下 200 行：显示上限取屏幕高度的 1/3，其余保留在 m.toolLog。
	if capH := m.height / 3; capH > 3 && toolH > capH {
		toolH = capH
	}
	chrome += toolH // 工具调用列表（框外逐行）
	outH := m.height - chrome - popupH
	if outH < 4 {
		outH = 4
	}
	// 顺序：历史回复框 → 发送内容 → 工具调用（框外）→ 当前回复框 → 输入框。
	curH := 0
	if len(m.out) > 0 {
		curH = len(m.out) + 2
		// 当前框最多占一半，另一半留给历史框；否则第二条会把第一条挤没。
		if half := outH / 2; curH > half {
			curH = half
		}
		if curH < 3 {
			curH = 3
		}
	}
	histStr, histH := m.renderHistoryBoxes(w, outH-curH)
	if histStr != "" {
		b.WriteString(histStr + "\n")
	}
	if len(m.lastEcho) > 0 {
		b.WriteString(strings.Join(m.lastEcho, "\n") + "\n")
	}
	if len(m.toolLog) > 0 {
		b.WriteString(m.renderToolLog(w, toolH) + "\n")
	}
	b.WriteString(m.renderOutputBox(w, outH-histH))
	if m.loading {
		b.WriteString(m.renderProgress(w))
	}
	b.WriteString(popup)
	if m.busy {
		b.WriteString(m.renderThinkingLine(w))
	}
	b.WriteString(m.renderInput(w))
	return b.String()
}

// allOutputLines 把历史回复块与当前回复拼成扁平日志（会话落盘用）。
func (m *osModel) allOutputLines() []string {
	var out []string
	for _, blk := range m.replyBlocks {
		out = append(out, blk...)
	}
	return append(out, m.out...)
}

// renderHistoryBoxes 把历史回复逐个渲染成独立的 forge 框（从新到旧取能放下的）。
// 每条回复一个框，不与下一条合并。
func (m *osModel) renderHistoryBoxes(w, room int) (string, int) {
	var boxes []string
	used := 0
	for i := len(m.replyBlocks) - 1; i >= 0; i-- {
		if room-used <= 4 {
			break
		}
		avail := room - used - 2 // 上下边框
		if avail < 1 {
			break
		}
		lines := m.replyBlocks[i]
		if len(lines) > avail {
			// 放不下的历史框截断（保留开头），而不是整块丢弃——
			// 否则第二条回复会看起来“占据了第一条的位置”。
			lines = append(append([]string{}, lines[:avail-1]...), mdDimStyle.Render("… (折叠)"))
		}
		box := m.styles.replyBox.Width(w - 2).Render(strings.Join(lines, "\n"))
		box = borderTitle(box, m.styles.accent.Render("forge"), "39")
		bh := lipgloss.Height(box)
		if used+bh > room {
			break
		}
		boxes = append([]string{box}, boxes...)
		used += bh
	}
	return strings.Join(boxes, "\n"), used
}

const maxToolLines = 200

// renderToolLog 把工具调用渲染在 forge 框外，逐行（取最近 limit 行）。
func (m *osModel) renderToolLog(w, limit int) string {
	if limit <= 0 {
		limit = 1
	}
	lines := m.toolLog
	if len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, truncateLine(l, w-2))
	}
	return strings.Join(out, "\n")
}

// renderThinkingLine 是内联的「AI 思考中」行：固定在输入框上方，不抢整屏。
// 与旧的 stateThinking 弹窗二选一 —— 现在只用这一行，结果直接落展示框。
func (m *osModel) renderThinkingLine(w int) string {
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
	s := fmt.Sprintf("── %s AI 思考中… %s · ⏱ %s · Ctrl+C 中断", ind, m.thinkingGoal, el)
	return truncateLine(s, w) + "\n"
}

func (m *osModel) renderHeader(w int) string {
	// 头部只留项目标识、模型与运行时长；token/cache/成本为空时不再显示占位符，
	// 也不再拼会话标题（如「宏内核 TUI — 设计 → …」）。
	el := time.Since(m.startTime).Round(time.Second)
	left := m.styles.logo.Render("◉ FORGE")
	model := m.model
	if m.modelUsed != "" {
		model = m.modelUsed
	}
	meta := fmt.Sprintf("model:%s · ⏱ %s", model, el)
	return left + "  " + m.styles.header.Render(meta) + "\n"
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
	// 还没有任何输出（用户尚未发送 / AI 未回复）时，整个回复框不显示。
	if len(m.out) == 0 {
		return ""
	}
	// 圆角「forge」框：输出在仪表盘快照下方，但保留边框。
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
		show = append(show, truncateLine(lines[i], w-6))
	}
	if len(show) == 0 {
		// 空闲时清空输出框（不塞固定内容）；仅加载中给一行提示。
		if m.loading {
			show = []string{m.styles.hint.Render("  （加载中…）")}
		} else {
			show = []string{""}
		}
	}
	title := m.styles.accent.Render("forge")
	if maxScroll > 0 {
		title += m.styles.hint.Render(fmt.Sprintf("  (%d/%d)", len(lines)-m.outScroll, len(lines)))
	}
	if m.outScroll > 0 {
		title += m.styles.err.Render(fmt.Sprintf("  ↓ %d 条新内容 · PgDn/End 回到底部", m.outScroll))
	}
	body := m.styles.replyBox.Width(w - 2).Render(strings.Join(show, "\n"))
	return borderTitle(body, title, "39") + "\n"
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
	// 仪表盘也套一个与 forge 回复框同款的圆角框（面板本身仍无框）。
	inner := w - 6
	if inner < 30 {
		inner = 30
	}
	title := m.styles.label.Render("▌ 仪表盘") + m.styles.hint.Render(" · skills / 参数")
	body := m.styles.dashBox.Width(w - 2).Render(strings.TrimRight(m.renderPanels(inner), "\n"))
	return borderTitle(body, title, "39") + "\n"
}

func (m *osModel) renderPanels(w int) string {
	const cols = panelCols
	const gap = 3
	colW := (w - gap*(cols-1) - 2) / cols
	if colW < 18 {
		colW = 18
	}
	var rows []string
	for i := 0; i < len(m.panels); i += cols {
		var cells []string
		for j := 0; j < cols; j++ {
			if j > 0 {
				cells = append(cells, strings.Repeat(" ", gap))
			}
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
	// 面板只读、无边框：名称亮蓝、内容高亮白，纯文本网格。
	head := m.styles.accent.Render(p.name) + m.styles.hint.Render(fmt.Sprintf(" (%d)", n))
	first := m.styles.hint.Render("（空）")
	if n > 0 {
		first = m.styles.bright.Render(truncateLine(p.lines()[0], colW-2))
	}
	content := head + "\n" + first
	return m.styles.panel.Width(colW).Render(content)
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
	// 输入框上下各一条分隔线；不再加前置空行（发送区别太高）。
	return rule + "\n" + line + q + "\n" + rule + "\n"
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
	case stateModelWizard:
		return "模型"
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
	b.WriteString(m.styles.label.Render("输入框命令（面板只读；所有按键都进输入框）") + "\n")
	cmds := []struct{ name, desc string }{
		{"/model", "模型设置（provider → API key → 模型 → URL）"},
		{"/task /opp /decide", "录入任务 / 机会 / 决策"},
		{"/ai <目标>", "让 AI 推进目标（结果内联）"},
		{"/check", "跑门禁自检"},
		{"/reload", "重新加载仪表盘"},
		{"/open N", "查看第 N 个面板详情"},
		{"/help", "本帮助"},
		{"/quit", "退出（会话落盘）"},
	}
	for _, c := range cmds {
		b.WriteString(fmt.Sprintf("  %-20s %s\n", c.name, c.desc))
	}
	b.WriteString("\n" + m.styles.hint.Render("PgUp/PgDn 滚展示框 · Ctrl+P 命令面板 · Ctrl+C 两次退出 · Esc 清空输入"))
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
	persist := fs.Bool("persist", false, "持久化 agent 记忆到全局 <forge-home>/memory/agent-memory.json（跨会话保留）")
	memFile := fs.String("memory-file", "", "记忆文件路径（隐含 --persist；默认 <forge-home>/memory/agent-memory.json）")
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
			path = filepath.Join(runtimeMemoryDir(base), "agent-memory.json")
		}
		if st, merr := agent.NewMemStoreFile(path); merr == nil {
			m.mem = st
			m.loadWorkflowMemory()
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
			if strings.TrimSpace(d.Frozen) != "" {
				m.frozenContext = d.Frozen // 恢复同一份冻结快照，不重建
			}
			m.goal = d.Goal
			m.threadGoal = d.ThreadGoal
			m.chatRetry = d.ChatRetry
			m.loopStopReq = false
			m.chatStopReq = false
			if d.LoopActive {
				m.loopActive = true
				m.loopMax = d.LoopMax
				m.loopIter = d.LoopIter
				m.loopErr = d.LoopErr
				if iv, perr := time.ParseDuration(d.LoopInterval); perr == nil && iv > 0 {
					m.loopInterval = iv
				} else {
					m.loopInterval = 0
				}
				m.loopWaiting = true
				m.pendingLoopCmd = loopTick(m.loopInterval)
				m.out = append(m.out, m.styles.hint.Render(fmt.Sprintf(
					"▶ 恢复自主循环（%s/%s，间隔 %s）：/loop stop 可停止",
					loopIterLabel(m.loopIter), loopMaxLabel(m.loopMax), loopIntervalLabel(m.loopInterval))))
			}
			m.out = append(m.out, m.styles.label.Render("── 恢复会话 "+m.sessionID+" ──"))
			m.out = append(m.out, d.Out...)
			if len(d.Sent) > 0 {
				m.sentLog = append(m.sentLog, d.Sent...)
				if len(d.Sent) >= 3 {
					m.lastEcho = append([]string(nil), d.Sent[len(d.Sent)-3:]...)
				}
			}
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
	if n <= 0 {
		return ""
	}
	// 按「显示宽度」而不是字节截断：`─` 是 3 字节，按字节切会把多字节字符
	// 劈成非法 UTF-8（终端显示 �…）。ansi.Truncate 同时兼容 ANSI 转义。
	if ansi.StringWidth(s) <= n {
		return s
	}
	return ansi.Truncate(s, n, "…")
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
