package oskernel

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/logos-42/hushfusion-forge/internal/osview"
)

// Kernel 是宏内核核心：拥有 OS 决策层的状态与持久化（块设备 = os/*.md），
// 所有访问经 Syscall 单一入口（对齐 Linux 的 syscall(2)）。
//
// phase-1：单进程单内核。读走 osview（同一把尺子），写走 store 原语 +
// invariant 写前守卫。权威日门仍是 oscheck（读同一批文件）。
type Kernel struct {
	Base string // os/ 目录
}

func New(base string) *Kernel { return &Kernel{Base: base} }

// Result 是一次系统调用的返回（对齐 kernel「只搬运事实」：OK/Data/Message）。
type Result struct {
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
	Data    any    `json:"data,omitempty"`
}

func ok(data any) Result    { return Result{OK: true, Data: data} }
func okMsg(m string) Result { return Result{OK: true, Message: m} }

// Syscall 是唯一入口：按名字分发到子系统驱动。未知 / 缺参 / 守卫拒绝都
// 返回 error（fail-closed，不假装成功）。
func (k *Kernel) Syscall(name string, args map[string]any) (Result, error) {
	h, ok := syscallTable[name]
	if !ok {
		return Result{}, fmt.Errorf("未知系统调用 %q", name)
	}
	return h(k, args)
}

// arg 助手：取字符串 / int 参数。
func arg(args map[string]any, key string) string {
	if v, ok := args[key]; ok && v != nil {
		return strings.TrimSpace(fmt.Sprintf("%v", v))
	}
	return ""
}

func argInt(args map[string]any, key string) int {
	if v, ok := args[key]; ok {
		switch n := v.(type) {
		case int:
			return n
		case float64:
			return int(n)
		case string:
			return atoiSafe(n)
		}
	}
	return 0
}

type handler func(k *Kernel, args map[string]any) (Result, error)

// syscallTable 是系统调用分发表（对齐 Linux syscall table）：
// 读（list/active/dashboard）+ 写（propose/add/advance，写前守卫）。
var syscallTable = map[string]handler{
	// 读
	"tasks.list":         sysTasksList,
	"tasks.active":       sysTasksActive,
	"experiments.list":   sysExperimentsList,
	"opportunities.list": sysOpportunitiesList,
	"decisions.list":     sysDecisionsList,
	"receipts.list":      sysReceiptsList,
	"dashboard":          sysDashboard,
	// 写（写前守卫）
	"tasks.propose":       sysTasksPropose,
	"opportunities.add":   sysOpportunitiesAdd,
	"decisions.add":       sysDecisionsAdd,
	"experiments.new":     sysExperimentsNew,
	"experiments.advance": sysExperimentsAdvance,
	// 元
	"invariants.check": sysInvariants,
	"version":          sysVersion,
}

func (k *Kernel) load() (*osview.Snapshot, error) { return osview.Load(k.Base) }

// ---- 读驱动 ----

func sysDashboard(k *Kernel, _ map[string]any) (Result, error) {
	s, err := k.load()
	if err != nil {
		return Result{}, err
	}
	return ok(s.Dashboard()), nil
}

func sysTasksList(k *Kernel, _ map[string]any) (Result, error) {
	s, err := k.load()
	if err != nil {
		return Result{}, err
	}
	return ok(s.Tasks), nil
}

func sysTasksActive(k *Kernel, _ map[string]any) (Result, error) {
	s, err := k.load()
	if err != nil {
		return Result{}, err
	}
	return ok(s.ActiveTask), nil
}

func sysExperimentsList(k *Kernel, _ map[string]any) (Result, error) {
	s, err := k.load()
	if err != nil {
		return Result{}, err
	}
	return ok(s.Experiments), nil
}

func sysOpportunitiesList(k *Kernel, _ map[string]any) (Result, error) {
	s, err := k.load()
	if err != nil {
		return Result{}, err
	}
	return ok(s.Opportunities), nil
}

func sysDecisionsList(k *Kernel, _ map[string]any) (Result, error) {
	s, err := k.load()
	if err != nil {
		return Result{}, err
	}
	return ok(s.Decisions), nil
}

func sysReceiptsList(k *Kernel, _ map[string]any) (Result, error) {
	s, err := k.load()
	if err != nil {
		return Result{}, err
	}
	return ok(s.Receipts), nil
}

func sysVersion(k *Kernel, _ map[string]any) (Result, error) {
	return ok(map[string]string{"kernel": "oskernel", "phase": "1", "store": "markdown-blockdev"}), nil
}

// ---- 写驱动（invariant 写前守卫） ----

