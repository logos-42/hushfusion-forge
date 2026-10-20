// Package harness: 本地自优化闭环（harness 层）。
//
// 它把"数学模型"（internal/mu 的交换子世界代数）跑成一批本地实验，把每条评估
// 落盘成数据（store），再用两层"自我调整"把数据变回更好的策略：
//
//	1. 规则层（确定性，纯 Go）：从 store 挖"哪个动作带来更高的 score 增量"，
//	   据此给启发式策略加偏置 —— 这是可复现、可量化的自调整。
//	2. 持续学习层（可选，Python 子进程复用 scripts/oml_continual_core.py）：
//	   把 (观测, 动作, 增量) 喂给 OML，训练一个 obs→动作的策略。
//	   torch/hibs_lnn 缺失时 fail-closed，回退到规则层。
//
// 设计纪律：harness 不重写物理/代数（internal/mu 是唯一的数学真源），也不造第二个
// 世界协议字节 —— 它只是用 mu 的纯函数把这些实验跑起来并记下来。
package harness

import (
	"fmt"
	"math/rand"

	"github.com/logos-42/hushfusion-forge/internal/config"
	"github.com/logos-42/hushfusion-forge/internal/mu"
)

// 默认值（可由 CLI flag 覆盖）。
const (
	DefaultEpisodes = 200
	DefaultBudget   = config.DefaultBudget // 24
	DefaultSeed     = 1
	// minSamplesOLM 是触发 OML 训练的最小样本数；样本不足时只跑规则层。
	minSamplesOLM = 64
	// epsilon 是 ε-贪心探索率（两层策略共用）。
	epsilon = 0.15
)

// HarnessSource 是 harness 写进 store 的来源标记（与 world 的 source 概念分离，
// 这里不进入 score，只用于记录归属）。
const HarnessSource = "mu_harness"

// SimState 是交换子世界的一条 episode 的可变状态。它只包 mu 的纯函数，不持有协议。
type SimState struct {
	v, v0  []float64
	mu     float64
	budget int
	used   int
	score  float64
	term   bool
	trunc  bool
}

// newSimState 用 seed 生一条有起伏的参照场 v0，v=v0 副本，μ=初值。
//
// 参照场必须有起伏（Q(v0)>0），否则 TD11 的桥 η=1−Q/Q0 无定义 —— 与 world 同一条纪律。
// NormFloat64 几乎必然给出有起伏的场，这里只在极端情况下重播种。
func newSimState(rng *rand.Rand, seed, budget int) *SimState {
	n := config.MuFieldCells
	for attempt := 0; ; attempt++ {
		r := rand.New(rand.NewSource(int64(seed) + int64(attempt)*1000003))
		v0 := make([]float64, n)
		for i := range v0 {
			v0[i] = r.NormFloat64()
		}
		if mu.FluctuationEnergy(v0, mu.AllCells(n)) > 0 {
			s := &SimState{
				v:      append([]float64(nil), v0...),
				v0:     v0,
				mu:     config.MuInitialMu,
				budget: budget,
			}
			s.score = mu.Score(s.v, s.v0, s.mu)
			return s
		}
	}
}

// step 施加第 k 个声明动作，返回 η（本次 update_mu 用的增益）与 score 增量。
func (s *SimState) step(k int, space mu.ActionSpace, regions []mu.Region, target float64) (eta, delta float64) {
	prev := s.score
	switch space.Kinds[k] {
	case mu.KindFlatten:
		region, ok := mu.RegionByKey(regions, space.Regions[k])
		if !ok {
			panic(fmt.Sprintf("harness: action %q names unknown region %q", space.Keys[k], space.Regions[k]))
		}
		s.v = mu.Flatten(s.v, region.Cells)
	case mu.KindUpdateMu:
		eta = mu.GainOf(s.v, s.v0, mu.AllCells(len(s.v)))
		s.mu = mu.MuStep(s.mu, eta)
	default:
		panic(fmt.Sprintf("harness: action %d has unknown kind %q", k, space.Kinds[k]))
	}
	s.used++
	s.score = mu.Score(s.v, s.v0, s.mu)
	delta = s.score - prev
	s.term = s.score >= target
	s.trunc = s.used >= s.budget
	return eta, delta
}

// Harness 把 mu 世界、store、策略与（可选）持续学习器编排成一个闭环。
type Harness struct {
	Regions []mu.Region
	Space   mu.ActionSpace
	Target  float64

	Store *Store

	random    *RandomPolicy
	heuristic *HeuristicPolicy
	oml       *OMLPolicy
	learner   ContinualLearner
	learnerOK bool

	policyName string
	useOML     bool
	seed       int
	runID      string

	lastLearnerErr error

	rng *rand.Rand
}

