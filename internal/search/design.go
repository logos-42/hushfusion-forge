// Design-space helpers for the search layer.
//
// The frozen doc comments of api.go describe two design-space operations in
// terms of the physics layer (stage A):
//
//	physics.RandomDesign(rng, spec)   uniform i.i.d. sample, canonical order
//	physics.VectorToCoils + physics.CoilsToVector   clip + z-sort ("canonical")
//
// Stage A is an independent parallel line, so calling it directly would make the
// search package untestable until A lands — and the whole point of
// runner.Scorer being an interface is that stage D's unit tests pass on their
// own. The two operations are therefore seam variables with a local
// implementation whose semantics are the documented ones; the physics-backed
// implementations are also provided (PhysicsCanonicalise) so the wiring is a
// one-line assignment in the serial closing round, not a rewrite:
//
//	search.SampleDesign = physics.RandomDesign
//	search.Canonicalise = search.PhysicsCanonicalise
//	search.WarmStartDesign = func(spec config.Spec) []float64 { b, _ := baseline.TextbookMirror(spec); return b.Design }
//
// The local implementations use config.Spec.Lower/Upper only: bounds stay in
// config, the single source of truth, so no physical number is duplicated here.
package search

import (
	"math/rand"
	"sort"

	"github.com/logos-42/hushfusion-forge/internal/config"
	"github.com/logos-42/hushfusion-forge/internal/physics"
)

// SampleDesign draws one uniform i.i.d. design of the search box and returns it
// in canonical (z-sorted) order. It owns the RNG stream: callers must call it in
// a fixed logical order for a run to be reproducible.
//
// Default: physics.RandomDesign, exactly as the frozen doc comment of Random
// prescribes — stage A is the single source of truth for the search box. The
// seam stays a variable so a unit test can substitute a deterministic sampler,
// and localSampleDesign below is kept as the reference implementation that the
// parity gate in search_test.go compares against (it is bit-identical to
// physics.RandomDesign for the same seed at the time of writing).
var SampleDesign = physics.RandomDesign

// Canonicalise clips a design to the box and puts it in canonical order (coils
// sorted by ascending z), killing the K! coil-permutation degeneracy. Default:
// the physics implementation (VectorToCoils + CoilsToVector); localCanonicalise
// below is the reference implementation the parity gate compares against.
var Canonicalise = PhysicsCanonicalise

// WarmStartDesign returns the design that seeds EvolutionWarm's initial
// population when Options.WarmStart is empty: the textbook mirror design
// (testdata/golden_baseline.json, "textbook_mirror"), the human design the
// machine is asked to beat.
var WarmStartDesign = localWarmStart

func localSampleDesign(rng *rand.Rand, spec config.Spec) []float64 {
	lo, hi := spec.Lower(), spec.Upper()
	x := make([]float64, len(lo))
	for j := range x {
		x[j] = lo[j] + rng.Float64()*(hi[j]-lo[j])
	}
	return localCanonicalise(x, spec)
}

// localCanonicalise mirrors physics.VectorToCoils + physics.CoilsToVector:
// clip every parameter into the box, then sort the (r, z, I) triples by z with a
// stable sort so equal z values keep their input order.
func localCanonicalise(x []float64, spec config.Spec) []float64 {
	out := make([]float64, len(x))
	copy(out, x)

	nc := spec.NCoils
	lo, hi := spec.Lower(), spec.Upper()
	if len(out) != spec.NParams() || nc <= 0 || len(lo) != len(out) {
		return out
	}
	for j := range out {
		out[j] = clipTo(out[j], lo[j], hi[j])
	}
	if nc == 1 {
		return out
	}
	type triple struct{ r, z, i float64 }
	cs := make([]triple, nc)
	for k := 0; k < nc; k++ {
		cs[k] = triple{r: out[k], z: out[nc+k], i: out[2*nc+k]}
	}
	sort.SliceStable(cs, func(a, b int) bool { return cs[a].z < cs[b].z })
	for k := 0; k < nc; k++ {
		out[k], out[nc+k], out[2*nc+k] = cs[k].r, cs[k].z, cs[k].i
	}
	return out
}

// PhysicsCanonicalise is the stage-A-backed implementation of Canonicalise. It
// is here (compiled and tested for parity once stage A lands) so the swap above
// is a one-line wiring change.
func PhysicsCanonicalise(x []float64, spec config.Spec) []float64 {
	coils, err := physics.VectorToCoils(x, spec)
	if err != nil {
		out := make([]float64, len(x))
		copy(out, x)
		return out
	}
	return physics.CoilsToVector(coils)
}

// textbookMirrorDesign is the human baseline in canonical (z-ascending) form,
// copied bit-for-bit from testdata/golden_baseline.json ("design").
var textbookMirrorDesign = []float64{
	0.3, 0.5, 0.5, 0.3,
	-1.0, -0.25, 0.25, 1.0,
	1621279.2385890577, 463222.63959687366, 463222.63959687366, 1621279.2385890577,
}

// localWarmStart returns the textbook mirror for the reference 4-coil device and
// the box centre for any other coil count (the mirror proportions are only
// defined for 4 coils). The real source of truth is baseline.TextbookMirror,
// which also *solves* the cell current; this literal is the golden copy of that
// solved design and is replaced by the call above once stage B lands.
func localWarmStart(spec config.Spec) []float64 {
	if spec.NCoils == 4 {
		x := make([]float64, len(textbookMirrorDesign))
		copy(x, textbookMirrorDesign)
		return localCanonicalise(x, spec)
	}
	lo, hi := spec.Lower(), spec.Upper()
	x := make([]float64, len(lo))
	for j := range x {
		x[j] = 0.5 * (lo[j] + hi[j])
	}
	return localCanonicalise(x, spec)
}

func clipTo(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
