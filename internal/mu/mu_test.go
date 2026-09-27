// mu_test.go —— 算子代数 / μ 动力学 / 目标函数的单测。
//
// 这里的每条断言都对应一条上游条目(GCA*/TD*), 而且**不是**"看起来差不多":
// 层状 ⟹ 交换子逐位为零、部分重叠 ⟹ 非零、抹平 ⟹ η = 1、顺序差 = (1−μ)(1−η_before)
// 全都是可以逐位断言的代数事实。
package mu

import (
	"math"
	"math/rand"
	"testing"

	"github.com/logos-42/hushfusion-forge/internal/config"
	"github.com/logos-42/hushfusion-forge/internal/design"
	"github.com/logos-42/hushfusion-forge/internal/physics"
)

// fltEqual 是浮点比较: 相对 1e-12(与仓内跨语言锚点同一容差口径)。
func fltEqual(a, b float64) bool {
	if a == b {
		return true
	}
	return math.Abs(a-b) <= 1e-12*math.Max(math.Abs(a), math.Abs(b))
}

// ulpBand 是"代数恒等式在 float64 里的残差带"(相对量)。
//
// 上游报告把 P² = P、Q(P_A v) = 0 的残差写成逐位 0.0; 本仓用**朴素左到右求和**复算时
// 它们是 1 ULP 量级, 原因是: 抹平后的向量在区域内是同一个数 m, 而"再对它们取一次均值"
// 要求 Σ(m) 恰好等于 |A|·m —— 朴素求和一般做不到(numpy 的分块求和 + 2 的幂的区域
// 大小才让它逐位相等)。这不是矛盾, 是**求和次序**的差别; 锚点里如实记着两边的数。
const ulpBand = 1e-15

func randomField(rng *rand.Rand, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = rng.NormFloat64()
	}
	return out
}

// --------------------------------------------------------------------------- GCA1–GCA5

// TestFlattenIsIdempotent 是上游 GCA1: P_A(P_A v) = P_A v —— 控制是"设定", 不是累计。
//
// 在实数域里这是恒等式; 在 float64 里残差落在 1 ULP 量级(见 ulpBand 的注释: 对一串
// 相同的数再取均值, 朴素求和一般不等于那个数)。判据因而写成"残差在 ULP 带内", 而不是
// 逐位相等 —— 后者会把一个浮点事实伪装成一条物理断言。
func TestFlattenIsIdempotent(t *testing.T) {
	rng := rand.New(rand.NewSource(20260927))
	a := Cells{0, 1, 2, 3, 4, 5, 6, 7}
	for i := 0; i < 200; i++ {
		v := randomField(rng, 16)
		once := Flatten(v, a)
		twice := Flatten(once, a)
		scale := MaxAbs(once)
		if d := MaxAbs(sub(twice, once)); d > ulpBand*scale {
			t.Fatalf("GCA1 violated: max|P_A(P_A v) − P_A v| = %v on iteration %d (scale %v)", d, i, scale)
		}
		// 自复合(同一个区域两次)也一样: 交换子对自己恒等零(GCA6 的退化情形)。
		if d := MaxAbs(Commutator(once, a, a)); d > ulpBand*scale {
			t.Fatalf("GCA6(self) violated: max|[P_A,P_A]v| = %v", d)
		}
	}
}

// TestFluctuationEnergyZeroIffConstant 是上游 GCA2a/GCA2b: Q_A ≥ 0, 且 Q_A = 0 ⟺ A 内常值。
func TestFluctuationEnergyZeroIffConstant(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	a := Cells{4, 5, 6, 7, 8, 9, 10, 11}
	for i := 0; i < 200; i++ {
		v := randomField(rng, 16)
		if q := FluctuationEnergy(v, a); q < 0 {
			t.Fatalf("GCA2a violated: Q_A = %v < 0", q)
		}
		// 抹平之后 Q 落在 ULP 带内(GCA2c; 逐位零只在实数域里成立, 见 ulpBand)。
		if q := FluctuationEnergy(Flatten(v, a), a); q > ulpBand*ulpBand*energyScale(v, a) {
			t.Fatalf("GCA2c violated: Q_A(P_A v) = %v is above the ULP band", q)
		}
		// Q = 0 ⟹ 常值(A 外随便)。
		constant := make([]float64, len(v))
		for j := range constant {
			constant[j] = 2.5
		}
		if q := FluctuationEnergy(constant, a); q != 0 {
			t.Fatalf("GCA2b violated: Q_A(constant field) = %v, want exactly 0", q)
		}
	}
}

