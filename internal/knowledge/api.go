// Package knowledge: turn a registry into design rules, with their evidence.
//
// FROZEN INTERFACE (v0.1) — owner: stage E.
//
// A rule is a *replicated, direction-bearing, quantified* statement about the
// design space — not a hunch and not a fitted model. The mining procedure is
// deliberately conservative because the whole point of the knowledge base is
// that the next design round should be able to trust it:
//
//  1. every (parameter, score-term) pair gets a Spearman rank correlation
//     computed PER RUN (algorithm x seed), i.e. on independent samples;
//  2. a candidate rule must have the SAME SIGN in every run — that is the
//     replication test, and the reported confidence is the fraction of runs that
//     agree;
//  3. direction is restated with a decile contrast: median of the term for
//     designs in the top decile of the parameter vs the bottom decile, so the
//     rule carries a magnitude and not just a sign;
//  4. the scope is written down (how many designs, which search box, which
//     physics model) because a rule mined in a vacuum-field box is a hypothesis
//     about that box, not a law of nature.
package knowledge

import (
	"github.com/logos-42/hushfusion-forge/internal/config"
	"github.com/logos-42/hushfusion-forge/internal/registry"
)

// MineOpts are the mining thresholds (defaults: 150, 0.20, 12).
type MineOpts struct {
	MinN      int
	MinAbsRho float64
	TopK      int
}

// Rule is one mined design rule. JSON keys are FROZEN (shared with the Python
// auxiliary layer, python/aux/analyze.py).
type Rule struct {
	RuleID        string  `json:"rule_id"`
	Parameter     string  `json:"parameter"`
	Term          string  `json:"term"`
	Rho           float64 `json:"rho"`
	SignAgreement float64 `json:"sign_agreement"`
	NDesigns      int     `json:"n_designs"`
	NRuns         int     `json:"n_runs"`
	DecileLow     float64 `json:"decile_low"`
	DecileHigh    float64 `json:"decile_high"`
	Statement     string  `json:"statement"`
	StatementEN   string  `json:"statement_en"`
	Scope         string  `json:"scope"`
}

// ParameterNames are the design-vector names: r_0..r_{K-1}, z_0.., I_0...
func ParameterNames(spec config.Spec) []string { panic("TODO(stage E): implement parameter names") }

// Spearman is the rank correlation coefficient with tie handling (average
// ranks). Must reproduce scipy.stats.spearmanr to < 1e-9 on the golden vectors
// used by the tests — implement rank transform + Pearson on ranks.
func Spearman(x, y []float64) float64 { panic("TODO(stage E): implement Spearman correlation") }

// MineRules mines replicated rules from registry records.
//
// Only feasible records count; runs with fewer than max(20, MinN/8) records are
// dropped; at least 2 runs must survive. A candidate keeps its rule only if the
// Spearman sign is identical in every surviving run and the worst-case |rho| is
// at least MinAbsRho; rules are ranked by |rho| and the top TopK are returned.
func MineRules(recs []registry.Record, spec config.Spec, opt MineOpts) []Rule {
	panic("TODO(stage E): implement rule mining")
}

// WriteRulesMD writes the human-readable knowledge base with the rules table,
// the scope note and a machine-readable JSON block.
func WriteRulesMD(rules []Rule, path string, spec config.Spec, nRecords int, tag string) error {
	panic("TODO(stage E): implement rules markdown writer")
}

// LoadRules reads rules back out of the embedded ```json block.
func LoadRules(path string) ([]Rule, error) { panic("TODO(stage E): implement rules loading") }

// RuleExpectation is a crude linear prior from the rules: for each rule, its
// rho times the parameter's normalised position away from the box centre,
// averaged over rules. Phase 1 uses it to bias proposals; it is intentionally
// simple so its contribution is measurable (and removable) in an ablation.
func RuleExpectation(rules []Rule, x []float64, spec config.Spec) float64 {
	panic("TODO(stage E): implement rule expectation")
}