func sysTasksPropose(k *Kernel, args map[string]any) (Result, error) {
	id := arg(args, "id")
	title := arg(args, "title")
	deps := arg(args, "deps")
	if deps == "" {
		deps = "—"
	}
	i, e, r, c := argInt(args, "i"), argInt(args, "e"), argInt(args, "r"), argInt(args, "c")
	if id == "" || title == "" || i == 0 || e == 0 || r == 0 || c == 0 {
		return Result{}, fmt.Errorf("tasks.propose: id/title/i/e/r/c 必填（评分 1-5）")
	}
	for _, v := range []int{i, e, r, c} {
		if v < 1 || v > 5 {
			return Result{}, fmt.Errorf("评分必须在 1..5（got %d）", v)
		}
	}
	// 写前守卫：ID 合法且不重复。
	if !taskRowRe.MatchString(id) {
		return Result{}, fmt.Errorf("任务 ID 需为 T-NN 形式（got %q）", id)
	}
	tasksPath := filepath.Join(k.Base, "TASKS.md")
	existing, err := os.ReadFile(tasksPath)
	if err != nil {
		return Result{}, fmt.Errorf("读 TASKS.md: %w", err)
	}
	if taskIDExists(existing, id) {
		return Result{}, fmt.Errorf("任务 ID %s 已存在（T7 不重复）", id)
	}
	p := i * e * r / c
	row := fmt.Sprintf("| %s | %s | %d | %d | %d | %d | **%d** | proposed | %s |",
		id, title, i, e, r, c, p, deps)
	out := patchTasksTable(string(existing), row, p)
	if err := os.WriteFile(tasksPath, []byte(out), 0o644); err != nil {
		return Result{}, err
	}
	return Result{OK: true, Data: p, Message: fmt.Sprintf("%s 已插入候选池（P=%d，proposed）", id, p)}, nil
}

func sysOpportunitiesAdd(k *Kernel, args map[string]any) (Result, error) {
	chance := arg(args, "chance")
	if chance == "" {
		return Result{}, fmt.Errorf("opportunities.add: chance 必填")
	}
	status := arg(args, "status")
	if status == "" {
		status = "proposed"
	}
	date := arg(args, "date")
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}
	row := fmt.Sprintf("| %s | %s | %s | %s | %s | %s |",
		chance, arg(args, "source"), arg(args, "match"), arg(args, "next"), status, date)
	if err := appendTableRow(filepath.Join(k.Base, "OPPORTUNITIES.md"), "## 机会池", row); err != nil {
		return Result{}, err
	}
	return okMsg(fmt.Sprintf("机会「%s」已追加", chance)), nil
}

func sysDecisionsAdd(k *Kernel, args map[string]any) (Result, error) {
	text := arg(args, "text")
	basis := arg(args, "basis")
	if text == "" || basis == "" {
		return Result{}, fmt.Errorf("decisions.add: text/basis 必填（依据禁止「我觉得」）")
	}
	alt := arg(args, "alt")
	if alt == "" {
		alt = "—"
	}
	consequence := arg(args, "consequence")
	if consequence == "" {
		consequence = "—"
	}
	status := arg(args, "status")
	if status == "" {
		status = "active"
	}
	path := filepath.Join(k.Base, "DECISIONS.md")
	nextID, err := nextDecisionID(path)
	if err != nil {
		return Result{}, err
	}
	row := fmt.Sprintf("| %s | %s | %s | %s | %s | %s | %s |",
		nextID, time.Now().Format("2006-01-02"), text, basis, alt, consequence, status)
	if err := appendTableRow(path, "## 决策日志", row); err != nil {
		return Result{}, err
	}
	return Result{OK: true, Data: nextID, Message: nextID + " 已追加"}, nil
}

func sysExperimentsNew(k *Kernel, args map[string]any) (Result, error) {
	tag := arg(args, "tag")
	q := arg(args, "q")
	if tag == "" || q == "" {
		return Result{}, fmt.Errorf("experiments.new: tag/q 必填")
	}
	expPath := filepath.Join(k.Base, "EXPERIMENTS.md")
	existing, err := os.ReadFile(expPath)
	if err != nil {
		return Result{}, fmt.Errorf("读 EXPERIMENTS.md: %w", err)
	}
	if expTagExists(existing, tag) {
		return Result{}, fmt.Errorf("实验 tag %q 已存在（证据映射不许重复；要推进请用 experiments.advance）", tag)
	}
	row := fmt.Sprintf("| `%s` | %s | | | | |", tag, q)
	if err := appendTableRow(expPath, "## 已有实验", row); err != nil {
		return Result{}, err
	}
	return okMsg(fmt.Sprintf("`%s` 已新增（态1）", tag)), nil
}

func sysExperimentsAdvance(k *Kernel, args map[string]any) (Result, error) {
	tag := arg(args, "tag")
	state := argInt(args, "state")
	text := arg(args, "text")
	if tag == "" || state < 1 || state > 5 || text == "" {
		return Result{}, fmt.Errorf("experiments.advance: tag/state(1-5)/text 必填")
	}
	if err := setExpState(filepath.Join(k.Base, "EXPERIMENTS.md"), tag, state, text); err != nil {
		return Result{}, err
	}
	return okMsg(fmt.Sprintf("`%s` 态%d 已写入", tag, state)), nil
}

// taskIDExists 扫描候选池是否已有该 ID（T7 写前守卫）。
func taskIDExists(tasksText []byte, id string) bool {
	for _, line := range strings.Split(string(tasksText), "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "|") {
			c := splitCells(t)
			if len(c) >= 1 && c[0] == id {
				return true
			}
		}
	}
	return false
}

// expTagExists 扫描实验映射表是否已有该 tag（证据映射不许重复的写前守卫）。
func expTagExists(expText []byte, tag string) bool {
	for _, line := range strings.Split(string(expText), "\n") {
		t := strings.TrimSpace(line)
		if !strings.HasPrefix(t, "|") {
			continue
		}
		c := splitCells(t)
		if len(c) == 6 && strings.Trim(c[0], "` ") == tag {
			return true
		}
	}
	return false
}
