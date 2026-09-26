// Unit tests for the four search algorithms.
//
// Design choice (this is the reason runner.Scorer exists): every test injects a
// TOY scorer instead of a real *runner.Runner. The toy is a deterministic smooth
// bowl on the design box whose optimum sits exactly on the human baseline
// design, and it synthesises its own ascending design ids ("D0001"...). Nothing
// here touches internal/physics or internal/registry, so stage D is verified
// end-to-end on its own, and the tests stay fast and bit-exact.
//
// What is proven here:
//
//	budget exactness      NEvals == Budget for every method, including budgets
//	                      that do not divide mu/lambda and a chunk boundary
//	trajectory            len(History) == Budget, monotone non-decreasing,
//	                      History[last] == BestScore, BestScore == max scored
//	reproducibility       same Options -> bit-identical Result (twice)
//	Workers invariance    Workers=4/8 == Workers=1, bit-identical scores,
//	                      trajectory, best design and the multiset of evaluated
//	                      designs (run this file under -race)
//	quality               evolution beats random's mean over 3 seeds, equal budget
//	warm start            EvolutionWarm's first evaluation IS the warm design
//	lineage               every child record carries the design_id of a parent
//	                      that really exists, from an earlier generation
//	EvalsToBeat           first index above the baseline, -1 when never beaten
//	LHS structure         one point per stratum per dimension, canonical + in box
//	dispatch              unknown method is a hard error
package search

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/logos-42/hushfusion-forge/internal/config"
	"github.com/logos-42/hushfusion-forge/internal/objective"
	"github.com/logos-42/hushfusion-forge/internal/physics"
	"github.com/logos-42/hushfusion-forge/internal/runner"
)

// ---------------------------------------------------------------- toy scorer --

// toyScorer implements runner.Scorer. Scores are a pure function of the design;
// the id counter is the only mutable state and it is mutex-guarded, so the toy
// is safe for the concurrent (Workers > 1) path.
type toyScorer struct {
	spec config.Spec
	opt  []float64 // optimum, in normalised box coordinates
	seq  []float64 // when non-nil, score = seq[EvalIndex % len(seq)] (x ignored)

	mu      sync.Mutex
	n       int
	seen    map[string]bool
	designs [][]float64
	metas   []runner.Meta
	ids     []string
	scores  []float64
}

func newToyScorer(spec config.Spec) *toyScorer {
	base := textbookMirrorDesign // the toy optimum IS the human baseline design
	lo, hi := spec.Lower(), spec.Upper()
	opt := make([]float64, len(base))
	for j := range opt {
		opt[j] = (base[j] - lo[j]) / (hi[j] - lo[j])
	}
	return &toyScorer{spec: spec, opt: opt, seen: map[string]bool{}}
}

func (s *toyScorer) score(x []float64, meta runner.Meta) float64 {
	if s.seq != nil {
		return s.seq[meta.EvalIndex%len(s.seq)]
	}
	lo, hi := s.spec.Lower(), s.spec.Upper()
	total := 0.0
	for j := range x {
		u := (x[j] - lo[j]) / (hi[j] - lo[j]) // normalised box coordinate
		d := u - s.opt[j]
		total -= d * d
	}
	return total
}

func (s *toyScorer) Score(x []float64, meta runner.Meta) objective.EvalResult {
	score := s.score(x, meta)
	design := append([]float64(nil), x...)

	s.mu.Lock()
	s.n++
	id := fmt.Sprintf("D%04d", s.n)
	experimentID := s.n
	s.designs = append(s.designs, design)
	s.metas = append(s.metas, meta)
	s.ids = append(s.ids, id)
	s.scores = append(s.scores, score)
	s.seen[id] = true
	s.mu.Unlock()

	return objective.EvalResult{
		Score:        score,
		Terms:        map[string]float64{"toy": score},
		Weighted:     map[string]float64{"toy": score},
		Penalties:    map[string]float64{},
		Feasible:     true,
		Design:       design,
		DesignID:     id,
		ExperimentID: experimentID,
	}
}

func (s *toyScorer) snapshot() (designs [][]float64, metas []runner.Meta, ids []string, scores []float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]float64(nil), s.designs...),
		append([]runner.Meta(nil), s.metas...),
		append([]string(nil), s.ids...),
		append([]float64(nil), s.scores...)
}

// fingerprint is the order-independent multiset of evaluated designs: it must be
// identical for Workers = 1 and Workers = 4 even though the recording order is
// not.
func (s *toyScorer) fingerprint() string {
	designs, _, _, _ := s.snapshot()
	parts := make([]string, len(designs))
	for i, d := range designs {
		parts[i] = fmt.Sprintf("%v", d)
	}
	sort.Strings(parts)
	return strings.Join(parts, "|")
}

