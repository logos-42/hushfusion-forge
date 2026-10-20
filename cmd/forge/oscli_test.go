package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/logos-42/hushfusion-forge/os/tools/oscore"
)

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
