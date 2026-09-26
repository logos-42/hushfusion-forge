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
package rlenv

import (
	"errors"

	"github.com/logos-42/hushfusion-forge/internal/config"
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
}

// ObsMetricKeys are the frozen metric slots appended to the observation vector.
var ObsMetricKeys = []string{
	"B_mid_T", "B_throat_T", "mirror_ratio", "volume_good", "ripple", "B_coil_max_T", "cost_proxy",
}

// ObsMetricRefs normalises those slots: raw/ref.
var ObsMetricRefs = []float64{1.0, 1.0, 1.0, 1.0, 0.1, 12.0, 1.0}

// NewEnv builds an environment. Defaults: MaxSteps 20, DeltaScale 0.15.
func NewEnv(sc runner.Scorer, spec config.Spec, maxSteps int, deltaScale float64) *Env {
	panic("TODO(stage F): implement environment constructor")
}

// ActionDim is the design-vector length.
func (e *Env) ActionDim() int { panic("TODO(stage F): implement action dimension") }

// ObservationDim is ActionDim + len(ObsMetricKeys).
func (e *Env) ObservationDim() int { panic("TODO(stage F): implement observation dimension") }

// Reset starts an episode from x0 (or a uniform random design when x0 is nil),
// evaluates and records it, and returns the observation.
func (e *Env) Reset(x0 []float64) []float64 { panic("TODO(stage F): implement reset") }

// Step applies an action, evaluates and records the new design (with the
// previous design as parent, so the episode forms a lineage chain).
func (e *Env) Step(action []float64) (obs []float64, reward float64, terminated, truncated bool, info Info) {
	panic("TODO(stage F): implement step")
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
	panic("TODO(stage F): implement random policy rollout")
}

// LoadPolicy always fails with ErrNoLearnedPolicy.
func LoadPolicy(path string) error { return ErrNoLearnedPolicy }
