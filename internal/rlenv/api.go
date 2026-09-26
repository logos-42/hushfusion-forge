// Package rlenv: the design environment — the Phase-1 landing pad for OaK / RL.
//
// FROZEN INTERFACE (v0.1) — owner: stage F.
//
// What is here: a complete, working environment. What is deliberately NOT here:
// a learned policy. That asymmetry is the point. Phase 0 asks "can the machine
// find a better design than a human?" and a search algorithm answers it. Phase 1
// asks "can the system learn to propose designs, and remember what it learned?",
// and only then does a policy belong here. Until then LoadPolicy returns
// ErrNoLearnedPolicy — a loud failure instead of a fabricated number.
//
// # Conventions implemented below
//
//   - The environment's state is the design vector in SI units, kept inside the
//     spec box at all times (actions are clipped, then the result is clipped).
//
//   - An observation is [normalised design | normalised metrics]:
//
//     obs[i]          = 2*(x[i]-lower[i])/(upper[i]-lower[i]) - 1   in [-1,1]
//     obs[D+i]        = metric(ObsMetricKeys[i]) / ObsMetricRefs[i]
//
//     Normalising the design is not cosmetic: r is O(0.1) and I is O(1e6), so a
//     policy reading raw SI units would see one dimension a million times larger
//     than another.
//
//   - An action is a delta in the same normalised units: the clipped action
//     a in [-1,1]^D is turned into dx[i] = a[i] * DeltaScale * (upper[i]-lower[i]).
//
//   - The state after a step is the design the scorer actually evaluated
//     (EvalResult.Design, i.e. the canonical clipped/z-sorted vector), not the
//     vector we hoped it would evaluate.
//
// # Loud-failure policy (the same rule as LoadPolicy)
//
// Where this environment cannot produce an honest answer it panics with a
// precise message instead of returning a made-up number: mismatched vector
// lengths, a nil scorer, and non-finite scores or metrics all fail loudly. Those
// are programming errors or upstream numerical bugs; silently substituting 0.0
// would convert them into invisible training noise.
package rlenv

import (
	"errors"
	"fmt"
	"math"
	"math/rand"

	"github.com/logos-42/hushfusion-forge/internal/config"
	"github.com/logos-42/hushfusion-forge/internal/objective"
	"github.com/logos-42/hushfusion-forge/internal/physics"
	"github.com/logos-42/hushfusion-forge/internal/runner"
)

// ErrNoLearnedPolicy is returned by LoadPolicy: Phase 1 is not implemented.
var ErrNoLearnedPolicy = errors.New(
	"Phase 1 not implemented: this repository contains a complete RL environment and a random-policy " +
		"reference, but no learned policy. A design-proposing policy is Phase-1 work (see PLAN.md)")

// Info is the per-step detail.
type Info struct {
	Score      float64 `json:"score"`
	DeltaScore float64 `json:"delta_score"`
	Feasible   bool    `json:"feasible"`
	DesignID   string  `json:"design_id"`
	Step       int     `json:"step"`
}

// Env is a gym-like design environment (no gym dependency).
//
//	obs = Reset(x0)
//	obs, reward, terminated, truncated, info = Step(action)
//
// action: delta on the design vector, normalised to [-1,1]^D and scaled by
// DeltaScale * (upper-lower) per parameter.
//
// reward: score(after) - score(before). A potential-based difference, so the
// episode return telescopes exactly to score(final) - score(initial) and no
// shaping constant has to be invented.
type Env struct {
	Sc         runner.Scorer
	Spec       config.Spec
	MaxSteps   int
	DeltaScale float64

	// --- episode state (unexported: build the environment with NewEnv) ---
	lower, upper []float64
	x            []float64       // current design, SI units, always inside the box
	metrics      physics.Metrics // metrics of the current design
	lastScore    float64         // score of the current design
	initialScore float64         // score at Reset, the episode's return reference
	bestScore    float64
	totalReward  float64
	step         int    // designs visited since Reset (0 == the reset design)
	nEvals       int    // evaluations this Env has spent, monotone across episodes
	lastDesignID string // design_id of the current design == parent of the next step
	curFeasible  bool   // feasibility of the current design
	started      bool   // Set false means Reset has not run yet
	done         bool
}

