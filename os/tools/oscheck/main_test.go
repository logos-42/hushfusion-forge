// 变异测试：每条检查至少一条「故意改坏必须变红」的用例 ——
// 与 owners_test.go 改名册证明门能红同一纪律。随 G5（go test ./...）自动跑。
package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/logos-42/hushfusion-forge/os/tools/oscore"
)

const baseTasks = `# TASKS
## 唯一主任务
### T-01｜示例主任务 — 状态: active
- 验收标准：os/acceptance/t01.md
## 候选池（按 P 排序）
| ID | 任务 | I | E | R | C | P | 状态 | 依赖/阻塞 |
|:--|:--|:-:|:-:|:-:|:-:|:-:|:--|:--|
| T-01 | 主任务 | 5 | 5 | 4 | 3 | 33 | active | — |
| T-02 | 候选二 | 3 | 3 | 5 | 1 | 45 | proposed | — |
| T-03 | 候选三 | 4 | 4 | 4 | 3 | 21 | proposed | — |
| T-04 | 被阻塞的 | 2 | 2 | 2 | 2 | 4 | blocked | 需上游数据 |
`

const baseExp = `# EXPERIMENTS
| 实验（tag） | 态 1 问题 | 态 2 方案 | 态 3 复现 | 态 4 独立反馈 | 态 5 决策 |
|:--|:--|:--|:--|:--|:--|
| demo | 有 | 有 | ✅ | ✅ | 决策 A |
`

var baseAccept = []string{"t01.md"}

// baseReceipts 给基线的 active 任务 T-01 配一份 submitted 回执（K1 绿）。
func baseReceipts() map[string]*oscore.Receipt {
	kernel, _ := json.Marshal(map[string]any{"ok": true, "via": "kernel-channel-actor"})
	return map[string]*oscore.Receipt{
		"T-01": {
			Task: "T-01", Status: oscore.StatusSubmitted,
			Submissions: []oscore.Submission{{
				At: "2026-10-18T00:00:00Z", Channel: "os",
				TaskID: "T-01", Marker: "os:T-01", Kernel: kernel,
			}},
		},
	}
}

type mutation struct {
	name       string
	tasks      string
	exp        string
	accept     []string
	receipts   map[string]*oscore.Receipt
	wantPrefix string // 期望出现的违规前缀（"T2:" 等）
}

