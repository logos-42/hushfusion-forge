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
// helpers
// ---------------------------------------------------------------------------

const (
	testdataDir = "../../testdata"

	// frozen tolerances from CONTRACT.md / api.go
	tolAxis       = 1e-12 // on-axis closed form vs textbook formula
	tolHelmholtz  = 1e-12 // Helmholtz centre field
	uniHelmholtz  = 1.2e-4
	tolDiscrete   = 1e-9 // discrete sum vs closed form at nSeg = 512
	tolGoldenFld  = 1e-9 // golden_field_samples.json
	tolGoldenMet  = 1e-6 // golden_baseline.json metrics
	tolVacuumId   = 1e-5 // div B / curl B by central differences
	discreteNSeg  = 512
	awayFromWireM = 5e-2 // "away from the wire" for the 512-segment anchor
)

func relDiff(got, want float64) float64 {
	return math.Abs(got-want) / math.Max(math.Abs(want), 1e-12)
}

func checkRel(t *testing.T, what string, got, want, tol float64) {
	t.Helper()
	checkRelScaled(t, what, got, want, 0, tol)
}

// zeroRoundoff is the absolute criterion used where a reference value is
// exactly zero. |B| is the natural scale, and floating-point cancellation of
// terms of that size leaves a residual of ~1e-16*|B| per operation; 1e-14*|B|
// leaves two decades of headroom while still asserting "roundoff, not physics".
const zeroRoundoff = 1e-14

// checkRelScaled is the relative comparison used for the golden field samples.
//
// One golden entry is exactly 0.0 because the configuration is symmetric
// (textbook_mirror sample 28 sits on the midplane of a z-symmetric coil set, so
// B_r cancels exactly in the reference implementation). A relative criterion is
// vacuous for a zero reference, so that single case is stated as an absolute
// one: |got| <= zeroRoundoff*|B| at that point. Every non-zero component is
// still compared relatively at the frozen 1e-9 -- the zero branch never applies
// to them, so this is not a relaxation of the anchor.
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

// wireDistance is the smallest distance from (r, z) to any coil wire.
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
// golden data shapes
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
// 1. elliptic integrals (AGM) against the independent scipy values
// ---------------------------------------------------------------------------

func TestEllipticKEAgainstScipy(t *testing.T) {
	// K(m), E(m) from scipy.special.ellipk / ellipe (the reference oracle's
	// own integrators); Go must reproduce them.
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
	// Tolerances are the measured, understood achievable bounds of the frozen
	// AGM formula in double precision -- not arbitrary slack. They are far
	// tighter than anything the field anchors need (1e-9), and they exist to
	// catch exactly the class of bug this test was written after: an earlier
	// revision stopped the iteration on an absolute criterion that could never
	// fire (see keEps in magnet.go) and lost 7.8e-14 on E(0.5), which a loose
	// 1e-12 gate accepted.
	//
	// Regime split: E = K*(1 - sum), and as m -> 1 the sum approaches 1, so the
	// subtraction loses ~1 digit: the floor is eps/|1-sum| which reaches
	// ~2e-15 at m = 1-1e-12. Below m = 0.99 there is no such cancellation.
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

	// m = 0 is exact: K = E = pi/2.
	k0, e0 := ellipticKE(0)
	if k0 != math.Pi/2 || e0 != math.Pi/2 {
		t.Errorf("K(0)=%.17g E(0)=%.17g, want exactly pi/2=%.17g", k0, e0, math.Pi/2)
	}
	// Out of the frozen domain m in [0,1): reported as the limit, never as a
	// silently finite number pretending to be K(1e-12 away from 1).
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
// 2. on-axis anchor: B_z(0,z) = mu0*I*a^2 / (2*(a^2+z^2)^1.5), B_r(0,z) = 0
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

				// OnAxisField must agree with the general evaluator.
				got := onAxisField([]Coil{{Radius: a, Z: 0, Current: i}}, []float64{z})[0]
				checkRel(t, "OnAxisField", got, want, tolAxis)
			}
		}
	}
	t.Logf("on-axis anchor: worst rel diff over %d (a, I, z) combinations: %.3e (tol %.0e)",
		len(radii)*len(currents)*len(zs), worstAxis, tolAxis)

	// Superposed on-axis field: two loops at +-0.25 (a Helmholtz-ish pair).
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
// 3. Helmholtz anchor
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

		// Non-uniformity inside |z| <= 0.1a.
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
// 4. independent discrete Biot-Savart sum vs closed form
// ---------------------------------------------------------------------------

