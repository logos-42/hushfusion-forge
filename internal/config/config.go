// Package config is the single source of truth for every physical constant,
// bound and weight used by the Forge engine.
//
// Rule of the house: no number that affects a score may be hard-coded anywhere
// else in the tree. If a number matters, it lives here together with its
// physical meaning and its provenance.
//
// FROZEN INTERFACE (v0.1). The JSON tags are the interchange format shared with
// the Python reference implementation (python/forge). Changing a tag is a
// schema migration, not an edit: it breaks the cross-language golden
// comparisons in testdata/ and must be done in both implementations at once.
package config

import "math"

// MU0 is the vacuum permeability [H/m].
const MU0 = 4.0e-7 * math.Pi

// Bounds is the search box for a single coil [SI].
type Bounds struct {
	Radius  [2]float64 `json:"radius"`  // [m]  loop radius
	Z       [2]float64 `json:"z"`       // [m]  axial position
	Current [2]float64 `json:"current"` // [A]  ampere-turns
}

// Weights are an engineering operating judgement, not physics. The raw
// (unweighted) terms are always stored alongside the score so any weighting can
// be re-derived after the fact.
type Weights struct {
	Field   float64 `json:"field"`   // log10(B_mid / B_ref)
	Mirror  float64 `json:"mirror"`  // log10(R / R_ref)
	Volume  float64 `json:"volume"`  // good-field volume fraction
	Ripple  float64 `json:"ripple"`  // non-monotonic in-cell field structure
	Cost    float64 `json:"cost"`    // ohmic cost proxy, normalised by the baseline
	Penalty float64 `json:"penalty"` // multiplier on normalised constraint violation
}

// Spec is the device under design + the evaluation window + engineering limits.
//
// Fidelity statement (v0.1): the field model is exact magnetostatics for
// circular filament currents in vacuum. There is NO plasma: no pressure, no
// diamagnetic response, no equilibrium, no finite-beta correction, no eddy
// currents, no conductor current sharing. Those are Phase-2 items (see PLAN.md).
type Spec struct {
	NCoils int    `json:"n_coils"`
	Bounds Bounds `json:"bounds"`

	BRef      float64 `json:"b_ref"`      // [T] field-term reference
	MirrorRef float64 `json:"mirror_ref"` // [-] mirror-ratio reference

	CoilFieldLimit float64 `json:"coil_field_limit"` // [T]   peak conductor field (HTS @20 K, conservative)
	JEng           float64 `json:"j_eng"`            // [A/m^2] winding-pack current density
	TPack          float64 `json:"t_pack"`           // [m]   winding-pack thickness

	RPlasma  float64 `json:"r_plasma"`   // [m] plasma radius (central cell)
	ZMid     float64 `json:"z_mid"`      // [m] half-height of the midplane sampling volume
	ZCell    float64 `json:"z_cell"`     // [m] half-length of the central cell
	ZAxisMax float64 `json:"z_axis_max"` // [m] axial extent sampled for the throat
	NAxis    int     `json:"n_axis"`     // axial sample count
	NVolR    int     `json:"n_vol_r"`    // radial sample count over the cell volume
	NVolZ    int     `json:"n_vol_z"`    // axial sample count over the cell volume

	ConfineFactor float64 `json:"confine_factor"` // |B| <= factor * B_mid counts as "good field"
	MinCoilSep    float64 `json:"min_coil_sep"`   // [m] minimum coil-centre separation

	Weights Weights `json:"weights"`
}

