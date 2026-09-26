// Full-path tests for objective.Evaluate: design vector -> physics metrics ->
// score. These need stage A (internal/physics) to be implemented, so they are
// parked today and fail with a precise "blocked on stage A" message rather than
// crashing the test binary. Do not delete or skip them: this file is the
// cross-line gate that the objective's algebra is fed by the real metric
// definitions (CONTRACT.md §4), and the golden end-to-end number lives in
// internal/baseline/golden_test.go.
//
// The fields the stubs return are hand-computable on purpose, so the expected
// values below are derived from the documented metric definitions rather than
// from the physics core itself.
package objective

import (
	"fmt"
	"math"
	"sync"
	"testing"

	"github.com/logos-42/hushfusion-forge/internal/config"
	"github.com/logos-42/hushfusion-forge/internal/physics"
)

// gradientSolver returns |B| = b0 + slope*|z|: a single-peaked on-axis profile,
// so the mirror ratio is > 1 by construction and every metric is a closed-form
// function of the two constants.
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

// fourCoil builds a canonical (z ascending) in-box design of four equal coils.
// Every current and radius is identical, so the ohmic cost proxy is
// 4 * I^2 * r = 4e11 [A^2 m] and the tests can pass costRef = 4e11 to place the
// cost term at exactly 1.0.
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

// probePhysics calls every physics entry point stage B depends on, with a benign
// well-separated design, and returns the panic the core still raises (nil once
// stage A has landed).
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

// TestEvaluateFullPathHandComputed drives Evaluate through the real metric
// computations with a fake field, and checks the result against values derived by
// hand from the documented definitions. Each subtest is built so that exactly one
// constraint is violated (or none), which is what makes the penalty branch
// attribution unambiguous.
func TestEvaluateFullPathHandComputed(t *testing.T) {
	requirePhysics(t)
	spec := config.DefaultSpec()
	grids := physics.BuildGrids(spec)

	t.Run("uniform_field_is_not_a_mirror", func(t *testing.T) {
		e := NewEvaluator(spec, stubSolver{mag: 2.0}, fourCoilCostRef, grids)
		x := fourCoil(-1.0, -0.3, 0.3, 1.0)
		res := e.Evaluate(x)

		// Uniform 2 T: B_mid = B_throat = 2 T, R = 1.
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
		// 2 T <= confine_factor * B_mid = 2.5 T at every sample, and a constant
		// profile has no interior extremum, so no ripple.
		if res.Terms[TermVolume] != 1.0 {
			t.Errorf("terms[volume] = %v, want 1 (2 T <= 1.25*2 T everywhere)", res.Terms[TermVolume])
		}
		if res.Terms[TermRipple] != 0 {
			t.Errorf("terms[ripple] = %v, want 0 for a constant profile", res.Terms[TermRipple])
		}
		if math.Abs(res.Terms[TermCost]-1.0) > 1e-12 {
			t.Errorf("terms[cost] = %v, want 1 (cost == cost_ref)", res.Terms[TermCost])
		}
		// 3 * 2 T from the other coils + the winding-pack self field.
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

		// Midplane volume: |B| = 2 + 5|z| over z = linspace(-0.15, 0.15, 5), so
		// the mean is 2 + 5*(0.15+0.075+0+0.075+0.15)/5 = 2.45 T.
		if math.Abs(res.Metrics.BMidT-2.45) > 1e-12 {
			t.Errorf("B_mid = %v, want 2.45", res.Metrics.BMidT)
		}
		// Throat: on-axis max is at |z| = z_axis_max = 1.4 -> 2 + 5*1.4 = 9 T.
		if math.Abs(res.Metrics.BThroatT-9.0) > 1e-12 {
			t.Errorf("B_throat = %v, want 9", res.Metrics.BThroatT)
		}
		if math.Abs(res.Metrics.MirrorRatio-9.0/2.45) > 1e-12 {
			t.Errorf("mirror ratio = %v, want %v", res.Metrics.MirrorRatio, 9.0/2.45)
		}
		// Cell samples: 2 + 5|z| <= 1.25*2.45 = 3.0625 -> |z| <= 0.2125, i.e. 9 of
		// the 33 axial samples on each of the 13 radii.
		if got, want := res.Terms[TermVolume], 117.0/429.0; math.Abs(got-want) > 1e-9 {
			t.Errorf("terms[volume] = %v, want %v (9 of 33 axial samples x 13 radii)", got, want)
		}
		// Outermost coils see 26 T from their neighbours ((2+3.5)+(2+6.5)+(2+10))
		// plus the self field: far over the 12 T limit.
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
		// 2 + 0.4|z| is a weak gradient: R = 2.56/2.036 = 1.257 > 1.1 and the
		// worst conductor sees 7.92 + π = 11.06 T < 12 T, so separation is the
		// only violated constraint.
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
		// Every cell sample passes the confinement test at this gradient.
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

// TestEvaluateConcurrentFullPathMatchesSerial is the -race check of the frozen
// concurrency contract on the real code path (design decode + metrics + score):
// many goroutines sharing one Evaluator must get exactly the serial results.
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

// TestScoreWrapperEqualsEvaluate pins that the convenience wrapper is the same
// evaluation, not a parallel implementation that can drift from it.
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
