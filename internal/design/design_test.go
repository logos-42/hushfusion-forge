// design_test.go —— 闭式解**逐条**钉死: 每一条都验它自己的定义关系、边界行为与单调性。
//
// 分界(重要): 这里**不出现任何上游 artifact 里的数字** —— 那是 anchor_test.go 的活,
// 它从 testdata/projectionphysics_anchors.json 读 (契约 §3 禁止把锚点手抄进 Go 源码)。
// 本文件用的是**关系**: 反解-正解互为逆、标度律、单调性、边界返回 +Inf/-1 而不是 NaN,
// 以及 Review 的门接线 (几条门必须存在、unknown 绝不进 pass)。
package design

import (
	"math"
	"testing"

	"github.com/logos-42/hushfusion-forge/internal/config"
	"github.com/logos-42/hushfusion-forge/internal/physics"
)

// approx 是给本文件用的相对比较 (纯关系断言, 不掺外部锚点)。
func approx(t *testing.T, label string, got, want, relTol float64) {
	t.Helper()
	if math.IsNaN(got) || math.IsNaN(want) {
		t.Fatalf("%s: NaN (got %v, want %v) — 闭式解不许吐 NaN", label, got, want)
	}
	den := math.Abs(want)
	if den < 1e-300 {
		den = 1
	}
	if rel := math.Abs(got-want) / den; rel > relTol {
		t.Fatalf("%s: got %v, want %v (相对差 %.3g > %g)", label, got, want, rel, relTol)
	}
}

// TestFieldDensityClosedForms 钉死 §2 的场-密度四条 (PF3 / FC1)。
func TestFieldDensityClosedForms(t *testing.T) {
	// FC1 的标度: n_max ∝ β·B²。
	if got, want := NMax(2*3.0), 4*NMax(3.0); math.Abs(got-want) > 1e-9*math.Abs(want) {
		t.Fatalf("FC1 标度 n_max(2B) = 4·n_max(B) 不成立: %v vs %v", got, want)
	}
	if got, want := NMax(-3.0), NMax(3.0); got != want {
		t.Fatalf("n_max 只依赖 B²: n_max(-3)=%v 应等于 n_max(3)=%v", got, want)
	}
	// FC1 与 B_min 的 β: 同一条式子, β 只影响反解那一步的除数。
	approx(t, "B_min(n, β=1) 与 n_max 同源", BMin(NMax(9.0), Beta), 9.0, 1e-12)
	if got := BMin(NMax(9.0), 2*Beta); !(got < 9.0) {
		t.Fatalf("β 翻倍时所需场应下降 (B_min ∝ 1/√β): %v", got)
	}

	// PF3 / FC1 互为反解: B_min(n_max(B)) == B, n_max(B_min(n)) == n。
	for _, b := range []float64{0.12, 1.6, 9.0, 12.2, 35.0} {
		approx(t, "B_min(n_max(B)) == B", BMin(NMax(b), Beta), b, 1e-12)
	}
	for _, n := range []float64{1e18, 1e20, 1e23} {
		approx(t, "n_max(B_min(n)) == n", NMax(BMin(n, Beta)), n, 1e-12)
	}

	// n_op = min(设计密度, n_max): 两侧各验一条。
	if got := NOp(0.12); got != NMax(0.12) {
		t.Fatalf("n_op 在场弱时应等于 n_max: %v vs %v", got, NMax(0.12))
	}
	if got := NOp(35.0); got != NDesign {
		t.Fatalf("n_op 在场强时应被设计密度封顶: %v vs %v", got, NDesign)
	}

	// τ_L = n·τ/n: 劳森是反比。
	approx(t, "τ_L(2n) = τ_L(n)/2", TauLawson(2*NDesign), TauLawson(NDesign)/2, 1e-15)

	// 无解边界: β ≤ 0 时 B_min 返回 +Inf, 不返回 NaN。
	if got := BMin(1e20, 0); !math.IsInf(got, 1) {
		t.Fatalf("BMin(n, β≤0) 必须是 +Inf (无解), got %v", got)
	}
}

