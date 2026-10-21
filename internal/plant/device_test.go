package plant

import (
	"testing"
	"time"

	"github.com/logos-42/hushfusion-forge/internal/contracts"
)

var base = time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)

func spec() Spec {
	return Spec{
		DeviceID: "dev-1",
		Actuators: []ActuatorSpec{
			{Name: "coil", Initial: 0, Gain: 1},
			{Name: "valve", Initial: 0, Gain: 2},
		},
		Sensors: []SensorSpec{
			{Name: "field", Unit: "T", Offset: 0.5},
		},
	}
}

func TestNewDeviceStartsInStandbyWithMeasurements(t *testing.T) {
	d := New(spec(), base)
	s := d.State()
	if s.Mode != contracts.ModeStandby {
		t.Fatalf("mode = %q, want standby", s.Mode)
	}
	if s.Kind != contracts.DeviceSimulated {
		t.Fatalf("kind = %q, want simulated", s.Kind)
	}
	if got := s.Measurements["field"]; got != 0.5 {
		t.Fatalf("field = %v, want 0.5（偏置）", got)
	}
}

func TestApplySetActuatorUpdatesStateAndSequence(t *testing.T) {
	d := New(spec(), base)
	cmd := contracts.ControlCommand{
		ContractVersion: contracts.ContractVersion,
		CommandID:       "c1",
		DeviceID:        "dev-1",
		Kind:            contracts.CmdSetActuator,
		Targets:         []contracts.ActuatorTarget{{Actuator: "coil", Value: 2}},
		Sequence:        1,
		IssuedAt:        base.Format(time.RFC3339Nano),
	}
	after, err := d.Apply(cmd, base)
	if err != nil {
		t.Fatal(err)
	}
	if after.Actuators["coil"] != 2 {
		t.Fatalf("coil = %v, want 2", after.Actuators["coil"])
	}
	if after.Sequence != 1 {
		t.Fatalf("sequence = %d, want 1", after.Sequence)
	}
	if after.Mode != contracts.ModeRunning {
		t.Fatalf("mode = %q, want running", after.Mode)
	}
	// 占位模型: 总增益 = 2*1 + 0*2 = 2, 传感器 = 2 + 0.5
	if got := after.Measurements["field"]; got != 2.5 {
		t.Fatalf("field = %v, want 2.5", got)
	}
}

func TestApplySafeStopEntersSafeStopMode(t *testing.T) {
	d := New(spec(), base)
	cmd := contracts.ControlCommand{
		ContractVersion: contracts.ContractVersion,
		CommandID:       "c2",
		DeviceID:        "dev-1",
		Kind:            contracts.CmdSafeStop,
		Sequence:        1,
		IssuedAt:        base.Format(time.RFC3339Nano),
	}
	after, err := d.Apply(cmd, base)
	if err != nil {
		t.Fatal(err)
	}
	if after.Mode != contracts.ModeSafeStop {
		t.Fatalf("mode = %q, want safe_stop", after.Mode)
	}
}

func TestApplyRejectsActionInSafeStopMode(t *testing.T) {
	d := New(spec(), base)
	stop := contracts.ControlCommand{
		ContractVersion: contracts.ContractVersion,
		CommandID:       "c1",
		DeviceID:        "dev-1",
		Kind:            contracts.CmdSafeStop,
		Sequence:        1,
		IssuedAt:        base.Format(time.RFC3339Nano),
	}
	if _, err := d.Apply(stop, base); err != nil {
		t.Fatal(err)
	}
	act := contracts.ControlCommand{
		ContractVersion: contracts.ContractVersion,
		CommandID:       "c2",
		DeviceID:        "dev-1",
		Kind:            contracts.CmdSetActuator,
		Targets:         []contracts.ActuatorTarget{{Actuator: "coil", Value: 1}},
		Sequence:        2,
		IssuedAt:        base.Format(time.RFC3339Nano),
	}
	if _, err := d.Apply(act, base); err == nil {
		t.Fatal("safe_stop 模式下动作类命令必须被拒绝")
	}
	// 拒绝必须**不改状态**（fail-closed 的另一半）。
	if d.State().Mode != contracts.ModeSafeStop {
		t.Fatalf("mode = %q, want safe_stop（被拒的命令不得改状态）", d.State().Mode)
	}
	if d.State().Sequence != 1 {
		t.Fatalf("sequence = %d, want 1（被拒的命令不得推进序号）", d.State().Sequence)
	}
}

func TestApplyRejectsUnknownActuator(t *testing.T) {
	d := New(spec(), base)
	cmd := contracts.ControlCommand{
		ContractVersion: contracts.ContractVersion,
		CommandID:       "c1",
		DeviceID:        "dev-1",
		Kind:            contracts.CmdSetActuator,
		Targets:         []contracts.ActuatorTarget{{Actuator: "nope", Value: 1}},
		Sequence:        1,
		IssuedAt:        base.Format(time.RFC3339Nano),
	}
	if _, err := d.Apply(cmd, base); err == nil {
		t.Fatal("未知执行器必须被拒绝")
	}
}

func TestApplyIsDeterministic(t *testing.T) {
	run := func() contracts.DeviceState {
		d := New(spec(), base)
		cmd := contracts.ControlCommand{
			ContractVersion: contracts.ContractVersion,
			CommandID:       "c1",
			DeviceID:        "dev-1",
			Kind:            contracts.CmdSetActuator,
			Targets:         []contracts.ActuatorTarget{{Actuator: "valve", Value: 3}},
			Sequence:        1,
			IssuedAt:        base.Format(time.RFC3339Nano),
		}
		s, err := d.Apply(cmd, base)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	a, b := run(), run()
	if a.Sequence != b.Sequence || a.Measurements["field"] != b.Measurements["field"] {
		t.Fatalf("两次运行结果不同: %+v vs %+v", a, b)
	}
}
