// Package rlenv: design 环境 —— OaK / RL 的 Phase-1 着陆场。
//
// 冻结接口 (v0.1) — 负责人: stage F。
//
// 这里有什么: 一个完整、可用的环境。这里刻意没有什么: 一个学到的 policy。这种
// 不对称正是要点。Phase 0 问“机器能找到比人更好的 design 吗?”, 由搜索算法回答。
// Phase 1 问“系统能学会提出 design, 并记住它学到的东西吗?”, 只有到那时 policy 才
// 属于这里。在那之前 LoadPolicy 返回 ErrNoLearnedPolicy —— 一个大声的失败, 而不是
// 一个编造出来的数字。
//
// # 下面实现的约定
//
//   - 环境的状态是以 SI 单位表示的 design 向量, 始终处在 spec 盒子之内 (先裁剪动作,
//     再裁剪结果)。
//
//   - 观测是 [归一化 design | 归一化 metrics]:
//
//     obs[i]          = 2*(x[i]-lower[i])/(upper[i]-lower[i]) - 1   位于 [-1,1]
//     obs[D+i]        = metric(ObsMetricKeys[i]) / ObsMetricRefs[i]
//
//     归一化 design 不是装饰性的: r 是 O(0.1) 而 I 是 O(1e6), 因此一个读原始 SI
//     单位的 policy 会看到某一维比另一维大一百万倍。
//
//   - 动作是在同一套归一化单位下的增量: 被裁剪到 [-1,1]^D 的动作 a 会变成
//     dx[i] = a[i] * DeltaScale * (upper[i]-lower[i])。
//
//   - 一步之后的状态是 scorer 实际求值过的那个 design (EvalResult.Design, 即规范的
//     已裁剪/z 排序向量), 而不是我们希望它求值的那个向量。
//
// # 大声失败策略 (与 LoadPolicy 同一条规则)
//
// 当这个环境无法给出诚实的答案时, 它会带精确消息 panic, 而不是返回一个编造的数字:
// 向量长度不匹配、nil scorer, 以及非有限的 score 或 metrics 都会大声失败。这些是
// 编程错误或上游数值 bug; 悄悄替换成 0.0 会把它们变成看不见的训练噪声。
package rlenv

import (
	"errors"
	"fmt"
	"math"
	"math/rand"

	"github.com/logos-42/hushfusion-forge/internal/config"
	"github.com/logos-42/hushfusion-forge/internal/objective"
	"github.com/logos-42/hushfusion-forge/internal/physics"
	"github.com/logos-42/hushfusion-forge/internal/runner"
)

// ErrNoLearnedPolicy 由 LoadPolicy 返回: Phase 1 尚未实现。
var ErrNoLearnedPolicy = errors.New(
	"Phase 1 not implemented: this repository contains a complete RL environment and a random-policy " +
		"reference, but no learned policy. A design-proposing policy is Phase-1 work (see PLAN.md)")

// Info 是每一步的细节。
type Info struct {
	Score      float64 `json:"score"`
	DeltaScore float64 `json:"delta_score"`
	Feasible   bool    `json:"feasible"`
	DesignID   string  `json:"design_id"`
	Step       int     `json:"step"`
}

// Env 是一个类 gym 的 design 环境 (不依赖 gym)。
//
//	obs = Reset(x0)
//	obs, reward, terminated, truncated, info = Step(action)
//
// action: design 向量上的增量, 归一化到 [-1,1]^D, 并按参数乘以
// DeltaScale * (upper-lower)。
//
// reward: score(之后) - score(之前)。这是一个基于势能的差, 因此整条 episode 的
// return 精确地望远镜式求和为 score(最终) - score(初始), 无需凭空发明一个 shaping
// 常数。
type Env struct {
	Sc         runner.Scorer
	Spec       config.Spec
	MaxSteps   int
	DeltaScale float64

	// --- episode 状态 (未导出: 用 NewEnv 构建环境) ---
	lower, upper []float64
	x            []float64       // 当前 design, SI 单位, 始终在盒子之内
	metrics      physics.Metrics // 当前 design 的 metrics
	lastScore    float64         // 当前 design 的 score
	initialScore float64         // Reset 时的 score, episode 的 return 基准
	bestScore    float64
	totalReward  float64
	step         int    // Reset 以来访问过的 design 数 (0 == reset 时的 design)
	nEvals       int    // 这个 Env 已花费的求值次数, 跨 episode 单调递增
	lastDesignID string // 当前 design 的 design_id == 下一步的父节点
	curFeasible  bool   // 当前 design 的可行性
	started      bool   // 设为 false 意味着 Reset 还没运行
	done         bool
}

