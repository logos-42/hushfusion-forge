package rlenv

import (
	"errors"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/logos-42/hushfusion-forge/internal/config"
	"github.com/logos-42/hushfusion-forge/internal/objective"
	"github.com/logos-42/hushfusion-forge/internal/physics"
	"github.com/logos-42/hushfusion-forge/internal/runner"
)

// ---------------------------------------------------------------------------
// 一个玩具 scorer: 确定、自包含、独立于其他所有 stage。它只触碰 config (冻结的
// spec/bounds) 与冻结的 EvalResult / Metrics 类型, 因此当 physics、objective 和
// registry 包仍是骨架时这些测试也能保持绿色。
// ---------------------------------------------------------------------------

type toyScorer struct {
	spec config.Spec

	// 记录观测用的台账, 用来验证来源信息与 lineage
	calls     int
	designs   []string // design_id handed out, in order
	parents   []string // meta.Parent received, in order
	algos     []string
	seeds     []int
	evalIdx   []int
	gens      []int
	lastEval  []float64 // 被求值时的原始向量 (经过任何规范化之后)
	gotDesign bool
	// nonFinite 让 scorer 返回 math.NaN, 以演练大声失败的路径
	nonFinite bool
}

// toyScore 是 design 向量的纯函数 —— 没有计数器, 没有状态 —— 这样 reward 序列
// 可以独立于环境自己的台账做望远镜式求和。
func toyScore(spec config.Spec, x []float64) float64 {
	lo, hi := spec.Lower(), spec.Upper()
	if len(x) != len(lo) {
		panic(fmt.Sprintf("toyScore: got %d params, want %d", len(x), len(lo)))
	}
	s := 0.0
	for i, v := range x {
		span := hi[i] - lo[i]
		target := lo[i] + (0.30+0.08*float64(i%5))*span
		d := (v - target) / span
		s -= d * d
	}
	// 一个平滑的耦合项, 使曲面不可分离 (policy 无法靠一次调一个坐标取胜)。
	s -= 0.25 * math.Sin(4*math.Pi*(x[0]-lo[0])/hi[0])
	return s
}

func toyMetrics(spec config.Spec, x []float64) physics.Metrics {
	n := spec.NCoils
	sumR, sumI := 0.0, 0.0
	for i := 0; i < n && i < len(x); i++ {
		sumR += x[i]
		sumI += x[2*n+i]
	}
	cost := 0.0
	for i := 0; i < n && i < len(x); i++ {
		cost += x[2*n+i] * x[2*n+i] * x[i]
	}
	bMid := 1.0 + 0.05*math.Sin(sumR)
	ratio := 2.0 + 0.5*math.Cos(sumI/1e6)
	return physics.Metrics{
		BMidT:       bMid,
		BThroatT:    bMid * ratio,
		ZThroatM:    -1.0 + 0.01*math.Sin(sumR),
		MirrorRatio: ratio,
		VolumeGood:  0.5 + 0.1*math.Sin(sumI/1e5),
		Ripple:      0.02 * math.Abs(math.Sin(sumR)),
		BCoilMaxT:   3.0 + 0.1*math.Cos(sumI/1e6),
		MinCoilGapM: 0.5,
		CostProxy:   cost,
		NCoils:      n,
		MU0:         config.MU0,
	}
}

func (t *toyScorer) Score(x []float64, meta runner.Meta) objective.EvalResult {
	t.calls++
	t.designs = append(t.designs, fmt.Sprintf("D%04d", t.calls))
	t.parents = append(t.parents, meta.Parent)
	t.algos = append(t.algos, meta.Algorithm)
	t.seeds = append(t.seeds, meta.Seed)
	t.evalIdx = append(t.evalIdx, meta.EvalIndex)
	t.gens = append(t.gens, meta.Generation)
	t.lastEval = append([]float64(nil), x...)
	t.gotDesign = true

	design := append([]float64(nil), x...)
	score := toyScore(t.spec, design)
	if t.nonFinite {
		score = math.NaN()
	}
	return objective.EvalResult{
		Score:  score,
		Design: design,
		Terms:  map[string]float64{objective.TermField: 0, objective.TermCost: 1},
		Weighted: map[string]float64{
			objective.TermField: 0, objective.TermCost: -1,
		},
		Penalties:    map[string]float64{},
		Feasible:     true,
		Metrics:      toyMetrics(t.spec, design),
		DesignID:     fmt.Sprintf("D%04d", t.calls),
		ExperimentID: t.calls,
	}
}

