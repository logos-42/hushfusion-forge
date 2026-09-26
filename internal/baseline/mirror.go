// Construction of the hand-designed mirror and the solve that fixes its
// current scale. Split out of api.go so the pieces that are pure arithmetic
// (coil layout, cost proxy, bounds audit, the bracketing solver) can be unit
// tested on their own, without a working field solver.
package baseline

import (
	"errors"
	"fmt"
	"math"

	"github.com/logos-42/hushfusion-forge/internal/config"
	"github.com/logos-42/hushfusion-forge/internal/physics"
)

const (
	// Name and Note of the hand design. The note is byte-identical to the one
	// the Python reference writes into testdata/golden_baseline.json, so the two
	// implementations describe the same machine in the same words.
	textbookName = "textbook_mirror"
	textbookNote = "Helmholtz-like central cell (r=0.50 m, spacing=r) + mirror throats " +
		"(r=0.30 m, |z|=1.00 m, I=3.5x cell). Cell current solved so the " +
		"midplane field hits spec.b_ref exactly; not a straw man."

	// Cell-current bracketing scan: the same 24-point geometric grid the
	// reference implementation uses (forge/optimization/baselines.py), refined by
	// bisection to a relative width of cellCurrentRelTol. The scan window is
	// deliberately wider than spec.Bounds.Current, so "the root is outside the
	// search box" is reported as a bracket failure or by checkInsideBounds,
	// never silently clipped.
	cellCurrentScanLo = 1.0e3
	cellCurrentScanHi = 5.0e6
	cellCurrentScanN  = 24
	cellCurrentRelTol = 1e-9

	cellBisectMaxIter = 200
)

// errNoAnalyticSolver is returned while stage A's internal/physics still has no
// implementation: there is nothing to call yet, and inventing a field would be
// worse than failing.
var errNoAnalyticSolver = errors.New(
	"internal/physics does not provide a usable solver yet: physics.AnalyticSolver has no " +
		"Name/Magnitude methods (stage A has not landed, or the receiver changed)")

// analyticSolver returns the physics package's closed-form solver.
//
// Why the run-time type assertion instead of `physics.AnalyticSolver{}`: stage B
// owns internal/baseline, stage A owns internal/physics, and while B was written
// AnalyticSolver had no methods yet — naming it as a physics.Solver would not
// compile, which would break `go build ./...` for every other line. Asserting at
// run time keeps this package compiling today and makes it start working the
// moment stage A lands; it needs no maintenance whether A attaches Name/Magnitude
// to the value or to the pointer.
//
// needs-mainline: once stage A lands this can become
//
//	return physics.AnalyticSolver{}, nil
//
// (or whatever constructor physics grows) and errNoAnalyticSolver can go.
func analyticSolver() (physics.Solver, error) {
	a := physics.AnalyticSolver{}
	for _, cand := range []any{a, &a} {
		if s, ok := cand.(physics.Solver); ok {
			return s, nil
		}
	}
	return nil, errNoAnalyticSolver
}

// mirrorCoils lays out the hand design, in the reference implementation's order
// (the two central-cell loops, then the two throats). testdata/golden_baseline.json
// "coils" is in this order, and "design" is the same machine in canonical
// (z-sorted) order, so keep the construction order as it is.
func mirrorCoils(g Geom, cellCurrent float64) []physics.Coil {
	throat := g.ThroatCurrentRatio * cellCurrent
	return []physics.Coil{
		{Radius: g.RCell, Z: -g.HalfGapCell, Current: cellCurrent},
		{Radius: g.RCell, Z: g.HalfGapCell, Current: cellCurrent},
		{Radius: g.RThroat, Z: -g.ZThroat, Current: throat},
		{Radius: g.RThroat, Z: g.ZThroat, Current: throat},
	}
}

// costProxy is the ohmic cost proxy sum_k I_k^2 r_k [A^2 m] — same definition as
// physics.Metrics.CostProxy, computed here because the baseline must be able to
// report its own cost before any metrics exist.
func costProxy(coils []physics.Coil) float64 {
	sum := 0.0
	for _, c := range coils {
		sum += c.Current * c.Current * c.Radius
	}
	return sum
}

// checkInsideBounds returns an error if any coil sits outside the search box.
//
// This is the "no clipped baseline" rule at the source: physics.VectorToCoils
// clips to spec.Bounds, so a hand design outside the box would be re-encoded as a
// different machine and "the machine beat the human" would be measured against a
// design nobody proposed. Inclusive comparison — a value exactly on a bound is
// not moved by clipping, so it is allowed.
func checkInsideBounds(coils []physics.Coil, spec config.Spec) error {
	for i, c := range coils {
		switch {
		case c.Radius < spec.Bounds.Radius[0] || c.Radius > spec.Bounds.Radius[1]:
			return fmt.Errorf("coil %d radius %.10g m is outside the search box [%g, %g] m: the baseline would be clipped when re-encoded",
				i, c.Radius, spec.Bounds.Radius[0], spec.Bounds.Radius[1])
		case c.Z < spec.Bounds.Z[0] || c.Z > spec.Bounds.Z[1]:
			return fmt.Errorf("coil %d z %.10g m is outside the search box [%g, %g] m: the baseline would be clipped when re-encoded",
				i, c.Z, spec.Bounds.Z[0], spec.Bounds.Z[1])
		case c.Current < spec.Bounds.Current[0] || c.Current > spec.Bounds.Current[1]:
			return fmt.Errorf("coil %d current %.10g A is outside the search box [%g, %g] A: the baseline would be clipped when re-encoded",
				i, c.Current, spec.Bounds.Current[0], spec.Bounds.Current[1])
		}
	}
	return nil
}

