// 人类 baseline 的端到端 golden 测试: 求解出的 cell 电流、
// 冻结的 metrics/terms/score, 以及 "未被搜索盒裁剪" 门禁
// (CONTRACT.md G6)。
//
// 这些全都需要 stage A 的 field solver, 所以目前被搁置: 它们会以一条
// 精确的 "blocked on stage A" 消息失败而不是让二进制崩溃, 并在
// internal/physics 落地的那一刻变绿。不要删除或跳过它们。
package baseline

import (
	"math"
	"sort"
	"testing"

	"github.com/logos-42/hushfusion-forge/internal/config"
	"github.com/logos-42/hushfusion-forge/internal/objective"
	"github.com/logos-42/hushfusion-forge/internal/physics"
)

// TestTextbookMirrorMatchesGolden 是 baseline 的 §5 锚点: 从零
// 构造手工设计, 再与 testdata/golden_baseline.json 比较 ——
// 线圈、成本、canonical 设计、它产出的 metrics, 以及 objective
// 给它的 composite score。
func TestTextbookMirrorMatchesGolden(t *testing.T) {
	requirePhysics(t)
	g := loadGoldenBaseline(t)
	spec := config.DefaultSpec()

	b, err := TextbookMirror(spec)
	if err != nil {
		t.Fatalf("TextbookMirror: %v", err)
	}
	if b.Name != g.Name {
		t.Errorf("name = %q, want the frozen %q", b.Name, g.Name)
	}
	if b.Note != g.Note {
		t.Errorf("note differs from the frozen reference note:\n got: %q\nwant: %q", b.Note, g.Note)
	}

	// 线圈, 按 reference 实现的构造顺序。
	if len(b.Coils) != len(g.Coils) {
		t.Fatalf("got %d coils, want %d", len(b.Coils), len(g.Coils))
	}
	for i := range g.Coils {
		got, want := b.Coils[i], g.Coils[i]
		if relDiff(got.Radius, want.Radius) > 1e-12 {
			t.Errorf("coil %d radius = %v, want %v", i, got.Radius, want.Radius)
		}
		if relDiff(got.Z, want.Z) > 1e-12 {
			t.Errorf("coil %d z = %v, want %v", i, got.Z, want.Z)
		}
		// 电流是求解出来的, 不是抄来的: 1e-9 是 solver 自身的
		// 容差, 所以这已经是该构造允许的最紧程度。
		if relDiff(got.Current, want.Current) > 1e-9 {
			t.Errorf("coil %d current = %.16g, want the frozen %.16g", i, got.Current, want.Current)
		}
	}
	if relDiff(b.Cost, g.CostProxy) > 1e-9 {
		t.Errorf("cost proxy = %.16g, want the frozen %.16g", b.Cost, g.CostProxy)
	}
	if len(b.Design) != len(g.Design) {
		t.Fatalf("design has %d entries, want %d", len(b.Design), len(g.Design))
	}
	for i := range g.Design {
		if relDiff(b.Design[i], g.Design[i]) > 1e-9 {
			t.Errorf("design[%d] = %.16g, want the frozen %.16g", i, b.Design[i], g.Design[i])
		}
	}

	// 要求机器打败的那个得分, 经真实流水线
	// (metrics -> terms -> weighted -> composite)算出。
	ev := objective.NewEvaluator(spec, analyticSolver(), g.CostRef, physics.BuildGrids(spec))
	res := ev.Evaluate(b.Design)

	if math.Abs(res.Score-g.Score) > 1e-6 {
		t.Errorf("score = %.16g, want the frozen %.16g (diff %g)", res.Score, g.Score, res.Score-g.Score)
	}
	for _, k := range []string{"field", "mirror", "volume", "ripple", "cost"} {
		if math.Abs(res.Terms[k]-g.Terms[k]) > 1e-6 {
			t.Errorf("terms[%s] = %.16g, want the frozen %.16g", k, res.Terms[k], g.Terms[k])
		}
		if math.Abs(res.Weighted[k]-g.Weighted[k]) > 1e-6 {
			t.Errorf("weighted[%s] = %.16g, want the frozen %.16g", k, res.Weighted[k], g.Weighted[k])
		}
	}
	for _, k := range []string{"conductor_field", "coil_separation", "not_a_mirror"} {
		if math.Abs(res.Penalties[k]-g.Penalties[k]) > 1e-9 {
			t.Errorf("penalties[%s] = %v, want the frozen %v", k, res.Penalties[k], g.Penalties[k])
		}
	}
	if !res.Feasible || !g.Feasible {
		t.Errorf("feasible = %v, want true (frozen file says %v)", res.Feasible, g.Feasible)
	}

	// 证据链(用 -v 可见): 断言背后的那些数字。
	t.Logf("solved: I_cell=%.10f A  I_throat=%.10f A  cost=%.9e", b.Design[9], b.Design[8], b.Cost)
	t.Logf("score=%.16g (golden %.16g, diff %.3g)", res.Score, g.Score, res.Score-g.Score)
	t.Logf("terms: field=%.6g mirror=%.6g volume=%.6g ripple=%.6g cost=%.6g",
		res.Terms["field"], res.Terms["mirror"], res.Terms["volume"], res.Terms["ripple"], res.Terms["cost"])
	t.Logf("feasible=%v penalties: conductor_field=%.6g coil_separation=%.6g not_a_mirror=%.6g",
		res.Feasible, res.Penalties["conductor_field"], res.Penalties["coil_separation"], res.Penalties["not_a_mirror"])

	// metrics 用相对 1e-6 (CONTRACT §5: "各项 metrics 相对差 < 1e-6")。像
	// cost_proxy 这样的 metric 是 O(1e12), 绝对容差要么无意义
	// 要么不可能; 接近零的 metric(ripple)退回到
	// 绝对下限 1e-6。
	m := res.Metrics
	for _, c := range []struct {
		name string
		got  float64
	}{
		{"B_mid_T", m.BMidT},
		{"B_throat_T", m.BThroatT},
		{"z_throat_m", m.ZThroatM},
		{"mirror_ratio", m.MirrorRatio},
		{"volume_good", m.VolumeGood},
		{"ripple", m.Ripple},
		{"B_coil_max_T", m.BCoilMaxT},
		{"min_coil_gap_m", m.MinCoilGapM},
		{"cost_proxy", m.CostProxy},
		{"mu0", m.MU0},
	} {
		want, ok := g.Metrics[c.name]
		if !ok {
			t.Fatalf("testdata/golden_baseline.json has no metric %q", c.name)
		}
		if !withinRelTol(c.got, want, 1e-6) {
			t.Errorf("metric %s = %.16g, want the frozen %.16g (rel diff %g)",
				c.name, c.got, want, relDiff(c.got, want))
		}
	}
	if m.NCoils != int(g.Metrics["n_coils"]) {
		t.Errorf("n_coils = %d, want %v", m.NCoils, g.Metrics["n_coils"])
	}
	if m.CoilProximityFloorHit != (g.Metrics["coil_proximity_floor_hit"] != 0) {
		t.Errorf("coil_proximity_floor_hit = %v, want %v", m.CoilProximityFloorHit, g.Metrics["coil_proximity_floor_hit"] != 0)
	}
}