// TestMuWindowClosedForms 钉死 §2 的 μ 窗口 (FC4 / FC11b), 含 B_death 的 1/a 标度。
func TestMuWindowClosedForms(t *testing.T) {
	// 定义即口径: B_death(a) 处 χ_μ 恰好等于 1 —— **但只在死区边界没被设计密度封顶的那一侧**。
	//
	// n_cross(a) = (NT_LAWSON/TAU0(a))·√FLOOR 随 a 增大而下降; 当 n_cross > N_DESIGN 时,
	// n_op(B_death) = N_DESIGN (被设计密度封顶), 于是 χ_μ(B_death) 退化成设计点值而不再等于 1,
	// 并且 B_death > B_min(N_DESIGN) —— 那时先否决的是 field_min, 死活判据不是独立约束。
	// 交叉尺度由常数自己算出来, 不写死:
	aCross := math.Sqrt(D0 * NTLawson * math.Sqrt(Floor) / NDesign) // n_cross(aCross) == N_DESIGN
	if !(aCross > 0) || math.IsInf(aCross, 0) || math.IsNaN(aCross) {
		t.Fatalf("交叉尺度算不出来: %v", aCross)
	}
	if !(aCross < ARef) {
		t.Fatalf("契约 §3 的 a = %v m 必须落在「未封顶」一侧, 但交叉尺度是 %v m", ARef, aCross)
	}
	for _, a := range []float64{ARef, 0.3, 0.5, 1.0} { // a > aCross: 死区边界是独立的
		if a <= aCross {
			continue
		}
		approx(t, "χ_μ(B_death(a), a) == 1", ChiMu(BDeath(a), a), 1.0, 1e-12)
		if !(BDeath(a) < BMin(NDesign, Beta)) {
			t.Fatalf("a=%v (> aCross): B_death 应低于 β 所要求的场, 否则它就不是独立判据", a)
		}
	}
	for _, a := range []float64{0.05, 0.1, 0.15} { // a < aCross: n_op 被封顶
		if a >= aCross {
			continue
		}
		if got := NOp(BDeath(a)); got != NDesign {
			t.Fatalf("a=%v (< aCross) 时 n_op(B_death) 应被设计密度封顶: %v vs %v", a, got, NDesign)
		}
		if !(BDeath(a) > BMin(NDesign, Beta)) {
			t.Fatalf("a=%v (< aCross): B_death 应高于 β 所要求的场 (field_min 先否决)", a)
		}
	}

	// X_req 与 χ_μ 只差一个 FLOOR。
	approx(t, "χ_μ = X_req/FLOOR", ChiMu(1.6, 0.2), XReq(1.6, 0.2)/Floor, 1e-15)

	// B_death ∝ 1/a (X_req ∝ n² 且 n ∝ B²)。
	approx(t, "B_death(0.1)/B_death(0.2) == 2", BDeath(0.1)/BDeath(0.2), 2, 1e-12)
	approx(t, "B_death(0.05)/B_death(0.2) == 4", BDeath(0.05)/BDeath(0.2), 4, 1e-12)
	approx(t, "B_death(0.3)·0.3 == B_death(0.2)·0.2", BDeath(0.3)*0.3, BDeath(0.2)*0.2, 1e-12)

	// 场越强窗口越开: χ_μ **非降**, 并在 B < B_min(N_DESIGN) 的区间严格递增;
	// 一旦 n_op 撞到设计密度就饱和成设计点值 (上界)。
	// 采样点按 B_min(N_DESIGN) 的倍数取, 不写死装置数 —— 这样它不会变成第二份锚点。
	bSat := BMin(NDesign, Beta)
	prev := ChiMu(0.02*bSat, ARef)
	saturated := ChiMu(bSat, ARef)
	for _, frac := range []float64{0.05, 0.1, 0.25, 0.5, 0.9, 1.0, 1.5, 2.0, 5.0, 10.0, 30.0} {
		b := frac * bSat
		got := ChiMu(b, ARef)
		if b < bSat {
			if !(got > prev) {
				t.Fatalf("B=%v (< B_min) 时 χ_μ 应严格递增: %v 不大于 %v", b, got, prev)
			}
		} else if !(got >= prev) {
			t.Fatalf("B=%v 时 χ_μ 不该下降: %v < %v", b, got, prev)
		}
		if got > saturated*(1+1e-15) {
			t.Fatalf("χ_μ 不该超过设计点饱和值 %v: χ_μ(%v)=%v", saturated, b, got)
		}
		prev = got
	}
	// 饱和那一段是常数: n_op 被设计密度封顶。
	approx(t, "撞设计密度后 χ_μ 恒定", ChiMu(30*bSat, ARef), ChiMu(2*bSat, ARef), 1e-15)
	// 场越强（装置越大）窗口越不开: a 越大 B_death 越小。
	if !(BDeath(0.05) > BDeath(0.1) && BDeath(0.1) > BDeath(0.2)) {
		t.Fatalf("B_death 应随 a 递减: %v %v %v", BDeath(0.05), BDeath(0.1), BDeath(0.2))
	}
}