// --------------------------------------------------------------- test helpers --

const testBudget = 600

func toyOptions(spec config.Spec, budget, seed int) Options {
	opt := DefaultOptions(spec)
	opt.Seed = seed
	opt.Budget = budget
	return opt
}

func floatsEqual(a, b []float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if math.Float64bits(a[i]) != math.Float64bits(b[i]) {
			return false
		}
	}
	return true
}

// sameResult describes the first difference between two Results, "" when they
// agree bit-for-bit. strictIDs is off only for the Workers comparison, where the
// registry (here: the toy id counter) may hand out ids in completion order —
// the documented single exception to reproducibility.
func sameResult(got, want Result, strictIDs bool) string {
	switch {
	case got.Algorithm != want.Algorithm:
		return fmt.Sprintf("Algorithm %q != %q", got.Algorithm, want.Algorithm)
	case got.Seed != want.Seed:
		return fmt.Sprintf("Seed %d != %d", got.Seed, want.Seed)
	case got.Budget != want.Budget:
		return fmt.Sprintf("Budget %d != %d", got.Budget, want.Budget)
	case got.NEvals != want.NEvals:
		return fmt.Sprintf("NEvals %d != %d", got.NEvals, want.NEvals)
	case math.Float64bits(got.BestScore) != math.Float64bits(want.BestScore):
		return fmt.Sprintf("BestScore %v(%x) != %v(%x)", got.BestScore, math.Float64bits(got.BestScore), want.BestScore, math.Float64bits(want.BestScore))
	case !floatsEqual(got.BestDesign, want.BestDesign):
		return fmt.Sprintf("BestDesign %v != %v", got.BestDesign, want.BestDesign)
	case !floatsEqual(got.History, want.History):
		return fmt.Sprintf("History differs (len %d vs %d)", len(got.History), len(want.History))
	case got.EvalsToBeat != want.EvalsToBeat:
		return fmt.Sprintf("EvalsToBeat %d != %d", got.EvalsToBeat, want.EvalsToBeat)
	case got.BestFeasible != want.BestFeasible:
		return fmt.Sprintf("BestFeasible %v != %v", got.BestFeasible, want.BestFeasible)
	case fmt.Sprint(got.BestTerms) != fmt.Sprint(want.BestTerms):
		return fmt.Sprintf("BestTerms %v != %v", got.BestTerms, want.BestTerms)
	}
	if strictIDs && got.BestDesignID != want.BestDesignID {
		return fmt.Sprintf("BestDesignID %q != %q", got.BestDesignID, want.BestDesignID)
	}
	return ""
}

func allMethods() []string {
	return []string{AlgorithmRandom, AlgorithmLHS, AlgorithmEvolution, AlgorithmEvolutionWarm}
}

// checkInsideBoxAndCanonical asserts every design handed to the scorer is inside
// the box and in canonical (z-ascending) order — the mutation/decoding contract.
func checkInsideBoxAndCanonical(t *testing.T, spec config.Spec, designs [][]float64) {
	t.Helper()
	lo, hi := spec.Lower(), spec.Upper()
	nc := spec.NCoils
	for i, x := range designs {
		if len(x) != spec.NParams() {
			t.Fatalf("design %d has length %d, want %d", i, len(x), spec.NParams())
		}
		for j, v := range x {
			if v < lo[j] || v > hi[j] {
				t.Fatalf("design %d param %d = %v outside [%v, %v]", i, j, v, lo[j], hi[j])
			}
		}
		for k := 1; k < nc; k++ {
			if x[nc+k] < x[nc+k-1] {
				t.Fatalf("design %d is not canonical: z = %v", i, x[nc:nc+nc])
			}
		}
	}
}

// --------------------------------------------------------- default / constants --

func TestDefaultOptionsMatchesDocumentedDefaults(t *testing.T) {
	spec := config.DefaultSpec()
	opt := DefaultOptions(spec)

	if opt.Spec.NCoils != spec.NCoils || opt.Spec.Bounds != spec.Bounds {
		t.Errorf("DefaultOptions did not carry the spec")
	}
	if opt.Budget != 1000 {
		t.Errorf("Budget = %d, want 1000", opt.Budget)
	}
	if opt.Mu != 16 || opt.Lam != 48 {
		t.Errorf("Mu/Lam = %d/%d, want 16/48", opt.Mu, opt.Lam)
	}
	if opt.Sigma0 != 0.25 || opt.SigmaFloor != 0.03 {
		t.Errorf("Sigma0/SigmaFloor = %v/%v, want 0.25/0.03", opt.Sigma0, opt.SigmaFloor)
	}
	if opt.Workers != 1 {
		t.Errorf("Workers = %d, want 1", opt.Workers)
	}
	if opt.WarmStart != nil {
		t.Errorf("WarmStart = %v, want nil (evolution_warm supplies the default)", opt.WarmStart)
	}
	if opt.Algorithm != AlgorithmEvolution {
		t.Errorf("Algorithm = %q, want %q", opt.Algorithm, AlgorithmEvolution)
	}
}

