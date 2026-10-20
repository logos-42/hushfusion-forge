package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/logos-42/hushfusion-forge/os/tools/oscore"
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

// 退格必须一次删掉整个 rune（含多字节），不留悬挂续字节。
func TestTrimLastRuneDropsFullRune(t *testing.T) {
	if got := string(trimLastRune([]byte("任务a"))); got != "任务" {
		t.Fatalf("trimLastRune 应删整个 rune, got %q", got)
	}
	if string(trimLastRune([]byte("x"))) != "" {
		t.Fatal("trimLastRune 单字节应删空")
	}
}

// TUI 录入路径端到端：submitForm 把表单写进临时 os/ 且保持可被 oscore 解析。
func TestSubmitFormTaskAndDecide(t *testing.T) {
	dir := t.TempDir()
	must := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("TASKS.md", `# TASKS
| ID | 任务 | I | E | R | C | P | 状态 | 依赖/阻塞 |
|:--|:--|:-:|:-:|:-:|:-:|:-:|:--|:--|
| T-01 | x | 5 | 5 | 4 | 3 | **33** | active | — |
`)
	must("DECISIONS.md", `# DECISIONS
## 决策日志
| ID | 日期 | 决策 | 依据 | 备选 | 后果 | 状态 |
|:--|:--|:--|:--|:--|:--|:--|
| D-01 | 2026-10-17 | a | b | c | d | done |
`)

	// 任务表单
	tf := newTaskForm()
	tf.fields[0].buf = []byte("T-09")
	tf.fields[1].buf = []byte("录入的任务")
	tf.fields[2].buf = []byte("3")
	tf.fields[3].buf = []byte("3")
	tf.fields[4].buf = []byte("3")
	tf.fields[5].buf = []byte("2")
	if msg, err := submitForm(dir, tf); err != nil {
		t.Fatalf("submitForm task: %v", err)
	} else if !strings.Contains(msg, "P=13") {
		t.Fatalf("应报告 P=13, got %q", msg)
	}
	rows := oscore.ParseTaskTable(readFileT(t, filepath.Join(dir, "TASKS.md")))
	if len(rows) != 2 || rows[1].ID != "T-09" || rows[1].State != "proposed" {
		t.Fatalf("任务写入后应可解析且为 proposed: %+v", rows)
	}

	// 决策表单
	df := newDecideForm()
	df.fields[0].buf = []byte("TUI 录入的决策")
	df.fields[1].buf = []byte("证据路径")
	msg, err := submitForm(dir, df)
	if err != nil {
		t.Fatalf("submitForm decide: %v", err)
	}
	if msg != "D-02 已录入" {
		t.Fatalf("应分配 D-02, got %q", msg)
	}
}

func readFileT(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
