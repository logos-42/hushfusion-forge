package sandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// haveBin 报告某可执行文件是否在 PATH（测试环境缺它时跳过而非假红）。
func haveBin(name string) bool {
	_, err := lookPath(name)
	return err == nil
}

func lookPath(name string) (string, error) {
	p := os.Getenv("PATH")
	if p == "" {
		return "", errors.New("no PATH")
	}
	for _, dir := range filepath.SplitList(p) {
		full := filepath.Join(dir, name)
		if st, err := os.Stat(full); err == nil && !st.IsDir() {
			return full, nil
		}
	}
	return "", errors.New("not found: " + name)
}

func TestEnvAllowlistAndInject(t *testing.T) {
	if !haveBin("sh") {
		t.Skip("缺 sh")
	}
	t.Setenv("SANDBOX_TEST_VAR", "from-host")
	res, err := Run(context.Background(), Policy{
		EnvAllow: []string{"SANDBOX_TEST_VAR", "NOT_SET_ANYWAY"},
		EnvSet:   map[string]string{"SANDBOX_INJECTED": "injected-value"},
	}, "sh", "-c", `echo "$SANDBOX_TEST_VAR|$SANDBOX_INJECTED|${HOME:-nohome}"`)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit=%d stderr=%s", res.ExitCode, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "from-host") {
		t.Fatalf("白名单变量应透传: %q", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "injected-value") {
		t.Fatalf("注入变量应可见: %q", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "nohome") {
		t.Fatalf("未列名的 HOME 不应透传: %q", res.Stdout)
	}
}

func TestEmptyEnvIsStrictest(t *testing.T) {
	if !haveBin("sh") {
		t.Skip("缺 sh")
	}
	// 探针法：父进程放一个只可能在"继承环境"时出现的变量。
	// （不能断言 PATH 为空——POSIX sh 在 PATH 未设时会自填默认值，
	//   那是 shell 的行为，不是沙箱泄了父环境。）
	t.Setenv("SANDBOX_LEAK_CANARY", "leaked")
	res, err := Run(context.Background(), Policy{}, "sh", "-c", `echo "[${SANDBOX_LEAK_CANARY:-noleak}]"`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Stdout, "[noleak]") {
		t.Fatalf("空白名单不应泄漏父环境变量: %q", res.Stdout)
	}
	// 反向对照：点名放行时就应看得到
	res2, err := Run(context.Background(), Policy{EnvAllow: []string{"SANDBOX_LEAK_CANARY"}},
		"sh", "-c", `echo "[${SANDBOX_LEAK_CANARY:-noleak}]"`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res2.Stdout, "[leaked]") {
		t.Fatalf("白名单点名后应放行: %q", res2.Stdout)
	}
}

func TestJailRootBecomesCwd(t *testing.T) {
	if !haveBin("sh") {
		t.Skip("缺 sh")
	}
	dir := t.TempDir()
	// 用"在 cwd 落一个标记文件"来验证 cwd 被钉在 jail 根 —— 比 pwd 可靠
	//（sh 的 pwd 可能打印逻辑路径 /var/... 而非物理路径 /private/var/...）。
	marker := ".jailmarker"
	res, err := Run(context.Background(), Policy{Root: dir}, "sh", "-c", "touch "+marker)
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit=%d stderr=%s", res.ExitCode, res.Stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, marker)); err != nil {
		t.Fatalf("标记文件应出现在 jail 根内: %v", err)
	}
	// 反向：jail 外不应出现
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), marker)); err == nil {
		t.Fatal("标记文件不应出现在 jail 根之外")
	}
}

func TestJailPathRejectsEscapes(t *testing.T) {
	root := t.TempDir()
	if _, err := JailPath(root, "sub/file.go"); err != nil {
		t.Fatalf("root 内相对路径应放行: %v", err)
	}
	for _, bad := range []string{"../etc/passwd", "../../..", "/etc/passwd", "a/../../b"} {
		if _, err := JailPath(root, bad); err == nil {
			t.Fatalf("逃逸路径 %q 应被拒", bad)
		}
	}
	// root 内的绝对路径应放行
	inside := filepath.Join(root, "x", "y")
	if _, err := JailPath(root, inside); err != nil {
		t.Fatalf("root 内绝对路径应放行: %v", err)
	}
	// 符号链接外跳应被拒
	if err := os.Symlink("/etc", filepath.Join(root, "etclink")); err == nil {
		if _, err := JailPath(root, "etclink/passwd"); err == nil {
			t.Fatal("符号链接逃逸应被拒")
		}
	}
}

func TestTimeoutKillsProcessGroup(t *testing.T) {
	if !haveBin("sleep") {
		t.Skip("缺 sleep")
	}
	start := time.Now()
	res, err := Run(context.Background(), Policy{Timeout: 120 * time.Millisecond, KillGrace: 50 * time.Millisecond},
		"sleep", "30")
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut || !res.Killed {
		t.Fatalf("超时应置 TimedOut+Killed: %+v", res)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("超时杀掉应很快, took %v", d)
	}
}

func TestTimeoutKillsOrphanedChildren(t *testing.T) {
	if !haveBin("sh") {
		t.Skip("缺 sh")
	}
	// 子进程再 fork 一个长 sleep：杀进程组必须把孙子也带走
	res, err := Run(context.Background(), Policy{Timeout: 150 * time.Millisecond, KillGrace: 50 * time.Millisecond},
		"sh", "-c", "sleep 30 & sleep 30")
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut {
		t.Fatalf("应超时: %+v", res)
	}
}

func TestContextCancelKills(t *testing.T) {
	if !haveBin("sleep") {
		t.Skip("缺 sleep")
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	res, err := Run(ctx, Policy{}, "sleep", "30")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Killed {
		t.Fatalf("ctx 取消应置 Killed: %+v", res)
	}
}

func TestBinaryNotFoundFailsClosed(t *testing.T) {
	_, err := Run(context.Background(), Policy{}, "definitelynotacommandxyz", "--help")
	if !errors.Is(err, ErrBinaryNotFound) {
		t.Fatalf("应返回 ErrBinaryNotFound, got %v", err)
	}
}

func TestRootNotDir(t *testing.T) {
	f := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), Policy{Root: f}, "true"); !errors.Is(err, ErrRootNotDir) {
		t.Fatalf("应返回 ErrRootNotDir, got %v", err)
	}
}

func TestNonZeroExitCode(t *testing.T) {
	if !haveBin("sh") {
		t.Skip("缺 sh")
	}
	res, err := Run(context.Background(), Policy{}, "sh", "-c", "exit 7")
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 7 {
		t.Fatalf("exit 应为 7, got %d", res.ExitCode)
	}
}

func TestEmptyCommandNameRejected(t *testing.T) {
	if _, err := Run(context.Background(), Policy{}, "  "); err == nil {
		t.Fatal("空命令名应被拒")
	}
}
