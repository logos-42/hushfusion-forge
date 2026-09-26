// Package physics: device parameterisation, magnetostatics, field metrics.
//
// FROZEN INTERFACE (v0.1) — owner: stage A (see internal/owners/owners.go).
// Implemented in magnet.go / metrics.go / geometry.go by the stage owner.
//
// Everything here is a *vacuum field* property. There is no plasma.
package physics

import (
	"math/rand"

	"github.com/logos-42/hushfusion-forge/internal/config"
)

// Coil is a single circular filament loop with its axis on the z-axis.
type Coil struct {
	Radius  float64 `json:"radius_m"`
	Z       float64 `json:"z_m"`
	Current float64 `json:"current_A"`
}

// Solver computes |B| of a coil set at cylindrical sample points.
//
// Two implementations must exist and must agree:
//   - AnalyticSolver: exact closed form via complete elliptic integrals (default)
//   - DiscreteSolver: direct numerical Biot-Savart summation over segments
//
// They share no code path, which is exactly why the second one is the
// independent check on the first (see cmd/forge verify).
type Solver interface {
	Name() string
	Magnitude(coils []Coil, r, z []float64) []float64
}

// AnalyticSolver is the exact circular-filament solver (elliptic integrals).
type AnalyticSolver struct{}

// DiscreteSolver sums Biot-Savart over NSeg straight segments per loop.
type DiscreteSolver struct{ NSeg int }

// EllipticKE returns the complete elliptic integrals of the first and second
// kind, K(m) and E(m), for parameter m = k^2 in [0, 1).
//
// Required algorithm (AGM, arithmetic-geometric mean) — use this, do not
// approximate:
//
//	a0 = 1, b0 = sqrt(1-m), c0 = sqrt(m)
//	a_{n+1} = (a_n + b_n)/2
//	b_{n+1} = sqrt(a_n * b_n)
//	c_{n+1} = (a_n - b_n)/2
//	K(m) = pi / (2 * a_N)
//	E(m) = K(m) * (1 - sum_{n=0..N} 2^(n-1) * c_n^2)
//
// with the n = 0 term of the sum being c_0^2 / 2. Iterate until c_n ~ 0
// (typically 5-10 iterations to machine precision).
func EllipticKE(m float64) (k, e float64) { panic("TODO(stage A): implement AGM elliptic integrals") }

// LoopField returns (B_r, B_z) of one circular filament loop of the given
// radius and current at the point (r, z), the loop lying in the plane z = 0.
//
// Closed form (Simpson et al., NASA/TM-2001-211135), with
//
//	alpha2 = a^2 + r^2 + z^2 - 2*a*r
//	beta2  = a^2 + r^2 + z^2 + 2*a*r
//	m      = 1 - alpha2/beta2
//	C      = mu0*I/pi
//
//	B_r = C*z/(2*alpha2*beta) * [ (a^2 + r^2 + z^2)*E(m) - alpha2*K(m) ]
//	B_z =   C  /(2*alpha2*beta) * [ (a^2 - r^2 - z^2)*E(m) + alpha2*K(m) ]
//
// On the axis (r -> 0) the exact limit must be used to avoid 0/0:
//
//	B_r = 0,  B_z = mu0*I*a^2 / (2*(a^2 + z^2)^1.5)
//
// The on-axis branch is exercised by the golden field samples in testdata/.
func LoopField(radius, current, r, z float64) (br, bz float64) {
	panic("TODO(stage A): implement exact circular-loop field")
}

// LoopFieldDiscrete is the independent implementation: a direct Biot-Savart sum
// over nSeg straight segments approximating the loop. It must agree with
// LoopField to < 1e-9 relative for nSeg >= 512 at points away from the wire.
func LoopFieldDiscrete(radius, current, r, z float64, nSeg int) (br, bz float64) {
	panic("TODO(stage A): implement discrete Biot-Savart sum")
}

// CoilsetField superposes the field of every coil (each shifted to its own z).
func CoilsetField(coils []Coil, r, z []float64) (br, bz []float64) {
	panic("TODO(stage A): implement coil-set superposition")
}

// OnAxisField returns |B| on the axis (r = 0). Exact and cheaper than the
// general evaluation: only the on-axis branch of LoopField is needed.
func OnAxisField(coils []Coil, z []float64) []float64 {
	panic("TODO(stage A): implement on-axis field")
}

