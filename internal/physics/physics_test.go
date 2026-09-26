package physics

import (
	"encoding/json"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/logos-42/hushfusion-forge/internal/config"
)

// ---------------------------------------------------------------------------
// 辅助函数
// ---------------------------------------------------------------------------

const (
	testdataDir = "../../testdata"

	// 来自 CONTRACT.md / api.go 的冻结容差
	tolAxis       = 1e-12 // 轴上闭式解对照教科书公式
	tolHelmholtz  = 1e-12 // Helmholtz 中心场
	uniHelmholtz  = 1.2e-4
	tolDiscrete   = 1e-9 // nSeg = 512 时离散和对照闭式解
	tolGoldenFld  = 1e-9 // golden_field_samples.json
	tolGoldenMet  = 1e-6 // golden_baseline.json 的度量
	tolVacuumId   = 1e-5 // 用中心差分算 div B / curl B
	discreteNSeg  = 512
	awayFromWireM = 5e-2 // 512 段锚点所要求的 "离导线较远"
)

func relDiff(got, want float64) float64 {
	return math.Abs(got-want) / math.Max(math.Abs(want), 1e-12)
}

func checkRel(t *testing.T, what string, got, want, tol float64) {
	t.Helper()
	checkRelScaled(t, what, got, want, 0, tol)
}

// zeroRoundoff 是参考值恰好为零时使用的绝对判据。|B| 是自然的尺度, 该量级的项在浮点
// 抵消后每次运算留下约 1e-16*|B| 的残差; 1e-14*|B| 留出两个数量级的余量, 同时仍然断言
// "这是舍入误差, 不是物理"。
const zeroRoundoff = 1e-14

// checkRelScaled 是用于 golden 场样本的相对比较。
//
// 其中一个 golden 条目恰好是 0.0, 因为该构型是对称的 (textbook_mirror 的第 28 个样本
// 位于 z 对称线圈组的中平面上, 于是 B_r 在参考实现里精确抵消)。相对判据对零参考值是
// 空洞的, 所以那个单例被表述为绝对判据: 该点处 |got| <= zeroRoundoff*|B|。每一个非零
// 分量仍然按冻结的 1e-9 做相对比较 —— 零分支从不适用于它们, 所以这不是对锚点的放宽。
func checkRelScaled(t *testing.T, what string, got, want, scale, tol float64) {
	t.Helper()
	if math.IsNaN(got) || math.IsInf(got, 0) {
		t.Errorf("%s: got %v, want %v", what, got, want)
		return
	}
	if want == 0 {
		abs := zeroRoundoff * scale
		if abs == 0 {
			abs = zeroRoundoff
		}
		if math.Abs(got) > abs {
			t.Errorf("%s: got %.17g want exactly 0; |value| > %.3e (= %.0e*|B|=%.3g)", what, got, abs, zeroRoundoff, scale)
		}
		return
	}
	if d := math.Abs(got-want) / math.Abs(want); d > tol {
		t.Errorf("%s: got %.17g want %.17g rel_diff=%.3e > %.3e", what, got, want, d, tol)
	}
}

func mustFinite(t *testing.T, what string, v float64) {
	t.Helper()
	if math.IsNaN(v) || math.IsInf(v, 0) {
		t.Errorf("%s is not finite: %v", what, v)
	}
}

