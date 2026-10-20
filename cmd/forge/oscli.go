package main

// forge os 的决策层视图 + 安全写入子命令（append-only，保持 oscheck 绿）。
// 只读命令走 internal/osview；写入命令就地改 os/ Markdown，但只做「追加 / 有序插入」，
// 不动状态机铁律（active→done 仍只由 ossubmit --settle 执行）。

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/logos-42/hushfusion-forge/internal/osview"
)

// osBaseDir 返回 os/ 决策层根目录（默认 ./os，可用环境变量覆盖）。
func osBaseDir() string {
	if d := os.Getenv("HUSHFUSION_OS_DIR"); d != "" {
		return d
	}
	return "os"
}

func loadOS() (*osview.Snapshot, error) {
	return osview.Load(osBaseDir())
}

// ---- 视图（只读） ----

func cmdOSShow() int {
	s, err := loadOS()
	if err != nil {
		return fail("%v", err)
	}
	fmt.Print(s.Dashboard())
	return 0
}

func cmdOSTasks() int {
	s, err := loadOS()
	if err != nil {
		return fail("%v", err)
	}
	if s.ActiveTask != nil {
		fmt.Printf("## active: %s — %s\n", s.ActiveTask.ID, s.ActiveTask.Title)
		fmt.Printf("P=%s 状态=%s 依赖=%s\n\n", s.ActiveTask.P, s.ActiveTask.State, s.ActiveTask.Deps)
		if s.ActiveDetail != "" {
			fmt.Println(s.ActiveDetail)
			fmt.Println()
		}
	}
	fmt.Println("## 候选池")
	fmt.Printf("%-6s %-8s %-5s %s\n", "ID", "P", "状态", "标题")
	for _, t := range s.Tasks {
		if t.State == "active" {
			continue
		}
		fmt.Printf("%-6s %-8s %-5s %s\n", t.ID, t.P, t.State, t.Title)
	}
	return 0
}

func cmdOSExps() int {
	s, err := loadOS()
	if err != nil {
		return fail("%v", err)
	}
	fmt.Printf("%-28s 态1 态2 态3 态4 态5\n", "实验(tag)")
	for _, e := range s.Experiments {
		fmt.Printf("%-28s %s %s %s %s %s\n", e.Tag,
			mark(e.States[0]), mark(e.States[1]), mark(e.States[2]), mark(e.States[3]), mark(e.States[4]))
	}
	return 0
}

func cmdOSOpps() int {
	s, err := loadOS()
	if err != nil {
		return fail("%v", err)
	}
	if len(s.Opportunities) == 0 {
		fmt.Println("(机会池空——待 T-02 首填)")
		return 0
	}
	for _, o := range s.Opportunities {
		fmt.Printf("- %s [%s] %s\n    匹配: %s\n    下一步: %s\n", o.Chance, o.Status, o.Source, o.Match, o.Next)
	}
	return 0
}

func cmdOSDecisions() int {
	s, err := loadOS()
	if err != nil {
		return fail("%v", err)
	}
	for _, d := range s.Decisions {
		fmt.Printf("### %s (%s) [%s]\n", d.ID, d.Date, d.Status)
		fmt.Printf("决策: %s\n依据: %s\n备选: %s\n后果: %s\n\n", d.Decision, d.Basis, d.Alt, d.Consequence)
	}
	return 0
}

func cmdOSReview() int {
	s, err := loadOS()
	if err != nil {
		return fail("%v", err)
	}
	fmt.Println(s.WeeklyReview)
	return 0
}

func cmdOSCheck() int {
	// 复用 oscheck 门（单一事实来源），不重复实现铁律。
	cmd := exec.Command("go", "run", "./os/tools/oscheck")
	cmd.Dir = repoRoot()
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		// oscheck 非零退出 = 有违规；把退出码透传。
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode()
		}
		return fail("oscheck 运行失败: %v", err)
	}
	return 0
}

func mark(s string) string {
	if strings.TrimSpace(s) == "" || s == "—" {
		return "·"
	}
	return "✓"
}

