package owners

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// repoRoot walks up from the test's working directory until go.mod is found.
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

// walkTracked lists every non-generated file in the repository, repo-relative.
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

// TestRosterHasNoOverlaps is the freeze gate: two stages may never claim the same
// path or overlapping prefixes.
func TestRosterHasNoOverlaps(t *testing.T) {
	if got := Overlaps(Stages); len(got) != 0 {
		t.Fatalf("ownership roster has %d overlap(s): %v", len(got), got)
	}
}

// TestRosterCoversEveryTrackedFile is the exhaustiveness half of the gate: no
// file in the repository may belong to nobody.
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

// TestOverlapGateCanGoRed proves the overlap check is not vacuously green: a
// mutated roster that points two stages at the same package must be rejected.
// (A gate that cannot fail is not a gate.)
func TestOverlapGateCanGoRed(t *testing.T) {
	mutated := []Stage{
		{ID: "A", Paths: []string{"internal/physics/"}},
		{ID: "B", Paths: []string{"internal/physics/"}},
	}
	if got := Overlaps(mutated); len(got) == 0 {
		t.Fatal("mutated roster with a duplicated package was NOT reported as overlapping")
	}

	// same file claimed twice
	mutated = []Stage{
		{ID: "A", Paths: []string{"internal/physics/magnet.go"}},
		{ID: "B", Paths: []string{"internal/physics/magnet.go"}},
	}
	if got := Overlaps(mutated); len(got) == 0 {
		t.Fatal("mutated roster claiming the same file twice was NOT reported")
	}

	// nested prefixes: one stage owning a parent of another's path
	mutated = []Stage{
		{ID: "A", Paths: []string{"internal/"}},
		{ID: "B", Paths: []string{"internal/physics/"}},
	}
	if got := Overlaps(mutated); len(got) == 0 {
		t.Fatal("nested prefixes were NOT reported as overlapping")
	}
}

// TestOwnerResolvesExactAndPrefix checks the resolver's precedence rule.
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
