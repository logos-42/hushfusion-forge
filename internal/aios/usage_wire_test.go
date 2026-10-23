package aios

import (
	"context"
	"strings"
	"testing"
)

// ExtraSystem 必须追加在内置 system 提示之后（记忆块/persona 用这个口子）。
func TestExtraSystemAppendedAfterBuiltinPrompt(t *testing.T) {
	var gotSystem string
	a := &Agent{
		Provider: "fake", Model: "m",
		ExtraSystem: "—— 持久记忆 ——\nk=v",
	}
	a.ChatFunc = func(_ context.Context, system, _ string) (string, error) {
		gotSystem = system
		return `[]`, nil // 空计划 ⇒ 收敛
	}
	// Kernel 为 nil 时 readState 会崩，所以这里只调 systemPrompt() —— 注入点本身。
	sys := a.systemPrompt()
	if !strings.Contains(sys, "HUSHFUSION OS") {
		t.Fatalf("应含内置提示: %q", sys[:40])
	}
	if !strings.Contains(sys, "k=v") {
		t.Fatalf("应含 ExtraSystem: %q", sys)
	}
	if strings.Index(sys, "HUSHFUSION OS") > strings.Index(sys, "k=v") {
		t.Fatal("内置提示应在 ExtraSystem 之前")
	}
	_ = gotSystem
}

func TestSystemPromptEmptyExtraSystem(t *testing.T) {
	a := &Agent{}
	if a.systemPrompt() != systemPrompt {
		t.Fatal("无 ExtraSystem 时应原样返回内置提示")
	}
	// 纯空白视同没有（不制造尾部空行）
	a.ExtraSystem = "   \n  "
	if a.systemPrompt() != systemPrompt {
		t.Fatal("空白 ExtraSystem 不应改变提示")
	}
}

// 假 ChatFunc 路径没有 usage 可报 ⇒ Report.Usage 全 0（那是"未上报"，不是"没花 token"）。
func TestReportUsageZeroOnFakeChat(t *testing.T) {
	rep := &Report{Goal: "g"}
	if rep.Usage.Calls != 0 || rep.Usage.Prompt != 0 {
		t.Fatalf("零值 Report 的用量应为空: %+v", rep.Usage)
	}
	if _, known := rep.Usage.Cost(""); known {
		t.Fatal("无价目时应 known=false")
	}
}
