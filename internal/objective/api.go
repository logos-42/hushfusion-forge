// Package objective: one number for the machine, plus every raw term it came
// from.
//
// FROZEN INTERFACE (v0.1) — owner: stage B.
//
// Design principle (important): the composite score is NEVER stored alone.
// Every evaluation records the raw physical terms and every constraint residual
// so that any weighting can be re-derived afterwards, and a reviewer can ask
// "did the machine win on physics, or did it just buy a cheaper magnet?"
// without re-running anything.
//
//	score = + w_field  * log10(B_mid / B_ref)
//	        + w_mirror * log10(max(R, 0.2) / R_ref)
//	        + w_volume * V_good
//	        - w_ripple * ripple
//	        - w_cost   * (cost / cost_ref)
//	        - w_penalty * (sum of constraint violations)
package objective

import (
	"github.com/logos-42/hushfusion-forge/internal/config"
	"github.com/logos-42/hushfusion-forge/internal/physics"
)

// Mirror log-term floor and "not a mirror" threshold. FROZEN: they are part of
// the score definition and the Python reference uses the same constants.
const (
	MirrorFloor = 0.2 // floor inside the mirror log term, keeps it finite
	MirrorMin   = 1.1 // below this mirror ratio, the "not_a_mirror" penalty applies
)

// Term keys used in Terms / Weighted. FROZEN — they appear in registry records
// and in the Python reference.
const (
	TermField  = "field"
	TermMirror = "mirror"
	TermVolume = "volume"
	TermRipple = "ripple"
	TermCost   = "cost"
)

// Penalty keys. FROZEN.
const (
	PenConductorField = "conductor_field"
	PenCoilSeparation = "coil_separation"
	PenNotAMirror     = "not_a_mirror"
)

// EvalResult is everything one evaluation produced.
type EvalResult struct {
	Score        float64            `json:"score"`
	Terms        map[string]float64 `json:"terms"`
	Weighted     map[string]float64 `json:"weighted"`
	Penalties    map[string]float64 `json:"penalties"`
	Feasible     bool               `json:"feasible"`
	Metrics      physics.Metrics    `json:"metrics"`
	Design       []float64          `json:"design"`
	DesignID     string             `json:"-"`
	ExperimentID int                `json:"-"`
}

// Evaluator scores designs. It holds the spec/grids/solver/cost reference so
// callers stay thin.
//
// CONCURRENCY: Evaluator is safe for concurrent use (the search layer evaluates
// candidates from several goroutines). Any mutable state must be atomic or
// immutable; do not add plain counters without a mutex or atomic.
type Evaluator struct {
	Spec    config.Spec
	Solver  physics.Solver
	CostRef float64
	Grids   physics.Grids
}

// NewEvaluator builds an evaluator. costRef is the ohmic cost of the human
// baseline, so the cost term reads 1.0 == "as expensive as the reference
// design". Callers pass baseline.TextbookMirror(spec).Cost.
func NewEvaluator(spec config.Spec, solver physics.Solver, costRef float64, grids physics.Grids) *Evaluator {
	panic("TODO(stage B): implement evaluator constructor")
}

// Evaluate scores one design vector.
//
//	terms[field]  = log10(max(B_mid, 1e-9) / B_ref)
//	terms[mirror] = log10(max(R, MirrorFloor) / MirrorRef)
//	terms[volume] = V_good
//	terms[ripple] = ripple
//	terms[cost]   = cost / costRef
//
//	penalties[conductor_field] = max(0, B_coil/CoilFieldLimit - 1)
//	penalties[coil_separation] = max(0, (MinCoilSep - min_gap)/MinCoilSep)
//	penalties[not_a_mirror]    = max(0, (MirrorMin - R)/MirrorMin)
//
//	Feasible = all penalties <= 0
//	Design   = the canonical (z-sorted, clipped) vector actually evaluated
func (e *Evaluator) Evaluate(x []float64) EvalResult {
	panic("TODO(stage B): implement evaluation")
}

// Score is a convenience wrapper returning only the composite score.
func (e *Evaluator) Score(x []float64) float64 { panic("TODO(stage B): implement score") }
