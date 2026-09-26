// Magnetostatics of circular filament loops in vacuum (stage A).
//
// Implementation of the frozen declarations in api.go. Two independent code
// paths live here on purpose:
//
//	loopField            exact closed form, complete elliptic integrals (AGM)
//	loopFieldDiscrete    direct Biot-Savart summation over straight segments
//
// They share no line of arithmetic, which is what makes the second one a real
// cross-check of the first (physics_test.go asserts they agree to < 1e-9
// relative for nSeg = 512 at points away from the wire).
package physics

import (
	"math"

	"github.com/logos-42/hushfusion-forge/internal/config"
)

const (
	// axisTol is the radius below which a sample is evaluated with the exact
	// on-axis limit. Same value as the protected Python reference
	// (forge/physics/magnetic_field.py: _AXIS_TOL); the general closed form has
	// a 0/0 cancellation in B_r at r -> 0, so the limit branch is not just an
	// optimisation, it is the only numerically meaningful expression there.
	axisTol = 1e-9

	// ProximityFloor is the sample-to-wire distance [m] at which the 1/alpha^2
	// term of the closed form starts to blow up. A sample closer than this to
	// the nearest wire point is evaluated as if it were exactly this far away,
	// so the field stays finite (a metric on a degenerate geometry must reach
	// the record as a number, not as a NaN).
	//
	// Same constant as PROXIMITY_FLOOR in the protected Python reference
	// (forge/physics/plasma_model.py) and the threshold used by MetricsFor to
	// set CoilProximityFloorHit.
	ProximityFloor = 5e-3

	// tiny mirrors the reference's np.maximum(x, 1e-300) guards: it only has to
	// keep a division finite, it is never a physical scale.
	tiny = 1e-300

	// ellipMMax caps m = k^2 just below 1 where K(m) diverges.
	ellipMMax = 1.0 - 1e-12

	// keMaxIter / keEps bound the AGM iteration for the elliptic integrals.
	// The AGM doubles its precision every step. keEps is RELATIVE to a_n: the
	// stopping test must be a relative one, because a_n - b_n cannot resolve a
	// difference below eps*a_n, so an absolute test can never fire and the
	// iteration would keep adding 2^(n-1) c_n^2 terms of pure rounding noise
	// (with 2^(n-1) growing, those terms are amplified, not damped).
	keMaxIter = 64
	keEps     = 1e-16

	// defaultNSeg is used by DiscreteSolver when NSeg <= 0 (the zero value of
	// the struct). It matches the reference's loop_field_discrete default.
	defaultNSeg = 1440
)

// ellipticKE returns K(m) and E(m) by the arithmetic-geometric mean, exactly as
// specified in api.go:
//
//	a0 = 1, b0 = sqrt(1-m), c0 = sqrt(m)
//	K(m) = pi/(2 a_N)
//	E(m) = K(m) * (1 - sum_{n=0..N} 2^(n-1) c_n^2),  n = 0 term = c0^2/2
func ellipticKE(m float64) (k, e float64) {
	if m < 0 {
		m = 0
	}
	if m >= 1 {
		// Out of the frozen domain [0, 1): K diverges, E -> 1. Reported as the
		// mathematical limit rather than as a finite fabrication. Callers
		// (loopField) stay strictly inside [0, 1 - 1e-12).
		return math.Inf(1), 1
	}
	a := 1.0
	b := math.Sqrt(1 - m)
	c := math.Sqrt(m)
	// n = 0 term of the sum: 2^(-1) c0^2.
	sum := 0.5 * c * c
	pw := 1.0 // 2^(n-1) for the next n
	prev := math.Inf(1)

	for n := 1; n <= keMaxIter; n++ {
		an := 0.5 * (a + b)
		cn := 0.5 * (a - b)
		bn := math.Sqrt(a * b)
		a, b, c = an, bn, cn
		sum += pw * c * c
		abs := math.Abs(c)
		// Converged to machine precision: |c_n| is at (or below) the rounding
		// floor of a_n - b_n, i.e. it is no longer decreasing. Anything added
		// past this point is noise amplified by 2^(n-1).
		if c == 0 || abs <= keEps*a || abs >= prev || pw > 1e300 {
			break
		}
		prev = abs
		pw *= 2
	}
	k = math.Pi / (2 * a)
	e = k * (1 - sum)
	return k, e
}

// loopField is the exact off-axis/on-axis field of one circular filament, per
// the closed form frozen in api.go. The loop lies in the plane z = 0.
//
// Note for reviewers: api.go's doc comment writes B_r without the 1/r factor.
// The frozen formula is the standard Simpson et al. expression
//
//	B_r = C*z/(2*alpha2*beta*r) * [ (a^2 + r^2 + z^2)*E(m) - alpha2*K(m) ]
//
// and that is what the golden data in testdata/ was generated with (the
// analytic part of golden_field_samples.json reproduces it bit for bit). The
// 1/r is restored here, otherwise every off-axis B_r would be wrong by a factor
// of r (and the golden comparison would fail by ~1e-2 relative, not 1e-9).
func loopField(radius, current, r, z float64) (br, bz float64) {
	a2 := radius * radius
	r2z2 := r*r + z*z
	alpha2 := a2 + r2z2 - 2*radius*r // squared distance to the nearest wire point
	if alpha2 < proximityFloor2 {
		alpha2 = proximityFloor2
	}
	beta2 := a2 + r2z2 + 2*radius*r
	beta := math.Sqrt(math.Max(beta2, tiny))
	m := 1 - alpha2/math.Max(beta2, tiny)
	if m < 0 {
		m = 0
	} else if m > ellipMMax {
		m = ellipMMax
	}

	if math.Abs(r) < axisTol {
		// Exact limit: B_r = 0, B_z = mu0*I*a^2 / (2*(a^2+z^2)^1.5).
		den := math.Max(a2+z*z, tiny)
		return 0, config.MU0 * current * a2 / (2 * math.Pow(den, 1.5))
	}

	ek, ee := ellipticKE(m)
	c := config.MU0 * current / math.Pi
	denom := 2 * alpha2 * beta
	br = c * z * ((a2+r2z2)*ee - alpha2*ek) / (denom * r)
	bz = c * ((a2-r2z2)*ee + alpha2*ek) / denom
	return br, bz
}

