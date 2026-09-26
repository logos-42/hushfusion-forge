// Package registry: the company's memory, not a log file.
//
// FROZEN INTERFACE (v0.1) — owner: stage C.
//
// Every design ever scored is one JSONL line with its parameters, raw physical
// terms, constraint residuals and lineage (parent_design). Two consequences:
//
//   - the search is never the source of truth, the registry is, so a run can be
//     re-scored, re-weighted and re-mined afterwards;
//   - design history is a tree, not a list, so "which branch produced the most
//     improvement?" is answerable — the primitive of a progress moat.
//
// The JSON schema is shared with the Python reference (see
// python/forge/registry.py). Field names are FROZEN; a mismatch is caught by the
// schema-parity gate.
package registry

import (
	"sync"

	"github.com/logos-42/hushfusion-forge/internal/physics"
)

// Params is the design split into named per-coil arrays.
type Params struct {
	RadiusM  []float64 `json:"radius_m"`
	ZM       []float64 `json:"z_m"`
	CurrentA []float64 `json:"current_A"`
}

// Record is one experiment.
type Record struct {
	ExperimentID int                `json:"experiment_id"`
	DesignID     string             `json:"design_id"`
	ParentDesign string             `json:"parent_design,omitempty"`
	Generation   int                `json:"generation"`
	Algorithm    string             `json:"algorithm"`
	Seed         int                `json:"seed"`
	EvalIndex    int                `json:"eval_index"`
	Tag          string             `json:"tag"`
	Timestamp    string             `json:"timestamp"`
	Score        float64            `json:"score"`
	Feasible     bool               `json:"feasible"`
	Params       Params             `json:"params"`
	Terms        map[string]float64 `json:"terms"`
	Weighted     map[string]float64 `json:"weighted"`
	Penalties    map[string]float64 `json:"penalties"`
	Metrics      physics.Metrics    `json:"metrics"`
	Note         string             `json:"note,omitempty"`
}

// RequiredFields are the keys a record must carry (Python's REQUIRED_FIELDS).
var RequiredFields = []string{"experiment_id", "design_id", "algorithm", "seed", "score", "params", "terms", "metrics"}

// Registry is an append-only JSONL design registry. Safe for concurrent use.
type Registry struct {
	Path string
	mu   sync.Mutex
	n    int
}

// Open opens (creating if needed) a registry at path and counts existing lines.
// A truncated final line is tolerated and skipped, never fatal.
func Open(path string) (*Registry, error) { panic("TODO(stage C): implement registry open") }

// Len is the number of records on disk.
func (r *Registry) Len() int { panic("TODO(stage C): implement registry length") }

// NextIDs returns the ids the next Append will use.
func (r *Registry) NextIDs() (experimentID int, designID string) {
	panic("TODO(stage C): implement id allocation")
}

// Append writes one record, assigning ExperimentID/DesignID from the running
// counter and a UTC timestamp when they are unset. It must be serialised so that
// ids stay gap-free and sequential even when searches run concurrently.
func (r *Registry) Append(rec Record) error { panic("TODO(stage C): implement append") }

// Records loads every record (tolerating a truncated last line).
func (r *Registry) Records() ([]Record, error) { panic("TODO(stage C): implement records") }

// Best returns the highest-scoring record, optionally restricted to feasible
// designs and/or one algorithm (empty string = any).
func (r *Registry) Best(feasibleOnly bool, algorithm string) (*Record, bool) {
	panic("TODO(stage C): implement best")
}

// BestPerAlgorithm returns the best record per algorithm name.
func (r *Registry) BestPerAlgorithm() map[string]Record {
	panic("TODO(stage C): implement best-per-algorithm")
}

// Lineage maps design_id -> direct children design_ids.
func (r *Registry) Lineage() map[string][]string { panic("TODO(stage C): implement lineage") }

// BranchRow is one row of the branch-improvement table.
type BranchRow struct {
	DesignID       string  `json:"design_id"`
	NChildren      int     `json:"n_children"`
	ParentScore    float64 `json:"parent_score"`
	BestChildScore float64 `json:"best_child_score"`
	Gain           float64 `json:"gain"`
}

// BranchImprovement reports, per parent design, how much its best child improved
// on it, sorted by gain descending.
func (r *Registry) BranchImprovement() []BranchRow {
	panic("TODO(stage C): implement branch improvement")
}

// Summary is a compact description of the registry contents.
type Summary struct {
	NRecords      int            `json:"n_records"`
	NFeasible     int            `json:"n_feasible"`
	PerAlgo       map[string]int `json:"per_algorithm"`
	BestScore     float64        `json:"best_score"`
	BestDesignID  string         `json:"best_design_id"`
	BestAlgorithm string         `json:"best_algorithm"`
}

// Summary computes the summary above.
func (r *Registry) Summary() Summary { panic("TODO(stage C): implement summary") }

// Check validates registry integrity: required fields present, ids sequential
// from 1, every parent_design referencing an existing design. Returns a list of
// human-readable problems (empty = healthy). Used by the acceptance gate.
func (r *Registry) Check() ([]string, error) { panic("TODO(stage C): implement integrity check") }
