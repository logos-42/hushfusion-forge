// Package safety 是 HUSHFUSION Forge 的 **Safety Kernel**：对一条控制命令做
// 确定性判决，返回 allow / deny。
//
// 它刻意**没有**依赖：LLM、网络、Bolloon、真实时钟。
//   - 时间由调用方注入（Evaluate(cmd, state, now)），所以判决可重放、可逐位复核；
//   - 判决逻辑是纯函数式的规则集，没有随机、没有并发状态；
//   - 任何"没检查到"的情形都必须落到 deny（fail-closed）。
//
// 判据（任一不满足即 deny，规则 id 会写进判决供审计）：
//
//	R01 契约版本一致
//	R02 目标设备一致
//	R03 命令种类合法
//	R04 设备模式允许该命令（状态机）
//	R05 状态新鲜（state 不得太旧）
//	R06 命令新鲜（命令不得太旧）
//	R07 命令序号严格递增（去重 + 乱序检测）
//	R08 执行器名已知
//	R09 执行器目标在限幅内
//	R10 执行器变化率不超过上限
//	R11 安全停机/关机命令在任意模式下都允许（可逃逸路径）
//
// 诚实边界：本包**不认识任何物理量**。限幅与速率上限是**工程策略参数**
// （由调用方给出，不是本包硬编码的物理常数）；它只保证"命令不会越过配置的边界"
// 与"状态机不会进入非法转换"。它不判断"这条命令在物理上是对的"——那是设计判决层的事。
package safety

import (
	"fmt"
	"time"

	"github.com/logos-42/hushfusion-forge/internal/contracts"
)

// 规则 id（写进 SafetyDecision.Rules，便于事后独立复核）。
const (
	RuleContractVersion = "R01"
	RuleDeviceMatch     = "R02"
	RuleCommandKind     = "R03"
	RuleDeviceMode      = "R04"
	RuleStateFresh      = "R05"
	RuleCommandFresh    = "R06"
	RuleSequence        = "R07"
	RuleActuatorKnown   = "R08"
	RuleActuatorBounds  = "R09"
	RuleRateLimit       = "R10"
	RuleEscapePath      = "R11"
)

// ActuatorLimit 是一个执行器的工程限幅。
type ActuatorLimit struct {
	// Min 允许的最小目标值。
	Min float64
	// Max 允许的最大目标值。
	Max float64
	// MaxRate 单步允许的最大变化量（|Δ| ≤ MaxRate）。
	MaxRate float64
}

// Limits 是一台设备的安全限幅表。
type Limits struct {
	// Actuators 执行器名 → 限幅。
	Actuators map[string]ActuatorLimit
	// MaxStateAge 允许的最大状态年龄（超出即视为状态过期）。
	MaxStateAge time.Duration
}

// Policy 是一次判决所用的策略（限幅 + 时效 + 模式规则）。
type Policy struct {
	// Limits 限幅表。
	Limits Limits
	// MaxCommandAge 允许的最大命令年龄。
	MaxCommandAge time.Duration
	// AllowedModes 允许接受"动作类"命令的设备模式。
	//
	// 注意: 停机/关机命令**不在**这个名单里, 它们走 R11 逃逸路径 ——
	// 一个卡在 fault 的设备必须仍然能被停掉, 否则"故障锁定"会变成"锁死"。
	AllowedModes []contracts.DeviceMode
}

// DefaultPolicy 返回一份默认策略（无限幅、只看模式、时效 30 s）。
//
// 这份默认值**只用于测试与仿真**: 真实设备必须显式给出限幅。
func DefaultPolicy() Policy {
	return Policy{
		Limits: Limits{
			Actuators:   map[string]ActuatorLimit{},
			MaxStateAge: 30 * time.Second,
		},
		MaxCommandAge: 30 * time.Second,
		AllowedModes:  []contracts.DeviceMode{contracts.ModeStandby, contracts.ModeRunning},
	}
}

