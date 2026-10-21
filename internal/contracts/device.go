package contracts

import "time"

// DeviceKind 是设备类型。
type DeviceKind string

const (
	// DeviceSimulated 仿真装置（无硬件）。
	DeviceSimulated DeviceKind = "simulated"
	// DeviceHardware 真实硬件适配器。**当前仓库没有任何硬件适配器**。
	DeviceHardware DeviceKind = "hardware"
)

// DeviceMode 是设备的运行模式（安全内核管理的状态机）。
type DeviceMode string

const (
	// ModeInit 初始化中。
	ModeInit DeviceMode = "init"
	// ModeStandby 待命（可接受命令，但不动作）。
	ModeStandby DeviceMode = "standby"
	// ModeRunning 运行中。
	ModeRunning DeviceMode = "running"
	// ModeFault 故障锁定（只接受停机类命令）。
	ModeFault DeviceMode = "fault"
	// ModeSafeStop 安全停机。
	ModeSafeStop DeviceMode = "safe_stop"
	// ModeShutdown 已关机。
	ModeShutdown DeviceMode = "shutdown"
)

// DeviceState 是一个时刻的设备状态快照。
//
// 状态是**被观测到的量**, 不是被声明的量: Timestamp 与 Sequence 一起给出,
// 使得"这条状态有多新""它是第几代"都可被下游判定（新鲜度判据的前提）。
type DeviceState struct {
	// ContractVersion 契约版本。
	ContractVersion string `json:"contract_version"`
	// DeviceID 设备标识。
	DeviceID string `json:"device_id"`
	// Kind 设备类型。
	Kind DeviceKind `json:"kind"`
	// Mode 当前模式。
	Mode DeviceMode `json:"mode"`
	// Sequence 单调递增的状态序号（每产生一个新状态 +1）。
	Sequence uint64 `json:"sequence"`
	// Timestamp 状态产生时刻（RFC3339）。
	Timestamp string `json:"timestamp"`
	// Measurements 传感器读数（键为传感器名）。
	Measurements map[string]float64 `json:"measurements,omitempty"`
	// Actuators 执行器状态（键为执行器名 → 位置/开度）。
	Actuators map[string]float64 `json:"actuators,omitempty"`
	// FaultReason 非空表示处于故障/停机的原因。
	FaultReason string `json:"fault_reason,omitempty"`
}

// NewDeviceState 造一个初态（mode=init）。
func NewDeviceState(deviceID string, kind DeviceKind, now time.Time) DeviceState {
	return DeviceState{
		ContractVersion: ContractVersion,
		DeviceID:        deviceID,
		Kind:            kind,
		Mode:            ModeInit,
		Timestamp:       now.UTC().Format(time.RFC3339Nano),
	}
}

// ActuatorTarget 是一个执行器的目标值。
type ActuatorTarget struct {
	// Actuator 执行器名。
	Actuator string `json:"actuator"`
	// Value 目标值。
	Value float64 `json:"value"`
}

// CommandKind 是控制命令的种类。
type CommandKind string

const (
	// CmdSetActuator 设定执行器目标。
	CmdSetActuator CommandKind = "set_actuator"
	// CmdSafeStop 安全停机。
	CmdSafeStop CommandKind = "safe_stop"
	// CmdShutdown 关机。
	CmdShutdown CommandKind = "shutdown"
)

// CommandKindAll 是全部命令种类的集合, 供 schema 枚举校验。
var CommandKindAll = []CommandKind{CmdSetActuator, CmdSafeStop, CmdShutdown}

// ControlCommand 是发给设备的控制命令。
//
// 它是**意图**, 不是许可: 未经 Safety Kernel 判决的 Command 不得进入设备。
// 因此命令里带 Sequence（用于去重与乱序检测）与 IssuedAt（用于新鲜度判定）,
// 而不是带"批准"字段——批准属于 SafetyDecision, 不属于命令本身。
type ControlCommand struct {
	// ContractVersion 契约版本。
	ContractVersion string `json:"contract_version"`
	// CommandID 命令唯一标识。
	CommandID string `json:"command_id"`
	// DeviceID 目标设备。
	DeviceID string `json:"device_id"`
	// Kind 命令种类。
	Kind CommandKind `json:"kind"`
	// Targets 执行器目标（Kind=set_actuator 时非空）。
	Targets []ActuatorTarget `json:"targets,omitempty"`
	// Sequence 命令序号（同一设备内单调递增）。
	Sequence uint64 `json:"sequence"`
	// IssuedAt 命令产生时刻（RFC3339）。
	IssuedAt string `json:"issued_at"`
	// Reason 人类可读的理由（审计用）。
	Reason string `json:"reason,omitempty"`
}

// ExperimentState 是实验的生命周期状态。
//
// 取值与 os/ 决策层已有语义保持一致：active→done 只能由人/内核裁定, AI 不得自称完成。
type ExperimentState string

const (
	// ExpProposed 已提出, 尚未开始。
	ExpProposed ExperimentState = "proposed"
	// ExpRunning 进行中。
	ExpRunning ExperimentState = "running"
	// ExpBlocked 被阻塞（缺输入/缺权限/被安全内核拒绝）。
	ExpBlocked ExperimentState = "blocked"
	// ExpDone 完成（**只由人/内核可置**）。
	ExpDone ExperimentState = "done"
	// ExpFailed 失败。
	ExpFailed ExperimentState = "failed"
	// ExpAbandoned 放弃。
	ExpAbandoned ExperimentState = "abandoned"
)

// Experiment 是一个实验的契约对象。
type Experiment struct {
	// ContractVersion 契约版本。
	ContractVersion string `json:"contract_version"`
	// ExperimentID 实验标识。
	ExperimentID string `json:"experiment_id"`
	// DesignID 被实验的设计。
	DesignID string `json:"design_id"`
	// State 生命周期状态。
	State ExperimentState `json:"state"`
	// Hypothesis 这个实验要检验的假设（一句话）。
	Hypothesis string `json:"hypothesis,omitempty"`
	// Gates 该实验要过的门（key 集合）。
	Gates []string `json:"gates,omitempty"`
	// Evidence 该实验产出的证据。
	Evidence []EvidenceRef `json:"evidence,omitempty"`
	// Notes 备注 / 失败原因。
	Notes []string `json:"notes,omitempty"`
}
