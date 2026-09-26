// Device parameterisation and evaluation grids (stage A).
//
// Mirrors forge/physics/geometry.py exactly, including the sample ordering of
// the stacked grids: golden comparisons slice the stacked arrays by
// [NAxis, NMid], so both the order and the count are part of the contract.
package physics

import (
	"fmt"
	"math"
	"math/rand"
	"sort"

	"github.com/logos-42/hushfusion-forge/internal/config"
)

// midplaneZSamples is the fixed axial sample count of the midplane volume. The
// frozen buildGrids doc comment pins it at 5 (the Python reference hard-codes
// linspace(-ZMid, ZMid, 5) for the midplane even though the cell volume uses
// spec.NVolZ).
const midplaneZSamples = 5

// linspace matches numpy.linspace(lo, hi, n): n evenly spaced values with both
// endpoints included, the last one set to hi exactly (numpy does the same
// final assignment, which removes the accumulated rounding at the endpoint).
func linspace(lo, hi float64, n int) []float64 {
	if n <= 0 {
		return nil
	}
	out := make([]float64, n)
	if n == 1 {
		out[0] = lo
		return out
	}
	step := (hi - lo) / float64(n-1)
	for i := range out {
		out[i] = lo + step*float64(i)
	}
	out[n-1] = hi
	return out
}

// buildGrids builds the stacked sample points
// [axis (r=0) | midplane volume | cell volume] in the frozen order:
//
//	axis   : linspace(-ZAxisMax, ZAxisMax, NAxis), r = 0
//	mid    : meshgrid(linspace(0, RPlasma, NVolR), linspace(-ZMid, ZMid, 5), "ij")
//	cell   : meshgrid(linspace(0, RPlasma, NVolR), linspace(-ZCell, ZCell, NVolZ), "ij")
//
// With meshgrid(..., "ij") the *radius* varies slowest, so the flattened order
// is (r_0, z_0), (r_0, z_1), ... , (r_1, z_0), ...
func buildGrids(spec config.Spec) Grids {
	axisZ := linspace(-spec.ZAxisMax, spec.ZAxisMax, spec.NAxis)
	radii := linspace(0, spec.RPlasma, spec.NVolR)
	zMid := linspace(-spec.ZMid, spec.ZMid, midplaneZSamples)
	zCell := linspace(-spec.ZCell, spec.ZCell, spec.NVolZ)

	nMid := len(radii) * len(zMid)
	nCell := len(radii) * len(zCell)

	stackR := make([]float64, 0, len(axisZ)+nMid+nCell)
	stackZ := make([]float64, 0, cap(stackR))
	midR := make([]float64, 0, nMid)
	cellR := make([]float64, 0, nCell)
	cellZ := make([]float64, 0, nCell)

	for _, z := range axisZ {
		stackR = append(stackR, 0)
		stackZ = append(stackZ, z)
	}
	for _, rr := range radii {
		for _, zz := range zMid {
			stackR = append(stackR, rr)
			stackZ = append(stackZ, zz)
			midR = append(midR, rr)
		}
	}
	for _, rr := range radii {
		for _, zz := range zCell {
			stackR = append(stackR, rr)
			stackZ = append(stackZ, zz)
			cellR = append(cellR, rr)
			cellZ = append(cellZ, zz)
		}
	}

	axisInCell := make([]bool, len(axisZ))
	for i, z := range axisZ {
		axisInCell[i] = math.Abs(z) <= spec.ZCell
	}

	return Grids{
		StackR:     stackR,
		StackZ:     stackZ,
		NAxis:      len(axisZ),
		NMid:       nMid,
		AxisZ:      axisZ,
		AxisInCell: axisInCell,
		MidR:       midR,
		CellR:      cellR,
		CellZ:      cellZ,
	}
}

// vectorToCoils decodes [r_0..r_K, z_0..z_K, I_0..I_K], clipping every entry to
// the spec bounds and sorting the coils by z (stable, so equal-z coils keep the
// vector order). The stable z-sort is the canonical form that removes the K!
// permutation degeneracy without changing the machine.
func vectorToCoils(x []float64, spec config.Spec) ([]Coil, error) {
	want := spec.NParams()
	if len(x) != want {
		return nil, fmt.Errorf("physics: design vector has %d entries, spec wants %d", len(x), want)
	}
	k := spec.NCoils
	if k <= 0 {
		return nil, nil
	}
	lo, hi := spec.Lower(), spec.Upper()
	coils := make([]Coil, k)
	for i := 0; i < k; i++ {
		coils[i] = Coil{
			Radius:  clampVec(x[i], lo[i], hi[i]),
			Z:       clampVec(x[k+i], lo[k+i], hi[k+i]),
			Current: clampVec(x[2*k+i], lo[2*k+i], hi[2*k+i]),
		}
	}
	sort.SliceStable(coils, func(i, j int) bool { return coils[i].Z < coils[j].Z })
	return coils, nil
}

// clampVec is numpy.clip(v, lo, hi): NaN propagates, otherwise the value is
// pinned into [lo, hi].
func clampVec(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// coilsToVector encodes coils in any order into the canonical vector
// (z-ascending, stable).
func coilsToVector(coils []Coil) []float64 {
	ordered := make([]Coil, len(coils))
	copy(ordered, coils)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Z < ordered[j].Z })
	k := len(ordered)
	out := make([]float64, 0, 3*k)
	for _, c := range ordered {
		out = append(out, c.Radius)
	}
	for _, c := range ordered {
		out = append(out, c.Z)
	}
	for _, c := range ordered {
		out = append(out, c.Current)
	}
	return out
}

// randomDesign draws a uniform sample of the search box and returns it in
// canonical order (clipped + z-sorted), so search never evaluates a permutation
// of a design it has already seen.
func randomDesign(rng *rand.Rand, spec config.Spec) []float64 {
	lo, hi := spec.Lower(), spec.Upper()
	x := make([]float64, len(lo))
	for i := range x {
		x[i] = lo[i] + rng.Float64()*(hi[i]-lo[i])
	}
	coils, err := vectorToCoils(x, spec)
	if err != nil {
		return x
	}
	return coilsToVector(coils)
}