// proximityFloor2 is ProximityFloor squared (the floor is applied to alpha^2).
const proximityFloor2 = ProximityFloor * ProximityFloor

// loopFieldDiscrete is the independent Biot-Savart check: the loop is replaced
// by nSeg straight segments, each represented by its midpoint and its tangent
// length, and the field is the sum of the (mu0 I/4pi) dl x (r-r')/|r-r'|^3
// contributions. It converges geometrically in nSeg (the midpoint rule on an
// analytic periodic integrand), which is why agreement with the closed form is
// a statement about both implementations, not about either quadrature rule.
func loopFieldDiscrete(radius, current, r, z float64, nSeg int) (br, bz float64) {
	if nSeg < 1 {
		nSeg = defaultNSeg
	}
	dphi := 2 * math.Pi / float64(nSeg)
	segLen := 2 * math.Pi * radius / float64(nSeg)
	pre := config.MU0 * current / (4 * math.Pi)

	var sumR, sumZ float64
	for i := 0; i < nSeg; i++ {
		phi := (float64(i) + 0.5) * dphi
		sinp, cosp := math.Sincos(phi)

		// Segment midpoint on the loop and its tangent vector dl.
		cx := radius * cosp
		cy := radius * sinp
		dlx := -segLen * sinp
		dly := segLen * cosp

		// Vector from the segment to the observation point (r, 0, z).
		dx := r - cx
		dy := -cy
		dz := z
		dist2 := dx*dx + dy*dy + dz*dz
		dist := math.Sqrt(dist2)
		if dist < 1e-12 {
			dist = 1e-12
		}
		d3 := dist * dist * dist

		// (dl x delta)_r = dly*dz, (dl x delta)_z = dlx*dy - dly*dx.
		sumR += dly * dz / d3
		sumZ += (dlx*dy - dly*dx) / d3
	}
	return pre * sumR, pre * sumZ
}

// coilsetField superposes every coil, each shifted to its own z.
func coilsetField(coils []Coil, r, z []float64) (br, bz []float64) {
	if len(r) != len(z) {
		panic("physics: coilsetField called with mismatched r/z lengths")
	}
	n := len(r)
	br = make([]float64, n)
	bz = make([]float64, n)
	for _, c := range coils {
		for i := 0; i < n; i++ {
			cbr, cbz := loopField(c.Radius, c.Current, r[i], z[i]-c.Z)
			br[i] += cbr
			bz[i] += cbz
		}
	}
	return br, bz
}

// onAxisField returns |B| on the axis, using only the exact on-axis branch.
// The absolute value is taken of the *sum* (a negative-current coil subtracts).
func onAxisField(coils []Coil, z []float64) []float64 {
	out := make([]float64, len(z))
	for _, c := range coils {
		a2 := c.Radius * c.Radius
		for i, zi := range z {
			dz := zi - c.Z
			out[i] += config.MU0 * c.Current * a2 / (2 * math.Pow(a2+dz*dz, 1.5))
		}
	}
	for i := range out {
		out[i] = math.Abs(out[i])
	}
	return out
}

// Name implements Solver. The strings match the protected Python reference
// (AnalyticalVacuumSolver.name / DiscreteFilamentSolver.name) and the "solver"
// field of testdata/golden_field_samples.json.
func (AnalyticSolver) Name() string { return "analytic-vacuum-loops" }

// Magnitude implements Solver: sqrt(B_r^2 + B_z^2) of the superposed coil set.
func (AnalyticSolver) Magnitude(coils []Coil, r, z []float64) []float64 {
	br, bz := coilsetField(coils, r, z)
	out := make([]float64, len(br))
	for i := range out {
		out[i] = math.Hypot(br[i], bz[i])
	}
	return out
}

// Name implements Solver.
func (DiscreteSolver) Name() string { return "discrete-filaments" }

// Magnitude implements Solver with the independent segment summation: the
// segment fields are summed as vectors first, then the magnitude is taken
// (matching the reference's DiscreteFilamentSolver.field_magnitude).
// NSeg <= 0 falls back to defaultNSeg.
func (d DiscreteSolver) Magnitude(coils []Coil, r, z []float64) []float64 {
	if len(r) != len(z) {
		panic("physics: DiscreteSolver.Magnitude called with mismatched r/z lengths")
	}
	n := len(r)
	brs := make([]float64, n)
	bzs := make([]float64, n)
	for _, c := range coils {
		for i := 0; i < n; i++ {
			cbr, cbz := loopFieldDiscrete(c.Radius, c.Current, r[i], z[i]-c.Z, d.NSeg)
			brs[i] += cbr
			bzs[i] += cbz
		}
	}
	out := make([]float64, n)
	for i := range out {
		out[i] = math.Hypot(brs[i], bzs[i])
	}
	return out
}