// TestPowerVolumeClosedForms 钉死 §2 的功率/体积标度 (P ∝ B⁴, V ∝ B⁻⁴)。
func TestPowerVolumeClosedForms(t *testing.T) {
	approx(t, "p_rel(B_ref, B_ref) == 1", PRel(BRefPower, BRefPower), 1, 0)
	approx(t, "v_rel(B_ref, B_ref) == 1", VRel(BRefPower, BRefPower), 1, 0)
	approx(t, "P ∝ B⁴", PRel(2*BRefPower, BRefPower), 16, 1e-12)
	approx(t, "V = 1/P", VRel(1.6, BRefPower), 1/PRel(1.6, BRefPower), 1e-15)
	// p_rel 只依赖比值。
	approx(t, "p_rel 齐次", PRel(18, 9), PRel(2, 1), 1e-15)
}

// TestMuDynamicsClosedForms 钉死 §2 的 μ 动力学 (TD19–TD21)。
func TestMuDynamicsClosedForms(t *testing.T) {
	// 闭式解与递推逐位一致 (上游 N4 的残差 7.77e-16 就是这个)。
	const mu0, eta = 0.0, 0.05
	mu := mu0
	for n := 1; n <= 400; n++ {
		mu = mu + eta*(1-mu)
		if got := MuAfterSteps(mu0, eta, n); math.Abs(got-mu) > 1e-12 {
			t.Fatalf("闭式解 vs 递推在第 %d 步分叉: %v vs %v", n, got, mu)
		}
	}

	// TD19: 窗口余量 m_i(1−μ_n) 沿轨道严格递减 (m_i 是常数, 所以就是 1−μ 严格递减)。
	prev := 1 - MuAfterSteps(mu0, eta, 0)
	for n := 1; n <= 500; n++ {
		got := 1 - MuAfterSteps(mu0, eta, n)
		if !(got < prev) {
			t.Fatalf("TD19: 第 %d 步的 1−μ 没有严格减小: %v vs %v", n, got, prev)
		}
		if got <= 0 {
			t.Fatalf("TD8: 第 %d 步的 1−μ ≤ 0 —— μ 有限步走不到 1", n)
		}
		prev = got
	}

	// TD8 + TD20: 锁定因子分母恒正、且严格递增。
	prevLock := LockingFactor(mu0)
	for n := 1; n <= 500; n++ {
		got := LockingFactor(MuAfterSteps(mu0, eta, n))
		if !(got > prevLock) {
			t.Fatalf("TD20: 第 %d 步的锁定因子没有严格递增: %v vs %v", n, got, prevLock)
		}
		prevLock = got
	}

	// TD21: 判据 (1−η)^n(1−μ₀) ≤ FLOOR 只在整数 n 上有意义 —— 解析值与整数语义必须自洽。
	analytic := WindowCloseStep(mu0, eta, Floor)
	if math.IsInf(analytic, 0) || analytic <= 0 {
		t.Fatalf("WindowCloseStep(0, 0.05, FLOOR) 应是正的有限值, got %v", analytic)
	}
	nStar := FirstClosedStep(mu0, eta, Floor)
	if nStar != int(math.Ceil(analytic)) {
		t.Fatalf("FirstClosedStep 必须是 ceil(解析值): %d vs %v", nStar, analytic)
	}
	// 第 nStar−1 步还没关、第 nStar 步已经关 (逐位递推, 不是把答案写死)。
	if reason := 1 - MuAfterSteps(mu0, eta, nStar-1); reason <= Floor {
		t.Fatalf("第 %d 步就关了 (1−μ=%v ≤ FLOOR=%v), 与 nStar=%d 矛盾", nStar-1, reason, Floor, nStar)
	}
	if closed := 1 - MuAfterSteps(mu0, eta, nStar); closed > Floor {
		t.Fatalf("第 %d 步还没关 (1−μ=%v > FLOOR=%v), 与 nStar=%d 矛盾", nStar, closed, Floor, nStar)
	}
	// 解析值本身就在 (nStar−1, nStar] 里。
	if !(analytic > float64(nStar-1) && analytic <= float64(nStar)) {
		t.Fatalf("解析关闭步 %v 不在 (%d, %d] 内", analytic, nStar-1, nStar)
	}

	// 边界: 全部显式, 不许 NaN。
	if got := WindowCloseStep(0, 1.0, Floor); !math.IsInf(got, 1) {
		t.Fatalf("η ≥ 1 ⟹ +Inf (一步到 1, 无窗口), got %v", got)
	}
	if got := WindowCloseStep(0, 0, Floor); !math.IsInf(got, 1) {
		t.Fatalf("η = 0 ⟹ +Inf (窗口永不关闭), got %v", got)
	}
	if got := FirstClosedStep(0, 1.5, Floor); got != -1 {
		t.Fatalf("永不关闭时 FirstClosedStep 必须是 -1, got %d", got)
	}
	if got := WindowCloseStep(1.0, 0.05, Floor); got != 0 {
		t.Fatalf("μ₀ ≥ 1 时关闭步必须是 0, got %v", got)
	}
	if got := LockingFactor(1.0); !math.IsInf(got, 1) {
		t.Fatalf("μ ≥ 1 时锁定因子是发散点 (+Inf), got %v", got)
	}
	if got := Rci(1.0); !math.IsInf(got, 1) {
		t.Fatalf("μ ≥ 1 时 R_ci 是 +Inf, got %v", got)
	}

	// FC4 反解与锁定因子互为逆: 1/√(1−μ(g)) == g。
	for _, g := range []float64{1.0, 3.0, 10.0, 100.0} {
		approx(t, "LockingFactor(MuFromGain(g)) == g", LockingFactor(MuFromGain(g)), g, 1e-12)
	}
	// 契约 §2 写死的那条: g = 10 ⟹ μ = 0.99。
	approx(t, "MuFromGain(10) == 0.99", MuFromGain(10), 0.99, 1e-15)

	// D1: R_ci 与 μ_min(δ) 互为逆 (FLOOR 与 D1 的边界由 anchor_test 钉)。
	// 组合两个闭式解要经过 1/(1−μ) 的抵消, 因此容差放到 1e-9 而不是 1e-12。
	for _, d := range []float64{1e-6, 1e-4, 1e-2} {
		approx(t, "R_ci(μ_min(δ)) == δ", Rci(MuMinFromDelta(d)), d, 1e-9)
	}
	// D3: 功率倍数 = (10^decades)^(1/k), 且随 k 减小而暴涨。
	approx(t, "PowerMultiple(1, 4) == 1e4", PowerMultiple(1, 4), 1e4, 1e-15)
	approx(t, "PowerMultiple(0.5, 4) == PowerMultiple(1,8)", PowerMultiple(0.5, 4), PowerMultiple(1, 8), 1e-15)
	if !(PowerMultiple(0.1, 4) > PowerMultiple(0.25, 4) && PowerMultiple(0.25, 4) > PowerMultiple(0.5, 4)) {
		t.Fatalf("功率倍数应随 k 减小而增大: %v %v %v",
			PowerMultiple(0.1, 4), PowerMultiple(0.25, 4), PowerMultiple(0.5, 4))
	}
	if got := PowerMultiple(0, 4); !math.IsInf(got, 1) {
		t.Fatalf("k ≤ 0 时幂律无意义 ⟹ +Inf, got %v", got)
	}
}

