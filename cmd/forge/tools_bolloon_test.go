package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/logos-42/hushfusion-forge/internal/agent"
)

// BOLLOON 工具应真实调用二进制并按信封返回（假二进制隔离网络）。
func TestBolloonToolsExecute(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bolloon")
	script := `#!/usr/bin/env bash
case "$1 $2" in
  "network status")
    echo '{"ok":true,"code":"OK","message":"status","data":{"did":"did:test","name":"forge","p2pOnline":true,"gatewayJoin":{"peerId":"p1"}}}'
    exit 0;;
  "task board")
    echo '{"ok":true,"code":"OK","data":{"count":1,"entries":[{"announcementId":"ann-1","capability":"fusion"}]}}'
    exit 0;;
  "p2p --json")
    echo '{"ok":true,"code":"OK","peerId":"12D3KooTest","multiaddrs":["/ip4/127.0.0.1/tcp/1"]}'
    exit 0;;
  *)
    echo '{"ok":true,"code":"OK","data":{"sent":true}}'
    exit 0;;
esac
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BOLLOON_BIN", bin)

	s := agent.NewSession(agent.DefaultConfig(), nil, agent.NewRegistry(), "")
	registerBolloonTools(s, dir)

	st, err := s.ExecuteTool(context.Background(), "bolloon.status", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"did:test", "p2pOnline", "p1"} {
		if !strings.Contains(st, want) {
			t.Fatalf("status 缺 %q: %s", want, st)
		}
	}

	board, err := s.ExecuteTool(context.Background(), "bolloon.board", map[string]any{"capability": "fusion"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(board, "ann-1") {
		t.Fatalf("board = %s", board)
	}

	p2p, err := s.ExecuteTool(context.Background(), "bolloon.p2p", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p2p, "12D3KooTest") {
		t.Fatalf("p2p = %s", p2p)
	}
}
