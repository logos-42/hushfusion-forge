// Package report: render the run artifacts a human (and the next design round)
// actually reads.
//
// FROZEN INTERFACE (v0.1) — owner: stage E.
//
// The report is written in Chinese with English technical terms preserved, and
// it must contain a dedicated 诚实边界 (honest-limits) section: what the model
// does not represent, what would falsify the result, and which claims are
// measured vs assumed. A report that only reports wins is not finished.
package report

import (
	"github.com/logos-42/hushfusion-forge/internal/experiment"
	"github.com/logos-42/hushfusion-forge/internal/knowledge"
)

// RenderMarkdown renders the full report.
//
// Required sections, in order:
//
//	# Forge <tag> 运行报告            (headline: machine vs human baseline, and
//	                                  on which terms it won/lost)
//	## 1. 设置                        (spec, solver, budget, seeds, methods, commit)
//	## 2. 人工基线 vs 机器最优          (term-by-term table, both directions)
//	## 3. 方法对比 (等预算)             (设计次数 / 最优性能 / 收敛速度 / 泛化)
//	## 4. 设计谱系                    (top branch improvements, if any)
//	## 5. 知识库 rules                (top rules + path)
//	## 6. 诚实边界                    (what v0.1 does NOT model; what would
//	                                  falsify; which numbers are measured)
//	## 7. 下一步 (Phase 1)            (interfaces already in place)
//
// rulePath is the path of the rules markdown file, referenced from §5.
func RenderMarkdown(rep *experiment.Report, rules []knowledge.Rule, rulePath string) string {
	panic("TODO(stage E): implement markdown report rendering")
}

// Write writes the rendered markdown to path.
func Write(path, md string) error { panic("TODO(stage E): implement report writing") }
