// Unit tests for the objective's algebra.
//
// These run WITHOUT stage A: the metrics are handed in directly (or through a
// stub Solver), because the scoring law — five terms, three constraint
// residuals, feasibility and the exact score decomposition — is stage B's and
// must be verifiable on its own. The tests that do need the physics core live in
// evaluate_fullpath_test.go and are parked until internal/physics lands.
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

// ---------------------------------------------------------------- fixtures ---

// stubSolver is a deterministic, physics-free Solver: the same |B| at every
// sample point. Physics-free on purpose — it lets the evaluator be exercised
// (and, in evaluate_fullpath_test.go, the whole Evaluate path) without stage A.
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

// evalFixture builds an Evaluator whose only interesting content is the spec and
// the cost reference. The grid is syntactically valid because NewEvaluator
// insists on that; its sample values never reach the algebra.
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

// cleanMirror is a metric set that violates nothing: a real mirror (R = 3.5),
// conductors well inside the limit, coils far apart.
func cleanMirror() physics.Metrics {
	return physics.Metrics{
		BMidT:       1.0,
		BThroatT:    3.5,
		ZThroatM:    -1.0,
		MirrorRatio: 3.5,
		VolumeGood:  0.5,
		Ripple:      0.0,
		BCoilMaxT:   3.0,
		MinCoilGapM: 0.5,
		CostProxy:   1.0e12,
		NCoils:      4,
		MU0:         config.MU0,
	}
}

// The frozen key sets, spelled out as literals on purpose: if a rename slips
// into the implementation, the registry schema and the Python reference diverge
// silently, and this is the cheap place to catch it.
var (
	frozenTermKeys    = []string{"field", "mirror", "volume", "ripple", "cost"}
	frozenPenaltyKeys = []string{"conductor_field", "coil_separation", "not_a_mirror"}
)

func relDiff(got, want float64) float64 {
	d := math.Abs(got - want)
	if w := math.Abs(want); w > d {
		return d / w
	}
	return d
}

// ------------------------------------------------------ terms / weights ------