// energyScale 是 Q_A 的量级(用它把"ULP 带"换算到残差上)。
func energyScale(v []float64, a Cells) float64 {
	return FluctuationEnergy(v, a)
}

// TestGainIsFullAfterFlatten 是上游 TD11b/TD12: 抹平一次 ⟹ Q = 0 ⟹ η = 1。
//
// 全域抹平那一路是**逐位**的: Q_Ω(flatten(v,Ω)) 是 1e-32 量级(ULP 残差), 而参照起伏
// Q_Ω(v₀) 是 O(1), 于是 1 − q/q₀ 在 float64 里恰好舍入到 1.0。这不是"差不多"——它是
// 本世界"构造性顺序差"能拿到确切闭式值的原因: 抹平全域 ⟹ η = 1 ⟹ μ 一步到天花板。
func TestGainIsFullAfterFlatten(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	omega := AllCells(16)
	for i := 0; i < 100; i++ {
		v0 := randomField(rng, 16)
		v := randomField(rng, 16)
		if eta := GainOf(Flatten(v, omega), v0, omega); eta != 1 {
			t.Fatalf("TD12 violated: eta after flattening the whole field = %v, want exactly 1", eta)
		}
		// 子区域抹平: 同一区域上的增益同样到满(差只在 ULP 量级)。
		sub := Cells{0, 1, 2, 3}
		if eta := GainOf(Flatten(v, sub), v0, sub); !fltEqual(eta, 1) {
			t.Fatalf("TD11b violated: eta after flattening a sub-region = %v, want 1", eta)
		}
	}
}

// TestGainRefusesZeroReference 是 TD11 的假设 Q_A(v₀) ≠ 0 的执行形式:
// 参照场自己没有起伏时, 桥无从谈起 —— 大声失败, 不返回一个 0/0 的数。
func TestGainRefusesZeroReference(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("Gain accepted Q_A(v0) = 0: the bridge eta = 1 − Q/Q0 has no meaning there")
		}
	}()
	_ = Gain(1.0, 0.0)
}

// --------------------------------------------------------------------------- GCA6 交换子

// TestCommutatorWitnessesMatchUpstream 复现上游 GCA6c / N9 的两个见证**向量本体**。
//
// 上游的键名把 flatten(A, flatten(B, v)) 写作"先 A 后 B"; 按定义它是 B 先作用。
// 这里断言的是组合式本体(与 Lean `flatten_not_commute_overlap` 的 hL/hR 同侧):
//
//	flatten({0,1}, flatten({1,2}, [1,0,0])) = [0.5, 0.5, 0.0]
//	flatten({1,2}, flatten({0,1}, [1,0,0])) = [0.5, 0.25, 0.25]
//
// 两个向量都是二进制可精确表示的数, 所以这里是**逐位相等**而不是近似。
func TestCommutatorWitnessesMatchUpstream(t *testing.T) {
	a := Cells{0, 1}
	b := Cells{1, 2}
	v := []float64{1, 0, 0}

	ab := Flatten(Flatten(v, b), a)
	ba := Flatten(Flatten(v, a), b)

	wantAB := []float64{0.5, 0.5, 0.0}
	wantBA := []float64{0.5, 0.25, 0.25}
	for i := range wantAB {
		if ab[i] != wantAB[i] {
			t.Fatalf("flatten(A, flatten(B, v))[%d] = %v, want %v", i, ab[i], wantAB[i])
		}
		if ba[i] != wantBA[i] {
			t.Fatalf("flatten(B, flatten(A, v))[%d] = %v, want %v", i, ba[i], wantBA[i])
		}
	}
	if Relate(a, b) != RelationPartialOverlap {
		t.Fatalf("Relate({0,1},{1,2}) = %q, want %q", Relate(a, b), RelationPartialOverlap)
	}
	if MaxAbs(Commutator(v, a, b)) == 0 {
		t.Fatal("GCA6c violated: the commutator of two partially overlapping regions is zero")
	}
}