func mutants() []mutation {
	return []mutation{
		{
			name: "两件 active",
			tasks: strings.Replace(baseTasks,
				"| T-03 | 候选三 | 4 | 4 | 4 | 3 | 21 | proposed",
				"| T-03 | 候选三 | 4 | 4 | 4 | 3 | 21 | active", 1),
			exp: baseExp, accept: baseAccept, receipts: baseReceipts(),
			wantPrefix: "T2:",
		},
		{
			name: "active 无验收文件", tasks: baseTasks, exp: baseExp,
			accept: []string{}, receipts: baseReceipts(), wantPrefix: "T4:",
		},
		{
			name: "P 算错",
			tasks: strings.Replace(baseTasks,
				"| T-03 | 候选三 | 4 | 4 | 4 | 3 | 21 |",
				"| T-03 | 候选三 | 4 | 4 | 4 | 3 | 22 |", 1),
			exp: baseExp, accept: baseAccept, receipts: baseReceipts(),
			wantPrefix: "T5:",
		},
		{
			name: "I 越界",
			tasks: strings.Replace(baseTasks,
				"| T-03 | 候选三 | 4 | 4 | 4 | 3 | 21 |",
				"| T-03 | 候选三 | 9 | 4 | 4 | 3 | 21 |", 1),
			exp: baseExp, accept: baseAccept, receipts: baseReceipts(),
			wantPrefix: "T5:",
		},
		{
			name: "blocked 无原因",
			tasks: strings.Replace(baseTasks,
				"| T-04 | 被阻塞的 | 2 | 2 | 2 | 2 | 4 | blocked | 需上游数据 |",
				"| T-04 | 被阻塞的 | 2 | 2 | 2 | 2 | 4 | blocked | — |", 1),
			exp: baseExp, accept: baseAccept, receipts: baseReceipts(),
			wantPrefix: "T6:",
		},
		{
			name: "非法状态",
			tasks: strings.Replace(baseTasks,
				"| T-03 | 候选三 | 4 | 4 | 4 | 3 | 21 | proposed |",
				"| T-03 | 候选三 | 4 | 4 | 4 | 3 | 21 | sleeping |", 1),
			exp: baseExp, accept: baseAccept, receipts: baseReceipts(),
			wantPrefix: "T1:",
		},
		{
			name: "ID 重复",
			tasks: strings.Replace(baseTasks,
				"| T-04 | 被阻塞的",
				"| T-03 | 被阻塞的", 1),
			exp: baseExp, accept: baseAccept, receipts: baseReceipts(),
			wantPrefix: "T7:",
		},
		{
			name: "active 缺详情节",
			tasks: strings.Replace(baseTasks,
				"### T-01｜示例主任务 — 状态: active",
				"### T-09｜别的任务 — 状态: proposed", 1),
			exp: baseExp, accept: baseAccept, receipts: baseReceipts(),
			wantPrefix: "T3:",
		},
		{
			name: "排序不符（表头声称按 P 排序）",
			tasks: strings.Replace(baseTasks,
				"| T-02 | 候选二 | 3 | 3 | 5 | 1 | 45 | proposed | — |\n| T-03 | 候选三 | 4 | 4 | 4 | 3 | 21 | proposed | — |",
				"| T-03 | 候选三 | 4 | 4 | 4 | 3 | 21 | proposed | — |\n| T-02 | 候选二 | 3 | 3 | 5 | 1 | 45 | proposed | — |", 1),
			exp: baseExp, accept: baseAccept, receipts: baseReceipts(),
			wantPrefix: "T8:",
		},
		{
			name: "跳态：态5 无态4", tasks: baseTasks,
			exp: strings.Replace(baseExp,
				"| demo | 有 | 有 | ✅ | ✅ | 决策 A |",
				"| demo | 有 | 有 | ✅ | — | 决策 A |", 1),
			accept: baseAccept, receipts: baseReceipts(), wantPrefix: "E1:",
		},
		{
			name: "孤儿验收文件", tasks: baseTasks, exp: baseExp,
			accept:   []string{"t01.md", "t99-ghost.md"},
			receipts: baseReceipts(), wantPrefix: "A1:",
		},
		{
			name: "active 无 kernel 回执", tasks: baseTasks, exp: baseExp,
			accept: baseAccept, receipts: map[string]*oscore.Receipt{},
			wantPrefix: "K1:",
		},
		{
			name: "active 挂着 failed 回执", tasks: baseTasks, exp: baseExp,
			accept: baseAccept, receipts: map[string]*oscore.Receipt{
				"T-01": {Task: "T-01", Status: oscore.StatusFailed},
			},
			wantPrefix: "K1:",
		},
		{
			name: "done 无 settled 回执",
			tasks: strings.Replace(baseTasks,
				"| T-03 | 候选三 | 4 | 4 | 4 | 3 | 21 | proposed",
				"| T-03 | 候选三 | 4 | 4 | 4 | 3 | 21 | done", 1),
			exp: baseExp, accept: baseAccept, receipts: baseReceipts(),
			wantPrefix: "K2:",
		},
		{
			name: "done 只有 submitted 回执",
			tasks: strings.Replace(baseTasks,
				"| T-03 | 候选三 | 4 | 4 | 4 | 3 | 21 | proposed",
				"| T-03 | 候选三 | 4 | 4 | 4 | 3 | 21 | done", 1),
			exp: baseExp, accept: baseAccept,
			receipts: func() map[string]*oscore.Receipt {
				m := baseReceipts()
				m["T-03"] = &oscore.Receipt{Task: "T-03", Status: oscore.StatusSubmitted}
				return m
			}(),
			wantPrefix: "K2:",
		},
	}
}