// Evaluate 对一条命令做判决。now 由调用方注入, 所以判决可重放。
//
// 返回值永远是**一条**判决: 没有"没检查到"这种第三种结果。
func Evaluate(cmd contracts.ControlCommand, st contracts.DeviceState, p Policy, now time.Time) contracts.SafetyDecision {
	deny := func(rule, reason string) contracts.SafetyDecision {
		return contracts.Deny(cmd.CommandID, cmd.DeviceID, reason, rule)
	}

	// R01 契约版本
	if cmd.ContractVersion != contracts.ContractVersion {
		return deny(RuleContractVersion, fmt.Sprintf(
			"命令契约版本 %q 与当前 %q 不一致", cmd.ContractVersion, contracts.ContractVersion))
	}
	if st.ContractVersion != contracts.ContractVersion {
		return deny(RuleContractVersion, fmt.Sprintf(
			"设备状态契约版本 %q 与当前 %q 不一致", st.ContractVersion, contracts.ContractVersion))
	}
	// R02 目标设备
	if cmd.DeviceID != st.DeviceID {
		return deny(RuleDeviceMatch, fmt.Sprintf(
			"命令目标设备 %q 与当前状态设备 %q 不一致", cmd.DeviceID, st.DeviceID))
	}
	// R03 命令种类
	known := false
	for _, k := range contracts.CommandKindAll {
		if cmd.Kind == k {
			known = true
			break
		}
	}
	if !known {
		return deny(RuleCommandKind, fmt.Sprintf("未知命令种类 %q", cmd.Kind))
	}

	// R11 逃逸路径: 停机/关机在任何模式下都允许。
	if cmd.Kind == contracts.CmdSafeStop || cmd.Kind == contracts.CmdShutdown {
		return contracts.Allow(cmd.CommandID, cmd.DeviceID,
			"停机类命令走逃逸路径, 不受模式与限幅限制", RuleEscapePath)
	}

	// R04 模式
	modeOK := false
	for _, m := range p.AllowedModes {
		if st.Mode == m {
			modeOK = true
			break
		}
	}
	if !modeOK {
		return deny(RuleDeviceMode, fmt.Sprintf("设备当前模式 %q 不接受动作类命令", st.Mode))
	}

	// R05 状态新鲜度
	if p.Limits.MaxStateAge > 0 {
		ts, err := time.Parse(time.RFC3339Nano, st.Timestamp)
		if err != nil {
			return deny(RuleStateFresh, fmt.Sprintf("设备状态时间戳不可解析: %v", err))
		}
		age := now.Sub(ts)
		if age > p.Limits.MaxStateAge {
			return deny(RuleStateFresh, fmt.Sprintf("设备状态已过期 %.3fs（上限 %.3fs）",
				age.Seconds(), p.Limits.MaxStateAge.Seconds()))
		}
	}

	// R06 命令新鲜度
	if p.MaxCommandAge > 0 {
		ts, err := time.Parse(time.RFC3339Nano, cmd.IssuedAt)
		if err != nil {
			return deny(RuleCommandFresh, fmt.Sprintf("命令时间戳不可解析: %v", err))
		}
		age := now.Sub(ts)
		if age > p.MaxCommandAge {
			return deny(RuleCommandFresh, fmt.Sprintf("命令已过期 %.3fs（上限 %.3fs）",
				age.Seconds(), p.MaxCommandAge.Seconds()))
		}
	}

	// R07 序号严格递增
	if cmd.Sequence <= st.Sequence {
		return deny(RuleSequence, fmt.Sprintf("命令序号 %d 未严格大于当前状态序号 %d", cmd.Sequence, st.Sequence))
	}

	// R08-R10 执行器
	if len(cmd.Targets) == 0 {
		return deny(RuleActuatorKnown, "动作类命令必须带至少一个执行器目标")
	}
	for _, t := range cmd.Targets {
		lim, known := p.Limits.Actuators[t.Actuator]
		if !known {
			return deny(RuleActuatorKnown, fmt.Sprintf("未知执行器 %q", t.Actuator))
		}
		if t.Value < lim.Min || t.Value > lim.Max {
			return deny(RuleActuatorBounds, fmt.Sprintf("执行器 %q 目标 %g 越界 [%.6g, %.6g]",
				t.Actuator, t.Value, lim.Min, lim.Max))
		}
		cur, hasCur := st.Actuators[t.Actuator]
		if hasCur && lim.MaxRate > 0 {
			delta := t.Value - cur
			if delta > lim.MaxRate || delta < -lim.MaxRate {
				return deny(RuleRateLimit, fmt.Sprintf("执行器 %q 单步变化 %g 超过速率上限 %.6g",
					t.Actuator, delta, lim.MaxRate))
			}
		}
	}

	return contracts.Allow(cmd.CommandID, cmd.DeviceID,
		"全部安全判据通过", RuleDeviceMode, RuleStateFresh, RuleCommandFresh, RuleSequence,
		RuleActuatorKnown, RuleActuatorBounds, RuleRateLimit)
}
