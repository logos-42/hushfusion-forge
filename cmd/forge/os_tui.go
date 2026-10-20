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
)

type panel struct {
	name    string
	lines   func() []string
	expanded bool
	scroll  int
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
	footer := cyan + "r=check  L=reload  Enter=展开/收起  ↑↓=选择/滚动  q=退出" + ansiReset

	draw := func() {
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
		case 'r', 'R':
			footer = runCheckInline()
			// 重读回执，刷新面板
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
			footer = yellow + "帮助：↑↓ 选择面板，Enter 展开看全表，r 跑 oscheck，L 重载，q 退出" + ansiReset
			draw()
		}
	}
	return 0
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
				out = append(out, green+bold+"[active] "+snap.ActiveTask.ID+" "+snap.ActiveTask.Title+ansiReset)
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
					if !isEmptyCell(e.States[i]) {
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
	// 标题栏
	b.WriteString(rev + bold + " HUSHFUSION OS — TUI " + strings.Repeat(" ", max(0, cols-18)) + ansiReset + "\n")
	avail := rows - 3 // 标题 + 底栏
	lineNo := 1
	for i, p := range panels {
		expanded := p.expanded
		title := fmt.Sprintf(" %s %s ", marker(i == sel), p.name)
		if i == sel {
			b.WriteString(bold + cyan + title + ansiReset + "\n")
		} else {
			b.WriteString(title + "\n")
		}
		lineNo++
		ls := p.lines()
		if !expanded {
			// 折叠：只显示首行
			if len(ls) > 0 {
				b.WriteString("   " + truncateLine(ls[0], cols-4) + "\n")
				lineNo++
			}
			continue
		}
		// 展开：可滚动显示全部
		if p.scroll < 0 {
			p.scroll = 0
		}
		if p.scroll > len(ls)-1 {
			p.scroll = max(0, len(ls)-1)
		}
		for j := p.scroll; j < len(ls) && lineNo < avail; j++ {
			b.WriteString("   " + truncateLine(ls[j], cols-4) + "\n")
			lineNo++
		}
	}
	// 底栏
	b.WriteString("\n" + footer + "\n")
	fmt.Print(b.String())
}

func marker(on bool) string {
	if on {
		return "▶"
	}
	return " "
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
