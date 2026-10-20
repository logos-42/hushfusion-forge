package aios

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/logos-42/hushfusion-forge/internal/oskernel"
)

func writeOS(t *testing.T) string {
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
`)
	w("EXPERIMENTS.md", "# EXPERIMENTS\n## 已有实验 → 状态映射\n| 实验（tag） | 态1 | 态2 | 态3 | 态4 | 态5 |\n|:--|:--|:--|:--|:--|:--|\n| `p0` | q | plan | ok | ok | done |\n")
	w("OPPORTUNITIES.md", "# OPPORTUNITIES\n## 机会池\n| 机会 | 来源 | 研究方向匹配 | 下一步 | 状态 | 日期 |\n|:--|:--|:--|:--|:--|:--|\n")
	w("DECISIONS.md", "# DECISIONS\n## 决策日志\n| ID | 日期 | 决策 | 依据 | 备选 | 后果 | 状态 |\n|:--|:--|:--|:--|:--|:--|:--|\n| D-01 | 2026-10-17 | a | b | c | d | done |\n")
	return dir
}

func fakeAgent(t *testing.T, reply string) (*Agent, *int) {
	t.Helper()
	calls := 0
	a := NewAgent(oskernel.New(writeOS(t)), "test", "", "")
	a.ChatFunc = func(ctx context.Context, system, user string) (string, error) {
		calls++
		return reply, nil
	}
	return a, &calls
}

func TestParsePlanHandlesFenceAndNoise(t *testing.T) {
	raw := "好的，这是我的计划：\n```json\n[{\"syscall\":\"tasks.propose\",\"args\":{\"id\":\"T-09\",\"title\":\"x\",\"i\":3,\"e\":3,\"r\":3,\"c\":2}}]\n```\n以上。"
	steps, err := parsePlan(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) != 1 || steps[0].Syscall != "tasks.propose" {
		t.Fatalf("解析错误: %+v", steps)
	}
}

func TestParsePlanRejectsGarbage(t *testing.T) {
	if _, err := parsePlan("我觉得应该继续研究，没有步骤。"); err == nil {
		t.Fatal("非 JSON 计划必须 fail-closed 报错")
	}
}

func TestRunExecutesAllowedAndRejectsForbidden(t *testing.T) {
	reply := `[
	  {"syscall":"tasks.propose","args":{"id":"T-09","title":"AI 提的任务","i":3,"e":3,"r":3,"c":2}},
	  {"syscall":"decisions.add","args":{"text":"AI 提议的决策","basis":"证据X"}},
	  {"syscall":"tasks.mark_done","args":{"id":"T-01"}},
	  {"syscall":"tasks.propose","args":{"id":"bad","title":"x","i":9,"e":1,"r":1,"c":1}}
	]`
	a, calls := fakeAgent(t, reply)
	rep, err := a.Run(context.Background(), "推进主线", false)
	if err != nil {
		t.Fatal(err)
	}
	if *calls != 1 {
		t.Fatalf("应调用 AI 一次, got %d", *calls)
	}
	allowed, rejected := 0, 0
	for _, p := range rep.Plan {
		if p.Allowed {
			allowed++
		} else {
			rejected++
		}
	}
	if allowed != 2 || rejected != 2 {
		t.Fatalf("应 2 允许/2 拒绝, got %d/%d", allowed, rejected)
	}
	okN, rejN := 0, 0
	for _, r := range rep.Results {
		if r.OK {
			okN++
		} else if strings.HasPrefix(r.Err, "拒绝执行") {
			rejN++
		}
	}
	if okN != 2 || rejN != 2 {
		t.Fatalf("应 2 成功/2 拒绝, got %d/%d (%+v)", okN, rejN, rep.Results)
	}
}

func TestRunDryRunDoesNotWrite(t *testing.T) {
	reply := `[{"syscall":"tasks.propose","args":{"id":"T-09","title":"x","i":1,"e":1,"r":1,"c":1}}]`
	a, _ := fakeAgent(t, reply)
	before := readFile(t, filepath.Join(a.Kernel.Base, "TASKS.md"))
	if _, err := a.Run(context.Background(), "g", true); err != nil {
		t.Fatal(err)
	}
	after := readFile(t, filepath.Join(a.Kernel.Base, "TASKS.md"))
	if before != after {
		t.Fatal("dry-run 不应改动任何文件")
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