func TestDiscreteMatchesAnalytic(t *testing.T) {
	coils := []Coil{
		{Radius: 0.5, Z: -0.25, Current: 463222.63959687366},
		{Radius: 0.5, Z: 0.25, Current: 463222.63959687366},
		{Radius: 0.3, Z: -1.0, Current: 1621279.2385890577},
		{Radius: 0.3, Z: 1.0, Current: 1621279.2385890577},
	}
	// A sweep of points, filtered to "away from the wire" (>= 5 cm, see the
	// honest limit documented in TestDiscreteConvergence).
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

	// The same check on the golden sample points that sit away from the wire.
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

// TestDiscreteConvergence records where the 512-segment anchor stops holding:
// the midpoint sum converges geometrically in nSeg, with a rate that degrades as
// the sample approaches the wire. At 5 cm the error is at machine precision;
// at 1 cm it needs ~1440 segments. This is the honest boundary of the "< 1e-9
// for nSeg >= 512" claim, measured rather than assumed.
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
	// Measured boundary (documented, not asserted as a pass/fail gate): the
	// 512-segment anchor is NOT valid this close to the wire.
	close512 := at(0.01, 512)
	t.Logf("wire distance 0.01 m: rel diff 512=%.3e, 1440=%.3e (512 is below the 1e-9 anchor here)", close512, at(0.01, 1440))
	if close512 <= tolDiscrete {
		t.Logf("note: 512 segments now meet 1e-9 at 1 cm too (%s)", "geometry changed?")
	}
}

// ---------------------------------------------------------------------------
// 5. golden field samples (cross-language truth, 3 designs x 32 points)
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
		// The design must already be canonical (z ascending): the golden data is.
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
				// Golden exactly zero (exact symmetry cancellation): the check
				// inside checkRelScaled demands a roundoff-level residual.
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
// 6. golden baseline metrics (< 1e-6 relative on every field)
// ---------------------------------------------------------------------------

func TestGoldenBaselineMetrics(t *testing.T) {
	var gb goldenBaseline
	loadJSON(t, "golden_baseline.json", &gb)
	spec := config.DefaultSpec()
	coils, err := VectorToCoils(gb.Design, spec)
	if err != nil {
		t.Fatalf("decode baseline design: %v", err)
	}
	// The golden design is the canonical encoding of the baseline.
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
	// cost_proxy of the baseline is the objective's cost reference.
	checkRel(t, "cost_ref", m.CostProxy, gb.CostRef, tolGoldenMet)
}

// checkGoldenMetrics compares every metric and returns the worst relative
// difference seen (so callers can log the evidence, not just pass/fail).
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

// TestCrossLanguageMetrics three extra designs, whose metrics were produced by
// the protected Python reference (forge/physics/plasma_model.py at commit
// 4375c9a) with the analytic solver over the same spec/grids. They exercise
// paths the baseline alone does not: non-zero ripple, volume_good = 1.0 and
// rippled multi-coil interiors.
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
// 7. vacuum identities: div B = 0 and curl B = 0 away from the conductor
// ---------------------------------------------------------------------------

