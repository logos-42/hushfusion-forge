package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPythonRun(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 不可用")
	}
	s := NewSession(Config{Root: t.TempDir()}, nil, NewRegistry(), "")
	if err := s.RegisterBuiltinTools(nil); err != nil {
		t.Fatal(err)
	}
	s.AllowWrites["python.run"] = true
	out, err := s.ExecuteTool(context.Background(), "python_run", map[string]any{
		"code": "print(6 * 7)",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "42") {
		t.Fatalf("python 输出 = %q", out)
	}
}

func TestPythonRejectsPrivateKey(t *testing.T) {
	s := NewSession(Config{Root: t.TempDir()}, nil, NewRegistry(), "")
	if err := s.RegisterBuiltinTools(nil); err != nil {
		t.Fatal(err)
	}
	s.AllowWrites["python.run"] = true
	code := `print("""-----BEGIN OPENSSH PRIVATE KEY-----
abc
-----END OPENSSH PRIVATE KEY-----""")`
	if _, err := s.ExecuteTool(context.Background(), "python_run", map[string]any{"code": code}); err == nil {
		t.Fatal("含私钥的 Python 代码应被拒绝")
	}
}

func TestReadAndShellSensitiveFence(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("API_KEY=secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewSession(Config{Root: root}, nil, NewRegistry(), "")
	if err := s.RegisterBuiltinTools(nil); err != nil {
		t.Fatal(err)
	}
	s.AllowWrites["shell"] = true
	if _, err := s.ExecuteTool(context.Background(), "read", map[string]any{"path": ".env"}); err == nil {
		t.Fatal("read .env 应被隐私围栏拒绝")
	}
	if _, err := s.ExecuteTool(context.Background(), "shell", map[string]any{"cmd": "cat .env"}); err == nil {
		t.Fatal("shell cat .env 应被隐私围栏拒绝")
	}
}
