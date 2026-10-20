// Package osview 是 HUSHFUSION OS 决策层的「只读视图层」：把 os/ 下的 Markdown
// 决策文档（NORTH_STAR / CURRENT_STATE / TASKS / EXPERIMENTS / OPPORTUNITIES /
// DECISIONS / WEEKLY_REVIEW）与 kernel 回执解析成结构化 Snapshot，并渲染成
// 仪表盘文本。CLI（forge os show / tasks / …）与 TUI（forge os tui）共用同一把尺子。
//
// 纪律（与 os/ 决策层同一哲学）：
//   - 本包**只读**：绝不修改任何 os/ 文档，写操作在 cmd/forge 里经 append-only
//     助手完成，且必须过 oscheck 门（T1–T8 / K1–K2 / E1 / A1）。
//   - 任务池解析复用 oscore（与 oscheck / ossubmit 同一把尺子），避免各说各话。
//   - 文档缺字段/缺文件时容忍（记进 LoadErrors），不让整个视图崩。
package osview

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/logos-42/hushfusion-forge/os/tools/oscore"
)

// Task 是 TASKS.md 候选池的一行（含标题列，oscore 未暴露标题）。
type Task struct {
	ID     string
	Title  string
	IEERC  // I/E/R/C/P 分数
	State  string
	Deps   string
}

// IEERC 是任务池的评分维度。
type IEERC struct {
	I, E, R, C, P string
}

// Experiment 是 EXPERIMENTS.md 五态映射表的一行。
type Experiment struct {
	Tag    string
	States [5]string // 态1..态5（与 EXPERIMENTS.md 头部铁律对应）
}

// Opportunity 是 OPPORTUNITIES.md 机会池的一行。
type Opportunity struct {
	Chance, Source, Match, Next, Status, Date string
}

// Decision 是 DECISIONS.md 决策日志的一行。
type Decision struct {
	ID, Date, Decision, Basis, Alt, Consequence, Status string
}

// Receipt 是 kernel 回执的精简视图。
type Receipt struct {
	Task    string
	Status  string
	Settled bool
}

// Snapshot 是 OS 决策层的结构化快照。
type Snapshot struct {
	BaseDir      string
	NorthStar    string
	CurrentState string
	Tasks        []Task
	ActiveTask   *Task
	ActiveDetail string
	Experiments  []Experiment
	Opportunities []Opportunity
	Decisions    []Decision
	WeeklyReview string
	Receipts     []Receipt
	LoadErrors   []string
}

var taskRowRe = regexp.MustCompile(`^(T-\d{2})$`)

// Load 读整个 os/ 目录，构造 Snapshot（容忍缺文件）。
func Load(baseDir string) (*Snapshot, error) {
	s := &Snapshot{BaseDir: baseDir}
	read := func(rel string) string {
		b, err := os.ReadFile(filepath.Join(baseDir, rel))
		if err != nil {
			s.LoadErrors = append(s.LoadErrors, fmt.Sprintf("%s: %v", rel, err))
			return ""
		}
		return string(b)
	}

	s.NorthStar = firstSection(read("NORTH_STAR.md"))
	s.CurrentState = oneLinerStatus(read("CURRENT_STATE.md"))
	s.loadTasks(read("TASKS.md"))
	s.loadExperiments(read("EXPERIMENTS.md"))
	s.loadOpportunities(read("OPPORTUNITIES.md"))
	s.loadDecisions(read("DECISIONS.md"))
	s.WeeklyReview = trimWeekly(read("WEEKLY_REVIEW.md"))
	s.loadReceipts(filepath.Join(baseDir, "kernel_receipts"))
	return s, nil
}

// ---- 任务池（复用 oscore 的解析纪律，并补标题列） ----

func (s *Snapshot) loadTasks(text string) {
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		if !strings.HasPrefix(t, "|") {
			continue
		}
		c := cells(t)
		if len(c) < 9 || !taskRowRe.MatchString(c[0]) {
			continue
		}
		task := Task{
			ID:    c[0],
			Title: c[1],
			IEERC: IEERC{I: c[2], E: c[3], R: c[4], C: c[5], P: c[6]},
			State: stateCell(c[7]),
			Deps:  c[8],
		}
		s.Tasks = append(s.Tasks, task)
		if task.State == "active" {
			t2 := task
			s.ActiveTask = &t2
		}
	}
	// 详情节（### T-XX … 状态: active）—— 取活跃任务详情
	s.ActiveDetail = activeDetail(text, s.ActiveTask)
}