func TestVacuumIdentities(t *testing.T) {
	coils := testCoils([]float64{
		0.3, 0.5, 0.5, 0.3, -1.0, -0.25, 0.25, 1.0,
		1621279.2385890577, 463222.63959687366, 463222.63959687366, 1621279.2385890577,
	})
	// Central differences with a small step: truncation ~ h^2, roundoff ~ eps/h.
	// z = 0 is skipped (both identities degenerate to 0/0 there by symmetry and
	// their term normalisations vanish).
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
		// curl B (azimuthal component) = dB_r/dz - dB_z/dr
		t3 := (brZP - brZM) / (2 * h)
		t4 := (bzRP - bzRM) / (2 * h)
		curlB := t3 - t4

		_, bz0 := at(r, z)
		br0, _ := at(r, z)
		bMag := math.Hypot(br0, bz0)

		relDiv := math.Abs(divB) / (math.Abs(t1) + math.Abs(t2))
		relCurl := math.Abs(curlB) / (math.Abs(t3) + math.Abs(t4))
		// second, independent normalisation: the residual over a gradient length
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
// 8. degenerate geometry: proximity floor, finite metrics, no NaN
// ---------------------------------------------------------------------------

func TestNearWireAndProximityFloor(t *testing.T) {
	const a, i = 0.5, 1.0e6

	// A sample sitting on the wire, and samples just inside/outside the floor.
	for _, d := range []float64{0, 1e-12, 1e-9, 1e-6, 1e-3, 4.9e-3, 5e-3, 1e-2} {
		r, z := a-d, 0.0
		br, bz := LoopField(a, i, r, z)
		mustFinite(t, "B_r near wire", br)
		mustFinite(t, "B_z near wire", bz)
		t.Logf("wire gap %.1e: B_r=%.6g B_z=%.6g", d, br, bz)
	}

	// Two coils 1 mm apart: the closed form is singular at the neighbour's
	// location, so the floor must keep the metric finite and set the flag.
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

	// Fully coincident coils (the golden "single_loop" design has four loops at
	// (0.35 m, 0 m)): alpha2 -> 0 and (a^2 - r^2 - z^2) -> 0 at once. The value
	// is the floored one, so it is only checked for finiteness here -- the
	// protected Python reference does not floor and is not a reference for this
	// case.
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

	// A design inside the box that is comfortably legal must not trip the flag.
	legal := []Coil{{Radius: 0.5, Z: -0.25, Current: 4e5}, {Radius: 0.5, Z: 0.25, Current: 4e5}, {Radius: 0.3, Z: -1.0, Current: 1.5e6}, {Radius: 0.3, Z: 1.0, Current: 1.5e6}}
	if ml := MetricsFor(legal, spec, grids, AnalyticSolver{}); ml.CoilProximityFloorHit {
		t.Errorf("a legal design must not report CoilProximityFloorHit")
	}
}

// TestDegenerateInputs pins the behaviour on inputs the search can actually
// produce: a one-coil design, and a grid with no samples at all.
func TestDegenerateInputs(t *testing.T) {
	spec := config.DefaultSpec()
	grids := BuildGrids(spec)

	one := []Coil{{Radius: 0.5, Z: 0, Current: 1.0e6}}
	m := MetricsFor(one, spec, grids, AnalyticSolver{})
	// MinCoilGapM is +Inf by contract for fewer than two coils; every other
	// metric must be a finite number.
	for _, v := range []float64{m.BMidT, m.BThroatT, m.ZThroatM, m.MirrorRatio, m.VolumeGood, m.Ripple, m.BCoilMaxT, m.CostProxy} {
		mustFinite(t, "single-coil metric", v)
	}
	// No other coil -> no conductor field from neighbours, only the pack anchor.
	checkRel(t, "single-coil B_coil_max", m.BCoilMaxT, spec.SelfField(), 1e-15)
	if !math.IsInf(m.MinCoilGapM, 1) {
		t.Errorf("single coil gap = %v, want +Inf", m.MinCoilGapM)
	}
	if m.NCoils != 1 || m.CoilProximityFloorHit {
		t.Errorf("single coil: n_coils=%d floor_hit=%v, want 1/false", m.NCoils, m.CoilProximityFloorHit)
	}

	// Empty grid: a documented bail-out, no panic, no NaN.
	empty := MetricsFor(one, spec, Grids{}, AnalyticSolver{})
	for _, v := range []float64{empty.BMidT, empty.BThroatT, empty.ZThroatM, empty.MirrorRatio, empty.VolumeGood, empty.Ripple, empty.BCoilMaxT, empty.CostProxy} {
		mustFinite(t, "empty-grid metric", v)
	}
	if empty.BMidT != 0 || empty.CostProxy != 0 {
		t.Errorf("empty grid should give the zero metric, got B_mid=%v cost=%v", empty.BMidT, empty.CostProxy)
	}

	// No coils at all.
	none := MetricsFor(nil, spec, grids, AnalyticSolver{})
	if none.NCoils != 0 || none.BCoilMaxT != 0 {
		t.Errorf("empty coil set: n_coils=%d B_coil_max=%v, want 0/0", none.NCoils, none.BCoilMaxT)
	}
	mustFinite(t, "empty coil set B_mid", none.BMidT)
}

// TestMetricAgreementAcrossSolvers re-runs the whole metric pipeline with the
// independent discrete solver. The two solvers share no code below the Solver
// interface, so agreeing metrics is a check on the metric definitions
// themselves (slicing, volume fraction, conductor-field superposition), not
// just on the field.
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
	// |z_throat| must agree: the throat of a z-symmetric coil set is attained at
	// two sample positions that differ by ~1e-16, so which of the two the argmax
	// returns is a roundoff-level tie-break, not a physics difference.
	checkRel(t, "discrete vs analytic |z_throat_m|", math.Abs(d.ZThroatM), math.Abs(a.ZThroatM), tolGoldenMet)
	if d.ZThroatM != a.ZThroatM {
		t.Logf("z_throat tie-break: discrete=%.17g analytic=%.17g (equal up to roundoff; |z| agrees)", d.ZThroatM, a.ZThroatM)
	}
}

