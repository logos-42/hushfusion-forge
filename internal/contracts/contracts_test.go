package contracts

import (
	"encoding/json"
	"testing"
)

func TestJudgeRejectsWhenAnyDecisiveGateFails(t *testing.T) {
	gates := []GateResult{
		{Key: "field_min", Status: GatePass, Value: 1.2},
		{Key: "death", Status: GateFail, Value: 0.9},
		{Key: "steps", Status: GatePass, Value: 164},
	}
	v := Judge("D1", gates, []string{"field_min", "death"})
	if v.Overall != VerdictRejected {
		t.Fatalf("Overall = %q, want %q", v.Overall, VerdictRejected)
	}
}

func TestJudgeDoesNotLetNonDecisiveFailBlockAcceptance(t *testing.T) {
	// steps 门是"报值不判生死"的门: 它 fail 不得改总判决。
	gates := []GateResult{
		{Key: "field_min", Status: GatePass, Value: 1.2},
		{Key: "steps", Status: GateFail, Value: 0},
	}
	v := Judge("D1", gates, []string{"field_min"})
	if v.Overall != VerdictAccepted {
		t.Fatalf("Overall = %q, want %q", v.Overall, VerdictAccepted)
	}
}

func TestJudgeNeverTurnsUnknownIntoPass(t *testing.T) {
	gates := []GateResult{
		{Key: "field_min", Status: GatePass, Value: 1.2},
		{Key: "fc5_locked", Status: GateUnknown, Value: 0},
	}
	v := Judge("D1", gates, []string{"field_min", "fc5_locked"})
	if v.Overall != VerdictInconclusive {
		t.Fatalf("Overall = %q, want %q", v.Overall, VerdictInconclusive)
	}
}

func TestJudgeWithoutDecisiveGateIsInconclusive(t *testing.T) {
	v := Judge("D1", []GateResult{{Key: "steps", Status: GatePass, Value: 1}}, []string{})
	if v.Overall != VerdictInconclusive {
		t.Fatalf("Overall = %q, want %q", v.Overall, VerdictInconclusive)
	}
}

func TestJudgePrefersRejectedOverInconclusive(t *testing.T) {
	gates := []GateResult{
		{Key: "a", Status: GateUnknown},
		{Key: "b", Status: GateFail},
	}
	v := Judge("D1", gates, []string{"a", "b"})
	if v.Overall != VerdictRejected {
		t.Fatalf("Overall = %q, want %q", v.Overall, VerdictRejected)
	}
}

func TestAllContractValuesValidateAgainstSchemas(t *testing.T) {
	cases := []struct {
		name  string
		value any
	}{
		{"DesignCandidate", DesignCandidate{
			ContractVersion: ContractVersion,
			DesignID:        "D1",
			Origin:          "ai",
			Parameters:      map[string]float64{"r": 0.2},
			Units:           map[string]string{"r": "m"},
		}},
		{"DesignVerdict", Judge("D1", []GateResult{
			{Key: "field_min", Status: GatePass, Value: 1.2, Threshold: 1.0},
		}, []string{"field_min"})},
		{"DeviceState", DeviceState{
			ContractVersion: ContractVersion,
			DeviceID:        "dev-1",
			Kind:            DeviceSimulated,
			Mode:            ModeStandby,
			Sequence:        1,
			Timestamp:       "2026-10-21T00:00:00Z",
		}},
		{"ControlCommand", ControlCommand{
			ContractVersion: ContractVersion,
			CommandID:       "c1",
			DeviceID:        "dev-1",
			Kind:            CmdSetActuator,
			Targets:         []ActuatorTarget{{Actuator: "coil", Value: 1}},
			Sequence:        2,
			IssuedAt:        "2026-10-21T00:00:00Z",
		}},
		{"Experiment", Experiment{
			ContractVersion: ContractVersion,
			ExperimentID:    "E1",
			DesignID:        "D1",
			State:           ExpProposed,
		}},
		{"SafetyDecision", Allow("c1", "dev-1", "ok", "R00")},
	}
	for _, c := range cases {
		if errs := ValidateValue(c.name, c.value); len(errs) != 0 {
			t.Errorf("%s 不合 schema: %v", c.name, errs)
		}
	}
}