// Grids are the stacked evaluation sample points:
// [axis (r=0) | midplane volume | cell volume].
type Grids struct {
	StackR     []float64 // cylindrical radius of every sample
	StackZ     []float64 // axial coordinate of every sample
	NAxis      int       // axis samples occupy Stack[0:NAxis]
	NMid       int       // midplane volume occupies Stack[NAxis : NAxis+NMid]
	AxisZ      []float64 // on-axis sample positions, for reporting
	AxisInCell []bool    // |AxisZ[i]| <= spec.ZCell
	MidR       []float64
	CellR      []float64
	CellZ      []float64
}

// BuildGrids mirrors the Python reference (forge/physics/geometry.py) exactly,
// including the order of the concatenated samples:
//
//	axis   : linspace(-ZAxisMax, +ZAxisMax, NAxis), r = 0
//	mid    : meshgrid(linspace(0, RPlasma, NVolR), linspace(-ZMid, ZMid, 5), "ij")
//	cell   : meshgrid(linspace(0, RPlasma, NVolR), linspace(-ZCell, ZCell, NVolZ), "ij")
//
// Note the midplane volume uses a fixed 5 axial samples. Sample order matters:
// golden comparisons slice the stacked array by [NAxis, NMid].
func BuildGrids(spec config.Spec) Grids {
	panic("TODO(stage A): implement evaluation grids")
}

// Metrics are the objective-relevant field metrics of one coil set.
//
// JSON tags are FROZEN: they are the cross-language interchange format and must
// match forge/physics/plasma_model.py key for key.
type Metrics struct {
	BMidT                 float64 `json:"B_mid_T"`
	BThroatT              float64 `json:"B_throat_T"`
	ZThroatM              float64 `json:"z_throat_m"`
	MirrorRatio           float64 `json:"mirror_ratio"`
	VolumeGood            float64 `json:"volume_good"`
	Ripple                float64 `json:"ripple"`
	BCoilMaxT             float64 `json:"B_coil_max_T"`
	MinCoilGapM           float64 `json:"min_coil_gap_m"`
	CostProxy             float64 `json:"cost_proxy"`
	CoilProximityFloorHit bool    `json:"coil_proximity_floor_hit"`
	NCoils                int     `json:"n_coils"`
	MU0                   float64 `json:"mu0"`
}

// MetricsFor computes every metric from a single field pass over g.StackR/StackZ.
//
// Definitions (must match the Python reference exactly):
//
//	B_mid    = mean(|B|) over the midplane volume samples
//	B_throat = max(|B|) on the axis over the whole sampled span
//	R        = B_throat / B_mid
//	V_good   = fraction of cell-volume samples with |B| <= ConfineFactor*B_mid
//	ripple   = AxisRipple(axis samples inside the cell, B_mid, 0.05)
//	B_coil   = max over coils of (field from all *other* coils at that coil's
//	           location + spec.SelfField())
//	cost     = sum_k I_k^2 * r_k
//	min_gap  = smallest 3-D distance between two coil centres
//
// If another coil sits closer than 5e-3 m, the closed form is singular: floor it
// and set CoilProximityFloorHit = true (the objective penalises that geometry;
// a finite number must still reach the record instead of a NaN).
func MetricsFor(coils []Coil, spec config.Spec, g Grids, s Solver) Metrics {
	panic("TODO(stage A): implement metrics")
}

// AxisRipple is the normalised amplitude of NON-monotonic structure on the axis.
//
// Sum |B_peak - B_adjacent_valley| over consecutive interior extrema that
// alternate (max, min), keeping only structures deeper than prominence*BMid,
// then divide by BMid. A monotonic or single-peaked profile returns exactly 0.
// Interior extrema are found by 3-point comparison B[i-1] < B[i] > B[i+1].
func AxisRipple(bAxisCell []float64, bMid, prominence float64) float64 {
	panic("TODO(stage A): implement axis ripple")
}

// MinCoilGap is the smallest distance between two coil centres; +Inf if fewer
// than two coils are given.
func MinCoilGap(coils []Coil) float64 { panic("TODO(stage A): implement min coil gap") }

// VectorToCoils decodes a design vector, clipping to the spec bounds and
// sorting by z (canonical form: kills the K! permutation degeneracy).
// An error is returned if len(x) != spec.NParams().
func VectorToCoils(x []float64, spec config.Spec) ([]Coil, error) {
	panic("TODO(stage A): implement design-vector decoding")
}

// CoilsToVector encodes coils (any order) into the canonical design vector.
func CoilsToVector(coils []Coil) []float64 { panic("TODO(stage A): implement encoding") }

// RandomDesign draws a uniform sample of the search box in canonical order.
func RandomDesign(rng *rand.Rand, spec config.Spec) []float64 {
	panic("TODO(stage A): implement uniform random design")
}
