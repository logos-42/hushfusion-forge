package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/logos-42/hushfusion-forge/internal/aios"
)

// captureStdout 捕获 fn 期间的 stdout（TUI 画屏用 fmt.Print 写 stdout）。
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()
	fn()
	_ = w.Close()
	os.Stdout = old
	return <-done
}

func TestDrawAIScreenNoPanic(t *testing.T) {
	rep := &aios.Report{
		Goal: "整理机会池",
		Plan: []aios.PlanStep{
			{Syscall: "tasks.propose", Allowed: true, Args: map[string]any{"id": "T-09", "i": 3}},
			{Syscall: "tasks.mark_done", Allowed: false, Reason: "越权"},
		},
		Results: []aios.ExecResult{
			{Syscall: "tasks.propose", OK: true, Message: "T-09 已插入（P=9）"},
		},
		Notes: []string{"AI 只提议"},
	}
	out := captureStdout(t, func() { drawAIScreen(rep, nil, "f") })
	if !strings.Contains(out, "AI 粘合报告") || !strings.Contains(out, "tasks.propose") {
		t.Fatalf("报告屏应含标题与步骤:\n%s", out)
	}
	if !strings.Contains(out, "✗拒绝") {
		t.Fatalf("越权步骤应显示拒绝:\n%s", out)
	}
}

func TestDrawAIScreenError(t *testing.T) {
	out := captureStdout(t, func() { drawAIScreen(nil, io.ErrUnexpectedEOF, "f") })
	if !strings.Contains(out, "运行失败") {
		t.Fatalf("错误应被呈现:\n%s", out)
	}
}

func TestDrawThinkingNoPanic(t *testing.T) {
	out := captureStdout(t, func() { drawThinking("目标X", "openrouter", "deepseek/deepseek-chat") })
	if !strings.Contains(out, "思考中") {
		t.Fatalf("应含思考中提示:\n%s", out)
	}
}

func TestNewAIGoalForm(t *testing.T) {
	f := newAIGoalForm()
	if f.kind != "aigoal" || len(f.fields) != 3 {
		t.Fatalf("aigoal 表单应有 3 字段(目标/provider/model), got kind=%s fields=%d", f.kind, len(f.fields))
	}
	if f.fields[0].label != "目标" {
		t.Fatalf("首字段应为目标, got %q", f.fields[0].label)
	}
}
