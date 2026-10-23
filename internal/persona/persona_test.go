package persona

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverOrdersByPriority(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "MEMORY.md", "长期事实")
	write(t, dir, "AGENTS.md", "协作规约")
	write(t, dir, "SOUL.md", "我是谁")
	docs, err := Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 3 {
		t.Fatalf("应发现 3 份, got %d", len(docs))
	}
	want := []string{"SOUL.md", "AGENTS.md", "MEMORY.md"}
	for i, w := range want {
		if docs[i].Name != w {
			t.Fatalf("pos %d = %s, want %s（优先级序）", i, docs[i].Name, w)
		}
	}
	if docs[0].Kind != KindPersona {
		t.Fatalf("SOUL.md 应为 persona, got %s", docs[0].Kind)
	}
}

func TestDiscoverEmptyDirIsNotError(t *testing.T) {
	docs, err := Discover(t.TempDir())
	if err != nil {
		t.Fatalf("空目录不应报错: %v", err)
	}
	if len(docs) != 0 {
		t.Fatalf("应无文档, got %d", len(docs))
	}
	if Compose(docs, 1000) != "" {
		t.Fatal("无文档应合成空块")
	}
}

func TestDiscoverSkipsDirectoryWithDocName(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "AGENTS.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	docs, err := Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 0 {
		t.Fatalf("同名目录不应被当成文档, got %v", docs)
	}
}

func TestComposeRespectsBudgetAndMarksDrops(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "SOUL.md", strings.Repeat("S", 100))
	write(t, dir, "AGENTS.md", strings.Repeat("A", 100))
	write(t, dir, "MEMORY.md", strings.Repeat("M", 100))
	docs, _ := Discover(dir)
	block := Compose(docs, 300)
	if runeLen(block) > 300 {
		t.Fatalf("应受预算约束: %d > 300", runeLen(block))
	}
	if !strings.Contains(block, "SOUL.md") {
		t.Fatal("最高优先级文档应保住")
	}
	if !strings.Contains(block, "未注入") {
		t.Fatal("被丢掉的低优先级文档应留痕")
	}
}

func TestComposeMarksReadTruncation(t *testing.T) {
	d := Doc{Name: "SOUL.md", Kind: KindPersona, Priority: 10, Content: "x", Bytes: maxDocBytes + 500, Truncated: true}
	block := Compose([]Doc{d}, 8000)
	if !strings.Contains(block, "已在读取时截断") {
		t.Fatalf("读取截断应留痕: %s", block)
	}
}

func TestBootstrapEndToEnd(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "SOUL.md", "人格描述")
	block, err := Bootstrap(dir, 4000)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(block, "# Agent Bootstrap") {
		t.Fatalf("块应以标题开头: %q", block[:40])
	}
	if !strings.Contains(block, "人格描述") {
		t.Fatal("块应含文档内容")
	}
}

func TestWithExtra(t *testing.T) {
	base := "# Agent Bootstrap\n\n## SOUL.md（persona）\nbase\n"
	got := WithExtra(base, "  运行时人格  ")
	if !strings.Contains(got, "运行时人格") {
		t.Fatal("应追加运行时人格")
	}
	if WithExtra(base, "   ") != base {
		t.Fatal("空白 extra 不应改变原块")
	}
	if got := WithExtra("", "只有运行时"); !strings.Contains(got, "只有运行时") {
		t.Fatal("空块也应能注入运行时人格")
	}
}

func TestDiscoverRejectsEmptyRoot(t *testing.T) {
	if _, err := Discover("  "); err == nil {
		t.Fatal("空 root 应被拒")
	}
}
