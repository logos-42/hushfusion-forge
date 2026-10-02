// objective 代数部分的单元测试。
//
// 这些测试在没有 stage A 的情况下运行: metrics 直接传入(或经由
// stub Solver), 因为计分律 —— 五个 term、三条约束
// 残差、feasibility 以及精确的得分分解 —— 属于 stage B,
// 必须能独立验证。确实需要 physics 核心的测试位于
// evaluate_fullpath_test.go, 在 internal/physics 落地前先搁置。
package objective

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/logos-42/hushfusion-forge/internal/config"
	"github.com/logos-42/hushfusion-forge/internal/physics"
)

// ---------------------------------------------------------------- 测试夹具 ---

// stubSolver 是一个确定性的、不含 physics 的 Solver: 每个
// 采样点都是同一个 |B|。刻意不含 physics —— 它让 evaluator 能被检验
// (并且在 evaluate_fullpath_test.go 里, 整条 Evaluate 路径也能)而不需要 stage A。
type stubSolver struct{ mag float64 }

func (s stubSolver) Name() string { return "stub_constant" }

func (s stubSolver) Magnitude(coils []physics.Coil, r, z []float64) []float64 {
	n := len(r)
	if len(z) < n {
		n = len(z)
	}
	out := make([]float64, n)
	for i := range out {
		out[i] = s.mag
	}
	return out
}

// evalFixture 构造一个 Evaluator, 它唯一有意义的内容是 spec 与
// cost reference。grid 在语法上有效, 因为 NewEvaluator
// 坚持如此; 它的采样值永远不会到达代数部分。
func evalFixture(t *testing.T, spec config.Spec, costRef float64) *Evaluator {
	t.Helper()
	return NewEvaluator(spec, stubSolver{mag: 1.0}, costRef, miniGrids(spec))
}

func miniGrids(spec config.Spec) physics.Grids {
	nMid := spec.NVolR * 5
	n := spec.NAxis + nMid + spec.NVolR*spec.NVolZ
	r := make([]float64, n)
	z := make([]float64, n)
	for i := range r {
		r[i] = 0.05
		z[i] = 0.01 * float64(i)
	}
	return physics.Grids{StackR: r, StackZ: z, NAxis: spec.NAxis, NMid: nMid}
}

func designVector(spec config.Spec) []float64 {
	x := make([]float64, spec.NParams())
	for i := range x {
		x[i] = 0.1 * float64(i+1)
	}
	return x
}

// cleanMirror 是一组不违反任何约束的 metric: 真正的 mirror(R = 3.5)、
// conductor 远在限值之内、线圈相距很远。
func cleanMirror() physics.Metrics {
	return physics.Metrics{
		BMidT:         1.0,
		BThroatT:      3.5,
		ZThroatM:      -1.0,
		MirrorRatio:   3.5,
		VolumeGood:    0.5,
		Ripple:        0.0,
		BCoilMaxT:     3.0,
		MinCoilGapM:   0.5,
		MinClearanceM: 0.25,
		CostProxy:     1.0e12,
		NCoils:        4,
		MU0:           config.MU0,
	}
}

// 冻结的 key 集合, 刻意写成字面量: 如果某个重命名溜进
// 实现里, registry schema 与 Python reference 会静默地
// 分叉, 而这里是廉价地抓到它的地方。
var (
	frozenTermKeys    = []string{"field", "mirror", "volume", "ripple", "cost"}
	frozenPenaltyKeys = []string{"conductor_field", "coil_separation", "not_a_mirror", "clearance"}
)

func relDiff(got, want float64) float64 {
	d := math.Abs(got - want)
	if w := math.Abs(want); w > d {
		return d / w
	}
	return d
}

// ------------------------------------------------------ term / weight ------

