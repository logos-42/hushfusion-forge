// Package control 是 HUSHFUSION Forge 的**控制闭环**：把传感器状态、AI 提议、
// 安全判决、执行器作用、证据生成串成一条可重放的链路。
//
// 闭环的形状（与 docs/architecture-v1.md §4 一致）：
//
//	sensor state → AI proposal → Safety Kernel → actuator → plant update → evidence
//
// 三条纪律（由代码与单测强制，不靠约定）：
//
//  1. **AI 只能提议**。AI 不接触执行器, 也不决定命令是否安全; 它只产出
//     contracts.ControlCommand 意图。
//  2. **未经判决的命令不进设备**。每条命令都必须先过 safety.Evaluate, 只有
//     allow 才被 apply; deny 一律记录进证据（**被拒绝也留痕**）。
//  3. **AI 掉线装置仍能安全停机**。提议失败时闭环自己合成一条 safe_stop 命令,
//     它走安全内核的逃逸路径, 因此断网/断电推理时装置进入安全停机而不是保持
//     上一条动作或自行其是。
package control

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/logos-42/hushfusion-forge/internal/contracts"
	"github.com/logos-42/hushfusion-forge/internal/plant"
	"github.com/logos-42/hushfusion-forge/internal/safety"
)

// Proposer 是 AI（或任何人）提出控制意图的接口。
//
// 它**只**返回意图。返回错误 = 提议不可用（网络断了 / 输出不是合法 JSON /
// 模型超时），闭环会据此走安全停机。
type Proposer interface {
	// Propose 基于当前状态提出一条命令。
	Propose(state contracts.DeviceState) (contracts.ControlCommand, error)
}

// ProposerFunc 让普通函数满足 Proposer。
type ProposerFunc func(state contracts.DeviceState) (contracts.ControlCommand, error)

// Propose 实现 Proposer。
func (f ProposerFunc) Propose(s contracts.DeviceState) (contracts.ControlCommand, error) { return f(s) }

// StepRecord 是一次闭环步的完整记录（证据的基本单元）。
type StepRecord struct {
	// Step 步号（从 1 开始）。
	Step int `json:"step"`
	// StateBefore 步前状态。
	StateBefore contracts.DeviceState `json:"state_before"`
	// Command 送进安全内核的命令。
	Command contracts.ControlCommand `json:"command"`
	// Decision 安全判决。
	Decision contracts.SafetyDecision `json:"decision"`
	// Applied 命令是否真的作用到了装置上。
	Applied bool `json:"applied"`
	// ApplyError 作用失败时的原因（为空即成功）。
	ApplyError string `json:"apply_error,omitempty"`
	// StateAfter 步后状态。
	StateAfter contracts.DeviceState `json:"state_after"`
	// ProposalError 提议失败时的原因（AI 掉线时非空）。
	ProposalError string `json:"proposal_error,omitempty"`
}

// Trace 是一次闭环运行的证据（StepRecord 的序列）。
type Trace struct {
	// ContractVersion 契约版本。
	ContractVersion string `json:"contract_version"`
	// TraceID 轨迹标识。
	TraceID string `json:"trace_id"`
	// DeviceID 设备标识。
	DeviceID string `json:"device_id"`
	// StartedAt 开始时刻（RFC3339）。
	StartedAt string `json:"started_at"`
	// Steps 逐步记录。
	Steps []StepRecord `json:"steps"`
}

// SHA256 返回轨迹内容的指纹（逐字节 over 紧凑 JSON）。
func (t *Trace) SHA256() (string, error) {
	b, err := json.Marshal(t)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// Evidence 返回这条轨迹的证据引用。
func (t *Trace) Evidence() (contracts.EvidenceRef, error) {
	sum, err := t.SHA256()
	if err != nil {
		return contracts.EvidenceRef{}, err
	}
	return contracts.EvidenceRef{
		Kind:       contracts.EvidenceTrace,
		URI:        "trace:" + t.TraceID,
		SHA256:     sum,
		ProducedAt: t.StartedAt,
	}, nil
}

// Loop 是控制闭环。
type Loop struct {
	device  *plant.Device
	policy  safety.Policy
	propos  Proposer
	trace   *Trace
	step    int
	seq     uint64
	now     func() time.Time
	traceID string
}

// Options 是闭环的构造参数。
type Options struct {
	// Device 仿真装置。
	Device *plant.Device
	// Policy 安全策略。
	Policy safety.Policy
	// Proposer 提议者。
	Proposer Proposer
	// Now 时钟（注入, 保证可重放）。为空则用 time.Now。
	Now func() time.Time
	// TraceID 轨迹标识。为空则用 device id + 起始时刻。
	TraceID string
}

// NewLoop 造一个闭环。
func NewLoop(o Options) *Loop {
	now := o.Now
	if now == nil {
		now = time.Now
	}
	traceID := o.TraceID
	if traceID == "" {
		traceID = o.Device.State().DeviceID + "-" + now().UTC().Format(time.RFC3339Nano)
	}
	return &Loop{
		device:  o.Device,
		policy:  o.Policy,
		propos:  o.Proposer,
		now:     now,
		traceID: traceID,
		trace: &Trace{
			ContractVersion: contracts.ContractVersion,
			TraceID:         traceID,
			DeviceID:        o.Device.State().DeviceID,
			StartedAt:       now().UTC().Format(time.RFC3339Nano),
		},
	}
}

// Trace 返回到目前为止的轨迹。
func (l *Loop) Trace() *Trace { return l.trace }

// Step 走一步闭环, 返回本步记录。
func (l *Loop) Step() (StepRecord, error) {
	l.step++
	before := l.device.State()
	now := l.now()

	cmd, proposalErr := l.propos.Propose(before)
	if proposalErr != nil {
		// AI 提议不可用 ⇒ 自己合成一条安全停机命令（逃逸路径）。
		l.seq++
		cmd = contracts.ControlCommand{
			ContractVersion: contracts.ContractVersion,
			CommandID:       fmt.Sprintf("%s-cmd-%d", l.traceID, l.seq),
			DeviceID:        before.DeviceID,
			Kind:            contracts.CmdSafeStop,
			Sequence:        before.Sequence + 1,
			IssuedAt:        now.UTC().Format(time.RFC3339Nano),
			Reason:          "提议不可用, 合成安全停机",
		}
	}

	decision := safety.Evaluate(cmd, before, l.policy, now)
	decision.EvaluatedAt = now.UTC().Format(time.RFC3339Nano)

	rec := StepRecord{
		Step:          l.step,
		StateBefore:   before,
		Command:       cmd,
		Decision:      decision,
		ProposalError: errString(proposalErr),
	}

	if decision.Decision == contracts.SafetyAllow {
		after, err := l.device.Apply(cmd, now)
		if err != nil {
			rec.ApplyError = err.Error()
			rec.StateAfter = l.device.State()
		} else {
			rec.Applied = true
			rec.StateAfter = after
		}
	} else {
		// 被拒绝: 不改装置, 但**留痕**（被拒绝的尝试是审计对象）。
		rec.StateAfter = l.device.State()
	}

	l.trace.Steps = append(l.trace.Steps, rec)
	return rec, nil
}

// Run 走 n 步。
func (l *Loop) Run(n int) ([]StepRecord, error) {
	out := make([]StepRecord, 0, n)
	for i := 0; i < n; i++ {
		rec, err := l.Step()
		if err != nil {
			return out, err
		}
		out = append(out, rec)
	}
	return out, nil
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
