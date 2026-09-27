package owners

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// repoRoot 从测试的工作目录向上查找，直到找到 go.mod。
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.mod not found above %s", dir)
		}
		dir = parent
	}
}

// walkTracked 列出仓库里每一个非生成文件，路径为仓库相对路径。
func walkTracked(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == "." {
				return nil
			}
			if !IsTracked(rel + "/") {
				return filepath.SkipDir
			}
			return nil
		}
		if IsTracked(rel) {
			files = append(files, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	return files
}

// TestRosterHasNoOverlaps 是冻结门：两个阶段绝不能认领同一条路径或
// 互相重叠的前缀。
func TestRosterHasNoOverlaps(t *testing.T) {
	if got := Overlaps(Stages); len(got) != 0 {
		t.Fatalf("ownership roster has %d overlap(s): %v", len(got), got)
	}
}

// TestRosterCoversEveryTrackedFile 是这道门的完整性那一半：仓库里
// 不允许有任何文件不属于任何人。
func TestRosterCoversEveryTrackedFile(t *testing.T) {
	root := repoRoot(t)
	files := walkTracked(t, root)
	if len(files) == 0 {
		t.Fatalf("walk found no files under %s", root)
	}
	if missing := Uncovered(files, Stages); len(missing) != 0 {
		t.Fatalf("%d file(s) belong to no stage: %v", len(missing), missing)
	}
	t.Logf("roster covers %d files across %d stages", len(files), len(Stages))
}

// TestOverlapGateCanGoRed 证明重叠检查不是空转的绿：一份被改动过、把
// 两个阶段指向同一个包的名册，必须被拒绝。
// （一道不可能失败的门根本不算门。）
func TestOverlapGateCanGoRed(t *testing.T) {
	mutated := []Stage{
		{ID: "A", Paths: []string{"internal/physics/"}},
		{ID: "B", Paths: []string{"internal/physics/"}},
	}
	if got := Overlaps(mutated); len(got) == 0 {
		t.Fatal("mutated roster with a duplicated package was NOT reported as overlapping")
	}

	// 同一个文件被认领两次
	mutated = []Stage{
		{ID: "A", Paths: []string{"internal/physics/magnet.go"}},
		{ID: "B", Paths: []string{"internal/physics/magnet.go"}},
	}
	if got := Overlaps(mutated); len(got) == 0 {
		t.Fatal("mutated roster claiming the same file twice was NOT reported")
	}

	// 嵌套前缀：一个阶段拥有另一个阶段路径的父目录
	mutated = []Stage{
		{ID: "A", Paths: []string{"internal/"}},
		{ID: "B", Paths: []string{"internal/physics/"}},
	}
	if got := Overlaps(mutated); len(got) == 0 {
		t.Fatal("nested prefixes were NOT reported as overlapping")
	}
}

// TestOwnerResolvesExactAndPrefix 检查解析器的优先级规则。
func TestOwnerResolvesExactAndPrefix(t *testing.T) {
	stages := []Stage{
		{ID: "root", Paths: []string{"go.mod", "internal/config/"}},
		{ID: "A", Paths: []string{"internal/physics/"}},
	}
	cases := map[string]string{
		"go.mod":                      "root",
		"internal/config/config.go":   "root",
		"internal/physics/metrics.go": "A",
		"internal/elsewhere/x.go":     "",
	}
	for rel, want := range cases {
		if got := Owner(rel, stages); got != want {
			t.Errorf("Owner(%q) = %q, want %q", rel, got, want)
		}
	}
}

// TestIsTrackedSkipsForeignCheckoutsButNotSources 盯住跳过规则的两侧。
//
// 只有前半条被测试的跳过列表, 会连同真文件一起被吃掉 —— 那时名册门仍然是绿的,
// 但它已经不再证明任何事。
func TestIsTrackedSkipsForeignCheckoutsButNotSources(t *testing.T) {
	for _, rel := range []string{
		".kilo/worktrees/immense-drop/internal/world/world.go",
		".kilocode/cache/x.json",
		".git/config",
		"runs/phase0/registry.jsonl",
	} {
		if IsTracked(rel) {
			t.Errorf("%s 属于别的工具/生成物, 名册门不该看见它", rel)
		}
	}
	for _, rel := range []string{
		"internal/world/world.go", "cmd/forge/main.go", "scripts/verify.sh",
		"docs/world-protocol.md", "testdata/world_trace_golden.jsonl",
	} {
		if !IsTracked(rel) {
			t.Errorf("%s 是本仓文件, 名册门必须看得见它(跳过列表吃掉了真文件)", rel)
		}
	}
}