// TestBaselineGreen：合法基线必须全绿（防门自己写坏）。
func TestBaselineGreen(t *testing.T) {
	if viols := runAll(baseTasks, baseExp, baseAccept, baseReceipts()); len(viols) > 0 {
		t.Fatalf("合法基线不应有违规，got %v", viols)
	}
}

// TestMutationsTurnRed：每条变异必须触发对应检查（证明门能红）。
func TestMutationsTurnRed(t *testing.T) {
	for _, m := range mutants() {
		t.Run(m.name, func(t *testing.T) {
			got := runAll(m.tasks, m.exp, m.accept, m.receipts)
			for _, v := range got {
				if strings.HasPrefix(v, m.wantPrefix) {
					return // 红在期望的检查上
				}
			}
			t.Fatalf("应触发 %s，got %v", m.wantPrefix, got)
		})
	}
}

// TestEachCheckHasMutation：名册式断言 —— T1..T8/E1/A1/K1/K2 每条检查
// 至少被一条变异覆盖，防止悄悄删掉某条检查而测试还绿。
func TestEachCheckHasMutation(t *testing.T) {
	covered := map[string]bool{}
	for _, m := range mutants() {
		covered[strings.TrimSuffix(m.wantPrefix, ":")] = true
	}
	for _, id := range []string{
		"T1", "T2", "T3", "T4", "T5", "T6", "T7", "T8", "E1", "A1", "K1", "K2",
	} {
		if !covered[id] {
			t.Errorf("检查 %s 没有任何变异用例覆盖", id)
		}
	}
}

// TestAcceptPrefix：T-01 → t01 的验收文件命名约定。
func TestAcceptPrefix(t *testing.T) {
	if got := oscore.AcceptPrefix("T-01"); got != "t01" {
		t.Fatalf("AcceptPrefix(T-01) = %q, want t01", got)
	}
}

// TestBoldPAndFullwidthState：真实 TASKS.md 的两个格式坑 ——
// P 列带 **粗体**、状态带全角括号注记，解析必须稳。
func TestBoldPAndFullwidthState(t *testing.T) {
	text := strings.Replace(baseTasks,
		"| T-03 | 候选三 | 4 | 4 | 4 | 3 | 21 | proposed |",
		"| T-03 | 候选三 | 4 | 4 | 4 | 3 | **21** | proposed（预备） |", 1)
	rows := oscore.ParseTaskTable(text)
	for _, r := range rows {
		if r.ID == "T-03" {
			if r.P != "**21**" {
				t.Fatalf("P 原文应保留为 **21**，got %q", r.P)
			}
			if r.State != "proposed" {
				t.Fatalf("状态应归一为 proposed，got %q", r.State)
			}
			if viols := checkScores(r); len(viols) != 0 {
				t.Fatalf("**21** 应能剥皮转 int，got %v", viols)
			}
			return
		}
	}
	t.Fatal("T-03 行没解析出来")
}

// TestE1MessageNumbering：E1 的消息必须报「态5…态4」而不是错位的编号
// （Python 版曾把 n+1/n 写进消息，变异测试只查前缀没抓到 —— 这里补上语义断言）。
func TestE1MessageNumbering(t *testing.T) {
	exp := strings.Replace(baseExp,
		"| demo | 有 | 有 | ✅ | ✅ | 决策 A |",
		"| demo | 有 | 有 | ✅ | — | 决策 A |", 1)
	viols := checkExperiments(exp)
	if len(viols) != 1 || !strings.Contains(viols[0], "态5有内容但态4为空") {
		t.Fatalf("E1 消息编号错位，got %v", viols)
	}
}
