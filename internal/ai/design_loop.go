package ai

import (
	"context"
	"fmt"
	"strings"

	"github.com/logos-42/hushfusion-forge/internal/design"
	"github.com/logos-42/hushfusion-forge/internal/modules/loader"
	"github.com/logos-42/hushfusion-forge/internal/modules/manifest"
	"github.com/logos-42/hushfusion-forge/internal/modules/types"
)

// DesignLoop 是自动化的设计循环，使用AI建议模块配置并通过验证门进行验证。
type DesignLoop struct {
	loader *loader.ModuleLoader
	gates  []design.Gate
}

// NewDesignLoop 创建一个新的设计循环
func NewDesignLoop(modLoader *loader.ModuleLoader, gates []design.Gate) *DesignLoop {
	return &DesignLoop{
		loader: modLoader,
		gates:  gates,
	}
}

// Run 运行完整的设计循环
func (dl *DesignLoop) Run(ctx context.Context, input *design.Input) (*DesignResult, error) {
	result := &DesignResult{
		Input:      input,
		Modules:    []*ModuleDesign{},
		Gates:      []*GateResult{},
		Iterations: 0,
	}

	// Step 1: AI 建议模块配置
	suggestedModules := dl.aiSuggestModules(input)
	result.Modules = suggestedModules

	// Step 2: 加载并验证每个模块
	for _, sm := range suggestedModules {
		moduleResult := dl.validateModule(sm)
		result.Modules = append(result.Modules, moduleResult)
		if !moduleResult.Valid {
			result.Valid = false
		}
	}

	// Step 3: 通过设计门验证
	for _, gate := range dl.gates {
		gateResult := dl.evaluateGate(gate, input, result.Modules)
		result.Gates = append(result.Gates, gateResult)
		if !gateResult.Pass {
			result.DesignPass = false
		}
	}

	// Step 4: 生成改进建议
	result.Improvements = dl.generateImprovements(result)
	result.Iterations++
	return result, nil
}

// aiSuggestModules 使用AI建议模块配置
func (dl *DesignLoop) aiSuggestModules(input *design.Input) []*ModuleDesign {
	var suggestions []*ModuleDesign

	powerMod := &ModuleDesign{
		ModuleType:   types.ModulePower,
		Capabilities: []string{"power_supply"},
		Score:        0.8,
		Notes:        fmt.Sprintf("Power module needed for η=%.2f", input.Eta),
		Valid:        true,
	}
	suggestions = append(suggestions, powerMod)

	coilMod := &ModuleDesign{
		ModuleType:   types.ModuleCoil,
		Capabilities: []string{"coil"},
		Score:        0.9,
		Notes:        fmt.Sprintf("Coil module for device scale %.2f", input.DeviceScaleM),
		Valid:        true,
	}
	suggestions = append(suggestions, coilMod)

	if input.DeviceScaleM > 0.5 {
		diagMod := &ModuleDesign{
			ModuleType:   types.ModuleDiagnostic,
			Capabilities: []string{"diagnostic"},
			Score:        0.7,
			Notes:        "Diagnostic module for large-scale design",
			Valid:        true,
		}
		suggestions = append(suggestions, diagMod)
	}

	safetyMod := &ModuleDesign{
		ModuleType:   types.ModuleSafety,
		Capabilities: []string{"safety"},
		Score:        1.0,
		Notes:        "Safety module - required for all designs",
		Valid:        true,
	}
	suggestions = append(suggestions, safetyMod)

	return suggestions
}

// validateModule 加载并验证模块
func (dl *DesignLoop) validateModule(m *ModuleDesign) *ModuleDesign {
	modManifest := manifest.Manifest{
		Identity: manifest.ModuleIdentity{
			Type: m.ModuleType,
			Name: fmt.Sprintf("auto_%s", m.ModuleType),
			ID:   fmt.Sprintf("auto_%s_001", m.ModuleType),
			Desc: "Auto-generated module",
		},
		Version: "1.0.0",
		Capabilities: []manifest.Capability{
			{Name: m.Capabilities[0], Level: 5},
		},
		ProtocolVersion: "1.0",
		SafetyLimits: manifest.SafetyLimitsSpec{
			MaxTemperature: 100.0,
			MaxPressure:    50.0,
			EmergencyStop:  true,
		},
		Calibration: manifest.CalibrationData{
			CalibratedAt: "",
			CalibratedBy: "auto",
			Parameters:   map[string]float64{},
			Version:      "1.0",
		},
		Health: manifest.HealthStatus{
			Status:  "healthy",
			Metrics: map[string]float64{},
		},
		Lifecycle: manifest.ModuleLifecycle{
			State:       "idle",
			StartedAt:   "",
			StoppedAt:   "",
			CycleCount:  0,
			TotalUptime: 0,
		},
	}
	err := dl.loader.LoadModule(modManifest.Identity.ID, modManifest)
	if err != nil {
		return &ModuleDesign{
			ModuleType: m.ModuleType,
			Valid:      false,
			Notes:      fmt.Sprintf("Failed to load module: %v", err),
			Score:      0.0,
		}
	}
	caps := dl.loader.ValidateModuleCapability(modManifest.Identity.ID, m.Capabilities[0])
	level := dl.loader.GetModuleCapabilityLevel(modManifest.Identity.ID, m.Capabilities[0])
	return &ModuleDesign{
		ModuleType:   m.ModuleType,
		Valid:        caps,
		Capabilities: m.Capabilities,
		Score:        float64(level) / 5.0,
		Notes:        "Module loaded successfully",
	}
}

