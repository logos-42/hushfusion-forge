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
	"encoding/json"
	"fmt"
	"sort"
	"strings"
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
func Open(path string) (*Registry, error) {
	n, err := openRegistryFile(path)
	if err != nil {
		return nil, err
	}
	return &Registry{Path: path, n: n}, nil
}

// Len is the number of records on disk.
func (r *Registry) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n
}

// NextIDs returns the ids the next Append will use.
func (r *Registry) NextIDs() (experimentID int, designID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	next := r.n + 1
	return next, formatDesignID(next)
}

// Append writes one record, assigning ExperimentID/DesignID from the running
// counter and a UTC timestamp when they are unset. It must be serialised so that
// ids stay gap-free and sequential even when searches run concurrently.
func (r *Registry) Append(rec Record) error {
	// Append is the frozen name; the assignment-and-return variant used by
	// runner.Score shares the exact same code path (see AppendAssign).
	_, err := r.AppendAssign(rec)
	return err
}

// Records loads every record (tolerating a truncated last line).
func (r *Registry) Records() ([]Record, error) {
	lines, err := r.readLines()
	if err != nil {
		return nil, err
	}
	out := make([]Record, 0, len(lines))
	for _, ln := range lines {
		if rec, ok := decodeRecord(ln.text); ok {
			out = append(out, rec)
		}
	}
	return out, nil
}

// Best returns the highest-scoring record, optionally restricted to feasible
// designs and/or one algorithm (empty string = any).
func (r *Registry) Best(feasibleOnly bool, algorithm string) (*Record, bool) {
	recs, err := r.Records()
	if err != nil {
		return nil, false
	}
	var best *Record
	for i := range recs {
		if feasibleOnly && !recs[i].Feasible {
			continue
		}
		if algorithm != "" && recs[i].Algorithm != algorithm {
			continue
		}
		if best == nil || recs[i].Score > best.Score {
			best = &recs[i]
		}
	}
	return best, best != nil
}

// BestPerAlgorithm returns the best record per algorithm name.
func (r *Registry) BestPerAlgorithm() map[string]Record {
	recs, err := r.Records()
	if err != nil {
		return map[string]Record{}
	}
	out := make(map[string]Record, 4)
	for i := range recs {
		cur, ok := out[recs[i].Algorithm]
		if !ok || recs[i].Score > cur.Score {
			out[recs[i].Algorithm] = recs[i]
		}
	}
	return out
}

// Lineage maps design_id -> direct children design_ids.
func (r *Registry) Lineage() map[string][]string {
	recs, err := r.Records()
	if err != nil {
		return map[string][]string{}
	}
	return lineageOf(recs)
}

// lineageOf builds design_id -> direct children from an already loaded slice.
// Child order follows record order, so the result is deterministic.
func lineageOf(recs []Record) map[string][]string {
	tree := make(map[string][]string)
	for i := range recs {
		if recs[i].ParentDesign == "" {
			continue
		}
		tree[recs[i].ParentDesign] = append(tree[recs[i].ParentDesign], recs[i].DesignID)
	}
	return tree
}

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
	recs, err := r.Records()
	if err != nil {
		return []BranchRow{}
	}
	children := lineageOf(recs)
	byID := make(map[string]Record, len(recs))
	for i := range recs {
		byID[recs[i].DesignID] = recs[i]
	}
	rows := make([]BranchRow, 0, len(children))
	seenParent := make(map[string]bool, len(children))
	// Iterate the records (not the map) so the row order is deterministic.
	for i := range recs {
		parent := recs[i].DesignID
		kids, isParent := children[parent]
		if !isParent || seenParent[parent] {
			continue
		}
		seenParent[parent] = true
		bestChild, nKnown := 0.0, 0
		for _, k := range kids {
			kid, ok := byID[k]
			if !ok {
				continue
			}
			if nKnown == 0 || kid.Score > bestChild {
				bestChild = kid.Score
			}
			nKnown++
		}
		if nKnown == 0 {
			// No child of this design ever reached the registry: a dangling
			// lineage edge, not an improvement row.
			continue
		}
		rows = append(rows, BranchRow{
			DesignID:       parent,
			NChildren:      len(kids),
			ParentScore:    recs[i].Score,
			BestChildScore: bestChild,
			Gain:           bestChild - recs[i].Score,
		})
	}
	// Gain descending; ties broken by design_id so the table is reproducible
	// (Go map iteration above is order-free, and an unstable tie order would make
	// the same registry render differently between runs).
	sort.Slice(rows, func(a, b int) bool {
		if rows[a].Gain != rows[b].Gain {
			return rows[a].Gain > rows[b].Gain
		}
		return rows[a].DesignID < rows[b].DesignID
	})
	return rows
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
func (r *Registry) Summary() Summary {
	sum := Summary{PerAlgo: map[string]int{}}
	recs, err := r.Records()
	if err != nil {
		return sum
	}
	sum.NRecords = len(recs)
	var best *Record
	for i := range recs {
		if recs[i].Feasible {
			sum.NFeasible++
		}
		sum.PerAlgo[recs[i].Algorithm]++
		if best == nil || recs[i].Score > best.Score {
			best = &recs[i]
		}
	}
	if best != nil {
		sum.BestScore = best.Score
		sum.BestDesignID = best.DesignID
		sum.BestAlgorithm = best.Algorithm
	}
	return sum
}

