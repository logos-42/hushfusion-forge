package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTermRunPersistentCwd(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash 不可用")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := NewSession(Config{Root: root, ShellTimeout: 10 * time.Second}, nil, NewRegistry(), "")
	if err := s.RegisterBuiltinTools(nil); err != nil {
		t.Fatal(err)
	}
	s.AllowWrites["term.run"] = true

	ctx := context.Background()
	if _, err := s.ExecuteTool(ctx, "term_run", map[string]any{"cmd": "cd sub"}); err != nil {
		t.Fatal(err)
	}
	out, err := s.ExecuteTool(ctx, "term_run", map[string]any{"cmd": "pwd"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, filepath.Join(root, "sub")) {
		t.Fatalf("cwd 未跨调用保持: %q", out)
	}

	if _, err := s.ExecuteTool(ctx, "term_reset", nil); err != nil {
		t.Fatal(err)
	}
	out, err = s.ExecuteTool(ctx, "term_run", map[string]any{"cmd": "pwd"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "/sub") {
		t.Fatalf("reset 后 cwd 应回到 workspace 根: %q", out)
	}
}

func TestTermRunRejectsSensitive(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash 不可用")
	}
	s := NewSession(Config{Root: t.TempDir()}, nil, NewRegistry(), "")
	if err := s.RegisterBuiltinTools(nil); err != nil {
		t.Fatal(err)
	}
	s.AllowWrites["term.run"] = true
	if _, err := s.ExecuteTool(context.Background(), "term_run", map[string]any{"cmd": "cat .env"}); err == nil {
		t.Fatal("term.run 读取敏感路径应被隐私围栏拒绝")
	}
}