func TestTermsAndWeightedForMirrorShapedMetrics(t *testing.T) {
	spec := config.DefaultSpec()
	const (
		mirrorRatio = 3.536386240927585
		cost        = 1.7917030355523e12
	)
	e := evalFixture(t, spec, cost)
	m := physics.Metrics{
		BMidT:         1.0,
		BThroatT:      mirrorRatio,
		ZThroatM:      -0.9975,
		MirrorRatio:   mirrorRatio,
		VolumeGood:    0.7808857808857809,
		Ripple:        0.0,
		BCoilMaxT:     3.416271368796406,
		MinCoilGapM:   0.5,
		MinClearanceM: 0.25,
		CostProxy:     cost,
		NCoils:        4,
		MU0:           config.MU0,
	}
	x := designVector(spec)
	res := e.evalFromMetrics(m, x)

	if got := res.Terms[TermField]; got != 0 {
		t.Errorf("terms[field] = %v, want exactly 0 (B_mid == B_ref)", got)
	}
	if want := math.Log10(mirrorRatio / spec.MirrorRef); res.Terms[TermMirror] != want {
		t.Errorf("terms[mirror] = %v, want %v", res.Terms[TermMirror], want)
	}
	if math.Abs(res.Terms[TermMirror]-0.2475296965206261) > 1e-12 {
		t.Errorf("terms[mirror] = %v, want the reference value 0.2475296965206261", res.Terms[TermMirror])
	}
	if got := res.Terms[TermVolume]; got != 0.7808857808857809 {
		t.Errorf("terms[volume] = %v, want 0.7808857808857809", got)
	}
	if got := res.Terms[TermRipple]; got != 0 {
		t.Errorf("terms[ripple] = %v, want 0", got)
	}
	if got := res.Terms[TermCost]; got != 1.0 {
		t.Errorf("terms[cost] = %v, want exactly 1 (cost == cost_ref)", got)
	}

	w := spec.Weights
	checks := []struct {
		key  string
		want float64
	}{
		{TermField, w.Field * res.Terms[TermField]},
		{TermMirror, w.Mirror * res.Terms[TermMirror]},
		{TermVolume, w.Volume * res.Terms[TermVolume]},
		{TermRipple, -w.Ripple * res.Terms[TermRipple]},
		{TermCost, -w.Cost * res.Terms[TermCost]},
	}
	for _, c := range checks {
		if got := res.Weighted[c.key]; got != c.want {
			t.Errorf("weighted[%s] = %v, want %v", c.key, got, c.want)
		}
	}
	if math.Abs(res.Weighted[TermVolume]-0.5856643356643356) > 1e-12 {
		t.Errorf("weighted[volume] = %v, want the reference value 0.5856643356643356", res.Weighted[TermVolume])
	}
	if got := res.Weighted[TermCost]; got != -1.0 {
		t.Errorf("weighted[cost] = %v, want -1", got)
	}
	// 是 -0.0, 不是 +0.0: ripple 的贡献取了负号, 而 Python
	// reference 往 testdata/golden_baseline.json 里写的是 "-0.0"。保持这个
	// 符号可以让两个实现可按字节比较。
	if v := res.Weighted[TermRipple]; v != 0 || !math.Signbit(v) {
		t.Errorf("weighted[ripple] = %v, want -0.0 (sign below zero)", v)
	}

	for _, k := range frozenPenaltyKeys {
		if got := res.Penalties[k]; got != 0 {
			t.Errorf("penalties[%s] = %v, want exactly 0 for a clean mirror", k, got)
		}
	}
	if !res.Feasible {
		t.Error("clean mirror reported infeasible")
	}
	if res.Metrics != m {
		t.Errorf("metrics were not passed through: got %+v want %+v", res.Metrics, m)
	}
	if len(res.Design) != len(x) {
		t.Errorf("design length = %d, want %d", len(res.Design), len(x))
	}
}

// --------------------------------------------------------- penalty 分支 --

