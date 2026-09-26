// The objective's algebra, split out of api.go so that it can be exercised
// without the physics core running (see CONTRACT.md §4: stage B's unit tests
// must not be blocked by stage A's package).
//
// Evaluate is the thin wrapper: decode the design vector, run
// physics.MetricsFor once, land here. Everything that decides the number — the
// five terms, their weights, the three constraint residuals, feasibility and the
// composite — lives in this one function, so there is exactly one place where a
// score can be formed.
package objective

import (
	"math"

	"github.com/logos-42/hushfusion-forge/internal/physics"
)

// termOrder and penaltyOrder are the only orders in which the composite score is
// summed. The score is formed by summing the weighted terms and the normalised
// violations in exactly this order, so that
//
//	score == Σ weighted - weights.penalty * Σ penalties
//
// holds bit-for-bit rather than "to within a rounding". The registry is meant to
// be re-auditable after the fact, and "the score does not match its own terms" is
// a hard failure, not a tolerance question. Keep in sync with the score formula
// at the top of api.go; the test spells the keys out as literals on purpose.
var (
	termOrder    = [...]string{TermField, TermMirror, TermVolume, TermRipple, TermCost}
	penaltyOrder = [...]string{PenConductorField, PenCoilSeparation, PenNotAMirror}
)

// evalFromMetrics builds the full evaluation of one design from its already
// computed field metrics, and from the canonical design vector it came from.
//
// Deliberately no error return and no clamping of NaN away: a metric the physics
// layer could not compute must reach the caller as NaN (loud) instead of being
// silently replaced by a plausible finite number (which would be a fabricated
// score). What this function does guarantee is that a *well defined* infeasible
// design — huge conductor field, coils on top of each other, no mirror at all —
// yields a finite score: the floor terms (MirrorFloor, 1e-9) and the max(0, ·)
// residuals are exactly what keeps those cases on the finite side.
func (e *Evaluator) evalFromMetrics(m physics.Metrics, design []float64) EvalResult {
	s := e.Spec
	w := s.Weights

	terms := map[string]float64{
		TermField:  math.Log10(math.Max(m.BMidT, 1e-9) / s.BRef),
		TermMirror: math.Log10(math.Max(m.MirrorRatio, MirrorFloor) / s.MirrorRef),
		TermVolume: m.VolumeGood,
		TermRipple: m.Ripple,
		TermCost:   m.CostProxy / e.CostRef,
	}
	weighted := map[string]float64{
		TermField:  w.Field * terms[TermField],
		TermMirror: w.Mirror * terms[TermMirror],
		TermVolume: w.Volume * terms[TermVolume],
		TermRipple: -w.Ripple * terms[TermRipple],
		TermCost:   -w.Cost * terms[TermCost],
	}
	penalties := map[string]float64{
		PenConductorField: math.Max(0, m.BCoilMaxT/s.CoilFieldLimit-1),
		PenCoilSeparation: math.Max(0, (s.MinCoilSep-m.MinCoilGapM)/s.MinCoilSep),
		PenNotAMirror:     math.Max(0, (MirrorMin-m.MirrorRatio)/MirrorMin),
	}

	score := 0.0
	for _, k := range termOrder {
		score += weighted[k]
	}
	violation := 0.0
	for _, k := range penaltyOrder {
		violation += penalties[k]
	}
	score -= w.Penalty * violation

	// Feasible mirrors the reference's all(v <= 0): written as !(v <= 0) so a NaN
	// residual counts as infeasible instead of slipping through as "no
	// violation".
	feasible := true
	for _, k := range penaltyOrder {
		if !(penalties[k] <= 0) {
			feasible = false
			break
		}
	}

	return EvalResult{
		Score:     score,
		Terms:     terms,
		Weighted:  weighted,
		Penalties: penalties,
		Feasible:  feasible,
		Metrics:   m,
		Design:    design,
	}
}
