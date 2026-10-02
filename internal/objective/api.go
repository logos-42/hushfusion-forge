// Package objective: 给机器的一个数字,
// 以及构成它的每一个原始 term。
//
// FROZEN INTERFACE (v0.1) — 负责人: stage B。
//
// 设计原则(重要): composite score 绝不单独存储。
// 每一次评估都记录原始物理 term 与每一条约束残差,
// 这样任何权重都能在事后重新推导, 审阅者也能直接问
// "机器是靠物理赢的, 还是只是买了个更便宜的磁体?"
// 而无须重跑任何东西。
//
//	score = + w_field  * log10(B_mid / B_ref)
//	        + w_mirror * log10(max(R, 0.2) / R_ref)
//	        + w_volume * V_good
//	        - w_ripple * ripple
//	        - w_cost   * (cost / cost_ref)
//	        - w_penalty * (约束违反量之和)
package objective

import (
	"fmt"
	"math"

	"github.com/logos-42/hushfusion-forge/internal/config"
	"github.com/logos-42/hushfusion-forge/internal/physics"
)

// mirror log term 的下限与 "not a mirror" 阈值。FROZEN: 它们属于
// 得分定义的一部分, Python reference 用的是同一组常量。
const (
	MirrorFloor = 0.2 // mirror log term 内部的下限, 保证它有限
	MirrorMin   = 1.1 // mirror ratio 低于此值时, "not_a_mirror" penalty 生效
)

// Terms / Weighted 中使用的 term key。FROZEN — 它们出现在 registry record
// 以及 Python reference 中。
const (
	TermField  = "field"
	TermMirror = "mirror"
	TermVolume = "volume"
	TermRipple = "ripple"
	TermCost   = "cost"
)

// Penalty key。FROZEN。
const (
	PenConductorField = "conductor_field"
	PenCoilSeparation = "coil_separation"
	PenNotAMirror     = "not_a_mirror"
	PenClearance      = "clearance" // 0.1.2 新增: 导体面到约束区域的净空 (可造性)
)

// EvalResult 是一次评估产出的全部内容。
type EvalResult struct {
	Score        float64            `json:"score"`
	Terms        map[string]float64 `json:"terms"`
	Weighted     map[string]float64 `json:"weighted"`
	Penalties    map[string]float64 `json:"penalties"`
	Feasible     bool               `json:"feasible"`
	Metrics      physics.Metrics    `json:"metrics"`
	Design       []float64          `json:"design"`
	DesignID     string             `json:"-"`
	ExperimentID int                `json:"-"`
}

// Evaluator 给设计打分。它持有 spec/grids/solver/cost reference,
// 好让调用方保持很薄。
//
// CONCURRENCY: Evaluator 可安全并发使用(search 层会从多个 goroutine
// 评估候选设计)。任何可变状态必须是 atomic 或
// 不可变的; 不要在没有 mutex 或 atomic 的情况下添加普通计数器。
type Evaluator struct {
	Spec    config.Spec
	Solver  physics.Solver
	CostRef float64
	Grids   physics.Grids
}

// NewEvaluator 构造一个 evaluator。costRef 是人类 baseline 的
// 欧姆成本, 因此 cost term 读到 1.0 == "与 reference design
// 一样贵"。调用方传 baseline.TextbookMirror(spec).Cost。
func NewEvaluator(spec config.Spec, solver physics.Solver, costRef float64, grids physics.Grids) *Evaluator {
	// 这里刻意不做任何默认。nil solver 或空 grid 集
	// 会静默产出一个全由 NaN 组成的得分, 而比较 NaN 的 search
	// 会烧掉整个 budget 却什么也学不到。前置条件改为大声报错。
	// 得分自身的除数与上限也一并检查: 它们之中任何一个
	// 为零都会把一个 term 变成 Inf/NaN。
	if solver == nil {
		panic("objective.NewEvaluator: nil solver (no default: pass the analytic or discrete solver explicitly)")
	}
	if !(costRef > 0) || math.IsInf(costRef, 0) {
		panic(fmt.Sprintf("objective.NewEvaluator: costRef must be finite and > 0, got %v", costRef))
	}
	for _, p := range []struct {
		name string
		v    float64
	}{
		{"spec.BRef", spec.BRef},
		{"spec.MirrorRef", spec.MirrorRef},
		{"spec.CoilFieldLimit", spec.CoilFieldLimit},
		{"spec.MinCoilSep", spec.MinCoilSep},
		{"spec.MinClearance", spec.MinClearance},
	} {
		if !(p.v > 0) || math.IsInf(p.v, 0) {
			panic(fmt.Sprintf("objective.NewEvaluator: %s must be finite and > 0, got %v", p.name, p.v))
		}
	}
	if spec.NCoils < 1 {
		panic(fmt.Sprintf("objective.NewEvaluator: spec.NCoils must be >= 1, got %d", spec.NCoils))
	}
	if len(grids.StackR) == 0 || len(grids.StackR) != len(grids.StackZ) {
		panic("objective.NewEvaluator: grids must be a non-empty stacked sample set (physics.BuildGrids) with len(StackR) == len(StackZ) > 0")
	}
	if grids.NAxis <= 0 || grids.NMid <= 0 || grids.NAxis+grids.NMid > len(grids.StackR) {
		panic(fmt.Sprintf("objective.NewEvaluator: inconsistent grids (NAxis=%d NMid=%d samples=%d)",
			grids.NAxis, grids.NMid, len(grids.StackR)))
	}
	return &Evaluator{Spec: spec, Solver: solver, CostRef: costRef, Grids: grids}
}

// Evaluate 给一个 design 向量打分。
//
//	terms[field]  = log10(max(B_mid, 1e-9) / B_ref)
//	terms[mirror] = log10(max(R, MirrorFloor) / MirrorRef)
//	terms[volume] = V_good
//	terms[ripple] = ripple
//	terms[cost]   = cost / costRef
//
//	penalties[conductor_field] = max(0, B_coil/CoilFieldLimit - 1)
//	penalties[coil_separation] = max(0, (MinCoilSep - min_gap)/MinCoilSep)
//	penalties[not_a_mirror]    = max(0, (MirrorMin - R)/MirrorMin)
//	penalties[clearance]       = max(0, (MinClearance - min_clearance)/MinClearance)   (0.1.2)
//
//	Feasible = 所有 penalties <= 0
//	Design   = 实际被评估的 canonical(z 排序、裁剪后)向量
func (e *Evaluator) Evaluate(x []float64) EvalResult {
	// 冻结签名没有 error 返回值, 而仓库里每个调用方
	// 构造的向量都恰好有 spec.NParams() 项(RandomDesign、mutation +
	// clip)。长度不对的向量是编程错误, 绝不是设计:
	// 给它补零或截断等于给一台没人提出过的机器打分,
	// 所以这里改为大声失败。在调用任何 physics 之前检查。
	if len(x) != e.Spec.NParams() {
		panic(fmt.Sprintf("objective.Evaluate: design vector has %d entries, spec wants %d", len(x), e.Spec.NParams()))
	}
	coils, err := physics.VectorToCoils(x, e.Spec)
	if err != nil {
		panic(fmt.Sprintf("objective.Evaluate: %v", err))
	}
	return e.evalFromMetrics(physics.MetricsFor(coils, e.Spec, e.Grids, e.Solver), physics.CoilsToVector(coils))
}

// Score 是只返回 composite score 的便利包装。
func (e *Evaluator) Score(x []float64) float64 { return e.Evaluate(x).Score }
