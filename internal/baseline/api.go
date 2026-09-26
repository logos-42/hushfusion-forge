// Package baseline: human baselines — deliberately strong, because a weak
// baseline proves nothing.
//
// FROZEN INTERFACE (v0.1) — owner: stage B.
package baseline

import (
	"fmt"

	"github.com/logos-42/hushfusion-forge/internal/config"
	"github.com/logos-42/hushfusion-forge/internal/physics"
)

// Geom are the hand-written proportions of the textbook mirror.
type Geom struct {
	RCell              float64 // [m] central-cell loop radius
	HalfGapCell        float64 // [m] half the central-cell spacing (Helmholtz: = RCell/2)
	RThroat            float64 // [m] mirror-throat loop radius
	ZThroat            float64 // [m] |z| of the throat loops
	ThroatCurrentRatio float64 // throat current / cell current
}

// DefaultGeom is the reference geometry: r_cell=0.50, half-gap 0.25 (Helmholtz
// condition spacing = radius), r_throat=0.30, |z_throat|=1.00, ratio 3.5.
func DefaultGeom() Geom {
	return Geom{RCell: 0.50, HalfGapCell: 0.25, RThroat: 0.30, ZThroat: 1.00, ThroatCurrentRatio: 3.5}
}

// Baseline is a named human design.
type Baseline struct {
	Name   string         `json:"name"`
	Note   string         `json:"note"`
	Coils  []physics.Coil `json:"coils"`
	Cost   float64        `json:"cost_proxy"`
	Design []float64      `json:"design"`
}

// HelmholtzPair is two identical loops separated by their own radius.
func HelmholtzPair(radius, current float64) []physics.Coil {
	// Spacing = radius, centred on z = 0 (the textbook uniform-field pair).
	return []physics.Coil{
		{Radius: radius, Z: -radius / 2.0, Current: current},
		{Radius: radius, Z: radius / 2.0, Current: current},
	}
}

// TextbookMirror is the hand-designed mirror the machine is asked to beat:
// a Helmholtz-like central cell plus two mirror throats.
//
// The cell current is SOLVED (not guessed) so the midplane volume-averaged field
// equals spec.BRef exactly, using a bracketing scan over a geometric grid
// followed by bisection to ~1e-9 relative. The resulting throat current is
// ~1.62 MA, which is why config.Bounds.Current[1] is 2.5e6.
//
// Golden values live in testdata/golden_baseline.json (score -0.2905708...):
// the Go implementation must reproduce that file within 1e-6.
func TextbookMirror(spec config.Spec) (Baseline, error) {
	// The reference implementation solves against its default closed-form
	// solver; same here (see analyticSolver for why this is not a literal
	// physics.AnalyticSolver{} yet).
	solver, err := analyticSolver()
	if err != nil {
		return Baseline{}, fmt.Errorf("baseline.TextbookMirror: %w", err)
	}
	return textbookMirror(spec, solver, physics.BuildGrids(spec))
}
