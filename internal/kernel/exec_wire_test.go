package kernel

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeFakeBolloonV2 写一个支持 sleep/envcheck 的假 bolloon，供沙箱/进程管理测试隔离。
func writeFakeBolloonV2(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bolloon")
	script := `#!/usr/bin/env bash
case "$1" in
  sleep)
    sleep "$2"
    echo '{"slept":true}'
    exit 0
    ;;
  envcheck)
    printf '{"canary":"%s","hasPath":"%s"}\n' "${SANDBOX_CANARY:-noleak}" "${PATH:+set}"
    exit 0
    ;;
  *)
    echo '{"ok":true,"code":"OK","message":"ok","data":{}}'
    exit 0
    ;;
esac
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("写假 bolloon: %v", err)
	}
	return path
}

// run 执行一次 exec 并取出内层 result（无错 + result 缺失都 fail）。
func run(t *testing.T, fn func() (map[string]any, error)) map[string]any {
	t.Helper()
	out, err := fn()
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	res, _ := out["result"].(map[string]any)
	if res == nil {
		t.Fatalf("result 缺失: %v", out)
	}
	return res
}

// 沙箱 env 白名单：未点名的不泄漏，点名的才透传。
func TestBolloonExecSandboxEnvAllowlist(t *testing.T) {
	t.Setenv("BOLLOON_BIN", writeFakeBolloonV2(t))
	t.Setenv("SANDBOX_CANARY", "leaked")
	c := New()

	res := run(t, func() (map[string]any, error) { return c.BolloonExec("envcheck") })
	if strings.Contains(strOf(res, "stdout"), "leaked") {
		t.Fatalf("未点名的父环境变量不应泄漏: %q", strOf(res, "stdout"))
	}
	if !strings.Contains(strOf(res, "stdout"), `"hasPath":"set"`) {
		t.Fatalf("默认白名单应含 PATH: %q", strOf(res, "stdout"))
	}

	t.Setenv("BOLLOON_EXEC_ENV_ALLOW", "PATH,SANDBOX_CANARY")
	res2 := run(t, func() (map[string]any, error) { return c.BolloonExec("envcheck") })
	if !strings.Contains(strOf(res2, "stdout"), "leaked") {
		t.Fatalf("点名后应放行: %q", strOf(res2, "stdout"))
	}
}

// 沙箱超时：到点强杀进程组，结果如实标记（不假装成功）。
func TestBolloonExecTimeoutFailsClosed(t *testing.T) {
	t.Setenv("BOLLOON_BIN", writeFakeBolloonV2(t))
	t.Setenv("BOLLOON_EXEC_TIMEOUT", "150ms")
	c := New()
	start := time.Now()
	res := run(t, func() (map[string]any, error) { return c.BolloonExec("sleep", "30") })
	if ok, _ := res["ok"].(bool); ok {
		t.Fatal("超时应 ok:false")
	}
	if !strings.Contains(strOf(res, "stderr"), "超时强杀") {
		t.Fatalf("stderr 应标记超时强杀: %q", strOf(res, "stderr"))
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("超时杀掉应很快, took %v", d)
	}
}

// ctx 取消：强杀进程组（不等自然结束）。
func TestBolloonExecContextCancel(t *testing.T) {
	t.Setenv("BOLLOON_BIN", writeFakeBolloonV2(t))
	c := New()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(120 * time.Millisecond); cancel() }()
	start := time.Now()
	res := run(t, func() (map[string]any, error) { return c.BolloonExecContext(ctx, "sleep", "30") })
	if ok, _ := res["ok"].(bool); ok {
		t.Fatal("ctx 取消后应 ok:false")
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("取消后应立即返回, took %v", d)
	}
}

// 并发闸：进程级上限，第二个 Client 的并发 exec 被 fail-closed 拒掉。
// （同一 Client 的 exec 已被 ChannelActor 串行化，所以用两个 Client 造真并发。）
func TestBolloonExecConcurrencyCap(t *testing.T) {
	t.Setenv("BOLLOON_BIN", writeFakeBolloonV2(t))
	t.Setenv("BOLLOON_EXEC_MAX_CONCURRENT", "1")
	c1, c2 := New(), New()

	done := make(chan struct{})
	go func() {
		_, _ = c1.BolloonExec("sleep", "0.6")
		close(done)
	}()
	time.Sleep(150 * time.Millisecond) // 让第一个真正跑起来

	res := run(t, func() (map[string]any, error) { return c2.BolloonExec("envcheck") })
	if ok, _ := res["ok"].(bool); ok {
		t.Fatal("并发满时应 ok:false（fail-closed）")
	}
	if !strings.Contains(strOf(res, "stderr"), "并发已达上限") {
		t.Fatalf("stderr 应说明并发上限: %q", strOf(res, "stderr"))
	}
	<-done
}

// 围栏 jail 根：BOLLOON_EXEC_ROOT 把 cwd 钉住（能在其中正常执行）。
func TestBolloonExecJailRoot(t *testing.T) {
	t.Setenv("BOLLOON_BIN", writeFakeBolloonV2(t))
	t.Setenv("BOLLOON_EXEC_ROOT", t.TempDir())
	c := New()
	res := run(t, func() (map[string]any, error) { return c.BolloonExec("noop") })
	if ok, _ := res["ok"].(bool); !ok {
		t.Fatalf("jail 根内应正常执行: %+v", res)
	}
}

// 未注入 exec 端口 ⇒ fail-closed（不变式保持）。
func TestBolloonExecPortMissingStillFailsClosed(t *testing.T) {
	c := New()
	if _, err := c.ExecThroughChannel([]string{"x"}, map[string]any{"recordStep": "echo"}); err == nil {
		t.Fatal("未注入 exec 端口必须 fail-closed")
	}
}