func TestTermsAndWeightedForMirrorShapedMetrics(t *testing.T) {
	spec := config.DefaultSpec()
	const (
		mirrorRatio = 3.536386240927585
		cost        = 1.7917030355523e12
	)
	e := evalFixture(t, spec, cost)
	m := physics.Metrics{
		BMidT:       1.0,
		BThroatT:    mirrorRatio,
		ZThroatM:    -0.9975,
		MirrorRatio: mirrorRatio,
		VolumeGood:  0.7808857808857809,
		Ripple:      0.0,
		BCoilMaxT:   3.416271368796406,
		MinCoilGapM: 0.5,
		CostProxy:   cost,
		NCoils:      4,
		MU0:         config.MU0,
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
	// -0.0, not +0.0: the ripple contribution is negated, and the Python
	// reference writes "-0.0" into testdata/golden_baseline.json. Keeping the
	// sign keeps the two implementations byte-comparable.
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

// --------------------------------------------------------- penalty branches --

// TestPenaltyBranches builds one design per constraint, each engineered to
// trigger exactly that constraint, and checks the residual, the feasibility flag
// and that the score stays finite.
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
		m.MirrorRatio = 1.0 // uniform field: R < MirrorMin = 1.1
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
		// Above the floor: the log term uses R itself.
		if want := math.Log10(1.0 / spec.MirrorRef); res.Terms[TermMirror] != want {
			t.Errorf("terms[mirror] = %v, want %v", res.Terms[TermMirror], want)
		}
		requireFinite(t, res.Score)
	})

	t.Run("mirror_ratio_at_zero_hits_the_floor", func(t *testing.T) {
		m := cleanMirror()
		m.MirrorRatio = 0.0 // must not become log10(0) = -Inf
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
		m := physics.Metrics{ // no field at all, one coil, no cost
			BMidT:       0.0,
			MirrorRatio: 0.0,
			BCoilMaxT:   0.0,
			MinCoilGapM: math.Inf(1), // physics.MinCoilGap returns +Inf for < 2 coils
			CostProxy:   0.0,
			NCoils:      1,
			MU0:         config.MU0,
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

// -------------------------------------------- score == its own components ---

// TestScoreIsExactlyTheSumOfItsParts is the anti-drift gate: the composite must
// be reproducible, bit for bit, from the weighted terms and residuals that are
// stored next to it. "Close enough" is not good enough here — a registry whose
// score cannot be re-derived from its own terms is a registry nobody can audit.
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

	zero := physics.Metrics{MinCoilGapM: math.Inf(1)}
	scenarios["degenerate_zero"] = zero

	absurd := cleanMirror()
	absurd.CostProxy = 1e300
	absurd.BCoilMaxT = 1e12
	absurd.MirrorRatio = 0.0
	scenarios["absurd_but_finite"] = absurd

	scenarios["golden_metrics"] = loadGoldenBaseline(t).metrics()

	for name, m := range scenarios {
		res := e.evalFromMetrics(m, designVector(spec))

		// Key sets are frozen: the registry schema and the Python reference read
		// these names.
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

		// weighted == weight * term (sign included), exactly.
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

		// The decomposition itself, summed in the documented order.
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

// ------------------------------------------------------- golden anchor -------

// TestGoldenScoreReproducedFromFrozenMetrics checks the scoring law against the
// cross-language anchor: feed testdata/golden_baseline.json's own metrics and
// cost reference into the objective and the score must come back out at
// -0.2905708161. This part needs no physics at all, so it is green today; the
// same number from the real field path is in internal/baseline
// (TestTextbookMirrorMatchesGolden).
func TestGoldenScoreReproducedFromFrozenMetrics(t *testing.T) {
	g := loadGoldenBaseline(t)
	spec := config.DefaultSpec()
	e := evalFixture(t, spec, g.CostRef)

	// The frozen artifact is what it claims to be (a tampered testdata/ must not
	// be able to fake a green run here).
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

// ------------------------------------------------------------ concurrency ----

// TestConcurrentAlgebraMatchesSerial is the -race half of the Evaluator's
// concurrency contract (api.go: "safe for concurrent use"): the shared Evaluator
// must produce results identical to the serial ones when hammered from many
// goroutines. The full Evaluate path is raced in evaluate_fullpath_test.go.
func TestConcurrentAlgebraMatchesSerial(t *testing.T) {
	spec := config.DefaultSpec()
	e := evalFixture(t, spec, 1.0e12)

	scenarios := []physics.Metrics{cleanMirror()}
	all3 := cleanMirror()
	all3.MirrorRatio = 0.9
	all3.BCoilMaxT = 15.0
	all3.MinCoilGapM = 0.01
	scenarios = append(scenarios, all3)
	scenarios = append(scenarios, physics.Metrics{MinCoilGapM: math.Inf(1)})

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

// TestEvaluatePanicsOnWrongLengthDesign pins the "no silent resize" rule:
// Evaluate has no error return, so a wrong-length vector must fail loudly rather
// than be padded or truncated (which would score a machine nobody proposed).
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

// ------------------------------------------------------------- golden file ---

// goldenBaseline mirrors testdata/golden_baseline.json.
//
// metrics is decoded as a map of floats on purpose: the file stores the bool and
// int slots (coil_proximity_floor_hit, n_coils) as JSON floats, so it will not
// decode straight into physics.Metrics (reported to mainline in the stage-B
// report). Coil decodes directly because physics.Coil's tags are the file's.
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

// ----------------------------------------------------------------- helpers ---

func requireFinite(t *testing.T, v float64) {
	t.Helper()
	if math.IsNaN(v) || math.IsInf(v, 0) {
		t.Errorf("got %v, want a finite number", v)
	}
}
