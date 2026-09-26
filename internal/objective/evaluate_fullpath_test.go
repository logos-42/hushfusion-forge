// objective.Evaluate 的全路径测试: design 向量 -> physics metrics ->
// score。这些测试需要 stage A(internal/physics)实现完成, 所以目前
// 被搁置, 会以一条精确的 "blocked on stage A" 消息失败,
// 而不是让测试二进制崩溃。不要删除或跳过它们: 本文件是
// 跨线门禁, 用来保证 objective 的代数部分吃的是真实 metric
// 定义(CONTRACT.md §4), 而 golden 端到端数字位于
// internal/baseline/golden_test.go。
//
// stub 返回的字段刻意做成可手算的, 因此下面的期望值是从文档化的
// metric 定义推导出来的, 而不是从 physics 核心
// 本身得来的。
package objective

import (
	"fmt"
	"math"
	"sync"
	"testing"

	"github.com/logos-42/hushfusion-forge/internal/config"
	"github.com/logos-42/hushfusion-forge/internal/physics"
)

// gradientSolver 返回 |B| = b0 + slope*|z|: 一个单峰的轴上剖面,
// 因此由构造可知 mirror ratio > 1, 且每个 metric 都是这两个常量的
// 闭合形式函数。
type gradientSolver struct{ b0, slope float64 }

func (s gradientSolver) Name() string { return "stub_gradient" }

func (s gradientSolver) Magnitude(coils []physics.Coil, r, z []float64) []float64 {
	n := len(r)
	if len(z) < n {
		n = len(z)
	}
	out := make([]float64, n)
	for i := range out {
		out[i] = s.b0 + s.slope*math.Abs(z[i])
	}
	return out
}

// fourCoil 构造一个 canonical(z 升序)、位于盒内的四等线圈设计。
// 每个电流与半径都相同, 所以欧姆成本代理为
// 4 * I^2 * r = 4e11 [A^2 m], 测试可以传 costRef = 4e11 把
// cost term 恰好放在 1.0。
func fourCoil(z0, z1, z2, z3 float64) []float64 {
	const (
		radius  = 0.4
		current = 5.0e5
	)
	return []float64{
		radius, radius, radius, radius,
		z0, z1, z2, z3,
		current, current, current, current,
	}
}

const fourCoilCostRef = 4.0e11

// probePhysics 用一个良性的、彼此分离良好的设计调用 stage B 依赖的
// 每一个 physics 入口, 并返回核心仍然抛出的 panic(stage A 落地后
// 为 nil)。
func probePhysics() (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v", r)
		}
	}()
	spec := config.DefaultSpec()
	coils, cerr := physics.VectorToCoils(fourCoil(-1.0, -0.3, 0.3, 1.0), spec)
	if cerr != nil {
		return cerr
	}
	grids := physics.BuildGrids(spec)
	if len(grids.StackR) == 0 || len(grids.StackR) != len(grids.StackZ) {
		return fmt.Errorf("physics.BuildGrids returned an empty or ragged sample set (%d radii / %d z)",
			len(grids.StackR), len(grids.StackZ))
	}
	_ = physics.MetricsFor(coils, spec, grids, stubSolver{mag: 1.0})
	_ = physics.MinCoilGap(coils)
	_ = physics.AxisRipple([]float64{1, 2, 1, 2, 1}, 1.0, 0.05)
	_ = physics.OnAxisField(coils, []float64{0.0, 0.5})
	return nil
}

func requirePhysics(t *testing.T) {
	t.Helper()
	if err := probePhysics(); err != nil {
		t.Fatalf("BLOCKED on stage A: internal/physics is not implemented yet (%v). "+
			"This test is parked deliberately; it goes green when the physics core lands. "+
			"It must not be deleted or skipped — it is the gate that the evaluator is wired to the "+
			"real metric definitions.", err)
	}
}