// TestBaselineInsideSearchBox 是冻结门禁 G6: 把手工设计经
// physics.VectorToCoils 解码回来, 并要求它原样返回。
//
// 它为什么存在: VectorToCoils 会按 spec.Bounds 裁剪。如果手工设计落在
// 盒子之外, 解码出来的机器就是一台*不同的*机器,
// "机器打败了人类" 就会是拿一个没人提出过的设计
// 来衡量的。半径、位置与电流上相对 1e-12 —— 也就是拿回
// 完全相同的浮点数。
//
// 顺序说明: Baseline.Coils 按 reference 实现的构造顺序存储
// (先两个 cell 线圈, 再两个 throat), 而
// VectorToCoils 返回 canonical 的 z 排序顺序。这道门禁关心的是
// 机器本身, 而不是它被列出的顺序, 所以两边在逐对比较前
// 都先按 z 排序 —— 而无序的表述(把解码出的线圈再编码
// 回来能逐位复现那个 design 向量)也一并检查。
func TestBaselineInsideSearchBox(t *testing.T) {
	requirePhysics(t)
	g := loadGoldenBaseline(t)
	spec := config.DefaultSpec()

	b, err := TextbookMirror(spec)
	if err != nil {
		t.Fatalf("TextbookMirror: %v", err)
	}
	if len(b.Design) != spec.NParams() {
		t.Fatalf("design has %d entries, want %d", len(b.Design), spec.NParams())
	}

	coils, err := physics.VectorToCoils(b.Design, spec)
	if err != nil {
		t.Fatalf("VectorToCoils: %v", err)
	}
	if len(coils) != len(b.Coils) {
		t.Fatalf("decoded %d coils, want %d", len(coils), len(b.Coils))
	}

	// "未被裁剪" 的无序表述: 经过解码器的往返
	// 必须还回完全相同的 design 向量。
	roundTrip := physics.CoilsToVector(coils)
	if len(roundTrip) != len(b.Design) {
		t.Fatalf("round trip has %d entries, want %d", len(roundTrip), len(b.Design))
	}
	for i := range b.Design {
		if relDiff(roundTrip[i], b.Design[i]) > 1e-12 {
			t.Errorf("round trip design[%d] = %.16g, want the original %.16g (the baseline was clipped)",
				i, roundTrip[i], b.Design[i])
		}
	}

	// 在 canonical 排序之后逐对比较。
	want := append([]physics.Coil(nil), b.Coils...)
	sort.Slice(want, func(i, j int) bool { return want[i].Z < want[j].Z })
	for i := range want {
		got := coils[i]
		for _, c := range []struct {
			name string
			got  float64
			want float64
		}{
			{"radius", got.Radius, want[i].Radius},
			{"z", got.Z, want[i].Z},
			{"current", got.Current, want[i].Current},
		} {
			if relDiff(c.got, c.want) > 1e-12 {
				t.Errorf("decoded coil %d %s = %.16g, want the undecoded %.16g (the baseline was clipped)",
					i, c.name, c.got, c.want)
			}
		}
	}
	// canonical 形式: 按 z 升序排序。
	for i := 1; i < len(coils); i++ {
		if coils[i].Z < coils[i-1].Z {
			t.Errorf("decoded coils are not z-sorted: z[%d] = %v < z[%d] = %v", i, coils[i].Z, i-1, coils[i-1].Z)
		}
	}
	// 而且构造本身就已经拒绝离开盒子。
	if err := checkInsideBounds(b.Coils, spec); err != nil {
		t.Errorf("TextbookMirror produced a design outside the search box: %v", err)
	}

	// 跨语言: 冻结的 design 向量解码出冻结的线圈集合。
	frozen, err := physics.VectorToCoils(g.Design, spec)
	if err != nil {
		t.Fatalf("VectorToCoils(golden design): %v", err)
	}
	frozenWant := append([]physics.Coil(nil), g.Coils...)
	sort.Slice(frozenWant, func(i, j int) bool { return frozenWant[i].Z < frozenWant[j].Z })
	if len(frozen) != len(frozenWant) {
		t.Fatalf("golden design decoded to %d coils, want %d", len(frozen), len(frozenWant))
	}
	for i := range frozenWant {
		if relDiff(frozen[i].Radius, frozenWant[i].Radius) > 1e-12 ||
			relDiff(frozen[i].Z, frozenWant[i].Z) > 1e-12 ||
			relDiff(frozen[i].Current, frozenWant[i].Current) > 1e-12 {
			t.Errorf("decoded golden coil %d = %+v, want %+v", i, frozen[i], frozenWant[i])
		}
	}
}