func TestConstantsCoverMethods(t *testing.T) {
	if len(Methods) != 4 {
		t.Fatalf("Methods has %d entries, want 4: %v", len(Methods), MethodNames())
	}
	want := allMethods()
	sort.Strings(want)
	if got := MethodNames(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("MethodNames() = %v, want %v", got, want)
	}
}

// ------------------------------------------------------------------ budget ----

func TestBudgetIsExactAndHistoryIsBestSoFar(t *testing.T) {
	spec := config.DefaultSpec()
	for _, budget := range []int{0, 1, 2, 7, 47, 48, 49, 100, 513, 1000} {
		for _, method := range allMethods() {
			t.Run(fmt.Sprintf("%s/budget=%d", method, budget), func(t *testing.T) {
				sc := newToyScorer(spec)
				res, err := Run(method, sc, toyOptions(spec, budget, 3))
				if err != nil {
					t.Fatalf("Run: %v", err)
				}
				if res.NEvals != budget {
					t.Fatalf("NEvals = %d, want exactly Budget = %d", res.NEvals, budget)
				}
				if res.Budget != budget {
					t.Fatalf("Budget field = %d, want %d", res.Budget, budget)
				}
				if len(res.History) != budget {
					t.Fatalf("len(History) = %d, want %d", len(res.History), budget)
				}
				for i := 1; i < len(res.History); i++ {
					if res.History[i] < res.History[i-1] {
						t.Fatalf("History not monotone at %d: %v -> %v", i, res.History[i-1], res.History[i])
					}
				}

				designs, metas, _, rawScores := sc.snapshot()
				if len(designs) != budget {
					t.Fatalf("scorer saw %d designs, want %d", len(designs), budget)
				}
				// EvalIndex must be exactly 0..budget-1, in order.
				for i, m := range metas {
					if m.EvalIndex != i {
						t.Fatalf("meta[%d].EvalIndex = %d, want %d", i, m.EvalIndex, i)
					}
					if m.Algorithm != method || m.Seed != 3 {
						t.Fatalf("meta[%d] = %+v, want algorithm %q seed 3", i, m, method)
					}
				}
				if budget == 0 {
					if res.BestDesign != nil || res.EvalsToBeat != -1 {
						t.Fatalf("empty run should have no best design and EvalsToBeat -1, got %v / %d", res.BestDesign, res.EvalsToBeat)
					}
					return
				}
				// Independent recomputation of the trajectory from what the
				// scorer actually received.
				wantBestIdx, wantRun := 0, math.Inf(-1)
				var wantHist []float64
				for i, v := range rawScores {
					if v > wantRun {
						wantRun = v
					}
					wantHist = append(wantHist, wantRun)
					if v > rawScores[wantBestIdx] {
						wantBestIdx = i
					}
				}
				if !floatsEqual(res.History, wantHist) {
					t.Fatalf("History != recomputed best-so-far\n got %v\nwant %v", res.History, wantHist)
				}
				if math.Float64bits(res.BestScore) != math.Float64bits(wantHist[len(wantHist)-1]) {
					t.Fatalf("BestScore %v != last History %v", res.BestScore, wantHist[len(wantHist)-1])
				}
				if res.BestScore != wantRun {
					t.Fatalf("BestScore %v != max scored %v", res.BestScore, wantRun)
				}
				if !floatsEqual(res.BestDesign, designs[wantBestIdx]) {
					t.Fatalf("BestDesign %v != design of the argmax %v", res.BestDesign, designs[wantBestIdx])
				}
				checkInsideBoxAndCanonical(t, spec, designs)
			})
		}
	}
}

// ------------------------------------------------------------ reproducibility --

func TestReproducibilityIsBitIdentical(t *testing.T) {
	spec := config.DefaultSpec()
	for _, seed := range []int{7, 11} {
		for _, method := range allMethods() {
			t.Run(fmt.Sprintf("%s/seed=%d", method, seed), func(t *testing.T) {
				a, err := Run(method, newToyScorer(spec), toyOptions(spec, testBudget, seed))
				if err != nil {
					t.Fatal(err)
				}
				b, err := Run(method, newToyScorer(spec), toyOptions(spec, testBudget, seed))
				if err != nil {
					t.Fatal(err)
				}
				if diff := sameResult(a, b, true); diff != "" {
					t.Fatalf("same Options gave different Results: %s", diff)
				}
				if a.NEvals != testBudget {
					t.Fatalf("NEvals = %d, want %d", a.NEvals, testBudget)
				}
			})
		}
	}
}

