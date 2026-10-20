package kernelbridge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 起 bridge 子进程 (真 Node, 真 @bolloon/bolloon-kernel)。前置: node + bridge/node_modules 已装。
func newTestClient(t *testing.T) *Client {
	t.Helper()
	root, err := filepath.Abs("../../")
	if err != nil {
		t.Fatalf("abs root: %v", err)
	}
	server := filepath.Join(root, "bridge", "server.mjs")
	if _, err := os.Stat(server); err != nil {
		t.Skipf("bridge server 不存在 (%s), 跳过: %v", server, err)
	}
	c, err := create("node", server)
	if err != nil {
		t.Fatalf("create bridge: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func boolOf(m map[string]any, key string) bool {
	v, _ := m[key].(bool)
	return v
}

func strOf(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

// G24a: channel 串行 —— 同 channel 两个任务 (先 sleep 后 0), 完成的顺序必须是
// 提交顺序 (B 不可能在 A 完成之前返回)。这是 ChannelActor mailbox 的机制本体,
// 不是 bridge 自造的排队。
func TestChannelSubmitSerialFIFO(t *testing.T) {
	c := newTestClient(t)
	a, err := c.ChannelSubmit("g-serial", "t-a", "A", 120)
	if err != nil {
		t.Fatalf("submit A: %v", err)
	}
	b, err := c.ChannelSubmit("g-serial", "t-b", "B", 0)
	if err != nil {
		t.Fatalf("submit B: %v", err)
	}
	if !boolOf(a, "ok") {
		t.Fatalf("A 应 ok, got %v", a)
	}
	if !boolOf(b, "ok") {
		t.Fatalf("B 应 ok, got %v", b)
	}
	// B 的 startedAt 必须 >= A 的 finishedAt (串行: B 等 A 完才开跑)
	aStart, _ := a["startedAt"].(float64)
	aFinish, _ := a["finishedAt"].(float64)
	bStart, _ := b["startedAt"].(float64)
	if bStart < aFinish {
		t.Fatalf("串行性破坏: B started=%v < A finished=%v", bStart, aFinish)
	}
	if aStart > aFinish {
		t.Fatalf("A 时序自相矛盾: started=%v > finished=%v", aStart, aFinish)
	}
}

// G24b: 不同 channel 并发 —— 同一个 client (同 stdio 管道) 上, g1 的 sleep
// 不应阻塞 g2 (kernel ChannelActor 的 mailbox 按 channel 隔离)。rpc 的写锁只
// 串行写字节, 读由后台循环按 id 分发 —— 两个并发请求都能拿到自己的响应。
func TestChannelIsolationBetweenChannels(t *testing.T) {
	c := newTestClient(t)
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
	select {
	case d := <-fastDone:
		if d < 0 {
			t.Fatalf("fast channel submit 失败")
		}
		if d > 150*time.Millisecond {
			t.Fatalf("fast channel 被 slow channel 拖住: %v (期望 <150ms, slow 在另一 channel)", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("fast channel submit 超时 (被跨 channel 阻塞)")
	}
	select {
	case d := <-slowDone:
		if d < 0 {
			t.Fatalf("slow channel submit 失败")
		}
		if d < 200*time.Millisecond {
			t.Fatalf("slow 完成过快 (sleep 没生效?): %v", d)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("slow channel submit 超时")
	}
}

// G24c: lifecycle 端口未注入 ⇒ fail-closed (ok:false, 不假装成功)。
func TestLifecyclePortMissingFailsClosed(t *testing.T) {
	c := newTestClient(t)
	out, err := c.LifecycleSubmit(map[string]any{
		"op": "record-step", "origin": "forge-test", "runId": "r1",
	}, nil) // ports 不注入
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

// G24d: lifecycle 端口注入后必须真走端口 (命名端口 `recordStep.echo` 由 bridge 解析成函数)。
func TestLifecyclePortInjected(t *testing.T) {
	c := newTestClient(t)
	out, err := c.LifecycleSubmit(map[string]any{
		"op": "record-step", "origin": "forge-test", "runId": "r9",
	}, map[string]any{
		"recordStep": "echo", // 命名端口: bridge 解析为 recordStep.echo
	})
	if err != nil {
		t.Fatalf("lifecycle submit: %v", err)
	}
	if !boolOf(out, "ok") {
		t.Fatalf("注入端口应 ok, got %v", out)
	}
	if strOf(out, "via") != "kernel-run-lifecycle" {
		t.Fatalf("via 应标 kernel-run-lifecycle, got %v", out)
	}
	// 未知名端口必须 fail-closed (诚实边界: 端口必须显式命名)
	unknown, err := c.LifecycleSubmit(map[string]any{
		"op": "record-step", "origin": "forge-test", "runId": "r9",
	}, map[string]any{
		"recordStep": "does-not-exist",
	})
	if err == nil && boolOf(unknown, "ok") {
		t.Fatalf("未知名端口必须拒绝, got %v", unknown)
	}
}

// G24e: transport 未注入 ⇒ fail-closed; 注入 ⇒ ok。
func TestTransportPort(t *testing.T) {
	c := newTestClient(t)
	missing, err := c.TransportSubmit(map[string]any{
		"op": "send", "origin": "forge-test", "peerId": "p1", "kind": "m", "payload": "hi",
	}, nil)
	if err != nil {
		t.Fatalf("transport submit: %v", err)
	}
	if boolOf(missing, "ok") {
		t.Fatalf("未注入 transport 必须 fail-closed, got %v", missing)
	}
	injected, err := c.TransportSubmit(map[string]any{
		"op": "send", "origin": "forge-test", "peerId": "p1", "kind": "m", "payload": "hi",
	}, map[string]any{
		"send": "echo",
	})
	if err != nil {
		t.Fatalf("transport injected: %v", err)
	}
	if !boolOf(injected, "ok") {
		t.Fatalf("注入 transport 应 ok, got %v", injected)
	}
}

// G24f: model.acquire 未注入连接端口 ⇒ 内核抛错 (K6 acquire 只读, 需要 openConnection)。
func TestModelAcquireRequiresConnectionPort(t *testing.T) {
	c := newTestClient(t)
	out, err := c.ModelAcquire(map[string]any{
		"provider": "openai", "model": "gpt-4o",
	}, nil)
	if err == nil {
		// 内核把"需要端口"表达成抛错 (ModelRuntime 构造要求 openConnection) —— bridge 转译成 error
		if out != nil && boolOf(out, "ok") {
			t.Fatalf("未注入连接端口不应 ok, got %v", out)
		}
		t.Skipf("bridge 返回非 error 形状: %v (若内核版本允许构造空端口则跳过)", out)
	}
	if !strings.Contains(err.Error(), "openConnection") {
		t.Fatalf("错误应提及 openConnection, got: %v", err)
	}
}

// G24h: bolloon.exec 把一次 bolloon CLI 调用放进内核 ChannelActor 里执行
// （kernel 作为执行核心），exec.bolloon 端口捕获 stdout。BOLLOON_BIN 指向假
// bolloon 以隔离外部进程。需 node + bridge/server.mjs。
func TestBolloonExecThroughKernel(t *testing.T) {
	fake := writeFakeBolloon(t)
	t.Setenv("BOLLOON_BIN", fake)
	c := newTestClient(t)
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
	stdout, _ := res["stdout"].(string)
	if !strings.Contains(stdout, "did:test") {
		t.Fatalf("exec.bolloon 应捕获 bolloon 的 stdout, 实际 %q", stdout)
	}
}

// G24i: bolloon.exec 端口未注入 ⇒ fail-closed（对齐 bolloon kernel 的"未注入即拒"
// 边界测试）。调用方必须显式传 ports:{exec:'bolloon'}，否则内核拒、不假装成功。
func TestBolloonExecPortMissingFailsClosed(t *testing.T) {
	c := newTestClient(t)
	var out map[string]any
	err := c.call("bolloon.exec", map[string]any{
		"args": []string{"network", "status"},
		// 不传 ports —— 内核必须拒
	}, &out)
	if err == nil {
		t.Fatalf("未注入 exec 端口必须 fail-closed, 却 ok: %v", out)
	}
}

// writeFakeBolloon 写一个有执行权限的假 bolloon 脚本：对 `network status` 吐
// 结构化信封，其余命令吐通用 OK 信封。供 TestBolloonExecThroughKernel 隔离外部进程。
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

// G24g: 响应都是合法 JSON (ndjson 逐行可解析) —— 防止 server 输出被截断/串行。
func TestResponsesAreValidJSONLines(t *testing.T) {
	c := newTestClient(t)
	for _, id := range []string{"j1", "j2"} {
		_, err := c.ChannelSubmit("g-json-"+id, "t-"+id, "m-"+id, 5)
		if err != nil {
			t.Fatalf("submit %s: %v", id, err)
		}
	}
	// ChannelPeek 的 state 是嵌套 JSON —— 必须能整体反序列化
	peek, err := c.ChannelPeek("g-json-j1")
	if err != nil {
		t.Fatalf("peek: %v", err)
	}
	if _, err := json.Marshal(peek); err != nil {
		t.Fatalf("peek 结果不可再序列化: %v", err)
	}
}
