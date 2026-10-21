package contracts

// SafetyDecisionKind 是安全判决的结果类别。
type SafetyDecisionKind string

const (
	// SafetyAllow 允许执行。
	SafetyAllow SafetyDecisionKind = "allow"
	// SafetyDeny 拒绝执行（**fail-closed 的默认结果**）。
	SafetyDeny SafetyDecisionKind = "deny"
)

// SafetyDecision 是安全内核对一条命令的判决。
//
// 它是审计的一等对象: 每一道 allow/deny 都带规则 id 与理由, 可被事后独立复核。
// 注意 deny 是**默认**值: 任何无法判决的情况都必须落到 deny, 不允许"没检查到就放行"。
type SafetyDecision struct {
	// ContractVersion 契约版本。
	ContractVersion string `json:"contract_version"`
	// Decision allow / deny。
	Decision SafetyDecisionKind `json:"decision"`
	// CommandID 被判决的命令。
	CommandID string `json:"command_id"`
	// DeviceID 目标设备。
	DeviceID string `json:"device_id"`
	// Rules 命中的规则（deny 时至少一条; allow 时列出通过的关键规则）。
	Rules []string `json:"rules,omitempty"`
	// Reason 人类可读理由。
	Reason string `json:"reason,omitempty"`
	// EvaluatedAt 判决时刻（RFC3339）。
	EvaluatedAt string `json:"evaluated_at,omitempty"`
}

// Allow 造一条 allow 判决。
func Allow(commandID, deviceID, reason string, rules ...string) SafetyDecision {
	return SafetyDecision{
		ContractVersion: ContractVersion,
		Decision:        SafetyAllow,
		CommandID:       commandID,
		DeviceID:        deviceID,
		Rules:           rules,
		Reason:          reason,
	}
}

// Deny 造一条 deny 判决。
func Deny(commandID, deviceID, reason string, rules ...string) SafetyDecision {
	return SafetyDecision{
		ContractVersion: ContractVersion,
		Decision:        SafetyDeny,
		CommandID:       commandID,
		DeviceID:        deviceID,
		Rules:           rules,
		Reason:          reason,
	}
}
