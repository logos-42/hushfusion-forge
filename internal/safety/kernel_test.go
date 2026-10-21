package safety

import (
	"testing"
	"time"

	"github.com/logos-42/hushfusion-forge/internal/contracts"
)

var base = time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)

func state() contracts.DeviceState {
	return contracts.DeviceState{
		ContractVersion: contracts.ContractVersion,
		DeviceID:        "dev-1",
		Kind:            contracts.DeviceSimulated,
		Mode:            contracts.ModeRunning,
		Sequence:        5,
		Timestamp:       base.Format(time.RFC3339Nano),
		Actuators:       map[string]float64{"coil": 1.0},
	}
}

func cmd() contracts.ControlCommand {
	return contracts.ControlCommand{
		ContractVersion: contracts.ContractVersion,
		CommandID:       "c1",
		DeviceID:        "dev-1",
		Kind:            contracts.CmdSetActuator,
		Targets:         []contracts.ActuatorTarget{{Actuator: "coil", Value: 1.5}},
		Sequence:        6,
		IssuedAt:        base.Format(time.RFC3339Nano),
	}
}

func policy() Policy {
	p := DefaultPolicy()
	p.Limits.Actuators["coil"] = ActuatorLimit{Min: 0, Max: 2, MaxRate: 0.5}
	return p
}

func TestAllowWhenAllRulesPass(t *testing.T) {
	d := Evaluate(cmd(), state(), policy(), base)
	if d.Decision != contracts.SafetyAllow {
		t.Fatalf("decision = %q, want allow (%s: %s)", d.Decision, d.Rules, d.Reason)
	}
}

func TestDenyOnContractVersionMismatch(t *testing.T) {
	c := cmd()
	c.ContractVersion = "9.9"
	d := Evaluate(c, state(), policy(), base)
	if d.Decision != contracts.SafetyDeny {
		t.Fatalf("decision = %q, want deny", d.Decision)
	}
	if len(d.Rules) == 0 || d.Rules[0] != RuleContractVersion {
		t.Fatalf("rules = %v, want %v first", d.Rules, RuleContractVersion)
	}
}

func TestDenyOnDeviceMismatch(t *testing.T) {
	c := cmd()
	c.DeviceID = "other"
	d := Evaluate(c, state(), policy(), base)
	if d.Decision != contracts.SafetyDeny || d.Rules[0] != RuleDeviceMatch {
		t.Fatalf("decision = %q rules = %v", d.Decision, d.Rules)
	}
}

func TestDenyOnUnknownCommandKind(t *testing.T) {
	c := cmd()
	c.Kind = "teleport"
	d := Evaluate(c, state(), policy(), base)
	if d.Decision != contracts.SafetyDeny || d.Rules[0] != RuleCommandKind {
		t.Fatalf("decision = %q rules = %v", d.Decision, d.Rules)
	}
}

func TestDenyOnDisallowedMode(t *testing.T) {
	s := state()
	s.Mode = contracts.ModeFault
	d := Evaluate(cmd(), s, policy(), base)
	if d.Decision != contracts.SafetyDeny || d.Rules[0] != RuleDeviceMode {
		t.Fatalf("decision = %q rules = %v", d.Decision, d.Rules)
	}
}

func TestDenyOnStaleState(t *testing.T) {
	s := state()
	s.Timestamp = base.Add(-time.Hour).Format(time.RFC3339Nano)
	d := Evaluate(cmd(), s, policy(), base)
	if d.Decision != contracts.SafetyDeny || d.Rules[0] != RuleStateFresh {
		t.Fatalf("decision = %q rules = %v", d.Decision, d.Rules)
	}
}

func TestDenyOnStaleCommand(t *testing.T) {
	c := cmd()
	c.IssuedAt = base.Add(-time.Hour).Format(time.RFC3339Nano)
	d := Evaluate(c, state(), policy(), base)
	if d.Decision != contracts.SafetyDeny || d.Rules[0] != RuleCommandFresh {
		t.Fatalf("decision = %q rules = %v", d.Decision, d.Rules)
	}
}

func TestDenyOnNonIncreasingSequence(t *testing.T) {
	c := cmd()
	c.Sequence = 5 // 与状态序号相等 => 不是严格递增
	d := Evaluate(c, state(), policy(), base)
	if d.Decision != contracts.SafetyDeny || d.Rules[0] != RuleSequence {
		t.Fatalf("decision = %q rules = %v", d.Decision, d.Rules)
	}
}

func TestDenyOnUnknownActuator(t *testing.T) {
	c := cmd()
	c.Targets = []contracts.ActuatorTarget{{Actuator: "nope", Value: 1}}
	d := Evaluate(c, state(), policy(), base)
	if d.Decision != contracts.SafetyDeny || d.Rules[0] != RuleActuatorKnown {
		t.Fatalf("decision = %q rules = %v", d.Decision, d.Rules)
	}
}

func TestDenyOnOutOfBoundsTarget(t *testing.T) {
	c := cmd()
	c.Targets = []contracts.ActuatorTarget{{Actuator: "coil", Value: 99}}
	d := Evaluate(c, state(), policy(), base)
	if d.Decision != contracts.SafetyDeny || d.Rules[0] != RuleActuatorBounds {
		t.Fatalf("decision = %q rules = %v", d.Decision, d.Rules)
	}
}

func TestDenyOnRateLimit(t *testing.T) {
	c := cmd()
	c.Targets = []contracts.ActuatorTarget{{Actuator: "coil", Value: 1.9}} // Δ=0.9 > 0.5
	d := Evaluate(c, state(), policy(), base)
	if d.Decision != contracts.SafetyDeny || d.Rules[0] != RuleRateLimit {
		t.Fatalf("decision = %q rules = %v", d.Decision, d.Rules)
	}
}

func TestEscapePathAllowsSafeStopEvenInFaultMode(t *testing.T) {
	s := state()
	s.Mode = contracts.ModeFault
	c := cmd()
	c.Kind = contracts.CmdSafeStop
	d := Evaluate(c, s, policy(), base)
	if d.Decision != contracts.SafetyAllow {
		t.Fatalf("decision = %q, want allow（故障态必须仍能停机）", d.Decision)
	}
	if len(d.Rules) == 0 || d.Rules[0] != RuleEscapePath {
		t.Fatalf("rules = %v, want %v first", d.Rules, RuleEscapePath)
	}
}

func TestEscapePathIgnoresActuatorBounds(t *testing.T) {
	// 停机命令不带执行器目标, 限幅不应拦住它。
	c := cmd()
	c.Kind = contracts.CmdShutdown
	c.Targets = nil
	d := Evaluate(c, state(), policy(), base)
	if d.Decision != contracts.SafetyAllow {
		t.Fatalf("decision = %q, want allow", d.Decision)
	}
}

func TestDecisionIsDeterministic(t *testing.T) {
	// 同一输入必须给出同一判决（无随机、无隐藏状态）。
	a := Evaluate(cmd(), state(), policy(), base)
	b := Evaluate(cmd(), state(), policy(), base)
	if a.Decision != b.Decision || a.Reason != b.Reason {
		t.Fatalf("判决不确定: %+v vs %+v", a, b)
	}
}