// repoRoot 找仓库根（含 go.mod 的目录）。
func repoRoot() string {
	dir, _ := os.Getwd()
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	d, _ := os.Getwd()
	return d
}

// ---- 安全写入（append-only / 有序插入） ----

var taskRowRe = regexp.MustCompile(`^\s*\|?\s*(T-\d{2})`)

// cmdOSPropose 新增一条 proposed 任务，按 P 降序有序插入候选池（T8），
// P=floor(I*E*R/C)（T5），状态恒 proposed（不动 active/done 铁律）。
func cmdOSPropose(args []string) int {
	fs := newFlagSet("os propose",
		"forge os propose --id T-XX --i <1-5> --e <1-5> --r <1-5> --c <1-5> --title <文本> [--deps <文本>]",
		"新增 proposed 任务：按 P 有序插入候选池，P=floor(I*E*R/C)。")
	id := fs.String("id", "", "任务 ID，如 T-09")
	iS := fs.Int("i", 0, "影响 1-5")
	eS := fs.Int("e", 0, "证据可能性 1-5")
	rS := fs.Int("r", 0, "解锁价值 1-5")
	cS := fs.Int("c", 0, "成本 1-5")
	title := fs.String("title", "", "任务标题")
	deps := fs.String("deps", "—", "依赖/阻塞说明")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *id == "" || *title == "" || *iS == 0 || *eS == 0 || *rS == 0 || *cS == 0 {
		fmt.Fprintln(os.Stderr, "forge os propose: --id/--title/--i/--e/--r/--c 必填（评分 1-5）")
		fs.Usage()
		return 2
	}
	for _, v := range []int{*iS, *eS, *rS, *cS} {
		if v < 1 || v > 5 {
			return fail("评分必须在 1..5（got %d）", v)
		}
	}
	p := (*iS) * (*eS) * (*rS) / (*cS)
	row := fmt.Sprintf("| %s | %s | %d | %d | %d | %d | **%d** | proposed | %s |",
		*id, *title, *iS, *eS, *rS, *cS, p, *deps)

	path := filepath.Join(osBaseDir(), "TASKS.md")
	text, err := os.ReadFile(path)
	if err != nil {
		return fail("读 TASKS.md: %v", err)
	}
	out := patchTasksTable(string(text), row, p)
	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		return fail("写 TASKS.md: %v", err)
	}
	note("os propose: %s 已插入候选池（P=%d，状态=proposed）", *id, p)
	note("提醒: 跑 `forge os check` 确认 T1–T8 仍绿")
	return 0
}

// patchTasksTable 在候选池表格里按 P 降序有序插入一行（active 行保持置顶）。
// 保留表头与分隔行（|:--|），不破坏 Markdown 结构（否则 oscheck T8 必红）。
func patchTasksTable(text, newRow string, newP int) string {
	lines := strings.Split(text, "\n")
	hdrIdx := -1
	for i, l := range lines {
		if strings.Contains(l, "| ID | 任务 |") {
			hdrIdx = i
			break
		}
	}
	if hdrIdx < 0 {
		return text
	}
	// 跳过表头后的分隔行（|:--|…），但要把它原样保留在输出里。
	idx := hdrIdx + 1
	if idx < len(lines) && isSepRowOS(splitCells(lines[idx])) {
		idx++
	}
	start := idx
	end := start
	type tr struct {
		line   string
		p      int
		active bool
	}
	var parsed []tr
	for end < len(lines) {
		l := strings.TrimSpace(lines[end])
		if strings.HasPrefix(l, "|") && taskRowRe.MatchString(l) {
			c := splitCells(l)
			p := atoiSafe(c[6])
			parsed = append(parsed, tr{line: lines[end], p: p, active: strings.HasPrefix(strings.TrimSpace(c[7]), "active")})
			end++
		} else {
			break
		}
	}
	nr := tr{line: newRow, p: newP, active: false}
	var out []string
	inserted := false
	for _, r := range parsed {
		if !inserted && !r.active && nr.p > r.p {
			out = append(out, nr.line)
			inserted = true
		}
		out = append(out, r.line)
	}
	if !inserted {
		out = append(out, nr.line)
	}
	// lines[:start] 已包含表头（及可能的分隔行）；在其后插入有序数据行。
	newLines := append([]string{}, lines[:start]...)
	newLines = append(newLines, out...)
	newLines = append(newLines, lines[end:]...)
	return strings.Join(newLines, "\n")
}

