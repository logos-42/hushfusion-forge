package oskernel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 造一个最小 os/ 目录供内核系统调用测试。
func writeOSDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	w := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w("TASKS.md", `# TASKS
## 候选池（按 P 排序）
| ID | 任务 | I | E | R | C | P | 状态 | 依赖/阻塞 |
|:--|:--|:-:|:-:|:-:|:-:|:-:|:--|:--|
| T-01 | 主线 | 5 | 5 | 4 | 3 | **33** | active | — |
| T-08 | a | 4 | 3 | 4 | 1 | **48** | proposed | — |
`)
	w("EXPERIMENTS.md", `# EXPERIMENTS
## 已有实验 → 状态映射
| 实验（tag） | 态1 | 态2 | 态3 | 态4 | 态5 |
|:--|:--|:--|:--|:--|:--|
| `+"`p0`"+` | q | plan | ok | ok | done |
`)
	w("OPPORTUNITIES.md", `# OPPORTUNITIES
## 机会池
| 机会 | 来源 | 研究方向匹配 | 下一步 | 状态 | 日期 |
|:--|:--|:--|:--|:--|:--|
`)
	w("DECISIONS.md", `# DECISIONS
## 决策日志
| ID | 日期 | 决策 | 依据 | 备选 | 后果 | 状态 |
|:--|:--|:--|:--|:--|:--|:--|
| D-01 | 2026-10-17 | a | b | c | d | done |
`)
	return dir
}

func TestKernelProposeOrderedAndReturnsP(t *testing.T) {
	dir := writeOSDir(t)
	k := New(dir)
	res, err := k.Syscall("tasks.propose", map[string]any{
		"id": "T-09", "title": "新任务", "i": 3, "e": 3, "r": 3, "c": 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.Data.(int) != 13 {
		t.Fatalf("应返回 P=13, got %+v", res)
	}
	// 写前守卫：重复 ID 必须拒。
	if _, err := k.Syscall("tasks.propose", map[string]any{"id": "T-01", "title": "x", "i": 1, "e": 1, "r": 1, "c": 1}); err == nil {
		t.Fatal("重复 ID 应被拒（T7）")
	}
	// 评分越界必须拒。
	if _, err := k.Syscall("tasks.propose", map[string]any{"id": "T-10", "title": "x", "i": 9, "e": 1, "r": 1, "c": 1}); err == nil {
		t.Fatal("评分越界应被拒（T5）")
	}
}

func TestKernelDecideAndExperimentsE1(t *testing.T) {
	dir := writeOSDir(t)
	k := New(dir)

	res, err := k.Syscall("decisions.add", map[string]any{"text": "新决策", "basis": "证据"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Data.(string) != "D-02" {
		t.Fatalf("应分配 D-02, got %v", res.Data)
	}

	if _, err := k.Syscall("experiments.new", map[string]any{"tag": "exp_x", "q": "问题"}); err != nil {
		t.Fatal(err)
	}
	// 重复 tag 必须拒（证据映射不许重复）。
	if _, err := k.Syscall("experiments.new", map[string]any{"tag": "exp_x", "q": "再来"}); err == nil {
		t.Fatal("重复实验 tag 应被拒")
	}
	// E1：跳过态1 直接写态3 必须拒。
	if _, err := k.Syscall("experiments.advance", map[string]any{"tag": "exp_x", "state": 3, "text": "x"}); err == nil {
		t.Fatal("跳态应被 E1 拒绝")
	}
	// 逐级：态2 → 态3 应成功。
	if _, err := k.Syscall("experiments.advance", map[string]any{"tag": "exp_x", "state": 2, "text": "方案"}); err != nil {
		t.Fatal(err)
	}
	if _, err := k.Syscall("experiments.advance", map[string]any{"tag": "exp_x", "state": 3, "text": "复现"}); err != nil {
		t.Fatal(err)
	}
}

func TestKernelUnknownSyscall(t *testing.T) {
	if _, err := New(".").Syscall("nope.nope", nil); err == nil {
		t.Fatal("未知系统调用必须报错")
	}
}

func TestInvariantsAuditDetectsTwoActive(t *testing.T) {
	dir := writeOSDir(t)
	// 再造一个 active，触发 T2。
	path := filepath.Join(dir, "TASKS.md")
	b, _ := os.ReadFile(path)
	bad := strings.Replace(string(b), "| T-08 | a | 4 | 3 | 4 | 1 | **48** | proposed | — |",
		"| T-08 | a | 4 | 3 | 4 | 1 | **48** | active | — |", 1)
	os.WriteFile(path, []byte(bad), 0o644)

	res, err := New(dir).Syscall("invariants.check", nil)
	if err != nil {
		t.Fatal(err)
	}
	m := res.Data.(map[string]any)
	if m["ok"].(bool) {
		t.Fatal("两个 active 应被判 T2 违规")
	}
}
