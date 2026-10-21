package types

// ModuleType 定义了所有受支持的模块类型
type ModuleType string

const (
	ModuleDesign              ModuleType = "design"
	ModulePhysicalModel       ModuleType = "physical_model"
	ModuleSimulation          ModuleType = "simulation"
	ModuleDiagnostic          ModuleType = "diagnostic"
	ModulePower               ModuleType = "power"
	ModuleCoil                ModuleType = "coil"
	ModuleVacuum              ModuleType = "vacuum"
	ModuleCooling             ModuleType = "cooling"
	ModuleSafety              ModuleType = "safety"
	ModuleAgent               ModuleType = "agent"
	ModuleReportsEvidence     ModuleType = "reports_evidence"
)

// ModuleTypeMap 返回模块类型的中文描述映射
var ModuleTypeMap = map[ModuleType]string{
	ModuleDesign:              "设计模块",
	ModulePhysicalModel:       "物理模型模块",
	ModuleSimulation:          "仿真模块",
	ModuleDiagnostic:          "诊断模块",
	ModulePower:               "电源模块",
	ModuleCoil:                "线圈模块",
	ModuleVacuum:              "真空模块",
	ModuleCooling:             "冷却模块",
	ModuleSafety:              "安全模块",
	ModuleAgent:               "Agent 模块",
	ModuleReportsEvidence:     "报告与证据模块",
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