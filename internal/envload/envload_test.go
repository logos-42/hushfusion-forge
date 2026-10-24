package envload

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDoesNotOverwriteAndMasksSecrets(t *testing.T) {
	root := t.TempDir()
	content := "# comment\nFOO=from-file\nAPI_KEY=super-secret\nPLAIN=hello\n"
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FOO", "from-shell")
	res, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("FOO") != "from-shell" {
		t.Fatalf("不应覆盖已有 env: %q", os.Getenv("FOO"))
	}
	if os.Getenv("API_KEY") != "super-secret" {
		t.Fatalf("新 key 应加载: %q", os.Getenv("API_KEY"))
	}
	for _, e := range res.Entries {
		switch e.Key {
		case "FOO":
			if e.State != "exists" || e.Masked == "from-shell" {
				t.Fatalf("FOO 状态/脱敏错误: %+v", e)
			}
		case "API_KEY":
			if e.State != "loaded" || e.Masked != "***" {
				t.Fatalf("密钥必须脱敏: %+v", e)
			}
		}
	}
}

func TestLoadRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.env")
	if err := os.WriteFile(outside, []byte("EVIL=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, ".env")); err != nil {
		t.Skipf("symlink 不可用: %v", err)
	}
	if _, err := Load(root); err == nil {
		t.Fatal("应拒绝越出 workspace 的 .env")
	}
}
