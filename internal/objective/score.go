// objective 的代数部分, 从 api.go 拆出来, 以便
// 在 physics 核心不运行的情况下也能被检验(见 CONTRACT.md §4: stage B 的单元测试
// 不能被 stage A 的包阻塞)。
//
// Evaluate 是那层薄包装: 解码 design 向量, 跑一次
// physics.MetricsFor, 然后落到这里。决定那个数字的一切 ——
// 五个 term、它们的 weight、三条约束残差、feasibility 以及
// composite —— 都活在这一个函数里, 所以形成一个 score 的位置
// 恰好只有一处。
package objective

import (
	"math"

	"github.com/logos-42/hushfusion-forge/internal/physics"
)

// termOrder 和 penaltyOrder 是 composite score 求和时唯一允许的顺序。
// 得分由加权 term 与归一化违反量按这个确切顺序累加而成,
// 使得
//
//	score == Σ weighted - weights.penalty * Σ penalties
//
// 逐位成立, 而不是 "在舍入误差内"成立。registry 的意义就在于
// 事后可重新审计, 而 "得分与它自己的 term 对不上"
// 是硬失败, 不是容差问题。与 api.go 顶部的 score 公式
// 保持同步; 测试刻意把 key 写成字面量。
var (
	termOrder    = [...]string{TermField, TermMirror, TermVolume, TermRipple, TermCost}
	penaltyOrder = [...]string{PenConductorField, PenCoilSeparation, PenNotAMirror, PenClearance}
)

// evalFromMetrics 从某个设计已经算好的 field metrics,
// 以及它来源的 canonical design 向量, 构造这个设计的完整评估。
//
// 刻意不返回 error, 也不把 NaN 钳掉: physics 层算不出来的 metric
// 必须以 NaN 形式(大声地)到达调用方, 而不是
// 被静默替换成一个看似合理的有限数(那将是一个伪造的
// 得分)。这个函数确实保证的是: 一个*定义良好*的不可行
// 设计 —— 巨大的 conductor field、线圈叠在一起、完全没有 mirror ——
// 会得到有限得分: 那些下限项(MirrorFloor、1e-9)与 max(0, ·)
// 残差正是把这些情形留在有限一侧的东西。
func (e *Evaluator) evalFromMetrics(m physics.Metrics, design []float64) EvalResult {
	s := e.Spec
	w := s.Weights

	terms := map[string]float64{
		TermField:  math.Log10(math.Max(m.BMidT, 1e-9) / s.BRef),
		TermMirror: math.Log10(math.Max(m.MirrorRatio, MirrorFloor) / s.MirrorRef),
		TermVolume: m.VolumeGood,
		TermRipple: m.Ripple,
		TermCost:   m.CostProxy / e.CostRef,
	}
	weighted := map[string]float64{
		TermField:  w.Field * terms[TermField],
		TermMirror: w.Mirror * terms[TermMirror],
		TermVolume: w.Volume * terms[TermVolume],
		TermRipple: -w.Ripple * terms[TermRipple],
		TermCost:   -w.Cost * terms[TermCost],
	}
	penalties := map[string]float64{
		PenConductorField: math.Max(0, m.BCoilMaxT/s.CoilFieldLimit-1),
		PenCoilSeparation: math.Max(0, (s.MinCoilSep-m.MinCoilGapM)/s.MinCoilSep),
		PenNotAMirror:     math.Max(0, (MirrorMin-m.MirrorRatio)/MirrorMin),
		PenClearance:      math.Max(0, (s.MinClearance-m.MinClearanceM)/s.MinClearance),
	}

	score := 0.0
	for _, k := range termOrder {
		score += weighted[k]
	}
	violation := 0.0
	for _, k := range penaltyOrder {
		violation += penalties[k]
	}
	score -= w.Penalty * violation

	// Feasible 复刻 reference 的 all(v <= 0): 写成 !(v <= 0) 是为了让 NaN
	// 残差被算作 infeasible, 而不是以 "无违反"
	// 的名义溜过去。
	feasible := true
	for _, k := range penaltyOrder {
		if !(penalties[k] <= 0) {
			feasible = false
			break
		}
	}

	return EvalResult{
		Score:     score,
		Terms:     terms,
		Weighted:  weighted,
		Penalties: penalties,
		Feasible:  feasible,
		Metrics:   m,
		Design:    design,
	}
}