// cmdOSOpp 追加一条机会（OPPORTUNITIES.md 机会池表，oscheck 不查此表，安全）。
func cmdOSOpp(args []string) int {
	fs := newFlagSet("os opp",
		"forge os opp --chance <文本> --source <文本> --match <文本> --next <文本> [--status proposed] [--date YYYY-MM-DD]",
		"追加一条合作/资金机会。")
	chance := fs.String("chance", "", "机会描述")
	source := fs.String("source", "", "来源")
	match := fs.String("match", "", "研究方向匹配")
	next := fs.String("next", "", "下一步")
	status := fs.String("status", "proposed", "状态")
	date := fs.String("date", time.Now().Format("2006-01-02"), "日期")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *chance == "" {
		return fail("os opp: --chance 必填")
	}
	row := fmt.Sprintf("| %s | %s | %s | %s | %s | %s |", *chance, *source, *match, *next, *status, *date)
	path := filepath.Join(osBaseDir(), "OPPORTUNITIES.md")
	if err := appendTableRow(path, "## 机会池", row); err != nil {
		return fail("%v", err)
	}
	note("os opp: 已追加机会「%s」", *chance)
	return 0
}

// cmdOSDecide 追加一条决策（DECISIONS.md 决策日志，oscheck 不查，安全）。
func cmdOSDecide(args []string) int {
	fs := newFlagSet("os decide",
		"forge os decide --text <决策> --basis <依据> [--alt <备选>] [--consequence <后果>] [--status active]",
		"追加一条决策到决策日志。")
	text := fs.String("text", "", "决策一句话")
	basis := fs.String("basis", "", "依据（证据路径，禁止「我觉得」）")
	alt := fs.String("alt", "—", "被否方案+原因")
	consequence := fs.String("consequence", "—", "后果")
	status := fs.String("status", "active", "状态")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *text == "" || *basis == "" {
		return fail("os decide: --text/--basis 必填")
	}
	date := time.Now().Format("2006-01-02")
	path := filepath.Join(osBaseDir(), "DECISIONS.md")
	nextID, err := nextDecisionID(path)
	if err != nil {
		return fail("%v", err)
	}
	row := fmt.Sprintf("| %s | %s | %s | %s | %s | %s | %s |",
		nextID, date, *text, *basis, *alt, *consequence, *status)
	if err := appendTableRow(path, "## 决策日志", row); err != nil {
		return fail("%v", err)
	}
	note("os decide: %s 已追加", nextID)
	return 0
}

// cmdOSExp 新增实验或推进实验态（E1 不跳态）。
func cmdOSExp(args []string) int {
	if len(args) == 0 {
		return fail("os exp: 需要子命令 new|advance")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "new":
		return osExpNew(rest)
	case "advance":
		return osExpAdvance(rest)
	default:
		return fail("os exp: 未知子命令 %q（用 new|advance）", sub)
	}
}

func osExpNew(args []string) int {
	fs := newFlagSet("os exp new",
		"forge os exp new --tag <tag> --q <研究问题>",
		"新增实验（仅填态1 问题；态2-5 留空，E1 不跳态）。")
	tag := fs.String("tag", "", "实验 tag，如 my_exp")
	q := fs.String("q", "", "可检验研究问题")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *tag == "" || *q == "" {
		return fail("os exp new: --tag/--q 必填")
	}
	row := fmt.Sprintf("| `%s` | %s | | | | |", *tag, *q)
	path := filepath.Join(osBaseDir(), "EXPERIMENTS.md")
	if err := appendTableRow(path, "## 已有实验", row); err != nil {
		return fail("%v", err)
	}
	note("os exp new: `%s` 已新增（态1）", *tag)
	return 0
}