// TestDifferentSeedsDiverge guards against a Seed that is silently ignored. It
// compares the multiset of evaluated designs rather than the trajectory, because
// evolution_warm's trajectory is legitimately flat in this toy: the warm start
// IS the toy optimum, so every seed's best-so-far is already maximal at
// evaluation 0.
func TestDifferentSeedsDiverge(t *testing.T) {
	spec := config.DefaultSpec()
	for _, method := range allMethods() {
		scA := newToyScorer(spec)
		scB := newToyScorer(spec)
		if _, err := Run(method, scA, toyOptions(spec, 200, 1)); err != nil {
			t.Fatal(err)
		}
		if _, err := Run(method, scB, toyOptions(spec, 200, 2)); err != nil {
			t.Fatal(err)
		}
		if scA.fingerprint() == scB.fingerprint() {
			t.Errorf("%s: seeds 1 and 2 evaluated exactly the same designs — seed is ignored", method)
		}
	}
}

// --------------------------------------------------------- Workers invariance --

func TestWorkersDoNotChangeResults(t *testing.T) {
	spec := config.DefaultSpec()
	for _, method := range allMethods() {
		t.Run(method, func(t *testing.T) {
			refSc := newToyScorer(spec)
			ref, err := Run(method, refSc, toyOptions(spec, testBudget, 7))
			if err != nil {
				t.Fatal(err)
			}
			refFP := refSc.fingerprint()
			for _, w := range []int{2, 4, 8} {
				sc := newToyScorer(spec)
				opt := toyOptions(spec, testBudget, 7)
				opt.Workers = w
				got, err := Run(method, sc, opt)
				if err != nil {
					t.Fatal(err)
				}
				if diff := sameResult(got, ref, false); diff != "" {
					t.Fatalf("Workers=%d changed the Result: %s", w, diff)
				}
				if got.BestDesignID != ref.BestDesignID {
					t.Logf("Workers=%d: BestDesignID %q vs %q (registry order may differ; ids are excluded)", w, got.BestDesignID, ref.BestDesignID)
				}
				if fp := sc.fingerprint(); fp != refFP {
					t.Fatalf("Workers=%d evaluated a different multiset of designs", w)
				}
			}
		})
	}
}

// ------------------------------------------------------------------- quality --

func TestEvolutionBeatsRandomOnTheToyProblem(t *testing.T) {
	spec := config.DefaultSpec()
	const budget = 1000
	seeds := []int{1, 2, 3}

	var randSum, evoSum, lhsSum, warmSum float64
	for _, seed := range seeds {
		r, err := Run(AlgorithmRandom, newToyScorer(spec), toyOptions(spec, budget, seed))
		if err != nil {
			t.Fatal(err)
		}
		e, err := Run(AlgorithmEvolution, newToyScorer(spec), toyOptions(spec, budget, seed))
		if err != nil {
			t.Fatal(err)
		}
		l, err := Run(AlgorithmLHS, newToyScorer(spec), toyOptions(spec, budget, seed))
		if err != nil {
			t.Fatal(err)
		}
		w, err := Run(AlgorithmEvolutionWarm, newToyScorer(spec), toyOptions(spec, budget, seed))
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("seed %d  random=%.6f  lhs=%.6f  evolution=%.6f  evolution_warm=%.6f", seed, r.BestScore, l.BestScore, e.BestScore, w.BestScore)
		randSum += r.BestScore
		evoSum += e.BestScore
		lhsSum += l.BestScore
		warmSum += w.BestScore
	}
	n := float64(len(seeds))
	t.Logf("mean over %d seeds  random=%.6f  lhs=%.6f  evolution=%.6f  evolution_warm=%.6f", len(seeds), randSum/n, lhsSum/n, evoSum/n, warmSum/n)
	if !(evoSum/n > randSum/n) {
		t.Fatalf("evolution mean (%.6f) did not beat random mean (%.6f) at equal budget %d", evoSum/n, randSum/n, budget)
	}
}

// --------------------------------------------------------------- warm start ---