// TestPenaltyBranches 为每条约束构造一个设计, 每个都刻意
// 触发恰好那条约束, 并检查残差、feasibility 标志
// 以及得分保持有限。
func TestPenaltyBranches(t *testing.T) {
	spec := config.DefaultSpec()
	e := evalFixture(t, spec, 1.0e12)

	t.Run("conductor_field", func(t *testing.T) {
		m := cleanMirror()
		m.BCoilMaxT = 15.0 // spec.CoilFieldLimit = 12 T
		res := e.evalFromMetrics(m, designVector(spec))

		want := 15.0/12.0 - 1.0
		if got := res.Penalties[PenConductorField]; math.Abs(got-want) > 1e-12 {
			t.Errorf("penalties[conductor_field] = %v, want %v", got, want)
		}
		if res.Penalties[PenCoilSeparation] != 0 || res.Penalties[PenNotAMirror] != 0 {
			t.Errorf("only the conductor field should violate: %v", res.Penalties)
		}
		if res.Feasible {
			t.Error("a conductor at 15 T must be infeasible")
		}
		requireFinite(t, res.Score)
	})

	t.Run("coil_separation", func(t *testing.T) {
		m := cleanMirror()
		m.MinCoilGapM = 0.01 // spec.MinCoilSep = 0.05 m
		res := e.evalFromMetrics(m, designVector(spec))

		want := (0.05 - 0.01) / 0.05
		if got := res.Penalties[PenCoilSeparation]; math.Abs(got-want) > 1e-12 {
			t.Errorf("penalties[coil_separation] = %v, want %v", got, want)
		}
		if res.Penalties[PenConductorField] != 0 || res.Penalties[PenNotAMirror] != 0 {
			t.Errorf("only the coil separation should violate: %v", res.Penalties)
		}
		if res.Feasible {
			t.Error("coils 1 cm apart must be infeasible")
		}
		requireFinite(t, res.Score)
	})

	t.Run("not_a_mirror", func(t *testing.T) {
		m := cleanMirror()
		m.MirrorRatio = 1.0 // 均匀场: R < MirrorMin = 1.1
		res := e.evalFromMetrics(m, designVector(spec))

		want := (MirrorMin - 1.0) / MirrorMin
		if got := res.Penalties[PenNotAMirror]; math.Abs(got-want) > 1e-12 {
			t.Errorf("penalties[not_a_mirror] = %v, want %v", got, want)
		}
		if res.Penalties[PenConductorField] != 0 || res.Penalties[PenCoilSeparation] != 0 {
			t.Errorf("only the mirror ratio should violate: %v", res.Penalties)
		}
		if res.Feasible {
			t.Error("a device with no mirror ratio must be infeasible")
		}
		// 在下限之上: log term 直接用 R 本身。
		if want := math.Log10(1.0 / spec.MirrorRef); res.Terms[TermMirror] != want {
			t.Errorf("terms[mirror] = %v, want %v", res.Terms[TermMirror], want)
		}
		requireFinite(t, res.Score)
	})

	t.Run("mirror_ratio_at_zero_hits_the_floor", func(t *testing.T) {
		m := cleanMirror()
		m.MirrorRatio = 0.0 // 不能变成 log10(0) = -Inf
		res := e.evalFromMetrics(m, designVector(spec))

		if want := math.Log10(MirrorFloor / spec.MirrorRef); res.Terms[TermMirror] != want {
			t.Errorf("terms[mirror] = %v, want the floored value %v", res.Terms[TermMirror], want)
		}
		if got := res.Penalties[PenNotAMirror]; got != 1.0 {
			t.Errorf("penalties[not_a_mirror] = %v, want exactly 1 at R = 0", got)
		}
		requireFinite(t, res.Score)
	})

	t.Run("empty_field_and_single_coil_stay_finite", func(t *testing.T) {
		m := physics.Metrics{ // 完全没有 field, 一个线圈, 无成本
			BMidT:         0.0,
			MirrorRatio:   0.0,
			BCoilMaxT:     0.0,
			MinCoilGapM:   math.Inf(1), // 线圈数 < 2 时 physics.MinCoilGap 返回 +Inf
			MinClearanceM: 0.25,
			CostProxy:     0.0,
			NCoils:        1,
			MU0:           config.MU0,
		}
		res := e.evalFromMetrics(m, designVector(spec))

		for _, k := range frozenTermKeys {
			requireFinite(t, res.Terms[k])
		}
		requireFinite(t, res.Weighted[TermField])
		requireFinite(t, res.Weighted[TermCost])
		requireFinite(t, res.Penalties[PenCoilSeparation])
		if got := res.Penalties[PenCoilSeparation]; got != 0 {
			t.Errorf("penalties[coil_separation] = %v, want 0 for an unconstrained single coil", got)
		}
		if want := math.Log10(1e-9); math.Abs(res.Terms[TermField]-want) > 1e-12 {
			t.Errorf("terms[field] = %v, want the floored log10(1e-9/1) = %v", res.Terms[TermField], want)
		}
		requireFinite(t, res.Score)
	})
}

// -------------------------------------------- score == 它自己的组成部分 ---

