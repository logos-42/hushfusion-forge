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
	"fmt"
	"math/rand"
	"sort"
	"strings"

	"github.com/logos-42/hushfusion-forge/internal/config"
	"github.com/logos-42/hushfusion-forge/internal/physics"
	"github.com/logos-42/hushfusion-forge/internal/runner"
)

// Algorithm names. FROZEN: they are written into every registry record as
// Meta.Algorithm and into Result.Algorithm.
const (
	AlgorithmRandom        = "random"
	AlgorithmLHS           = "lhs"
	AlgorithmEvolution     = "evolution"
	AlgorithmEvolutionWarm = "evolution_warm"
)

// Documented defaults (mirrored by DefaultOptions).
const (
	DefaultBudget     = 1000
	DefaultMu         = 16
	DefaultLam        = 48
	DefaultSigma0     = 0.25
	DefaultSigmaFloor = 0.03
	DefaultWorkers    = 1
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
//
// Seed is 0 and BaselineScore is 0 (unset) on purpose: the seed is a run
// argument, and the baseline score belongs to whoever ran the human baseline.
// Algorithm is the primary method, "evolution"; each algorithm stamps its own
// canonical name on the records it writes, so a mis-set field cannot mislabel a
// registry record.
func DefaultOptions(spec config.Spec) Options {
	return Options{
		Spec:       spec,
		Seed:       0,
		Budget:     DefaultBudget,
		Mu:         DefaultMu,
		Lam:        DefaultLam,
		Sigma0:     DefaultSigma0,
		SigmaFloor: DefaultSigmaFloor,
		Workers:    DefaultWorkers,
		Algorithm:  AlgorithmEvolution,
	}
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
func Random(sc runner.Scorer, opt Options) Result {
	rng := rand.New(rand.NewSource(int64(opt.Seed)))
	st := newRunState(sc, AlgorithmRandom, opt)
	batch := make([][]float64, 0, evalChunk)
	for i, n := 0, budgetOf(opt); i < n; i++ {
		batch = append(batch, SampleDesign(rng, opt.Spec))
		if len(batch) == evalChunk {
			st.evalAll(batch, 0, nil)
			batch = batch[:0]
		}
	}
	if len(batch) > 0 {
		st.evalAll(batch, 0, nil)
	}
	return st.result(opt)
}

// LHS: one design per stratum per dimension; stratum order permuted per
// dimension from the same seeded RNG, then mapped into the box and canonicalised.
//
// Point i uses stratum perm_j[i] in dimension j and samples uniformly inside
// that stratum; RNG draws are taken dimension-major (dimension j, then all n
// strata) so the stream is fixed by (seed, budget, spec) alone.
func LHS(sc runner.Scorer, opt Options) Result {
	spec := opt.Spec
	rng := rand.New(rand.NewSource(int64(opt.Seed)))
	n := budgetOf(opt)
	d := spec.NParams()
	lo, hi := spec.Lower(), spec.Upper()

	xs := make([][]float64, n)
	for i := range xs {
		xs[i] = make([]float64, d)
	}
	for j := 0; j < d; j++ {
		perm := rng.Perm(n)
		width := (hi[j] - lo[j]) / float64(n)
		for i := 0; i < n; i++ {
			u := float64(perm[i]) + rng.Float64() // one draw per (dimension, stratum)
			xs[i][j] = lo[j] + u*width
		}
	}
	for i := range xs {
		xs[i] = Canonicalise(xs[i], spec)
	}

	st := newRunState(sc, AlgorithmLHS, opt)
	st.evalAll(xs, 0, nil)
	return st.result(opt)
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
	return evolutionRun(sc, opt, AlgorithmEvolution, opt.WarmStart)
}

// EvolutionWarm is Evolution with opt.WarmStart defaulting to the textbook
// mirror design when none is supplied. Algorithm name: "evolution_warm".
func EvolutionWarm(sc runner.Scorer, opt Options) Result {
	warm := opt.WarmStart
	if len(warm) == 0 {
		warm = WarmStartDesign(opt.Spec)
	}
	return evolutionRun(sc, opt, AlgorithmEvolutionWarm, warm)
}

// Methods maps a method name to its implementation.
var Methods = map[string]func(runner.Scorer, Options) Result{
	AlgorithmRandom:        Random,
	AlgorithmLHS:           LHS,
	AlgorithmEvolution:     Evolution,
	AlgorithmEvolutionWarm: EvolutionWarm,
}

// MethodNames returns the known method names, sorted (for CLI help and error
// messages). Additive helper — no frozen signature was touched.
func MethodNames() []string {
	out := make([]string, 0, len(Methods))
	for name := range Methods {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Run dispatches by name and errors on an unknown method.
//
// An unknown name is a hard error and never falls back to a default algorithm:
// a run must not be recorded under a method that does not exist.
func Run(method string, sc runner.Scorer, opt Options) (Result, error) {
	fn, ok := Methods[method]
	if !ok {
		return Result{}, fmt.Errorf("search: unknown method %q (known: %s)", method, strings.Join(MethodNames(), ", "))
	}
	opt.Algorithm = method
	return fn(sc, opt), nil
}
