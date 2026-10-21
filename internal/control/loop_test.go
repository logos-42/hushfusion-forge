package control

import (
	"errors"
	"testing"
	"time"

	"github.com/logos-42/hushfusion-forge/internal/contracts"
	"github.com/logos-42/hushfusion-forge/internal/plant"
	"github.com/logos-42/hushfusion-forge/internal/safety"
)

var base = time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)

func spec() plant.Spec {
	return plant.Spec{
		DeviceID: "dev-1",
		Actuators: []plant.ActuatorSpec{
			{Name: "coil", Initial: 0, Gain: 1},
		},
		Sensors: []plant.SensorSpec{
			{Name: "field", Unit: "T", Offset: 0},
		},
	}
}

func policy() safety.Policy {
	p := safety.DefaultPolicy()
	p.Limits.Actuators["coil"] = safety.ActuatorLimit{Min: 0, Max: 2, MaxRate: 1}
	return p
}

func newLoop(p controlProposer) (*Loop, *plant.Device) {
	d := plant.New(spec(), base)
	l := NewLoop(Options{
		Device:   d,
		Policy:   policy(),
		Proposer: p,
		Now:      func() time.Time { return base },
		TraceID:  "trace-1",
	})
	return l, d
}

// controlProposer 是测试里的假 AI。
type controlProposer func(contracts.DeviceState) (contracts.ControlCommand, error)

func (f controlProposer) Propose(s contracts.DeviceState) (contracts.ControlCommand, error) {
	return f(s)
}

func cmdFor(s contracts.DeviceState, actuator string, value float64, id string) contracts.ControlCommand {
	return contracts.ControlCommand{
		ContractVersion: contracts.ContractVersion,
		CommandID:       id,
		DeviceID:        s.DeviceID,
		Kind:            contracts.CmdSetActuator,
		Targets:         []contracts.ActuatorTarget{{Actuator: actuator, Value: value}},
		Sequence:        s.Sequence + 1,
		IssuedAt:        base.Format(time.RFC3339Nano),
	}
}

func TestLoopAppliesAllowedCommand(t *testing.T) {
	l, d := newLoop(func(s contracts.DeviceState) (contracts.ControlCommand, error) {
		return cmdFor(s, "coil", 1, "c1"), nil
	})
	rec, err := l.Step()
	if err != nil {
		t.Fatal(err)
	}
	if rec.Decision.Decision != contracts.SafetyAllow {
		t.Fatalf("decision = %q (%s)", rec.Decision.Decision, rec.Decision.Reason)
	}
	if !rec.Applied {
		t.Fatalf("命令未作用: %s", rec.ApplyError)
	}
	if d.State().Actuators["coil"] != 1 {
		t.Fatalf("coil = %v, want 1", d.State().Actuators["coil"])
	}
	if rec.StateAfter.Sequence != 1 {
		t.Fatalf("sequence = %d, want 1", rec.StateAfter.Sequence)
	}
}

func TestLoopRejectsOutOfBoundCommandAndKeepsDeviceUnchanged(t *testing.T) {
	l, d := newLoop(func(s contracts.DeviceState) (contracts.ControlCommand, error) {
		return cmdFor(s, "coil", 99, "c1"), nil
	})
	rec, err := l.Step()
	if err != nil {
		t.Fatal(err)
	}
	if rec.Decision.Decision != contracts.SafetyDeny {
		t.Fatalf("decision = %q, want deny", rec.Decision.Decision)
	}
	if rec.Applied {
		t.Fatal("被拒的命令不得作用到装置")
	}
	if d.State().Actuators["coil"] != 0 {
		t.Fatalf("coil = %v, want 0（装置未被改动）", d.State().Actuators["coil"])
	}
	// 被拒绝的尝试必须留痕。
	if len(l.Trace().Steps) != 1 || l.Trace().Steps[0].Decision.Decision != contracts.SafetyDeny {
		t.Fatal("被拒绝的命令必须留在证据里")
	}
}

