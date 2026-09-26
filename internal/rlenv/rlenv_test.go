package rlenv

import (
	"errors"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/logos-42/hushfusion-forge/internal/config"
	"github.com/logos-42/hushfusion-forge/internal/objective"
	"github.com/logos-42/hushfusion-forge/internal/physics"
	"github.com/logos-42/hushfusion-forge/internal/runner"
)

// ---------------------------------------------------------------------------
// A toy scorer: deterministic, self-contained, independent of every other
// stage. Only config (the frozen spec/bounds) and the frozen EvalResult /
// Metrics types are touched, so these tests stay green while the physics,
// objective and registry packages are still skeletons.
// ---------------------------------------------------------------------------

type toyScorer struct {
	spec config.Spec

	// observation bookkeeping, used to verify provenance and lineage
	calls     int
	designs   []string // design_id handed out, in order
	parents   []string // meta.Parent received, in order
	algos     []string
	seeds     []int
	evalIdx   []int
	gens      []int
	lastEval  []float64 // the raw vector as evaluated (after any canonicalisation)
	gotDesign bool
	// nonFinite makes the scorer return math.NaN to exercise the loud-failure path
	nonFinite bool
}

// toyScore is a pure function of the design vector — no counters, no state — so
// that a reward sequence can be telescoped independently of the environment's
// own bookkeeping.
func toyScore(spec config.Spec, x []float64) float64 {
	lo, hi := spec.Lower(), spec.Upper()
	if len(x) != len(lo) {
		panic(fmt.Sprintf("toyScore: got %d params, want %d", len(x), len(lo)))
	}
	s := 0.0
	for i, v := range x {
		span := hi[i] - lo[i]
		target := lo[i] + (0.30+0.08*float64(i%5))*span
		d := (v - target) / span
		s -= d * d
	}
	// A smooth coupling so the surface is not separable (a policy cannot win by
	// tuning one coordinate at a time).
	s -= 0.25 * math.Sin(4*math.Pi*(x[0]-lo[0])/hi[0])
	return s
}

func toyMetrics(spec config.Spec, x []float64) physics.Metrics {
	n := spec.NCoils
	sumR, sumI := 0.0, 0.0
	for i := 0; i < n && i < len(x); i++ {
		sumR += x[i]
		sumI += x[2*n+i]
	}
	cost := 0.0
	for i := 0; i < n && i < len(x); i++ {
		cost += x[2*n+i] * x[2*n+i] * x[i]
	}
	bMid := 1.0 + 0.05*math.Sin(sumR)
	ratio := 2.0 + 0.5*math.Cos(sumI/1e6)
	return physics.Metrics{
		BMidT:       bMid,
		BThroatT:    bMid * ratio,
		ZThroatM:    -1.0 + 0.01*math.Sin(sumR),
		MirrorRatio: ratio,
		VolumeGood:  0.5 + 0.1*math.Sin(sumI/1e5),
		Ripple:      0.02 * math.Abs(math.Sin(sumR)),
		BCoilMaxT:   3.0 + 0.1*math.Cos(sumI/1e6),
		MinCoilGapM: 0.5,
		CostProxy:   cost,
		NCoils:      n,
		MU0:         config.MU0,
	}
}

func (t *toyScorer) Score(x []float64, meta runner.Meta) objective.EvalResult {
	t.calls++
	t.designs = append(t.designs, fmt.Sprintf("D%04d", t.calls))
	t.parents = append(t.parents, meta.Parent)
	t.algos = append(t.algos, meta.Algorithm)
	t.seeds = append(t.seeds, meta.Seed)
	t.evalIdx = append(t.evalIdx, meta.EvalIndex)
	t.gens = append(t.gens, meta.Generation)
	t.lastEval = append([]float64(nil), x...)
	t.gotDesign = true

	design := append([]float64(nil), x...)
	score := toyScore(t.spec, design)
	if t.nonFinite {
		score = math.NaN()
	}
	return objective.EvalResult{
		Score:  score,
		Design: design,
		Terms:  map[string]float64{objective.TermField: 0, objective.TermCost: 1},
		Weighted: map[string]float64{
			objective.TermField: 0, objective.TermCost: -1,
		},
		Penalties:    map[string]float64{},
		Feasible:     true,
		Metrics:      toyMetrics(t.spec, design),
		DesignID:     fmt.Sprintf("D%04d", t.calls),
		ExperimentID: t.calls,
	}
}