func activeDetail(text string, active *Task) string {
	if active == nil {
		return ""
	}
	re := regexp.MustCompile(`(?s)###\s+` + regexp.QuoteMeta(active.ID) + `.*?(?:\n## |\z)`)
	if m := re.FindString(text); m != "" {
		return strings.TrimSpace(m)
	}
	return ""
}

// ---- 实验五态映射（复用 oscore） ----

func (s *Snapshot) loadExperiments(text string) {
	for _, c := range oscore.ParseExperimentRows(text) {
		tag := strings.Trim(c[0], "` ")
		exp := Experiment{Tag: tag}
		for i := 0; i < 5; i++ {
			exp.States[i] = c[i+1]
		}
		s.Experiments = append(s.Experiments, exp)
	}
}

// ---- 机会池 / 决策日志（通用表格解析） ----

func (s *Snapshot) loadOpportunities(text string) {
	body := sectionBetween(text, "## 机会池", "## 资金")
	rows := tableRows(body)
	for _, c := range rows {
		if len(c) < 6 {
			continue
		}
		if strings.Contains(c[0], "机会") || allDash(c) {
			continue // 表头 / 占位行
		}
		s.Opportunities = append(s.Opportunities, Opportunity{
			Chance: c[0], Source: c[1], Match: c[2], Next: c[3], Status: c[4], Date: c[5],
		})
	}
}

func (s *Snapshot) loadDecisions(text string) {
	body := sectionBetween(text, "## 决策日志", "## 决策模板")
	rows := tableRows(body)
	for _, c := range rows {
		if len(c) < 7 {
			continue
		}
		if strings.Contains(c[0], "ID") || allDash(c) {
			continue
		}
		s.Decisions = append(s.Decisions, Decision{
			ID: c[0], Date: c[1], Decision: c[2], Basis: c[3], Alt: c[4], Consequence: c[5], Status: c[6],
		})
	}
}

func (s *Snapshot) loadReceipts(dir string) {
	rs, err := oscore.LoadReceipts(dir)
	if err != nil {
		s.LoadErrors = append(s.LoadErrors, fmt.Sprintf("receipts: %v", err))
		return
	}
	ids := make([]string, 0, len(rs))
	for id := range rs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		r := rs[id]
		s.Receipts = append(s.Receipts, Receipt{
			Task:    r.Task,
			Status:  r.Status,
			Settled: r.Settle != nil,
		})
	}
}

// ---- 渲染：仪表盘 ----

// Dashboard 渲染一段紧凑的多节仪表盘文本（CLI 与 TUI 的纯文本回退都用它）。
func (s *Snapshot) Dashboard() string {
	var b strings.Builder
	b.WriteString("# HUSHFUSION OS — 决策层仪表盘\n\n")

	b.WriteString("## 北极星 / 当前状态\n")
	b.WriteString(truncate(s.NorthStar, 240) + "\n")
	if s.CurrentState != "" {
		fmt.Fprintf(&b, "> %s\n", truncate(s.CurrentState, 200))
	}
	b.WriteString("\n")

	b.WriteString("## 任务池（active ≤ 1）\n")
	if s.ActiveTask != nil {
		fmt.Fprintf(&b, "- **[active] %s** %s (P=%s, %s)\n", s.ActiveTask.ID, s.ActiveTask.Title, s.ActiveTask.P, s.ActiveTask.State)
	} else {
		b.WriteString("- _无 active 任务_\n")
	}
	cand := 0
	for _, t := range s.Tasks {
		if t.State == "active" {
			continue
		}
		fmt.Fprintf(&b, "- [%s] %s — P=%s %s\n", t.State, t.ID, t.P, t.Title)
		cand++
	}
	fmt.Fprintf(&b, "（候选 %d 条）\n\n", cand)

	b.WriteString("## 实验五态\n")
	for _, e := range s.Experiments {
		depth := 0
		for i := 0; i < 5; i++ {
			if !oscore.IsEmpty(e.States[i]) {
				depth = i + 1
			}
		}
		fmt.Fprintf(&b, "- `%s` 走到第 %d 态\n", e.Tag, depth)
	}
	b.WriteString("\n")

	b.WriteString("## 机会池\n")
	if len(s.Opportunities) == 0 {
		b.WriteString("_（空——待 T-02 首填）_\n")
	}
	for _, o := range s.Opportunities {
		fmt.Fprintf(&b, "- %s — %s [%s]\n", o.Chance, o.Next, o.Status)
	}
	b.WriteString("\n")

	b.WriteString("## 决策日志（最新在前）\n")
	for i, d := range s.Decisions {
		if i >= 6 {
			fmt.Fprintf(&b, "- … 共 %d 条\n", len(s.Decisions))
			break
		}
		fmt.Fprintf(&b, "- **%s** %s [%s]\n", d.ID, truncate(d.Decision, 80), d.Status)
	}
	b.WriteString("\n")

	b.WriteString("## kernel 回执\n")
	if len(s.Receipts) == 0 {
		b.WriteString("_（无回执）_\n")
	}
	for _, r := range s.Receipts {
		fmt.Fprintf(&b, "- %s → %s%s\n", r.Task, r.Status, ternary(r.Settled, " (settled)", ""))
	}
	if len(s.LoadErrors) > 0 {
		b.WriteString("\n> 读盘警告：\n")
		for _, e := range s.LoadErrors {
			fmt.Fprintf(&b, "> - %s\n", e)
		}
	}
	return b.String()
}