// ---------------------------------------------------------------------------
// 9. one stacked sampling pass per MetricsFor
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
	// K conductor-field calls, each over the K-1 other coil locations.
	wantCalls := 1 + len(coils)
	if cnt.calls != wantCalls {
		t.Errorf("MetricsFor made %d field calls, want %d (lens=%v)", cnt.calls, wantCalls, cnt.lens)
	}
	for _, n := range cnt.lens[1:] {
		if n != len(coils)-1 {
			t.Errorf("conductor-field call over %d points, want %d", n, len(coils)-1)
		}
	}
	// The counting wrapper delegates to the same solver, so the metric values
	// must be bit-identical to a plain run (the wrapper only counts). Compared
	// against the golden as well, at the frozen 1e-6.
	plain := MetricsFor(coils, spec, grids, AnalyticSolver{})
	if m != plain {
		t.Errorf("counting wrapper changed the metrics:\n got %+v\nwant %+v", m, plain)
	}
	t.Logf("counting solver: worst rel diff vs golden %.3e (tol %.0e)", checkGoldenMetrics(t, "counting solver", m, gb.Metrics), tolGoldenMet)
}

// ---------------------------------------------------------------------------
// 10. grids: layout, counts and ordering
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
	// Axis block: r = 0, z = linspace(-ZAxisMax, ZAxisMax, NAxis) with exact ends.
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
	// Symmetric span: numpy.linspace lands exactly on 0 for an odd count.
	if g.AxisZ[spec.NAxis/2] != 0 {
		t.Errorf("axis centre sample = %v, want exactly 0", g.AxisZ[spec.NAxis/2])
	}
	// Midplane block: meshgrid(r, z, "ij") -> radius slowest, 5 z per radius.
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
	// First midplane sample is r=0 on the midplane; the cell block starts right
	// after it with the same radius.
	if g.StackR[spec.NAxis] != 0 || g.StackZ[spec.NAxis] != -spec.ZMid {
		t.Errorf("first midplane sample = (%v,%v), want (0,%v)", g.StackR[spec.NAxis], g.StackZ[spec.NAxis], -spec.ZMid)
	}
	if g.StackR[spec.NAxis+nMid] != 0 || math.Abs(g.StackZ[spec.NAxis+nMid]+spec.ZCell) > 1e-15 {
		t.Errorf("first cell sample = (%v,%v), want (0,%v)", g.StackR[spec.NAxis+nMid], g.StackZ[spec.NAxis+nMid], -spec.ZCell)
	}
	// axis_in_cell: |z| <= ZCell.
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
// 11. AxisRipple definition
// ---------------------------------------------------------------------------