func TestLoopFailsClosedWhenProposerIsDown(t *testing.T) {
	l, d := newLoop(func(s contracts.DeviceState) (contracts.ControlCommand, error) {
		return contracts.ControlCommand{}, errors.New("AI 掉线")
	})
	rec, err := l.Step()
	if err != nil {
		t.Fatal(err)
	}
	if rec.ProposalError == "" {
		t.Fatal("提议失败必须被记录")
	}
	if rec.Decision.Decision != contracts.SafetyAllow {
		t.Fatalf("decision = %q, want allow（安全停机走逃逸路径）", rec.Decision.Decision)
	}
	if rec.Command.Kind != contracts.CmdSafeStop {
		t.Fatalf("command kind = %q, want safe_stop", rec.Command.Kind)
	}
	if d.State().Mode != contracts.ModeSafeStop {
		t.Fatalf("mode = %q, want safe_stop", d.State().Mode)
	}
}

func TestLoopRejectsStaleState(t *testing.T) {
	// 时钟比状态新 1 s, 而状态新鲜度上限是 1 ns ⇒ 状态过期。
	now := base.Add(time.Second)
	d := plant.New(spec(), base)
	p := policy()
	p.Limits.MaxStateAge = time.Nanosecond
	l := NewLoop(Options{
		Device: d,
		Policy: p,
		Proposer: controlProposer(func(s contracts.DeviceState) (contracts.ControlCommand, error) {
			return cmdFor(s, "coil", 1, "c1"), nil
		}),
		Now:     func() time.Time { return now },
		TraceID: "trace-stale",
	})
	rec, err := l.Step()
	if err != nil {
		t.Fatal(err)
	}
	if rec.Decision.Decision != contracts.SafetyDeny {
		t.Fatalf("decision = %q, want deny", rec.Decision.Decision)
	}
	if len(rec.Decision.Rules) == 0 || rec.Decision.Rules[0] != safety.RuleStateFresh {
		t.Fatalf("rules = %v, want %v first", rec.Decision.Rules, safety.RuleStateFresh)
	}
	if rec.Applied {
		t.Fatal("过期状态下的命令不得作用")
	}
}

func TestTraceIsReproducibleAndCarriesEvidence(t *testing.T) {
	mk := func() *Trace {
		l, _ := newLoop(func(s contracts.DeviceState) (contracts.ControlCommand, error) {
			return cmdFor(s, "coil", 1, "c1"), nil
		})
		if _, err := l.Run(3); err != nil {
			t.Fatal(err)
		}
		return l.Trace()
	}
	a, b := mk(), mk()
	ha, err := a.SHA256()
	if err != nil {
		t.Fatal(err)
	}
	hb, err := b.SHA256()
	if err != nil {
		t.Fatal(err)
	}
	if ha != hb {
		t.Fatalf("同一输入序列的轨迹指纹不同: %s vs %s", ha, hb)
	}
	ev, err := a.Evidence()
	if err != nil {
		t.Fatal(err)
	}
	if ev.Kind != contracts.EvidenceTrace || ev.SHA256 != ha || ev.URI == "" {
		t.Fatalf("证据引用不完整: %+v", ev)
	}
	if errs := contracts.ValidateValue("SafetyDecision", a.Steps[0].Decision); len(errs) != 0 {
		t.Fatalf("判决不合契约 schema: %v", errs)
	}
}

func TestLoopRejectsUnknownActuatorFromAI(t *testing.T) {
	l, d := newLoop(func(s contracts.DeviceState) (contracts.ControlCommand, error) {
		return cmdFor(s, "hallucinated", 1, "c1"), nil
	})
	rec, err := l.Step()
	if err != nil {
		t.Fatal(err)
	}
	if rec.Decision.Decision != contracts.SafetyDeny {
		t.Fatalf("decision = %q, want deny", rec.Decision.Decision)
	}
	if d.State().Sequence != 0 {
		t.Fatalf("sequence = %d, want 0", d.State().Sequence)
	}
}