func newToy(spec config.Spec) *toyScorer { return &toyScorer{spec: spec} }

// midDesign returns a deterministic in-box starting design.
func midDesign(spec config.Spec) []float64 {
	lo, hi := spec.Lower(), spec.Upper()
	x := make([]float64, len(lo))
	for i := range x {
		x[i] = lo[i] + 0.5*(hi[i]-lo[i])
	}
	return x
}

// ---------------------------------------------------------------------------
// G10 — the anti-fabrication gate.
// ---------------------------------------------------------------------------

// TestNoLearnedPolicy is acceptance gate G10: the repository has no learned
// policy, so LoadPolicy must fail for every input instead of returning a
// fabricated number. A file that exists is still not a policy.
func TestNoLearnedPolicy(t *testing.T) {
	paths := []string{
		"", "policy.json", "checkpoints/ppo.bin", "/nonexistent/path/policy.joblib",
	}
	for _, p := range paths {
		err := LoadPolicy(p)
		if err == nil {
			t.Fatalf("LoadPolicy(%q) returned nil: a learned policy must not be invented here", p)
		}
		if !errors.Is(err, ErrNoLearnedPolicy) {
			t.Fatalf("LoadPolicy(%q) error = %v, want ErrNoLearnedPolicy", p, err)
		}
	}

	// Even a real, readable file must not be accepted: if LoadPolicy ever starts
	// reading a file it also has to start proving what is in it.
	dir := t.TempDir()
	f := filepath.Join(dir, "plausible_policy.json")
	if werr := os.WriteFile(f, []byte(`{"weights":[1,2,3],"score":-1.234}`), 0o644); werr != nil {
		t.Fatalf("write temp policy: %v", werr)
	}
	if err := LoadPolicy(f); !errors.Is(err, ErrNoLearnedPolicy) {
		t.Fatalf("LoadPolicy(%q) on an existing file = %v, want ErrNoLearnedPolicy", f, err)
	}

	// The error must be self-explaining, not a bare "not implemented".
	if msg := ErrNoLearnedPolicy.Error(); !strings.Contains(msg, "Phase 1") {
		t.Errorf("ErrNoLearnedPolicy message should name Phase 1, got %q", msg)
	}
}

// ---------------------------------------------------------------------------
// reward = score(after) - score(before), checked against an independent
// evaluation of the first and final designs.
// ---------------------------------------------------------------------------

// TestRewardTelescopes asserts the episode return equals
// score(final) - score(initial) to 1e-9, with score() recomputed outside the
// environment from the same deterministic scorer.
func TestRewardTelescopes(t *testing.T) {
	spec := config.DefaultSpec()
	sc := newToy(spec)
	const steps = 20

	env := NewEnv(sc, spec, steps, 0.15)
	x0 := midDesign(spec)
	env.Reset(x0)

	rng := rand.New(rand.NewSource(20260926))
	action := make([]float64, env.ActionDim())
	sum := 0.0
	for i := 0; i < steps; i++ {
		for j := range action {
			action[j] = 2*rng.Float64() - 1
		}
		_, reward, terminated, truncated, info := env.Step(action)
		if terminated {
			t.Fatalf("step %d reported terminated: the design space has no absorbing state", i)
		}
		if truncated != (i == steps-1) {
			t.Fatalf("step %d reported truncated=%v; the step limit %d must be flagged on its last step",
				i, truncated, steps)
		}
		if math.Abs(reward-info.DeltaScore) > 1e-12 {
			t.Fatalf("step %d: returned reward %v != info.DeltaScore %v", i, reward, info.DeltaScore)
		}
		sum += reward
	}

	finalDesign := sc.lastEval
	want := toyScore(spec, finalDesign) - toyScore(spec, x0)

	if math.Abs(sum-want) > 1e-9 {
		t.Fatalf("episode return does not telescope: sum(reward)=%.17g, score(final)-score(initial)=%.17g, diff=%.3g",
			sum, want, math.Abs(sum-want))
	}
	if math.Abs(env.totalReward-sum) > 1e-12 {
		t.Fatalf("env.totalReward=%v != sum of step rewards %v", env.totalReward, sum)
	}
	if math.Abs(sum-(env.lastScore-env.initialScore)) > 1e-9 {
		t.Fatalf("env bookkeeping does not telescope: %v vs %v", sum, env.lastScore-env.initialScore)
	}
	if env.step != steps {
		t.Fatalf("env.step = %d, want %d", env.step, steps)
	}
	if env.bestScore < math.Max(env.initialScore, env.lastScore)-1e-12 {
		t.Fatalf("bestScore %v is below the trajectories it summarises (initial %v, final %v)",
			env.bestScore, env.initialScore, env.lastScore)
	}
}

