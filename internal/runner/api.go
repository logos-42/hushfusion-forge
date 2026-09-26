// Package runner: 给一个 design 评分、给它一个 id、把它追加进 registry。
//
// 冻结接口 (v0.1) — 负责人: stage C。
//
// 这一层把“某个优化器调用了一个函数”变成“一次带 lineage 的 experiment”。每个
// 搜索算法都经过这里, 这才使得 registry 的完整性来自结构本身, 而不是来自纪律。
package runner

import (
	"github.com/logos-42/hushfusion-forge/internal/objective"
	"github.com/logos-42/hushfusion-forge/internal/registry"
)

// Meta 是附加在一个被评分的 design 上的来源信息。
type Meta struct {
	Algorithm  string
	Seed       int
	Generation int
	EvalIndex  int
	Parent     string // 父 design_id (lineage 边), 根节点为 ""
	Note       string
}

// Scorer 是搜索层依赖的接缝。搜索算法接受一个 Scorer 而不是 *Runner, 这样它们的
// 单元测试可以注入一个确定的玩具 scorer, 与 physics 层无关地通过。
type Scorer interface {
	Score(x []float64, meta Meta) objective.EvalResult
}

// Runner 通过 Evaluator 评分, 并记录进 Registry。
type Runner struct {
	Reg *registry.Registry
	Ev  *objective.Evaluator
	Tag string
}

// New 构建一个 runner。
func New(reg *registry.Registry, ev *objective.Evaluator, tag string) *Runner {
	return &Runner{Reg: reg, Ev: ev, Tag: tag}
}

// Score 求值一个 design 并把它追加进 registry, 把 Meta.Algorithm/Tag/Parent 填进
// record, 并返回已填好 DesignID 与 ExperimentID 的 EvalResult (搜索层用 DesignID
// 构建 lineage 树)。
//
// 求值可能从多个 goroutine 被调用 (并行搜索); 记录必须经 registry 保持串行化。
func (r *Runner) Score(x []float64, meta Meta) objective.EvalResult {
	return r.recordResult(r.evaluate(x), meta)
}