// Defaults used when the corresponding field is left zero.
const (
	defaultMaxSteps   = 20
	defaultDeltaScale = 0.15
)

// algorithm tags written into the registry through runner.Meta.
const (
	algorithmEnv          = "rlenv"
	algorithmRandomPolicy = "random_policy"
)

// ObsMetricKeys are the frozen metric slots appended to the observation vector.
var ObsMetricKeys = []string{
	"B_mid_T", "B_throat_T", "mirror_ratio", "volume_good", "ripple", "B_coil_max_T", "cost_proxy",
}

// ObsMetricRefs normalises those slots: raw/ref.
var ObsMetricRefs = []float64{1.0, 1.0, 1.0, 1.0, 0.1, 12.0, 1.0}

// NewEnv builds an environment. Defaults: MaxSteps 20, DeltaScale 0.15.
func NewEnv(sc runner.Scorer, spec config.Spec, maxSteps int, deltaScale float64) *Env {
	if maxSteps <= 0 {
		maxSteps = defaultMaxSteps
	}
	if deltaScale <= 0 {
		deltaScale = defaultDeltaScale
	}
	e := &Env{Sc: sc, Spec: spec, MaxSteps: maxSteps, DeltaScale: deltaScale}
	e.ensure()
	return e
}

// ensure fills in defaults for a partially built Env and validates the frozen
// observation layout. It makes a keyed struct literal (Env{Sc: sc, Spec: spec})
// work as well as NewEnv.
func (e *Env) ensure() {
	if e.Sc == nil {
		panic("rlenv: Env.Sc is nil — the environment needs a runner.Scorer")
	}
	if len(ObsMetricKeys) != len(ObsMetricRefs) {
		panic(fmt.Sprintf("rlenv: frozen observation layout is inconsistent: %d metric keys vs %d refs",
			len(ObsMetricKeys), len(ObsMetricRefs)))
	}
	if e.MaxSteps <= 0 {
		e.MaxSteps = defaultMaxSteps
	}
	if e.DeltaScale <= 0 {
		e.DeltaScale = defaultDeltaScale
	}
	if len(e.lower) != e.Spec.NParams() {
		e.lower = e.Spec.Lower()
		e.upper = e.Spec.Upper()
		if len(e.lower) != e.Spec.NParams() || len(e.upper) != e.Spec.NParams() {
			panic(fmt.Sprintf("rlenv: spec bounds have length %d/%d, want NParams=%d",
				len(e.lower), len(e.upper), e.Spec.NParams()))
		}
	}
}

// ActionDim is the design-vector length.
func (e *Env) ActionDim() int { e.ensure(); return e.Spec.NParams() }

// ObservationDim is ActionDim + len(ObsMetricKeys).
func (e *Env) ObservationDim() int { e.ensure(); return e.Spec.NParams() + len(ObsMetricKeys) }

// Reset starts an episode from x0 (or a uniform random design when x0 is nil),
// evaluates and records it, and returns the observation.
func (e *Env) Reset(x0 []float64) []float64 {
	e.ensure()
	if x0 == nil {
		// Uniform over the box. The global rand source (thread-safe) only seeds a
		// per-call generator, so two concurrent resets cannot share a stream.
		rng := rand.New(rand.NewSource(rand.Int63()))
		x0 = physics.RandomDesign(rng, e.Spec)
	}
	if len(x0) != e.Spec.NParams() {
		panic(fmt.Sprintf("rlenv: Reset got a %d-element design, want NParams=%d",
			len(x0), e.Spec.NParams()))
	}

	e.x = e.clipDesign(x0)
	e.step = 0
	e.done = false
	e.totalReward = 0
	e.started = true
	e.bestScore = math.Inf(-1)

	res := e.score(0, "")
	e.absorb(res)

	e.initialScore = e.lastScore
	e.bestScore = e.lastScore
	return e.observation()
}