// 对应字段留零时使用的默认值。
//
// 房规: 影响分数的数字只有一个家 —— 这两个默认值住在 internal/config, 这里只做引用,
// 不许再抄一份字面量(两份"差不多的"默认值会让两边安静地漂移)。
// api.go 的冻结面是签名 / 类型 / JSON tag 与 golden 数值; 私有常量的初始值不在其中。
const (
	defaultMaxSteps   = config.DefaultMaxSteps
	defaultDeltaScale = config.DefaultDeltaScale
)

// 通过 runner.Meta 写入 registry 的 algorithm 标签。
const (
	algorithmEnv          = "rlenv"
	algorithmRandomPolicy = "random_policy"
)

// ObsMetricKeys 是追加到观测向量末尾的冻结 metric 槽位。
var ObsMetricKeys = []string{
	"B_mid_T", "B_throat_T", "mirror_ratio", "volume_good", "ripple", "B_coil_max_T", "cost_proxy",
}

// ObsMetricRefs 对这些槽位进行归一化: raw/ref。
var ObsMetricRefs = []float64{1.0, 1.0, 1.0, 1.0, 0.1, 12.0, 1.0}

// NewEnv 构建一个环境。默认值: MaxSteps 20, DeltaScale 0.15。
func NewEnv(sc runner.Scorer, spec config.Spec, maxSteps int, deltaScale float64) *Env {
	if maxSteps <= 0 {
		maxSteps = defaultMaxSteps
	}
	if deltaScale <= 0 {
		deltaScale = defaultDeltaScale
	}
	e := &Env{Sc: sc, Spec: spec, MaxSteps: maxSteps, DeltaScale: deltaScale}
	e.ensure()
	return e
}

// ensure 为一个部分构建的 Env 填上默认值, 并校验冻结的观测布局。它让带键的
// struct 字面量 (Env{Sc: sc, Spec: spec}) 也能像 NewEnv 一样工作。
func (e *Env) ensure() {
	if e.Sc == nil {
		panic("rlenv: Env.Sc is nil — the environment needs a runner.Scorer")
	}
	if len(ObsMetricKeys) != len(ObsMetricRefs) {
		panic(fmt.Sprintf("rlenv: frozen observation layout is inconsistent: %d metric keys vs %d refs",
			len(ObsMetricKeys), len(ObsMetricRefs)))
	}
	if e.MaxSteps <= 0 {
		e.MaxSteps = defaultMaxSteps
	}
	if e.DeltaScale <= 0 {
		e.DeltaScale = defaultDeltaScale
	}
	if len(e.lower) != e.Spec.NParams() {
		e.lower = e.Spec.Lower()
		e.upper = e.Spec.Upper()
		if len(e.lower) != e.Spec.NParams() || len(e.upper) != e.Spec.NParams() {
			panic(fmt.Sprintf("rlenv: spec bounds have length %d/%d, want NParams=%d",
				len(e.lower), len(e.upper), e.Spec.NParams()))
		}
	}
}

// ActionDim 是 design 向量的长度。
func (e *Env) ActionDim() int { e.ensure(); return e.Spec.NParams() }

// ObservationDim 等于 ActionDim + len(ObsMetricKeys)。
func (e *Env) ObservationDim() int { e.ensure(); return e.Spec.NParams() + len(ObsMetricKeys) }

// Reset 从 x0 开始一条 episode (x0 为 nil 时使用均匀随机 design), 对它求值并
// 记录, 然后返回观测。
func (e *Env) Reset(x0 []float64) []float64 {
	e.ensure()
	if x0 == nil {
		// 在盒子内均匀分布。全局 rand 源 (线程安全) 只用来给每次调用的生成器
		// 播种, 因此两次并发 reset 不会共享同一个流。
		rng := rand.New(rand.NewSource(rand.Int63()))
		x0 = physics.RandomDesign(rng, e.Spec)
	}
	if len(x0) != e.Spec.NParams() {
		panic(fmt.Sprintf("rlenv: Reset got a %d-element design, want NParams=%d",
			len(x0), e.Spec.NParams()))
	}

	e.x = e.clipDesign(x0)
	e.step = 0
	e.done = false
	e.totalReward = 0
	e.started = true
	e.bestScore = math.Inf(-1)

	res := e.score(0, "")
	e.absorb(res)

	e.initialScore = e.lastScore
	e.bestScore = e.lastScore
	return e.observation()
}

