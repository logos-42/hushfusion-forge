// Package owners 是并行构建所用的、可被机器校验的文件归属名册。
//
// 按委派协议：只有当名册 (a) 互不重叠且 (b) 覆盖完整，并且这一事实是由
// 测试而不是由人对着两个列表核对出来的时候，并行线才是安全的。
// owners_test.go 就是那道检查；它还会改动名册，以证明这道检查确实
// 能变红。
package owners

import (
	"path/filepath"
	"strings"
)

// Stage 把一组路径分配给唯一的负责人。
//
// 路径可以是精确文件，也可以是目录前缀（结尾带 "/"）—— 父线保留根目录、
// config 和 testdata；每条并行线拥有整个包，因此即使它们新增文件，
// 两条线也不会碰撞。
type Stage struct {
	ID    string
	Name  string
	Owner string
	Paths []string
}

// Stages 是冻结的名册。新增一个阶段或移动一条路径属于契约变更：
// 必须在同一个 commit 里同步反映到 CONTRACT.md。
var Stages = []Stage{
	{
		ID:    "root",
		Name:  "frozen shared skeleton (go.mod, config, roster, contracts, golden data, acceptance gates)",
		Owner: "parent",
		Paths: []string{
			"go.mod",
			"LICENSE",
			"README.md",
			"README.en.md",
			"PLAN.md",
			"CONTRACT.md",
			".gitignore",
			"internal/config/",
			"internal/owners/",
			"testdata/",
			"scripts/",
			"knowledge/",
		},
	},
	{
		ID:    "wiki",
		Name:  "wiki-first knowledge system (docs/wiki, manifests, platform config, wiki-lint CI)",
		Owner: "parent",
		Paths: []string{
			"docs/",
			"manifests/",
			"AGENTS.md",
			"CLAUDE.md",
			".cursorrules",
			".windsurfrules",
			".claude/",
			".github/",
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
		// internal/world/ 是同一轮的所有权: 世界协议(docs/world-protocol.md)只是把
		// rlenv 的语义搬到进程边界之外, 它和 rlenv、CLI 接线是同一段工作。
		// 新增路径 = 合同变更, 已在 CONTRACT.md §2 同步(2026-09-27)。
		Paths: []string{"internal/rlenv/", "cmd/", "internal/world/"},
	},
	{
		ID:    "G",
		Name:  "Python auxiliary layer: reference/oracle, analysis, schema parity",
		Owner: "agent-G",
		Paths: []string{"python/"},
	},
	{
		ID:    "design",
		Name:  "internal design gate layer: ProjectionPhysics closed forms + six gates + upstream anchors",
		Owner: "design",
		Paths: []string{"internal/design/"},
	},
}

// Overlaps 返回冲突的配对：完全相同的路径、重复的前缀，或一条线的前缀
// 包含另一条线的。返回空表示名册有效。
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

// Owner 返回拥有某个仓库相对路径的阶段，无人认领时返回 ""。
// 精确文件匹配优先于目录前缀。
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

// Covered 返回被某个阶段认领的那些文件的子集。
func Covered(files []string, stages []Stage) []string {
	var out []string
	for _, f := range files {
		if Owner(f, stages) != "" {
			out = append(out, f)
		}
	}
	return out
}

// Uncovered 返回没有任何阶段认领的文件。
func Uncovered(files []string, stages []Stage) []string {
	var out []string
	for _, f := range files {
		if Owner(f, stages) == "" {
			out = append(out, f)
		}
	}
	return out
}

// IsTracked 报告某个仓库相对路径是否应当参与覆盖检查
// （生成产物以及 VCS / 缓存会被忽略）。
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