// TestTextbookMirrorSolvesCellCurrentToBRef 检验该求解的定义性质 ——
// 不是机器的得分, 而是它赖以建立的物理陈述:
// 中平面体积平均场必须等于 spec.BRef, 精度到 solver 自身
// 1e-9 的相对包围区间。
func TestTextbookMirrorSolvesCellCurrentToBRef(t *testing.T) {
	requirePhysics(t)
	g := loadGoldenBaseline(t)
	spec := config.DefaultSpec()

	b, err := TextbookMirror(spec)
	if err != nil {
		t.Fatalf("TextbookMirror: %v", err)
	}
	iCell := b.Coils[0].Current
	iThroat := b.Coils[2].Current

	if relDiff(iCell, 463222.63959687366) > 1e-8 {
		t.Errorf("cell current = %.16g, want ~463222.63959687366", iCell)
	}
	if relDiff(iThroat, 1621279.2385890577) > 1e-8 {
		t.Errorf("throat current = %.16g, want ~1621279.2385890577", iThroat)
	}
	if want := DefaultGeom().ThroatCurrentRatio * iCell; relDiff(iThroat, want) > 1e-12 {
		t.Errorf("throat current = %.16g, want %.16g (ratio 3.5 x cell)", iThroat, want)
	}
	if relDiff(b.Cost, 1.791703035e12) > 1e-8 {
		t.Errorf("cost proxy = %.16g, want ~1.791703035e12", b.Cost)
	}

	// 求解自己声称的东西, 直接测量。
	m := physics.MetricsFor(b.Coils, spec, physics.BuildGrids(spec), analyticSolver())
	if relDiff(m.BMidT, spec.BRef) > 1e-9 {
		t.Errorf("solved midplane field = %.16g T, want spec.BRef = %g T", m.BMidT, spec.BRef)
	}
	if relDiff(m.CostProxy, b.Cost) > 1e-12 {
		t.Errorf("the baseline's own cost proxy (%v) disagrees with the metric (%v)", b.Cost, m.CostProxy)
	}
	// 而且人类 baseline 满足每一条约束: 只有人类本身是合法设计
	// 时, 这个比较才有意义。
	if m.BCoilMaxT > spec.CoilFieldLimit {
		t.Errorf("human baseline conductor field = %v T, above the %v T limit", m.BCoilMaxT, spec.CoilFieldLimit)
	}
	if m.MinCoilGapM < spec.MinCoilSep {
		t.Errorf("human baseline coil gap = %v m, below the %v m minimum", m.MinCoilGapM, spec.MinCoilSep)
	}
	if m.MirrorRatio < objective.MirrorMin {
		t.Errorf("human baseline mirror ratio = %v, below the %v minimum", m.MirrorRatio, objective.MirrorMin)
	}
	if relDiff(m.CostProxy, g.CostProxy) > 1e-9 {
		t.Errorf("cost proxy = %.16g, want the frozen %.16g", m.CostProxy, g.CostProxy)
	}
}