func newToy(spec config.Spec) *toyScorer { return &toyScorer{spec: spec} }

// midDesign 返回一个确定的、位于盒内的起始 design。
func midDesign(spec config.Spec) []float64 {
	lo, hi := spec.Lower(), spec.Upper()
	x := make([]float64, len(lo))
	for i := range x {
		x[i] = lo[i] + 0.5*(hi[i]-lo[i])
	}
	return x
}

// ---------------------------------------------------------------------------
// G10 —— 反造假门。
// ---------------------------------------------------------------------------

// TestNoLearnedPolicy 是验收门 G10: 本仓库没有学到的 policy, 因此 LoadPolicy 对
// 任何输入都必须失败, 而不是返回一个编造的数字。一个存在着的文件仍然不是 policy。
func TestNoLearnedPolicy(t *testing.T) {
	paths := []string{
		"", "policy.json", "checkpoints/ppo.bin", "/nonexistent/path/policy.joblib",
	}
	for _, p := range paths {
		err := LoadPolicy(p)
		if err == nil {
			t.Fatalf("LoadPolicy(%q) returned nil: a learned policy must not be invented here", p)
		}
		if !errors.Is(err, ErrNoLearnedPolicy) {
			t.Fatalf("LoadPolicy(%q) error = %v, want ErrNoLearnedPolicy", p, err)
		}
	}

	// 即使是真实、可读的文件也不能被接受: 如果 LoadPolicy 有朝一日开始读文件, 它
	// 也必须有朝一日开始证明文件里是什么。
	dir := t.TempDir()
	f := filepath.Join(dir, "plausible_policy.json")
	if werr := os.WriteFile(f, []byte(`{"weights":[1,2,3],"score":-1.234}`), 0o644); werr != nil {
		t.Fatalf("write temp policy: %v", werr)
	}
	if err := LoadPolicy(f); !errors.Is(err, ErrNoLearnedPolicy) {
		t.Fatalf("LoadPolicy(%q) on an existing file = %v, want ErrNoLearnedPolicy", f, err)
	}

	// 这个错误必须能自我解释, 而不是一句光秃秃的 "not implemented"。
	if msg := ErrNoLearnedPolicy.Error(); !strings.Contains(msg, "Phase 1") {
		t.Errorf("ErrNoLearnedPolicy message should name Phase 1, got %q", msg)
	}
}

// ---------------------------------------------------------------------------
// reward = score(之后) - score(之前), 对照对首个与最终 design 的独立求值来检查。
// ---------------------------------------------------------------------------

