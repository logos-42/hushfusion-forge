package main

import (
	"strings"
	"testing"
)

func TestDispWidthCJKAndANSI(t *testing.T) {
	if got := dispWidth("abc"); got != 3 {
		t.Fatalf("ASCII 宽应=3, got %d", got)
	}
	if got := dispWidth("任务"); got != 4 {
		t.Fatalf("CJK 每字宽=2, '任务' 应=4, got %d", got)
	}
	// ANSI 转义不占宽
	if got := dispWidth(cyan + "ab" + ansiReset); got != 2 {
		t.Fatalf("ANSI 应被忽略, got %d", got)
	}
}

func TestFitLinePadsAndTruncates(t *testing.T) {
	const inner = 20
	line := fitLine("hi", inner)
	if dispWidth(line) != inner {
		t.Fatalf("fitLine 应补齐到宽 %d, got %d (%q)", inner, dispWidth(line), line)
	}
	long := strings.Repeat("x", 100)
	if dispWidth(fitLine(long, inner)) != inner {
		t.Fatalf("超长行应被截断到宽 %d", inner)
	}
}

func TestRenderPanelHasBorders(t *testing.T) {
	out := renderPanel("任务池", []string{"[active] T-01 主线", "T-08 xxx"}, 40, true)
	for _, frag := range []string{"╭", "╮", "│", "╰", "╯", "任务池"} {
		if !strings.Contains(out, frag) {
			t.Fatalf("面板应含 %q:\n%s", frag, out)
		}
	}
}

func TestBlankCell(t *testing.T) {
	if !blankCell("") || !blankCell("—") || !blankCell("  ") {
		t.Fatal("空位应判定为空")
	}
	if blankCell("ok") {
		t.Fatal("非空内容不应判定为空")
	}
}
