package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/logos-42/hushfusion-forge/os/tools/oscore"
)

func writeF(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
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

// doPropose/doOpp/doDecide（CLI 与 TUI 共用的内核写入核心）端到端：写进临时 os/
// 且可被 oscore 解析，propose 按 P=floor(I*E*R/C) 且为 proposed。
func TestDoProposeOppDecide(t *testing.T) {
	dir := t.TempDir()
	writeF(t, dir, "TASKS.md", "# TASKS\n| ID | 任务 | I | E | R | C | P | 状态 | 依赖/阻塞 |\n|:--|:--|:-:|:-:|:-:|:-:|:-:|:--|:--|\n| T-01 | x | 5 | 5 | 4 | 3 | **33** | active | — |\n")
	writeF(t, dir, "DECISIONS.md", "# DECISIONS\n## 决策日志\n| ID | 日期 | 决策 | 依据 | 备选 | 后果 | 状态 |\n|:--|:--|:--|:--|:--|:--|:--|\n| D-01 | 2026-10-17 | a | b | c | d | done |\n")
	writeF(t, dir, "OPPORTUNITIES.md", "# OPP\n## 机会池\n| 机会 | 来源 | 匹配 | 下一步 | 状态 | 日期 |\n|:--|:--|:--|:--|:--|:--|\n")

	p, err := doPropose(dir, "T-09", 3, 3, 3, 2, "任务", "—")
	if err != nil {
		t.Fatal(err)
	}
	if p != 13 {
		t.Fatalf("P 应为 floor(3*3*3/2)=13, got %d", p)
	}
	rows := oscore.ParseTaskTable(readFileT(t, filepath.Join(dir, "TASKS.md")))
	if len(rows) != 2 || rows[1].ID != "T-09" || rows[1].State != "proposed" {
		t.Fatalf("propose 写入后应为 proposed: %+v", rows)
	}

	if err := doOpp(dir, "渠道", "self", "m", "next", "proposed", "2026-10-21"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readFileT(t, filepath.Join(dir, "OPPORTUNITIES.md")), "渠道") {
		t.Fatal("doOpp 应把机会写进 OPPORTUNITIES.md")
	}

	id, err := doDecide(dir, "决策", "证据", "—", "—", "active")
	if err != nil {
		t.Fatal(err)
	}
	if id != "D-02" {
		t.Fatalf("应分配 D-02, got %s", id)
	}
}
