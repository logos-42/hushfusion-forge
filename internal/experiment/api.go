// Package experiment: equal-budget benchmark harness + the Phase-0 pipeline.
//
// FROZEN INTERFACE (v0.1) — owner: stage E.
//
// Three things are measured, and they answer different questions:
//
//	最优性能 (best-of-budget)  how good the best design gets at a fixed cost
//	收敛速度 (evals to beat)   how much design effort the machine needs before it
//	                          is already better than a competent engineer — the
//	                          *rate* of the learning loop, not its endpoint
//	泛化 (robustness probe)   each method's best design re-scored under perturbed
//	                          requirements, against the human baseline re-solved
//	                          for the same perturbation. A design that only wins
//	                          inside the exact box it was searched in has not
//	                          generalised, and saying so is part of the result.
//
// The Report JSON schema is consumed by the Python auxiliary layer
// (python/aux/analyze.py); keys are FROZEN.
package experiment

import (
	"github.com/logos-42/hushfusion-forge/internal/baseline"
	"github.com/logos-42/hushfusion-forge/internal/config"
	"github.com/logos-42/hushfusion-forge/internal/physics"
	"github.com/logos-42/hushfusion-forge/internal/registry"
	"github.com/logos-42/hushfusion-forge/internal/search"
)

// Variant is a requirement perturbation: a name plus spec overrides.
type Variant struct {
	Name     string             `json:"name"`
	Override map[string]float64 `json:"override"`
}

// SpecVariants are the six perturbations used by the generalisation probe.
var SpecVariants = []Variant{
	{Name: "b_ref=0.8T", Override: map[string]float64{"b_ref": 0.8}},
	{Name: "b_ref=1.2T", Override: map[string]float64{"b_ref": 1.2}},
	{Name: "z_cell=0.60m", Override: map[string]float64{"z_cell": 0.60}},
	{Name: "z_cell=1.00m", Override: map[string]float64{"z_cell": 1.00}},
	{Name: "r_plasma=0.12m", Override: map[string]float64{"r_plasma": 0.12}},
	{Name: "r_plasma=0.18m", Override: map[string]float64{"r_plasma": 0.18}},
}

// ApplyVariant returns the spec with the variant's overrides applied.
// Supported keys: b_ref, z_cell, r_plasma. Unknown keys must be reported as an
// error rather than silently ignored.
func ApplyVariant(spec config.Spec, v Variant) (config.Spec, error) {
	panic("TODO(stage E): implement variant application")
}

// Meta is the run provenance written into the report.
type Meta struct {
	Tag       string   `json:"tag"`
	Timestamp string   `json:"timestamp"`
	Budget    int      `json:"budget"`
	Seeds     []int    `json:"seeds"`
	Methods   []string `json:"methods"`
	GitCommit string   `json:"git_commit"`
	Platform  string   `json:"platform"`
	GoVersion string   `json:"go_version"`
	Workers   int      `json:"workers"`
}

// Agg is per-method statistics across seeds. Never a single-seed claim.
type Agg struct {
	NSeeds              int     `json:"n_seeds"`
	Budget              int     `json:"budget"`
	BestMean            float64 `json:"best_mean"`
	BestStd             float64 `json:"best_std"`
	BestMin             float64 `json:"best_min"`
	BestMax             float64 `json:"best_max"`
	NBeatingBaseline    int     `json:"n_beating_baseline"`
	FracBeatingBaseline float64 `json:"frac_beating_baseline"`
	EvalsToBeatMean     float64 `json:"evals_to_beat_mean"`   // -1 when no seed beat it
	EvalsToBeatMedian   float64 `json:"evals_to_beat_median"` // -1 when no seed beat it
	BaselineScore       float64 `json:"baseline_score"`
}