// TestScoreIsExactlyTheSumOfItsParts 是防漂移门禁: composite 必须
// 能仅凭与它并存的 weighted term 与残差逐位复现。
// 这里 "差不多" 不够好 —— 一个得分无法从自身 term
// 重新推导出来的 registry 是没人能审计的 registry。
func TestScoreIsExactlyTheSumOfItsParts(t *testing.T) {
	spec := config.DefaultSpec()
	e := evalFixture(t, spec, 1.0e12)

	scenarios := map[string]physics.Metrics{}
	scenarios["clean_mirror"] = cleanMirror()

	all3 := cleanMirror()
	all3.BMidT = 0.05
	all3.MirrorRatio = 0.9
	all3.VolumeGood = 0.2
	all3.Ripple = 0.3
	all3.BCoilMaxT = 15.0
	all3.MinCoilGapM = 0.01
	all3.CostProxy = 2.0e12
	scenarios["all_three_violated"] = all3

	zero := physics.Metrics{MinCoilGapM: math.Inf(1), MinClearanceM: 0.25}
	scenarios["degenerate_zero"] = zero

	absurd := cleanMirror()
	absurd.CostProxy = 1e300
	absurd.BCoilMaxT = 1e12
	absurd.MirrorRatio = 0.0
	scenarios["absurd_but_finite"] = absurd

	scenarios["golden_metrics"] = loadGoldenBaseline(t).metrics()

	for name, m := range scenarios {
		res := e.evalFromMetrics(m, designVector(spec))

		// key 集合是冻结的: registry schema 与 Python reference 读的
		// 就是这些名字。
		if len(res.Terms) != len(frozenTermKeys) {
			t.Fatalf("%s: %d term keys, want %d", name, len(res.Terms), len(frozenTermKeys))
		}
		for _, k := range frozenTermKeys {
			if _, ok := res.Terms[k]; !ok {
				t.Errorf("%s: missing term key %q", name, k)
			}
		}
		if len(res.Penalties) != len(frozenPenaltyKeys) {
			t.Fatalf("%s: %d penalty keys, want %d", name, len(res.Penalties), len(frozenPenaltyKeys))
		}
		for _, k := range frozenPenaltyKeys {
			if _, ok := res.Penalties[k]; !ok {
				t.Errorf("%s: missing penalty key %q", name, k)
			}
		}

		// weighted == weight * term(含符号), 精确成立。
		w := spec.Weights
		exact := map[string]float64{
			TermField:  w.Field * res.Terms[TermField],
			TermMirror: w.Mirror * res.Terms[TermMirror],
			TermVolume: w.Volume * res.Terms[TermVolume],
			TermRipple: -w.Ripple * res.Terms[TermRipple],
			TermCost:   -w.Cost * res.Terms[TermCost],
		}
		for _, k := range frozenTermKeys {
			if got := res.Weighted[k]; got != exact[k] {
				t.Errorf("%s: weighted[%s] = %v, want %v (weight * term)", name, k, got, exact[k])
			}
		}

		// 分解本身, 按文档化的顺序累加。
		sumWeighted := 0.0
		for _, k := range frozenTermKeys {
			sumWeighted += res.Weighted[k]
		}
		sumPenalties := 0.0
		for _, k := range frozenPenaltyKeys {
			sumPenalties += res.Penalties[k]
		}
		want := sumWeighted - spec.Weights.Penalty*sumPenalties
		if res.Score != want {
			t.Errorf("%s: score = %v, want exactly %v (Σweighted - w_penalty·Σpenalties; diff %g)",
				name, res.Score, want, res.Score-want)
		}
		requireFinite(t, res.Score)
	}
}

// ------------------------------------------------------- golden 锚点 -------

