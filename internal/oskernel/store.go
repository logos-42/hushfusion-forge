// Package oskernel 是 HUSHFUSION OS 的「宏内核」层（2026-10-20 起，phase-1）。
//
// 动机（leo 2026-10-20）：把 OS 从「散落的 Markdown 文档系统」升级为
// **Linux 式宏内核架构**——一个内核核心拥有状态与持久化，所有访问经
// **系统调用（syscall）**入口，各决策域是内核里的子系统驱动，铁律是
// 系统调用边界的**不变量守卫**。映射关系：
//
//	Linux 内核              HUSHFUSION OS kernel
//	────────────────────    ────────────────────────────────
//	syscall(2) 入口          Kernel.Syscall(name, args)
//	子系统（VFS/net/sched）  drivers：tasks/experiments/opportunities/decisions/receipts
//	块设备 / VFS             blockdev：os/*.md（git 背书，oscheck 仍是权威门）
//	能力/不变量检查           invariant：写前守卫（active≤1 / P=floor(I×E×R/C) / E1 不跳态…）
//
// phase-1 边界（诚实）：
//   - 持久化仍是 Markdown（git 背书 + oscheck 权威门），内核把它当「块设备」读写；
//   - 写系统调用**写前守卫**保证内核永不引入违规；权威日门仍是 `forge os check`(oscheck)；
//   - 进程内单内核（多内核/并发在 headless 注入后，见 T-08）。
package oskernel

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// taskRowRe 与 oscore 同一把尺子（候选池行以 T-NN 开头）。
var taskRowRe = regexp.MustCompile(`^\s*\|?\s*(T-\d{2})`)

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

var sepReOS = regexp.MustCompile(`^:?-+:?$`)

func isSepRowOS(c []string) bool {
	for _, x := range c {
		if !sepReOS.MatchString(strings.TrimSpace(x)) {
			return false
		}
	}
	return true
}

func atoiSafe(s string) int {
	s = strings.NewReplacer("*", "", "`", "", " ", "").Replace(s)
	n, _ := strconv.Atoi(s)
	return n
}

// patchTasksTable 在候选池表格里按 P 降序有序插入一行（active 行保持置顶），
// 保留表头与分隔行（否则 oscheck T8 必红）。
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
	newLines := append([]string{}, lines[:start]...)
	newLines = append(newLines, out...)
	newLines = append(newLines, lines[end:]...)
	return strings.Join(newLines, "\n")
}

// appendTableRow 在指定小节的表格末尾追加一行（保结构，不改其它行）。
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
			if strings.HasPrefix(strings.TrimSpace(l), "|") && !isSepRowOS(splitCells(l)) {
				lastRow = i
			}
		}
	}
	if lastRow < 0 {
		return fmt.Errorf("%s 未找到「%s」下的表格数据行", path, sectionHeader)
	}
	out := append([]string{}, lines[:lastRow+1]...)
	out = append(out, row)
	out = append(out, lines[lastRow+1:]...)
	return os.WriteFile(path, []byte(strings.Join(out, "\n")), 0o644)
}

// setExpState 写入实验某态（E1：态 n 非空 ⇒ 态 n-1 必非空，不能跳态）。
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

// nextDecisionID 扫描现有决策，返回下一个 D-NN。
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