func TestWarmStartIsTheFirstEvaluation(t *testing.T) {
	spec := config.DefaultSpec()

	t.Run("explicit warm start", func(t *testing.T) {
		sc := newToyScorer(spec)
		opt := toyOptions(spec, testBudget, 5)
		opt.WarmStart = append([]float64(nil), textbookMirrorDesign...)
		res, err := Run(AlgorithmEvolutionWarm, sc, opt)
		if err != nil {
			t.Fatal(err)
		}
		if res.Algorithm != AlgorithmEvolutionWarm {
			t.Fatalf("Algorithm = %q, want %q", res.Algorithm, AlgorithmEvolutionWarm)
		}
		designs, metas, _, _ := sc.snapshot()
		if len(designs) == 0 {
			t.Fatal("nothing was evaluated")
		}
		if !floatsEqual(designs[0], opt.WarmStart) {
			t.Fatalf("first evaluation %v is not the warm-start design %v (knowledge wired but never injected)", designs[0], opt.WarmStart)
		}
		if metas[0].EvalIndex != 0 || metas[0].Generation != 0 || metas[0].Parent != "" {
			t.Fatalf("warms start meta = %+v, want EvalIndex 0, Generation 0, no parent", metas[0])
		}
	})

	t.Run("default warm start", func(t *testing.T) {
		sc := newToyScorer(spec)
		res, err := Run(AlgorithmEvolutionWarm, sc, toyOptions(spec, testBudget, 5))
		if err != nil {
			t.Fatal(err)
		}
		want := WarmStartDesign(spec)
		designs, _, _, _ := sc.snapshot()
		if !floatsEqual(designs[0], want) {
			t.Fatalf("first evaluation %v is not WarmStartDesign %v", designs[0], want)
		}
		if !floatsEqual(want, textbookMirrorDesign) {
			t.Fatalf("default warm start %v is not the textbook mirror %v", want, textbookMirrorDesign)
		}
		checkInsideBoxAndCanonical(t, spec, [][]float64{want})
		// The knowledge must actually pay: the toy optimum is the baseline.
		if res.BestScore < -1e-12 {
			t.Fatalf("warm start began at the toy optimum but BestScore = %v", res.BestScore)
		}
	})

	t.Run("plain evolution honours a supplied warm start", func(t *testing.T) {
		sc := newToyScorer(spec)
		opt := toyOptions(spec, testBudget, 5)
		opt.WarmStart = append([]float64(nil), textbookMirrorDesign...)
		res, err := Run(AlgorithmEvolution, sc, opt)
		if err != nil {
			t.Fatal(err)
		}
		if res.Algorithm != AlgorithmEvolution {
			t.Fatalf("Algorithm = %q, want %q", res.Algorithm, AlgorithmEvolution)
		}
		designs, _, _, _ := sc.snapshot()
		if !floatsEqual(designs[0], opt.WarmStart) {
			t.Fatalf("first evaluation %v is not the warm-start design", designs[0])
		}
	})

	t.Run("no warm start means random init", func(t *testing.T) {
		sc := newToyScorer(spec)
		if _, err := Run(AlgorithmEvolution, sc, toyOptions(spec, testBudget, 5)); err != nil {
			t.Fatal(err)
		}
		designs, _, _, _ := sc.snapshot()
		if floatsEqual(designs[0], textbookMirrorDesign) {
			t.Fatal("evolution without WarmStart injected the baseline anyway")
		}
	})
}

// ------------------------------------------------------------------ lineage ---

func TestLineageParentsExistAndPrecedeTheirChildren(t *testing.T) {
	spec := config.DefaultSpec()
	for _, method := range []string{AlgorithmEvolution, AlgorithmEvolutionWarm} {
		for _, workers := range []int{1, 4} {
			t.Run(fmt.Sprintf("%s/workers=%d", method, workers), func(t *testing.T) {
				sc := newToyScorer(spec)
				opt := toyOptions(spec, testBudget, 9)
				opt.Workers = workers
				res, err := Run(method, sc, opt)
				if err != nil {
					t.Fatal(err)
				}
				designs, metas, ids, _ := sc.snapshot()

				// id bookkeeping: when was each design first seen, and in which generation?
				seenGen := map[string]int{}
				seenIdx := map[string]int{}
				roots, children := 0, 0
				for i := range metas {
					m := metas[i]
					if m.Generation == 0 {
						roots++
						if m.Parent != "" {
							t.Fatalf("root %s (eval %d) has parent %q", ids[i], i, m.Parent)
						}
					} else {
						children++
						if m.Parent == "" {
							t.Fatalf("child %s (eval %d, generation %d) has no parent: lineage is broken", ids[i], i, m.Generation)
						}
						g, ok := seenGen[m.Parent]
						if !ok {
							t.Fatalf("child %s references parent %q, which no earlier evaluation produced", ids[i], m.Parent)
						}
						if g >= m.Generation {
							t.Fatalf("child %s in generation %d references parent %q from generation %d", ids[i], m.Generation, m.Parent, g)
						}
						if seenIdx[m.Parent] > i {
							t.Fatalf("parent %q was recorded after its child %s", m.Parent, ids[i])
						}
					}
					if _, dup := seenGen[ids[i]]; dup {
						t.Fatalf("design id %q was handed out twice", ids[i])
					}
					seenGen[ids[i]] = m.Generation
					seenIdx[ids[i]] = i
				}
				if roots == 0 || children == 0 {
					t.Fatalf("expected a non-empty lineage: %d roots, %d children", roots, children)
				}
				if roots != 16 {
					t.Fatalf("initial population = %d, want mu = 16", roots)
				}
				if got := roots + children; got != testBudget {
					t.Fatalf("scored %d designs, want %d", got, testBudget)
				}
				if res.BestDesignID == "" {
					t.Fatal("BestDesignID is empty; the search layer needs ids from the scorer for lineage")
				}
				_ = designs
			})
		}
	}
}