func TestAxisRipple(t *testing.T) {
	const bMid = 1.0
	// Monotonic profile: no interior extrema -> exactly 0.
	if got := AxisRipple([]float64{1, 2, 3, 4, 5, 6}, bMid, 0.05); got != 0 {
		t.Errorf("monotonic ripple = %v, want 0", got)
	}
	// Single peak: one extremum -> exactly 0.
	if got := AxisRipple([]float64{1, 2, 3, 4, 3, 2, 1}, bMid, 0.05); got != 0 {
		t.Errorf("single-peaked ripple = %v, want 0", got)
	}
	// Too short / non-positive B_mid -> 0.
	if got := AxisRipple([]float64{1, 2, 3, 4}, bMid, 0.05); got != 0 {
		t.Errorf("short profile ripple = %v, want 0", got)
	}
	if got := AxisRipple([]float64{1, 2, 3, 2, 1, 2, 3}, 0, 0.05); got != 0 {
		t.Errorf("zero B_mid ripple = %v, want 0", got)
	}
	// Deep structure between two peaks: sum |peak - adjacent valley| / B_mid.
	// profile peaks at 4.0 and 3.5 with a valley of 3.0 between them.
	prof := []float64{1, 2, 3, 4.0, 3.0, 3.5, 1}
	got := AxisRipple(prof, bMid, 0.05)
	want := (math.Abs(4.0-3.0) + math.Abs(3.0-3.5)) / bMid
	if math.Abs(got-want) > 1e-15 {
		t.Errorf("alternating ripple = %v, want %v", got, want)
	}
	// Same shape but with an alternating structure shallower than
	// prominence*B_mid: only structures deeper than 0.05*B_mid count.
	// Two alternating extrema (a max at 4.0, a min at 3.96) whose depth 0.04 is
	// below prominence*B_mid = 0.05: it does not count.
	shallow := []float64{1, 4.0, 3.96, 4.5, 5.0}
	if got := axisRipple(shallow, bMid, 0.05); got != 0 {
		t.Errorf("shallow structure ripple = %v, want 0 (0.04 < prominence 0.05)", got)
	}
	// Prominence is a parameter, not a constant: the same structure counts once
	// the threshold drops below it.
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
	// 3-D distance includes the radius difference.
	checkRel(t, "min gap with radius offset", MinCoilGap([]Coil{{Radius: 0.3, Z: 0, Current: 1}, {Radius: 0.5, Z: 0, Current: 1}}), 0.2, 1e-15)
}

// ---------------------------------------------------------------------------
// 13. design vector encode/decode
// ---------------------------------------------------------------------------

func TestVectorCoilsRoundTrip(t *testing.T) {
	spec := config.DefaultSpec()
	// Out-of-bounds vector: must be clipped, then z-sorted, then re-encodable.
	x := []float64{0.3, 2.0, 0.5, -0.2 /*r*/, 1.9, -3.0, 0.25, 1.0 /*z*/, 5e7, 1.0, 4.6e5, 1.6e6}
	coils, err := VectorToCoils(x, spec)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	// clipped: r=[0.3,1.0,0.5,0.1] z=[1.2,-1.2,0.25,1.0] I=[2.5e6,1e4,4.6e5,1.6e6],
	// then z-sorted: -1.2, 0.25, 1.0, 1.2.
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
	// Permutation invariance: a permuted design decodes to the same canonical
	// vector (the K! degeneracy is removed by the z-sort).
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
	// Wrong length is an error.
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
		// Canonical: z ascending.
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
	// Same seed -> same stream (reproducibility is a Phase-0 gate).
	a := RandomDesign(rand.New(rand.NewSource(7)), spec)
	b := RandomDesign(rand.New(rand.NewSource(7)), spec)
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("seed 7 is not reproducible at index %d", i)
		}
	}
}

// ---------------------------------------------------------------------------
// 14. solver identities
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
	// Equivalence with the component evaluator.
	br, bz := CoilsetField(coils, rs, zs)
	ana := AnalyticSolver{}.Magnitude(coils, rs, zs)
	for i := range rs {
		checkRel(t, "AnalyticSolver vs CoilsetField", ana[i], math.Hypot(br[i], bz[i]), 1e-15)
	}
}