// TestGoldenScoreReproducedFromFrozenMetrics 用跨语言锚点检验计分律:
// 把 testdata/golden_baseline.json 自己的 metrics 与
// cost reference 喂给 objective, 得分必须原样吐出
// -0.2905708161。这部分完全不需要 physics, 所以今天就是绿的;
// 真实 field 路径给出的同一个数字位于 internal/baseline
// (TestTextbookMirrorMatchesGolden)。
func TestGoldenScoreReproducedFromFrozenMetrics(t *testing.T) {
	g := loadGoldenBaseline(t)
	spec := config.DefaultSpec()
	e := evalFixture(t, spec, g.CostRef)

	// 冻结产物就是它自称的东西(被篡改的 testdata/ 不能
	// 在这里伪造出一次绿跑)。
	if math.Abs(g.Score-(-0.2905708160753513)) > 1e-12 {
		t.Fatalf("testdata/golden_baseline.json score = %.16g, want -0.2905708160753513", g.Score)
	}
	if math.Abs(g.CostRef-1.791703035e12) > 1e6 {
		t.Fatalf("testdata/golden_baseline.json cost_ref = %.16g, want 1.791703035e12", g.CostRef)
	}
	if len(g.Design) != spec.NParams() {
		t.Fatalf("golden design has %d entries, want %d", len(g.Design), spec.NParams())
	}
	if got := g.Design[8]; math.Abs(got-1621279.2385890577) > 1e-9*1621279.2385890577 {
		t.Fatalf("golden design[8] (throat current) = %.16g, want 1621279.2385890577", got)
	}
	if got := g.Design[9]; math.Abs(got-463222.63959687366) > 1e-9*463222.63959687366 {
		t.Fatalf("golden design[9] (cell current) = %.16g, want 463222.63959687366", got)
	}

	res := e.evalFromMetrics(g.metrics(), g.Design)

	if math.Abs(res.Score-g.Score) > 1e-12 {
		t.Errorf("score = %.16g, want the frozen %.16g", res.Score, g.Score)
	}
	if math.Abs(res.Score-(-0.2905708161)) > 1e-6 {
		t.Errorf("score = %.16g, want -0.2905708161 +- 1e-6", res.Score)
	}
	for _, k := range frozenTermKeys {
		if d := math.Abs(res.Terms[k] - g.Terms[k]); d > 1e-12 {
			t.Errorf("terms[%s] = %.16g, want the frozen %.16g", k, res.Terms[k], g.Terms[k])
		}
		if d := math.Abs(res.Weighted[k] - g.Weighted[k]); d > 1e-12 {
			t.Errorf("weighted[%s] = %.16g, want the frozen %.16g", k, res.Weighted[k], g.Weighted[k])
		}
	}
	for _, k := range frozenPenaltyKeys {
		if res.Penalties[k] != g.Penalties[k] {
			t.Errorf("penalties[%s] = %v, want the frozen %v", k, res.Penalties[k], g.Penalties[k])
		}
	}
	if !res.Feasible {
		t.Error("the human baseline must be feasible")
	}
	if !g.Feasible {
		t.Error("testdata/golden_baseline.json says the human baseline is infeasible")
	}
	t.Logf("frozen metrics -> score=%.16g (golden %.16g, diff %.3g); terms field=%.6g mirror=%.6g volume=%.6g cost=%.6g",
		res.Score, g.Score, res.Score-g.Score,
		res.Terms["field"], res.Terms["mirror"], res.Terms["volume"], res.Terms["cost"])
	if v := res.Weighted[TermRipple]; !math.Signbit(v) || v != 0 {
		t.Errorf("weighted[ripple] = %v, want the reference's -0.0 (sign parity)", v)
	}
}

// ------------------------------------------------------------ 并发 ----

// TestConcurrentAlgebraMatchesSerial 是 Evaluator 并发契约的 -race
// 那一半(api.go: "safe for concurrent use"): 共享的 Evaluator
// 在被许多 goroutine 猛打时必须产出与串行相同的结果。
// 完整的 Evaluate 路径在 evaluate_fullpath_test.go 里做 race。
func TestConcurrentAlgebraMatchesSerial(t *testing.T) {
	spec := config.DefaultSpec()
	e := evalFixture(t, spec, 1.0e12)

	scenarios := []physics.Metrics{cleanMirror()}
	all3 := cleanMirror()
	all3.MirrorRatio = 0.9
	all3.BCoilMaxT = 15.0
	all3.MinCoilGapM = 0.01
	scenarios = append(scenarios, all3)
	scenarios = append(scenarios, physics.Metrics{MinCoilGapM: math.Inf(1), MinClearanceM: 0.25})

	designs := make([][]float64, len(scenarios))
	serial := make([]EvalResult, len(scenarios))
	for i, m := range scenarios {
		designs[i] = designVector(spec)
		serial[i] = e.evalFromMetrics(m, designs[i])
	}

	const goroutines = 16
	const reps = 40
	got := make([][]EvalResult, goroutines)
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			row := make([]EvalResult, len(scenarios))
			for r := 0; r < reps; r++ {
				for i, m := range scenarios {
					row[i] = e.evalFromMetrics(m, designs[i])
				}
			}
			got[g] = row
		}(g)
	}
	wg.Wait()

	for g, row := range got {
		for i, res := range row {
			want := serial[i]
			if res.Score != want.Score {
				t.Errorf("goroutine %d scenario %d: score %v, serial %v", g, i, res.Score, want.Score)
			}
			if res.Feasible != want.Feasible {
				t.Errorf("goroutine %d scenario %d: feasible %v, serial %v", g, i, res.Feasible, want.Feasible)
			}
			for _, k := range frozenTermKeys {
				if res.Terms[k] != want.Terms[k] || res.Weighted[k] != want.Weighted[k] {
					t.Errorf("goroutine %d scenario %d: term %s = %v/%v, serial %v/%v",
						g, i, k, res.Terms[k], res.Weighted[k], want.Terms[k], want.Weighted[k])
				}
			}
			for _, k := range frozenPenaltyKeys {
				if res.Penalties[k] != want.Penalties[k] {
					t.Errorf("goroutine %d scenario %d: penalty %s = %v, serial %v", g, i, k, res.Penalties[k], want.Penalties[k])
				}
			}
			if res.Metrics != want.Metrics {
				t.Errorf("goroutine %d scenario %d: metrics drifted", g, i)
			}
		}
	}
}