// TestEvaluateFullPathHandComputed 用假 field 驱动 Evaluate 走真实的
// metric 计算, 并把结果与由文档化定义手工推导出的
// 值对照。每个子测试都构造得恰好违反一条
// 约束(或一条都不违反), 这正是让 penalty 分支
// 归因不含糊的原因。
func TestEvaluateFullPathHandComputed(t *testing.T) {
	requirePhysics(t)
	spec := config.DefaultSpec()
	grids := physics.BuildGrids(spec)

	t.Run("uniform_field_is_not_a_mirror", func(t *testing.T) {
		e := NewEvaluator(spec, stubSolver{mag: 2.0}, fourCoilCostRef, grids)
		x := fourCoil(-1.0, -0.3, 0.3, 1.0)
		res := e.Evaluate(x)

		// 均匀 2 T: B_mid = B_throat = 2 T, R = 1。
		if math.Abs(res.Metrics.BMidT-2.0) > 1e-12 {
			t.Errorf("B_mid = %v, want 2 (constant field)", res.Metrics.BMidT)
		}
		if math.Abs(res.Metrics.MirrorRatio-1.0) > 1e-12 {
			t.Errorf("mirror ratio = %v, want 1 (constant field)", res.Metrics.MirrorRatio)
		}
		if want := math.Log10(2.0 / spec.BRef); math.Abs(res.Terms[TermField]-want) > 1e-12 {
			t.Errorf("terms[field] = %v, want %v", res.Terms[TermField], want)
		}
		if want := math.Log10(1.0 / spec.MirrorRef); math.Abs(res.Terms[TermMirror]-want) > 1e-12 {
			t.Errorf("terms[mirror] = %v, want %v", res.Terms[TermMirror], want)
		}
		// 在每个采样点都有 2 T <= confine_factor * B_mid = 2.5 T, 而常值
		// 剖面没有内部极值, 所以没有 ripple。
		if res.Terms[TermVolume] != 1.0 {
			t.Errorf("terms[volume] = %v, want 1 (2 T <= 1.25*2 T everywhere)", res.Terms[TermVolume])
		}
		if res.Terms[TermRipple] != 0 {
			t.Errorf("terms[ripple] = %v, want 0 for a constant profile", res.Terms[TermRipple])
		}
		if math.Abs(res.Terms[TermCost]-1.0) > 1e-12 {
			t.Errorf("terms[cost] = %v, want 1 (cost == cost_ref)", res.Terms[TermCost])
		}
		// 来自其他线圈的 3 * 2 T + 绕线包自场。
		if got, want := res.Metrics.BCoilMaxT, 3*2.0+spec.SelfField(); math.Abs(got-want) > 1e-9*want {
			t.Errorf("B_coil_max = %v, want %v (3 other coils at 2 T + self field)", got, want)
		}
		if got := res.Penalties[PenConductorField]; got != 0 {
			t.Errorf("penalties[conductor_field] = %v, want 0", got)
		}
		if got := res.Penalties[PenCoilSeparation]; got != 0 {
			t.Errorf("penalties[coil_separation] = %v, want 0 (coils 0.6 m apart)", got)
		}
		if got, want := res.Penalties[PenNotAMirror], (MirrorMin-1.0)/MirrorMin; math.Abs(got-want) > 1e-12 {
			t.Errorf("penalties[not_a_mirror] = %v, want %v", got, want)
		}
		if res.Feasible {
			t.Error("a uniform field must be infeasible")
		}
		requireFinite(t, res.Score)
		requireSameDesign(t, res.Design, x)
	})

	t.Run("gradient_field_overloads_the_conductor", func(t *testing.T) {
		e := NewEvaluator(spec, gradientSolver{b0: 2.0, slope: 5.0}, fourCoilCostRef, grids)
		x := fourCoil(-1.0, -0.3, 0.3, 1.0)
		res := e.Evaluate(x)

		// 中平面体积: 在 z = linspace(-0.15, 0.15, 5) 上 |B| = 2 + 5|z|, 所以
		// 均值为 2 + 5*(0.15+0.075+0+0.075+0.15)/5 = 2.45 T。
		if math.Abs(res.Metrics.BMidT-2.45) > 1e-12 {
			t.Errorf("B_mid = %v, want 2.45", res.Metrics.BMidT)
		}
		// Throat: 轴上最大值在 |z| = z_axis_max = 1.4 处 -> 2 + 5*1.4 = 9 T。
		if math.Abs(res.Metrics.BThroatT-9.0) > 1e-12 {
			t.Errorf("B_throat = %v, want 9", res.Metrics.BThroatT)
		}
		if math.Abs(res.Metrics.MirrorRatio-9.0/2.45) > 1e-12 {
			t.Errorf("mirror ratio = %v, want %v", res.Metrics.MirrorRatio, 9.0/2.45)
		}
		// Cell 采样点: 2 + 5|z| <= 1.25*2.45 = 3.0625 -> |z| <= 0.2125, 即 13 条半径每条
		// 33 个轴向采样点中的 9 个。
		if got, want := res.Terms[TermVolume], 117.0/429.0; math.Abs(got-want) > 1e-9 {
			t.Errorf("terms[volume] = %v, want %v (9 of 33 axial samples x 13 radii)", got, want)
		}
		// 最外侧线圈从其邻居看到 26 T((2+3.5)+(2+6.5)+(2+10))
		// 再加上自场: 远超 12 T 的上限。
		wantB := 26.0 + spec.SelfField()
		if math.Abs(res.Metrics.BCoilMaxT-wantB) > 1e-9*wantB {
			t.Errorf("B_coil_max = %v, want %v (Σ over the other coils at their |Δz| + self field)", res.Metrics.BCoilMaxT, wantB)
		}
		if got := res.Penalties[PenConductorField]; got <= 0 {
			t.Errorf("penalties[conductor_field] = %v, want > 0", got)
		} else if want := wantB/spec.CoilFieldLimit - 1.0; math.Abs(got-want) > 1e-9*want {
			t.Errorf("penalties[conductor_field] = %v, want %v", got, want)
		}
		if got := res.Penalties[PenNotAMirror]; got != 0 {
			t.Errorf("penalties[not_a_mirror] = %v, want 0 (R = 3.67 > MirrorMin)", got)
		}
		if got := res.Penalties[PenCoilSeparation]; got != 0 {
			t.Errorf("penalties[coil_separation] = %v, want 0 (coils 0.6 m apart)", got)
		}
		if res.Feasible {
			t.Error("29 T on the conductor must be infeasible")
		}
		requireFinite(t, res.Score)
	})

	t.Run("near_coincident_coils_violate_separation", func(t *testing.T) {
		e := NewEvaluator(spec, gradientSolver{b0: 2.0, slope: 0.4}, fourCoilCostRef, grids)
		x := fourCoil(-0.35, -0.34, 0.3, 1.0)
		res := e.Evaluate(x)

		if got, want := res.Metrics.MinCoilGapM, 0.01; math.Abs(got-want) > 1e-12 {
			t.Errorf("min coil gap = %v, want %v", got, want)
		}
		if got, want := res.Penalties[PenCoilSeparation], (spec.MinCoilSep-0.01)/spec.MinCoilSep; math.Abs(got-want) > 1e-12 {
			t.Errorf("penalties[coil_separation] = %v, want %v", got, want)
		}
		// 2 + 0.4|z| 是弱梯度: R = 2.56/2.036 = 1.257 > 1.1, 且
		// 最差 conductor 看到 7.92 + π = 11.06 T < 12 T, 所以被违反的
		// 唯一约束是 separation。
		if got := res.Penalties[PenNotAMirror]; got != 0 {
			t.Errorf("penalties[not_a_mirror] = %v, want 0", got)
		}
		if got := res.Penalties[PenConductorField]; got != 0 {
			t.Errorf("penalties[conductor_field] = %v, want 0", got)
		}
		if res.Metrics.CoilProximityFloorHit {
			t.Error("coil proximity floor reported as hit although the closest pair is 1 cm apart")
		}
		if res.Feasible {
			t.Error("coils 1 cm apart must be infeasible")
		}
		// 在此梯度下每个 cell 采样点都通过 confining 检验。
		if res.Terms[TermVolume] != 1.0 {
			t.Errorf("terms[volume] = %v, want 1", res.Terms[TermVolume])
		}
		requireFinite(t, res.Score)
	})

	t.Run("feasible_when_nothing_is_violated", func(t *testing.T) {
		e := NewEvaluator(spec, gradientSolver{b0: 2.0, slope: 0.4}, fourCoilCostRef, grids)
		x := fourCoil(-1.2, -0.4, 0.4, 1.2)
		res := e.Evaluate(x)

		for _, k := range frozenPenaltyKeys {
			if res.Penalties[k] != 0 {
				t.Errorf("penalties[%s] = %v, want 0", k, res.Penalties[k])
			}
		}
		if !res.Feasible {
			t.Error("a design with no violated constraint reported infeasible")
		}
		if res.Metrics.MirrorRatio <= MirrorMin {
			t.Errorf("mirror ratio = %v, want > %v", res.Metrics.MirrorRatio, MirrorMin)
		}
		requireFinite(t, res.Score)
	})
}