// Step applies an action, evaluates and records the new design (with the
// previous design as parent, so the episode forms a lineage chain).
func (e *Env) Step(action []float64) (obs []float64, reward float64, terminated, truncated bool, info Info) {
	e.ensure()
	if !e.started {
		panic("rlenv: Step called before Reset")
	}
	if len(action) != e.Spec.NParams() {
		panic(fmt.Sprintf("rlenv: Step got a %d-element action, want ActionDim=%d",
			len(action), e.Spec.NParams()))
	}
	if e.done {
		// A finished episode is a no-op, not a fabricated reward: report the
		// terminal state again and charge nothing for it.
		return e.observation(), 0, true, true, Info{
			Score: e.lastScore, DeltaScore: 0, Feasible: e.curFeasible,
			DesignID: e.lastDesignID, Step: e.step,
		}
	}

	// Move the state: an action is a delta in normalised units, scaled per
	// parameter and clipped both in action space and in the design box.
	for i := range e.x {
		a := clip(action[i], -1, 1)
		e.x[i] = clip(e.x[i]+a*e.DeltaScale*(e.upper[i]-e.lower[i]), e.lower[i], e.upper[i])
	}

	parent := e.lastDesignID
	before := e.lastScore
	res := e.score(e.step+1, parent)
	e.absorb(res)

	e.step++
	e.totalReward += e.lastScore - before

	terminated = false // the design space has no absorbing state
	truncated = e.step >= e.MaxSteps
	e.done = truncated

	info = Info{
		Score:      e.lastScore,
		DeltaScore: e.lastScore - before,
		Feasible:   e.curFeasible,
		DesignID:   e.lastDesignID,
		Step:       e.step,
	}
	return e.observation(), info.DeltaScore, terminated, truncated, info
}

// score evaluates the current design with the right provenance. generation is
// the position of this design in the episode (0 = the reset design); parent is
// the design_id of the previous design ("" for a root).
func (e *Env) score(generation int, parent string) objective.EvalResult {
	note := fmt.Sprintf("rlenv episode design %d", generation)
	if generation == 0 {
		note = "rlenv episode start"
	}
	res := e.Sc.Score(copyOf(e.x), runner.Meta{
		Algorithm:  algorithmEnv,
		Generation: generation,
		EvalIndex:  e.nEvals,
		Parent:     parent,
		Note:       note,
	})
	e.nEvals++
	if !isFinite(res.Score) {
		panic(fmt.Sprintf("rlenv: scorer returned a non-finite score (%v) — refusing to build an "+
			"episode on a number that is not a number", res.Score))
	}
	return res
}

// absorb stores what the scorer actually evaluated as the new state.
func (e *Env) absorb(res objective.EvalResult) {
	if len(res.Design) == len(e.x) {
		e.x = copyOf(res.Design)
	}
	// Otherwise the scorer did not echo a canonical design (a toy scorer, or a
	// layer that returns nil) and the vector we sent stands as the state;
	// inventing anything else here would make the state unfalsifiable.
	e.lastScore = res.Score
	if res.Score > e.bestScore {
		e.bestScore = res.Score
	}
	e.metrics = res.Metrics
	e.curFeasible = res.Feasible
	e.lastDesignID = res.DesignID
}

// observation renders the current state for a policy.
func (e *Env) observation() []float64 {
	out := make([]float64, 0, e.ObservationDim())
	for i := range e.x {
		span := e.upper[i] - e.lower[i]
		if span <= 0 {
			panic(fmt.Sprintf("rlenv: degenerate bound span (upper-lower=%v) at parameter %d", span, i))
		}
		out = append(out, clip(2*(e.x[i]-e.lower[i])/span-1, -1, 1))
	}
	for i, key := range ObsMetricKeys {
		ref := ObsMetricRefs[i]
		if ref == 0 {
			ref = 1
		}
		v := ObsMetricValue(e.metrics, key)
		if !isFinite(v) {
			panic(fmt.Sprintf("rlenv: metric %q is not finite (%v) — refusing to hand a policy a "+
				"NaN observation", key, v))
		}
		out = append(out, v/ref)
	}
	return out
}

