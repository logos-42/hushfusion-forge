// Field-structure metrics of a coil set (stage A).
//
// Implementation of metricsFor / axisRipple / minCoilGap from api.go, matching
// forge/physics/plasma_model.py key for key. Everything here is a vacuum-field
// property: no plasma, no pressure, no equilibrium.
package physics

import (
	"math"

	"github.com/logos-42/hushfusion-forge/internal/config"
)

// RippleProminence is the relative prominence (in units of B_mid) below which
// an axis structure does not count as ripple. Frozen: the Python reference
// calls axisRipple(..., prominence=0.05) as a default argument, and api.go's
// metricsFor definition names the same 0.05.
const RippleProminence = 0.05

// bMidFloor keeps B_mid out of the denominator of mirror_ratio and out of the
// volume_good threshold when a degenerated design produces a zero midplane
// field. Same 1e-9 floor as the reference.
const bMidFloor = 1e-9

// metricsFor computes every metric from a single pass over g.StackR/g.StackZ
// (plus K auxiliary single-coil calls for the conductor field, exactly as the
// reference does). The stacked arrays are sampled once and then sliced by
// [NAxis, NMid].
func metricsFor(coils []Coil, spec config.Spec, g Grids, s Solver) Metrics {
	m := Metrics{
		NCoils: len(coils),
		MU0:    config.MU0,
		// Set below; declared here so a bail-out still carries a finite gap.
		MinCoilGapM: minCoilGap(coils),
	}
	if s == nil || len(g.StackR) == 0 || len(g.StackR) != len(g.StackZ) {
		return m
	}

	// Single field pass over the stacked samples.
	bAll := s.Magnitude(coils, g.StackR, g.StackZ)

	// Slice by the frozen layout, clamped so a misbehaving solver cannot panic
	// the metric path (a finite record beats a crash).
	n := len(bAll)
	nAxis := g.NAxis
	if nAxis > n {
		nAxis = n
	}
	nMid := g.NMid
	if nAxis+nMid > n {
		nMid = n - nAxis
	}
	bAxis := bAll[:nAxis]
	bMidSamples := bAll[nAxis : nAxis+nMid]
	bCell := bAll[nAxis+nMid:]

	// B_mid = mean |B| over the midplane volume.
	bMid := 0.0
	for _, v := range bMidSamples {
		bMid += v
	}
	if len(bMidSamples) > 0 {
		bMid /= float64(len(bMidSamples))
	}

	// B_throat = max |B| on the axis over the whole sampled span (first index
	// wins a tie, like numpy.argmax).
	bThroat := 0.0
	iThroat := 0
	for i, v := range bAxis {
		if i == 0 || v > bThroat {
			bThroat = v
			iThroat = i
		}
	}

	m.BMidT = bMid
	m.BThroatT = bThroat
	floor := math.Max(bMid, bMidFloor)
	m.MirrorRatio = bThroat / floor
	if iThroat < len(g.AxisZ) {
		m.ZThroatM = g.AxisZ[iThroat]
	}

	// volume_good = fraction of the cell volume with |B| <= ConfineFactor*B_mid.
	good := 0
	for _, v := range bCell {
		if v <= spec.ConfineFactor*floor {
			good++
		}
	}
	if len(bCell) > 0 {
		m.VolumeGood = float64(good) / float64(len(bCell))
	}

	// ripple over the axis samples that sit inside the cell.
	axisCell := make([]float64, 0, len(bAxis))
	for i := range bAxis {
		if i < len(g.AxisInCell) && g.AxisInCell[i] {
			axisCell = append(axisCell, bAxis[i])
		}
	}
	m.Ripple = axisRipple(axisCell, bMid, RippleProminence)

	// B_coil_max and the proximity flag.
	bOthers, floorHit := conductorField(coils, spec, s)
	m.CoilProximityFloorHit = floorHit
	if len(coils) > 0 {
		peak := math.Inf(-1)
		for _, v := range bOthers {
			if v > peak {
				peak = v
			}
		}
		m.BCoilMaxT = peak + spec.SelfField()
	}

	// cost_proxy = sum_k I_k^2 r_k.
	cost := 0.0
	for _, c := range coils {
		cost += c.Current * c.Current * c.Radius
	}
	m.CostProxy = cost
	return m
}