// ---------------------------------------------------------------------------
// Shapes, bounds, and clipping.
// ---------------------------------------------------------------------------

func TestObsDimAndBounds(t *testing.T) {
	spec := config.DefaultSpec()
	sc := newToy(spec)
	env := NewEnv(sc, spec, 8, 0.15)

	dim := spec.NParams()
	if got := env.ActionDim(); got != dim {
		t.Fatalf("ActionDim = %d, want %d", got, dim)
	}
	if got, want := env.ObservationDim(), dim+len(ObsMetricKeys); got != want {
		t.Fatalf("ObservationDim = %d, want %d", got, want)
	}
	if len(ObsMetricKeys) != len(ObsMetricRefs) {
		t.Fatalf("frozen observation layout broken: %d keys vs %d refs", len(ObsMetricKeys), len(ObsMetricRefs))
	}

	lo, hi := spec.Lower(), spec.Upper()
	checkObs := func(tag string, obs []float64) {
		t.Helper()
		if len(obs) != env.ObservationDim() {
			t.Fatalf("%s: observation length %d, want %d", tag, len(obs), env.ObservationDim())
		}
		for i := 0; i < dim; i++ {
			if obs[i] < -1 || obs[i] > 1 {
				t.Fatalf("%s: normalised design slot %d = %v outside [-1,1]", tag, i, obs[i])
			}
		}
		for i := dim; i < len(obs); i++ {
			if math.IsNaN(obs[i]) || math.IsInf(obs[i], 0) {
				t.Fatalf("%s: metric slot %q = %v is not finite", tag, ObsMetricKeys[i-dim], obs[i])
			}
		}
		// the evaluated design must always sit inside the spec box
		for i, v := range sc.lastEval {
			if v < lo[i]-1e-9 || v > hi[i]+1e-9 {
				t.Fatalf("%s: evaluated parameter %d = %v outside [%v, %v]", tag, i, v, lo[i], hi[i])
			}
		}
	}

	// A wildly out-of-box start design must be clipped, not accepted.
	wild := make([]float64, dim)
	for i := range wild {
		if i%2 == 0 {
			wild[i] = lo[i] - 1e9
		} else {
			wild[i] = hi[i] + 1e9
		}
	}
	obs := env.Reset(wild)
	checkObs("reset(out-of-box)", obs)
	for i := range wild {
		if sc.lastEval[i] != lo[i] && sc.lastEval[i] != hi[i] {
			t.Fatalf("parameter %d = %v was not clipped to a bound [%v, %v]", i, sc.lastEval[i], lo[i], hi[i])
		}
	}

	// Extreme actions must never leave the box either.
	rng := rand.New(rand.NewSource(7))
	action := make([]float64, dim)
	for step := 0; step < 40; step++ {
		for j := range action {
			if rng.Intn(2) == 0 {
				action[j] = -1e6
			} else {
				action[j] = 1e6
			}
		}
		obs, _, _, _, _ = env.Step(action)
		if len(obs) == 0 {
			t.Fatal("Step returned an empty observation")
		}
		checkObs(fmt.Sprintf("step(%d)", step), obs)
	}

	// The normalised design slots must map back to the state the scorer saw:
	// obs[i] = 2*(x-lo)/(hi-lo) - 1.
	for i := 0; i < dim; i++ {
		want := 2*(sc.lastEval[i]-lo[i])/(hi[i]-lo[i]) - 1
		if math.Abs(obs[i]-want) > 1e-12 {
			t.Fatalf("obs slot %d = %v, want %v (normalised design mapping)", i, obs[i], want)
		}
	}
	// Spot-check two metric slots against their reference scaling.
	m := toyMetrics(spec, sc.lastEval)
	wantMid := m.BMidT / ObsMetricRefs[0]
	if math.Abs(obs[dim]-wantMid) > 1e-12 {
		t.Fatalf("B_mid slot = %v, want %v", obs[dim], wantMid)
	}
	wantRipple := m.Ripple / ObsMetricRefs[4]
	if math.Abs(obs[dim+4]-wantRipple) > 1e-12 {
		t.Fatalf("ripple slot = %v, want %v", obs[dim+4], wantRipple)
	}
}