func loadJSON(t *testing.T, name string, v any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(testdataDir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
}

func testCoils(x []float64) []Coil {
	k := len(x) / 3
	out := make([]Coil, k)
	for i := 0; i < k; i++ {
		out[i] = Coil{Radius: x[i], Z: x[k+i], Current: x[2*k+i]}
	}
	return out
}

// wireDistance 是从 (r, z) 到任一线圈导线的最小距离。
func wireDistance(coils []Coil, r, z float64) float64 {
	best := math.Inf(1)
	for _, c := range coils {
		if d := math.Hypot(r-c.Radius, z-c.Z); d < best {
			best = d
		}
	}
	return best
}

// ---------------------------------------------------------------------------
// golden 数据的结构
// ---------------------------------------------------------------------------

type goldenSamples struct {
	Samples []struct {
		DesignName string    `json:"design_name"`
		Design     []float64 `json:"design"`
		PointsR    []float64 `json:"points_r"`
		PointsZ    []float64 `json:"points_z"`
		Br         []float64 `json:"br"`
		Bz         []float64 `json:"bz"`
		BMag       []float64 `json:"b_mag"`
	} `json:"samples"`
}

type goldenMetrics struct {
	BMidT                 float64 `json:"B_mid_T"`
	BThroatT              float64 `json:"B_throat_T"`
	ZThroatM              float64 `json:"z_throat_m"`
	MirrorRatio           float64 `json:"mirror_ratio"`
	VolumeGood            float64 `json:"volume_good"`
	Ripple                float64 `json:"ripple"`
	BCoilMaxT             float64 `json:"B_coil_max_T"`
	MinCoilGapM           float64 `json:"min_coil_gap_m"`
	CostProxy             float64 `json:"cost_proxy"`
	CoilProximityFloorHit float64 `json:"coil_proximity_floor_hit"`
	NCoils                float64 `json:"n_coils"`
	MU0                   float64 `json:"mu0"`
}

type goldenBaseline struct {
	Design  []float64     `json:"design"`
	Metrics goldenMetrics `json:"metrics"`
	CostRef float64       `json:"cost_ref"`
	Score   float64       `json:"score"`
}

// ---------------------------------------------------------------------------
// 1. 椭圆积分 (AGM) 对照独立的 scipy 值
// ---------------------------------------------------------------------------

func TestEllipticKEAgainstScipy(t *testing.T) {
	// K(m)、E(m) 来自 scipy.special.ellipk / ellipe (即参考 oracle 自己的积分器);
	// Go 必须复现它们。
	cases := []struct {
		m, k, e float64
	}{
		{0.0, 1.5707963267948966, 1.5707963267948966},
		{0.01, 1.5747455615173558, 1.5668619420216683},
		{0.05, 1.591003453790792, 1.5509733517804725},
		{0.1, 1.6124413487202192, 1.5307576368977633},
		{0.25, 1.685750354812596, 1.4674622093394272},
		{0.5, 1.8540746773013719, 1.3506438810476755},
		{0.7, 2.075363135292469, 1.2416705679458229},
		{0.75, 2.156515647499643, 1.2110560275684594},
		{0.9, 2.5780921133481733, 1.1047747327040733},
		{0.95, 2.9083372484445515, 1.0604737277662784},
		{0.99, 3.6956373629898747, 1.015993545025224},
		{0.999, 4.841132560550296, 1.0021707908344453},
		{0.999999, 8.294051463601061, 1.0000038970261722},
		{0.999999999, 11.747927296421043, 1.0000000056239633},
		{1 - 1e-12, 15.201815980070121, 1.0000000000073508},
	}
	// 容差是冻结的 AGM 公式在双精度下实测并已理解的可达界限 —— 不是随手放宽的松弛量。
	// 它们比任何场锚点所需 (1e-9) 都紧得多, 存在的目的正是抓这个测试被写出来要抓的那
	// 一类 bug: 早先的一个修订版用绝对判据来停止迭代, 而那个判据永远不会触发
	// (见 magnet.go 的 keEps), 于是 E(0.5) 丢了 7.8e-14, 而一个宽松的 1e-12 门居然
	// 接受了它。
	//
	// 分区: E = K*(1 - sum), 当 m -> 1 时 sum 趋近 1, 于是这次减法丢掉约 1 位:
	// 下限是 eps/|1-sum|, 在 m = 1-1e-12 处达到约 2e-15。m = 0.99 以下没有这种抵消。
	worstK, worstE, worstEnear := 0.0, 0.0, 0.0
	for _, c := range cases {
		tol := 1e-15
		if c.m > 0.99 {
			tol = 5e-15
		}
		k, e := ellipticKE(c.m)
		checkRel(t, "K(m="+strconvF(c.m)+")", k, c.k, tol)
		checkRel(t, "E(m="+strconvF(c.m)+")", e, c.e, tol)
		worstK = math.Max(worstK, math.Abs(k-c.k)/c.k)
		if c.m > 0.99 {
			worstEnear = math.Max(worstEnear, math.Abs(e-c.e)/c.e)
		} else {
			worstE = math.Max(worstE, math.Abs(e-c.e)/c.e)
		}
	}
	t.Logf("worst vs scipy over %d m values: K %.2e, E(m<=0.99) %.2e, E(m>0.99) %.2e", len(cases), worstK, worstE, worstEnear)

	// m = 0 是精确的: K = E = pi/2。
	k0, e0 := ellipticKE(0)
	if k0 != math.Pi/2 || e0 != math.Pi/2 {
		t.Errorf("K(0)=%.17g E(0)=%.17g, want exactly pi/2=%.17g", k0, e0, math.Pi/2)
	}
	// 落在冻结定义域 m 属于 [0,1) 之外: 按极限上报, 绝不悄悄地给出一个有限数假装它是
	// 距 1 仅 1e-12 的 K。
	if k1, e1 := ellipticKE(1.0); !math.IsInf(k1, 1) || e1 != 1 {
		t.Errorf("K(1)=%v E(1)=%v, want +Inf/1 (K diverges, E(1)=1)", k1, e1)
	}
	if k2, _ := ellipticKE(1.5); !math.IsInf(k2, 1) {
		t.Errorf("K(1.5)=%v, want +Inf (out of domain saturates at m=1)", k2)
	}
}

func strconvF(v float64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// ---------------------------------------------------------------------------
// 2. 轴上锚点: B_z(0,z) = mu0*I*a^2 / (2*(a^2+z^2)^1.5), B_r(0,z) = 0
// ---------------------------------------------------------------------------

func TestOnAxisAnchor(t *testing.T) {
	worstAxis := 0.0
	radii := []float64{0.10, 0.3, 0.5, 1.0}
	currents := []float64{1.0e4, 4.6322263959687366e5, 2.5e6}
	zs := []float64{0, 1e-6, 0.25, 0.75, 1.0, 1.4, 3.7, -0.25, -1.0}
	for _, a := range radii {
		for _, i := range currents {
			for _, z := range zs {
				br, bz := LoopField(a, i, 0, z)
				if br != 0 {
					t.Errorf("B_r(0,%.3g) = %v, want exactly 0", z, br)
				}
				want := config.MU0 * i * a * a / (2 * math.Pow(a*a+z*z, 1.5))
				checkRel(t, "B_z(axis)", bz, want, tolAxis)
				mustFinite(t, "B_z(axis)", bz)
				worstAxis = math.Max(worstAxis, math.Abs(bz-want)/want)

				// OnAxisField 必须与一般求值器一致。
				got := onAxisField([]Coil{{Radius: a, Z: 0, Current: i}}, []float64{z})[0]
				checkRel(t, "OnAxisField", got, want, tolAxis)
			}
		}
	}
	t.Logf("on-axis anchor: worst rel diff over %d (a, I, z) combinations: %.3e (tol %.0e)",
		len(radii)*len(currents)*len(zs), worstAxis, tolAxis)

	// 叠加后的轴上场: 两个位于 +-0.25 的环 (一对类 Helmholtz 的环)。
	coils := []Coil{{Radius: 0.5, Z: -0.25, Current: 4.6322263959687366e5}, {Radius: 0.5, Z: 0.25, Current: 4.6322263959687366e5}}
	z := []float64{-1, 0, 0.5}
	got := onAxisField(coils, z)
	for idx, zi := range z {
		want := 0.0
		for _, c := range coils {
			dz := zi - c.Z
			want += config.MU0 * c.Current * c.Radius * c.Radius / (2 * math.Pow(c.Radius*c.Radius+dz*dz, 1.5))
		}
		checkRel(t, "onAxisField superposition", got[idx], math.Abs(want), tolAxis)
	}
}

// ---------------------------------------------------------------------------
// 3. Helmholtz 锚点
// ---------------------------------------------------------------------------

func TestHelmholtzAnchor(t *testing.T) {
	for _, c := range []struct{ a, i float64 }{{0.5, 1e6}, {0.3, 1.6e6}, {1.0, 2e6}} {
		coils := []Coil{{Radius: c.a, Z: -c.a / 2, Current: c.i}, {Radius: c.a, Z: c.a / 2, Current: c.i}}

		br, bz := CoilsetField(coils, []float64{0}, []float64{0})
		if br[0] != 0 {
			t.Errorf("Helmholtz centre B_r = %v, want 0 by symmetry", br[0])
		}
		got := math.Hypot(br[0], bz[0])
		want := math.Pow(4.0/5.0, 1.5) * config.MU0 * c.i / c.a
		checkRel(t, "Helmholtz centre field", got, want, tolHelmholtz)

		// |z| <= 0.1a 之内的不均匀度。
		const n = 201
		vals := make([]float64, n)
		zs := make([]float64, n)
		for k := 0; k < n; k++ {
			z := -0.1*c.a + 0.2*c.a*float64(k)/float64(n-1)
			zs[k] = z
			b1, b2 := CoilsetField(coils, []float64{0}, []float64{z})
			if b1[0] != 0 {
				t.Errorf("Helmholtz B_r(z=%.4g) = %v, want 0 by symmetry", z, b1[0])
			}
			vals[k] = math.Hypot(b1[0], b2[0])
		}
		lo, hi, sum := vals[0], vals[0], 0.0
		for _, v := range vals {
			lo = math.Min(lo, v)
			hi = math.Max(hi, v)
			sum += v
		}
		mean := sum / float64(n)
		ptpMean := (hi - lo) / mean
		ptpCtr := (hi - lo) / got
		t.Logf("a=%.2g: uniformity (max-min)/mean=%.6e (max-min)/B0=%.6e", c.a, ptpMean, ptpCtr)
		if ptpMean >= uniHelmholtz || ptpCtr >= uniHelmholtz {
			t.Errorf("Helmholtz non-uniformity over |z|<=0.1a: (max-min)/mean=%.6e (max-min)/B0=%.6e, want both < %.3e",
				ptpMean, ptpCtr, uniHelmholtz)
		}
	}
}

// ---------------------------------------------------------------------------
// 4. 独立的离散 Biot-Savart 求和对照闭式解
// ---------------------------------------------------------------------------

func TestDiscreteMatchesAnalytic(t *testing.T) {
	coils := []Coil{
		{Radius: 0.5, Z: -0.25, Current: 463222.63959687366},
		{Radius: 0.5, Z: 0.25, Current: 463222.63959687366},
		{Radius: 0.3, Z: -1.0, Current: 1621279.2385890577},
		{Radius: 0.3, Z: 1.0, Current: 1621279.2385890577},
	}
	// 一批扫描点, 过滤到 "离导线较远" (>= 5 cm, 见 TestDiscreteConvergence 中记录的
	// 诚实边界)。
	var rs, zs []float64
	for _, r := range []float64{0, 0.02, 0.05, 0.15, 0.25, 0.45, 0.75, 1.1, 2.0} {
		for _, z := range []float64{0, 0.1, 0.4, 0.8, 1.2, 1.6, -0.6, -1.35, 2.5} {
			rs = append(rs, r)
			zs = append(zs, z)
		}
	}
	var keepR, keepZ []float64
	for i := range rs {
		if wireDistance(coils, rs[i], zs[i]) >= awayFromWireM {
			keepR = append(keepR, rs[i])
			keepZ = append(keepZ, zs[i])
		}
	}
	if len(keepR) < 30 {
		t.Fatalf("test sweep degenerated: only %d points survived", len(keepR))
	}
	ana := AnalyticSolver{}.Magnitude(coils, keepR, keepZ)
	dis := DiscreteSolver{NSeg: discreteNSeg}.Magnitude(coils, keepR, keepZ)
	worst, worstAt := 0.0, -1
	for i := range keepR {
		d := relDiff(dis[i], ana[i])
		if d > worst {
			worst, worstAt = d, i
		}
	}
	t.Logf("nSeg=%d: %d points, worst rel diff %.3e at (r=%.4g, z=%.4g)", discreteNSeg, len(keepR), worst, keepR[worstAt], keepZ[worstAt])
	if worst >= tolDiscrete {
		t.Errorf("discrete vs analytic worst rel diff %.3e >= %.3e", worst, tolDiscrete)
	}

	// 对位于离导线较远处的 golden 采样点做同样的检查。
	var g goldenSamples
	loadJSON(t, "golden_field_samples.json", &g)
	n := 0
	for _, s := range g.Samples {
		coils := testCoils(s.Design)
		for i := range s.PointsR {
			if wireDistance(coils, s.PointsR[i], s.PointsZ[i]) < awayFromWireM {
				continue
			}
			n++
			a := AnalyticSolver{}.Magnitude(coils, s.PointsR[i:i+1], s.PointsZ[i:i+1])[0]
			d := DiscreteSolver{NSeg: discreteNSeg}.Magnitude(coils, s.PointsR[i:i+1], s.PointsZ[i:i+1])[0]
			if r := relDiff(d, a); r >= tolDiscrete {
				t.Errorf("%s point %d (r=%.4g,z=%.4g): discrete vs analytic rel diff %.3e >= %.3e",
					s.DesignName, i, s.PointsR[i], s.PointsZ[i], r, tolDiscrete)
			}
		}
	}
	t.Logf("golden sample points with wire distance >= %.3g m: %d", awayFromWireM, n)
}

// TestDiscreteConvergence 记录 512 段锚点从哪里开始不再成立: 中点求和按 nSeg 几何
// 收敛, 收敛率随采样点靠近导线而退化。在 5 cm 处误差已在机器精度; 在 1 cm 处需要
// ~1440 段。这就是 "< 1e-9 for nSeg >= 512" 这一说法的诚实边界, 是量出来的而非假定。
func TestDiscreteConvergence(t *testing.T) {
	const a, i = 0.5, 1.0e6
	coils := []Coil{{Radius: a, Current: i}}
	at := func(d float64, nSeg int) float64 {
		ana := AnalyticSolver{}.Magnitude(coils, []float64{a}, []float64{d})[0]
		dis := DiscreteSolver{NSeg: nSeg}.Magnitude(coils, []float64{a}, []float64{d})[0]
		return relDiff(dis, ana)
	}
	e128, e256, e512 := at(0.05, 128), at(0.05, 256), at(0.05, 512)
	t.Logf("wire distance 0.05 m: rel diff 128=%.3e 256=%.3e 512=%.3e", e128, e256, e512)
	if !(e128 > e256 && e256 > e512) {
		t.Errorf("discrete sum is not converging with nSeg: 128=%.3e 256=%.3e 512=%.3e", e128, e256, e512)
	}
	if e512 >= tolDiscrete {
		t.Errorf("at 5 cm from the wire, nSeg=512 rel diff %.3e >= %.3e", e512, tolDiscrete)
	}
	// 实测边界 (仅记录, 不作为通过/失败的门): 离导线这么近时 512 段锚点并**不**成立。
	close512 := at(0.01, 512)
	t.Logf("wire distance 0.01 m: rel diff 512=%.3e, 1440=%.3e (512 is below the 1e-9 anchor here)", close512, at(0.01, 1440))
	if close512 <= tolDiscrete {
		t.Logf("note: 512 segments now meet 1e-9 at 1 cm too (%s)", "geometry changed?")
	}
}

// ---------------------------------------------------------------------------
// 5. golden 场样本 (跨语言真值, 3 个设计 x 32 个点)
// ---------------------------------------------------------------------------

func TestGoldenFieldSamples(t *testing.T) {
	var g goldenSamples
	loadJSON(t, "golden_field_samples.json", &g)
	if len(g.Samples) != 3 {
		t.Fatalf("want 3 designs, got %d", len(g.Samples))
	}
	axisPoints, zeroCancels := 0, 0
	worst := 0.0
	for _, s := range g.Samples {
		if len(s.PointsR) != 32 || len(s.Br) != 32 || len(s.Bz) != 32 || len(s.BMag) != 32 {
			t.Fatalf("%s: want 32 samples, got r=%d br=%d bz=%d |B|=%d", s.DesignName, len(s.PointsR), len(s.Br), len(s.Bz), len(s.BMag))
		}
		coils, err := VectorToCoils(s.Design, config.DefaultSpec())
		if err != nil {
			t.Fatalf("%s: decode design: %v", s.DesignName, err)
		}
		// 设计必须已经是规范形式 (z 升序): golden 数据就是。
		for i := 1; i < len(coils); i++ {
			if coils[i].Z < coils[i-1].Z {
				t.Fatalf("%s: golden design is not z-sorted", s.DesignName)
			}
		}
		mag := AnalyticSolver{}.Magnitude(coils, s.PointsR, s.PointsZ)
		for i := range s.PointsR {
			br, bz := CoilsetField(coils, s.PointsR[i:i+1], s.PointsZ[i:i+1])
			mustFinite(t, "B_r", br[0])
			mustFinite(t, "B_z", bz[0])
			mustFinite(t, "|B|", mag[i])
			if s.PointsR[i] == 0 {
				axisPoints++
				if br[0] != 0 {
					t.Errorf("%s point %d: B_r on axis = %v, want 0", s.DesignName, i, br[0])
				}
			}
			checkRelScaled(t, s.DesignName+" B_r["+strconvF(float64(i))+"]", br[0], s.Br[i], s.BMag[i], tolGoldenFld)
			checkRelScaled(t, s.DesignName+" B_z["+strconvF(float64(i))+"]", bz[0], s.Bz[i], s.BMag[i], tolGoldenFld)
			checkRelScaled(t, s.DesignName+" |B|["+strconvF(float64(i))+"]", mag[i], s.BMag[i], s.BMag[i], tolGoldenFld)
			if s.Br[i] == 0 {
				// golden 值精确为零 (对称性精确抵消): checkRelScaled 内部的检查
				// 要求残差处于舍入水平。
				zeroCancels++
			} else {
				worst = math.Max(worst, math.Abs(br[0]-s.Br[i])/math.Abs(s.Br[i]))
			}
			worst = math.Max(worst, math.Abs(bz[0]-s.Bz[i])/math.Abs(s.Bz[i]))
			worst = math.Max(worst, math.Abs(mag[i]-s.BMag[i])/s.BMag[i])
		}
	}
	if axisPoints != 12 {
		t.Errorf("want 12 on-axis golden samples (3 designs x 4), got %d", axisPoints)
	}
	t.Logf("golden_field_samples.json: worst rel diff over 96 points x 3 components: %.3e (tol %.0e)", worst, tolGoldenFld)
	t.Logf("golden components with an exactly-zero reference: %d (checked at %.0e*|B| roundoff level)", zeroCancels, zeroRoundoff)
}

// ---------------------------------------------------------------------------
// 6. golden 基线度量 (每个场都 < 1e-6 相对)
// ---------------------------------------------------------------------------

func TestGoldenBaselineMetrics(t *testing.T) {
	var gb goldenBaseline
	loadJSON(t, "golden_baseline.json", &gb)
	spec := config.DefaultSpec()
	coils, err := VectorToCoils(gb.Design, spec)
	if err != nil {
		t.Fatalf("decode baseline design: %v", err)
	}
	// golden 的设计就是该基线的规范编码。
	round := CoilsToVector(coils)
	if len(round) != len(gb.Design) {
		t.Fatalf("round trip length %d != %d", len(round), len(gb.Design))
	}
	for i := range round {
		checkRel(t, "canonical round trip["+strconvF(float64(i))+"]", round[i], gb.Design[i], 1e-12)
	}

	grids := BuildGrids(spec)
	m := MetricsFor(coils, spec, grids, AnalyticSolver{})
	t.Logf("golden baseline metrics: worst rel diff %.3e (tol %.0e)", checkGoldenMetrics(t, "textbook_mirror", m, gb.Metrics), tolGoldenMet)
	// 基线的 cost_proxy 就是目标函数的成本参考值。
	checkRel(t, "cost_ref", m.CostProxy, gb.CostRef, tolGoldenMet)
}

// checkGoldenMetrics 比较每一个度量并返回所见到的最差相对差值 (这样调用方可以记录
// 证据, 而不只是通过/失败)。
func checkGoldenMetrics(t *testing.T, name string, got Metrics, want goldenMetrics) float64 {
	t.Helper()
	pairs := [][2]float64{
		{got.BMidT, want.BMidT}, {got.BThroatT, want.BThroatT}, {got.ZThroatM, want.ZThroatM},
		{got.MirrorRatio, want.MirrorRatio}, {got.VolumeGood, want.VolumeGood}, {got.Ripple, want.Ripple},
		{got.BCoilMaxT, want.BCoilMaxT}, {got.MinCoilGapM, want.MinCoilGapM}, {got.CostProxy, want.CostProxy},
		{got.MU0, want.MU0},
	}
	worst := 0.0
	for _, pr := range pairs {
		worst = math.Max(worst, relDiff(pr[0], pr[1]))
	}
	checkRel(t, name+" B_mid_T", got.BMidT, want.BMidT, tolGoldenMet)
	checkRel(t, name+" B_throat_T", got.BThroatT, want.BThroatT, tolGoldenMet)
	checkRel(t, name+" z_throat_m", got.ZThroatM, want.ZThroatM, tolGoldenMet)
	checkRel(t, name+" mirror_ratio", got.MirrorRatio, want.MirrorRatio, tolGoldenMet)
	checkRel(t, name+" volume_good", got.VolumeGood, want.VolumeGood, tolGoldenMet)
	checkRel(t, name+" ripple", got.Ripple, want.Ripple, tolGoldenMet)
	checkRel(t, name+" B_coil_max_T", got.BCoilMaxT, want.BCoilMaxT, tolGoldenMet)
	checkRel(t, name+" min_coil_gap_m", got.MinCoilGapM, want.MinCoilGapM, tolGoldenMet)
	checkRel(t, name+" cost_proxy", got.CostProxy, want.CostProxy, tolGoldenMet)
	if got.NCoils != int(want.NCoils) {
		t.Errorf("%s n_coils: got %d want %v", name, got.NCoils, want.NCoils)
	}
	checkRel(t, name+" mu0", got.MU0, want.MU0, tolGoldenMet)
	hit := 0.0
	if got.CoilProximityFloorHit {
		hit = 1.0
	}
	if hit != want.CoilProximityFloorHit {
		t.Errorf("%s coil_proximity_floor_hit: got %v want %v", name, got.CoilProximityFloorHit, want.CoilProximityFloorHit)
	}
	return worst
}

// TestCrossLanguageMetrics 用另外三个设计, 它们的度量由受保护的 Python 参考实现
// (forge/physics/plasma_model.py, commit 4375c9a) 用解析 solver 在同一个 spec/grids 上
// 产生。它们走过仅靠基线覆盖不到的路径: 非零 ripple、volume_good = 1.0 以及带波纹的
// 多元胞内部。
func TestCrossLanguageMetrics(t *testing.T) {
	type ref struct {
		name string
		x    []float64
		m    goldenMetrics
	}
	refs := []ref{
		{
			name: "coils_in_cell_A",
			x:    []float64{0.3, 0.4, 0.6, 0.5, -0.9, -0.3, 0.3, 0.9, 8.0e5, 3.0e5, 5.0e5, 9.0e5},
			m: goldenMetrics{
				BMidT: 0.8132689603561428, BThroatT: 1.8259746994719952, ZThroatM: -0.8925,
				MirrorRatio: 2.2452285633431446, VolumeGood: 0.6736596736596736, Ripple: 0.0890189860061299,
				BCoilMaxT: 3.4513149709589594, MinCoilGapM: 0.608276253029822, CostProxy: 7.83e11,
				NCoils: 4, MU0: 1.2566370614359173e-06,
			},
		},
		{
			name: "wide_asym_C",
			x:    []float64{0.2, 0.55, 0.8, 0.95, -1.1, -0.15, 0.3, 1.05, 8.0e5, 2.5e5, 1.2e5, 9.0e5},
			m: goldenMetrics{
				BMidT: 0.524075026507872, BThroatT: 2.5990432628962834, ZThroatM: -1.1025,
				MirrorRatio: 4.959296153100025, VolumeGood: 1.0, Ripple: 0.31996748518434365,
				BCoilMaxT: 3.424267042458956, MinCoilGapM: 0.51478150704935, CostProxy: 9.43395e11,
				NCoils: 4, MU0: 1.2566370614359173e-06,
			},
		},
		{
			name: "dip_D",
			x:    []float64{0.45, 0.2, 0.7, 0.33, -0.6, -0.1, 0.55, 0.95, 1.5e6, 5.0e4, 2.3e6, 7.7e5},
			m: goldenMetrics{
				BMidT: 1.6505354646115262, BThroatT: 2.953156361023085, ZThroatM: 0.875,
				MirrorRatio: 1.7892110920004658, VolumeGood: 0.46153846153846156, Ripple: 0.48029371098979323,
				BCoilMaxT: 4.561209231479859, MinCoilGapM: 0.544885309033011, CostProxy: 4.911657e12,
				NCoils: 4, MU0: 1.2566370614359173e-06,
			},
		},
	}
	spec := config.DefaultSpec()
	grids := BuildGrids(spec)
	for _, r := range refs {
		coils, err := VectorToCoils(r.x, spec)
		if err != nil {
			t.Fatalf("%s: decode: %v", r.name, err)
		}
		m := MetricsFor(coils, spec, grids, AnalyticSolver{})
		t.Logf("%s: worst rel diff %.3e (ripple=%.6g)", r.name, checkGoldenMetrics(t, r.name, m, r.m), m.Ripple)
		if r.m.Ripple == 0 {
			t.Errorf("%s: this reference design was picked because its ripple is non-zero", r.name)
		}
	}
}

// ---------------------------------------------------------------------------
// 7. 真空恒等式: 远离导体处 div B = 0 与 curl B = 0
// ---------------------------------------------------------------------------

func TestVacuumIdentities(t *testing.T) {
	coils := testCoils([]float64{
		0.3, 0.5, 0.5, 0.3, -1.0, -0.25, 0.25, 1.0,
		1621279.2385890577, 463222.63959687366, 463222.63959687366, 1621279.2385890577,
	})
	// 小步长的中心差分: 截断误差 ~ h^2, 舍入误差 ~ eps/h。跳过 z = 0 (由对称性, 两个
	// 恒等式在那里都退化成 0/0, 它们的项归一化也消失)。
	const h = 1e-5
	at := func(r, z float64) (float64, float64) {
		br, bz := CoilsetField(coils, []float64{r}, []float64{z})
		return br[0], bz[0]
	}
	points := [][2]float64{
		{0.02, 0.9}, {0.05, 0.3}, {0.1, 0.7}, {0.2, 0.3}, {0.25, -0.5}, {0.4, 0.6}, {0.6, 1.1},
	}
	for _, p := range points {
		r, z := p[0], p[1]
		if d := wireDistance(coils, r, z); d < 0.05 {
			t.Fatalf("test point (%.3g,%.3g) is %.3g m from a wire", r, z, d)
		}
		brP, _ := at(r+h, z)
		brM, _ := at(r-h, z)
		_, bzP := at(r, z+h)
		_, bzM := at(r, z-h)
		brZP, _ := at(r, z+h)
		brZM, _ := at(r, z-h)
		_, bzRP := at(r+h, z)
		_, bzRM := at(r-h, z)

		// div B = (1/r) d(r B_r)/dr + dB_z/dz
		t1 := ((r+h)*brP - (r-h)*brM) / (2 * h * r)
		t2 := (bzP - bzM) / (2 * h)
		divB := t1 + t2
		// curl B (方位角分量) = dB_r/dz - dB_z/dr
		t3 := (brZP - brZM) / (2 * h)
		t4 := (bzRP - bzRM) / (2 * h)
		curlB := t3 - t4

		_, bz0 := at(r, z)
		br0, _ := at(r, z)
		bMag := math.Hypot(br0, bz0)

		relDiv := math.Abs(divB) / (math.Abs(t1) + math.Abs(t2))
		relCurl := math.Abs(curlB) / (math.Abs(t3) + math.Abs(t4))
		// 第二种独立归一化: 残差相对于一个梯度长度
		scaleDiv := math.Abs(divB) * 0.5 / bMag
		scaleCurl := math.Abs(curlB) * 0.5 / bMag
		if relDiv >= tolVacuumId || relCurl >= tolVacuumId || scaleDiv >= tolVacuumId || scaleCurl >= tolVacuumId {
			t.Errorf("vacuum identity at (r=%.3g,z=%.3g): rel_div=%.3e rel_curl=%.3e div*L/B=%.3e curl*L/B=%.3e (tol %.0e)",
				r, z, relDiv, relCurl, scaleDiv, scaleCurl, tolVacuumId)
		}
		t.Logf("(r=%.3g,z=%.3g) rel_div=%.3e rel_curl=%.3e", r, z, relDiv, relCurl)
	}
}

// ---------------------------------------------------------------------------
// 8. 退化几何: proximity floor、有限度量、无 NaN
// ---------------------------------------------------------------------------

func TestNearWireAndProximityFloor(t *testing.T) {
	const a, i = 0.5, 1.0e6

	// 一个正好坐在导线上的采样点, 以及恰在 floor 内/外两侧的几个采样点。
	for _, d := range []float64{0, 1e-12, 1e-9, 1e-6, 1e-3, 4.9e-3, 5e-3, 1e-2} {
		r, z := a-d, 0.0
		br, bz := LoopField(a, i, r, z)
		mustFinite(t, "B_r near wire", br)
		mustFinite(t, "B_z near wire", bz)
		t.Logf("wire gap %.1e: B_r=%.6g B_z=%.6g", d, br, bz)
	}

	// 两个相距 1 mm 的线圈: 闭式解在邻线圈的位置上是奇异的, 所以 floor 必须让度量保持
	// 有限并置上那个标志。
	close := []Coil{{Radius: 0.5, Z: 0, Current: 1.0e6}, {Radius: 0.5, Z: 1e-3, Current: 1.0e6}}
	spec := config.DefaultSpec()
	grids := BuildGrids(spec)
	m := MetricsFor(close, spec, grids, AnalyticSolver{})
	for _, v := range []float64{m.BMidT, m.BThroatT, m.ZThroatM, m.MirrorRatio, m.VolumeGood, m.Ripple, m.BCoilMaxT, m.MinCoilGapM, m.CostProxy} {
		mustFinite(t, "metric of a sub-floor coil pair", v)
	}
	if !m.CoilProximityFloorHit {
		t.Errorf("1 mm coil separation must set CoilProximityFloorHit")
	}

	// 完全重合的线圈 (golden 的 "single_loop" 设计有四个环在 (0.35 m, 0 m)):
	// alpha2 -> 0 与 (a^2 - r^2 - z^2) -> 0 同时发生。这里的值是被钳住的值, 所以只检查
	// 有限性 —— 受保护的 Python 参考实现不做钳制, 它不是这种情况的参考。
	coincident := []Coil{
		{Radius: 0.35, Current: 3e5}, {Radius: 0.35, Current: 3e5},
		{Radius: 0.35, Current: 3e5}, {Radius: 0.35, Current: 3e5},
	}
	mc := MetricsFor(coincident, spec, grids, AnalyticSolver{})
	for _, v := range []float64{mc.BMidT, mc.BThroatT, mc.ZThroatM, mc.MirrorRatio, mc.VolumeGood, mc.Ripple, mc.BCoilMaxT, mc.MinCoilGapM, mc.CostProxy} {
		mustFinite(t, "metric of coincident coils", v)
	}
	if !mc.CoilProximityFloorHit {
		t.Errorf("coincident coils must set CoilProximityFloorHit")
	}
	if mc.MinCoilGapM != 0 {
		t.Errorf("coincident coils gap = %v, want 0", mc.MinCoilGapM)
	}

	// 一个位于盒内、明显合法的设计不得触发该标志。
	legal := []Coil{{Radius: 0.5, Z: -0.25, Current: 4e5}, {Radius: 0.5, Z: 0.25, Current: 4e5}, {Radius: 0.3, Z: -1.0, Current: 1.5e6}, {Radius: 0.3, Z: 1.0, Current: 1.5e6}}
	if ml := MetricsFor(legal, spec, grids, AnalyticSolver{}); ml.CoilProximityFloorHit {
		t.Errorf("a legal design must not report CoilProximityFloorHit")
	}
}

// TestDegenerateInputs 钉住搜索确实可能产生的输入上的行为: 单线圈设计, 以及完全没有
// 采样点的网格。
func TestDegenerateInputs(t *testing.T) {
	spec := config.DefaultSpec()
	grids := BuildGrids(spec)

	one := []Coil{{Radius: 0.5, Z: 0, Current: 1.0e6}}
	m := MetricsFor(one, spec, grids, AnalyticSolver{})
	// 按契约, 少于两个线圈时 MinCoilGapM 是 +Inf; 其它每个度量都必须是有限数。
	for _, v := range []float64{m.BMidT, m.BThroatT, m.ZThroatM, m.MirrorRatio, m.VolumeGood, m.Ripple, m.BCoilMaxT, m.CostProxy} {
		mustFinite(t, "single-coil metric", v)
	}
	// 没有别的线圈 -> 没有来自邻居的导体场, 只有绕组包锚点。
	checkRel(t, "single-coil B_coil_max", m.BCoilMaxT, spec.SelfField(), 1e-15)
	if !math.IsInf(m.MinCoilGapM, 1) {
		t.Errorf("single coil gap = %v, want +Inf", m.MinCoilGapM)
	}
	if m.NCoils != 1 || m.CoilProximityFloorHit {
		t.Errorf("single coil: n_coils=%d floor_hit=%v, want 1/false", m.NCoils, m.CoilProximityFloorHit)
	}

	// 空网格: 一个文档化的提前退出, 不 panic, 不出 NaN。
	empty := MetricsFor(one, spec, Grids{}, AnalyticSolver{})
	for _, v := range []float64{empty.BMidT, empty.BThroatT, empty.ZThroatM, empty.MirrorRatio, empty.VolumeGood, empty.Ripple, empty.BCoilMaxT, empty.CostProxy} {
		mustFinite(t, "empty-grid metric", v)
	}
	if empty.BMidT != 0 || empty.CostProxy != 0 {
		t.Errorf("empty grid should give the zero metric, got B_mid=%v cost=%v", empty.BMidT, empty.CostProxy)
	}

	// 完全没有线圈。
	none := MetricsFor(nil, spec, grids, AnalyticSolver{})
	if none.NCoils != 0 || none.BCoilMaxT != 0 {
		t.Errorf("empty coil set: n_coils=%d B_coil_max=%v, want 0/0", none.NCoils, none.BCoilMaxT)
	}
	mustFinite(t, "empty coil set B_mid", none.BMidT)
}

// TestMetricAgreementAcrossSolvers 用独立的离散 solver 重跑整条度量流水线。两个 solver
// 在 Solver 接口之下不共享任何代码, 所以度量一致是对度量定义本身 (切片、体积分数、
// 导体场叠加) 的检查, 而不只是对场的检查。
func TestMetricAgreementAcrossSolvers(t *testing.T) {
	spec := config.DefaultSpec()
	grids := BuildGrids(spec)
	var gb goldenBaseline
	loadJSON(t, "golden_baseline.json", &gb)
	coils, err := VectorToCoils(gb.Design, spec)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	a := MetricsFor(coils, spec, grids, AnalyticSolver{})
	d := MetricsFor(coils, spec, grids, DiscreteSolver{NSeg: 1440})
	for _, c := range []struct {
		name string
		x, y float64
	}{
		{"B_mid_T", d.BMidT, a.BMidT},
		{"B_throat_T", d.BThroatT, a.BThroatT},
		{"mirror_ratio", d.MirrorRatio, a.MirrorRatio},
		{"volume_good", d.VolumeGood, a.VolumeGood},
		{"ripple", d.Ripple, a.Ripple},
		{"B_coil_max_T", d.BCoilMaxT, a.BCoilMaxT},
		{"min_coil_gap_m", d.MinCoilGapM, a.MinCoilGapM},
		{"cost_proxy", d.CostProxy, a.CostProxy},
	} {
		checkRel(t, "discrete vs analytic "+c.name, c.x, c.y, tolGoldenMet)
		t.Logf("%s: discrete=%.12g analytic=%.12g rel=%.2e", c.name, c.x, c.y, relDiff(c.x, c.y))
	}
	if d.CoilProximityFloorHit != a.CoilProximityFloorHit {
		t.Errorf("floor flag differs between solvers: %v vs %v", d.CoilProximityFloorHit, a.CoilProximityFloorHit)
	}
	// |z_throat| 必须一致: 一个 z 对称线圈组的 throat 在两个彼此相差 ~1e-16 的采样
	// 位置上取得, 所以 argmax 返回其中哪一个只是舍入级的并列打破, 不是物理差异。
	checkRel(t, "discrete vs analytic |z_throat_m|", math.Abs(d.ZThroatM), math.Abs(a.ZThroatM), tolGoldenMet)
	if d.ZThroatM != a.ZThroatM {
		t.Logf("z_throat tie-break: discrete=%.17g analytic=%.17g (equal up to roundoff; |z| agrees)", d.ZThroatM, a.ZThroatM)
	}
}

// ---------------------------------------------------------------------------
// 9. 每次 MetricsFor 只有一次堆叠采样 pass
// ---------------------------------------------------------------------------

type countingSolver struct {
	inner      Solver
	stack      []float64
	calls      int
	lens       []int
	stackCalls int
}

func (c *countingSolver) Name() string { return c.inner.Name() }

func (c *countingSolver) Magnitude(coils []Coil, r, z []float64) []float64 {
	c.calls++
	c.lens = append(c.lens, len(r))
	if len(r) == len(c.stack) {
		c.stackCalls++
	}
	return c.inner.Magnitude(coils, r, z)
}

func TestSingleStackSamplingPass(t *testing.T) {
	var gb goldenBaseline
	loadJSON(t, "golden_baseline.json", &gb)
	spec := config.DefaultSpec()
	coils, err := VectorToCoils(gb.Design, spec)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	grids := BuildGrids(spec)
	cnt := &countingSolver{inner: AnalyticSolver{}, stack: grids.StackR}
	m := MetricsFor(coils, spec, grids, cnt)

	if cnt.stackCalls != 1 {
		t.Errorf("field evaluated over the full stack %d times, want exactly 1 (lens=%v)", cnt.stackCalls, cnt.lens)
	}
	// K 次导体场调用, 每次覆盖另外 K-1 个线圈的位置。
	wantCalls := 1 + len(coils)
	if cnt.calls != wantCalls {
		t.Errorf("MetricsFor made %d field calls, want %d (lens=%v)", cnt.calls, wantCalls, cnt.lens)
	}
	for _, n := range cnt.lens[1:] {
		if n != len(coils)-1 {
			t.Errorf("conductor-field call over %d points, want %d", n, len(coils)-1)
		}
	}
	// 计数包装器委托给同一个 solver, 所以度量值必须与一次普通运行逐位相同 (包装器只
	// 负责计数)。同时也按冻结的 1e-6 与 golden 比较。
	plain := MetricsFor(coils, spec, grids, AnalyticSolver{})
	if m != plain {
		t.Errorf("counting wrapper changed the metrics:\n got %+v\nwant %+v", m, plain)
	}
	t.Logf("counting solver: worst rel diff vs golden %.3e (tol %.0e)", checkGoldenMetrics(t, "counting solver", m, gb.Metrics), tolGoldenMet)
}

// ---------------------------------------------------------------------------
// 10. 网格: 布局、数量与次序
// ---------------------------------------------------------------------------

func TestBuildGridsLayout(t *testing.T) {
	spec := config.DefaultSpec()
	g := BuildGrids(spec)
	nMid := spec.NVolR * 5
	nCell := spec.NVolR * spec.NVolZ
	if len(g.StackR) != spec.NAxis+nMid+nCell || len(g.StackZ) != spec.NAxis+nMid+nCell {
		t.Fatalf("stack length %d/%d, want %d", len(g.StackR), len(g.StackZ), spec.NAxis+nMid+nCell)
	}
	if g.NAxis != spec.NAxis || g.NMid != nMid {
		t.Fatalf("NAxis=%d NMid=%d, want %d/%d", g.NAxis, g.NMid, spec.NAxis, nMid)
	}
	if len(g.MidR) != nMid || len(g.CellR) != nCell || len(g.CellZ) != nCell {
		t.Fatalf("mid/cell aux lengths %d/%d/%d, want %d/%d/%d", len(g.MidR), len(g.CellR), len(g.CellZ), nMid, nCell, nCell)
	}
	// 轴上块: r = 0, z = linspace(-ZAxisMax, ZAxisMax, NAxis) 且端点精确。
	if g.StackR[0] != 0 || g.StackR[spec.NAxis-1] != 0 {
		t.Errorf("axis block is not r = 0")
	}
	if g.AxisZ[0] != -spec.ZAxisMax || g.AxisZ[spec.NAxis-1] != spec.ZAxisMax {
		t.Errorf("axis endpoints %.17g..%.17g, want exactly +-%.17g", g.AxisZ[0], g.AxisZ[spec.NAxis-1], spec.ZAxisMax)
	}
	for i, z := range g.StackZ[:spec.NAxis] {
		if z != g.AxisZ[i] || g.StackR[i] != 0 {
			t.Fatalf("axis sample %d: stack=(%v,%v) axis z=%v", i, g.StackR[i], z, g.AxisZ[i])
		}
	}
	// 对称跨度: 奇数个数时 numpy.linspace 恰好落在 0 上。
	if g.AxisZ[spec.NAxis/2] != 0 {
		t.Errorf("axis centre sample = %v, want exactly 0", g.AxisZ[spec.NAxis/2])
	}
	// 中平面块: meshgrid(r, z, "ij") -> 半径最慢, 每个半径 5 个 z。
	for i := 0; i < spec.NVolR; i++ {
		for j := 0; j < 5; j++ {
			k := spec.NAxis + i*5 + j
			wantR := spec.RPlasma * float64(i) / float64(spec.NVolR-1)
			if math.Abs(g.StackR[k]-wantR) > 1e-15 {
				t.Fatalf("mid sample %d: r=%v want %v", k, g.StackR[k], wantR)
			}
			wantZ := -spec.ZMid + 2*spec.ZMid*float64(j)/4
			if math.Abs(g.StackZ[k]-wantZ) > 1e-15 {
				t.Fatalf("mid sample %d: z=%v want %v", k, g.StackZ[k], wantZ)
			}
			if g.MidR[k-spec.NAxis] != g.StackR[k] {
				t.Fatalf("MidR not aligned with the midplane block at %d", k)
			}
		}
	}
	// 第一个中平面采样点是中平面上的 r=0; 元胞块紧接着它以同样的半径开始。
	if g.StackR[spec.NAxis] != 0 || g.StackZ[spec.NAxis] != -spec.ZMid {
		t.Errorf("first midplane sample = (%v,%v), want (0,%v)", g.StackR[spec.NAxis], g.StackZ[spec.NAxis], -spec.ZMid)
	}
	if g.StackR[spec.NAxis+nMid] != 0 || math.Abs(g.StackZ[spec.NAxis+nMid]+spec.ZCell) > 1e-15 {
		t.Errorf("first cell sample = (%v,%v), want (0,%v)", g.StackR[spec.NAxis+nMid], g.StackZ[spec.NAxis+nMid], -spec.ZCell)
	}
	// axis_in_cell: |z| <= ZCell。
	nIn := 0
	for i, z := range g.AxisZ {
		if g.AxisInCell[i] != (math.Abs(z) <= spec.ZCell) {
			t.Fatalf("AxisInCell[%d] wrong for z=%v", i, z)
		}
		if g.AxisInCell[i] {
			nIn++
		}
	}
	if nIn == 0 {
		t.Errorf("no axis sample inside the cell")
	}
	t.Logf("stack=%d (axis %d, mid %d, cell %d), axis samples in cell: %d", len(g.StackR), g.NAxis, nMid, nCell, nIn)
}

// ---------------------------------------------------------------------------
// 11. AxisRipple 的定义
// ---------------------------------------------------------------------------

func TestAxisRipple(t *testing.T) {
	const bMid = 1.0
	// 单调剖面: 没有内部极值 -> 精确为 0。
	if got := AxisRipple([]float64{1, 2, 3, 4, 5, 6}, bMid, 0.05); got != 0 {
		t.Errorf("monotonic ripple = %v, want 0", got)
	}
	// 单峰: 只有一个极值 -> 精确为 0。
	if got := AxisRipple([]float64{1, 2, 3, 4, 3, 2, 1}, bMid, 0.05); got != 0 {
		t.Errorf("single-peaked ripple = %v, want 0", got)
	}
	// 太短 / B_mid 非正 -> 0。
	if got := AxisRipple([]float64{1, 2, 3, 4}, bMid, 0.05); got != 0 {
		t.Errorf("short profile ripple = %v, want 0", got)
	}
	if got := AxisRipple([]float64{1, 2, 3, 2, 1, 2, 3}, 0, 0.05); got != 0 {
		t.Errorf("zero B_mid ripple = %v, want 0", got)
	}
	// 两个峰之间的深结构: sum |peak - adjacent valley| / B_mid。
	// 剖面在 4.0 与 3.5 处起峰, 两峰之间是一个 3.0 的谷。
	prof := []float64{1, 2, 3, 4.0, 3.0, 3.5, 1}
	got := AxisRipple(prof, bMid, 0.05)
	want := (math.Abs(4.0-3.0) + math.Abs(3.0-3.5)) / bMid
	if math.Abs(got-want) > 1e-15 {
		t.Errorf("alternating ripple = %v, want %v", got, want)
	}
	// 同样的形状, 但相间结构比 prominence*B_mid 更浅: 只有比 0.05*B_mid 更深的结构
	// 才算。两个相间极值 (4.0 处的 max, 3.96 处的 min), 深度 0.04 低于
	// prominence*B_mid = 0.05: 它不算。
	shallow := []float64{1, 4.0, 3.96, 4.5, 5.0}
	if got := axisRipple(shallow, bMid, 0.05); got != 0 {
		t.Errorf("shallow structure ripple = %v, want 0 (0.04 < prominence 0.05)", got)
	}
	// prominence 是参数, 不是常量: 一旦阈值降到该结构之下, 同一个结构就算数了。
	wantShallow := 0.04 / bMid
	if got := AxisRipple(shallow, bMid, 0.005); math.Abs(got-wantShallow) > 1e-15 {
		t.Errorf("ripple with prominence 0.005 = %v, want %v", got, wantShallow)
	}
}

// ---------------------------------------------------------------------------
// 12. MinCoilGap
// ---------------------------------------------------------------------------

func TestMinCoilGap(t *testing.T) {
	if g := MinCoilGap(nil); !math.IsInf(g, 1) {
		t.Errorf("empty gap = %v, want +Inf", g)
	}
	if g := MinCoilGap([]Coil{{Radius: 0.5, Z: 0, Current: 1}}); !math.IsInf(g, 1) {
		t.Errorf("single coil gap = %v, want +Inf", g)
	}
	coils := []Coil{{Radius: 0.5, Z: -0.25, Current: 1}, {Radius: 0.5, Z: 0.25, Current: 1}, {Radius: 0.3, Z: 1.0, Current: 1}}
	checkRel(t, "min gap", MinCoilGap(coils), 0.5, 1e-15)
	// 3-D 距离包含半径差。
	checkRel(t, "min gap with radius offset", MinCoilGap([]Coil{{Radius: 0.3, Z: 0, Current: 1}, {Radius: 0.5, Z: 0, Current: 1}}), 0.2, 1e-15)
}

// ---------------------------------------------------------------------------
// 13. 设计向量的编码/解码
// ---------------------------------------------------------------------------

func TestVectorCoilsRoundTrip(t *testing.T) {
	spec := config.DefaultSpec()
	// 越界向量: 必须被裁剪, 然后按 z 排序, 然后可重新编码。
	x := []float64{0.3, 2.0, 0.5, -0.2 /*r*/, 1.9, -3.0, 0.25, 1.0 /*z*/, 5e7, 1.0, 4.6e5, 1.6e6}
	coils, err := VectorToCoils(x, spec)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	// 裁剪后: r=[0.3,1.0,0.5,0.1] z=[1.2,-1.2,0.25,1.0] I=[2.5e6,1e4,4.6e5,1.6e6],
	// 再按 z 排序: -1.2, 0.25, 1.0, 1.2。
	want := []Coil{
		{Radius: spec.Bounds.Radius[1], Z: -spec.Bounds.Z[1], Current: 1.0e4},
		{Radius: 0.5, Z: 0.25, Current: 4.6e5},
		{Radius: spec.Bounds.Radius[0], Z: 1.0, Current: 1.6e6},
		{Radius: 0.3, Z: spec.Bounds.Z[1], Current: spec.Bounds.Current[1]},
	}
	for i := range want {
		if coils[i] != want[i] {
			t.Errorf("coil %d = %+v, want %+v", i, coils[i], want[i])
		}
	}
	back, err := VectorToCoils(CoilsToVector(coils), spec)
	if err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	for i := range coils {
		if back[i] != coils[i] {
			t.Errorf("round trip coil %d = %+v, want %+v", i, back[i], coils[i])
		}
	}
	// 排列不变性: 一个被置换的设计解码成同一个规范向量 (K! 简并被 z 排序消除)。
	base := []float64{0.5, 0.3, 0.5, 0.3 /*r*/, -0.25, 1.0, 0.25, -1.0 /*z*/, 4.6e5, 1.6e6, 4.6e5, 1.6e6}
	perm := []float64{0.3, 0.5, 0.3, 0.5 /*r*/, 1.0, -0.25, -1.0, 0.25 /*z*/, 1.6e6, 4.6e5, 1.6e6, 4.6e5}
	cb, err := VectorToCoils(base, spec)
	if err != nil {
		t.Fatalf("decode base: %v", err)
	}
	cp, err := VectorToCoils(perm, spec)
	if err != nil {
		t.Fatalf("decode permuted: %v", err)
	}
	vb, vp := CoilsToVector(cb), CoilsToVector(cp)
	for i := range vb {
		if vb[i] != vp[i] {
			t.Fatalf("permutation changed the canonical vector at %d: %v vs %v", i, vb[i], vp[i])
		}
	}
	// 长度不对是一个错误。
	if _, err := VectorToCoils([]float64{0.5, 0.5}, spec); err == nil {
		t.Errorf("decoding a 2-entry vector must fail")
	}
}

func TestRandomDesignIsInBoxAndCanonical(t *testing.T) {
	spec := config.DefaultSpec()
	rng := rand.New(rand.NewSource(7))
	lo, hi := spec.Lower(), spec.Upper()
	for n := 0; n < 200; n++ {
		x := RandomDesign(rng, spec)
		if len(x) != spec.NParams() {
			t.Fatalf("design has %d params, want %d", len(x), spec.NParams())
		}
		for i, v := range x {
			if v < lo[i] || v > hi[i] {
				t.Fatalf("design[%d] = %v outside [%v, %v]", i, v, lo[i], hi[i])
			}
		}
		// 规范形式: z 升序。
		k := spec.NCoils
		for i := 1; i < k; i++ {
			if x[k+i] < x[k+i-1] {
				t.Fatalf("design is not canonical: z = %v", x[k:2*k])
			}
		}
		coils, err := VectorToCoils(x, spec)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got := CoilsToVector(coils); len(got) != len(x) {
			t.Fatalf("re-encode length %d != %d", len(got), len(x))
		}
	}
	// 同一个 seed -> 同一条流 (可复现性是 Phase-0 的一道门)。
	a := RandomDesign(rand.New(rand.NewSource(7)), spec)
	b := RandomDesign(rand.New(rand.NewSource(7)), spec)
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("seed 7 is not reproducible at index %d", i)
		}
	}
}