// TestSourceTableShape 钉死场源材料类表的结构 (数值由 anchor_test 反查上游)。
func TestSourceTableShape(t *testing.T) {
	wantOrder := []string{
		SourceMATBGN2Perp, SourceMATBGN2Par, SourceMATTGN3Par,
		SourceITERTF, SourceSPARCTF, SourceCFR2Vac, SourceCFR2Comp,
	}
	table := SourceTable()
	if len(table) != len(wantOrder) {
		t.Fatalf("场源材料类应 %d 行, got %d", len(wantOrder), len(table))
	}
	for i, key := range wantOrder {
		if table[i].Key != key {
			t.Fatalf("第 %d 行是 %q, 上游顺序要求 %q", i, table[i].Key, key)
		}
		row := table[i]
		if row.Label == "" || row.Citation == "" {
			t.Fatalf("%s: label/citation 不许为空 (label=%q citation=%q)", key, row.Label, row.Citation)
		}
		if !(row.BCapT > 0) {
			t.Fatalf("%s: B_cap 必须为正, got %v", key, row.BCapT)
		}
		got, ok := LookupSource(key)
		if !ok || got != row {
			t.Fatalf("LookupSource(%q) 与表不一致: %+v ok=%v", key, got, ok)
		}
	}
	// 未知 key 必须显式失败, 不许返回零值 Source 蒙混过去。
	if got, ok := LookupSource("MATBG_N2_parallel"); ok {
		t.Fatalf("未知 key 必须返回 false, got %+v", got)
	}
	// 表是拷一份: 改返回值不许改到真源。
	table[0].BCapT = -1
	if again := SourceTable()[0].BCapT; again == -1 {
		t.Fatal("SourceTable 返回的切片与真源共享底层数组 —— 调用方能改坏上游口径")
	}
}