// conductorField returns, for every coil, the summed magnitude of the fields
// produced by all *other* coils at that coil's location, plus whether any coil
// pair sat closer than ProximityFloor.
//
// Two details are frozen by the reference and matter numerically:
//   - the magnitudes are summed (not the vectors): sum_j |B_j(x_i)|;
//   - one field call per *source* coil evaluates that source at every other
//     coil's location, which is K calls instead of K*(K-1) single-point calls
//     and the same physics.
func conductorField(coils []Coil, spec config.Spec, s Solver) ([]float64, bool) {
	bOthers := make([]float64, len(coils))
	floorHit := false
	if len(coils) == 0 {
		return bOthers, floorHit
	}
	tr := make([]float64, 0, len(coils)-1)
	tz := make([]float64, 0, len(coils)-1)
	for j, src := range coils {
		targets := make([]int, 0, len(coils)-1)
		tr, tz = tr[:0], tz[:0]
		for i, tgt := range coils {
			if i == j {
				continue
			}
			targets = append(targets, i)
			tr = append(tr, tgt.Radius)
			tz = append(tz, tgt.Z-src.Z)
			if math.Hypot(tgt.Radius-src.Radius, tgt.Z-src.Z) < ProximityFloor {
				floorHit = true
			}
		}
		if len(targets) == 0 {
			continue
		}
		srcOnly := []Coil{{Radius: src.Radius, Z: 0, Current: src.Current}}
		mags := s.Magnitude(srcOnly, tr, tz)
		for k, i := range targets {
			if k < len(mags) {
				bOthers[i] += mags[k]
			}
		}
	}
	return bOthers, floorHit
}

// axisRipple is the normalised amplitude of the non-monotonic structure on the
// axis, per the frozen definition: interior extrema are found by 3-point
// comparison, only structures deeper than prominence*bMid count, only
// consecutive extrema that alternate (max, min) are summed, and the result is
// divided by bMid. A monotonic or single-peaked profile gives exactly 0.
func axisRipple(bAxisCell []float64, bMid, prominence float64) float64 {
	n := len(bAxisCell)
	if n < 5 || bMid <= 0 {
		return 0
	}
	type extremum struct {
		value float64
		isMax bool
	}
	exts := make([]extremum, 0, n/2)
	for i := 1; i < n-1; i++ {
		left, mid, right := bAxisCell[i-1], bAxisCell[i], bAxisCell[i+1]
		isMax := mid > left && mid > right
		isMin := mid < left && mid < right
		if isMax || isMin {
			exts = append(exts, extremum{value: mid, isMax: isMax})
		}
	}
	if len(exts) < 2 {
		return 0
	}
	total := 0.0
	for k := 0; k+1 < len(exts); k++ {
		if exts[k].isMax == exts[k+1].isMax {
			continue // not an alternating (max, min) / (min, max) pair
		}
		amp := math.Abs(exts[k].value - exts[k+1].value)
		if amp > prominence*bMid {
			total += amp
		}
	}
	return total / bMid
}

// minCoilGap is the smallest 3-D distance between two coil centres, +Inf for
// fewer than two coils. The (r, z) pair is the natural 3-D distance between
// axisymmetric coil centres, since both sit on the same azimuth.
func minCoilGap(coils []Coil) float64 {
	best := math.Inf(1)
	for i := range coils {
		for j := i + 1; j < len(coils); j++ {
			d := math.Hypot(coils[i].Radius-coils[j].Radius, coils[i].Z-coils[j].Z)
			if d < best {
				best = d
			}
		}
	}
	return best
}
