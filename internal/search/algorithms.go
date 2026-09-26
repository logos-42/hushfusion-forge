// Shared machinery for the search algorithms of api.go.
//
// Two invariants are enforced here rather than in each algorithm:
//
//   - the budget is EXACT: designs are only ever produced when a slot of the
//     budget is still free (see the min(...) guards), so NEvals == Budget always;
//   - results never depend on Options.Workers: results are stored by logical
//     evaluation index, the RNG is only ever advanced on the calling goroutine,
//     and the selection sort is stable over a logically ordered candidate slice.
//     Concurrency may only change the order in which a Scorer records, which is
//     the documented exception in api.go.
package search

import (
	"math"
	"sync"
	"sync/atomic"

	"github.com/logos-42/hushfusion-forge/internal/objective"
	"github.com/logos-42/hushfusion-forge/internal/runner"
)

// evalChunk is how many designs are handed to the scorer per concurrent batch.
// It is a constant, so it cannot make results depend on Workers.
const evalChunk = 512

// runState accumulates the evaluations of one run in logical (EvalIndex) order.
type runState struct {
	sc      runner.Scorer
	name    string
	seed    int
	workers int

	results []objective.EvalResult // indexed by EvalIndex
	designs [][]float64            // the designs submitted, indexed by EvalIndex
}

func newRunState(sc runner.Scorer, name string, opt Options) *runState {
	return &runState{
		sc:      sc,
		name:    name,
		seed:    opt.Seed,
		workers: opt.Workers,
		results: make([]objective.EvalResult, 0, max(opt.Budget, 0)),
		designs: make([][]float64, 0, max(opt.Budget, 0)),
	}
}

func (s *runState) nEvals() int { return len(s.results) }

// evalAll scores xs (in order) in chunks of evalChunk and appends the results in
// the same logical order. parents is optional: when non-nil, parents[i] becomes
// the lineage edge of xs[i].
func (s *runState) evalAll(xs [][]float64, gen int, parents []string) {
	for start := 0; start < len(xs); start += evalChunk {
		end := min(start+evalChunk, len(xs))
		metas := make([]runner.Meta, end-start)
		for i := start; i < end; i++ {
			parent := ""
			if parents != nil {
				parent = parents[i]
			}
			metas[i-start] = runner.Meta{
				Algorithm:  s.name,
				Seed:       s.seed,
				Generation: gen,
				EvalIndex:  s.nEvals() + (i - start),
				Parent:     parent,
			}
		}
		results := s.pass(xs[start:end], metas)
		s.results = append(s.results, results...)
		s.designs = append(s.designs, xs[start:end]...)
	}
}

// pass evaluates one batch. Workers <= 1 runs it inline; more workers pull
// indices from a shared counter, so each result is still written to its own
// logical slot and the outcome is independent of scheduling.
func (s *runState) pass(xs [][]float64, metas []runner.Meta) []objective.EvalResult {
	out := make([]objective.EvalResult, len(xs))
	if s.workers <= 1 || len(xs) <= 1 {
		for i := range xs {
			out[i] = s.sc.Score(xs[i], metas[i])
		}
		return out
	}
	var next atomic.Int64
	next.Store(-1)
	var wg sync.WaitGroup
	for w := 0; w < s.workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1))
				if i >= len(xs) {
					return
				}
				out[i] = s.sc.Score(xs[i], metas[i])
			}
		}()
	}
	wg.Wait()
	return out
}

// evaluatedDesign is the canonical vector actually evaluated at EvalIndex i: the
// scorer's echo of it when it reports one, otherwise what was submitted.
func (s *runState) evaluatedDesign(i int) []float64 {
	if d := s.results[i].Design; len(d) > 0 {
		return append([]float64(nil), d...)
	}
	return append([]float64(nil), s.designs[i]...)
}

// result folds the evaluations into the frozen Result: best-so-far trajectory,
// the best evaluation (earliest wins ties, so it is deterministic) and the index
// of the first evaluation that exceeded the baseline.
func (s *runState) result(opt Options) Result {
	res := Result{
		Algorithm:   s.name,
		Seed:        s.seed,
		Budget:      opt.Budget,
		NEvals:      len(s.results),
		EvalsToBeat: -1,
		History:     make([]float64, 0, len(s.results)),
	}
	best := -1
	run := math.Inf(-1)
	for i := range s.results {
		r := &s.results[i]
		if best < 0 || r.Score > s.results[best].Score {
			best = i
		}
		if r.Score > run {
			run = r.Score
		}
		res.History = append(res.History, run)
		if res.EvalsToBeat < 0 && r.Score > opt.BaselineScore {
			res.EvalsToBeat = i
		}
	}
	if best >= 0 {
		b := &s.results[best]
		res.BestScore = b.Score
		res.BestTerms = b.Terms
		res.BestMetrics = b.Metrics
		res.BestDesignID = b.DesignID
		res.BestFeasible = b.Feasible
		res.BestDesign = s.evaluatedDesign(best)
	}
	return res
}

func budgetOf(opt Options) int { return max(opt.Budget, 0) }

// positive applies the documented default to an unset (<= 0) evolution knob, so
// a zero-valued Options behaves like DefaultOptions.
func positive(v, def float64) float64 {
	if v <= 0 {
		return def
	}
	return v
}

func positiveInt(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}
