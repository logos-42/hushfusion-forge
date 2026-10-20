package harness

import (
	"math"
	"math/rand"
)

// Policy 是 harness 的动作决策接口。观测是 obs() 渲染的向量，返回动作下标。
type Policy interface {
	Name() string
	Act(obs []float64) int
}

// RandomPolicy 均匀随机（闭环的基线，用来证明自调整确实带来改善）。
type RandomPolicy struct {
	actionDim int
	rng       *rand.Rand
}

func (p *RandomPolicy) Name() string      { return "random" }
func (p *RandomPolicy) Act(_ []float64) int { return p.rng.Intn(p.actionDim) }

// HeuristicPolicy 是规则层策略：每个动作学一个线性价值 w_a·obs，ε-贪心选最大者。
//
// 它用 store 里的 (obs, action, delta) 以 SGD 拟合 —— 这是确定性的、纯 Go 的、
// 可解释的"自我调整"，并且**状态相关**（不同观测选不同动作），不会退化成常值动作。
// 它和 OML 层都做 obs→action：规则层是简单的线性策略（永远可用、不需 torch），
// OML 层是 fancy 的持续学习器（Python）。两者互为补充，OML 不可用时规则层兜底。
type HeuristicPolicy struct {
	actionDim int
	obsDim    int
	W         [][]float64 // [actionDim][obsDim] 每动作的线性价值权重
	lr        float64
	epsilon   float64
	rng       *rand.Rand
}

func (p *HeuristicPolicy) Name() string { return "heuristic" }

// Fit 在记录上做若干遍 SGD，更新每个动作的线性价值权重。
func (p *HeuristicPolicy) Fit(records []StepRecord) {
	if p.W == nil {
		p.W = make([][]float64, p.actionDim)
		for a := range p.W {
			p.W[a] = make([]float64, p.obsDim)
		}
	}
	for pass := 0; pass < 4; pass++ {
		for _, r := range records {
			if r.Action < 0 || r.Action >= p.actionDim || len(r.Obs) != p.obsDim {
				continue
			}
			pred := dot(p.W[r.Action], r.Obs)
			err := pred - r.Delta
			w := p.W[r.Action]
			for i := 0; i < p.obsDim; i++ {
				w[i] -= p.lr * err * r.Obs[i]
			}
		}
	}
}

func (p *HeuristicPolicy) Act(obs []float64) int {
	if p.rng.Float64() < p.epsilon {
		return p.rng.Intn(p.actionDim)
	}
	best, bestV := 0, math.Inf(-1)
	for a := 0; a < p.actionDim; a++ {
		v := dot(p.W[a], obs)
		if v > bestV || (v == bestV && p.rng.Float64() < 0.2) {
			best, bestV = a, v
		}
	}
	return best
}

// OMLPolicy 持续学习层策略：用训练好的学习器从观测预测动作，ε-贪心兜底。
//
// 若学习器不可用（torch/hibs_lnn 缺失，或尚未 Fit），退化为随机 —— 规则层仍在工作。
type OMLPolicy struct {
	actionDim int
	learner   ContinualLearner
	epsilon   float64
	rng       *rand.Rand
}

func (p *OMLPolicy) Name() string { return "oml" }

func (p *OMLPolicy) Act(obs []float64) int {
	if p.rng.Float64() < p.epsilon {
		return p.rng.Intn(p.actionDim)
	}
	a, err := p.learner.Act(obs)
	if err != nil || a < 0 || a >= p.actionDim {
		return p.rng.Intn(p.actionDim)
	}
	return a
}

// dot 计算两个等长向量的内积。
func dot(a, b []float64) float64 {
	var s float64
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}