// solveCellCurrent solves the central-cell current so the midplane volume-averaged
// field equals spec.BRef exactly (to cellCurrentRelTol relative).
//
// Every coil current scales linearly with the cell current, so the midplane field
// is linear in it: the root is unique, and a bracket scan over the geometric grid
// finds it whenever it lies inside [cellCurrentScanLo, cellCurrentScanHi].
func solveCellCurrent(spec config.Spec, g Geom, grids physics.Grids, s physics.Solver) (float64, error) {
	f := func(iCell float64) float64 {
		return physics.MetricsFor(mirrorCoils(g, iCell), spec, grids, s).BMidT - spec.BRef
	}
	return bisectBracket(cellCurrentScanLo, cellCurrentScanHi, cellCurrentScanN, cellCurrentRelTol, f)
}

// textbookMirror is TextbookMirror with the solver and grids supplied, so tests
// can drive the whole construction with an injected field.
func textbookMirror(spec config.Spec, s physics.Solver, grids physics.Grids) (Baseline, error) {
	g := DefaultGeom()
	iCell, err := solveCellCurrent(spec, g, grids, s)
	if err != nil {
		return Baseline{}, fmt.Errorf("textbook mirror cell current: %w", err)
	}
	coils := mirrorCoils(g, iCell)
	if err := checkInsideBounds(coils, spec); err != nil {
		return Baseline{}, err
	}
	return Baseline{
		Name:   textbookName,
		Note:   textbookNote,
		Coils:  coils,
		Cost:   costProxy(coils),
		Design: physics.CoilsToVector(coils),
	}, nil
}

// bisectBracket locates a sign change of f on an n-point geometric grid over
// [lo, hi] and refines it by bisection until the bracket is narrower than
// relTol relative.
//
// Failure modes are returned, never papered over: an unusable argument list, a
// grid that does not straddle a root, a NaN residual (which would otherwise be
// refined into a fabricated root). An infinite residual is allowed — its sign is
// still meaningful, so the bisection stays honest there.
func bisectBracket(lo, hi float64, n int, relTol float64, f func(float64) float64) (float64, error) {
	switch {
	case !(lo > 0) || !(hi > lo):
		return 0, fmt.Errorf("invalid bracket scan window [%g, %g]: need 0 < lo < hi", lo, hi)
	case n < 2:
		return 0, fmt.Errorf("bracket scan needs at least 2 grid points, got %d", n)
	case !(relTol > 0):
		return 0, fmt.Errorf("invalid relative tolerance %g", relTol)
	}
	xLo := lo
	yLo, err := evalNoNaN(f, xLo)
	if err != nil {
		return 0, err
	}
	if yLo == 0 {
		return xLo, nil
	}
	for i := 1; i < n; i++ {
		xHi := lo * math.Pow(hi/lo, float64(i)/float64(n-1))
		yHi, err := evalNoNaN(f, xHi)
		if err != nil {
			return 0, err
		}
		if yHi == 0 {
			return xHi, nil
		}
		if (yLo < 0) != (yHi < 0) {
			return bisect(xLo, xHi, yLo, relTol, f)
		}
		xLo, yLo = xHi, yHi
	}
	return 0, fmt.Errorf("no sign change over %d geometric grid points in [%g, %g]: cannot bracket a root", n, lo, hi)
}

// bisect refines a straddling bracket [lo, hi] with the signs of the residuals
// known to differ. It returns a point within relTol*|root| of the root.
func bisect(lo, hi, fLo, relTol float64, f func(float64) float64) (float64, error) {
	for i := 0; i < cellBisectMaxIter; i++ {
		mid := 0.5 * (lo + hi)
		if mid <= lo || mid >= hi {
			// The bracket is down to adjacent floats: this is as close as the
			// arithmetic can get.
			return 0.5 * (lo + hi), nil
		}
		fMid, err := evalNoNaN(f, mid)
		if err != nil {
			return 0, err
		}
		if fMid == 0 {
			return mid, nil
		}
		if (fLo < 0) != (fMid < 0) {
			hi = mid
		} else {
			lo, fLo = mid, fMid
		}
		if hi-lo <= relTol*math.Abs(mid) {
			return 0.5 * (lo + hi), nil
		}
	}
	return 0, fmt.Errorf("bisection did not reach a relative width of %g in %d iterations", relTol, cellBisectMaxIter)
}

// evalNoNaN evaluates f and rejects a NaN residual: refining a NaN into a
// "solution" would produce a fabricated current instead of a solved one.
func evalNoNaN(f func(float64) float64, x float64) (float64, error) {
	y := f(x)
	if math.IsNaN(y) {
		return 0, fmt.Errorf("the bracketed function returned NaN at %g", x)
	}
	return y, nil
}
