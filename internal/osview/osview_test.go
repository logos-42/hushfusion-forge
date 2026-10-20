package osview

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 在临时目录放一份最小 os/ 文档，验证解析与渲染不崩、能识别 active。
func writeTempOS(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("NORTH_STAR.md", "# NORTH_STAR\n方向：做对的事。\n")
	write("CURRENT_STATE.md", "---\ntitle: x\n---\n**一句话状态**：在跑 Phase 0。\n")
	write("TASKS.md", `# TASKS

| ID | 任务 | I | E | R | C | P | 状态 | 依赖/阻塞 |
|:--|:--|:-:|:-:|:-:|:-:|:-:|:--|:--|
| T-01 | 主线 | 5 | 5 | 4 | 3 | **33** | active | — |
| T-02 | 候选 | 3 | 3 | 5 | 1 | **45** | proposed | — |
`)
	write("EXPERIMENTS.md", `# EXPERIMENTS

| 实验（tag） | 态1 | 态2 | 态3 | 态4 | 态5 |
|:--|:--|:--|:--|:--|:--|
| p0 | q | plan | ok | ok | done |
`)
	write("OPPORTUNITIES.md", `# OPPORTUNITIES

## 机会池

| 机会 | 来源 | 研究方向匹配 | 下一步 | 状态 | 日期 |
|:--|:--|:--|:--|:--|:--|
| 某合作 | self | 匹配 | 行动 | proposed | 2026-10-20 |
`)
	write("DECISIONS.md", `# DECISIONS

## 决策日志

| ID | 日期 | 决策 | 依据 | 备选 | 后果 | 状态 |
|:--|:--|:--|:--|:--|:--|:--|
| D-01 | 2026-10-17 | 决策一 | 证据 | 备选 | 后果 | done |
`)
	write("WEEKLY_REVIEW.md", `# WEEKLY_REVIEW

## 2026-10-18

- 本周推进了主线。
`)
	return dir
}

func TestLoadParsesActiveAndTables(t *testing.T) {
	dir := writeTempOS(t)
	s, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.ActiveTask == nil || s.ActiveTask.ID != "T-01" {
		t.Fatalf("应识别 active=T-01, got %+v", s.ActiveTask)
	}
	if len(s.Tasks) != 2 {
		t.Fatalf("应解析 2 个任务, got %d", len(s.Tasks))
	}
	if len(s.Experiments) != 1 || s.Experiments[0].Tag != "p0" {
		t.Fatalf("实验解析错误: %+v", s.Experiments)
	}
	if len(s.Opportunities) != 1 {
		t.Fatalf("机会解析错误: %+v", s.Opportunities)
	}
	if len(s.Decisions) != 1 || s.Decisions[0].ID != "D-01" {
		t.Fatalf("决策解析错误: %+v", s.Decisions)
	}
	dash := s.Dashboard()
	if !strings.Contains(dash, "T-01") || !strings.Contains(dash, "p0") {
		t.Fatalf("仪表盘应含任务与实验:\n%s", dash)
	}
}

func TestSeparatorRowsSkipped(t *testing.T) {
	// 直接喂含分隔行的表，确认 tableRows 不把 |:--| 当数据、且数据行保留。
	text := "| ID | 任务 |\n|:--|:--|\n| T-01 | x |\n"
	rows := tableRows(text)
	hasT01 := false
	for _, r := range rows {
		for _, c := range r {
			if sepRe.MatchString(strings.TrimSpace(c)) {
				t.Fatalf("分隔行单元格 %q 泄漏进结果 %+v", c, rows)
			}
		}
		if r[0] == "T-01" {
			hasT01 = true
		}
	}
	if !hasT01 {
		t.Fatalf("数据行 T-01 应保留: %+v", rows)
	}
}
