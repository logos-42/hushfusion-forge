package oskernel

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/logos-42/hushfusion-forge/os/tools/oscore"
)

// sysInvariants 是内核的只读不变量审计：复用 oscore（与 oscheck 同一把尺子）
// 对当前状态跑 T1/T2/T5/T6/T7 + E1 的核心检查，返回违规列表。
//
// 定位（诚实边界）：这是**进程内快速审计**，给 TUI/CLI 即时反馈；**权威日门
// 仍是 `forge os check`(oscheck)**（它还覆盖 K1/K2 kernel 回执与 A1 验收孤儿，
// 且自身经变异测试）。写系统调用的写前守卫保证内核永不*引入*新违规。
func sysInvariants(k *Kernel, _ map[string]any) (Result, error) {
	tasksText, err := os.ReadFile(filepath.Join(k.Base, "TASKS.md"))
	if err != nil {
		return Result{}, err
	}
	viols := auditTasks(string(tasksText))

	if expText, err := os.ReadFile(filepath.Join(k.Base, "EXPERIMENTS.md")); err == nil {
		viols = append(viols, auditExperiments(string(expText))...)
	}
	return ok(map[string]any{"violations": viols, "ok": len(viols) == 0}), nil
}

func auditTasks(text string) []string {
	var viols []string
	rows := oscore.ParseTaskTable(text)
	if len(rows) == 0 {
		return []string{"T0: 候选池表格解析为空"}
	}
	seen := map[string]bool{}
	active := 0
	for _, r := range rows {
		if seen[r.ID] {
			viols = append(viols, "T7: 任务 ID 重复 "+r.ID)
		}
		seen[r.ID] = true
		if !oscore.ValidStates[r.State] {
			viols = append(viols, "T1: "+r.ID+" 状态非法: "+strconv.Quote(r.State))
		}
		if r.State == "active" {
			active++
		}
		viols = append(viols, auditScore(r)...)
		if r.State == "blocked" && oscore.IsEmpty(r.Deps) {
			viols = append(viols, "T6: "+r.ID+" blocked 但未写阻塞原因")
		}
	}
	if active > 1 {
		viols = append(viols, fmt.Sprintf("T2: active 任务超过 1 件（%d）", active))
	}
	return viols
}

func auditScore(r oscore.TaskRow) []string {
	vals := map[string]int{}
	for _, kv := range [][2]string{{"I", r.I}, {"E", r.E}, {"R", r.R}, {"C", r.C}, {"P", r.P}} {
		v, err := oscore.ToInt(kv[1])
		if err != nil {
			return []string{"T5: " + r.ID + " I/E/R/C/P 不是整数"}
		}
		vals[kv[0]] = v
	}
	for _, k := range []string{"I", "E", "R", "C"} {
		if vals[k] < 1 || vals[k] > 5 {
			return []string{"T5: " + r.ID + " I/E/R/C 必须在 1..5"}
		}
	}
	if vals["P"] != vals["I"]*vals["E"]*vals["R"]/vals["C"] {
		return []string{"T5: " + r.ID + " P 与 I×E×R/C 不符"}
	}
	return nil
}

func auditExperiments(text string) []string {
	var viols []string
	for _, c := range oscore.ParseExperimentRows(text) {
		tag := c[0]
		for n := 5; n >= 2; n-- {
			if !oscore.IsEmpty(c[n]) && oscore.IsEmpty(c[n-1]) {
				viols = append(viols, fmt.Sprintf("E1: 实验 %s 态%d 有内容但态%d 为空（不能跳态）", tag, n, n-1))
			}
		}
	}
	return viols
}