// TestActionShapeAndTerminationAreLoud documents the failure policy: a
// malformed action, a Step before Reset, and a non-finite score all fail
// loudly instead of producing a plausible-looking number.
func TestActionShapeAndTerminationAreLoud(t *testing.T) {
	spec := config.DefaultSpec()

	t.Run("wrong action length panics", func(t *testing.T) {
		env := NewEnv(newToy(spec), spec, 4, 0.15)
		env.Reset(midDesign(spec))
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("Step with a wrong-length action did not fail loudly")
			}
		}()
		env.Step(make([]float64, env.ActionDim()-1))
	})

	t.Run("Step before Reset panics", func(t *testing.T) {
		env := NewEnv(newToy(spec), spec, 4, 0.15)
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("Step before Reset did not fail loudly")
			}
		}()
		env.Step(make([]float64, env.ActionDim()))
	})

	t.Run("nil scorer panics", func(t *testing.T) {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("a nil scorer did not fail loudly")
			}
		}()
		NewEnv(nil, spec, 4, 0.15)
	})

	t.Run("non-finite score panics", func(t *testing.T) {
		sc := newToy(spec)
		sc.nonFinite = true
		env := NewEnv(sc, spec, 4, 0.15)
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("a NaN score was accepted instead of failing loudly")
			}
		}()
		env.Reset(midDesign(spec))
	})

	t.Run("finished episode is a no-op", func(t *testing.T) {
		env := NewEnv(newToy(spec), spec, 2, 0.15)
		env.Reset(midDesign(spec))
		action := make([]float64, env.ActionDim())
		if _, _, term, trunc, _ := env.Step(action); term || trunc {
			t.Fatal("step 1 of a 2-step episode ended it early")
		}
		_, _, terminated, truncated, _ := env.Step(action)
		if !truncated {
			t.Fatalf("step 2 of MaxSteps=2 returned truncated=%v, want true", truncated)
		}
		if terminated {
			t.Fatal("the design space has no absorbing state, terminated should stay false")
		}

		before := env.totalReward
		_, reward, terminated, truncated, info := env.Step(action)
		if !terminated || !truncated {
			t.Fatalf("stepping past MaxSteps returned terminated=%v truncated=%v, want both true", terminated, truncated)
		}
		if reward != 0 || info.DeltaScore != 0 {
			t.Fatalf("a no-op step reported reward=%v (info %v): a finished episode must not invent one", reward, info)
		}
		if env.totalReward != before {
			t.Fatalf("total reward changed on a no-op step: %v -> %v", before, env.totalReward)
		}
	})
}

// ---------------------------------------------------------------------------
// Lineage.
// ---------------------------------------------------------------------------

