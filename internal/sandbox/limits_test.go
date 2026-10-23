package sandbox

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestRlimitsFlags(t *testing.T) {
	r := Rlimits{CPU: 5 * time.Second, AS: 64 << 20, NOFILE: 32, NPROC: 16}
	got := strings.Join(r.Flags(), " ")
	for _, want := range []string{"--cpu=5", "--as=67108864", "--nofile=32", "--nproc=16"} {
		if !strings.Contains(got, want) {
			t.Fatalf("缺 %q: %q", want, got)
		}
	}
	if !(Rlimits{}).IsZero() {
		t.Fatal("零值应 IsZero")
	}
	if (Rlimits{CPU: time.Second}).IsZero() {
		t.Fatal("设了 CPU 就不该 IsZero")
	}
}

// 要求 RLIMIT 但宿主没有 prlimit ⇒ 必须**拒绝启动**（fail-closed），
// 不能静默跑一个"没限制却报告说了限制"的进程。
func TestLimitsRequestedWithoutPrlimitFailsClosed(t *testing.T) {
	if _, err := exec.LookPath("prlimit"); err == nil {
		t.Skip("本机有 prlimit（Linux），跳过缺失路径")
	}
	_, err := Run(context.Background(), Policy{Limits: Rlimits{CPU: time.Second}}, "sh", "-c", "true")
	if err == nil {
		t.Fatal("缺 prlimit 时应拒绝启动")
	}
	if !errors.Is(err, ErrLimitsUnsupported) {
		t.Fatalf("应返回 ErrLimitsUnsupported, got %v", err)
	}
	if !strings.Contains(err.Error(), "prlimit") {
		t.Fatalf("错误应给出可操作提示: %v", err)
	}
}

func TestNetIsolateRequestedWithoutUnshareFailsClosed(t *testing.T) {
	if _, err := exec.LookPath("unshare"); err == nil {
		t.Skip("本机有 unshare（Linux），跳过缺失路径")
	}
	_, err := Run(context.Background(), Policy{NetIsolate: true}, "sh", "-c", "true")
	if err == nil {
		t.Fatal("缺 unshare 时应拒绝启动")
	}
	if !errors.Is(err, ErrNetIsolateUnsupported) {
		t.Fatalf("应返回 ErrNetIsolateUnsupported, got %v", err)
	}
}

// 不要求隔离时不应套任何 wrapper：Wrapped 为空是可核对的证据。
func TestNoWrapByDefault(t *testing.T) {
	if !haveBin("sh") {
		t.Skip("缺 sh")
	}
	res, err := Run(context.Background(), Policy{Root: t.TempDir()}, "sh", "-c", "true")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Wrapped) != 0 {
		t.Fatalf("默认不应套 wrapper, got %v", res.Wrapped)
	}
}

// 有 prlimit 时（Linux）应真的套上，并在结果里如实报告。
func TestLimitsAppliedWhenPrlimitExists(t *testing.T) {
	if _, err := exec.LookPath("prlimit"); err != nil {
		t.Skip("本机无 prlimit")
	}
	if !haveBin("sh") {
		t.Skip("缺 sh")
	}
	res, err := Run(context.Background(), Policy{Limits: Rlimits{CPU: 5 * time.Second}}, "sh", "-c", "true")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Wrapped) == 0 || !strings.Contains(strings.Join(res.Wrapped, ","), "rlimit:prlimit") {
		t.Fatalf("应报告已套 prlimit: %v", res.Wrapped)
	}
}