// Step 施加一个动作, 求值并记录新的 design (以前一个 design 为父节点, 因此这条
// episode 形成一条 lineage 链)。
func (e *Env) Step(action []float64) (obs []float64, reward float64, terminated, truncated bool, info Info) {
	e.ensure()
	if !e.started {
		panic("rlenv: Step called before Reset")
	}
	if len(action) != e.Spec.NParams() {
		panic(fmt.Sprintf("rlenv: Step got a %d-element action, want ActionDim=%d",
			len(action), e.Spec.NParams()))
	}
	if e.done {
		// 已结束的 episode 是空操作, 不是编造出来的 reward: 再次报告终止状态,
		// 且不为此计任何分。
		return e.observation(), 0, true, true, Info{
			Score: e.lastScore, DeltaScore: 0, Feasible: e.curFeasible,
			DesignID: e.lastDesignID, Step: e.step,
		}
	}

	// 移动状态: 动作是归一化单位下的增量, 按参数缩放, 并在动作空间与 design
	// 盒子两侧都裁剪。
	for i := range e.x {
		a := clip(action[i], -1, 1)
		e.x[i] = clip(e.x[i]+a*e.DeltaScale*(e.upper[i]-e.lower[i]), e.lower[i], e.upper[i])
	}

	parent := e.lastDesignID
	before := e.lastScore
	res := e.score(e.step+1, parent)
	e.absorb(res)

	e.step++
	e.totalReward += e.lastScore - before

	terminated = false // design 空间没有吸收态
	truncated = e.step >= e.MaxSteps
	e.done = truncated

	info = Info{
		Score:      e.lastScore,
		DeltaScore: e.lastScore - before,
		Feasible:   e.curFeasible,
		DesignID:   e.lastDesignID,
		Step:       e.step,
	}
	return e.observation(), info.DeltaScore, terminated, truncated, info
}

// score 以正确的来源信息求值当前 design。generation 是这个 design 在 episode 中
// 的位置 (0 = reset 时的 design); parent 是前一个 design 的 design_id (根节点
// 为 "")。
func (e *Env) score(generation int, parent string) objective.EvalResult {
	note := fmt.Sprintf("rlenv episode design %d", generation)
	if generation == 0 {
		note = "rlenv episode start"
	}
	res := e.Sc.Score(copyOf(e.x), runner.Meta{
		Algorithm:  algorithmEnv,
		Generation: generation,
		EvalIndex:  e.nEvals,
		Parent:     parent,
		Note:       note,
	})
	e.nEvals++
	if !isFinite(res.Score) {
		panic(fmt.Sprintf("rlenv: scorer returned a non-finite score (%v) — refusing to build an "+
			"episode on a number that is not a number", res.Score))
	}
	return res
}

// absorb 把 scorer 实际求值的结果存为新状态。
func (e *Env) absorb(res objective.EvalResult) {
	if len(res.Design) == len(e.x) {
		e.x = copyOf(res.Design)
	}
	// 否则说明 scorer 没有回显一个规范 design (玩具 scorer, 或某一层返回了
	// nil), 那么我们发出去的向量就作为状态; 在这里发明别的东西会让状态变得
	// 不可证伪。
	e.lastScore = res.Score
	if res.Score > e.bestScore {
		e.bestScore = res.Score
	}
	e.metrics = res.Metrics
	e.curFeasible = res.Feasible
	e.lastDesignID = res.DesignID
}

// observation 为 policy 渲染当前状态。
func (e *Env) observation() []float64 {
	out := make([]float64, 0, e.ObservationDim())
	for i := range e.x {
		span := e.upper[i] - e.lower[i]
		if span <= 0 {
			panic(fmt.Sprintf("rlenv: degenerate bound span (upper-lower=%v) at parameter %d", span, i))
		}
		out = append(out, clip(2*(e.x[i]-e.lower[i])/span-1, -1, 1))
	}
	for i, key := range ObsMetricKeys {
		ref := ObsMetricRefs[i]
		if ref == 0 {
			ref = 1
		}
		v := ObsMetricValue(e.metrics, key)
		if !isFinite(v) {
			panic(fmt.Sprintf("rlenv: metric %q is not finite (%v) — refusing to hand a policy a "+
				"NaN observation", key, v))
		}
		out = append(out, v/ref)
	}
	return out
}