// clipDesign copies x and clamps it into the spec box.
func (e *Env) clipDesign(x []float64) []float64 {
	out := make([]float64, len(x))
	for i := range x {
		out[i] = clip(x[i], e.lower[i], e.upper[i])
	}
	return out
}

// ObsMetricValue maps a physics.Metrics field to its frozen observation key.
//
// An unknown key panics: a key added to ObsMetricKeys without a mapping here
// must not silently become 0.0 in an observation vector.
func ObsMetricValue(m physics.Metrics, key string) float64 {
	switch key {
	case "B_mid_T":
		return m.BMidT
	case "B_throat_T":
		return m.BThroatT
	case "mirror_ratio":
		return m.MirrorRatio
	case "volume_good":
		return m.VolumeGood
	case "ripple":
		return m.Ripple
	case "B_coil_max_T":
		return m.BCoilMaxT
	case "cost_proxy":
		return m.CostProxy
	default:
		panic(fmt.Sprintf("rlenv: observation metric key %q has no mapping in ObsMetricValue", key))
	}
}

// Rollout is a policy evaluation summary.
type Rollout struct {
	Policy      string  `json:"policy"`
	NSteps      int     `json:"n_steps"`
	TotalReward float64 `json:"total_reward"`
	BestScore   float64 `json:"best_score"`
	FinalScore  float64 `json:"final_score"`
}

// RandomPolicyRollout is the honest reference: a policy that runs and is
// deliberately dumb (uniform random actions). Phase 1 compares a learner against
// this at equal budget.
func RandomPolicyRollout(e *Env, nSteps int, seed int64) Rollout {
	if e == nil {
		panic("rlenv: RandomPolicyRollout needs an environment")
	}
	e.ensure()
	if nSteps <= 0 {
		panic(fmt.Sprintf("rlenv: RandomPolicyRollout needs a positive step count, got %d", nSteps))
	}

	// Evaluate on a private copy so the caller's environment is left untouched,
	// and stamp every record with the rollout's seed and policy name.
	rng := rand.New(rand.NewSource(seed))
	env := NewEnv(policyScorer{sc: e.Sc, seed: int(seed), policy: algorithmRandomPolicy},
		e.Spec, e.MaxSteps, e.DeltaScale)

	env.Reset(physics.RandomDesign(rng, e.Spec))

	out := Rollout{Policy: algorithmRandomPolicy, BestScore: env.bestScore}
	action := make([]float64, env.ActionDim())
	for i := 0; i < nSteps; i++ {
		for j := range action {
			action[j] = 2*rng.Float64() - 1
		}
		_, _, terminated, truncated, _ := env.Step(action)
		out.NSteps++
		if terminated || truncated {
			break
		}
	}
	out.TotalReward = env.totalReward
	out.FinalScore = env.lastScore
	if env.bestScore > out.BestScore {
		out.BestScore = env.bestScore
	}
	return out
}

// policyScorer tags every record it passes through with the policy that
// produced it, so a rollout never lands in the registry as "rlenv" — the
// registry has to say which policy proposed the design.
type policyScorer struct {
	sc     runner.Scorer
	seed   int
	policy string
}

func (p policyScorer) Score(x []float64, meta runner.Meta) objective.EvalResult {
	meta.Algorithm = p.policy
	meta.Seed = p.seed
	return p.sc.Score(x, meta)
}

// LoadPolicy always fails with ErrNoLearnedPolicy.
func LoadPolicy(path string) error { return ErrNoLearnedPolicy }

// --- small helpers ---

func clip(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func copyOf(x []float64) []float64 {
	out := make([]float64, len(x))
	copy(out, x)
	return out
}

func isFinite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
