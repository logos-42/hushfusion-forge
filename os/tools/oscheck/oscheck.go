// Package main 实现 HUSHFUSION OS 决策层一致性门（os/tools/oscheck）。
//
// 把 os/SPEC.md §3 的状态转换铁律从「人肉纪律」变成可执行检查：
//
//	任务池（os/TASKS.md）
//	  T1  状态值合法：proposed / active / blocked / done / dropped
//	  T2  active ≤ 1（唯一主任务铁律）
//	  T3  每个 active 任务有详情节（### T-XX … 状态: active）且与表内状态一致
//	  T4  每个 active 任务在 os/acceptance/ 有验收标准文件（<id小写去连字符>*.md）
//	  T5  P = floor(I×E×R/C)，且 I/E/R/C ∈ 1..5
//	  T6  blocked 必须写阻塞原因（依赖列非空且非「—」）
//	  T7  任务 ID 不重复
//	  T8  候选池与表头声明一致：active 行置顶，其余按 P 降序
//
//	kernel 回执（os/kernel_receipts/，OS↔kernel 双向环下半圈，2026-10-18）
//	  K1  active 任务必有 kernel 回执且状态非 failed ——
//	      AI 接入后任务经 kernel 提交（go run ./os/tools/ossubmit），回执必在
//	  K2  标 done 的任务必有 settled 回执（勾验计数 + 证据路径）——
//	      done 不许自称，必须经 ossubmit --settle 逐条勾验
//
//	实验状态机（os/EXPERIMENTS.md）
//	  E1  态 n 非空 ⇒ 态 n−1 非空（态5 需态4 … 态2 需态1）——
//	      「没有独立反馈，结论不许进第 5 态」（SPEC §3 / EXPERIMENTS.md 头部铁律）
//
//	验收目录（os/acceptance/）
//	  A1  每个 acceptance 文件能对应回任务池某个 ID 前缀（不许孤儿文件）
//
// 铁律出处：os/SPEC.md §3 与 docs/wiki/os-operations.md「状态转换铁律」。
// 解析层在 oscore 包（与 ossubmit 共用同一把尺子）。
// 本门自身经 main_test.go 变异测试：每条检查至少有一条「故意改坏必须变红」
// 的用例 —— 与 owners_test.go 改名册证明门能红同一纪律；测试随 G5
// （go test ./...）自动进门禁。
package main

import (
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/logos-42/hushfusion-forge/os/tools/oscore"
)

