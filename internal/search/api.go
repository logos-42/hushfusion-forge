// Package search: search algorithms over the design space.
//
// FROZEN INTERFACE (v0.1) — owner: stage D.
//
// Every algorithm has the same contract: it gets an evaluation *budget*, it
// spends it through a runner.Scorer (so the registry is complete by
// construction), and it returns its best-so-far trajectory — the only honest way
// to compare algorithms at equal cost.
//
//	random          uniform i.i.d. sampling — the null hypothesis
//	lhs             latin-hypercube (stratified space-filling) sampling
//	evolution       (mu+lambda) elitist evolution strategy, mutation variance
//	                annealed linearly with the spent budget
//	evolution_warm  identical, but the human baseline seeds the initial
//	                population — the cheapest possible form of *knowledge reuse*,
//	                included because "does inherited design knowledge pay?" is a
//	                measurable question, not a slogan
//
// A true grid search is deliberately NOT provided: at D = 12, even 5 points per
// axis is 2.4e8 evaluations. lhs is the honest stand-in, and the substitution is
// stated in the report rather than papered over.
//
// Reproducibility rule: given the same Options, Results are bit-identical —
// EXCEPT the order in which records land in the registry when Workers > 1.
// Scores, trajectories and best designs must not depend on Workers.
package search

import (
	"github.com/logos-42/hushfusion-forge/internal/config"
	"github.com/logos-42/hushfusion-forge/internal/physics"
	"github.com/logos-42/hushfusion-forge/internal/runner"
)

// Options fully specifies one run. Seed and Budget are the only knobs a caller
// normally sets; the rest have documented defaults (DefaultOptions).
type Options struct {
	Spec          config.Spec
	Seed          int
	Budget        int
	BaselineScore float64 // human baseline score; used only for EvalsToBeat
	Mu            int     // evolution: number of parents
	Lam           int     // evolution: children per generation
	Sigma0        float64 // evolution: initial mutation scale (fraction of the box)
	SigmaFloor    float64 // evolution: minimum mutation scale
	WarmStart     []float64
	Workers       int // <=1 serial; >1 evaluates a generation concurrently
	Algorithm     string
}

// DefaultOptions mirrors the Python reference and the report's stated settings:
// budget 1000, mu 16, lambda 48, sigma0 0.25, sigmaFloor 0.03, 1 worker.
func DefaultOptions(spec config.Spec) Options {
	panic("TODO(stage D): implement DefaultOptions")
}

// Result is the outcome of one algorithm run.
type Result struct {
	Algorithm    string             `json:"algorithm"`
	Seed         int                `json:"seed"`
	Budget       int                `json:"budget"`
	NEvals       int                `json:"n_evals"`
	BestScore    float64            `json:"best_score"`
	BestDesign   []float64          `json:"best_design"`
	BestTerms    map[string]float64 `json:"best_terms"`
	BestMetrics  physics.Metrics    `json:"best_metrics"`
	BestDesignID string             `json:"best_design_id"`
	BestFeasible bool               `json:"best_feasible"`
	EvalsToBeat  int                `json:"evals_to_beat"` // -1 when the baseline was never passed
	History      []float64          `json:"history,omitempty"`
}

// Random: uniform i.i.d. designs, budget evaluations.
// Per iteration: x = physics.RandomDesign(rng, spec) with
//
//	rng = rand.New(rand.NewSource(int64(seed)))
//
// (Go's RNG stream differs from numpy's by design; only the physics is
// cross-language anchored, not the random path.)
func Random(sc runner.Scorer, opt Options) Result { panic("TODO(stage D): implement random search") }

// LHS: one design per stratum per dimension; stratum order permuted per
// dimension from the same seeded RNG, then mapped into the box and canonicalised.
func LHS(sc runner.Scorer, opt Options) Result {
	panic("TODO(stage D): implement latin hypercube search")
}

// Evolution: elitist (mu+lambda) evolution strategy.
//
//   - initialise mu parents (warm start first, then uniform random designs)
//   - per generation: lambda children, each a copy of a uniformly random parent
//     plus sigma * (upper-lower) * N(0,1), clipped to the box, then canonicalised
//   - sigma = max(SigmaFloor, Sigma0 * (1 - nEvals/budget))
//   - selection: the mu best of (parents + children), so elitism is guaranteed
//   - children are recorded with their parent's design_id (lineage edge)
//
// The budget is exact: nEvals never exceeds Budget.
func Evolution(sc runner.Scorer, opt Options) Result {
	panic("TODO(stage D): implement evolution strategy")
}

// EvolutionWarm is Evolution with opt.WarmStart defaulting to the textbook
// mirror design when none is supplied. Algorithm name: "evolution_warm".
func EvolutionWarm(sc runner.Scorer, opt Options) Result {
	panic("TODO(stage D): implement warm-started evolution")
}

// Methods maps a method name to its implementation.
var Methods = map[string]func(runner.Scorer, Options) Result{}

// Run dispatches by name and errors on an unknown method.
func Run(method string, sc runner.Scorer, opt Options) (Result, error) {
	panic("TODO(stage D): implement method dispatch")
}