// --------------------------------------------------------------- EvalsToBeat --

func TestEvalsToBeat(t *testing.T) {
	spec := config.DefaultSpec()
	seq := []float64{0.1, 0.5, -1.0, 0.9, 0.2}
	cases := []struct {
		baseline float64
		want     int
		why      string
	}{
		{0.6, 3, "first score strictly above 0.6 is seq[3]"},
		{0.5, 3, "exactly equal does not count as beating"},
		{0.9, -1, "never strictly exceeded"},
		{0.2, 1, "seq[1]=0.5 already exceeds 0.2"},
		{0.1, 1, "seq[0]=0.1 is not > 0.1"},
		{1.0, -1, "baseline above every score"},
		{-2.0, 0, "baseline below every score: the first evaluation beats it"},
	}
	for _, method := range allMethods() {
		for _, tc := range cases {
			t.Run(fmt.Sprintf("%s/baseline=%v", method, tc.baseline), func(t *testing.T) {
				sc := &toyScorer{spec: spec, seq: seq, seen: map[string]bool{}}
				opt := toyOptions(spec, len(seq), 4)
				opt.BaselineScore = tc.baseline
				res, err := Run(method, sc, opt)
				if err != nil {
					t.Fatal(err)
				}
				if res.EvalsToBeat != tc.want {
					t.Fatalf("EvalsToBeat = %d, want %d (%s)", res.EvalsToBeat, tc.want, tc.why)
				}
				if res.NEvals != len(seq) {
					t.Fatalf("NEvals = %d, want %d", res.NEvals, len(seq))
				}
			})
		}
	}

	t.Run("cross-check against the recorded scores", func(t *testing.T) {
		sc := newToyScorer(spec)
		opt := toyOptions(spec, 300, 6)
		opt.BaselineScore = -1.0
		res, err := Run(AlgorithmRandom, sc, opt)
		if err != nil {
			t.Fatal(err)
		}
		_, _, _, raw := sc.snapshot()
		want := -1
		for i := range raw {
			if raw[i] > opt.BaselineScore {
				want = i
				break
			}
		}
		if res.EvalsToBeat != want {
			t.Fatalf("EvalsToBeat = %d, want %d from the recorded scores", res.EvalsToBeat, want)
		}
		if res.History[res.EvalsToBeat] <= opt.BaselineScore {
			t.Fatalf("history at EvalsToBeat (%v) does not exceed the baseline %v", res.History[res.EvalsToBeat], opt.BaselineScore)
		}
	})
}

// --------------------------------------------------------------------- LHS ----

func TestLHSStratificationAndDeterminism(t *testing.T) {
	spec := config.DefaultSpec()
	const n = 60
	nc := spec.NCoils
	lo, hi := spec.Lower(), spec.Upper()
	for _, seed := range []int{1, 2, 3, 4, 5, 6} {
		sc := newToyScorer(spec)
		if _, err := Run(AlgorithmLHS, sc, toyOptions(spec, n, seed)); err != nil {
			t.Fatal(err)
		}
		designs, _, _, _ := sc.snapshot()
		if len(designs) != n {
			t.Fatalf("seed %d: scored %d designs, want %d", seed, len(designs), n)
		}
		checkInsideBoxAndCanonical(t, spec, designs)

		// One point per stratum per dimension.
		//
		// The check has to be permutation-invariant: canonicalisation reorders a
		// design's (r, z, I) triples by z, so the k-th coil of design i is NOT
		// the k-th stratum draw. What canonicalisation cannot change is the
		// multiset of values per parameter family (radius / z / current), so the
		// invariant is: over all n designs and all nc coil positions of a
		// family, every stratum of that family is hit exactly nc times.
		for fam := 0; fam < 3; fam++ {
			j := fam * nc
			width := (hi[j] - lo[j]) / float64(n)
			counts := make([]int, n)
			total := 0
			for _, x := range designs {
				for p := 0; p < nc; p++ {
					v := x[fam*nc+p]
					if v < lo[j] || v > hi[j] {
						t.Fatalf("seed %d family %d: value %v outside [%v, %v]", seed, fam, v, lo[j], hi[j])
					}
					k := int((v - lo[j]) / width)
					if k < 0 || k >= n {
						k = n - 1 // a value rounding to exactly hi belongs to the last stratum
					}
					counts[k]++
					total++
				}
			}
			if total != n*nc {
				t.Fatalf("seed %d family %d: counted %d values, want %d", seed, fam, total, n*nc)
			}
			for k, c := range counts {
				if c != nc {
					t.Fatalf("seed %d family %d stratum %d hit %d times, want exactly %d (one per design per dimension)", seed, fam, k, c, nc)
				}
			}
		}
	}
}

