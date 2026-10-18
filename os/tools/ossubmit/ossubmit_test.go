// ossubmit 单测：翻转手术 / 勾验计数 / 假 kernel 全链 / 真 bridge 冒烟。
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/logos-42/hushfusion-forge/os/tools/oscore"
)

const tasksFixture = `# TASKS
### T-01｜示例主任务 — 状态: active
- 验收标准：os/acceptance/t01.md
## 候选池（按 P 排序）
| ID | 任务 | I | E | R | C | P | 状态 | 依赖/阻塞 |
|:--|:--|:-:|:-:|:-:|:-:|:-:|:--|:--|
| T-01 | 主任务 | 5 | 5 | 4 | 3 | 33 | active | — |
| T-02 | 候选二 | 3 | 3 | 5 | 1 | 45 | proposed | — |
`

// TestFlipTaskState：翻转只动目标行/详情节，其它字节逐字保留。
func TestFlipTaskState(t *testing.T) {
	out, err := flipTaskState(tasksFixture, "T-01", "active", "done")
	if err != nil {
		t.Fatalf("flip: %v", err)
	}
	if strings.Contains(out, "状态: active") || strings.Contains(out, "| active |") {
		t.Fatalf("还有残留 active:\n%s", out)
	}
	if !strings.Contains(out, "### T-01｜示例主任务 — 状态: done") {
		t.Fatalf("详情节没翻:\n%s", out)
	}
	if !strings.Contains(out, "| 33 | done | — |") {
		t.Fatalf("表格行没翻:\n%s", out)
	}
	// 未涉及的行必须逐字保留
	if !strings.Contains(out, "| T-02 | 候选二 | 3 | 3 | 5 | 1 | 45 | proposed | — |") {
		t.Fatalf("T-02 行被误改:\n%s", out)
	}
	// from 不匹配 ⇒ 必须拒绝（不能无中生有）
	if _, err := flipTaskState(tasksFixture, "T-01", "blocked", "done"); err == nil {
		t.Fatal("from 不匹配必须报错")
	}
	// 任务不存在 ⇒ 拒绝
	if _, err := flipTaskState(tasksFixture, "T-99", "active", "done"); err == nil {
		t.Fatal("不存在的任务必须报错")
	}
}

// TestCountChecklist：checkbox 统计。
func TestCountChecklist(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.md")
	os.WriteFile(p, []byte("- [x] 一\n- [ ] 二\n- [X] 三\n"), 0o644)
	checked, total, err := countChecklist(p)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if checked != 2 || total != 3 {
		t.Fatalf("checked=%d total=%d（应 2/3）", checked, total)
	}
}

// fakeKernel 是假 kernelClient：ok/fail 可配，记录调用。
type fakeKernel struct {
	fail    bool
	channel []string
	life    int
}

func (f *fakeKernel) ChannelSubmit(channelID, taskID, marker string, sleepMs int) (map[string]any, error) {
	f.channel = append(f.channel, channelID+"/"+taskID)
	if f.fail {
		return map[string]any{"ok": false, "detail": "rejected"}, nil
	}
	return map[string]any{"ok": true, "via": "fake-kernel"}, nil
}

func (f *fakeKernel) LifecycleSubmit(request, ports map[string]any) (map[string]any, error) {
	f.life++
	return map[string]any{"ok": true, "via": "fake-lifecycle"}, nil
}

// newFixtureRoot 建一个最小 os/ 结构（TASKS + acceptance + runs 证据）。
func newFixtureRoot(t *testing.T, acceptance string) string {
	t.Helper()
	root := t.TempDir()
	mustMkWrite(t, filepath.Join(root, "os", "TASKS.md"), tasksFixture)
	mustMkWrite(t, filepath.Join(root, "os", "acceptance", "t01-demo.md"), acceptance)
	mustMkWrite(t, filepath.Join(root, "runs", "demo", "results.json"), "{}")
	return root
}

func mustMkWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestSubmitSettleRoundTrip：假 kernel 走完整环 —— submit 回执 → 全勾 →
// settle 翻状态 → 回执 settled → TASKS.md 变 done。
func TestSubmitSettleRoundTrip(t *testing.T) {
	root := newFixtureRoot(t, "# 验收\n- [x] 一\n- [x] 二\n")
	k := &fakeKernel{}
	tasksText := tasksFixture

	receipt, err := submitTask(k, root, "T-01", tasksText)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if receipt.Status != oscore.StatusSubmitted {
		t.Fatalf("status=%s（应 submitted）", receipt.Status)
	}

	settled, err := settleTask(k, root, "T-01", []string{"runs/demo"})
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	if settled.Status != oscore.StatusSettled {
		t.Fatalf("status=%s（应 settled）", settled.Status)
	}
	if settled.Settle == nil || settled.Settle.Checked != 2 || settled.Settle.Total != 2 {
		t.Fatalf("settle 记录不对: %+v", settled.Settle)
	}
	// TASKS.md 已回写
	raw, _ := os.ReadFile(filepath.Join(root, "os", "TASKS.md"))
	if strings.Contains(string(raw), "| active |") ||
		!strings.Contains(string(raw), "状态: done") {
		t.Fatalf("TASKS.md 没回写 done:\n%s", raw)
	}
	// settled 回执可再加载（K2 证据）
	loaded, err := oscore.LoadReceipt(receiptPath(root, "T-01"))
	if err != nil || loaded.Status != oscore.StatusSettled {
		t.Fatalf("settled 回执回读失败: %v %+v", err, loaded)
	}
}

// TestSettleRefusesUnchecked：验收没全勾必须拒绝翻状态（done 不许自称）。
func TestSettleRefusesUnchecked(t *testing.T) {
	root := newFixtureRoot(t, "# 验收\n- [x] 一\n- [ ] 二\n")
	k := &fakeKernel{}
	if _, err := submitTask(k, root, "T-01", tasksFixture); err != nil {
		t.Fatal(err)
	}
	if _, err := settleTask(k, root, "T-01", []string{"runs/demo"}); err == nil {
		t.Fatal("未全勾必须拒绝 settle")
	}
	raw, _ := os.ReadFile(filepath.Join(root, "os", "TASKS.md"))
	if !strings.Contains(string(raw), "| active |") {
		t.Fatal("拒绝后 TASKS.md 不许被动")
	}
}

// TestSettleRefusesMissingEvidence：证据路径不存在必须拒绝。
func TestSettleRefusesMissingEvidence(t *testing.T) {
	root := newFixtureRoot(t, "# 验收\n- [x] 一\n")
	k := &fakeKernel{}
	if _, err := submitTask(k, root, "T-01", tasksFixture); err != nil {
		t.Fatal(err)
	}
	if _, err := settleTask(k, root, "T-01", []string{"runs/ghost"}); err == nil {
		t.Fatal("不存在的证据路径必须拒绝")
	}
}

// TestSubmitFailClosed：kernel 拒绝 ⇒ 回执 failed 且返回错误。
func TestSubmitFailClosed(t *testing.T) {
	root := newFixtureRoot(t, "# 验收\n- [x] 一\n")
	k := &fakeKernel{fail: true}
	if _, err := submitTask(k, root, "T-01", tasksFixture); err == nil {
		t.Fatal("kernel ok=false 必须让 submit 失败")
	}
	r, err := oscore.LoadReceipt(receiptPath(root, "T-01"))
	if err != nil || r.Status != oscore.StatusFailed {
		t.Fatalf("failed 回执应如实落盘: %v %+v", err, r)
	}
}
