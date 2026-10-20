package main

import (
	"strings"
	"testing"
)

// G: patchTasksTable 必须按 P 降序有序插入且不破坏分隔行（否则 oscheck T8 必红）。
func TestPatchTasksTableInsertOrder(t *testing.T) {
	base := `# TASKS

## 候选池（按 P 排序）

| ID | 任务 | I | E | R | C | P | 状态 | 依赖/阻塞 |
|:--|:--|:-:|:-:|:-:|:-:|:-:|:--|:--|
| T-01 | 主线 | 5 | 5 | 4 | 3 | **33** | active | — |
| T-08 | a | 4 | 3 | 4 | 1 | **48** | proposed | — |
| T-02 | b | 3 | 3 | 5 | 1 | **45** | proposed | — |
| T-04 | c | 4 | 2 | 5 | 3 | **13** | proposed | — |
| T-07 | d | 3 | 2 | 4 | 2 | **12** | proposed | — |
`
	// 新任务 P=13，应插在 T-04 之后、T-07 之前（同 P 稳定排在已有之后）。
	out := patchTasksTable(base,
		"| T-09 | 新 | 3 | 3 | 3 | 2 | **13** | proposed | — |", 13)

	// 分隔行必须还在
	if !strings.Contains(out, "|:--|") {
		t.Fatal("分隔行被吞掉")
	}
	order := []string{}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "|") &&
			strings.Contains(line, "T-") {
			c := splitCells(line)
			if len(c) >= 1 {
				order = append(order, c[0])
			}
		}
	}
	want := []string{"T-01", "T-08", "T-02", "T-04", "T-09", "T-07"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("插入顺序错误: got %s, want %s", strings.Join(order, ","), strings.Join(want, ","))
	}
}

func TestPatchTasksTableKeepsActiveTop(t *testing.T) {
	base := `| ID | 任务 | I | E | R | C | P | 状态 | 依赖/阻塞 |
|:--|:--|:-:|:-:|:-:|:-:|:-:|:--|:--|
| T-01 | x | 5 | 5 | 4 | 3 | **33** | active | — |
| T-08 | y | 4 | 3 | 4 | 1 | **48** | proposed | — |
`
	out := patchTasksTable(base, "| T-09 | z | 1 | 1 | 1 | 1 | **1** | proposed | — |", 1)
	firstData := ""
	for _, line := range strings.Split(out, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "|") && strings.Contains(t, "T-") {
			firstData = splitCells(t)[0]
			break
		}
	}
	if firstData != "T-01" {
		t.Fatalf("active 行必须置顶, got %s", firstData)
	}
}
