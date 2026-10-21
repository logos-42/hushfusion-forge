// Package plant 是 HUSHFUSION Forge 的**仿真装置层**：一个没有硬件的设备，
// 用来把未来的真实设备接口先跑通。
//
// 诚实边界（重要，不要在引用本包数字时省略）：
//   - 本包的传感器/执行器模型是**占位模型**，不是物理模型。它回答
//     "命令 → 状态 → 证据"这条链路是否闭合，**不回答**"这台装置能不能聚变"；
//   - 本包**不**复用 internal/physics 的静磁学：那是设计判决的物理底座，
//     把两件事混在一处会让"仿真通过"被误读成"物理成立"；
//   - 本包是确定性的：给定同样的输入序列，输出逐位相同（可 replay）。
package plant

import (
	"fmt"
	"sort"
	"time"

	"github.com/logos-42/hushfusion-forge/internal/contracts"
)

// ActuatorSpec 描述一个执行器（占位模型）。
type ActuatorSpec struct {
	// Name 执行器名。
	Name string
	// Initial 初值。
	Initial float64
	// Gain 占位增益：读数 = Gain × 目标值（**不是**物理）。
	Gain float64
}

// SensorSpec 描述一个传感器（占位模型）。
type SensorSpec struct {
	// Name 传感器名。
	Name string
	// Unit 单位。
	Unit string
	// Offset 偏置：读数 = 目标值×总增益 + Offset。
	Offset float64
}

// Spec 是仿真装置的规格。
type Spec struct {
	// DeviceID 设备标识。
	DeviceID string
	// Actuators 执行器规格。
	Actuators []ActuatorSpec
	// Sensors 传感器规格。
	Sensors []SensorSpec
}

// Device 是仿真装置本体。
//
// 它持有状态，并且是**唯一**改写状态的地方（状态机在 Apply/Transition 里）。
type Device struct {
	spec    Spec
	state   contracts.DeviceState
	lastSeq uint64
}

// New 造一台仿真装置（初始模式 init，序号 0）。
func New(spec Spec, now time.Time) *Device {
	d := &Device{spec: spec}
	d.state = contracts.NewDeviceState(spec.DeviceID, contracts.DeviceSimulated, now)
	d.state.Mode = contracts.ModeStandby
	d.state.Actuators = map[string]float64{}
	for _, a := range spec.Actuators {
		d.state.Actuators[a.Name] = a.Initial
	}
	d.state.Measurements = map[string]float64{}
	d.refreshMeasurements()
	return d
}

// State 返回当前状态（副本）。
func (d *Device) State() contracts.DeviceState {
	s := d.state
	s.Measurements = copyMap(d.state.Measurements)
	s.Actuators = copyMap(d.state.Actuators)
	return s
}

// ActuatorNames 返回执行器名（有序）。
func (d *Device) ActuatorNames() []string {
	out := make([]string, 0, len(d.spec.Actuators))
	for _, a := range d.spec.Actuators {
		out = append(out, a.Name)
	}
	sort.Strings(out)
	return out
}

// Apply 把一条**已被安全内核允许**的命令作用到装置上, 并产出新状态。
//
// 它不自己判断命令是否合法 —— 那是 safety 包的事。这里只做状态机与占位模型更新。
// 状态机非法转换 → 返回错误且**不改状态**（fail-closed 的另一半）。
func (d *Device) Apply(cmd contracts.ControlCommand, now time.Time) (contracts.DeviceState, error) {
	next := d.state
	switch cmd.Kind {
	case contracts.CmdSafeStop:
		next.Mode = contracts.ModeSafeStop
		next.FaultReason = ""
	case contracts.CmdShutdown:
		next.Mode = contracts.ModeShutdown
		next.FaultReason = ""
	case contracts.CmdSetActuator:
		if d.state.Mode != contracts.ModeStandby && d.state.Mode != contracts.ModeRunning {
			return d.state, fmt.Errorf("设备处于 %q 模式, 不接受动作类命令", d.state.Mode)
		}
		for _, t := range cmd.Targets {
			if _, ok := d.state.Actuators[t.Actuator]; !ok {
				return d.state, fmt.Errorf("未知执行器 %q", t.Actuator)
			}
			next.Actuators[t.Actuator] = t.Value
		}
		if d.state.Mode == contracts.ModeStandby {
			next.Mode = contracts.ModeRunning
		}
	default:
		return d.state, fmt.Errorf("未知命令种类 %q", cmd.Kind)
	}
	next.Sequence = d.state.Sequence + 1
	next.Timestamp = now.UTC().Format(time.RFC3339Nano)
	next.Measurements = copyMap(d.state.Measurements)
	next.Actuators = copyMap(d.state.Actuators)
	d.state = next
	d.refreshMeasurements()
	return d.State(), nil
}

// refreshMeasurements 按占位模型更新传感器读数。
func (d *Device) refreshMeasurements() {
	total := 0.0
	for _, a := range d.spec.Actuators {
		total += d.state.Actuators[a.Name] * a.Gain
	}
	for _, s := range d.spec.Sensors {
		d.state.Measurements[s.Name] = total + s.Offset
	}
}

func copyMap(in map[string]float64) map[string]float64 {
	out := make(map[string]float64, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