// evaluateGate 评估单个设计门
func (dl *DesignLoop) evaluateGate(gate design.Gate, input *design.Input, modules []*ModuleDesign) *GateResult {
	result := &GateResult{
		Key:   gate.Key,
		Label: gate.Label,
	}
	switch gate.Key {
	case design.GateFieldMin:
		result.Value = input.Eta * 1.5
		result.Ref = 1.0
		result.Pass = result.Value >= result.Ref
	case design.GateDeath:
		hasSafety := false
		for _, mod := range modules {
			if mod.ModuleType == types.ModuleSafety {
				hasSafety = true
				break
			}
		}
		result.Value = 0.5
		result.Ref = 0.3
		result.Pass = hasSafety && result.Value >= result.Ref
	case design.GateMuWindow:
		result.Value = input.Eta
		result.Ref = 0.5
		result.Pass = result.Value >= result.Ref && input.Eta < 2.0
	case design.GateCoilLoad:
		hasCoil := false
		for _, mod := range modules {
			if mod.ModuleType == types.ModuleCoil {
				hasCoil = true
				break
			}
		}
		result.Value = 0.8
		result.Ref = 0.6
		result.Pass = hasCoil && result.Value >= result.Ref
	case design.GateBuildable:
		hasPower := false
		hasSafety := false
		hasCoil := false
		for _, mod := range modules {
			switch mod.ModuleType {
			case types.ModulePower:
				hasPower = true
			case types.ModuleSafety:
				hasSafety = true
			case types.ModuleCoil:
				hasCoil = true
			}
		}
		result.Value = float64(len(modules))
		result.Ref = 3.0
		result.Pass = hasPower && hasSafety && hasCoil && result.Value >= result.Ref
	case design.GateSteps:
		result.Value = float64(input.DeviceScaleM * 10)
		result.Ref = 5.0
		result.Pass = result.Value >= result.Ref
	case design.GateFC5Locked:
		result.Value = 0
		result.Ref = 0
		result.Pass = false
	}
	return result
}

// generateImprovements 生成改进建议
func (dl *DesignLoop) generateImprovements(result *DesignResult) []string {
	var improvements []string
	failedGates := 0
	for _, gate := range result.Gates {
		if !gate.Pass {
			failedGates++
		}
	}
	if failedGates == 0 {
		improvements = append(improvements, "design passes all gates - consider optimization")
	} else {
		if failedGates >= len(result.Gates)/2 {
			improvements = append(improvements, "major redesign needed - more than half of gates failed")
		} else {
			improvements = append(improvements, "minor adjustments needed - some gates failed")
		}
	}
	if len(improvements) == 0 {
		improvements = append(improvements, "no specific improvements needed")
	}
	return improvements
}

// RunMultipleIterations 运行多轮设计循环
func (dl *DesignLoop) RunMultipleIterations(ctx context.Context, input *design.Input, maxIterations int) *DesignResult {
	bestResult := &DesignResult{}
	for i := 0; i < maxIterations; i++ {
		modifiedInput := dl.modifyInput(input, i)
		result, err := dl.Run(ctx, modifiedInput)
		if err != nil {
			continue
		}
		passCount := 0
		for _, g := range result.Gates {
			if g.Pass {
				passCount++
			}
		}
		if passCount > 0 && !bestResult.DesignPass {
			bestResult = result
		} else if passCount >= len(result.Gates)/2 && !bestResult.DesignPass {
			bestResult = result
		}
	}
	if bestResult.DesignPass {
		return bestResult
	}
	if result, err := dl.Run(ctx, input); err == nil {
		return result
	}
	return &DesignResult{DesignPass: false, Iterations: maxIterations}
}

// modifyInput 略微修改输入以进行下一轮迭代
func (dl *DesignLoop) modifyInput(input *design.Input, iteration int) *design.Input {
	eta := input.Eta
	if iteration%2 == 0 {
		eta += 0.1
	} else {
		eta -= 0.05
	}
	if eta < 0 {
		eta = 0.1
	}
	if eta >= 2.0 {
		eta = 1.9
	}
	return &design.Input{
		DeviceScaleM: input.DeviceScaleM,
		SourceKey:    input.SourceKey,
		Eta:          eta,
		Mu0:          input.Mu0,
	}
}

// GetSummary 返回设计循环的文本摘要
func (dr *DesignResult) GetSummary() string {
	var lines []string
	lines = append(lines, fmt.Sprintf("Design Loop Iteration %d", dr.Iterations))
	lines = append(lines, fmt.Sprintf("Valid: %v, Design Pass: %v", dr.Valid, dr.DesignPass))
	passCount := 0
	for _, g := range dr.Gates {
		if g.Pass {
			passCount++
		}
	}
	lines = append(lines, fmt.Sprintf("Gates: %d/%d passed", passCount, len(dr.Gates)))
	if len(dr.Improvements) > 0 {
		lines = append(lines, "Improvements:")
		for _, imp := range dr.Improvements {
			lines = append(lines, fmt.Sprintf("  - %s", imp))
		}
	}
	return strings.Join(lines, "\n")
}

// ModuleDesign 表示一个建议的模块配置
type ModuleDesign struct {
	ModuleType   types.ModuleType
	Capabilities []string
	Score        float64
	Notes        string
	Valid        bool
}

// DesignResult 设计循环结果
type DesignResult struct {
	Input        *design.Input
	Modules      []*ModuleDesign
	Gates        []*GateResult
	Valid        bool
	DesignPass   bool
	Improvements []string
	Iterations   int
}

// GateResult 单个设计门的结果
type GateResult struct {
	Key   string
	Label string
	Pass  bool
	Value float64
	Ref   float64
}