// TestRewardTelescopes 断言 episode 的 return 等于 score(最终) - score(初始),
// 误差在 1e-9 以内, 且 score() 是在环境之外用同一个确定 scorer 重新算出来的。
func TestRewardTelescopes(t *testing.T) {
	spec := config.DefaultSpec()
	sc := newToy(spec)
	const steps = 20

	env := NewEnv(sc, spec, steps, 0.15)
	x0 := midDesign(spec)
	env.Reset(x0)

	rng := rand.New(rand.NewSource(20260926))
	action := make([]float64, env.ActionDim())
	sum := 0.0
	nonZeroRewards := 0
	prevEval := append([]float64(nil), sc.lastEval...)
	moved := 0
	for i := 0; i < steps; i++ {
		for j := range action {
			action[j] = 2*rng.Float64() - 1
		}
		_, reward, terminated, truncated, info := env.Step(action)
		if terminated {
			t.Fatalf("step %d reported terminated: the design space has no absorbing state", i)
		}
		if truncated != (i == steps-1) {
			t.Fatalf("step %d reported truncated=%v; the step limit %d must be flagged on its last step",
				i, truncated, steps)
		}
		if math.Abs(reward-info.DeltaScore) > 1e-12 {
			t.Fatalf("step %d: returned reward %v != info.DeltaScore %v", i, reward, info.DeltaScore)
		}
		if reward != 0 {
			nonZeroRewards++
		}
		for j := range prevEval {
			if sc.lastEval[j] != prevEval[j] {
				moved++
				break
			}
		}
		prevEval = append(prevEval[:0], sc.lastEval...)
		sum += reward
	}

	// episode 必须真的在移动: 一个在冻结 design 上也能通过的“reward 望远镜式
	// 求和”测试证明不了任何东西。
	if moved == 0 {
		t.Fatal("no step changed the design: the action had no effect on the state")
	}
	if nonZeroRewards == 0 {
		t.Fatal("every step returned reward 0: the episode is a fixed point, so telescoping is vacuous")
	}

	finalDesign := sc.lastEval
	want := toyScore(spec, finalDesign) - toyScore(spec, x0)

	if math.Abs(sum-want) > 1e-9 {
		t.Fatalf("episode return does not telescope: sum(reward)=%.17g, score(final)-score(initial)=%.17g, diff=%.3g",
			sum, want, math.Abs(sum-want))
	}
	if math.Abs(env.totalReward-sum) > 1e-12 {
		t.Fatalf("env.totalReward=%v != sum of step rewards %v", env.totalReward, sum)
	}
	if math.Abs(sum-(env.lastScore-env.initialScore)) > 1e-9 {
		t.Fatalf("env bookkeeping does not telescope: %v vs %v", sum, env.lastScore-env.initialScore)
	}
	if env.step != steps {
		t.Fatalf("env.step = %d, want %d", env.step, steps)
	}
	if env.bestScore < math.Max(env.initialScore, env.lastScore)-1e-12 {
		t.Fatalf("bestScore %v is below the trajectories it summarises (initial %v, final %v)",
			env.bestScore, env.initialScore, env.lastScore)
	}
}

// ---------------------------------------------------------------------------
// 形状、边界与裁剪。
// ---------------------------------------------------------------------------

// TestActionSemantics 钉住一个动作做了什么: 每个参数精确地是
// DeltaScale*(upper-lower) 的归一化增量, 在动作空间裁剪到 [-1,1]、在 design 空间
// 裁剪到盒子, 以及移动后 design 的 reward。
func TestActionSemantics(t *testing.T) {
	spec := config.DefaultSpec()
	sc := newToy(spec)
	lo, hi := spec.Lower(), spec.Upper()
	const scale = 0.15

	env := NewEnv(sc, spec, 4, scale)
	x0 := midDesign(spec) // 盒子中心: +1 的动作不会被裁剪
	env.Reset(x0)

	action := make([]float64, env.ActionDim())
	for i := range action {
		action[i] = 1
	}
	obs, reward, _, _, _ := env.Step(action)

	for i := range x0 {
		want := x0[i] + 1*scale*(hi[i]-lo[i])
		if math.Abs(sc.lastEval[i]-want) > 1e-12 {
			t.Fatalf("parameter %d after a +1 action = %v, want %v (span %v)",
				i, sc.lastEval[i], want, hi[i]-lo[i])
		}
		// 因为盒子中心是 0, 归一化观测单位下是 2*scale
		wantObs := 2 * scale
		if math.Abs(obs[i]-wantObs) > 1e-12 {
			t.Fatalf("normalised slot %d after a +1 action = %v, want %v", i, obs[i], wantObs)
		}
	}
	wantReward := toyScore(spec, sc.lastEval) - toyScore(spec, x0)
	if math.Abs(reward-wantReward) > 1e-12 {
		t.Fatalf("reward %v != score(after)-score(before) = %v", reward, wantReward)
	}
	if reward == 0 {
		t.Fatal("a design-changing action returned reward 0")
	}

	// 越界动作的行为必须与裁剪后的完全一致 (增量由 DeltaScale 限制, 而不是由
	// 调用方对量级的想法决定)。
	for i := range action {
		action[i] = 1e9
	}
	obsHuge, rewardHuge, _, _, _ := env.Step(action)
	for i := range x0 {
		want := midDesign(spec)[i] + 2*scale*(hi[i]-lo[i]) // 从中心出发两步 +1
		if math.Abs(sc.lastEval[i]-want) > 1e-12 {
			t.Fatalf("parameter %d after a +1e9 action = %v, want %v (clip to +1)", i, sc.lastEval[i], want)
		}
		if math.Abs(obsHuge[i]-(2*2*scale)) > 1e-12 {
			t.Fatalf("normalised slot %d after a +1e9 action = %v, want %v", i, obsHuge[i], 4*scale)
		}
	}
	if rewardHuge == 0 {
		t.Fatal("a clipped-but-nonzero action returned reward 0")
	}
}