// TestCommutatorZeroIffLamellar 是上游 GCA6a/GCA6b: **层状 ⟹ 交换子为零**。
//
// 这是"构造性零"的那一半: 它让世界里的非零读数可以被归因到区域关系上, 而不是被一个
// 阈值吸收(上游 N6 的层状行也是同一个形态: 重叠度 0 或 1 时范数落在浮点噪声里)。
//
// 两种层状要分开读: **不交**那一支是逐位零(两个均值互不读取对方的格点); **嵌套**那一支
// 落在 1 ULP 带内(被抹平的区域再参与外层的求和, 舍入次序变了)。世界里的"零"判据因此
// 用那把尺子自己的零带(与 G19a 的控制组同一口径), 不是逐位相等。
func TestCommutatorZeroIffLamellar(t *testing.T) {
	rng := rand.New(rand.NewSource(13))
	regions := DeclaredRegions(16)
	lamellar, overlapping := 0, 0
	for i := range regions {
		for j := i + 1; j < len(regions); j++ {
			a, b := regions[i].Cells, regions[j].Cells
			rel := Relate(a, b)
			lam := Lamellar(a, b)
			if lam {
				lamellar++
			} else {
				overlapping++
			}
			for trial := 0; trial < 20; trial++ {
				v := randomField(rng, 16)
				scale := MaxAbs(v)
				d := MaxAbs(Commutator(v, a, b))
				if lam && d > ulpBand*scale {
					t.Fatalf("GCA6a/b violated: %s/%s are lamellar (%s) but max|[P_A,P_B]v| = %v",
						regions[i].Key, regions[j].Key, rel, d)
				}
				if rel == RelationDisjoint && d != 0 {
					t.Fatalf("GCA6a violated: %s/%s are disjoint but the commutator is %v (want exactly 0)",
						regions[i].Key, regions[j].Key, d)
				}
				// 部分重叠那一支必须严格非零; 这里的 1e-6 只是"不是舍入噪声"的量级检查,
				// 真正的判据在门 G21(实测 A 与闭式预测一致)。
				if !lam && d < 1e-6*scale {
					t.Fatalf("GCA6c violated: %s/%s partially overlap but max|[P_A,P_B]v| = %v is at noise level",
						regions[i].Key, regions[j].Key, d)
				}
			}
		}
	}
	// 声明的区域族里两类都必须存在 —— 否则这条测试会因为"没有部分重叠的对"而空过。
	total := len(regions) * (len(regions) - 1) / 2
	if lamellar == 0 || overlapping == 0 {
		t.Fatalf("the declared region family must contain both classes (lamellar=%d, partial=%d)",
			lamellar, overlapping)
	}
	if lamellar+overlapping != total {
		t.Fatalf("region pair accounting: lamellar %d + partial %d != %d pairs", lamellar, overlapping, total)
	}
}

// TestNestedAbsorption 是上游 GCA6b 的算子形式: 嵌套 ⟹ 两个次序都等于大区域的控制。
func TestNestedAbsorption(t *testing.T) {
	rng := rand.New(rand.NewSource(17))
	outer := Cells{0, 1, 2, 3, 4, 5, 6, 7}
	inner := Cells{0, 1, 2, 3}
	for i := 0; i < 50; i++ {
		v := randomField(rng, 16)
		flat := Flatten(v, outer)
		for _, got := range [][]float64{Flatten(Flatten(v, inner), outer), Flatten(Flatten(v, outer), inner)} {
			if d := MaxAbs(sub(got, flat)); d > ulpBand*MaxAbs(flat) {
				t.Fatalf("GCA6b violated: max|composite − P_outer v| = %v", d)
			}
		}
	}
}

// --------------------------------------------------------------------------- TD1–TD10

