package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestSafePathWorkspaceAndGlobal(t *testing.T) {
	ws := t.TempDir()
	gd := t.TempDir()
	s := NewSession(Config{Root: ws, GlobalRoot: gd}, nil, NewRegistry(), "")

	got, err := s.safePath("notes/a.md")
	if err != nil || got != filepath.Join(ws, "notes/a.md") {
		t.Fatalf("workspace 相对路径 = %q, err=%v", got, err)
	}

	got, err = s.safePath(filepath.Join(gd, "memory.json"))
	if err != nil || got != filepath.Join(gd, "memory.json") {
		t.Fatalf("全局目录绝对路径 = %q, err=%v", got, err)
	}

	outside := filepath.Join(t.TempDir(), "secret.txt")
	if _, err := s.safePath(outside); err == nil {
		t.Fatalf("workspace/global 之外的路径应被拒绝: %s", outside)
	}
}

func TestAgentPathsTool(t *testing.T) {
	ws := t.TempDir()
	gd := t.TempDir()
	s := NewSession(Config{Root: ws, GlobalRoot: gd}, nil, NewRegistry(), "")
	memPath := filepath.Join(gd, "memory.json")
	ms, err := NewMemStoreFile(memPath)
	if err != nil {
		t.Fatal(err)
	}
	s.Memory = ms
	if err := s.RegisterBuiltinTools(nil); err != nil {
		t.Fatal(err)
	}
	out, err := s.ExecuteTool(context.Background(), "agent_paths", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"workspace=" + ws, "global_dir=" + gd, "memory_file=" + memPath} {
		if !strings.Contains(out, want) {
			t.Fatalf("agent_paths 缺 %q:\n%s", want, out)
		}
	}
}