// checkTasks 是 T1–T8（任务池状态机铁律）。
func checkTasks(tasksText string, acceptanceNames []string) []string {
	var viols []string
	rows := oscore.ParseTaskTable(tasksText)
	if len(rows) == 0 {
		return []string{"T0: 候选池表格解析为空（TASKS.md 结构变了？）"}
	}

	seen := map[string]bool{}
	for _, r := range rows {
		if seen[r.ID] { // T7
			viols = append(viols, "T7: 任务 ID 重复 "+r.ID)
		}
		seen[r.ID] = true

		if !oscore.ValidStates[r.State] { // T1
			viols = append(viols, "T1: "+r.ID+" 状态非法: "+strconv.Quote(r.State))
		}

		viols = append(viols, checkScores(r)...)

		if r.State == "blocked" && oscore.IsEmpty(r.Deps) { // T6
			viols = append(viols, "T6: "+r.ID+" blocked 但未写阻塞原因")
		}
	}

	var active []string
	for _, r := range rows {
		if r.State == "active" {
			active = append(active, r.ID)
		}
	}
	if len(active) > 1 { // T2
		viols = append(viols,
			"T2: active 任务超过 1 件（唯一主任务铁律）: "+strings.Join(active, ", "))
	}

	details := oscore.ParseDetailSections(tasksText)
	for _, tid := range active { // T3 / T4
		secs := details[tid]
		switch {
		case len(secs) == 0:
			viols = append(viols, "T3: active 任务 "+tid+
				" 缺少详情节（### "+tid+"… 状态: active）")
		case len(secs) > 1:
			viols = append(viols, "T3: "+tid+" 有多个详情节（应恰好 1 个）")
		case secs[0] != "active":
			viols = append(viols, "T3: "+tid+" 详情节状态 "+
				strconv.Quote(secs[0])+" 与表内 active 不一致")
		}
		prefix := oscore.AcceptPrefix(tid)
		if !anyHasPrefix(acceptanceNames, prefix) {
			viols = append(viols, "T4: active 任务 "+tid+
				" 在 os/acceptance/ 没有验收标准文件（"+prefix+"*.md）")
		}
	}

	for tid := range details { // 详情节引用的任务必须在表里
		if !seen[tid] {
			viols = append(viols, "T3: 详情节 "+tid+" 在候选池表格中不存在")
		}
	}

	// T8 排序：与表头声明「按 P 排序」一致 —— active 置顶，其余 P 降序
	type keyed struct {
		id string
		k  [2]int
	}
	var ks []keyed
	for _, r := range rows {
		p, err := oscore.ToInt(r.P)
		if err != nil {
			p = -1
		}
		pin := 1
		if r.State == "active" {
			pin = 0
		}
		ks = append(ks, keyed{id: r.ID, k: [2]int{pin, -p}})
	}
	sorted := make([]keyed, len(ks))
	copy(sorted, ks)
	sort.SliceStable(sorted, func(a, b int) bool {
		if sorted[a].k[0] != sorted[b].k[0] {
			return sorted[a].k[0] < sorted[b].k[0]
		}
		return sorted[a].k[1] < sorted[b].k[1]
	})
	var actual, expected []string
	for idx := range ks {
		actual = append(actual, ks[idx].id)
		expected = append(expected, sorted[idx].id)
	}
	if strings.Join(actual, ",") != strings.Join(expected, ",") {
		viols = append(viols, "T8: 候选池排序与表头声明不符：当前 ["+
			strings.Join(actual, " ")+"]，应为 ["+strings.Join(expected, " ")+"]")
	}

	return viols
}

// checkScores 是 T5：P = floor(I×E×R/C)，且 I/E/R/C ∈ 1..5。
func checkScores(r oscore.TaskRow) []string {
	vals := map[string]int{}
	for _, kv := range [][2]string{{"I", r.I}, {"E", r.E}, {"R", r.R},
		{"C", r.C}, {"P", r.P}} {
		v, err := oscore.ToInt(kv[1])
		if err != nil {
			return []string{"T5: " + r.ID + " I/E/R/C/P 不是整数: " +
				strings.Join([]string{r.I, r.E, r.R, r.C, r.P}, ",")}
		}
		vals[kv[0]] = v
	}
	for _, k := range []string{"I", "E", "R", "C"} {
		if vals[k] < 1 || vals[k] > 5 {
			return []string{"T5: " + r.ID + " I/E/R/C 必须在 1..5（got " +
				strings.Join([]string{r.I, r.E, r.R, r.C}, ",") + "）"}
		}
	}
	prod := vals["I"] * vals["E"] * vals["R"]
	if vals["P"] != prod/vals["C"] {
		return []string{"T5: " + r.ID + " P=" + r.P + " 与 I×E×R/C=floor(实际 " +
			oscore.FormatFloat(float64(prod)/float64(vals["C"])) + ") 不符（应为 " +
			strconv.Itoa(prod/vals["C"]) + "）"}
	}
	return nil
}

func anyHasPrefix(names []string, prefix string) bool {
	for _, n := range names {
		stem := strings.TrimSuffix(n, filepath.Ext(n))
		if strings.HasPrefix(stem, prefix) {
			return true
		}
	}
	return false
}

