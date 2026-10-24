package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, root, rel string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScanCategorizesWorkspace(t *testing.T) {
	root := t.TempDir()
	write(t, root, "NORTH_STAR.md")
	write(t, root, "os/TASKS.md")
	write(t, root, "os/evidence/sync.json")
	write(t, root, "os/acceptance/t01.md")
	write(t, root, "os/kernel_receipts/T-01.json")
	write(t, root, "docs/design/loop.md")
	write(t, root, "python/aux/x.py")
	idx := Scan(root)
	got := map[string]int{}
	for _, c := range idx.Categories {
		got[c.Name] = len(c.Files)
	}
	for name, want := range map[string]int{
		"project": 2, "evidence": 1, "acceptance": 1, "receipts": 1, "design": 1, "python": 1,
	} {
		if got[name] != want {
			t.Fatalf("%s = %d, want %d（全部 %v）", name, got[name], want, got)
		}
	}
	out := Render(idx, 1)
	if out == "" {
		t.Fatal("Render 不应为空")
	}
}
