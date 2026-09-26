// The end-to-end golden tests for the human baseline: the solved cell current,
// the frozen metrics/terms/score, and the "not clipped by the search box" gate
// (CONTRACT.md G6).
//
// All of these need stage A's field solver, so they are parked today: they fail
// with a precise "blocked on stage A" message instead of crashing the binary, and
// go green the moment internal/physics lands. Do not delete or skip them.
package baseline

import (
	"math"
	"sort"
	"testing"

	"github.com/logos-42/hushfusion-forge/internal/config"
	"github.com/logos-42/hushfusion-forge/internal/objective"
	"github.com/logos-42/hushfusion-forge/internal/physics"
)

// TestTextbookMirrorMatchesGolden is the §5 anchor for the baseline: build the
// hand design from scratch, then compare it with testdata/golden_baseline.json —
// coils, cost, canonical design, the metrics it produces, and the composite score
// the objective gives it.
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

	// Coils, in the reference implementation's construction order.
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
		// The current is SOLVED, not transcribed: 1e-9 is the solver's own
		// tolerance, so this is as tight as the construction allows.
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

	// The score the machine is asked to beat, computed through the real pipeline
	// (metrics -> terms -> weighted -> composite).
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

	// Evidence trail (visible with -v): the numbers behind the assertions.
	t.Logf("solved: I_cell=%.10f A  I_throat=%.10f A  cost=%.9e", b.Design[9], b.Design[8], b.Cost)
	t.Logf("score=%.16g (golden %.16g, diff %.3g)", res.Score, g.Score, res.Score-g.Score)
	t.Logf("terms: field=%.6g mirror=%.6g volume=%.6g ripple=%.6g cost=%.6g",
		res.Terms["field"], res.Terms["mirror"], res.Terms["volume"], res.Terms["ripple"], res.Terms["cost"])
	t.Logf("feasible=%v penalties: conductor_field=%.6g coil_separation=%.6g not_a_mirror=%.6g",
		res.Feasible, res.Penalties["conductor_field"], res.Penalties["coil_separation"], res.Penalties["not_a_mirror"])

	// Metrics, RELATIVE 1e-6 (CONTRACT §5: "各项 metrics 相对差 < 1e-6"). A metric
	// like cost_proxy is O(1e12), so an absolute tolerance would be either
	// meaningless or impossible; near-zero metrics (ripple) fall back to the
	// absolute 1e-6 floor.
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

// TestBaselineInsideSearchBox is the frozen gate G6: decode the hand design back
// through physics.VectorToCoils and require it to come back unchanged.
//
// Why it exists: VectorToCoils clips to spec.Bounds. If the hand design sat
// outside the box, the decoded machine would be a *different* machine, and
// "the machine beat the human" would be measured against a design nobody
// proposed. Relative 1e-12 on radius, position and current — i.e. exactly the
// same floats back.
//
// Ordering note: Baseline.Coils is stored in the reference implementation's
// construction order (the two cell loops, then the two throats) while
// VectorToCoils returns the canonical z-sorted order. The gate is about the
// machine, not about the order it is listed in, so both sides are z-sorted before
// the pairwise comparison — and the order-free statement (re-encoding the decoded
// coils reproduces the design vector, bit for bit) is checked as well.
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

	// Order-free statement of "not clipped": the round trip through the decoder
	// must give back the very same design vector.
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

	// Pairwise, after canonical ordering.
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
	// Canonical form: sorted by z, ascending.
	for i := 1; i < len(coils); i++ {
		if coils[i].Z < coils[i-1].Z {
			t.Errorf("decoded coils are not z-sorted: z[%d] = %v < z[%d] = %v", i, coils[i].Z, i-1, coils[i-1].Z)
		}
	}
	// And the construction itself already refuses to leave the box.
	if err := checkInsideBounds(b.Coils, spec); err != nil {
		t.Errorf("TextbookMirror produced a design outside the search box: %v", err)
	}

	// Cross-language: the frozen design vector decodes to the frozen coil set.
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

// TestTextbookMirrorSolvesCellCurrentToBRef checks the defining property of the
// solve — not the machine's score, but the physical statement it was built on:
// the midplane volume-averaged field must equal spec.BRef, to the solver's own
// 1e-9 relative bracket.
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

	// The solve's own claim, measured directly.
	m := physics.MetricsFor(b.Coils, spec, physics.BuildGrids(spec), analyticSolver())
	if relDiff(m.BMidT, spec.BRef) > 1e-9 {
		t.Errorf("solved midplane field = %.16g T, want spec.BRef = %g T", m.BMidT, spec.BRef)
	}
	if relDiff(m.CostProxy, b.Cost) > 1e-12 {
		t.Errorf("the baseline's own cost proxy (%v) disagrees with the metric (%v)", b.Cost, m.CostProxy)
	}
	// And the human baseline is inside every constraint: the comparison is only
	// meaningful if the human is a legal design.
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
