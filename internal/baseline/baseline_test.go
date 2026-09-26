// 手工设计 baseline 的测试, 不需要 physics 核心:
// 线圈布局、欧姆成本代理、搜索盒审计、包围
// 求解器, 以及对 testdata/golden_baseline.json 的冻结检查。
//
// 确实需要 field solver 的测试(求解出的 cell 电流是否
// 复现 golden 得分 / metrics?)在 golden_test.go 里。
package baseline

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/logos-42/hushfusion-forge/internal/config"
	"github.com/logos-42/hushfusion-forge/internal/physics"
)

// --------------------------------------------------------------- 测试夹具 ----

// stubSolver 是一个确定性的、不含 physics 的 field: 处处同一个 |B|。
// 只是为了在没有真实 solver 的情况下让 probePhysics() 能调用
// physics.MetricsFor(见 stage A 里的 evaluate 风格测试); 它本身不算任何 physics。
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

// probePhysics 调用 stage B 依赖的每一个 physics 入口, 并返回
// 核心仍然抛出的 panic(stage A 落地后为 nil)。它存在的意义是让
// "尚未实现" 变成一次精确的测试失败, 而不是一个未恢复的
// panic —— 那会把整个测试二进制拖垮并掩盖其他结果。
func probePhysics() (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v", r)
		}
	}()
	spec := config.DefaultSpec()
	x := []float64{0.4, 0.4, 0.4, 0.4, -1.0, -0.3, 0.3, 1.0, 5.0e5, 5.0e5, 5.0e5, 5.0e5}
	coils, cerr := physics.VectorToCoils(x, spec)
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
	_ = physics.CoilsToVector(coils)
	return nil
}

func requirePhysics(t *testing.T) {
	t.Helper()
	if err := probePhysics(); err != nil {
		t.Fatalf("BLOCKED on stage A: internal/physics is not implemented yet (%v). "+
			"This test is parked deliberately; it goes green when the physics core lands. "+
			"It must not be deleted or skipped — it is the cross-line gate G6/§5 anchor.", err)
	}
}

// ------------------------------------------------------------- golden 文件 ---

// goldenBaseline 镜像 testdata/golden_baseline.json(冻结的跨语言
// 产物)。metrics 解码成 float 的 map, 因为该文件把
// bool 与 int 槽位存成 JSON float —— Python 生成端给它们 float() 了 —— 所以它
// 无法直接解码进 physics.Metrics。Coil 可以直接解码: 它的 JSON
// tag 恰好就是该文件的 key。
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
	if len(g.Coils) == 0 || len(g.Design) == 0 {
		t.Fatalf("%s carries no coils/design", path)
	}
	return g
}

func relDiff(got, want float64) float64 {
	d := math.Abs(got - want)
	if w := math.Abs(want); w > d {
		return d / w
	}
	return d
}

// withinRelTol 的含义是 "相对差小于 tol, 同时对 (近似)为零的量
// 采用 tol 作为绝对下限": CONTRACT 把 metrics 规定为相对
// 1e-6, 但 ripple 恰好 = 0, 此时纯相对检验是无定义的。
func withinRelTol(got, want, tol float64) bool {
	d := math.Abs(got - want)
	return d <= tol || d <= tol*math.Abs(want)
}

// ---------------------------------------------------------- 线圈布局 ------

func TestHelmholtzPairGeometry(t *testing.T) {
	const (
		radius  = 0.5
		current = 3.0e5
	)
	coils := HelmholtzPair(radius, current)
	if len(coils) != 2 {
		t.Fatalf("got %d coils, want 2", len(coils))
	}
	for i, c := range coils {
		if c.Radius != radius {
			t.Errorf("coil %d radius = %v, want %v", i, c.Radius, radius)
		}
		if c.Current != current {
			t.Errorf("coil %d current = %v, want %v", i, c.Current, current)
		}
	}
	// Helmholtz 条件: 间距等于线圈半径, 以 z = 0 为中心。
	if got, want := coils[0].Z, -radius/2.0; got != want {
		t.Errorf("coil 0 z = %v, want %v", got, want)
	}
	if got, want := coils[1].Z, radius/2.0; got != want {
		t.Errorf("coil 1 z = %v, want %v", got, want)
	}
	if got := coils[1].Z - coils[0].Z; got != radius {
		t.Errorf("separation = %v, want the loop radius %v", got, radius)
	}
}