// BaseRec is the human baseline as recorded.
type BaseRec struct {
	Name      string             `json:"name"`
	Note      string             `json:"note"`
	Score     float64            `json:"score"`
	Feasible  bool               `json:"feasible"`
	Terms     map[string]float64 `json:"terms"`
	Weighted  map[string]float64 `json:"weighted"`
	Penalties map[string]float64 `json:"penalties"`
	Metrics   physics.Metrics    `json:"metrics"`
	Design    []float64          `json:"design"`
	CostProxy float64            `json:"cost_proxy"`
	DesignID  string             `json:"design_id"`
}

// BestRec is the single best design found by the machine.
type BestRec struct {
	DesignID  string             `json:"design_id"`
	Algorithm string             `json:"algorithm"`
	Seed      int                `json:"seed"`
	Score     float64            `json:"score"`
	Terms     map[string]float64 `json:"terms"`
	Metrics   physics.Metrics    `json:"metrics"`
	Design    []float64          `json:"design"`
	Feasible  bool               `json:"feasible"`
}

// Robustness is the generalisation probe result.
type Robustness struct {
	Variants           []string                      `json:"variants"`
	BaselinePerVariant map[string]float64            `json:"baseline_score_per_variant"`
	PerDesign          map[string]map[string]float64 `json:"per_design"`
	Summary            map[string]RobustSummary      `json:"summary"`
}

// RobustSummary is the per-design roll-up of the probe.
type RobustSummary struct {
	MeanDeltaVsBaseline  float64 `json:"mean_delta_vs_baseline"`
	WorstDeltaVsBaseline float64 `json:"worst_delta_vs_baseline"`
	NVariantsWinning     int     `json:"n_variants_winning"`
	NVariants            int     `json:"n_variants"`
}

// Report is the full artifact written to runs/<tag>/results.json.
type Report struct {
	Meta            Meta                 `json:"meta"`
	Spec            map[string]any       `json:"spec"`
	Solver          string               `json:"solver"`
	CostRef         float64              `json:"cost_ref"`
	Baseline        BaseRec              `json:"baseline"`
	Runs            []search.Result      `json:"runs"`
	History         map[string][]float64 `json:"history"`
	Aggregate       map[string]Agg       `json:"aggregate"`
	Robustness      Robustness           `json:"robustness"`
	Best            *BestRec             `json:"best"`
	RegistrySummary registry.Summary     `json:"registry_summary"`
	RLEnvReference  map[string]any       `json:"rl_env_reference,omitempty"`
}

// Opts configures a benchmark run.
type Opts struct {
	Budget   int
	Seeds    []int
	Methods  []string
	Tag      string
	Workers  int
	Baseline *baseline.Baseline
	Progress func(method string, seed int, res search.Result, seconds float64)
}

// RunBenchmark records the human baseline, then runs every method at every seed
// with an identical budget, then aggregates and probes generalisation.
//
// The baseline is recorded as its own algorithm ("human_baseline") and becomes
// design D0001 — the root of the lineage tree, so "which branch improved on the
// human?" is answerable.
func RunBenchmark(reg *registry.Registry, spec config.Spec, opt Opts) (*Report, error) {
	panic("TODO(stage E): implement benchmark harness")
}

// Aggregate computes per-method statistics across seeds.
func Aggregate(runs []search.Result, baselineScore float64) map[string]Agg {
	panic("TODO(stage E): implement aggregation")
}

// RobustnessProbe re-scores designs under perturbed requirements, relative to
// the human baseline re-solved for each variant (so the zero line is always
// "a human re-designing for the new requirement").
func RobustnessProbe(spec config.Spec, designs map[string][]float64, variants []Variant) (Robustness, error) {
	panic("TODO(stage E): implement robustness probe")
}

// WriteJSON writes the report as indented JSON.
func WriteJSON(rep *Report, path string) error { panic("TODO(stage E): implement report writing") }

// LoadReport reads a report back.
func LoadReport(path string) (*Report, error) { panic("TODO(stage E): implement report loading") }