// ------------------------------------------------------------ sigma schedule --

func TestMutationScaleFollowsTheAnnealedSigma(t *testing.T) {
	spec := config.DefaultSpec()
	sc := newToyScorer(spec)
	const budget = 1000
	if _, err := Run(AlgorithmEvolution, sc, toyOptions(spec, budget, 8)); err != nil {
		t.Fatal(err)
	}
	designs, metas, ids, _ := sc.snapshot()
	lo, hi := spec.Lower(), spec.Upper()

	byID := map[string][]float64{}
	for i, id := range ids {
		byID[id] = designs[i]
	}
	meanStep := func(gen int) (float64, int) {
		sum, count := 0.0, 0
		for i, m := range metas {
			if m.Generation != gen || m.Parent == "" {
				continue
			}
			p, ok := byID[m.Parent]
			if !ok {
				continue
			}
			acc := 0.0
			for j := range p {
				d := (designs[i][j] - p[j]) / (hi[j] - lo[j])
				acc += d * d
			}
			sum += math.Sqrt(acc)
			count++
		}
		return sum / float64(count), count
	}
	first, nFirst := meanStep(1)
	last, nLast := meanStep(metas[len(metas)-1].Generation)
	if nFirst == 0 || nLast == 0 {
		t.Fatalf("no children to measure (%d, %d)", nFirst, nLast)
	}
	t.Logf("mean normalised step: generation 1 = %.4f, last generation = %.4f", first, last)
	if !(first > 3*last) {
		t.Fatalf("mutation scale did not anneal: gen1 step %.4f, last step %.4f (sigma0=0.25 -> sigmaFloor=0.03 expected ~8x)", first, last)
	}
}

// ------------------------------------------------------------------ dispatch --

func TestRunDispatch(t *testing.T) {
	spec := config.DefaultSpec()
	if _, err := Run("bayesian", newToyScorer(spec), toyOptions(spec, 10, 1)); err == nil {
		t.Fatal("Run accepted an unknown method; it must be a hard error, not a silent default")
	} else if !strings.Contains(err.Error(), "bayesian") {
		t.Fatalf("error %q does not name the unknown method", err)
	}
	for _, method := range MethodNames() {
		res, err := Run(method, newToyScorer(spec), toyOptions(spec, 50, 2))
		if err != nil {
			t.Fatalf("Run(%q): %v", method, err)
		}
		if res.Algorithm != method {
			t.Fatalf("Run(%q).Algorithm = %q", method, res.Algorithm)
		}
		if res.NEvals != 50 {
			t.Fatalf("Run(%q).NEvals = %d, want 50", method, res.NEvals)
		}
	}
}

// -------------------------------------------------- stage-A wiring (skippable) --

// nonCanonicalProbe is a design with the coils out of z-order and two entries
// outside the box, so canonicalisation has real work to do.
func nonCanonicalProbe() []float64 {
	return []float64{0.4, 0.9, 0.2, 0.7, 0.5, -2.0, 1.0, -0.1, 3.0e6, 1.0, 5.0e5, 2.0e6}
}

// TestDesignHelperSeamsAreWiredToPhysics is the parity gate for design.go:
//
//  1. the local reference implementation equals the physics one;
//  2. the wired (shipping) Canonicalise is the physics one;
//  3. the wired SampleDesign consumes the RNG stream exactly like
//     physics.RandomDesign, i.e. the wiring is a no-op for the random path.
//
// internal/physics is an independent parallel line, so the test skips while it
// is still a set of panicking stubs; now that it is implemented the gate is
// hard.
func TestDesignHelperSeamsAreWiredToPhysics(t *testing.T) {
	spec := config.DefaultSpec()
	if !physicsAvailable(spec) {
		t.Skip("stage A (internal/physics) is not implemented yet: the local design helpers are the only implementation available; this test becomes a hard parity check once physics lands")
	}
	x := nonCanonicalProbe()
	local := localCanonicalise(x, spec)
	phys := PhysicsCanonicalise(x, spec)
	if !floatsEqual(local, phys) {
		t.Fatalf("localCanonicalise %v != PhysicsCanonicalise %v", local, phys)
	}
	if !floatsEqual(Canonicalise(x, spec), phys) {
		t.Fatalf("Canonicalise is not wired to physics: %v != %v", Canonicalise(x, spec), phys)
	}
	checkInsideBoxAndCanonical(t, spec, [][]float64{phys})

	for seed := 1; seed <= 20; seed++ {
		got := SampleDesign(rand.New(rand.NewSource(int64(seed))), spec)
		want := physics.RandomDesign(rand.New(rand.NewSource(int64(seed))), spec)
		if !floatsEqual(got, want) {
			t.Fatalf("SampleDesign is not wired to physics.RandomDesign at seed %d: %v != %v", seed, got, want)
		}
		checkInsideBoxAndCanonical(t, spec, [][]float64{got})
	}
}