// TestDefaultGeomMatchesTheFrozenReference 把手写的比例钉在
// testdata/golden_baseline.json 里的几何上, 这样对
// DefaultGeom 的修改就不能静默改变人类 baseline 是哪台机器。
func TestDefaultGeomMatchesTheFrozenReference(t *testing.T) {
	g := loadGoldenBaseline(t)
	geom := DefaultGeom()

	want := Geom{RCell: 0.50, HalfGapCell: 0.25, RThroat: 0.30, ZThroat: 1.00, ThroatCurrentRatio: 3.5}
	if geom != want {
		t.Fatalf("DefaultGeom() = %+v, want %+v", geom, want)
	}
	// 线圈按 (cell, cell, throat, throat) 构造 —— 也就是 reference
	// 写进 golden 文件的顺序。
	wantR := []float64{geom.RCell, geom.RCell, geom.RThroat, geom.RThroat}
	wantZ := []float64{-geom.HalfGapCell, geom.HalfGapCell, -geom.ZThroat, geom.ZThroat}
	if len(g.Coils) != len(wantR) {
		t.Fatalf("golden file has %d coils, want %d", len(g.Coils), len(wantR))
	}
	for i, c := range g.Coils {
		if relDiff(c.Radius, wantR[i]) > 1e-12 {
			t.Errorf("golden coil %d radius = %v, want %v", i, c.Radius, wantR[i])
		}
		if relDiff(c.Z, wantZ[i]) > 1e-12 {
			t.Errorf("golden coil %d z = %v, want %v", i, c.Z, wantZ[i])
		}
	}
}

// TestMirrorCoilsMatchesGoldenGeometry 在 golden cell 电流下摆出 baseline 里
// 与 solver 无关的那部分, 并逐元素地与冻结文件
// 比较(结构精确一致, 电流取自文件自身的值)。
func TestMirrorCoilsMatchesGoldenGeometry(t *testing.T) {
	g := loadGoldenBaseline(t)
	geom := DefaultGeom()
	iCell := g.Design[9] // canonical design = [r..., z..., I...], z 升序

	coils := mirrorCoils(geom, iCell)
	if len(coils) != len(g.Coils) {
		t.Fatalf("got %d coils, want %d", len(coils), len(g.Coils))
	}
	for i := range coils {
		got, want := coils[i], g.Coils[i]
		if relDiff(got.Radius, want.Radius) > 1e-12 {
			t.Errorf("coil %d radius = %v, want %v", i, got.Radius, want.Radius)
		}
		if relDiff(got.Z, want.Z) > 1e-12 {
			t.Errorf("coil %d z = %v, want %v", i, got.Z, want.Z)
		}
		if relDiff(got.Current, want.Current) > 1e-12 {
			t.Errorf("coil %d current = %v, want %v", i, got.Current, want.Current)
		}
	}
	// throat 电流等于 cell 电流乘以冻结的 ratio。
	if got, want := coils[2].Current, geom.ThroatCurrentRatio*iCell; got != want {
		t.Errorf("throat current = %v, want %v", got, want)
	}
	// canonical 向量按 z 排序; golden 文件的 "design" 就是那个向量。
	design := []float64{0.30, 0.50, 0.50, 0.30, -1.00, -0.25, 0.25, 1.00,
		coils[2].Current, coils[0].Current, coils[1].Current, coils[3].Current}
	for i := range design {
		if relDiff(design[i], g.Design[i]) > 1e-12 {
			t.Errorf("design[%d] = %v, want %v", i, design[i], g.Design[i])
		}
	}
}

// TestCostProxyMatchesGoldenCost 检验欧姆成本代理: 闭合形式
// sum_k I_k^2 r_k, 同时对照 golden 数字与它在此几何下的
// 代数形式(2 个相同 cell 线圈 + 2 个在 3.5 倍电流下的相同 throat)。
func TestCostProxyMatchesGoldenCost(t *testing.T) {
	g := loadGoldenBaseline(t)
	geom := DefaultGeom()
	iCell := g.Design[9]

	got := costProxy(mirrorCoils(geom, iCell))
	if relDiff(got, g.CostProxy) > 1e-12 {
		t.Errorf("cost proxy = %v, want the golden %v", got, g.CostProxy)
	}
	// 2*I^2*r_cell + 2*(ratio*I)^2*r_throat
	want := 2.0*iCell*iCell*geom.RCell + 2.0*math.Pow(geom.ThroatCurrentRatio*iCell, 2)*geom.RThroat
	if relDiff(got, want) > 1e-12 {
		t.Errorf("cost proxy = %v, want the algebraic %v", got, want)
	}
	if relDiff(costProxy(g.Coils), g.CostProxy) > 1e-12 {
		t.Errorf("cost proxy of the golden coils = %v, want %v", costProxy(g.Coils), g.CostProxy)
	}
	if got := costProxy(nil); got != 0 {
		t.Errorf("cost proxy of no coils = %v, want 0", got)
	}
}

