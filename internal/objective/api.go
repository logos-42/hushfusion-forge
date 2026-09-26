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
	"fmt"
	"math"

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
	// Nothing is defaulted here on purpose. A nil solver or an empty grid set
	// would silently produce a score made of NaNs, and a search comparing NaNs
	// burns an entire budget to learn nothing. Preconditions are loud instead.
	// The score's own divisors and limits are checked too: every one of them
	// turns a term into Inf/NaN if it is zero.
	if solver == nil {
		panic("objective.NewEvaluator: nil solver (no default: pass the analytic or discrete solver explicitly)")
	}
	if !(costRef > 0) || math.IsInf(costRef, 0) {
		panic(fmt.Sprintf("objective.NewEvaluator: costRef must be finite and > 0, got %v", costRef))
	}
	for _, p := range []struct {
		name string
		v    float64
	}{
		{"spec.BRef", spec.BRef},
		{"spec.MirrorRef", spec.MirrorRef},
		{"spec.CoilFieldLimit", spec.CoilFieldLimit},
		{"spec.MinCoilSep", spec.MinCoilSep},
	} {
		if !(p.v > 0) || math.IsInf(p.v, 0) {
			panic(fmt.Sprintf("objective.NewEvaluator: %s must be finite and > 0, got %v", p.name, p.v))
		}
	}
	if spec.NCoils < 1 {
		panic(fmt.Sprintf("objective.NewEvaluator: spec.NCoils must be >= 1, got %d", spec.NCoils))
	}
	if len(grids.StackR) == 0 || len(grids.StackR) != len(grids.StackZ) {
		panic("objective.NewEvaluator: grids must be a non-empty stacked sample set (physics.BuildGrids) with len(StackR) == len(StackZ) > 0")
	}
	if grids.NAxis <= 0 || grids.NMid <= 0 || grids.NAxis+grids.NMid > len(grids.StackR) {
		panic(fmt.Sprintf("objective.NewEvaluator: inconsistent grids (NAxis=%d NMid=%d samples=%d)",
			grids.NAxis, grids.NMid, len(grids.StackR)))
	}
	return &Evaluator{Spec: spec, Solver: solver, CostRef: costRef, Grids: grids}
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
	// The frozen signature has no error return, and every caller in the tree
	// builds vectors of exactly spec.NParams() entries (RandomDesign, mutation +
	// clip). A vector of another length is a programming error, never a design:
	// padding or truncating it would score a machine nobody proposed, so this
	// fails loudly instead. Checked before any physics call.
	if len(x) != e.Spec.NParams() {
		panic(fmt.Sprintf("objective.Evaluate: design vector has %d entries, spec wants %d", len(x), e.Spec.NParams()))
	}
	coils, err := physics.VectorToCoils(x, e.Spec)
	if err != nil {
		panic(fmt.Sprintf("objective.Evaluate: %v", err))
	}
	return e.evalFromMetrics(physics.MetricsFor(coils, e.Spec, e.Grids, e.Solver), physics.CoilsToVector(coils))
}

// Score is a convenience wrapper returning only the composite score.
func (e *Evaluator) Score(x []float64) float64 { return e.Evaluate(x).Score }
