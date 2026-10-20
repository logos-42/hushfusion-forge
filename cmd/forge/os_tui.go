package main

// forge os tui —— 零依赖终端 UI（不引入任何外部 TUI 库，遵守仓库契约）。
//
// 实现：用 `stty raw -echo` 进入原始模式逐字节读键，用 ANSI 转义码画框 / 上色 /
// 清屏。把 osview 的结构化 Snapshot 渲染成可导航的决策层面板，并能在 TUI 内
// 直接触发 oscheck 门（r）与重新加载（L）。
//
// 纪律：TUI 只「看」与「触发只读/已存在的安全操作」，不在这里直接改 os/ 文档
// （写入走 forge os propose/opp/decide/exp，且必须过 oscheck）。

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/logos-42/hushfusion-forge/internal/osview"
)

const (
	ansiClear = "\033[2J\033[H"
	ansiReset = "\033[0m"
	cyan      = "\033[36m"
	green     = "\033[32m"
	yellow    = "\033[33m"
	bold      = "\033[1m"
	rev       = "\033[7m"
	gray      = "\033[90m"
	white     = "\033[97m"
)

type panel struct {
	name     string
	lines    func() []string
	expanded bool
	scroll   int
}

func cmdOSTUI() int {
	base := osBaseDir()
	snap, err := osview.Load(base)
	if err != nil {
		return fail("%v", err)
	}

	// 进原始模式
	restore, terr := enterRaw()
	if terr != nil {
		// 非终端（被管道）则退化成仪表盘文本。
		fmt.Print(snap.Dashboard())
		return 0
	}
	defer restore()

	panels := buildPanels(snap)
	sel := 0
	var form *formState
	footer := cyan + "r=check  L=reload  n=新任务 o=机会 d=决策  Enter=展开  ↑↓=选择  q=退出" + ansiReset

	draw := func() {
		if form != nil {
			drawForm(form, footer)
			return
		}
		renderTUI(panels, sel, footer)
	}

	draw()
	reader := bufio.NewReader(os.Stdin)
	for {
		b, err := reader.ReadByte()
		if err != nil {
			if err == io.EOF {
				break
			}
			continue
		}
		// 表单录入模式：吞掉按键，构造字段。
		if form != nil {
			done, cancelled, _ := form.handleByte(reader, b)
			if cancelled {
				form = nil
				footer = yellow + "已取消录入" + ansiReset
				draw()
				continue
			}
			if done {
				f := form
				form = nil
				if m, e := submitForm(base, f); e != nil {
					footer = yellow + "录入失败: " + e.Error() + ansiReset
				} else {
					footer = green + m + ansiReset
					// 写成功后重载视图。
					if s2, e2 := osview.Load(base); e2 == nil {
						snap = s2
						panels = buildPanels(snap)
					}
				}
				draw()
				continue
			}
			draw()
			continue
		}
		// 转义序列（方向键等）：ESC [ X
		if b == 27 {
			_, _ = reader.ReadByte() // '['
			code, _ := reader.ReadByte()
			switch code {
			case 'A': // up
				if panels[sel].expanded {
					panels[sel].scroll--
				} else if sel > 0 {
					sel--
				}
			case 'B': // down
				if panels[sel].expanded {
					panels[sel].scroll++
				} else if sel < len(panels)-1 {
					sel++
				}
			}
			draw()
			continue
		}
		switch b {
		case 'q', 'Q', 3: // q / Ctrl-C
			fmt.Print(ansiClear)
			return 0
		case 13, 10: // Enter
			panels[sel].expanded = !panels[sel].expanded
			panels[sel].scroll = 0
			draw()
		case 'n', 'N': // 新任务
			form = newTaskForm()
			draw()
		case 'o', 'O': // 新机会
			form = newOppForm()
			draw()
		case 'd', 'D': // 新决策
			form = newDecideForm()
			draw()
		case 'r', 'R':
			footer = runCheckInline()
			if s2, e := osview.Load(base); e == nil {
				snap = s2
				panels = buildPanels(snap)
			}
			draw()
		case 'L', 'l':
			if s2, e := osview.Load(base); e == nil {
				snap = s2
				panels = buildPanels(snap)
				footer = green + "已重新加载" + ansiReset
			}
			draw()
		case '?':
			footer = yellow + "帮助：↑↓ 选择面板，Enter 展开看全表，n/o/d 录入任务/机会/决策，r 跑 oscheck，L 重载，q 退出" + ansiReset
			draw()
		}
	}
	return 0
}

// ---- 表单录入（TUI 内直接录入任务/机会/决策） ----

type formField struct {
	label string
	buf   []byte
	hide  bool // 是否回显（预留）
}

type formState struct {
	title  string
	kind   string // task | opp | decide
	fields []formField
	idx    int
}

func newTaskForm() *formState {
	return &formState{
		title: "新任务（proposed）",
		kind:  "task",
		fields: []formField{
			{label: "ID (T-XX)"},
			{label: "标题"},
			{label: "I (1-5)"},
			{label: "E (1-5)"},
			{label: "R (1-5)"},
			{label: "C (1-5)"},
			{label: "依赖/阻塞 (可空)"},
		},
	}
}