// TestSchemaPropertiesMatchGoFields 盯住"schema 与 Go 类型同步"这件事:
// schema 里出现一个 Go 没有的字段（或反过来）说明两侧已漂移, 交换格式会被静默破坏。
func TestSchemaPropertiesMatchGoFields(t *testing.T) {
	cases := map[string]any{
		"DesignCandidate": DesignCandidate{},
		"DesignVerdict":   DesignVerdict{},
		"DeviceState":     DeviceState{},
		"ControlCommand":  ControlCommand{},
		"Experiment":      Experiment{},
		"SafetyDecision":  SafetyDecision{},
	}
	for name, sample := range cases {
		schema, err := LoadSchema(SchemaFile[name])
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		props, ok := schema["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s: schema 没有 properties", name)
		}
		schemaFields := map[string]bool{}
		for k := range props {
			schemaFields[k] = true
		}
		goFields := map[string]bool{}
		for _, f := range GoTypeFields(sample) {
			goFields[f] = true
		}
		for f := range schemaFields {
			if !goFields[f] {
				t.Errorf("%s: schema 有字段 %q 而 Go 类型没有", name, f)
			}
		}
		for f := range goFields {
			if !schemaFields[f] {
				t.Errorf("%s: Go 类型有字段 %q 而 schema 没有", name, f)
			}
		}
	}
}

func TestSchemaRejectsUnknownFieldAndBadEnum(t *testing.T) {
	schema, err := LoadSchema(SchemaFile["DeviceState"])
	if err != nil {
		t.Fatal(err)
	}
	doc := map[string]any{
		"contract_version": ContractVersion,
		"device_id":        "d",
		"kind":             "simulated",
		"mode":             "standby",
		"sequence":         float64(1),
		"timestamp":        "2026-10-21T00:00:00Z",
		"surprise":         "x",
	}
	if errs := ValidateAgainstSchema(schema, doc); len(errs) == 0 {
		t.Fatal("额外字段必须被拒绝, 但校验器放行了")
	}
	doc2 := map[string]any{
		"contract_version": ContractVersion,
		"device_id":        "d",
		"kind":             "simulated",
		"mode":             "teleport",
		"sequence":         float64(1),
		"timestamp":        "2026-10-21T00:00:00Z",
	}
	if errs := ValidateAgainstSchema(schema, doc2); len(errs) == 0 {
		t.Fatal("非法 mode 枚举必须被拒绝, 但校验器放行了")
	}
}

func TestContractVersionConstantIsPinned(t *testing.T) {
	// 契约版本是交换格式的版本; 改动它必须是一次显式的决定, 所以这里钉住当前值。
	if ContractVersion != "0.1" {
		t.Fatalf("ContractVersion = %q, want 0.1", ContractVersion)
	}
}

func TestJSONRoundTripKeepsFields(t *testing.T) {
	cmd := ControlCommand{
		ContractVersion: ContractVersion,
		CommandID:       "c1",
		DeviceID:        "d1",
		Kind:            CmdSetActuator,
		Targets:         []ActuatorTarget{{Actuator: "coil", Value: 0.5}},
		Sequence:        3,
		IssuedAt:        "2026-10-21T00:00:00Z",
	}
	b, err := json.Marshal(cmd)
	if err != nil {
		t.Fatal(err)
	}
	var got ControlCommand
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.CommandID != cmd.CommandID || got.Sequence != cmd.Sequence ||
		len(got.Targets) != 1 || got.Targets[0].Value != 0.5 {
		t.Fatalf("往返后字段不一致: %+v", got)
	}
}
