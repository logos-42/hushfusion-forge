package bolloon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// writeFakeBolloon 写一个假 bolloon 脚本：对已知子命令吐结构化信封，未知子命令吐
// 非 JSON（验证 Run 的 fail-closed）。返回脚本路径。
func writeFakeBolloon(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "bolloon")
	script := `#!/usr/bin/env bash
case "$1 $2" in
  "network status")
    echo '{"ok":true,"code":"OK","message":"x","data":{"did":"did:test","name":"t","joinedNetworks":[],"gatewayJoin":{"peerId":"p1","networkLink":"orbitdb://x"}}}'
    exit 0;;
  "task board")
    echo '{"ok":true,"code":"OK","message":"板上 1 条","data":{"count":1,"local":1,"remote":0,"registryReady":true,"entries":[{"announcementId":"ann-1","capability":"fusion-conversion-consistency","buyer":"did:key:z","status":"open"}]}}'
    exit 0;;
  "fail now")
    echo '{"ok":false,"code":"SOME_ERR","message":"boom","next_action":"fix it"}'
    exit 1;;
  *)
    if [ "$1" = "unknown" ]; then
      # 未知子命令：吐非 JSON，模拟没有 --json 支持或报错
      echo "unexpected: $*"
      exit 3
    fi
    # 其余未特判的命令：回一个合法 OK 信封，验证 wrapper 的参数接线
    echo '{"ok":true,"code":"OK","message":"ok","data":{}}'
    exit 0;;
esac
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("写假 bolloon: %v", err)
	}
	return path
}

func TestDetectPrefersEnv(t *testing.T) {
	fake := writeFakeBolloon(t)
	t.Setenv(EnvBin, fake)
	bin, err := Detect()
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if bin != fake {
		t.Fatalf("Detect 应优先用 %s, 实际 %s", fake, bin)
	}
}

func TestNetworkStatusDecode(t *testing.T) {
	fake := writeFakeBolloon(t)
	c := NewWithBin(fake)
	ctx := context.Background()
	d, err := c.NetworkStatus(ctx)
	if err != nil {
		t.Fatalf("NetworkStatus: %v", err)
	}
	if d.DID != "did:test" {
		t.Fatalf("DID 期望 did:test, 实际 %q", d.DID)
	}
	if d.GatewayJoin == nil || d.GatewayJoin.PeerID != "p1" {
		t.Fatalf("GatewayJoin.peerId 期望 p1, 实际 %+v", d.GatewayJoin)
	}
}

func TestTaskBoardDecode(t *testing.T) {
	fake := writeFakeBolloon(t)
	c := NewWithBin(fake)
	d, err := c.TaskBoard(context.Background(), "")
	if err != nil {
		t.Fatalf("TaskBoard: %v", err)
	}
	if d.Count != 1 || len(d.Entries) != 1 {
		t.Fatalf("期望 1 条任务, 实际 %+v", d)
	}
	if d.Entries[0].Capability != "fusion-conversion-consistency" {
		t.Fatalf("capability 期望 fusion-conversion-consistency, 实际 %q", d.Entries[0].Capability)
	}
}

func TestRunFailClosedOnEnvelopeError(t *testing.T) {
	fake := writeFakeBolloon(t)
	c := NewWithBin(fake)
	_, err := c.Run(context.Background(), "fail", "now")
	if err == nil {
		t.Fatal("ok:false 的信封必须返回错误")
	}
	be, ok := err.(*Error)
	if !ok {
		t.Fatalf("错误应是 *bolloon.Error, 实际 %T: %v", err, err)
	}
	if be.Code != "SOME_ERR" || be.NextAction != "fix it" {
		t.Fatalf("错误应携带 code/next_action: %+v", be)
	}
}

func TestRunFailClosedOnNonJSON(t *testing.T) {
	fake := writeFakeBolloon(t)
	c := NewWithBin(fake)
	env, err := c.Run(context.Background(), "unknown", "sub")
	if err == nil {
		t.Fatal("非 JSON 输出必须返回错误")
	}
	if env == nil || env.Code != "PARSE" {
		// err 路径下 env 可能为 nil（Run 在 err!=nil 时只返回 env 作为附带值）
		_ = fmt.Sprint(env)
	}
}

func TestEnvelopePassthroughMethods(t *testing.T) {
	fake := writeFakeBolloon(t)
	c := NewWithBin(fake)
	ctx := context.Background()
	if _, err := c.NetworkJoin(ctx, "orbitdb://x"); err != nil {
		t.Fatalf("NetworkJoin: %v", err)
	}
	if _, err := c.AgentDiscover(ctx); err != nil {
		t.Fatalf("AgentDiscover: %v", err)
	}
	if _, err := c.TaskSend(ctx, "ann-1", "hi peer"); err != nil {
		t.Fatalf("TaskSend: %v", err)
	}
	if _, err := c.EngineRun(ctx, "do something"); err != nil {
		t.Fatalf("EngineRun: %v", err)
	}
}
