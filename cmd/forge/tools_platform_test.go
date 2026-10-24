package main

import (
	"context"
	"strings"
	"testing"

	"github.com/logos-42/hushfusion-forge/internal/agent"
)

// 平台工具应全部注册，且 env/workspace/dchat 只读工具能真实返回。
func TestPlatformToolsExecute(t *testing.T) {
	base := setupTempOS(t)
	s := agent.NewSession(agent.DefaultConfig(), nil, agent.NewRegistry(), "")
	registerProjectTools(s, base)

	out, err := s.ExecuteTool(context.Background(), "env.status", nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "super-secret") {
		t.Fatal("env.status 不应返回密钥明文")
	}

	tree, err := s.ExecuteTool(context.Background(), "workspace.tree", map[string]any{"per_category": 3})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"project", "design", "tools"} {
		if !strings.Contains(tree, want) {
			t.Fatalf("workspace.tree 缺分类 %q: %s", want, tree)
		}
	}

	chans, err := s.ExecuteTool(context.Background(), "dchat.channels", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"iroh", "hyperswarm", "orbitdb"} {
		if !strings.Contains(chans, want) {
			t.Fatalf("dchat.channels 缺 %q: %s", want, chans)
		}
	}
}

// safeRepoPath 必须阻止路径穿越。
func TestSafeRepoPathBlocksEscape(t *testing.T) {
	if _, err := safeRepoPath("../../../etc/passwd"); err == nil {
		t.Fatal("应拒绝越出 workspace 的路径")
	}
	if _, err := safeRepoPath("README.md"); err != nil {
		t.Fatalf("workspace 内路径应放行: %v", err)
	}
}