// TestMuStepMatchesUpstreamDynamics 逐条对上 TD1a/TD2/TD3/TD5/TD7/TD8 的断言。
func TestMuStepMatchesUpstreamDynamics(t *testing.T) {
	// TD1a 有界: μ,η ∈ [0,1] ⟹ μ' ∈ [0,1]。
	for i := 0; i <= 100; i++ {
		for j := 0; j <= 100; j++ {
			muVal, eta := float64(i)/100, float64(j)/100
			got := MuStep(muVal, eta)
			if got < 0 || got > 1 {
				t.Fatalf("TD1a violated: muStep(%v,%v) = %v outside [0,1]", muVal, eta, got)
			}
			if muVal < 1 && eta > 0 && !(got > muVal) {
				t.Fatalf("TD2 violated: muStep(%v,%v) = %v is not strictly larger", muVal, eta, got)
			}
			if muVal < 1 && eta > 0 && eta < 1 && !(got < 1) {
				t.Fatalf("TD3 violated: muStep(%v,%v) = %v reaches 1 with a finite gain", muVal, eta, got)
			}
		}
	}
	// TD5 满增益一步到 1; TD4 μ = 1 是不动点。
	if got := MuStep(0.37, 1); got != 1 {
		t.Fatalf("TD5 violated: muStep(0.37,1) = %v, want 1", got)
	}
	if got := MuStep(1, 0.2); got != 1 {
		t.Fatalf("TD4 violated: muStep(1,0.2) = %v, want 1", got)
	}
	// TD7 闭式解 vs 逐位递推(同一批数, 两条路径)。
	muVal, eta := 0.1, 0.05
	iter := muVal
	for n := 0; n < 300; n++ {
		iter = MuStep(iter, eta)
	}
	if closed := MuAfterSteps(muVal, eta, 300); !fltEqual(iter, closed) {
		t.Fatalf("TD7 violated: iterative %v vs closed form %v", iter, closed)
	}
	// TD8 有限步不可达。
	if MuAfterSteps(0, eta, 300) >= 1 {
		t.Fatal("TD8 violated: the orbit reached 1 in a finite number of steps")
	}
}

// --------------------------------------------------------------------------- TD12/TD15/TD16

// muApply 是 μ 世界的一次转移的**代数本体**: 抹平只改场, 更新只改 μ(用当前场的增益)。
// 世界(internal/world)与计量脚本(Python)各自实现这一条, 三条实现互相对账。
func muApply(v []float64, muVal float64, kind ActionKind, region Cells, v0 []float64, omega Cells) ([]float64, float64) {
	switch kind {
	case KindFlatten:
		return Flatten(v, region), muVal
	case KindUpdateMu:
		return v, MuStep(muVal, GainOf(v, v0, omega))
	default:
		panic("mu: unknown action kind in the test helper")
	}
}