// Check validates registry integrity: required fields present, ids sequential
// from 1, every parent_design referencing an existing design. Returns a list of
// human-readable problems (empty = healthy). Used by the acceptance gate.
func (r *Registry) Check() ([]string, error) {
	lines, err := r.readLines()
	if err != nil {
		return nil, err
	}
	type entry struct {
		lineNo int // physical line number, for messages
		pos    int // 1-based record ordinal, what experiment_id is checked against
		raw    map[string]json.RawMessage
		rec    Record
	}
	problems := []string{}
	entries := make([]entry, 0, len(lines))
	for i, ln := range lines {
		raw, rawErr := decodeObject(ln.text)
		rec, recErr := decodeRecord(ln.text)
		if rawErr != nil || !recErr {
			// Only a truncated FINAL line is tolerated (that is what a killed
			// process leaves behind). A corrupt line in the middle is real damage:
			// the record it held is gone for good. Note that it still occupies its
			// position in the sequence, so it does not shift the ids of the
			// records after it.
			if i == len(lines)-1 {
				continue
			}
			problems = append(problems, fmt.Sprintf(
				"line %d: not valid JSON (a truncated final line is tolerated, an unreadable interior line is not)", ln.no))
			continue
		}
		entries = append(entries, entry{lineNo: ln.no, pos: i + 1, raw: raw, rec: rec})
	}

	// 1. experiment_id sequential from 1: the k-th record must carry id k, so a
	//    deleted record, a renumbering, a duplicate or a corrupt line all show up
	//    as the same checkable statement.
	firstLineOfID := make(map[int]int, len(entries))
	for _, e := range entries {
		id := e.rec.ExperimentID
		switch {
		case id <= 0:
			problems = append(problems, fmt.Sprintf(
				"line %d: experiment_id %d is out of range (record %d must carry id %d)",
				e.lineNo, id, e.pos, e.pos))
		case id != e.pos:
			if first, dup := firstLineOfID[id]; dup {
				problems = append(problems, fmt.Sprintf(
					"line %d: experiment_id %d is not sequential (duplicate of the record on line %d; record %d must carry id %d)",
					e.lineNo, id, first, e.pos, e.pos))
			} else {
				problems = append(problems, fmt.Sprintf(
					"line %d: experiment_id %d is not sequential (record %d must carry id %d)",
					e.lineNo, id, e.pos, e.pos))
			}
		}
		if _, seen := firstLineOfID[id]; !seen {
			firstLineOfID[id] = e.lineNo
		}
	}

	// 2. required fields present in the RAW line (a typed decode cannot see a
	//    missing key: it just yields the zero value), plus design_id hygiene.
	firstLineOfDesign := make(map[string]int, len(entries))
	for _, e := range entries {
		var missing []string
		for _, f := range RequiredFields {
			if _, ok := e.raw[f]; !ok {
				missing = append(missing, f)
			}
		}
		if len(missing) > 0 {
			problems = append(problems, fmt.Sprintf(
				"line %d: missing required field(s): %s", e.lineNo, strings.Join(missing, ", ")))
		}
		switch {
		case e.rec.DesignID == "":
			problems = append(problems, fmt.Sprintf("line %d: design_id is empty", e.lineNo))
		default:
			if first, dup := firstLineOfDesign[e.rec.DesignID]; dup {
				problems = append(problems, fmt.Sprintf(
					"line %d: duplicate design_id %q (first seen on line %d)", e.lineNo, e.rec.DesignID, first))
			} else {
				firstLineOfDesign[e.rec.DesignID] = e.lineNo
			}
		}
	}

	// 3. every lineage edge points at a design that exists.
	for _, e := range entries {
		if e.rec.ParentDesign == "" {
			continue
		}
		if _, ok := firstLineOfDesign[e.rec.ParentDesign]; !ok {
			problems = append(problems, fmt.Sprintf(
				"line %d: parent_design %q does not exist in the registry", e.lineNo, e.rec.ParentDesign))
		}
	}
	return problems, nil
}