// ------------------------------------------------------- 搜索盒审计 ----

func TestCheckInsideBoundsRejectsClipping(t *testing.T) {
	spec := config.DefaultSpec()
	g := loadGoldenBaseline(t)

	if err := checkInsideBounds(g.Coils, spec); err != nil {
		t.Fatalf("the human baseline must sit inside the search box: %v", err)
	}
	// 恰好落在边界上是允许的: 裁剪不会移动它。
	edge := []physics.Coil{{Radius: spec.Bounds.Radius[1], Z: spec.Bounds.Z[0], Current: spec.Bounds.Current[1]}}
	if err := checkInsideBounds(edge, spec); err != nil {
		t.Errorf("a coil exactly on the bounds must be accepted: %v", err)
	}

	cases := map[string][]physics.Coil{
		"radius too large": {{Radius: spec.Bounds.Radius[1] + 1e-9, Z: 0, Current: 1e5}},
		"radius too small": {{Radius: spec.Bounds.Radius[0] - 1e-9, Z: 0, Current: 1e5}},
		"z too large":      {{Radius: 0.5, Z: spec.Bounds.Z[1] + 1e-9, Current: 1e5}},
		"z too small":      {{Radius: 0.5, Z: spec.Bounds.Z[0] - 1e-9, Current: 1e5}},
		"current too high": {{Radius: 0.5, Z: 0, Current: spec.Bounds.Current[1] + 1.0}},
		"current too low":  {{Radius: 0.5, Z: 0, Current: spec.Bounds.Current[0] - 1.0}},
		"one of four":      {g.Coils[0], g.Coils[1], {Radius: 1.5, Z: 0.9, Current: 1e5}, g.Coils[3]},
	}
	for name, coils := range cases {
		if err := checkInsideBounds(coils, spec); err == nil {
			t.Errorf("%s: no error reported, but the design would be clipped when re-encoded", name)
		}
	}
}

// ------------------------------------------------- 包围法求根 -----

func TestBisectBracket(t *testing.T) {
	const root = 463222.63959687366

	t.Run("linear_root_inside_the_grid", func(t *testing.T) {
		got, err := bisectBracket(cellCurrentScanLo, cellCurrentScanHi, cellCurrentScanN, cellCurrentRelTol,
			func(i float64) float64 { return i - root })
		if err != nil {
			t.Fatalf("bisectBracket: %v", err)
		}
		if relDiff(got, root) > cellCurrentRelTol {
			t.Errorf("root = %.16g, want %.16g (rel %g)", got, root, relDiff(got, root))
		}
	})

	t.Run("nonlinear_monotone_root", func(t *testing.T) {
		// f 是真实求解的 reference 面: 严格递增、弯曲。
		f := func(i float64) float64 { return math.Pow(i, 1.5) - 2.0e7 }
		want := math.Pow(2.0e7, 1.0/1.5)
		got, err := bisectBracket(cellCurrentScanLo, cellCurrentScanHi, cellCurrentScanN, cellCurrentRelTol, f)
		if err != nil {
			t.Fatalf("bisectBracket: %v", err)
		}
		if relDiff(got, want) > 1e-8 {
			t.Errorf("root = %.16g, want %.16g", got, want)
		}
		// 在返回点上残差必须真的小(二分只有在落在根上
		// 才有用, 仅仅落在包围区间内不算)。
		if res := math.Abs(f(got)); res > 1.0 {
			t.Errorf("residual at the root = %v, want ~0", res)
		}
	})

	t.Run("root_at_the_lower_end", func(t *testing.T) {
		got, err := bisectBracket(cellCurrentScanLo, cellCurrentScanHi, cellCurrentScanN, cellCurrentRelTol,
			func(i float64) float64 { return i - cellCurrentScanLo })
		if err != nil {
			t.Fatalf("bisectBracket: %v", err)
		}
		if got != cellCurrentScanLo {
			t.Errorf("root = %v, want the lower end %v", got, cellCurrentScanLo)
		}
	})

	t.Run("no_sign_change_is_reported", func(t *testing.T) {
		if _, err := bisectBracket(cellCurrentScanLo, cellCurrentScanHi, cellCurrentScanN, cellCurrentRelTol,
			func(i float64) float64 { return i + 1.0 }); err == nil {
			t.Error("a function with no root in the window must be reported, not guessed")
		}
	})

	t.Run("nan_residual_is_reported", func(t *testing.T) {
		calls := 0
		_, err := bisectBracket(cellCurrentScanLo, cellCurrentScanHi, cellCurrentScanN, cellCurrentRelTol,
			func(i float64) float64 {
				calls++
				if calls > 3 {
					return math.NaN()
				}
				return i - root
			})
		if err == nil {
			t.Error("a NaN residual must be reported instead of refined into a fabricated root")
		}
	})

	t.Run("bad_arguments", func(t *testing.T) {
		f := func(i float64) float64 { return i - root }
		for _, c := range []struct {
			name string
			lo   float64
			hi   float64
			n    int
			tol  float64
		}{
			{"lo <= 0", 0, 1e6, 24, 1e-9},
			{"hi <= lo", 1e6, 1e3, 24, 1e-9},
			{"n < 2", 1e3, 5e6, 1, 1e-9},
			{"tol <= 0", 1e3, 5e6, 24, 0},
		} {
			if _, err := bisectBracket(c.lo, c.hi, c.n, c.tol, f); err == nil {
				t.Errorf("%s: no error reported", c.name)
			}
		}
	})
}

