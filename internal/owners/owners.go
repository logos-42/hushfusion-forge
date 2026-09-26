// Package owners is the machine-verifiable file-ownership roster for the
// parallel build.
//
// Per the delegation protocol: parallel lines are only safe when the roster is
// (a) non-overlapping and (b) exhaustive, and when that fact is checked by a
// test rather than by a human comparing two lists. owners_test.go is that check;
// it also mutates the roster to prove the check can actually go red.
package owners

import (
	"path/filepath"
	"strings"
)

// Stage assigns a set of paths to exactly one owner.
//
// Paths may be exact files or directory prefixes (trailing "/") — the parent
// keeps the root, config and testdata; each parallel stage owns whole packages,
// so no two stages can collide even when they add new files.
type Stage struct {
	ID    string
	Name  string
	Owner string
	Paths []string
}

// Stages is the frozen roster. Adding a stage or moving a path is a contract
// change: it must be reflected in CONTRACT.md in the same commit.
var Stages = []Stage{
	{
		ID:    "root",
		Name:  "frozen shared skeleton (go.mod, config, roster, contracts, golden data, verify script, docs)",
		Owner: "parent",
		Paths: []string{
			"go.mod",
			"LICENSE",
			"README.md",
			"PLAN.md",
			"CONTRACT.md",
			".gitignore",
			"internal/config/",
			"internal/owners/",
			"testdata/",
			"scripts/",
		},
	},
	{
		ID:    "A",
		Name:  "physics: magnetostatics, metrics, geometry (numerical core)",
		Owner: "agent-A",
		Paths: []string{"internal/physics/"},
	},
	{
		ID:    "B",
		Name:  "objective + human baselines",
		Owner: "agent-B",
		Paths: []string{"internal/objective/", "internal/baseline/"},
	},
	{
		ID:    "C",
		Name:  "design registry + experiment runner",
		Owner: "agent-C",
		Paths: []string{"internal/registry/", "internal/runner/"},
	},
	{
		ID:    "D",
		Name:  "search algorithms (random, lhs, evolution, evolution_warm)",
		Owner: "agent-D",
		Paths: []string{"internal/search/"},
	},
	{
		ID:    "E",
		Name:  "benchmark harness + knowledge mining + report rendering",
		Owner: "agent-E",
		Paths: []string{"internal/experiment/", "internal/knowledge/", "internal/report/"},
	},
	{
		ID:    "F",
		Name:  "RL environment + CLI wiring (wiring is a single-owner round, done last)",
		Owner: "agent-F",
		Paths: []string{"internal/rlenv/", "cmd/"},
	},
	{
		ID:    "G",
		Name:  "Python auxiliary layer: reference/oracle, analysis, schema parity",
		Owner: "agent-G",
		Paths: []string{"python/"},
	},
}

// Overlaps returns conflicting pairs: identical paths, duplicate prefixes, or
// one stage's prefix containing another's. Empty means the roster is valid.
func Overlaps(stages []Stage) [][2]string {
	var out [][2]string
	seen := map[string]string{}
	for _, s := range stages {
		for _, p := range s.Paths {
			if owner, dup := seen[p]; dup {
				out = append(out, [2]string{owner, s.ID + ":" + p})
				continue
			}
			seen[p] = s.ID
		}
	}
	for _, a := range stages {
		for _, b := range stages {
			if a.ID >= b.ID {
				continue
			}
			for _, pa := range a.Paths {
				for _, pb := range b.Paths {
					if !strings.HasSuffix(pa, "/") || !strings.HasSuffix(pb, "/") {
						continue
					}
					if strings.HasPrefix(pb, pa) || strings.HasPrefix(pa, pb) {
						out = append(out, [2]string{a.ID + ":" + pa, b.ID + ":" + pb})
					}
				}
			}
		}
	}
	return out
}

// Owner returns the stage owning a repo-relative path, or "" when uncovered.
// Exact file matches win over directory prefixes.
func Owner(rel string, stages []Stage) string {
	best := ""
	bestLen := -1
	for _, s := range stages {
		for _, p := range s.Paths {
			if !strings.HasSuffix(p, "/") {
				if p == rel {
					return s.ID
				}
				continue
			}
			if strings.HasPrefix(rel, p) && len(p) > bestLen {
				best = s.ID
				bestLen = len(p)
			}
		}
	}
	return best
}

// Covered returns the subset of files owned by some stage.
func Covered(files []string, stages []Stage) []string {
	var out []string
	for _, f := range files {
		if Owner(f, stages) != "" {
			out = append(out, f)
		}
	}
	return out
}

// Uncovered returns the files no stage claims.
func Uncovered(files []string, stages []Stage) []string {
	var out []string
	for _, f := range files {
		if Owner(f, stages) == "" {
			out = append(out, f)
		}
	}
	return out
}

// IsTracked reports whether a repo-relative path should participate in the
// coverage check at all (generated output and VCS/caches are ignored).
func IsTracked(rel string) bool {
	rel = filepath.ToSlash(rel)
	if rel == "" || strings.HasPrefix(rel, ".git/") {
		return false
	}
	for _, frag := range []string{
		"runs/", "__pycache__/", ".pytest_cache/", ".venv/", "node_modules/", ".DS_Store",
		".egg-info/", "go-build/",
	} {
		if strings.Contains(rel, frag) {
			return false
		}
	}
	if strings.HasSuffix(rel, ".pyc") || strings.HasSuffix(rel, ".log") {
		return false
	}
	return true
}
