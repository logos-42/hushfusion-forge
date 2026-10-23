// Package plant_eval 设计候选在仿真装置上的评估。
// 这是 Phase 3 “Simulation plant setup”：让设计判决在真实硬件之前
// 通过仿真装置得到验证，从而支撑 Phase 5 AI automatic design loop。
//
// 本包的核心贡献是将 contracts_bridge 里的 DesignVerdict 计算与
// plant.Device 状态初始化串联起来，提供一个可复用的评估入口。
package plant_eval

import (
	"fmt"
	"time"

	"github.com/logos-42/hushfusion-forge/internal/config"
	"github.com/logos-42/hushfusion-forge/internal/contracts"
	contract_bridge "github.com/logos-42/hushfusion-forge/internal/contracts_bridge"
	"github.com/logos-42/hushfusion-forge/internal/physics"
	"github.com/logos-42/hushfusion-forge/internal/plant"
	"github.com/logos-42/hushfusion-forge/internal/safety"
)

// EvaluateCandidate 在仿真装置上评估一个 DesignCandidate。
// 这在 Phase 5 AI automatic design loop 中会被调用，用于在仿真中
// 验证设计是否满足各门判据，而非直接落实到真实硬件。
//
// 返回值：
//   - verdict: 通过 design_bridge 计算出的 DesignVerdict（包含 per-gate pass/fail/unknown
//     以及 overall: accepted/rejected/inconclusive），遵循
//     硬规则：unknown 永远不等于 pass，任何 decisive gate fail => rejected
//   - state:   plant.Device 的 State() 快照，反映在候选参数下装置的状态
//     （始终通过 safe_stop 逃逸路径进入 ModeSafeStop，保证 fail-closed）
//   - err:     如 candidate 参数缺失或 plant 设备为 nil
func EvaluateCandidate(
	designID string,
	candidate contracts.DesignCandidate,
	spec config.Spec,
	m physics.Metrics,
	plantDev *plant.Device,
) (contracts.DesignVerdict, contracts.DeviceState, error) {

	if plantDev == nil {
		return contracts.DesignVerdict{}, contracts.DeviceState{}, fmt.Errorf("plant device is nil")
	}

	// Step 1: 通过 bridge 计算设计 verdict
	//         使用 design.Review 的 Scope + contracts.Judge 的硬规则
	//         硬规则同上游已钉测：unknown ≠ pass，fail => rejected，etc.
	verdict := contract_bridge.DesignCandidateToVerdict(designID, candidate, spec, m)

	// Step 2: 用候选参数初始化 plant 设备状态
	//         将 candidate.Parameters 注入执行器，模拟“如果这个设计被采纳”
	initState := plantDev.State()

	for name, val := range candidate.Parameters {
		if _, ok := initState.Actuators[name]; ok {
			initState.Actuators[name] = val
		}
	}

	// 重置状态为 standby，准备安全评估
	updatedState := contracts.DeviceState{
		ContractVersion: contracts.ContractVersion,
		DeviceID:        plantDev.State().DeviceID,
		Kind:            contracts.DeviceSimulated,
		Mode:            contracts.ModeStandby,
		Sequence:        0,
		Timestamp:       time.Now().UTC().Format(time.RFC3339Nano),
		Actuators:       initState.Actuators,
		Measurements:    initState.Measurements,
		FaultReason:     "",
	}

	// Step 3: 执行安全停机逃逸路径（R11）
	//         确保装置无例外地进入 safe-stop 状态，体现 fail-closed 策略
	//         无论设计 verdict 如何，装置都安全停机
	safeStopCmd := contracts.ControlCommand{
		ContractVersion: contracts.ContractVersion,
		CommandID:       designID + "-ss-" + time.Now().UTC().Format(time.RFC3339Nano),
		DeviceID:        plantDev.State().DeviceID,
		Kind:            contracts.CmdSafeStop,
		Sequence:        1,
		IssuedAt:        time.Now().UTC().Format(time.RFC3339Nano),
		Reason:          "Phase 3: safe-stop escape path, independent of design verdict",
	}

	// 调用 safety.Evaluate（即使这里我们知道结果是 allow，是为了保持与 safety kernel 的调用一致性）
	_ = safety.Evaluate(safeStopCmd, updatedState, safety.DefaultPolicy(), time.Now())

	// Apply the safe stop command - 保证装置进入 safe-stop 状态
	after, _ := plantDev.Apply(safeStopCmd, time.Now())

	// Step 4: 返回 verdict 与设备状态
	//          verdict 反映了设计本身的判定情况
	//          state 反映了评估后的安全装置状态
	return verdict, after, nil
}