func newOppForm() *formState {
	return &formState{
		title: "新机会",
		kind:  "opp",
		fields: []formField{
			{label: "机会"},
			{label: "来源"},
			{label: "研究方向匹配"},
			{label: "下一步"},
		},
	}
}

func newDecideForm() *formState {
	return &formState{
		title: "新决策",
		kind:  "decide",
		fields: []formField{
			{label: "决策"},
			{label: "依据（证据路径，禁止「我觉得」）"},
		},
	}
}

func (f *formState) val(i int) string { return string(f.fields[i].buf) }

// handleByte 处理一个按键；返回 (完成, 取消, 消息)。
func (f *formState) handleByte(reader *bufio.Reader, b byte) (done, cancelled bool, msg string) {
	switch b {
	case 27: // Esc（含方向键转义序列）：若为方向键则忽略，否则取消
		if reader.Buffered() >= 2 {
			nb, _ := reader.ReadByte()
			if nb == '[' {
				_, _ = reader.ReadByte()
				return false, false, ""
			}
		}
		return false, true, ""
	case 13, 10: // Enter：下一字段 / 提交
		f.idx++
		if f.idx >= len(f.fields) {
			return true, false, ""
		}
		return false, false, ""
	case 127, 8: // 退格：删一个 rune
		buf := f.fields[f.idx].buf
		f.fields[f.idx].buf = trimLastRune(buf)
		return false, false, ""
	default:
		// 可打印字符（含 UTF-8 多字节：>=0x80）
		if (b >= 0x20 && b < 0x7f) || b >= 0x80 {
			f.fields[f.idx].buf = append(f.fields[f.idx].buf, b)
		}
		return false, false, ""
	}
}

// submitForm 把表单写进 os/（复用 CLI 的 append 核心）。
func submitForm(base string, f *formState) (string, error) {
	switch f.kind {
	case "task":
		i := atoiSafe(f.val(2))
		e := atoiSafe(f.val(3))
		r := atoiSafe(f.val(4))
		c := atoiSafe(f.val(5))
		deps := f.val(6)
		if deps == "" {
			deps = "—"
		}
		p, err := doPropose(base, f.val(0), i, e, r, c, f.val(1), deps)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("任务 %s 已录入（P=%d）", f.val(0), p), nil
	case "opp":
		if err := doOpp(base, f.val(0), f.val(1), f.val(2), f.val(3), "proposed", today()); err != nil {
			return "", err
		}
		return "机会已录入", nil
	case "decide":
		id, err := doDecide(base, f.val(0), f.val(1), "—", "—", "active")
		if err != nil {
			return "", err
		}
		return id + " 已录入", nil
	}
	return "", fmt.Errorf("未知表单类型 %q", f.kind)
}

func today() string { return time.Now().Format("2006-01-02") }

func trimLastRune(b []byte) []byte {
	if len(b) == 0 {
		return b
	}
	i := len(b) - 1
	for i > 0 && (b[i]&0xC0) == 0x80 {
		i--
	}
	return b[:i]
}

func drawForm(f *formState, footer string) {
	cols, rows := termSize()
	if cols < 40 {
		cols = 80
	}
	if rows < 8 {
		rows = 24
	}
	var b strings.Builder
	b.WriteString(ansiClear)

	// 顶部栏
	title := " 录入：" + f.title + " "
	b.WriteString(rev + bold + title + strings.Repeat(" ", max(0, cols-dispWidth(title))) + ansiReset + "\n\n")

	// 表单字段（当前字段高亮 + 块光标）
	for i, fld := range f.fields {
		active := i == f.idx
		label := fld.label + ": "
		val := string(fld.buf)
		if active {
			val += "█"
		}
		line := label + val
		// 前缀：当前字段用 ▶，其它用空格
		prefix := "  "
		if active {
			prefix = cyan + bold + "▶ " + ansiReset
		}
		b.WriteString(prefix + fitLine(line, cols-4) + "\n")
	}

	// 中部留白，底栏贴底
	pad := rows - 4 - len(f.fields)
	for i := 0; i < pad; i++ {
		b.WriteString("\n")
	}
	hint := "Enter=下一字段/提交 · Esc=取消"
	b.WriteString(bar(hint, cols))
	b.WriteString(bar(footer, cols))
	fmt.Print(b.String())
}

// enterRaw 进入终端原始模式；返回还原函数。
func enterRaw() (func(), error) {
	if os.Getenv("TERM") == "" {
		return nil, fmt.Errorf("非终端")
	}
	if err := exec.Command("stty", "raw", "-echo").Run(); err != nil {
		return nil, err
	}
	restore := func() {
		_ = exec.Command("stty", "-raw", "echo").Run()
		fmt.Print(ansiReset)
	}
	return restore, nil
}

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

