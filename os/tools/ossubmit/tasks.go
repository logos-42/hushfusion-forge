// TASKS.md 状态翻转 —— ossubmit --settle 的最后一步（唯一允许写 TASKS.md 的代码路径）。
//
// 只动目标任务的两处：候选池表格行的状态列 + ### 详情节的「状态: xxx」。
// 其余字节逐字保留（表格其它列、说明段落、候选池排序都由 oscheck T8 盯着）。
package main

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/logos-42/hushfusion-forge/os/tools/oscore"
)

var detailStateRe = regexp.MustCompile(`^###\s+(T-\d{2})[^0-9].*?状态[:：]\s*([A-Za-z]+)`)

// flipTaskState 把 taskID 的状态从 from 翻到 to（表格行必翻恰好一次；
// 详情节存在则翻、不存在不算错 —— T3 只要求 active 有详情节）。
func flipTaskState(text, taskID, from, to string) (string, error) {
	lines := strings.Split(text, "\n")
	rowFlipped, detailFlipped := 0, 0

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		// 候选池表格行：| T-01 | … | active | … |
		if strings.HasPrefix(trimmed, "| "+taskID+" |") {
			parts := strings.Split(line, "|")
			// parts[0]=="" parts[1]=" T-01 " … parts[8]=" active " parts[末]=""
			if len(parts) >= 9 && oscore.StateCell(parts[8]) == from {
				parts[8] = strings.Replace(parts[8], from, to, 1)
				lines[i] = strings.Join(parts, "|")
				rowFlipped++
			}
		}

		// ### 详情节：### T-01｜… — 状态: active
		if m := detailStateRe.FindStringSubmatch(trimmed); m != nil &&
			m[1] == taskID && m[2] == from {
			lines[i] = strings.Replace(line, "状态: "+from, "状态: "+to, 1)
			if lines[i] == line { // 全角冒号
				lines[i] = strings.Replace(line, "状态："+from, "状态："+to, 1)
			}
			if lines[i] != line {
				detailFlipped++
			}
		}
	}

	if rowFlipped != 1 {
		return "", fmt.Errorf("%s 表格行状态翻转 %d 次（应恰好 1 次，from=%s）",
			taskID, rowFlipped, from)
	}
	_ = detailFlipped // 详情节 0 或 1 次都合法；重复详情节由 oscheck T3 抓
	return strings.Join(lines, "\n"), nil
}
