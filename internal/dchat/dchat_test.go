package dchat

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 假 Node 助手：只验证 Go 侧进程/协议接线，不连真实 P2P 网络。
func fakeNode(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "fake-node")
	script := `#!/bin/sh
while read line; do
  case "$line" in
    *status*) echo '{"ok":true,"data":{"channel":"hyperswarm","available":false}}' ;;
    *send*) echo '{"ok":true,"data":{"sent":true}}' ;;
    *) echo '{"ok":false,"error":"bad request"}' ;;
  esac
done
`
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestManagerStatusSend(t *testing.T) {
	t.Setenv("DCHAT_NODE_BIN", fakeNode(t))
	m, err := NewManager()
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	st, err := m.Status(context.Background(), "hyperswarm")
	if err != nil {
		t.Fatal(err)
	}
	if st["channel"] != "hyperswarm" {
		t.Fatalf("status = %+v", st)
	}
	sent, err := m.Send(context.Background(), "hyperswarm", "topic", "hello")
	if err != nil {
		t.Fatal(err)
	}
	if sent["sent"] != true {
		t.Fatalf("send = %+v", sent)
	}
	if _, err := m.Status(context.Background(), "nope"); err == nil {
		t.Fatal("未知频道应 fail-closed")
	}
}

func TestManagerFailsClosedOnBadResponse(t *testing.T) {
	t.Setenv("DCHAT_NODE_BIN", fakeNode(t))
	m, err := NewManager()
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	_, err = m.Receive(context.Background(), "hyperswarm", "topic", 0)
	if err == nil || !strings.Contains(err.Error(), "bad request") {
		t.Fatalf("应把 ok:false 转成错误, got %v", err)
	}
}
