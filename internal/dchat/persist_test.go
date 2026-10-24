package dchat

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestManagerPersistentDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DCHAT_NODE_DIR", dir)
	t.Setenv("DCHAT_NODE_BIN", fakeNode(t))
	m, err := NewManager()
	if err != nil {
		t.Fatal(err)
	}
	if m.temp {
		t.Fatal("显式 DCHAT_NODE_DIR 不应按临时目录处理")
	}
	if _, err := m.Status(context.Background(), "hyperswarm"); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	// 持久目录不随 Close 删除：脚本/package.json 保留，node_modules 可复用。
	for _, f := range []string{"dchat-node.mjs", "package.json"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatalf("持久目录文件 %s 不应被删除: %v", f, err)
		}
	}
}

func TestNeedsDepInstall(t *testing.T) {
	if !needsDepInstall(errFromString("dchat: 助手返回非 JSON")) && needsDepInstall(errFromString("Cannot find package 'orbit-db'")) == false {
		t.Fatal("needsDepInstall 判定错误")
	}
	if needsDepInstall(nil) {
		t.Fatal("nil 不应触发安装")
	}
}

type strErr string

func (e strErr) Error() string { return string(e) }

func errFromString(s string) error { return strErr(s) }