// TestEvaluatePanicsOnWrongLengthDesign 钉住 "不做静默缩放" 规则:
// Evaluate 没有 error 返回, 所以长度不对的向量必须大声失败,
// 而不是被补零或截断(那会给一台没人提出过的机器打分)。
func TestEvaluatePanicsOnWrongLengthDesign(t *testing.T) {
	spec := config.DefaultSpec()
	e := evalFixture(t, spec, 1.0e12)

	for _, n := range []int{0, spec.NParams() - 1, spec.NParams() + 1} {
		func() {
			defer func() {
				if r := recover(); r == nil {
					t.Errorf("Evaluate on a %d-entry vector (want %d) did not panic", n, spec.NParams())
				}
			}()
			e.Evaluate(make([]float64, n))
		}()
	}
}

// ------------------------------------------------------------- golden 文件 ---

// goldenBaseline 镜像 testdata/golden_baseline.json。
//
// metrics 刻意解码成 float 的 map: 该文件把 bool 与
// int 槽位(coil_proximity_floor_hit、n_coils)存成 JSON float, 所以它无法
// 直接解码进 physics.Metrics(已在 stage-B 报告中
// 上报主线)。Coil 可以直接解码, 因为 physics.Coil 的 tag 就是该文件的 key。
type goldenBaseline struct {
	Name      string             `json:"name"`
	Note      string             `json:"note"`
	Design    []float64          `json:"design"`
	Coils     []physics.Coil     `json:"coils"`
	CostProxy float64            `json:"cost_proxy"`
	CostRef   float64            `json:"cost_ref"`
	Score     float64            `json:"score"`
	Feasible  bool               `json:"feasible"`
	Terms     map[string]float64 `json:"terms"`
	Weighted  map[string]float64 `json:"weighted"`
	Penalties map[string]float64 `json:"penalties"`
	Metrics   map[string]float64 `json:"metrics"`
}

func (g goldenBaseline) metrics() physics.Metrics {
	m := physics.Metrics{
		BMidT:                 g.Metrics["B_mid_T"],
		BThroatT:              g.Metrics["B_throat_T"],
		ZThroatM:              g.Metrics["z_throat_m"],
		MirrorRatio:           g.Metrics["mirror_ratio"],
		VolumeGood:            g.Metrics["volume_good"],
		Ripple:                g.Metrics["ripple"],
		BCoilMaxT:             g.Metrics["B_coil_max_T"],
		MinCoilGapM:           g.Metrics["min_coil_gap_m"],
		MinClearanceM:         g.Metrics["min_clearance_m"],
		CostProxy:             g.Metrics["cost_proxy"],
		CoilProximityFloorHit: g.Metrics["coil_proximity_floor_hit"] != 0,
		NCoils:                int(g.Metrics["n_coils"]),
		MU0:                   g.Metrics["mu0"],
	}
	return m
}

func loadGoldenBaseline(t *testing.T) goldenBaseline {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "golden_baseline.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var g goldenBaseline
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return g
}

// ----------------------------------------------------------------- 辅助函数 ---

func requireFinite(t *testing.T, v float64) {
	t.Helper()
	if math.IsNaN(v) || math.IsInf(v, 0) {
		t.Errorf("got %v, want a finite number", v)
	}
}
