// Package contract_bridge bridges internal/design.Scope → internal/contracts types.
// This is phase 2 of the HUSHFUSION Forge AI execution plan:
// "建立统一设计判决漏斗" — Establish Unified Design Decision Funnel.
//
// Delivery:
//   - Every design candidate outputs per-gate verdicts (pass / fail / unknown)
//   - GateResult has: key, status, value, threshold, unit, detail, evidence
//   - DesignVerdict has: overall (accepted/rejected/inconclusive) + gates + decisive_keys
//   - Hard rules (Judge function): unknown never = pass, fail → rejected, etc.
package contract_bridge

import (
	"strings"

	"github.com/logos-42/hushfusion-forge/internal/contracts"
	"github.com/logos-42/hushfusion-forge/internal/design"
	"github.com/logos-42/hushfusion-forge/internal/config"
	"github.com/logos-42/hushfusion-forge/internal/physics"
)

// gateResultFromDesignGate converts a design.Gate to contracts.GateResult.
func gateResultFromDesignGate(g design.Gate) contracts.GateResult {
	var status contracts.GateStatus
	if g.Unknown {
		status = contracts.GateUnknown
	} else if g.Pass {
		status = contracts.GatePass
	} else {
		status = contracts.GateFail
	}
	return contracts.GateResult{
		Key:       g.Key,
		Status:    status,
		Value:     g.Value,
		Threshold: g.Ref,
		Detail:    strings.TrimSpace(g.Provenance),
	}
}

// ScopeToGateResults converts a design.Scope's gates into contracts.GateResult slice.
func ScopeToGateResults(s design.Scope) []contracts.GateResult {
	var results []contracts.GateResult
	gateKeys := []string{
		design.GateFieldMin,
		design.GateDeath,
		design.GateMuWindow,
		design.GateCoilLoad,
		design.GateBuildable,
		design.GateSteps,
		design.GateFC5Locked,
	}
	for _, key := range gateKeys {
		if g, ok := s.GateByKey(key); ok {
			results = append(results, gateResultFromDesignGate(g))
		} else {
			results = append(results, contracts.GateResult{
				Key:  key,
				Status: contracts.GateUnknown,
				Detail: "门未计算 (输入不足)",
			})
		}
	}
	return results
}

// ScopeToDesignVerdict converts a design.Scope into a contracts.DesignVerdict,
// using the contracts.Judge function with the decisive gate keys.
func ScopeToDesignVerdict(designID string, s design.Scope) contracts.DesignVerdict {
	gates := ScopeToGateResults(s)

	decisive := []string{
		design.GateFieldMin,
		design.GateDeath,
		design.GateMuWindow,
		design.GateCoilLoad,
		design.GateBuildable,
		// GateSteps excluded: "报值, 不判生死" (per contract §5)
	}

	v := contracts.Judge(designID, gates, decisive)
	v.DesignID = designID
	return v
}

// DesignCandidateToVerdict evaluates a DesignCandidate against spec/metrics
// and returns a full DesignVerdict. Main entry point for the funnel.
func DesignCandidateToVerdict(designID string, candidate contracts.DesignCandidate,
	spec config.Spec, m physics.Metrics) contracts.DesignVerdict {

	scope := design.Review(spec, m, design.Input{})
	return ScopeToDesignVerdict(designID, scope)
}