// clipDesign 复制 x 并把它夹进 spec 盒子。
func (e *Env) clipDesign(x []float64) []float64 {
	out := make([]float64, len(x))
	for i := range x {
		out[i] = clip(x[i], e.lower[i], e.upper[i])
	}
	return out
}

// ObsMetricValue 把一个 physics.Metrics 字段映射到它冻结的观测键。
//
// 未知的键会 panic: 一个加进 ObsMetricKeys 却在这里没有映射的键, 绝不能在观测向量
// 里悄悄变成 0.0。
func ObsMetricValue(m physics.Metrics, key string) float64 {
	switch key {
	case "B_mid_T":
		return m.BMidT
	case "B_throat_T":
		return m.BThroatT
	case "mirror_ratio":
		return m.MirrorRatio
	case "volume_good":
		return m.VolumeGood
	case "ripple":
		return m.Ripple
	case "B_coil_max_T":
		return m.BCoilMaxT
	case "cost_proxy":
		return m.CostProxy
	default:
		panic(fmt.Sprintf("rlenv: observation metric key %q has no mapping in ObsMetricValue", key))
	}
}

// Rollout 是一次 policy 评估的摘要。
type Rollout struct {
	Policy      string  `json:"policy"`
	NSteps      int     `json:"n_steps"`
	TotalReward float64 `json:"total_reward"`
	BestScore   float64 `json:"best_score"`
	FinalScore  float64 `json:"final_score"`
}

// RandomPolicyRollout 是诚实的参照: 一个真的会跑、且刻意很笨的 policy (均匀随机
// 动作)。Phase 1 在同等预算下把学习器与它对比。
func RandomPolicyRollout(e *Env, nSteps int, seed int64) Rollout {
	if e == nil {
		panic("rlenv: RandomPolicyRollout needs an environment")
	}
	e.ensure()
	if nSteps <= 0 {
		panic(fmt.Sprintf("rlenv: RandomPolicyRollout needs a positive step count, got %d", nSteps))
	}

	// 在一份私有副本上求值, 这样调用方的环境不被触碰, 并给每条 record 打上本次
	// rollout 的 seed 与 policy 名称。
	rng := rand.New(rand.NewSource(seed))
	env := NewEnv(policyScorer{sc: e.Sc, seed: int(seed), policy: algorithmRandomPolicy},
		e.Spec, e.MaxSteps, e.DeltaScale)

	env.Reset(physics.RandomDesign(rng, e.Spec))

	out := Rollout{Policy: algorithmRandomPolicy, BestScore: env.bestScore}
	action := make([]float64, env.ActionDim())
	for i := 0; i < nSteps; i++ {
		for j := range action {
			action[j] = 2*rng.Float64() - 1
		}
		_, _, terminated, truncated, _ := env.Step(action)
		out.NSteps++
		if terminated || truncated {
			break
		}
	}
	out.TotalReward = env.totalReward
	out.FinalScore = env.lastScore
	if env.bestScore > out.BestScore {
		out.BestScore = env.bestScore
	}
	return out
}

// policyScorer 给它经手的每条 record 打上产出它的 policy 标签, 这样一次 rollout
// 就不会以 "rlenv" 的身份落进 registry —— registry 必须能说出是哪个 policy 提出了
// 这个 design。
type policyScorer struct {
	sc     runner.Scorer
	seed   int
	policy string
}

func (p policyScorer) Score(x []float64, meta runner.Meta) objective.EvalResult {
	meta.Algorithm = p.policy
	meta.Seed = p.seed
	return p.sc.Score(x, meta)
}

// LoadPolicy 总是以 ErrNoLearnedPolicy 失败。
func LoadPolicy(path string) error { return ErrNoLearnedPolicy }

// --- 小辅助函数 ---

func clip(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func copyOf(x []float64) []float64 {
	out := make([]float64, len(x))
	copy(out, x)
	return out
}

func isFinite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
