// Package runner: score a design, give it an id, append it to the registry.
//
// FROZEN INTERFACE (v0.1) — owner: stage C.
//
// This is the layer that turns "an optimiser called a function" into "an
// experiment with a lineage". Every search algorithm goes through here, which is
// what makes the registry complete by construction rather than by discipline.
package runner

import (
	"github.com/logos-42/hushfusion-forge/internal/objective"
	"github.com/logos-42/hushfusion-forge/internal/registry"
)

// Meta is the provenance attached to one scored design.
type Meta struct {
	Algorithm  string
	Seed       int
	Generation int
	EvalIndex  int
	Parent     string // parent design_id (lineage edge), "" for a root
	Note       string
}

// Scorer is the seam the search layer depends on. Search algorithms take a
// Scorer, not a *Runner, so their unit tests can inject a deterministic toy
// scorer and pass independently of the physics layer.
type Scorer interface {
	Score(x []float64, meta Meta) objective.EvalResult
}

// Runner scores through an Evaluator and records into a Registry.
type Runner struct {
	Reg *registry.Registry
	Ev  *objective.Evaluator
	Tag string
}

// New builds a runner.
func New(reg *registry.Registry, ev *objective.Evaluator, tag string) *Runner {
	return &Runner{Reg: reg, Ev: ev, Tag: tag}
}

// Score evaluates one design and appends it to the registry, filling
// Meta.Algorithm/Tag/Parent into the record, and returning the EvalResult with
// DesignID and ExperimentID populated (the search layer uses DesignID to build
// the lineage tree).
//
// Evaluation may be called from several goroutines (parallel search); recording
// must stay serialised through the registry.
func (r *Runner) Score(x []float64, meta Meta) objective.EvalResult {
	return r.recordResult(r.evaluate(x), meta)
}