// TestOrderDifferenceMatchesTD16 是上游 TD16 的显式式子:
//
//	μ(先抹平再更新) − μ(先更新再抹平) = (1 − μ)(1 − η_before)
//
// 前提是**抹平的那个区域就是算增益的那个区域**(TD14a 的 hA : A.Nonempty 与
// h0 : Q_A(v₀) ≠ 0)。本世界里 update_mu 用的是 Ω 上的增益, 所以这条式子对应
// 动作对 (抹平 Ω, 更新 μ) —— 也正是"构造性 A > 0"用的那一对。
func TestOrderDifferenceMatchesTD16(t *testing.T) {
	rng := rand.New(rand.NewSource(19))
	omega := AllCells(16)

	// (i) 显式形式: 抹平 Ω ⟹ μ 一步到 1, 差 = (1 − μ)(1 − η_before)。
	for trial := 0; trial < 50; trial++ {
		v0 := randomField(rng, 16)
		v := randomField(rng, 16)
		muVal := rng.Float64()
		etaBefore := GainOf(v, v0, omega)

		vFlat, _ := muApply(v, muVal, KindFlatten, omega, v0, omega)
		_, afterFlat := muApply(vFlat, muVal, KindUpdateMu, omega, v0, omega)
		_, beforeFlat := muApply(v, muVal, KindUpdateMu, omega, v0, omega)

		if afterFlat != 1 {
			t.Fatalf("TD14a violated: mu after flatten-then-update = %v, want exactly 1", afterFlat)
		}
		if gap, want := afterFlat-beforeFlat, OrderGap(muVal, etaBefore); !fltEqual(gap, want) {
			t.Fatalf("TD16 violated: order gap %v vs (1−μ)(1−η_before) = %v", gap, want)
		}
	}

	// (ii) 一般形式(TD15): 差 = (η_after − η_before)(1 − μ)。子区域抹平走的是这一条 ——
	// 这时 η_after < 1, 差值由"抹平让增益涨了多少"决定, 依然严格为正。
	region := Cells{0, 1, 2, 3, 4, 5, 6, 7}
	for trial := 0; trial < 50; trial++ {
		v0 := randomField(rng, 16)
		v := randomField(rng, 16)
		muVal := rng.Float64()
		etaBefore := GainOf(v, v0, omega)

		vFlat, _ := muApply(v, muVal, KindFlatten, region, v0, omega)
		_, afterFlat := muApply(vFlat, muVal, KindUpdateMu, region, v0, omega)
		_, beforeFlat := muApply(v, muVal, KindUpdateMu, region, v0, omega)
		etaAfter := GainOf(vFlat, v0, omega)

		if !(etaAfter > etaBefore) {
			t.Fatalf("TD13 violated: flattening did not raise the gain (%v -> %v)", etaBefore, etaAfter)
		}
		gap, want := afterFlat-beforeFlat, (etaAfter-etaBefore)*(1-muVal)
		if !fltEqual(gap, want) {
			t.Fatalf("TD15 violated: order gap %v vs (η_after − η_before)(1 − μ) = %v", gap, want)
		}
		if !(gap > 0) {
			t.Fatalf("the order gap must be strictly positive, got %v", gap)
		}
	}
}

// TestConstructorOrderGapOnWholeField 是「构造性 A > 0」的核心断言。
//
// 动作对 = (抹平 Ω, 更新 μ)。两次序的终态场**相同**(都抹平过一次), 只有 μ 不同:
//
//	A = score(先抹平后更新) − score(先更新后抹平)
//	  = (W_mu/Ceiling)·(1−μ)(1−η_before) + W_closed·1[先更新后抹平还没关闭]
//
// 两项都是正的(μ < 1、η_before < 1), 所以 A > 0 **由定理保证**(TD12 + TD15/TD16),
// 而不是由某个阈值保证。
func TestConstructorOrderGapOnWholeField(t *testing.T) {
	rng := rand.New(rand.NewSource(23))
	omega := AllCells(16)

	// 见证一(与上游 TD17 同一组数): 起点场 v = v₀, μ = 0 ⟹ η_before = 0, 差最大 = 1 − μ。
	// 上游用 Fin 2 的 [1,0]; 这里用一条 16 格的场向量, 式子完全相同。
	{
		v0 := randomField(rng, 16)
		vFlat := Flatten(v0, omega)
		afterFlat := MuStep(config.MuInitialMu, GainOf(vFlat, v0, omega)) // 抹平 ⟹ η = 1 ⟹ μ = 1
		beforeFlat := MuStep(config.MuInitialMu, GainOf(v0, v0, omega))   // η = 0 ⟹ μ 原地
		if afterFlat != 1 || beforeFlat != 0 {
			t.Fatalf("TD17-style witness: after flatten %v (want 1), before flatten %v (want 0)",
				afterFlat, beforeFlat)
		}
		A := Score(vFlat, v0, afterFlat) - Score(vFlat, v0, beforeFlat)
		if !(A > 0) {
			t.Fatalf("the constructive order dependence is not positive: A = %v", A)
		}
		want := config.MuScoreWeightMu/Ceiling()*OrderGap(config.MuInitialMu, 0) + config.MuScoreWeightClosed
		if !fltEqual(A, want) {
			t.Fatalf("A = %v, the theorem's closed form says %v", A, want)
		}
	}

	// 见证二(一般情形): 起点已经抹平过一次(η_before ∈ (0,1)), 差 = (1−μ)(1−η_before)。
	for trial := 0; trial < 50; trial++ {
		v0 := randomField(rng, 16)
		v := Flatten(v0, Cells{0, 1, 2, 3}) // 先做一次部分抹平, 让 η_before > 0
		muVal := rng.Float64() * 0.5

		vFlat, _ := muApply(v, muVal, KindFlatten, omega, v0, omega)
		_, afterFlat := muApply(vFlat, muVal, KindUpdateMu, omega, v0, omega)
		_, beforeFlat := muApply(v, muVal, KindUpdateMu, omega, v0, omega)

		A := Score(vFlat, v0, afterFlat) - Score(vFlat, v0, beforeFlat)
		want := config.MuScoreWeightMu/Ceiling()*OrderGap(muVal, GainOf(v, v0, omega)) + config.MuScoreWeightClosed
		if !(A > 0) {
			t.Fatalf("A = %v is not positive (mu=%v)", A, muVal)
		}
		if !fltEqual(A, want) {
			t.Fatalf("A = %v, closed form %v", A, want)
		}
	}
}