// ---- 通用解析助手 ----

func cells(line string) []string {
	s := strings.TrimSpace(line)
	s = strings.Trim(s, "|")
	parts := strings.Split(s, "|")
	out := make([]string, len(parts))
	for i, p := range parts {
		out[i] = strings.TrimSpace(p)
	}
	return out
}

func tableRows(text string) [][]string {
	var rows [][]string
	inFence := false
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "```") {
			inFence = !inFence
			continue
		}
		if inFence || !strings.HasPrefix(t, "|") {
			continue
		}
		c := cells(t)
		if len(c) == 0 || allDash(c) || isSepRow(c) {
			continue
		}
		rows = append(rows, c)
	}
	return rows
}

// isSepRow 报告一行是否为 Markdown 表分隔行（| :-- | :-- | …）。
func isSepRow(c []string) bool {
	for _, x := range c {
		s := strings.TrimSpace(x)
		if !sepRe.MatchString(s) {
			return false
		}
	}
	return true
}

var sepRe = regexp.MustCompile(`^:?-+:?$`)

func stateCell(cell string) string {
	if i := strings.IndexAny(cell, "（("); i >= 0 {
		return strings.TrimSpace(cell[:i])
	}
	return strings.TrimSpace(cell)
}

func allDash(c []string) bool {
	for _, x := range c {
		if !oscore.IsEmpty(x) {
			return false
		}
	}
	return true
}

func sectionBetween(text, startHeader, endHeader string) string {
	lines := strings.Split(text, "\n")
	in := false
	var out []string
	for _, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), startHeader) {
			in = true
			continue
		}
		if in && endHeader != "" && strings.HasPrefix(strings.TrimSpace(l), endHeader) {
			break
		}
		if in {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

func firstSection(text string) string {
	// 取第一个标题之后的前几段非空文字。
	lines := strings.Split(text, "\n")
	var out []string
	started := false
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "#") {
			if started {
				break
			}
			started = true
			continue
		}
		if started && t != "" {
			out = append(out, t)
		}
		if started && len(out) >= 4 {
			break
		}
	}
	return strings.Join(out, " ")
}

func oneLinerStatus(text string) string {
	for _, l := range strings.Split(text, "\n") {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "**") && strings.Contains(t, "状态") {
			return t
		}
	}
	// 退化：第一行非空
	for _, l := range strings.Split(text, "\n") {
		if t := strings.TrimSpace(l); t != "" && !strings.HasPrefix(t, "#") && !strings.HasPrefix(t, "---") {
			return t
		}
	}
	return ""
}

func trimWeekly(text string) string {
	// 取周复盘最后一段（最近一次）
	lines := strings.Split(text, "\n")
	var blocks [][]string
	var cur []string
	for _, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), "## ") && len(cur) > 0 {
			blocks = append(blocks, cur)
			cur = nil
		}
		cur = append(cur, l)
	}
	if len(cur) > 0 {
		blocks = append(blocks, cur)
	}
	if len(blocks) == 0 {
		return strings.TrimSpace(text)
	}
	return strings.TrimSpace(strings.Join(blocks[len(blocks)-1], "\n"))
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func ternary(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}