// ------------------------------------------------------------- 文件冻结 ---

// TestGoldenBaselineFilePinsTheFrozenAnchors 把 testdata/golden_baseline.json 的
// 头条数字以字面量形式重述一遍。该文件归父线所有且已冻结:
// 如果它被改动, 下面每一个比较(以及 golden 测试里的比较)都会愉快地
// 拿实现去和一个已经移动的目标比, 所以这些锚点在这里
// 以文字加数字的形式一次性钉住。
func TestGoldenBaselineFilePinsTheFrozenAnchors(t *testing.T) {
	g := loadGoldenBaseline(t)

	if g.Name != "textbook_mirror" {
		t.Errorf("name = %q, want %q", g.Name, "textbook_mirror")
	}
	if relDiff(g.Score, -0.2905708160753513) > 1e-12 {
		t.Errorf("score = %.16g, want -0.2905708160753513", g.Score)
	}
	if !g.Feasible {
		t.Error("the human baseline must be feasible")
	}
	for _, c := range []struct {
		name string
		got  float64
		want float64
		tol  float64
	}{
		{"cost_proxy", g.CostProxy, 1.7917030355523e12, 1e-9},
		{"cost_ref", g.CostRef, 1.7917030355523e12, 1e-9},
		{"design[8] throat current", g.Design[8], 1621279.2385890577, 1e-12},
		{"design[9] cell current", g.Design[9], 463222.63959687366, 1e-12},
		{"B_mid_T", g.Metrics["B_mid_T"], 1.0, 1e-9},
		{"B_throat_T", g.Metrics["B_throat_T"], 3.5363862409275857, 1e-9},
		{"mirror_ratio", g.Metrics["mirror_ratio"], 3.536386240927585, 1e-9},
		{"volume_good", g.Metrics["volume_good"], 0.7808857808857809, 1e-9},
		{"B_coil_max_T", g.Metrics["B_coil_max_T"], 3.416271368796406, 1e-9},
		{"min_coil_gap_m", g.Metrics["min_coil_gap_m"], 0.5, 1e-12},
		{"z_throat_m", g.Metrics["z_throat_m"], -0.9975, 1e-9},
		{"ripple", g.Metrics["ripple"], 0.0, 1e-12},
		{"terms[field]", g.Terms["field"], 0.0, 1e-9},
		{"terms[mirror]", g.Terms["mirror"], 0.2475296965206261, 1e-9},
		{"terms[volume]", g.Terms["volume"], 0.7808857808857809, 1e-9},
		{"terms[ripple]", g.Terms["ripple"], 0.0, 1e-12},
		{"terms[cost]", g.Terms["cost"], 1.0, 1e-12},
	} {
		if relDiff(c.got, c.want) > c.tol {
			t.Errorf("%s = %.16g, want %.16g (rel %g)", c.name, c.got, c.want, relDiff(c.got, c.want))
		}
	}
	for _, k := range []string{"conductor_field", "coil_separation", "not_a_mirror"} {
		if g.Penalties[k] != 0 {
			t.Errorf("penalties[%s] = %v, want 0", k, g.Penalties[k])
		}
	}
	if g.Weighted["cost"] != -1.0 {
		t.Errorf("weighted[cost] = %v, want -1", g.Weighted["cost"])
	}
}