// --------------------------------------------------------------------------- 目标函数

// TestScoreStrictlyMonotoneInMu 是**构造性 A > 0 的前提**: 真目标函数必须随 μ 严格增长。
//
// 顺序差只改变 μ(场在两种次序下相同), 所以若 score 对 μ 不严格单调, 那个差就可能在
// 目标函数里被抹掉 —— 上一轮的教训正是"结构没进目标函数, 只在观测里"(四档 A 分布逐位
// 相同)。这条单测把"结构真的进目标函数"钉在编译单元里。
func TestScoreStrictlyMonotoneInMu(t *testing.T) {
	rng := rand.New(rand.NewSource(29))
	v0 := randomField(rng, 16)
	v := Flatten(v0, Cells{0, 1, 2, 3, 4, 5, 6, 7})

	prev := Score(v, v0, 0)
	for i := 1; i <= 1000; i++ {
		muVal := float64(i) / 1000
		got := Score(v, v0, muVal)
		if !(got > prev) {
			t.Fatalf("score is not strictly increasing in mu: score(%v) = %v <= previous %v",
				muVal, got, prev)
		}
		prev = got
	}
	// 天花板那一跳也是向上的(窗口关闭只加项)。
	below, above := Score(v, v0, Ceiling()-1e-12), Score(v, v0, Ceiling())
	if !(above > below) {
		t.Fatalf("the FC11 closure term must increase the score: %v -> %v", below, above)
	}
}

// TestScoreReadsTheField 证明 score 真的读场(不只是 μ 的函数)。
//
// 两个场在同一个 μ 下必须给出不同分数 —— 这是"抹平次序改变场 ⟹ A ≠ 0"的那条通道;
// 若 score 与场无关, 交换子(一个纯场的量)就永远读不出来。
func TestScoreReadsTheField(t *testing.T) {
	rng := rand.New(rand.NewSource(31))
	v0 := randomField(rng, 16)
	a := Score(v0, v0, 0.3)
	b := Score(Flatten(v0, Cells{0, 1, 2, 3, 4, 5, 6, 7}), v0, 0.3)
	if a == b {
		t.Fatalf("score does not read the field: both %v", a)
	}
	// 抹平全域 ⟹ Q = 0 ⟹ 增益项取满。
	full := Flatten(v0, AllCells(16))
	want := config.MuScoreWeightMu*(0.3/Ceiling()) + config.MuScoreWeightGain
	if got := Score(full, v0, 0.3); !fltEqual(got, want) {
		t.Fatalf("score at a flat field = %v, want %v", got, want)
	}
}

