package types

// ModuleType 定义了所有受支持的模块类型
// 这些类型对应 Phase 6 的模块清单要求
type ModuleType string

const (
	// Design Module: 负责生成设计候选
	ModuleDesign ModuleType = "design"

	// PhysicalModel Module: 描述系统物理行为
	ModulePhysicalModel ModuleType = "physical_model"

	// Simulation Module: 运行物理仿真
	ModuleSimulation ModuleType = "simulation"

	// Diagnostic Module: 诊断系统状态
	ModuleDiagnostic ModuleType = "diagnostic"

	// Power Module: 管理电能分配
	ModulePower ModuleType = "power"

	// Coil Module: 控制电磁线圈
	ModuleCoil ModuleType = "coil"

	// Vacuum Module: 管理真空系统
	ModuleVacuum ModuleType = "vacuum"

	// Cooling Module: 管理温度控制
	ModuleCooling ModuleType = "cooling"

	// Safety Module: 安全策略和判决
	ModuleSafety ModuleType = "safety"

	// Agent Module: 多智能体协调
	ModuleAgent ModuleType = "agent"

	// ReportsEvidence Module: 生成报告和证据链
	ModuleReportsEvidence ModuleType = "reports_evidence"
)

// ModuleTypeMap 返回模块类型的中文描述映射
var ModuleTypeMap = map[ModuleType]string{
	ModuleDesign:         "设计模块",
	ModulePhysicalModel:  "物理模型模块",
	ModuleSimulation:     "仿真模块",
	ModuleDiagnostic:     "诊断模块",
	ModulePower:          "电源模块",
	ModuleCoil:           "线圈模块",
	ModuleVacuum:         "真空模块",
	ModuleCooling:        "冷却模块",
	ModuleSafety:         "安全模块",
	ModuleAgent:          "Agent 模块",
	ModuleReportsEvidence: "报告与证据模块",
}

// IsValid 检查模块类型是否有效
func IsValid(mt ModuleType) bool {
	_, ok := ModuleTypeMap[mt]
	return ok
}

// AllTypes 返回所有已注册的模块类型
func AllTypes() []ModuleType {
	types := make([]ModuleType, 0, len(ModuleTypeMap))
	for t := range ModuleTypeMap {
		types = append(types, t)
	}
	return types
}