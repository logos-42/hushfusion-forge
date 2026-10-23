package kernel

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func boolOf(m map[string]any, key string) bool {
	v, _ := m[key].(bool)
	return v
}

func strOf(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

// G24a: channel 串行 —— 同 channel 两个任务 (先 sleep 后 0), 完成顺序必须是提交顺序。
// 原生内核: 同一 actor 一把执行锁, 同步提交天然 FIFO。
func TestChannelSubmitSerialFIFO(t *testing.T) {
	c := New()
	defer c.Close()
	a, err := c.ChannelSubmit("g-serial", "t-a", "A", 120)
	if err != nil {
		t.Fatalf("submit A: %v", err)
	}
	b, err := c.ChannelSubmit("g-serial", "t-b", "B", 0)
	if err != nil {
		t.Fatalf("submit B: %v", err)
	}
	if !boolOf(a, "ok") || !boolOf(b, "ok") {
		t.Fatalf("A/B 应 ok, got a=%v b=%v", a, b)
	}
	aStart, _ := a["startedAt"].(int64)
	aFinish, _ := a["finishedAt"].(int64)
	bStart, _ := b["startedAt"].(int64)
	if bStart < aFinish {
		t.Fatalf("串行性破坏: B started=%d < A finished=%d", bStart, aFinish)
	}
	if aStart > aFinish {
		t.Fatalf("A 时序自相矛盾: started=%d > finished=%d", aStart, aFinish)
	}
	// 回读: peek 应记住最后一次提交的 task/marker。
	pk, err := c.ChannelPeek("g-serial")
	if err != nil {
		t.Fatal(err)
	}
	if !boolOf(pk, "ok") {
		t.Fatalf("peek 应 ok: %v", pk)
	}
	state, _ := pk["state"].(map[string]any)
	last, _ := state["lastTask"].(map[string]any)
	if strOf(last, "taskId") != "t-b" || strOf(last, "marker") != "B" {
		t.Fatalf("peek 回读应记住最后一次提交: %+v", last)
	}
}

// G24b: 不同 channel 并发 —— 同一 client 上, g-slow 的 sleep 不阻塞 g-fast。
// 用相对顺序 (fast 早于 slow) 替代脆化墙钟阈值。
func TestChannelIsolationBetweenChannels(t *testing.T) {
	c := New()
	defer c.Close()
	fastDone := make(chan time.Duration, 1)
	slowDone := make(chan time.Duration, 1)
	go func() {
		start := time.Now()
		res, err := c.ChannelSubmit("g-slow", "t-2", "slow", 250)
		if err != nil {
			slowDone <- -1
			return
		}
		if !boolOf(res, "ok") {
			t.Errorf("slow submit 应 ok, got %v", res)
		}
		slowDone <- time.Since(start)
	}()
	time.Sleep(30 * time.Millisecond) // 确保慢任务先入队
	go func() {
		start := time.Now()
		res, err := c.ChannelSubmit("g-fast", "t-1", "fast", 0)
		if err != nil {
			fastDone <- -1
			return
		}
		if !boolOf(res, "ok") {
			t.Errorf("fast submit 应 ok, got %v", res)
		}
		fastDone <- time.Since(start)
	}()
	dFast := recvWithin(t, fastDone, 5*time.Second, "fast channel submit 超时")
	dSlow := recvWithin(t, slowDone, 5*time.Second, "slow channel submit 超时")
	if dFast >= dSlow {
		t.Fatalf("channel 隔离被破坏: fast=%v 不早于 slow=%v", dFast, dSlow)
	}
	if dSlow < 200*time.Millisecond {
		t.Fatalf("slow 完成过快 (sleep 没生效?): %v", dSlow)
	}
}

func recvWithin(t *testing.T, ch <-chan time.Duration, timeout time.Duration, msg string) time.Duration {
	t.Helper()
	select {
	case d := <-ch:
		if d < 0 {
			t.Fatalf("%s（失败）", msg)
		}
		return d
	case <-time.After(timeout):
		t.Fatalf("%s", msg)
		return -1
	}
}

// G24c: lifecycle 端口未注入 ⇒ fail-closed (ok:false, 不假装成功)。
func TestLifecyclePortMissingFailsClosed(t *testing.T) {
	c := New()
	out, err := c.LifecycleSubmit(
		map[string]any{"op": "record-step", "origin": "forge-test", "runId": "r1"}, nil)
	if err != nil {
		t.Fatalf("lifecycle submit: %v", err)
	}
	if boolOf(out, "ok") {
		t.Fatalf("未注入端口必须 fail-closed, got ok=true: %v", out)
	}
	if strOf(out, "via") != "kernel-run-lifecycle" {
		t.Fatalf("via 应标 kernel-run-lifecycle, got %v", out)
	}
}

// G24d: lifecycle 端口注入后必须真走端口 (命名端口 recordStep.echo)。
func TestLifecyclePortInjected(t *testing.T) {
	c := New()
	out, err := c.LifecycleSubmit(
		map[string]any{"op": "record-step", "origin": "forge-test", "runId": "r9"},
		map[string]any{"recordStep": "echo"})
	if err != nil {
		t.Fatalf("lifecycle submit: %v", err)
	}
	if !boolOf(out, "ok") {
		t.Fatalf("注入端口应 ok, got %v", out)
	}
	if strOf(out, "via") != "kernel-run-lifecycle" {
		t.Fatalf("via 应标 kernel-run-lifecycle, got %v", out)
	}
	// 未知名端口必须报错 (诚实边界: 端口必须显式命名)。
	_, err = c.LifecycleSubmit(
		map[string]any{"op": "record-step", "origin": "forge-test", "runId": "r9"},
		map[string]any{"recordStep": "does-not-exist"})
	if err == nil {
		t.Fatal("未知名端口必须报错")
	}
}

// G24e: transport 未注入 ⇒ fail-closed; 注入 ⇒ ok。
func TestTransportPort(t *testing.T) {
	c := New()
	missing, err := c.TransportSubmit(
		map[string]any{"op": "send", "origin": "forge-test", "peerId": "p1", "kind": "m", "payload": "hi"}, nil)
	if err != nil {
		t.Fatalf("transport submit: %v", err)
	}
	if boolOf(missing, "ok") {
		t.Fatalf("未注入 transport 必须 fail-closed, got %v", missing)
	}
	injected, err := c.TransportSubmit(
		map[string]any{"op": "send", "origin": "forge-test", "peerId": "p1", "kind": "m", "payload": "hi"},
		map[string]any{"send": "echo"})
	if err != nil {
		t.Fatalf("transport injected: %v", err)
	}
	if !boolOf(injected, "ok") {
		t.Fatalf("注入 transport 应 ok, got %v", injected)
	}
}

// G24f: model.acquire 未注入连接端口 ⇒ 报错 (构造期硬约束)。
func TestModelAcquireRequiresConnectionPort(t *testing.T) {
	c := New()
	_, err := c.ModelAcquire(map[string]any{"provider": "openai", "model": "gpt-4o"}, nil)
	if err == nil {
		t.Fatal("未注入 openConnection 端口应报错")
	}
	if !strings.Contains(err.Error(), "openConnection") {
		t.Fatalf("错误应提及 openConnection, got: %v", err)
	}
}

// model.acquire 注入 fake 连接端口 ⇒ 拿到租约, lease.call 恒 ok。
func TestModelAcquireFakeLease(t *testing.T) {
	c := New()
	out, err := c.ModelAcquire(map[string]any{"provider": "openai", "model": "gpt-4o"},
		map[string]any{"openConnection": "fake"})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if !boolOf(out, "ok") || strOf(out, "via") != "kernel-model-runtime" {
		t.Fatalf("acquire 形状错: %v", out)
	}
	lease, _ := out["lease"].(map[string]any)
	call, _ := lease["call"].(func(map[string]any) map[string]any)
	if call == nil {
		t.Fatal("lease 应带 call")
	}
	if !boolOf(call(map[string]any{"x": 1}), "ok") {
		t.Fatal("fake lease.call 应 ok")
	}
	// 缺 provider/model ⇒ acquire 拒 (缺了不许猜)。
	if _, err := c.ModelAcquire(map[string]any{"provider": "openai"}, map[string]any{"openConnection": "fake"}); err == nil {
		t.Fatal("缺 model 应被拒")
	}
}

// writeFakeBolloon 写一个有执行权限的假 bolloon 脚本, 供 TestBolloonExecThroughKernel
// 隔离外部真实进程。
func writeFakeBolloon(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "bolloon")
	script := `#!/usr/bin/env bash
if [ "$1" = "network" ] && [ "$2" = "status" ]; then
  echo '{"ok":true,"code":"OK","message":"x","data":{"did":"did:test"}}'
  exit 0
fi
echo '{"ok":true,"code":"OK","message":"ok","data":{}}'
exit 0
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("写假 bolloon: %v", err)
	}
	return path
}

// G24h: bolloon.exec 把一次 bolloon CLI 调用放进内核 ChannelActor 里执行,
// exec.bolloon 端口捕获 stdout。
func TestBolloonExecThroughKernel(t *testing.T) {
	fake := writeFakeBolloon(t)
	t.Setenv("BOLLOON_BIN", fake)
	c := New()
	out, err := c.BolloonExec("network", "status")
	if err != nil {
		t.Fatalf("BolloonExec: %v", err)
	}
	if strOf(out, "via") != "kernel-channel-actor" {
		t.Fatalf("via 应标 kernel-channel-actor, got %v", out)
	}
	res, _ := out["result"].(map[string]any)
	if res == nil {
		t.Fatalf("result 缺失: %v", out)
	}
	if stdout := strOf(res, "stdout"); !strings.Contains(stdout, "did:test") {
		t.Fatalf("exec.bolloon 应捕获 bolloon 的 stdout, 实际 %q", stdout)
	}
}

// G24i: bolloon.exec 端口未注入 ⇒ fail-closed (对齐原内核"未注入即拒")。
func TestBolloonExecPortMissingFailsClosed(t *testing.T) {
	c := New()
	_, err := c.ExecThroughChannel([]string{"network", "status"}, nil)
	if err == nil {
		t.Fatal("未注入 exec 端口必须 fail-closed")
	}
}

// G24g: 响应都是合法 JSON —— peek 的 state 可整体再序列化。
func TestResponsesAreValidJSON(t *testing.T) {
	c := New()
	for _, id := range []string{"j1", "j2"} {
		if _, err := c.ChannelSubmit("g-json-"+id, "t-"+id, "m-"+id, 5); err != nil {
			t.Fatalf("submit %s: %v", id, err)
		}
	}
	pk, err := c.ChannelPeek("g-json-j1")
	if err != nil {
		t.Fatalf("peek: %v", err)
	}
	if _, err := json.Marshal(pk); err != nil {
		t.Fatalf("peek 结果不可再序列化: %v", err)
	}
}