// Options 配置一个 Harness。
type Options struct {
	StorePath  string
	PolicyName string // random | heuristic | oml
	UseOML     bool
	OMLScript  string // 默认 scripts/harness_oml.py
	OMLModel   string // OML 模型落盘路径
	Seed       int
	RunID      string
}

// New 构建一个 Harness：声明世界、开 store、造策略。
func New(opts Options) (*Harness, error) {
	if opts.StorePath == "" {
		return nil, fmt.Errorf("harness: StorePath 为空")
	}
	regions := mu.DeclaredRegions(config.MuFieldCells)
	space := mu.DeclaredActionSpace(regions)
	target := mu.TargetScore(config.MuScoreWeightMu, config.MuScoreWeightGain, config.MuScoreWeightClosed)

	store, err := OpenStore(opts.StorePath)
	if err != nil {
		return nil, fmt.Errorf("harness: open store %s: %w", opts.StorePath, err)
	}

	seed := opts.Seed
	if seed == 0 {
		seed = DefaultSeed
	}
	rng := rand.New(rand.NewSource(int64(seed)))

	h := &Harness{
		Regions:    regions,
		Space:      space,
		Target:     target,
		Store:      store,
		policyName: opts.PolicyName,
		useOML:     opts.UseOML,
		seed:       seed,
		runID:      opts.RunID,
		rng:        rng,
	}
	if h.policyName == "" {
		h.policyName = "heuristic"
	}
	if h.runID == "" {
		h.runID = fmt.Sprintf("h%d", seed)
	}

	h.random = &RandomPolicy{actionDim: space.Dim(), rng: rng}
	h.heuristic = &HeuristicPolicy{actionDim: space.Dim(), obsDim: ObsDim(), lr: 0.05, epsilon: epsilon, rng: rng}
	// 预载已有 store 的规则（跨 run 积累）。
	if recs, err := store.Records(); err == nil {
		h.heuristic.Fit(recs)
	}

	if opts.UseOML {
		script := opts.OMLScript
		if script == "" {
			script = "scripts/harness_oml.py"
		}
		model := opts.OMLModel
		if model == "" {
			model = "runs/harness/oml_model.pt"
		}
		pl := &pythonLearner{script: script, model: model, obsDim: ObsDim(), actionDim: space.Dim()}
		h.learner = pl
		h.oml = &OMLPolicy{actionDim: space.Dim(), learner: pl, epsilon: epsilon, rng: rng}
	}

	return h, nil
}

// ObsDim 是观测维数 = 场格点数 + 5（mu, eta, window_margin, budget_remaining, depth）。
func ObsDim() int {
	return config.MuFieldCells + 5
}

// obs 渲染当前观测（与 world 的 obsV3 同构但只取 harness 需要的槽位，不进协议字节）。
func (h *Harness) obs(s *SimState) []float64 {
	n := len(s.v)
	out := make([]float64, 0, n+5)
	out = append(out, s.v...)
	eta := mu.GainOf(s.v, s.v0, mu.AllCells(n))
	out = append(out, s.mu, eta, mu.WindowMargin(s.mu))
	if s.budget > 0 {
		out = append(out, float64(s.budget-s.used)/float64(s.budget))
		out = append(out, float64(s.used)/float64(s.budget))
	} else {
		out = append(out, 0, 0)
	}
	return out
}

// activePolicy 返回当前应该使用的策略：oml > heuristic > random。
func (h *Harness) activePolicy() Policy {
	switch h.policyName {
	case "random":
		return h.random
	case "oml":
		if h.learner != nil && h.learnerOK {
			return h.oml
		}
		return h.heuristic // OML 不可用 → 回退规则层（fail-closed）
	default:
		return h.heuristic
	}
}