// fixtures -------------------------------------------------------------------

func fixtureMetrics() physics.Metrics {
	// 合成指标: 只为驱动门的判定, 不冒充任何真实装置的数值。
	return physics.Metrics{
		BMidT: 1.0, BThroatT: 2.5, MirrorRatio: 2.5,
		BCoilMaxT: 3.0, MinCoilGapM: 0.5, NCoils: 4, MU0: config.MU0,
	}
}

func reviewWith(t *testing.T, spec config.Spec, m physics.Metrics, in Input) Scope {
	t.Helper()
	s := Review(spec, m, in)
	if len(s.Gates) != 7 {
		t.Fatalf("应有 7 条 gate (六道门 + fc5_locked), got %d", len(s.Gates))
	}
	if s.Passed+s.Failed+s.Unknown != len(s.Gates) {
		t.Fatalf("计数不自洽: pass=%d fail=%d unknown=%d / %d 条",
			s.Passed, s.Failed, s.Unknown, len(s.Gates))
	}
	return s
}

// TestReviewGateWiring 钉死六道门 + FC5 unknown 的接线与总判决口径。
func TestReviewGateWiring(t *testing.T) {
	spec := config.DefaultSpec()
	m := fixtureMetrics()
	in := Input{DeviceScaleM: ARef, SourceKey: SourceMATBGN2Par, Eta: 0.05, Mu0: 0}

	s := reviewWith(t, spec, m, in)

	// 每条门都在, 且出处非空 (门必须能指回上游条目)。
	for _, key := range []string{GateFieldMin, GateDeath, GateMuWindow, GateCoilLoad,
		GateBuildable, GateSteps, GateFC5Locked} {
		g, ok := s.GateByKey(key)
		if !ok {
			t.Fatalf("缺门 %q", key)
		}
		if g.Label == "" || g.Formula == "" || g.Provenance == "" {
			t.Fatalf("门 %q 的 label/formula/provenance 不许为空: %+v", key, g)
		}
	}

	// fc5_locked: 上游未证 ⟹ unknown, 绝不报 pass; 也不进总判决。
	fc5, _ := s.GateByKey(GateFC5Locked)
	if !fc5.Unknown || fc5.Pass {
		t.Fatalf("fc5_locked 必须 Unknown=true 且 Pass=false, got %+v", fc5)
	}
	for _, k := range DecisiveGateKeys {
		if k == GateFC5Locked || k == GateSteps {
			t.Fatalf("总判决不许包含 %q", k)
		}
	}

	// 契约 §3 的口径: 1.6 T 这条在场天花板下 μ 窗口是开的。
	if s.Source.BCapT < s.BDeathT {
		t.Fatalf("MATBG∥ 的 B_cap %v 应不低于 B_death %v", s.Source.BCapT, s.BDeathT)
	}
	if g, _ := s.GateByKey(GateDeath); !g.Pass || g.Unknown {
		t.Fatalf("MATBG∥ 的死活判据应通过: %+v", g)
	}
	if g, _ := s.GateByKey(GateMuWindow); !g.Pass || g.Unknown || !(g.Value > 1) {
		t.Fatalf("MATBG∥ 的 μ 窗口门应通过: %+v", g)
	}
	// 0.12 T 那条: 死活判据与 μ 窗口**都**不过, 且 B_cap < B_min ⟹ β 撑不住。
	perp := reviewWith(t, spec, m, Input{DeviceScaleM: ARef, SourceKey: SourceMATBGN2Perp, Eta: 0.05})
	if g, _ := perp.GateByKey(GateDeath); g.Pass || g.Unknown {
		t.Fatalf("MATBG⊥(0.12 T) 的死活判据必须不过: %+v", g)
	}
	if g, _ := perp.GateByKey(GateMuWindow); g.Pass || g.Value >= 1 {
		t.Fatalf("MATBG⊥ 的 χ_μ 必须 < 1 (窗口关闭): %+v", g)
	}
	if perp.AllDecisivePass {
		t.Fatal("MATBG⊥ 在 a=0.2 下总判决必须是「不过」")
	}

	// buildable: 两个条件各自能否决。
	floorHit := m
	floorHit.CoilProximityFloorHit = true
	if g, _ := reviewWith(t, spec, floorHit, in).GateByKey(GateBuildable); g.Pass {
		t.Fatal("coil_proximity_floor_hit = true 时可造性门必须不过")
	}
	tight := m
	tight.MinCoilGapM = 2*spec.TPack - 1e-9
	if g, _ := reviewWith(t, spec, tight, in).GateByKey(GateBuildable); g.Pass {
		t.Fatal("min_coil_gap < 2·TPack 时可造性门必须不过")
	}
	if g, _ := reviewWith(t, spec, m, in).GateByKey(GateBuildable); !g.Pass {
		t.Fatalf("gap 够大且没踩地板时可造性门应通过: %+v", g)
	}

	// coil_load: 天花板是 min(spec.CoilFieldLimit, B_cap)。
	load, _ := s.GateByKey(GateCoilLoad)
	if want := math.Min(spec.CoilFieldLimit, SourceMATBGN2ParBCap()); load.Ref != want {
		t.Fatalf("coil_load 阈值应是 min(%v, %v) = %v, got %v",
			spec.CoilFieldLimit, SourceMATBGN2ParBCap(), want, load.Ref)
	}

	// field_min: B_throat = 2.5 T 应远高于设计所需场。
	if g, _ := s.GateByKey(GateFieldMin); !g.Pass {
		t.Fatalf("2.5 T 的约束场应撑得住设计密度: %+v", g)
	}
	weak := m
	weak.BThroatT = 0.5
	if g, _ := reviewWith(t, spec, weak, in).GateByKey(GateFieldMin); g.Pass {
		t.Fatal("0.5 T 的约束场不许过 field_min")
	}
}