// TestCeilingMatchesDesignReview 把本包的 Ceiling 与判决层报出来的 MuCeiling 钉在一起:
// 复用不是一句口头声明, 两条路径给出的必须是同一个数。
func TestCeilingMatchesDesignReview(t *testing.T) {
	scope := design.Review(config.DefaultSpec(), physics.Metrics{},
		design.Input{DeviceScaleM: design.ARef, SourceKey: design.SourceMATBGN2Par, Eta: 0.05, Mu0: 0})
	if !fltEqual(Ceiling(), scope.MuCeiling) {
		t.Fatalf("Ceiling() = %v, design.Scope.MuCeiling = %v", Ceiling(), scope.MuCeiling)
	}
}

// TestReusedClosedFormsAreForwarded 钉住"窗口余量与关闭步复用 internal/design":
// 转发的值与 design 自己的输出逐位相同(不是重算出来的另一个数)。
func TestReusedClosedFormsAreForwarded(t *testing.T) {
	for _, muVal := range []float64{0, 0.2, 0.5, 0.999} {
		for _, eta := range []float64{0.05, 0.3, 0.9} {
			if got, want := WindowCloseStep(muVal, eta), design.WindowCloseStep(muVal, eta, design.Floor); got != want {
				t.Fatalf("WindowCloseStep(%v,%v) = %v, design says %v", muVal, eta, got, want)
			}
			if got, want := FirstClosedStep(muVal, eta), design.FirstClosedStep(muVal, eta, design.Floor); got != want {
				t.Fatalf("FirstClosedStep(%v,%v) = %d, design says %d", muVal, eta, got, want)
			}
		}
		if got, want := LockingFactor(muVal), design.LockingFactor(muVal); got != want {
			t.Fatalf("LockingFactor(%v) = %v, design says %v", muVal, got, want)
		}
		if got, want := WindowMargin(muVal), 1-muVal; got != want {
			t.Fatalf("WindowMargin(%v) = %v, want %v", muVal, got, want)
		}
	}
}

// TestDeclaredActionSpaceIsWellFormed 钉住声明式动作空间的形状:
// 每个区域一个抹平动作 + 一个 μ 更新动作, 线编码是 one-hot 且能被 Index 精确识别。
func TestDeclaredActionSpaceIsWellFormed(t *testing.T) {
	regions := DeclaredRegions(config.MuFieldCells)
	space := DeclaredActionSpace(regions)

	if space.Dim() != len(regions)+1 {
		t.Fatalf("action space has %d actions for %d regions, want %d", space.Dim(), len(regions), len(regions)+1)
	}
	if space.Keys[space.Dim()-1] != ActionKeyUpdateMu {
		t.Fatalf("the last declared action must be %q, got %q", ActionKeyUpdateMu, space.Keys[space.Dim()-1])
	}
	seen := map[string]bool{}
	for k, key := range space.Keys {
		if seen[key] {
			t.Fatalf("duplicate action key %q", key)
		}
		seen[key] = true
		if got, ok := space.Index(space.Vectors[k]); !ok || got != k {
			t.Fatalf("Index did not recognise its own vector %d", k)
		}
	}
	// 声明之外的动作必须被拒 —— 用一个真值但不是声明的向量探。
	if _, ok := space.Index([]float64{0.5, 0, 0, 0, 0, 0}); ok {
		t.Fatal("Index accepted a vector that is not one of the declared actions")
	}
	if _, ok := space.Index([]float64{1, 0}); ok {
		t.Fatal("Index accepted a vector of the wrong dimension")
	}
	// 区域族的三种关系都必须出现(否则"层状 vs 部分重叠"这个对照是空的)。
	relations := map[Relation]int{}
	for i := range regions {
		for j := i + 1; j < len(regions); j++ {
			relations[Relate(regions[i].Cells, regions[j].Cells)]++
		}
	}
	if relations[RelationPartialOverlap] == 0 {
		t.Fatal("the declared region family has no partially overlapping pair — the non-zero half is untestable")
	}
	if relations[RelationDisjoint] == 0 || relations[RelationNested] == 0 {
		t.Fatalf("the declared region family must contain disjoint and nested pairs too, got %v", relations)
	}
}

// sub 是逐分量差(测试内部小工具)。
func sub(a, b []float64) []float64 {
	out := make([]float64, len(a))
	for i := range a {
		out[i] = a[i] - b[i]
	}
	return out
}