// physicsAvailable reports whether the stage-A functions behind the seam are
// implemented, by probing them once and treating a panic as "not yet".
func physicsAvailable(spec config.Spec) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	rng := rand.New(rand.NewSource(3))
	x := physics.RandomDesign(rng, spec)
	return len(x) == spec.NParams()
}

// TestWarmStartDefaultIsTheGoldenBaseline checks the hard-coded fallback warm
// start against the cross-language anchor in testdata/: EvolutionWarm must be
// seeded with the human design the machine is asked to beat, not with something
// that merely looks like it.
func TestWarmStartDefaultIsTheGoldenBaseline(t *testing.T) {
	spec := config.DefaultSpec()
	warm := WarmStartDesign(spec)

	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "golden_baseline.json"))
	if err != nil {
		t.Skipf("golden baseline not readable from the test: %v", err)
	}
	var golden struct {
		Name   string    `json:"name"`
		Design []float64 `json:"design"`
	}
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatalf("golden_baseline.json: %v", err)
	}
	if !floatsEqual(warm, golden.Design) {
		t.Fatalf("WarmStartDesign returned %v, want the golden %q design %v", warm, golden.Name, golden.Design)
	}
	checkInsideBoxAndCanonical(t, spec, [][]float64{warm})
}

// ---------------------------------------------------------------------- misc --

// TestZeroValuedOptionsUseTheDocumentedDefaults feeds a literally zero-valued
// Options (not built by DefaultOptions) to the evolution strategy: the knobs
// must fall back to mu=16 / lambda=48 / sigma0=0.25 / sigmaFloor=0.03, the last
// generation must be truncated to the remaining budget, and the run must still
// be budget-exact. The spec is also zero, so the design vectors are empty and
// only the structure is being checked.
func TestZeroValuedOptionsUseTheDocumentedDefaults(t *testing.T) {
	var opt Options
	opt.Budget = 100 // 16 parents + 48 + 36 (the last generation is truncated)

	sc := &toyScorer{seq: []float64{0.0, 1.0, 0.5}, seen: map[string]bool{}}
	res, err := Run(AlgorithmEvolution, sc, opt)
	if err != nil {
		t.Fatal(err)
	}
	if res.NEvals != 100 {
		t.Fatalf("NEvals = %d, want exactly 100", res.NEvals)
	}
	if len(res.History) != 100 {
		t.Fatalf("len(History) = %d, want 100", len(res.History))
	}
	_, metas, _, _ := sc.snapshot()
	roots, children := 0, 0
	for _, m := range metas {
		if m.Generation == 0 {
			roots++
		} else {
			children++
		}
	}
	if roots != 16 {
		t.Fatalf("roots = %d, want the default mu = 16", roots)
	}
	if children != 84 {
		t.Fatalf("children = %d, want 84 (48 + the truncated 36)", children)
	}
}

func TestToyScorerSanity(t *testing.T) {
	spec := config.DefaultSpec()
	sc := newToyScorer(spec)
	// The optimum of the toy is the human baseline, so it scores 0.
	if got := sc.score(textbookMirrorDesign, runner.Meta{}); math.Abs(got) > 1e-12 {
		t.Fatalf("toy optimum scores %v, want 0", got)
	}
	// Far corners score strictly worse, and the score is symmetric in so far as
	// the bowl definition is.
	lo, hi := spec.Lower(), spec.Upper()
	if got := sc.score(lo, runner.Meta{}); got >= 0 {
		t.Fatalf("corner score %v should be negative", got)
	}
	if got := sc.score(hi, runner.Meta{}); got >= 0 {
		t.Fatalf("corner score %v should be negative", got)
	}
	// The toy's design ids ascend, which is what makes lineage checkable.
	r1 := sc.Score(lo, runner.Meta{EvalIndex: 0})
	r2 := sc.Score(hi, runner.Meta{EvalIndex: 1})
	if r1.DesignID != "D0001" || r2.DesignID != "D0002" {
		t.Fatalf("toy ids = %q, %q, want D0001, D0002", r1.DesignID, r2.DesignID)
	}
}
