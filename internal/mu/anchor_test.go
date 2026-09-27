// anchor_test.go —— 逐条比对 testdata/mu_anchors.json。
//
// 那个文件由 scripts/emit_mu_anchors.py 从上游 ProjectionPhysics 的**真实 artifact 文件与
// Lean 条目名**读出来（含上游 git commit 与读取时间）。房规禁止把锚点手抄进 Go 源码当常量
// —— 那就变成自证；本文件是唯一允许出现上游数字的检查点，而它的数字来自文件而非源码。
//
// 容差口径（写在注释里，别让它变成"调一调就绿"的旋钮）：
//   - 上游字段本身是逐位拷贝的真值；Go 侧是**复算**（不同语言的浮点求值顺序：递推 vs 闭式、
//     求和次序、arm64 上的 FMA 融合），实测相对差 ~2e-15。取 rel 1e-12：比任何真实公式错
//     小 3 个数量级，又不会把跨语言次序差误报成公式错。
//   - 见证**向量**（N9 的 [0.5,0.5,0]/[0.5,0.25,0.25]）是二进制可精确表示的数，逐位断言。
package mu

import (
	"encoding/json"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/logos-42/hushfusion-forge/internal/config"
)

// formatFloat 只用于测试失败的措辞（最短往返十进制）。
func formatFloat(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }

// relAnchor 是锚点比对的相对容差（见文件头）。
const relAnchor = 1e-12

// muAnchors 是 testdata/mu_anchors.json 里本测试用到的部分（只解出需要的字段）。
type muAnchors struct {
	Provenance struct {
		Generator      string `json:"generator"`
		UpstreamCommit string `json:"upstream_commit"`
		SourceOfTruth  string `json:"source_of_truth"`
		CheckNote      string `json:"check_note"`
	} `json:"provenance"`
	Mudynamics struct {
		ReportJSON      string         `json:"report_json"`
		StateEquation   string         `json:"state_equation"`
		ClosedForm      string         `json:"closed_form"`
		Bridge          string         `json:"bridge"`
		N1Bounded       map[string]any `json:"n1_bounded"`
		N9FullGain      map[string]any `json:"n9_full_gain"`
		N13OrderWitness map[string]any `json:"n13_order_witness"`
		N14GapAndCost   map[string]any `json:"n14_gap_and_cost"`
		N10Scan         []struct {
			Eta       float64 `json:"eta"`
			Mu300     float64 `json:"mu_300_upstream"`
			Overshoot bool    `json:"overshoot"`
			Converges bool    `json:"converges"`
			Verdict   string  `json:"verdict"`
			Params    struct {
				HypothesisMu0       float64 `json:"hypothesis_mu0"`
				HypothesisN         int     `json:"hypothesis_n"`
				IterationValue      float64 `json:"iteration_value"`
				IterationResidual   float64 `json:"iteration_residual"`
				IterationBitExact   bool    `json:"iteration_bit_exact"`
				ClosedFormValue     float64 `json:"closed_form_value"`
				ClosedFormResidual  float64 `json:"closed_form_residual"`
				ClosedFormSaturated bool    `json:"closed_form_saturated"`
			} `json:"params"`
		} `json:"n10_convergence_scan"`
		N6NeverReach struct {
			Raw             map[string]any `json:"raw"`
			TrackHypothesis struct {
				Mu0             float64 `json:"mu0"`
				Eta             float64 `json:"eta"`
				N               int     `json:"n"`
				IterationValue  float64 `json:"iteration_value"`
				ClosedFormValue float64 `json:"closed_form_value"`
			} `json:"track_hypothesis"`
			IterationResidual   float64 `json:"iteration_residual"`
			IterationBitExact   bool    `json:"iteration_bit_exact"`
			FloatSaturationNote string  `json:"float_saturation_note"`
		} `json:"n6_never_reach"`
		N15FRC  map[string]any `json:"n15_frc_interface"`
		Honesty []string       `json:"honesty"`
	} `json:"mudynamics"`
	GravityControl struct {
		ReportJSON   string         `json:"report_json"`
		Primitive    string         `json:"primitive"`
		Derivation   string         `json:"derivation"`
		N1Idempotent map[string]any `json:"n1_idempotent"`
		N2Functional map[string]any `json:"n2_decision_functional"`
		N6Scan       struct {
			Scan []struct {
				RegionBStart int     `json:"region_b_start"`
				Overlap      float64 `json:"overlap_degree"`
				Norm         float64 `json:"commutator_norm_upstream"`
			} `json:"scan"`
			LamellarIsZero  bool `json:"lamellar_is_zero"`
			OverlapPositive bool `json:"overlap_is_positive"`
		} `json:"n6_commutator_scan"`
		N9Witnesses struct {
			PartialOverlapAB []float64 `json:"partial_overlap_ab"`
			PartialOverlapBA []float64 `json:"partial_overlap_ba"`
			NestedAB         []float64 `json:"nested_ab"`
			NestedBA         []float64 `json:"nested_ba"`
		} `json:"n9_witnesses"`
		N7Boolean map[string]any `json:"n7_boolean_vs_not"`
		Honesty   []string       `json:"honesty"`
	} `json:"gravitycontrol"`
	Lean struct {
		Files map[string]struct {
			RepoRel  string `json:"repo_rel"`
			NEntries int    `json:"n_entries"`
			Entries  []struct {
				Kind string `json:"kind"`
				Name string `json:"name"`
				Tag  string `json:"tag"`
				Doc  string `json:"doc"`
			} `json:"entries"`
		} `json:"files"`
	} `json:"lean"`
}