// TestObsDimAndBounds 检查形状、归一化 design 映射、metric 槽位缩放, 以及“没有
// 动作能把 design 推出盒子”这一不变量。
func TestObsDimAndBounds(t *testing.T) {
	spec := config.DefaultSpec()
	sc := newToy(spec)
	env := NewEnv(sc, spec, 8, 0.15)

	dim := spec.NParams()
	if got := env.ActionDim(); got != dim {
		t.Fatalf("ActionDim = %d, want %d", got, dim)
	}
	if got, want := env.ObservationDim(), dim+len(ObsMetricKeys); got != want {
		t.Fatalf("ObservationDim = %d, want %d", got, want)
	}
	if len(ObsMetricKeys) != len(ObsMetricRefs) {
		t.Fatalf("frozen observation layout broken: %d keys vs %d refs", len(ObsMetricKeys), len(ObsMetricRefs))
	}

	lo, hi := spec.Lower(), spec.Upper()
	checkObs := func(tag string, obs []float64) {
		t.Helper()
		if len(obs) != env.ObservationDim() {
			t.Fatalf("%s: observation length %d, want %d", tag, len(obs), env.ObservationDim())
		}
		for i := 0; i < dim; i++ {
			if obs[i] < -1 || obs[i] > 1 {
				t.Fatalf("%s: normalised design slot %d = %v outside [-1,1]", tag, i, obs[i])
			}
		}
		for i := dim; i < len(obs); i++ {
			if math.IsNaN(obs[i]) || math.IsInf(obs[i], 0) {
				t.Fatalf("%s: metric slot %q = %v is not finite", tag, ObsMetricKeys[i-dim], obs[i])
			}
		}
		// 被求值的 design 必须始终落在 spec 盒子之内
		for i, v := range sc.lastEval {
			if v < lo[i]-1e-9 || v > hi[i]+1e-9 {
				t.Fatalf("%s: evaluated parameter %d = %v outside [%v, %v]", tag, i, v, lo[i], hi[i])
			}
		}
	}

	// 一个远远在盒子外的起始 design 必须被裁剪, 而不是被接受。
	wild := make([]float64, dim)
	for i := range wild {
		if i%2 == 0 {
			wild[i] = lo[i] - 1e9
		} else {
			wild[i] = hi[i] + 1e9
		}
	}
	obs := env.Reset(wild)
	checkObs("reset(out-of-box)", obs)
	for i := range wild {
		if sc.lastEval[i] != lo[i] && sc.lastEval[i] != hi[i] {
			t.Fatalf("parameter %d = %v was not clipped to a bound [%v, %v]", i, sc.lastEval[i], lo[i], hi[i])
		}
	}

	// 极端动作同样绝不能离开盒子。
	rng := rand.New(rand.NewSource(7))
	action := make([]float64, dim)
	for step := 0; step < 40; step++ {
		for j := range action {
			if rng.Intn(2) == 0 {
				action[j] = -1e6
			} else {
				action[j] = 1e6
			}
		}
		obs, _, _, _, _ = env.Step(action)
		if len(obs) == 0 {
			t.Fatal("Step returned an empty observation")
		}
		checkObs(fmt.Sprintf("step(%d)", step), obs)
	}

	// 归一化 design 槽位必须能映射回 scorer 看到的状态:
	// obs[i] = 2*(x-lo)/(hi-lo) - 1。
	for i := 0; i < dim; i++ {
		want := 2*(sc.lastEval[i]-lo[i])/(hi[i]-lo[i]) - 1
		if math.Abs(obs[i]-want) > 1e-12 {
			t.Fatalf("obs slot %d = %v, want %v (normalised design mapping)", i, obs[i], want)
		}
	}
	// 抽查两个 metric 槽位相对其参考缩放的取值。
	m := toyMetrics(spec, sc.lastEval)
	wantMid := m.BMidT / ObsMetricRefs[0]
	if math.Abs(obs[dim]-wantMid) > 1e-12 {
		t.Fatalf("B_mid slot = %v, want %v", obs[dim], wantMid)
	}
	wantRipple := m.Ripple / ObsMetricRefs[4]
	if math.Abs(obs[dim+4]-wantRipple) > 1e-12 {
		t.Fatalf("ripple slot = %v, want %v", obs[dim+4], wantRipple)
	}
}