// DefaultSpec returns the v0.1 reference device and evaluation window.
//
// The current ceiling is set so the hand-designed reference
// (internal/baseline.TextbookMirror, throat current ~1.62 MA) is strictly
// *inside* the box. A baseline that gets clipped when re-encoded as a design
// vector would be scored as a different machine, and "machine beats human"
// would be measured against a design nobody proposed. Enforced by
// TestBaselineInsideSearchBox.
func DefaultSpec() Spec {
	return Spec{
		NCoils: 4,
		Bounds: Bounds{
			Radius:  [2]float64{0.10, 1.00},
			Z:       [2]float64{-1.20, 1.20},
			Current: [2]float64{1.0e4, 2.5e6},
		},
		BRef:           1.0,
		MirrorRef:      2.0,
		CoilFieldLimit: 12.0,
		JEng:           1.0e8,
		TPack:          0.05,
		RPlasma:        0.15,
		ZMid:           0.15,
		ZCell:          0.80,
		ZAxisMax:       1.40,
		NAxis:          161,
		NVolR:          13,
		NVolZ:          33,
		ConfineFactor:  1.25,
		MinCoilSep:     0.05,
		Weights: Weights{
			Field:   1.0,
			Mirror:  0.5,
			Volume:  0.75,
			Ripple:  0.5,
			Cost:    1.0,
			Penalty: 10.0,
		},
	}
}

// NParams is the design-vector length: [r_0..r_K, z_0..z_K, I_0..I_K].
func (s Spec) NParams() int { return 3 * s.NCoils }

// SelfField is the field on the conductor from the coil's own winding pack [T].
//
// Model: a solenoid-like pack of thickness TPack carrying uniform current
// density JEng has B ~ mu0*j*t/2 at the winding face (exact for an infinite
// slab). It is the documented anchor for "a pack at the current-density limit
// already sits in this much field", replaced by a winding-pack/FEM model in
// Phase 2.
func (s Spec) SelfField() float64 { return MU0 * s.JEng * s.TPack / 2.0 }

// Lower is the design-vector lower bound, shape (NParams).
func (s Spec) Lower() []float64 {
	out := make([]float64, 0, s.NParams())
	for i := 0; i < s.NCoils; i++ {
		out = append(out, s.Bounds.Radius[0])
	}
	for i := 0; i < s.NCoils; i++ {
		out = append(out, s.Bounds.Z[0])
	}
	for i := 0; i < s.NCoils; i++ {
		out = append(out, s.Bounds.Current[0])
	}
	return out
}

// Upper is the design-vector upper bound, shape (NParams).
func (s Spec) Upper() []float64 {
	out := make([]float64, 0, s.NParams())
	for i := 0; i < s.NCoils; i++ {
		out = append(out, s.Bounds.Radius[1])
	}
	for i := 0; i < s.NCoils; i++ {
		out = append(out, s.Bounds.Z[1])
	}
	for i := 0; i < s.NCoils; i++ {
		out = append(out, s.Bounds.Current[1])
	}
	return out
}

// AsMap renders the spec with the same keys as the Python reference
// (python/forge/config.py: Spec.as_dict), including the two derived fields.
// This is what the schema-parity gate compares.
func (s Spec) AsMap() map[string]any {
	return map[string]any{
		"n_coils":          s.NCoils,
		"bounds":           map[string]any{"radius": s.Bounds.Radius[:], "z": s.Bounds.Z[:], "current": s.Bounds.Current[:]},
		"b_ref":            s.BRef,
		"mirror_ref":       s.MirrorRef,
		"coil_field_limit": s.CoilFieldLimit,
		"j_eng":            s.JEng,
		"t_pack":           s.TPack,
		"r_plasma":         s.RPlasma,
		"z_mid":            s.ZMid,
		"z_cell":           s.ZCell,
		"z_axis_max":       s.ZAxisMax,
		"n_axis":           s.NAxis,
		"n_vol_r":          s.NVolR,
		"n_vol_z":          s.NVolZ,
		"confine_factor":   s.ConfineFactor,
		"min_coil_sep":     s.MinCoilSep,
		"weights": map[string]any{
			"field": s.Weights.Field, "mirror": s.Weights.Mirror, "volume": s.Weights.Volume,
			"ripple": s.Weights.Ripple, "cost": s.Weights.Cost, "penalty": s.Weights.Penalty,
		},
		"self_field_T": s.SelfField(),
		"n_params":     s.NParams(),
	}
}