// SourceMATBGN2ParBCap 从表里取 MATBG∥ 的场天花板 (测试用, 不重复数字)。
func SourceMATBGN2ParBCap() float64 {
	s, _ := LookupSource(SourceMATBGN2Par)
	return s.BCapT
}

// TestReviewUnknownSourceAndSteps 钉死「读不到输入就报 unknown」与「关闭步不判生死」。
func TestReviewUnknownSourceAndSteps(t *testing.T) {
	spec := config.DefaultSpec()
	m := fixtureMetrics()

	// 未知场源: 依赖 B_cap 的三条门一律 unknown, 总判决不过。
	bad := reviewWith(t, spec, m, Input{DeviceScaleM: ARef, SourceKey: "MATBG_N2_侧面", Eta: 0.05})
	for _, key := range []string{GateDeath, GateMuWindow, GateCoilLoad} {
		g, _ := bad.GateByKey(key)
		if !g.Unknown {
			t.Fatalf("未知场源时 %q 必须报 unknown: %+v", key, g)
		}
	}
	if bad.SourceKnown || bad.AllDecisivePass {
		t.Fatalf("未知场源不许总判决通过: %+v", bad)
	}
	if len(bad.Notes) == 0 {
		t.Fatal("未知场源必须在 Notes 里说清楚")
	}
	// 不依赖 B_cap 的门照常可判。
	if g, _ := bad.GateByKey(GateFieldMin); g.Unknown {
		t.Fatal("field_min 不依赖场源, 不该被未知 key 拖成 unknown")
	}
	if g, _ := bad.GateByKey(GateBuildable); g.Unknown {
		t.Fatal("buildable 不依赖场源, 不该被未知 key 拖成 unknown")
	}

	// η ≥ 1: 关闭步报 +Inf (窗口不存在), 但**不判生死** —— 总判决只看五条门。
	s := reviewWith(t, spec, m, Input{DeviceScaleM: ARef, SourceKey: SourceMATBGN2Par, Eta: 1.5})
	steps, _ := s.GateByKey(GateSteps)
	if steps.Pass || !math.IsInf(steps.Value, 1) {
		t.Fatalf("η=1.5 时关闭步应为 +Inf 且不通过: %+v", steps)
	}
	if _, ok := s.GateByKey(GateSteps); ok && s.AllDecisivePass !=
		(gatePass(t, s, GateFieldMin) && gatePass(t, s, GateDeath) && gatePass(t, s, GateMuWindow) &&
			gatePass(t, s, GateCoilLoad) && gatePass(t, s, GateBuildable)) {
		t.Fatal("总判决口径不是「五条门全过」(关闭步不该影响它)")
	}
	if s.CloseStepInt != -1 {
		t.Fatalf("η=1.5 时整数关闭步应是 -1 (永不关闭), got %d", s.CloseStepInt)
	}

	// 上下文量在 Scope 里齐备 (CLI 的总结块直接引用它们)。
	if !(s.MuCeiling < 1 && s.MuCeiling > 0) {
		t.Fatalf("μ 天花板应在 (0,1): %v", s.MuCeiling)
	}
	if !(math.Abs(s.MuCeiling-(1-Floor)) < 1e-15) {
		t.Fatalf("μ 天花板应是 1−FLOOR: %v vs %v", s.MuCeiling, 1-Floor)
	}
	// μ₀ = 0 时锁定因子恰好是 1 (分母 1); μ₀ > 0 时才大于 1 —— 别把两条混起来。
	if s.LockingFactor != 1 {
		t.Fatalf("μ₀=0 时锁定因子应为 1, got %v", s.LockingFactor)
	}
	if s.RciMu0 != 0 {
		t.Fatalf("μ₀=0 时 R_ci 应为 0, got %v", s.RciMu0)
	}
	pos := reviewWith(t, spec, m, Input{DeviceScaleM: ARef, SourceKey: SourceMATBGN2Par,
		Eta: 0.05, Mu0: 0.5})
	if !(pos.LockingFactor > 1) || !(pos.RciMu0 > 0) {
		t.Fatalf("μ₀=0.5 时锁定因子与 R_ci 都应 > 0 的基准: %v %v",
			pos.LockingFactor, pos.RciMu0)
	}
	approx(t, "锁定因子 = 1/√(1−μ₀)", pos.LockingFactor, 1/math.Sqrt(1-0.5), 1e-15)
	if s.Tau0S <= 0 || s.BMinT <= 0 || s.BDeathT <= 0 {
		t.Fatalf("Tau0/B_min/B_death 必须为正: %v %v %v", s.Tau0S, s.BMinT, s.BDeathT)
	}
}

func gatePass(t *testing.T, s Scope, key string) bool {
	t.Helper()
	g, ok := s.GateByKey(key)
	if !ok {
		t.Fatalf("缺门 %q", key)
	}
	return g.Pass && !g.Unknown
}