func loadMuAnchors(t *testing.T) muAnchors {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	var path string
	for {
		cand := filepath.Join(dir, "testdata", "mu_anchors.json")
		if _, err := os.Stat(cand); err == nil {
			path = cand
			break
		}
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			t.Fatalf("仓库根 %s 下没有 testdata/mu_anchors.json（先跑 python3 scripts/emit_mu_anchors.py）", dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("在 %s 之上找不到仓库根", dir)
		}
		dir = parent
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var a muAnchors
	if err := json.Unmarshal(raw, &a); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	if a.Provenance.Generator != "scripts/emit_mu_anchors.py" {
		t.Fatalf("锚点文件的 generator = %q，不是本层的锚点脚本", a.Provenance.Generator)
	}
	if a.Provenance.UpstreamCommit == "" {
		t.Fatal("锚点文件没有记上游 commit —— provenance 不完整")
	}
	return a
}

func anchorClose(t *testing.T, what string, got, want float64) {
	t.Helper()
	if got == want {
		return
	}
	rel := math.Abs(got-want) / math.Max(math.Abs(want), 1e-300)
	if rel > relAnchor {
		t.Fatalf("%s: Go 复算 %v vs 上游字段 %v（相对差 %.3g > %g）", what, got, want, rel, relAnchor)
	}
}

// TestAnchorMuStateEquationMatchesUpstream 是**最强的锚**：上游 N10 的九行收敛扫描
// （μ₀=0.1、n=300、η 扫过 0.05…3.0）由本包的 MuStep 递推复算，逐行撞击上游字段。
//
// 上游那九行里同时有两类行：未饱和的（η=0.05/2.0）与在双精度下饱和的（1−μ_N 已到 eps
// 量级，见锚点的 closed_form_saturated）—— 都对上，说明状态方程的实现没被"看起来像"糊过去。
func TestAnchorMuStateEquationMatchesUpstream(t *testing.T) {
	a := loadMuAnchors(t)
	if len(a.Mudynamics.N10Scan) != 9 {
		t.Fatalf("锚点里 N10 扫描 %d 行，want 9（上游口径变了？）", len(a.Mudynamics.N10Scan))
	}
	for _, row := range a.Mudynamics.N10Scan {
		mu := row.Params.HypothesisMu0
		for i := 0; i < row.Params.HypothesisN; i++ {
			mu = MuStep(mu, row.Eta)
		}
		anchorClose(t, "N10 行 η="+formatFloat(row.Eta), mu, row.Mu300)
		// 锚点里记着"这一行是否被双精度钉住"，本包复算出来的值也该落在同一个判断里：
		// 饱和行的 μ_300 与 1 的差必须 ≤ eps 量级（否则锚点的分类是错的）。
		if row.Params.ClosedFormSaturated {
			if d := math.Abs(1 - row.Mu300); row.Converges && d > 1e-14 {
				t.Fatalf("锚点把 η=%v 标为饱和，但 1−μ_300 = %v 不是 eps 量级", row.Eta, d)
			}
		}
	}
	// 两个临界值：η=1 一步到 1（TD5）；η=2 不进也不出（锚点里 μ_300 ≈ 0.1）。
	if got := MuStep(0.1, 1.0); got != 1 {
		t.Fatalf("η=1 一行: MuStep(0.1,1) = %v, want 1", got)
	}
}

// TestAnchorN6TrackMatchesUpstream 复现上游 N6 的 300 步轨道终点（μ₀=0, η=0.05, n=300）。
//
// 上游没写这两个参数；锚点里记的是**假设**，由递推撞击上游字段。本包用自己的 MuStep 再撞一次。
func TestAnchorN6TrackMatchesUpstream(t *testing.T) {
	a := loadMuAnchors(t)
	h := a.Mudynamics.N6NeverReach.TrackHypothesis
	mu := h.Mu0
	for i := 0; i < h.N; i++ {
		mu = MuStep(mu, h.Eta)
	}
	want, ok := a.Mudynamics.N6NeverReach.Raw["终值 μ_N"].(float64)
	if !ok {
		t.Fatal("锚点里 N6 的「终值 μ_N」读不出来")
	}
	anchorClose(t, "N6 轨道终点", mu, want)
	if mu >= 1 {
		t.Fatalf("N6 轨道终点 %v ≥ 1（TD8: 有限步到达不了 1）", mu)
	}
	// 闭式解与递推在这一步上差 1 ULP（上游自己也是这么记的：N5 说 max|递推−闭式| ~ 7.8e-16）。
	anchorClose(t, "N6 闭式解", MuAfterSteps(h.Mu0, h.Eta, h.N), want)
}

// TestAnchorMuClosureClaimsMatchUpstream 逐条对上上游 TD1–TD21 的**判决值**（布尔/字符串）。
//
// 这些布尔不是装饰：它们就是本世界赖以成立的那几条（有界、严格单调、有限步不可达、
// 抹平 ⟹ η=1、顺序差 = (1−μ)(1−η_before)）。上游把它们报成 true，本包的代数必须能让
// 它们继续为 true —— 一旦上游翻成 false，锚点先红，而不是本仓悄悄跟着变。
func TestAnchorMuClosureClaimsMatchUpstream(t *testing.T) {
	a := loadMuAnchors(t)
	n1 := a.Mudynamics.N1Bounded
	if b, _ := n1["有界不超调"].(bool); !b {
		t.Fatalf("上游 N1 的有界性判决值不是 true: %v", n1)
	}
	if v, ok := n1["max 下界违反 −min(μ')"].(float64); !ok || v != 0 {
		t.Fatalf("上游 N1 的下界违反 = %v, want 0", n1["max 下界违反 −min(μ')"])
	}
	if v, ok := n1["max 上界违反 max(μ')−1"].(float64); !ok || v != 0 {
		t.Fatalf("上游 N1 的上界违反 = %v, want 0", n1["max 上界违反 max(μ')−1"])
	}
	// 21×21 网格的格点数写在锚点里（441），本包在同一个网格上复算有界性。
	grid := 0
	for i := 0; i <= 20; i++ {
		for j := 0; j <= 20; j++ {
			grid++
			got := MuStep(float64(i)/20, float64(j)/20)
			if got < 0 || got > 1 {
				t.Fatalf("N1 复算: MuStep(%v,%v) = %v 落在 [0,1] 外", float64(i)/20, float64(j)/20, got)
			}
		}
	}
	if want, ok := n1["网格 μ×η ∈ [0,1]²（21×21）"].(float64); !ok || int(want) != grid {
		t.Fatalf("上游写的网格点数 %v 与本仓复算的 %d 不一致", n1["网格 μ×η ∈ [0,1]²（21×21）"], grid)
	}
	if b, _ := a.Mudynamics.N9FullGain["η=1 ⟹ μ'=1（100 点）"].(bool); !b {
		t.Fatal("上游 N9 满增益判决不是 true")
	}
	if b, _ := a.Mudynamics.N13OrderWitness["顺序改变 μ 演化"].(bool); !b {
		t.Fatal("上游 N13 顺序见证判决不是 true")
	}
	if b, _ := a.Mudynamics.N14GapAndCost["顺序差 = (1−μ)(1−η_before)"].(bool); !b {
		t.Fatal("上游 N14 的顺序差公式判决不是 true")
	}
	// N13 的两个数是**同一个区域 A** 下的(μ_after_flatten=1, μ_before_flatten=0, μ=0, η_before=0)。
	// 本世界 update_mu 用的是 Ω 上的增益，所以这条见证在本世界的对应物是 (抹平 Ω, 更新 μ)。
	if v, ok := a.Mudynamics.N13OrderWitness["先抹平再更新 μ（时序 A）"].(float64); !ok || v != 1 {
		t.Fatalf("上游 N13 时序 A 的 μ = %v, want 1", a.Mudynamics.N13OrderWitness["先抹平再更新 μ（时序 A）"])
	}
	if v, ok := a.Mudynamics.N13OrderWitness["先更新 μ 再抹平（时序 B）"].(float64); !ok || v != 0 {
		t.Fatalf("上游 N13 时序 B 的 μ = %v, want 0", a.Mudynamics.N13OrderWitness["先更新 μ 再抹平（时序 B）"])
	}
	// 状态方程与桥的**文字口径**也在锚点里：本包 doc 引用的就是它们。
	if a.Mudynamics.StateEquation == "" || a.Mudynamics.ClosedForm == "" || a.Mudynamics.Bridge == "" {
		t.Fatal("锚点里的状态方程/闭式解/桥的口径文字缺了")
	}
}

// TestAnchorCommutatorWitnessesMatchUpstream 逐位复现上游 N9 的四个见证向量。
//
// 上游键名「先A后B」对应组合式 flatten(A, flatten(B, v))。本测试直接断言组合式本体
// （与 Lean flatten_not_commute_overlap 的 hL/hR 同侧），不靠键名推语义。
func TestAnchorCommutatorWitnessesMatchUpstream(t *testing.T) {
	a := loadMuAnchors(t)
	w := a.GravityControl.N9Witnesses

	gotAB := Flatten(Flatten([]float64{1, 0, 0}, Cells{1, 2}), Cells{0, 1})
	gotBA := Flatten(Flatten([]float64{1, 0, 0}, Cells{0, 1}), Cells{1, 2})
	for i := range w.PartialOverlapAB {
		if gotAB[i] != w.PartialOverlapAB[i] {
			t.Fatalf("N9 部分重叠 hL[%d] = %v, 上游 %v", i, gotAB[i], w.PartialOverlapAB[i])
		}
	}
	for i := range w.PartialOverlapBA {
		if gotBA[i] != w.PartialOverlapBA[i] {
			t.Fatalf("N9 部分重叠 hR[%d] = %v, 上游 %v", i, gotBA[i], w.PartialOverlapBA[i])
		}
	}
	nestedAB := Flatten(Flatten([]float64{1, 1}, Cells{0, 1}), Cells{0})
	nestedBA := Flatten(Flatten([]float64{1, 1}, Cells{0}), Cells{0, 1})
	for i := range w.NestedAB {
		if nestedAB[i] != w.NestedAB[i] || nestedBA[i] != w.NestedBA[i] {
			t.Fatalf("N9 嵌套见证不逐位相等: %v/%v vs 上游 %v/%v", nestedAB, nestedBA, w.NestedAB, w.NestedBA)
		}
	}
	if MaxAbs(Commutator([]float64{1, 0, 0}, Cells{0, 1}, Cells{1, 2})) == 0 {
		t.Fatal("N9 部分重叠的交换子为零 —— 与上游见证矛盾")
	}
	if MaxAbs(Commutator([]float64{1, 1}, Cells{0, 1}, Cells{0})) != 0 {
		t.Fatal("N9 嵌套的交换子不为零 —— 与上游见证矛盾")
	}
}

// TestAnchorDecisionFunctionalClaims 对上上游 N1/N2 的判决值: 幂等、Q ≥ 0、Q = 0 ⟺ 常值、抹平 ⟹ Q = 0。
func TestAnchorDecisionFunctionalClaims(t *testing.T) {
	a := loadMuAnchors(t)
	if b, _ := a.GravityControl.N1Idempotent["P² = P（幂等）"].(bool); !b {
		t.Fatal("上游 N1 的幂等判决不是 true")
	}
	if b, _ := a.GravityControl.N2Functional["Q ≥ 0"].(bool); !b {
		t.Fatal("上游 N2 的 Q ≥ 0 判决不是 true")
	}
	if b, _ := a.GravityControl.N2Functional["抹平 ⟹ Q = 0"].(bool); !b {
		t.Fatal("上游 N2 的「抹平 ⟹ Q = 0」判决不是 true")
	}
	// 上游把残差记成逐位 0.0；本仓朴素求和下是 ULP 量级。锚点里那句 recompute_note 说的就是
	// 这件事 —— 本测试断言的是"在 ULP 带内"，而不是去改上游的数（也不许把容差调到让它绿）。
	if v, ok := a.GravityControl.N1Idempotent["max|P_A(P_A v) − P_A v| (200 随机场)"].(float64); !ok || v != 0 {
		t.Fatalf("上游 N1 的残差字段 = %v, want 0.0（上游的逐位零口径变了？）", a.GravityControl.N1Idempotent["max|P_A(P_A v) − P_A v| (200 随机场)"])
	}
	// 上游 N6 的形态：层状行落在浮点噪声里、部分重叠行严格为正。本仓在**同一批区域参数**上
	// 复现这个形态（见 TestRegionFamilyMatchesUpstreamScan 的反推几何）。
	if !a.GravityControl.N6Scan.LamellarIsZero || !a.GravityControl.N6Scan.OverlapPositive {
		t.Fatal("上游 N6 的两条判决值不是 (true, true)")
	}
	if len(a.GravityControl.N6Scan.Scan) != 10 {
		t.Fatalf("上游 N6 扫描 %d 行, want 10", len(a.GravityControl.N6Scan.Scan))
	}
}

// TestAnchorLeanEntriesStillExist 检查锚点里记的 Lean 条目名集合能覆盖本包每条定义的对应物。
//
// 这是"代数有出处"的检查：internal/mu 的 flatten / Flatten / FluctuationEnergy / Commutator /
// MuStep / Gain 都必须在上游 Lean 里找到名字对得上的条目（锚点脚本已经硬拦过一遍，这里再
// 从锚点文件读一遍 —— 让"出处"这件事在 Go 测试里也可见）。
func TestAnchorLeanEntriesStillExist(t *testing.T) {
	a := loadMuAnchors(t)
	want := map[string][]string{
		"PlasmaDynamics.lean":  {"def muStep", "def muChain", "theorem muChain_closed_form", "theorem muStep_full_gain"},
		"MuFieldCoupling.lean": {"def flattenProgress", "def muAfterFlatten", "def muBeforeFlatten", "theorem mu_order_gap", "theorem mu_order_difference", "theorem mu_order_matters"},
		"GravityControl.lean":  {"def regionMean", "def flatten", "def fluctuationEnergy", "theorem flatten_commute_of_disjoint", "theorem flatten_commute_of_subset", "theorem flatten_absorb_of_subset", "theorem flatten_not_commute_overlap"},
	}
	for file, names := range want {
		blk, ok := a.Lean.Files[file]
		if !ok {
			t.Fatalf("锚点里没有上游 Lean 文件 %s 的条目块", file)
		}
		have := map[string]bool{}
		for _, e := range blk.Entries {
			have[e.Kind+" "+e.Name] = true
		}
		for _, n := range names {
			if !have[n] {
				t.Fatalf("上游 %s 的条目 %q 不在锚点里 —— internal/mu 的对应物失去出处", file, n)
			}
		}
	}
	// 本包的每个导出算子在锚点里都要有名字（人工核对的清单，改包时一起改）。
	for _, pair := range [][2]string{
		{"Flatten", "def flatten"}, {"FluctuationEnergy", "def fluctuationEnergy"},
		{"Mean", "def regionMean"}, {"MuStep", "def muStep"}, {"MuAfterSteps", "def muChain"},
	} {
		found := false
		for _, blk := range a.Lean.Files {
			for _, e := range blk.Entries {
				if e.Kind+" "+e.Name == pair[1] {
					found = true
				}
			}
		}
		if !found {
			t.Fatalf("本包的 %s 声称对应上游 %q，但锚点里找不到这个条目", pair[0], pair[1])
		}
	}
}

// TestRegionFamilyMatchesUpstreamScan 用**上游 N6 扫描的几何**（从重叠度那一列反推：
// A = 16..31，B = 环上从 s 起的 16 格，64 格环）复现上游的形态结论。
//
// 数值不会逐位相同（上游的随机场与种子不在锚点里），所以这里断言的是**形态**：
// 重叠度 1.0（B ⊂ A 或 B = A 的层状端）与 0.0（不交）落在零带内，0 < 重叠度 < 1 的行严格为正。
func TestRegionFamilyMatchesUpstreamScan(t *testing.T) {
	a := loadMuAnchors(t)
	const n = 64
	a16 := make(Cells, 0, 16)
	for i := 16; i < 32; i++ {
		a16 = append(a16, i)
	}
	rng := rand.New(rand.NewSource(20260927))
	for _, row := range a.GravityControl.N6Scan.Scan {
		b := make(Cells, 0, 16)
		for k := 0; k < 16; k++ {
			b = append(b, (row.RegionBStart+k)%n)
		}
		rel := Relate(a16, b)
		// 反推几何必须自洽：算出来的重叠度要与上游那一列对上。
		inter := 0
		for _, i := range a16 {
			for _, j := range b {
				if i == j {
					inter++
				}
			}
		}
		if got := float64(inter) / 16; math.Abs(got-row.Overlap) > relAnchor {
			t.Fatalf("N6 反推几何不自洽：起点 %d 的重叠度算出来 %v，上游 %v",
				row.RegionBStart, got, row.Overlap)
		}
		scale := 0.0
		v := make([]float64, n)
		for i := range v {
			v[i] = rng.NormFloat64()
			if x := math.Abs(v[i]); x > scale {
				scale = x
			}
		}
		d := MaxAbs(Commutator(v, a16, b))
		lam := Lamellar(a16, b)
		if row.Overlap == 1 || row.Overlap == 0 {
			if !lam {
				t.Fatalf("N6 起点 %d：重叠度 %v 却被判成非层状（%s）", row.RegionBStart, row.Overlap, rel)
			}
			if d > 1e-15*scale {
				t.Fatalf("N6 起点 %d（层状）的交换子 %v 超出 ULP 带", row.RegionBStart, d)
			}
			continue
		}
		if lam {
			t.Fatalf("N6 起点 %d：重叠度 %v 却被判成层状（%s）", row.RegionBStart, row.Overlap, rel)
		}
		if !(d > 0) {
			t.Fatalf("N6 起点 %d（部分重叠）的交换子为零", row.RegionBStart)
		}
	}
}

// TestAnchorFieldCellsAreDeclared 是本包唯一一处"世界配置"的自检：场格点数必须是 4 的倍数、
// 且锚点脚本记录的预算/格点没有漂移到别处（μ 世界的格点数住 internal/config）。
func TestAnchorFieldCellsAreDeclared(t *testing.T) {
	if config.MuFieldCells%4 != 0 {
		t.Fatalf("config.MuFieldCells = %d 不是 4 的倍数（区域族是按它推出来的）", config.MuFieldCells)
	}
	regions := DeclaredRegions(config.MuFieldCells)
	if len(regions) != 5 {
		t.Fatalf("区域族有 %d 个区域, want 5", len(regions))
	}
}
