// record.go — how one evaluation becomes one registry record.
//
// Score() is deliberately split in three: evaluate (upstream physics/objective),
// record (pure, this file), and record+back-fill (the registry's lock). That
// split is what lets the recording path be tested for real without an evaluator
// that works yet, and it is also why the lineage edge returned to the search
// layer is the id of the record that was actually written.
package runner

import (
	"fmt"

	"github.com/logos-42/hushfusion-forge/internal/objective"
	"github.com/logos-42/hushfusion-forge/internal/registry"
)

// evaluate runs the upstream evaluation, with a clear failure when the runner was
// built without its dependencies (a nil dereference three frames deep is much
// harder to read than this).
func (r *Runner) evaluate(x []float64) objective.EvalResult {
	if r.Reg == nil {
		panic("runner.Score: nil Registry — build the runner with runner.New(reg, ev, tag)")
	}
	if r.Ev == nil {
		panic("runner.Score: nil Evaluator — build it with objective.NewEvaluator(...) and pass it to runner.New")
	}
	return r.Ev.Evaluate(x)
}

// record builds the registry record for one evaluation. ExperimentID, DesignID
// and Timestamp are left unset on purpose: the registry assigns them while it
// holds its lock, which is the only way ids stay gap-free and unique when
// searches evaluate from several goroutines.
func (r *Runner) record(res objective.EvalResult, meta Meta) registry.Record {
	return registry.Record{
		ParentDesign: meta.Parent,
		Generation:   meta.Generation,
		Algorithm:    meta.Algorithm,
		Seed:         meta.Seed,
		EvalIndex:    meta.EvalIndex,
		Tag:          r.Tag,
		Score:        res.Score,
		Feasible:     res.Feasible,
		Params:       designParams(res.Design, res.Metrics.NCoils),
		Terms:        orEmpty(res.Terms),
		Weighted:     orEmpty(res.Weighted),
		Penalties:    orEmpty(res.Penalties),
		Metrics:      res.Metrics,
		Note:         meta.Note,
	}
}

// recordResult appends the record and back-fills the ids it was given into the
// EvalResult the search layer receives.
func (r *Runner) recordResult(res objective.EvalResult, meta Meta) objective.EvalResult {
	if r.Reg == nil {
		panic("runner.Score: nil Registry — build the runner with runner.New(reg, ev, tag)")
	}
	assigned, err := r.Reg.AppendAssign(r.record(res, meta))
	if err != nil {
		// A record that was scored but not stored did not happen: the registry is
		// the source of truth for every claim the report makes downstream. The
		// Python reference raised here too (Registry.append -> ValueError/OSError);
		// the frozen Go signature has no error to return, so it is reported by
		// panicking rather than by dropping the experiment silently.
		panic(fmt.Sprintf("runner.Score: recording the experiment in %s failed: %v", r.Reg.Path, err))
	}
	res.DesignID = assigned.DesignID
	res.ExperimentID = assigned.ExperimentID
	return res
}

// designParams splits the canonical design vector [r_0..r_K, z_0..z_K,
// I_0..I_K] into the named per-coil arrays of a record, exactly as the Python
// reference does (design[:n], design[n:2n], design[2n:] with n = n_coils).
//
// It must not panic on a malformed vector: if the evaluation came back with a
// length that is not 3*n_coils, the record still has to carry whatever evidence
// exists (a record with a short params array is diagnosable; a crashed run is
// not).
func designParams(design []float64, nCoils int) registry.Params {
	k := nCoils
	if k <= 0 || 3*k > len(design) {
		k = len(design) / 3
	}
	if k < 0 {
		k = 0
	}
	radius := make([]float64, 0, k)
	radius = append(radius, design[:k]...)
	zEnd := min(2*k, len(design))
	z := make([]float64, 0, k)
	z = append(z, design[k:zEnd]...)
	current := make([]float64, 0, k)
	if 2*k < len(design) {
		current = append(current, design[2*k:]...)
	}
	return registry.Params{RadiusM: radius, ZM: z, CurrentA: current}
}

// orEmpty keeps the JSON type of a record's map fields stable: a nil map would be
// written as null where the Python reference writes {}.
func orEmpty(m map[string]float64) map[string]float64 {
	if m == nil {
		return map[string]float64{}
	}
	return m
}