// TestEvaluateConcurrentFullPathMatchesSerial 是对冻结的并发契约在真实代码路径
// (design 解码 + metrics + score)上做的 -race 检查:
// 许多 goroutine 共享一个 Evaluator 必须得到与串行完全相同的结果。
func TestEvaluateConcurrentFullPathMatchesSerial(t *testing.T) {
	requirePhysics(t)
	spec := config.DefaultSpec()
	grids := physics.BuildGrids(spec)
	e := NewEvaluator(spec, gradientSolver{b0: 2.0, slope: 0.4}, fourCoilCostRef, grids)

	designs := [][]float64{
		fourCoil(-1.0, -0.3, 0.3, 1.0),
		fourCoil(-0.35, -0.34, 0.3, 1.0),
		fourCoil(-1.2, -0.4, 0.4, 1.2),
	}
	serial := make([]EvalResult, len(designs))
	for i, x := range designs {
		serial[i] = e.Evaluate(x)
	}

	const goroutines, reps = 16, 20
	got := make([][]EvalResult, goroutines)
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			row := make([]EvalResult, len(designs))
			for r := 0; r < reps; r++ {
				for i, x := range designs {
					row[i] = e.Evaluate(x)
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
				t.Errorf("goroutine %d design %d: score %v, serial %v", g, i, res.Score, want.Score)
			}
			if res.Feasible != want.Feasible || res.Metrics != want.Metrics {
				t.Errorf("goroutine %d design %d: feasible/metrics drifted from the serial result", g, i)
			}
			for _, k := range frozenTermKeys {
				if res.Terms[k] != want.Terms[k] || res.Weighted[k] != want.Weighted[k] {
					t.Errorf("goroutine %d design %d: term %s drifted", g, i, k)
				}
			}
			for _, k := range frozenPenaltyKeys {
				if res.Penalties[k] != want.Penalties[k] {
					t.Errorf("goroutine %d design %d: penalty %s drifted", g, i, k)
				}
			}
			for j := range want.Design {
				if res.Design[j] != want.Design[j] {
					t.Errorf("goroutine %d design %d: design[%d] drifted", g, i, j)
					break
				}
			}
		}
	}
}

// TestScoreWrapperEqualsEvaluate 钉住: 便利包装就是同一次
// 评估, 而不是一个可能与它跑偏的并行实现。
func TestScoreWrapperEqualsEvaluate(t *testing.T) {
	requirePhysics(t)
	spec := config.DefaultSpec()
	grids := physics.BuildGrids(spec)
	e := NewEvaluator(spec, gradientSolver{b0: 2.0, slope: 5.0}, fourCoilCostRef, grids)

	x := fourCoil(-1.0, -0.3, 0.3, 1.0)
	if got, want := e.Score(x), e.Evaluate(x).Score; got != want {
		t.Errorf("Score = %v, Evaluate().Score = %v", got, want)
	}
}

func requireSameDesign(t *testing.T, got, want []float64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("canonical design has %d entries, want %d", len(got), len(want))
	}
	for i := range want {
		if math.Abs(got[i]-want[i]) > 1e-12*math.Max(math.Abs(want[i]), 1e-12) {
			t.Errorf("canonical design[%d] = %v, want %v (an in-box canonical vector must survive decoding untouched)",
				i, got[i], want[i])
		}
	}
}