// TestActionShapeAndTerminationAreLoud 记录失败策略: 畸形的动作、Reset 之前的
// Step、以及非有限的 score 都会大声失败, 而不是产出一个看起来合理的数字。
func TestActionShapeAndTerminationAreLoud(t *testing.T) {
	spec := config.DefaultSpec()

	t.Run("wrong action length panics", func(t *testing.T) {
		env := NewEnv(newToy(spec), spec, 4, 0.15)
		env.Reset(midDesign(spec))
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("Step with a wrong-length action did not fail loudly")
			}
		}()
		env.Step(make([]float64, env.ActionDim()-1))
	})

	t.Run("Step before Reset panics", func(t *testing.T) {
		env := NewEnv(newToy(spec), spec, 4, 0.15)
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("Step before Reset did not fail loudly")
			}
		}()
		env.Step(make([]float64, env.ActionDim()))
	})

	t.Run("nil scorer panics", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("a nil scorer did not fail loudly")
			}
		}()
		NewEnv(nil, spec, 4, 0.15)
	})

	t.Run("rollout input guards", func(t *testing.T) {
		env := NewEnv(newToy(spec), spec, 4, 0.15)
		// nil 环境与非正的步数是调用方错误, 必须照此报告, 而不是悄悄按 0 步
		// rollout。
		func() {
			defer func() {
				if r := recover(); r == nil {
					t.Error("RandomPolicyRollout(nil, ...) did not fail loudly")
				}
			}()
			RandomPolicyRollout(nil, 3, 1)
		}()
		func() {
			defer func() {
				if r := recover(); r == nil {
					t.Error("RandomPolicyRollout(env, 0, ...) did not fail loudly")
				}
			}()
			RandomPolicyRollout(env, 0, 1)
		}()
		// 守卫在任何求值之前运行, 因此 0 步的调用不得触碰 scorer。
		if sc := env.Sc.(*toyScorer); sc.calls != 0 {
			t.Errorf("the rejected rollout spent %d evaluations", sc.calls)
		}
	})

	t.Run("non-finite score panics", func(t *testing.T) {
		sc := newToy(spec)
		sc.nonFinite = true
		env := NewEnv(sc, spec, 4, 0.15)
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("a NaN score was accepted instead of failing loudly")
			}
		}()
		env.Reset(midDesign(spec))
	})

	t.Run("finished episode is a no-op", func(t *testing.T) {
		env := NewEnv(newToy(spec), spec, 2, 0.15)
		env.Reset(midDesign(spec))
		action := make([]float64, env.ActionDim())
		if _, _, term, trunc, _ := env.Step(action); term || trunc {
			t.Fatal("step 1 of a 2-step episode ended it early")
		}
		_, _, terminated, truncated, _ := env.Step(action)
		if !truncated {
			t.Fatalf("step 2 of MaxSteps=2 returned truncated=%v, want true", truncated)
		}
		if terminated {
			t.Fatal("the design space has no absorbing state, terminated should stay false")
		}

		before := env.totalReward
		_, reward, terminated, truncated, info := env.Step(action)
		if !terminated || !truncated {
			t.Fatalf("stepping past MaxSteps returned terminated=%v truncated=%v, want both true", terminated, truncated)
		}
		if reward != 0 || info.DeltaScore != 0 {
			t.Fatalf("a no-op step reported reward=%v (info %v): a finished episode must not invent one", reward, info)
		}
		if env.totalReward != before {
			t.Fatalf("total reward changed on a no-op step: %v -> %v", before, env.totalReward)
		}
	})
}

// ---------------------------------------------------------------------------
// Lineage。
// ---------------------------------------------------------------------------

