// Elitist (mu+lambda) evolution strategy — the shared body of Evolution and
// EvolutionWarm (api.go). The only difference between the two is where the
// warm-start design comes from.
//
// Determinism notes (why Workers cannot change a Result):
//
//   - every child is drawn on the calling goroutine, in a fixed order, before
//     any evaluation happens;
//   - results are stored by logical evaluation index, never in completion order;
//   - selection is a STABLE sort of (parents ++ children) by score descending, so
//     ties keep the logical order irrespective of which child finished first;
//   - the best-so-far trajectory is folded from the logical index order.
package search

import (
	"math"
	"math/rand"
	"sort"

	"github.com/logos-42/hushfusion-forge/internal/runner"
)

// member is one member of the current population.
type member struct {
	design []float64 // canonical design the scorer was handed
	score  float64
	id     string // design_id returned by the scorer (lineage handle)
}

// evolutionRun is Evolution's body with the warm-start design and the algorithm
// name passed in. When warm is empty the initial population is pure i.i.d. random.
func evolutionRun(sc runner.Scorer, opt Options, name string, warm []float64) Result {
	spec := opt.Spec
	budget := budgetOf(opt)
	mu := positiveInt(opt.Mu, DefaultMu)
	lam := positiveInt(opt.Lam, DefaultLam)
	sigma0 := positive(opt.Sigma0, DefaultSigma0)
	sigmaFloor := positive(opt.SigmaFloor, DefaultSigmaFloor)

	d := spec.NParams()
	lo, hi := spec.Lower(), spec.Upper()
	rng := rand.New(rand.NewSource(int64(opt.Seed)))
	st := newRunState(sc, name, opt)

	// --- generation 0: the initial population (the warm start is drawn first) --
	nInit := min(mu, budget)
	xs := make([][]float64, 0, nInit)
	for i := 0; i < nInit; i++ {
		if i == 0 && len(warm) == d {
			xs = append(xs, Canonicalise(append([]float64(nil), warm...), spec))
			continue
		}
		xs = append(xs, SampleDesign(rng, spec))
	}
	st.evalAll(xs, 0, nil)

	pop := make([]member, 0, mu)
	for i := range xs {
		pop = append(pop, member{
			design: st.evaluatedDesign(i),
			score:  st.results[i].Score,
			id:     st.results[i].DesignID,
		})
	}

	// --- generations 1.. : children, then (mu+lambda) elitist selection --------
	for gen := 1; st.nEvals() < budget; gen++ {
		spent := st.nEvals()
		sigma := math.Max(sigmaFloor, sigma0*(1.0-float64(spent)/float64(budget)))
		k := min(lam, budget-spent)

		children := make([][]float64, 0, k)
		parents := make([]string, 0, k)
		for j := 0; j < k; j++ {
			p := rng.Intn(len(pop))
			child := make([]float64, d)
			for t := 0; t < d; t++ {
				step := sigma * (hi[t] - lo[t]) * rng.NormFloat64()
				child[t] = pop[p].design[t] + step
			}
			children = append(children, Canonicalise(child, spec))
			parents = append(parents, pop[p].id)
		}
		st.evalAll(children, gen, parents)

		first := st.nEvals() - len(children)
		cand := make([]member, 0, len(pop)+len(children))
		cand = append(cand, pop...)
		for j := range children {
			i := first + j
			cand = append(cand, member{
				design: st.evaluatedDesign(i),
				score:  st.results[i].Score,
				id:     st.results[i].DesignID,
			})
		}
		sort.SliceStable(cand, func(a, b int) bool { return cand[a].score > cand[b].score })
		if len(cand) > mu {
			cand = cand[:mu]
		}
		pop = cand
	}
	return st.result(opt)
}