func osExpAdvance(args []string) int {
	fs := newFlagSet("os exp advance",
		"forge os exp advance --tag <tag> --state <1-5> --text <内容>",
		"推进实验某态（E1：态 n 非空 ⇒ 态 n-1 必非空，不能跳态）。")
	tag := fs.String("tag", "", "实验 tag")
	state := fs.Int("state", 0, "目标态 1-5")
	text := fs.String("text", "", "该态内容")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *tag == "" || *state < 1 || *state > 5 || *text == "" {
		return fail("os exp advance: --tag/--state(1-5)/--text 必填")
	}
	path := filepath.Join(osBaseDir(), "EXPERIMENTS.md")
	if err := setExpState(path, *tag, *state, *text); err != nil {
		return fail("%v", err)
	}
	note("os exp advance: `%s` 态%d 已写入", *tag, *state)
	return 0
}

// ---- Markdown 写入助手（append / set-cell，保持结构） ----

func appendTableRow(path, sectionHeader, row string) error {
	text, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("读 %s: %w", path, err)
	}
	lines := strings.Split(string(text), "\n")
	inSec, lastRow := false, -1
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), sectionHeader) {
			inSec = true
			continue
		}
		if inSec {
			if strings.HasPrefix(strings.TrimSpace(l), "## ") {
				break
			}
			if strings.HasPrefix(strings.TrimSpace(l), "|") && !strings.HasPrefix(strings.TrimSpace(l), "|--") {
				lastRow = i
			}
		}
	}
	if lastRow < 0 {
		return fmt.Errorf("%s 未找到「%s」下的表格行", path, sectionHeader)
	}
	out := append([]string{}, lines[:lastRow+1]...)
	out = append(out, row)
	out = append(out, lines[lastRow+1:]...)
	return os.WriteFile(path, []byte(strings.Join(out, "\n")), 0o644)
}

func setExpState(path string, tag string, state int, text string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("读 %s: %w", path, err)
	}
	lines := strings.Split(string(raw), "\n")
	found := false
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if !strings.HasPrefix(t, "|") {
			continue
		}
		c := splitCells(t)
		if len(c) != 6 || strings.Trim(c[0], "` ") != tag {
			continue
		}
		// E1 不跳态：态 state 之前必须都非空。
		for n := 1; n < state; n++ {
			if isEmptyCell(c[n]) {
				return fmt.Errorf("E1 违反：态%d 为空，不能先写态%d（实验 %s 必须逐级走）", n, state, tag)
			}
		}
		c[state] = text
		lines[i] = "| " + strings.Join(c, " | ") + " |"
		found = true
		break
	}
	if !found {
		return fmt.Errorf("EXPERIMENTS.md 找不到实验 %s", tag)
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644)
}

func nextDecisionID(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	maxN := 0
	re := regexp.MustCompile(`\| (D-(\d{2})) `)
	for _, m := range re.FindAllStringSubmatch(string(raw), -1) {
		if n, err := strconv.Atoi(m[2]); err == nil && n > maxN {
			maxN = n
		}
	}
	return fmt.Sprintf("D-%02d", maxN+1), nil
}

func splitCells(line string) []string {
	s := strings.TrimSpace(line)
	s = strings.Trim(s, "|")
	parts := strings.Split(s, "|")
	out := make([]string, len(parts))
	for i, p := range parts {
		out[i] = strings.TrimSpace(p)
	}
	return out
}

func isEmptyCell(s string) bool {
	s = strings.TrimSpace(s)
	return s == "" || s == "—" || s == "-" || s == "–"
}

func atoiSafe(s string) int {
	s = strings.NewReplacer("*", "", "`", "", " ", "").Replace(s)
	n, _ := strconv.Atoi(s)
	return n
}

var sepReOS = regexp.MustCompile(`^:?-+:?$`)

func isSepRowOS(c []string) bool {
	for _, x := range c {
		if !sepReOS.MatchString(strings.TrimSpace(x)) {
			return false
		}
	}
	return true
}