// checkExperiments 是 E1：态 n 非空 ⇒ 态 n−1 非空（不能跳态）。
func checkExperiments(expText string) []string {
	var viols []string
	rows := oscore.ParseExperimentRows(expText)
	if len(rows) == 0 {
		return []string{"E0: 实验映射表解析为空（EXPERIMENTS.md 结构变了？）"}
	}
	for _, c := range rows {
		tag := strings.Trim(c[0], "` ")
		for n := 5; n >= 2; n-- { // c[n] 是态 n（c[0]=tag）
			if !oscore.IsEmpty(c[n]) && oscore.IsEmpty(c[n-1]) {
				viols = append(viols, "E1: 实验 "+tag+" 态"+
					strconv.Itoa(n)+"有内容但态"+strconv.Itoa(n-1)+
					"为空（状态必须逐级走，不能跳态）")
			}
		}
	}
	return viols
}

// checkAcceptance 是 A1：acceptance 文件必须能对应回某个任务 ID 前缀。
func checkAcceptance(acceptanceNames, taskIDs []string) []string {
	var viols []string
	prefixes := make([]string, 0, len(taskIDs))
	for _, t := range taskIDs {
		prefixes = append(prefixes, oscore.AcceptPrefix(t))
	}
	// 长前缀优先（t02 不会误吞 t02a 的文件）
	sort.SliceStable(prefixes, func(a, b int) bool {
		return len(prefixes[a]) > len(prefixes[b])
	})
	names := make([]string, len(acceptanceNames))
	copy(names, acceptanceNames)
	sort.Strings(names)
	for _, name := range names {
		stem := strings.TrimSuffix(name, filepath.Ext(name))
		if !startsWithAny(stem, prefixes) {
			viols = append(viols, "A1: os/acceptance/"+name+
				" 对应不上任何任务 ID（孤儿验收文件）")
		}
	}
	return viols
}

func startsWithAny(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// checkKernelReceipts 是 K1/K2：OS↔kernel 双向环的机器强制。
//
//	K1: active 任务必有回执且状态 ∈ {submitted, settled}
//	    （failed 也不行 —— 提交被拒 ≠ 提交存在）
//	K2: state=done 必有 settled 回执（done 不许自称，必须经 --settle 勾验）
func checkKernelReceipts(tasksText string, receipts map[string]*oscore.Receipt) []string {
	var viols []string
	for _, r := range oscore.ParseTaskTable(tasksText) {
		receipt := receipts[r.ID]
		switch r.State {
		case "active":
			if receipt == nil {
				viols = append(viols, "K1: active 任务 "+r.ID+
					" 没有 kernel 回执（先 go run ./os/tools/ossubmit 提交）")
			} else if receipt.Status != oscore.StatusSubmitted &&
				receipt.Status != oscore.StatusSettled {
				viols = append(viols, "K1: active 任务 "+r.ID+
					" 的 kernel 回执状态为 "+strconv.Quote(receipt.Status)+
					"（须为 submitted/settled；failed 回执不许挂着 active）")
			}
		case "done":
			if receipt == nil || receipt.Status != oscore.StatusSettled ||
				receipt.Settle == nil {
				viols = append(viols, "K2: "+r.ID+
					" 标 done 但没有 settled 回执（done 必须经 ossubmit --settle 逐条勾验）")
			}
		}
	}
	return viols
}

// runAll 汇总全部检查（顺序：任务池 → kernel 回执 → 实验 → 验收目录）。
func runAll(tasksText, expText string, acceptanceNames []string,
	receipts map[string]*oscore.Receipt) []string {
	rows := oscore.ParseTaskTable(tasksText)
	taskIDs := make([]string, 0, len(rows))
	for _, r := range rows {
		taskIDs = append(taskIDs, r.ID)
	}
	viols := checkTasks(tasksText, acceptanceNames)
	viols = append(viols, checkKernelReceipts(tasksText, receipts)...)
	viols = append(viols, checkExperiments(expText)...)
	viols = append(viols, checkAcceptance(acceptanceNames, taskIDs)...)
	return viols
}
