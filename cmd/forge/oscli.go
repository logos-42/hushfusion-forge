package main

// forge os 的决策层视图 + 安全写入子命令（append-only，保持 oscheck 绿）。
// 只读命令走 internal/osview；写入命令就地改 os/ Markdown，但只做「追加 / 有序插入」，
// 不动状态机铁律（active→done 仍只由 ossubmit --settle 执行）。

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/logos-42/hushfusion-forge/internal/aios"
	"github.com/logos-42/hushfusion-forge/internal/oskernel"
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

// ---- 安全写入（经 oskernel 系统调用；写前守卫保 oscheck 绿） ----
//
// 宏内核（2026-10-20）：所有写操作都作为一次内核系统调用发出，由 oskernel 的
// 子系统驱动执行并在写前做不变量守卫（active≤1 / P=floor(I×E×R/C) / ID 不重复 /
// E1 不跳态）。CLI 与 TUI 只是系统调用的两个客户端。

func osKernel() *oskernel.Kernel { return oskernel.New(osBaseDir()) }

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
	p, err := doPropose(osBaseDir(), *id, *iS, *eS, *rS, *cS, *title, *deps)
	if err != nil {
		return fail("os propose: %v", err)
	}
	note("os propose: %s 已插入候选池（P=%d，状态=proposed）", *id, p)
	note("提醒: 跑 `forge os check` 确认 T1–T8 仍绿")
	return 0
}

// doPropose 走内核 tasks.propose（CLI 与 TUI 共用）：有序插入 proposed 任务，
// 由内核做写前守卫（ID 不重复 / 评分合法 / P=floor(I×E×R/C)）。
func doPropose(base, id string, i, e, r, c int, title, deps string) (int, error) {
	res, err := oskernel.New(base).Syscall("tasks.propose", map[string]any{
		"id": id, "title": title, "deps": deps, "i": i, "e": e, "r": r, "c": c,
	})
	if err != nil {
		return 0, err
	}
	p, _ := res.Data.(int)
	return p, nil
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
	if err := doOpp(osBaseDir(), *chance, *source, *match, *next, *status, *date); err != nil {
		return fail("%v", err)
	}
	note("os opp: 已追加机会「%s」", *chance)
	return 0
}

// doOpp 走内核 opportunities.add（CLI 与 TUI 共用）。
func doOpp(base, chance, source, match, next, status, date string) error {
	_, err := oskernel.New(base).Syscall("opportunities.add", map[string]any{
		"chance": chance, "source": source, "match": match, "next": next, "status": status, "date": date,
	})
	return err
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
	nextID, err := doDecide(osBaseDir(), *text, *basis, *alt, *consequence, *status)
	if err != nil {
		return fail("%v", err)
	}
	note("os decide: %s 已追加", nextID)
	return 0
}

// doDecide 走内核 decisions.add（CLI 与 TUI 共用），返回新分配的决策 ID。
func doDecide(base, text, basis, alt, consequence, status string) (string, error) {
	res, err := oskernel.New(base).Syscall("decisions.add", map[string]any{
		"text": text, "basis": basis, "alt": alt, "consequence": consequence, "status": status,
	})
	if err != nil {
		return "", err
	}
	id, _ := res.Data.(string)
	return id, nil
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
	if _, err := osKernel().Syscall("experiments.new", map[string]any{"tag": *tag, "q": *q}); err != nil {
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
	if _, err := osKernel().Syscall("experiments.advance", map[string]any{"tag": *tag, "state": *state, "text": *text}); err != nil {
		return fail("%v", err)
	}
	note("os exp advance: `%s` 态%d 已写入", *tag, *state)
	return 0
}

// atoiSafe 供 TUI 表单把字符串评分转 int（其余写入逻辑已移入 oskernel）。
func atoiSafe(s string) int {
	s = strings.NewReplacer("*", "", "`", "", " ", "").Replace(s)
	n, _ := strconv.Atoi(s)
	return n
}

// cmdOSAI 是 phase-3 AI 粘合入口：AI 读 OS 状态并经内核受控系统调用提议动作。
// 纪律：AI 只提议（propose/add/new/advance），内核写前守卫 + 越权拒绝，
// 科学结论/完成判定仍归 leo 或独立反馈。
func cmdOSAI(args []string) int {
	fs := newFlagSet("os ai",
		"forge os ai --goal <文本> [--provider zai] [--model m] [--base-url u] [--dry-run] [--max 8]",
		"phase-3 AI 粘合：AI 读 OS 状态并经内核受控系统调用提议动作（只提议，不判完成）。")
	goal := fs.String("goal", "", "要推进的目标")
	provider := fs.String("provider", "zai", "AI provider（见 forge ai list）")
	model := fs.String("model", "", "模型（空=provider 默认）")
	baseURL := fs.String("base-url", "", "覆盖端点（custom 代理/网关）")
	dryRun := fs.Bool("dry-run", false, "只校验计划，不执行")
	max := fs.Int("max", 8, "单次计划最大写系统调用数")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *goal == "" {
		return fail("os ai: --goal 必填")
	}
	agent := aios.NewAgent(oskernel.New(osBaseDir()), *provider, *model, *baseURL)
	agent.MaxCalls = *max
	rep, err := agent.Run(context.Background(), *goal, *dryRun)
	if err != nil {
		return fail("os ai: %v", err)
	}
	printAIReport(rep)
	return 0
}

func printAIReport(rep *aios.Report) {
	fmt.Printf("## AI 粘合报告 — 目标: %s\n\n", rep.Goal)
	fmt.Println("### 计划（准入判定）")
	if len(rep.Plan) == 0 {
		fmt.Println("_（无步骤）_")
	}
	for i, p := range rep.Plan {
		mark := "✓允许"
		if !p.Allowed {
			mark = "✗拒绝: " + p.Reason
		}
		fmt.Printf("%d. `%s` %s — %s\n", i+1, p.Syscall, mark, argsSummary(p.Args))
	}
	if len(rep.Results) > 0 {
		fmt.Println("\n### 执行结果（经内核）")
		for _, r := range rep.Results {
			if r.OK {
				fmt.Printf("- ✓ `%s` %s\n", r.Syscall, r.Message)
			} else {
				fmt.Printf("- ✗ `%s` %s\n", r.Syscall, r.Err)
			}
		}
	}
	for _, n := range rep.Notes {
		fmt.Printf("\n> %s\n", n)
	}
}

// argsSummary 把一次系统调用的参数压成一行（键排序，稳定可读）。
func argsSummary(args map[string]any) string {
	if len(args) == 0 {
		return "{}"
	}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, args[k]))
	}
	return "{" + strings.Join(parts, " ") + "}"
}