func renderTUI(panels []panel, sel int, footer string) {
	cols, rows := termSize()
	if cols < 40 {
		cols = 80
	}
	if rows < 10 {
		rows = 24
	}
	var b strings.Builder
	b.WriteString(ansiClear)

	// 顶部应用栏
	title := " HUSHFUSION OS · 宏内核 TUI "
	b.WriteString(rev + bold + title + strings.Repeat(" ", max(0, cols-dispWidth(title))) + ansiReset + "\n")

	// 面板区：折叠面板各占 3 行（边框+1 行内容+边框），展开面板吃剩余高度。
	avail := rows - 3 // 顶栏 + 底栏 + 一行间隔
	expandedIdx := -1
	for i, p := range panels {
		if p.expanded {
			expandedIdx = i
		}
	}
	used := 0
	for i, p := range panels {
		ls := p.lines()
		var content []string
		height := 3
		if i == expandedIdx {
			// 展开：给它剩余可用高度。
			room := avail - used - 2
			if room < 1 {
				room = 1
			}
			if p.scroll < 0 {
				p.scroll = 0
			}
			if p.scroll > len(ls) {
				p.scroll = len(ls)
			}
			for j := p.scroll; j < len(ls) && len(content) < room; j++ {
				content = append(content, ls[j])
			}
			if len(content) == 0 {
				content = []string{"（空）"}
			}
			height = len(content) + 2
		} else if len(ls) > 0 {
			content = []string{ls[0]}
		} else {
			content = []string{"（空）"}
		}
		b.WriteString(renderPanel(p.name, content, cols, i == sel))
		used += height
		if used >= avail {
			break
		}
	}

	// 底部状态栏
	b.WriteString(bar(footer, cols))
	fmt.Print(b.String())
}

// renderPanel 画一个圆角边框面板，标题嵌在上边框；active 时边框高亮。
func renderPanel(name string, content []string, width int, active bool) string {
	border := gray
	if active {
		border = cyan + bold
	}
	inner := width - 2
	if inner < 4 {
		inner = 4
	}
	title := " " + marker(active) + " " + name + " "
	pad := inner - dispWidth(title)
	if pad < 0 {
		pad = 0
	}
	top := border + "╭" + title + strings.Repeat("─", pad) + "╮" + ansiReset
	var b strings.Builder
	b.WriteString(top + "\n")
	for _, ln := range content {
		b.WriteString(border + "│" + ansiReset + fitLine(ln, inner) + border + "│" + ansiReset + "\n")
	}
	b.WriteString(border + "╰" + strings.Repeat("─", inner) + "╯" + ansiReset + "\n")
	return b.String()
}

// bar 画一条全宽底栏（反色）。
func bar(text string, width int) string {
	t := " " + text + " "
	pad := width - dispWidth(t)
	if pad < 0 {
		pad = 0
	}
	return rev + t + strings.Repeat(" ", pad) + ansiReset + "\n"
}

// fitLine 把一行内容适配到 inner 显示宽：左一空格 + 内容（按显示宽截断）+ 右补齐。
func fitLine(s string, inner int) string {
	s = strings.TrimSpace(s)
	const lead = 1
	budget := inner - lead - 1 // 留一个尾随空格
	if budget < 1 {
		budget = 1
	}
	// 按显示宽截断
	var out []rune
	w := 0
	for _, r := range s {
		rw := runeWidth(r)
		if w+rw > budget {
			break
		}
		out = append(out, r)
		w += rw
	}
	body := string(out)
	tail := budget - w
	if tail < 0 {
		tail = 0
	}
	return " " + body + strings.Repeat(" ", tail) + " "
}

// dispWidth 计算字符串显示宽度（跳过 ANSI 转义，CJK 记 2）。
func dispWidth(s string) int {
	w := 0
	i := 0
	for i < len(s) {
		if s[i] == 0x1b { // ESC：跳过 \033[...m
			j := strings.IndexByte(s[i:], 'm')
			if j < 0 {
				break
			}
			i += j + 1
			continue
		}
		r := []rune(s[i:])[0]
		w += runeWidth(r)
		i += len(string(r))
	}
	return w
}

func runeWidth(r rune) int {
	if (r >= 0x1100 && r <= 0x115F) ||
		(r >= 0x2E80 && r <= 0xA4CF) ||
		(r >= 0xAC00 && r <= 0xD7A3) ||
		(r >= 0xF900 && r <= 0xFAFF) ||
		(r >= 0xFE30 && r <= 0xFE6F) ||
		(r >= 0xFF00 && r <= 0xFF60) ||
		(r >= 0xFFE0 && r <= 0xFFE6) ||
		(r >= 0x20000 && r <= 0x3FFFD) {
		return 2
	}
	return 1
}

func marker(on bool) string {
	if on {
		return "▶"
	}
	return " "
}

// blankCell 判断实验某态是否为空位（与 oscore.IsEmpty 同口径，TUI 本地用）。
func blankCell(s string) bool {
	s = strings.TrimSpace(s)
	return s == "" || s == "—" || s == "-" || s == "–"
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

func termSize() (int, int) {
	out, err := exec.Command("stty", "size").Output()
	if err != nil {
		return 80, 24
	}
	var rows, cols int
	fmt.Sscan(string(out), &rows, &cols)
	if cols == 0 {
		cols = 80
	}
	if rows == 0 {
		rows = 24
	}
	return cols, rows
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

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func ternary(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}
