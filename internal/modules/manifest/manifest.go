package manifest

import (
	"github.com/logos-42/hushfusion-forge/internal/modules/types"
)

// Manifest 是每个模块的声明文件，描述模块的身份、能力和生命周期。
// 所有模块必须实现此结构，系统在加载前会验证 manifest 的完整性。
type Manifest struct {
	Identity ModuleIdentity
	Version  string
	Capabilities  []Capability
	Inputs        []InputDescriptor
	Outputs       []OutputDescriptor
	Permissions   []Permission
	SafetyLimits  SafetyLimitsSpec
	ProtocolVersion string
	Calibration     CalibrationData
	Health          HealthStatus
	Provenance      ProvenanceInfo
	Lifecycle       ModuleLifecycle
}

// ModuleIdentity 模块身份标识
type ModuleIdentity struct {
	Type    types.ModuleType
	Name    string
	ID      string
	Desc    string
}

// Capability 模块能力描述
type Capability struct {
	Name      string
	Level     int
	Desc      string
	RequiredModules []string
}

// InputDescriptor 输入接口描述
type InputDescriptor struct {
	Name      string
	Type      string
	Format    string
	Required  bool
	Multiple  bool
}

// OutputDescriptor 输出接口描述
type OutputDescriptor struct {
	Name      string
	Type      string
	Format    string
	Optional  bool
	Broadcast bool
}

// Permission 模块权限声明
type Permission string

const (
	PermissionRead    Permission = "read"
	PermissionWrite   Permission = "write"
	PermissionExecute Permission = "execute"
	PermissionConfigure Permission = "configure"
	PermissionAdmin   Permission = "admin"
)

// SafetyLimitsSpec 安全边界限制规范
type SafetyLimitsSpec struct {
	MaxTemperature      float64
	MaxPressure         float64
	MaxCurrent          float64
	MaxPower            float64
	EmergencyStop       bool
	SafeState           SafeStateSpec
}

// SafeStateSpec 安全状态定义
type SafeStateSpec struct {
	Mode      string
	Timeout   int
	Action    string
}

// CalibrationData 校准数据
type CalibrationData struct {
	CalibratedAt  string
	CalibratedBy  string
	Parameters    map[string]float64
	Version       string
}

// HealthStatus 模块健康状态
type HealthStatus struct {
	Status      string
	LastCheck   string
	ErrorCount  int
	WarningCount int
	Metrics     map[string]float64
}

// ProvenanceInfo 来源溯源信息
type ProvenanceInfo struct {
	Origin      string
	CreatedAt   string
	CreatedBy   string
	VersionHistory []VersionEntry
}

type VersionEntry struct {
	Version    string
	ChangedBy  string
	ChangedAt  string
	Description string
}

// ModuleLifecycle 模块生命周期
type ModuleLifecycle struct {
	State      string
	StartedAt  string
	StoppedAt  string
	CycleCount int
	TotalUptime int64
}