// ---------------------------------------------------------------------------
// 14. solver 恒等式
// ---------------------------------------------------------------------------

func TestSolverNamesAndFiniteness(t *testing.T) {
	if got := (AnalyticSolver{}).Name(); got != "analytic-vacuum-loops" {
		t.Errorf("AnalyticSolver.Name() = %q, want %q (golden_field_samples.json solver tag)", got, "analytic-vacuum-loops")
	}
	if got := (DiscreteSolver{}).Name(); got != "discrete-filaments" {
		t.Errorf("DiscreteSolver.Name() = %q", got)
	}
	coils := []Coil{{Radius: 0.5, Z: 0, Current: 1e6}, {Radius: 0.3, Z: -1, Current: 1.5e6}}
	rs := []float64{0, 0.1, 0.5, 1.0}
	zs := []float64{0, 0.3, -1.4, 2.0}
	for _, s := range []Solver{AnalyticSolver{}, DiscreteSolver{NSeg: 256}, DiscreteSolver{}} {
		got := s.Magnitude(coils, rs, zs)
		if len(got) != len(rs) {
			t.Fatalf("%s: %d magnitudes for %d points", s.Name(), len(got), len(rs))
		}
		for i, v := range got {
			mustFinite(t, s.Name()+" |B|", v)
			if v < 0 {
				t.Errorf("%s: |B| = %v at point %d", s.Name(), v, i)
			}
		}
	}
	// 与分量求值器等价。
	br, bz := CoilsetField(coils, rs, zs)
	ana := AnalyticSolver{}.Magnitude(coils, rs, zs)
	for i := range rs {
		checkRel(t, "AnalyticSolver vs CoilsetField", ana[i], math.Hypot(br[i], bz[i]), 1e-15)
	}
}
