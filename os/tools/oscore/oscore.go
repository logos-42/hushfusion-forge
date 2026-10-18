// Package oscore 是 HUSHFUSION OS 决策层的共享解析层：
// TASKS.md 候选池 / 详情节 / EXPERIMENTS.md 映射表的解析，以及
// kernel 回执（os/kernel_receipts/*.json）的读写。
//
// 被 oscheck（一致性门）与 ossubmit（OS↔kernel 提交/回写）共同引用 ——
// 两边必须用同一把尺子读 TASKS.md，否则门和写回方各说各话。
// 检查编号 T1–T8 / E1 / A1 / K1–K2 的语义定义在 oscheck 包。
package oscore

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	taskRowRe = regexp.MustCompile(`^(T-\d{2})$`)
	// Python 侧 DETAIL_RE 的 Go 等价：RE2 不支持 \b，用 [^0-9] 保证
	// ID 后必须跟非数字字符（"T-012" 不算 T-01 的详情节）。
	detailRe = regexp.MustCompile(`^###\s+(T-\d{2})[^0-9].*?状态[:：]\s*([A-Za-z]+)`)
)

// ValidStates 是任务状态机的全部合法状态（SPEC §3）。
var ValidStates = map[string]bool{
	"proposed": true, "active": true, "blocked": true,
	"done": true, "dropped": true,
}

var emptyCell = map[string]bool{"": true, "—": true, "-": true, "–": true}

// TaskRow 是 TASKS.md 候选池表格的一行。
type TaskRow struct {
	ID    string
	I     string
	E     string
	R     string
	C     string
	P     string
	State string
	Deps  string
}

func cells(line string) []string {
	s := strings.Trim(line, " \t")
	s = strings.Trim(s, "|")
	parts := strings.Split(s, "|")
	out := make([]string, len(parts))
	for i, p := range parts {
		out[i] = strings.TrimSpace(p)
	}
	return out
}

// StateCell 把「proposed（预备）」归一成「proposed」。
func StateCell(cell string) string {
	if i := strings.IndexAny(cell, "（("); i >= 0 {
		return strings.TrimSpace(cell[:i])
	}
	return strings.TrimSpace(cell)
}

// IsEmpty 报告表格单元格是否为空位（空串或「—」类占位符）。
func IsEmpty(cell string) bool { return emptyCell[strings.TrimSpace(cell)] }

// ToInt 把表格单元格转成 int（剥掉 **粗体** 等 markdown 强调符）。
func ToInt(cell string) (int, error) {
	c := strings.NewReplacer("*", "", "`", "", " ", "").Replace(cell)
	return strconv.Atoi(c)
}

// AcceptPrefix 把任务 ID 转成验收文件前缀：T-01 → t01。
func AcceptPrefix(taskID string) string {
	return strings.ReplaceAll(strings.ToLower(taskID), "-", "")
}

// ParseTaskTable 解析 TASKS.md 候选池表格（首列匹配 T-NN 的行）。
func ParseTaskTable(text string) []TaskRow {
	var rows []TaskRow
	for _, line := range strings.Split(text, "\n") {
		s := strings.TrimSpace(line)
		if !strings.HasPrefix(s, "|") {
			continue
		}
		c := cells(s)
		if len(c) < 9 || !taskRowRe.MatchString(c[0]) {
			continue
		}
		rows = append(rows, TaskRow{
			ID: c[0], I: c[2], E: c[3], R: c[4], C: c[5], P: c[6],
			State: StateCell(c[7]), Deps: c[8],
		})
	}
	return rows
}

// ParseDetailSections 解析 ### T-XX 详情节，返回 {id: [状态…]}。
func ParseDetailSections(text string) map[string][]string {
	out := map[string][]string{}
	for _, line := range strings.Split(text, "\n") {
		if m := detailRe.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			out[m[1]] = append(out[m[1]], m[2])
		}
	}
	return out
}

// ParseExperimentRows 解析 EXPERIMENTS.md 的「已有实验 → 状态映射」表
// （6 列，跳过代码围栏、表头与分隔行）。cells[0]=tag，cells[1..5]=态1..态5。
func ParseExperimentRows(text string) [][]string {
	var rows [][]string
	inFence := false
	for _, line := range strings.Split(text, "\n") {
		s := strings.TrimSpace(line)
		if strings.HasPrefix(s, "```") {
			inFence = !inFence
			continue
		}
		if inFence || !strings.HasPrefix(s, "|") {
			continue
		}
		c := cells(s)
		if len(c) != 6 {
			continue
		}
		if strings.Contains(c[0], "实验") || allRuneIn(c[0], '-', ':') {
			continue // 表头 / 分隔行
		}
		rows = append(rows, c)
	}
	return rows
}

func allRuneIn(s string, allowed ...rune) bool {
	for _, r := range s {
		ok := false
		for _, a := range allowed {
			if r == a {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// FormatFloat 与 Python f"{x:.2f}" 等价（T5 违规消息用）。
func FormatFloat(x float64) string {
	return strconv.FormatFloat(x, 'f', 2, 64)
}