// TestStepFormsLineage 断言一条 episode 是一条连通的 lineage 链: 每一步的 design
// 都以前一个 design 为父节点, 且只有 reset 时的 design 是根。
func TestStepFormsLineage(t *testing.T) {
	spec := config.DefaultSpec()
	sc := newToy(spec)
	const steps = 6

	env := NewEnv(sc, spec, steps, 0.25)
	env.Reset(midDesign(spec))

	rng := rand.New(rand.NewSource(4242))
	action := make([]float64, env.ActionDim())
	seen := []string{}
	for i := 0; i < steps; i++ {
		for j := range action {
			action[j] = 2*rng.Float64() - 1
		}
		_, _, terminated, truncated, info := env.Step(action)
		if terminated {
			t.Fatalf("step %d reported terminated: the design space has no absorbing state", i)
		}
		if truncated != (i == steps-1) {
			t.Fatalf("step %d reported truncated=%v; the step limit %d must be flagged on its last step",
				i, truncated, steps)
		}
		if info.Step != i+1 {
			t.Fatalf("info.Step = %d, want %d", info.Step, i+1)
		}
		seen = append(seen, info.DesignID)
	}

	if len(sc.parents) != steps+1 {
		t.Fatalf("scorer saw %d evaluations, want %d (reset + %d steps)", len(sc.parents), steps+1, steps)
	}
	if sc.parents[0] != "" {
		t.Fatalf("reset design parent = %q, want an empty root", sc.parents[0])
	}
	for i := 1; i < len(sc.parents); i++ {
		if sc.parents[i] != sc.designs[i-1] {
			t.Fatalf("step %d parent = %q, want the previous design %q (the episode must be a chain)",
				i, sc.parents[i], sc.designs[i-1])
		}
	}
	for i, id := range seen {
		if id == "" {
			t.Fatalf("step %d reported an empty design_id", i)
		}
	}
	// EvalIndex 单调地计数求值次数; Generation 计数在 episode 中的位置。
	for i, idx := range sc.evalIdx {
		if idx != i {
			t.Fatalf("evaluation %d carried EvalIndex %d, want %d", i, idx, i)
		}
	}
	for i, g := range sc.gens {
		if g != i {
			t.Fatalf("evaluation %d carried Generation %d, want %d", i, g, i)
		}
	}
	for i, a := range sc.algos {
		if a != algorithmEnv {
			t.Fatalf("evaluation %d carried Algorithm %q, want %q", i, a, algorithmEnv)
		}
	}
	// 连续两条 episode: 第二次 reset 是一个新的根, 且 EvalIndex 继续计数
	// (registry 的求值计数器不重启)。
	firstEpisodeEvals := sc.calls
	env.Reset(midDesign(spec))
	if got := sc.parents[len(sc.parents)-1]; got != "" {
		t.Fatalf("second episode's reset parent = %q, want an empty root", got)
	}
	if got := sc.evalIdx[len(sc.evalIdx)-1]; got != firstEpisodeEvals {
		t.Fatalf("second episode's reset EvalIndex = %d, want %d (monotone across episodes)", got, firstEpisodeEvals)
	}
	if got := sc.gens[len(sc.gens)-1]; got != 0 {
		t.Fatalf("second episode's reset Generation = %d, want 0", got)
	}
}

// TestEnsureFillsDefaults 检查不经 NewEnv 的带键 struct 字面量仍然可用 (并且默认
// 值就是被文档化的那些)。
func TestEnsureFillsDefaults(t *testing.T) {
	spec := config.DefaultSpec()
	env := &Env{Sc: newToy(spec), Spec: spec}
	if env.ActionDim() != spec.NParams() {
		t.Fatalf("ActionDim = %d, want %d", env.ActionDim(), spec.NParams())
	}
	if env.MaxSteps != defaultMaxSteps || env.DeltaScale != defaultDeltaScale {
		t.Fatalf("defaults = (%d, %v), want (%d, %v)", env.MaxSteps, env.DeltaScale, defaultMaxSteps, defaultDeltaScale)
	}
	obs := env.Reset(midDesign(spec))
	if len(obs) != env.ObservationDim() {
		t.Fatalf("observation length %d, want %d", len(obs), env.ObservationDim())
	}

	e2 := NewEnv(newToy(spec), spec, 0, 0)
	if e2.MaxSteps != defaultMaxSteps || e2.DeltaScale != defaultDeltaScale {
		t.Fatalf("NewEnv(0,0) defaults = (%d, %v), want (%d, %v)", e2.MaxSteps, e2.DeltaScale, defaultMaxSteps, defaultDeltaScale)
	}
}