// TestStepFormsLineage asserts that one episode is a connected lineage chain:
// every step's design names the previous design as its parent, and only the
// reset design is a root.
func TestStepFormsLineage(t *testing.T) {
	spec := config.DefaultSpec()
	sc := newToy(spec)
	const steps = 6

	env := NewEnv(sc, spec, steps, 0.25)
	env.Reset(midDesign(spec))

	rng := rand.New(rand.NewSource(4242))
	action := make([]float64, env.ActionDim())
	seen := []string{}
	for i := 0; i < steps; i++ {
		for j := range action {
			action[j] = 2*rng.Float64() - 1
		}
		_, _, terminated, truncated, info := env.Step(action)
		if terminated {
			t.Fatalf("step %d reported terminated: the design space has no absorbing state", i)
		}
		if truncated != (i == steps-1) {
			t.Fatalf("step %d reported truncated=%v; the step limit %d must be flagged on its last step",
				i, truncated, steps)
		}
		if info.Step != i+1 {
			t.Fatalf("info.Step = %d, want %d", info.Step, i+1)
		}
		seen = append(seen, info.DesignID)
	}

	if len(sc.parents) != steps+1 {
		t.Fatalf("scorer saw %d evaluations, want %d (reset + %d steps)", len(sc.parents), steps+1, steps)
	}
	if sc.parents[0] != "" {
		t.Fatalf("reset design parent = %q, want an empty root", sc.parents[0])
	}
	for i := 1; i < len(sc.parents); i++ {
		if sc.parents[i] != sc.designs[i-1] {
			t.Fatalf("step %d parent = %q, want the previous design %q (the episode must be a chain)",
				i, sc.parents[i], sc.designs[i-1])
		}
	}
	for i, id := range seen {
		if id == "" {
			t.Fatalf("step %d reported an empty design_id", i)
		}
	}
	// EvalIndex counts evaluations monotonically; Generation counts position in
	// the episode.
	for i, idx := range sc.evalIdx {
		if idx != i {
			t.Fatalf("evaluation %d carried EvalIndex %d, want %d", i, idx, i)
		}
	}
	for i, g := range sc.gens {
		if g != i {
			t.Fatalf("evaluation %d carried Generation %d, want %d", i, g, i)
		}
	}
	for i, a := range sc.algos {
		if a != algorithmEnv {
			t.Fatalf("evaluation %d carried Algorithm %q, want %q", i, a, algorithmEnv)
		}
	}
	// Two episodes in a row: the second reset is a new root, and EvalIndex keeps
	// counting (the registry's evaluation counter does not restart).
	firstEpisodeEvals := sc.calls
	env.Reset(midDesign(spec))
	if got := sc.parents[len(sc.parents)-1]; got != "" {
		t.Fatalf("second episode's reset parent = %q, want an empty root", got)
	}
	if got := sc.evalIdx[len(sc.evalIdx)-1]; got != firstEpisodeEvals {
		t.Fatalf("second episode's reset EvalIndex = %d, want %d (monotone across episodes)", got, firstEpisodeEvals)
	}
	if got := sc.gens[len(sc.gens)-1]; got != 0 {
		t.Fatalf("second episode's reset Generation = %d, want 0", got)
	}
}

// TestEnsureFillsDefaults checks that a keyed struct literal without NewEnv is
// still usable (and that defaults are the documented ones).
func TestEnsureFillsDefaults(t *testing.T) {
	spec := config.DefaultSpec()
	env := &Env{Sc: newToy(spec), Spec: spec}
	if env.ActionDim() != spec.NParams() {
		t.Fatalf("ActionDim = %d, want %d", env.ActionDim(), spec.NParams())
	}
	if env.MaxSteps != defaultMaxSteps || env.DeltaScale != defaultDeltaScale {
		t.Fatalf("defaults = (%d, %v), want (%d, %v)", env.MaxSteps, env.DeltaScale, defaultMaxSteps, defaultDeltaScale)
	}
	obs := env.Reset(midDesign(spec))
	if len(obs) != env.ObservationDim() {
		t.Fatalf("observation length %d, want %d", len(obs), env.ObservationDim())
	}

	e2 := NewEnv(newToy(spec), spec, 0, 0)
	if e2.MaxSteps != defaultMaxSteps || e2.DeltaScale != defaultDeltaScale {
		t.Fatalf("NewEnv(0,0) defaults = (%d, %v), want (%d, %v)", e2.MaxSteps, e2.DeltaScale, defaultMaxSteps, defaultDeltaScale)
	}
}