// Run 跑 episodes 条 episode（每条预算 budget 步），每条评估落盘，返回本轮摘要。
//
// 它不在此自我调整 —— 调整由 SelfAdjust 显式触发，调用方决定"跑→调→再跑"的相位。
func (h *Harness) Run(episodes, budget int) (Summary, error) {
	sum := Summary{
		ActionDim:  h.Space.Dim(),
		ActionKeys: h.Space.Keys,
		Policy:     h.activePolicy().Name(),
		StorePath:  h.Store.path,
	}
	pol := h.activePolicy()
	for ep := 0; ep < episodes; ep++ {
		seed := h.seed*1000003 + ep
		st := newSimState(h.rng, seed, budget)
		epr := EpisodeResult{Episode: ep, Seed: seed}
		obs := h.obs(st)
		for !st.term && !st.trunc {
			k := pol.Act(obs)
			eta, delta := st.step(k, h.Space, h.Regions, h.Target)
			rec := StepRecord{
				RunID:      h.runID,
				Episode:    ep,
				Step:       st.used - 1,
				Seed:       seed,
				Action:     k,
				ActionKey:  h.Space.Keys[k],
				Mu:         st.mu,
				Eta:        eta,
				Score:      st.score,
				Delta:      delta,
				Terminated: st.term,
				Obs:        append([]float64(nil), obs...),
			}
			if err := h.Store.Append(rec); err != nil {
				return sum, err
			}
			epr.Actions = append(epr.Actions, k)
			obs = h.obs(st)
		}
		epr.FinalScore = st.score
		epr.Terminated = st.term
		epr.Steps = st.used
		sum.NEpisodes++
		sum.NSteps += st.used
		sum.MeanFinalScore += st.score
		if st.term {
			sum.TerminatedCount++
		}
		if st.score > sum.BestScore {
			sum.BestScore = st.score
		}
	}
	if sum.NEpisodes > 0 {
		sum.MeanFinalScore /= float64(sum.NEpisodes)
	}

	recs, err := h.Store.Records()
	if err != nil {
		return sum, err
	}
	sum.PerActionMeanDelta = perActionMeanDelta(recs, sum.ActionDim)
	sum.BestDesigns = topDesigns(h.Store.Episodes(), 5)
	sum.StoreLen = h.Store.Len()
	return sum, nil
}

// SelfAdjust 是"自我调整"的显式入口：
//
//  1. 用全量 store 重算启发式偏置（规则层，总是生效）；
//  2. 若启用 OML 且样本足够，把 (obs, action, delta) 喂给持续学习器（失败则记错并保留启发式）。
func (h *Harness) SelfAdjust() error {
	recs, err := h.Store.Records()
	if err != nil {
		return err
	}
	h.heuristic.Fit(recs)

	if h.learner != nil && len(recs) >= minSamplesOLM {
		samples := make([]Sample, 0, len(recs))
		for _, r := range recs {
			if len(r.Obs) == 0 {
				continue
			}
			samples = append(samples, Sample{Obs: r.Obs, Action: r.Action, Reward: r.Delta})
		}
		if err := h.learner.Fit(samples); err != nil {
			// fail-closed：不崩溃，保留启发式；记最后错误供报告/调试。
			h.lastLearnerErr = err
			return nil
		}
		h.learnerOK = true
	}
	return nil
}

// LastLearnerErr 返回最近一次 OML 训练的错误（nil = 成功或未启用）。
func (h *Harness) LastLearnerErr() error { return h.lastLearnerErr }

// Summarize 仅从 store 重建摘要（不跑新实验），供 `forge harness report` / `self` 使用。
func (h *Harness) Summarize() (Summary, error) {
	recs, err := h.Store.Records()
	if err != nil {
		return Summary{}, err
	}
	eps := h.Store.Episodes()
	var mean, best float64
	for _, e := range eps {
		mean += e.FinalScore
		if e.FinalScore > best {
			best = e.FinalScore
		}
	}
	var meanFinal float64
	if len(eps) > 0 {
		meanFinal = mean / float64(len(eps))
	}
	return Summary{
		NEpisodes:         len(eps),
		NSteps:            len(recs),
		StoreLen:          h.Store.Len(),
		BestScore:         best,
		MeanFinalScore:    meanFinal,
		ActionDim:         h.Space.Dim(),
		ActionKeys:        h.Space.Keys,
		Policy:            h.activePolicy().Name(),
		StorePath:         h.Store.path,
		PerActionMeanDelta: perActionMeanDelta(recs, h.Space.Dim()),
		BestDesigns:       topDesigns(eps, 5),
	}, nil
}

// perActionMeanDelta 从记录算每个动作的平均 score 增量。
func perActionMeanDelta(recs []StepRecord, dim int) []float64 {
	sum := make([]float64, dim)
	cnt := make([]int, dim)
	for _, r := range recs {
		if r.Action < 0 || r.Action >= dim {
			continue
		}
		sum[r.Action] += r.Delta
		cnt[r.Action]++
	}
	out := make([]float64, dim)
	for k := 0; k < dim; k++ {
		if cnt[k] > 0 {
			out[k] = sum[k] / float64(cnt[k])
		}
	}
	return out
}
