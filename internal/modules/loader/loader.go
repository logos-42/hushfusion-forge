package loader

import (
	"errors"
	"fmt"
	"github.com/logos-42/hushfusion-forge/internal/modules/manifest"
	"github.com/logos-42/hushfusion-forge/internal/modules/types"
	"sort"
	"strings"
	"sync"
)

// ModuleLoader 管理模块的生命周期，支持加载、卸载、状态检查和能力退化报告
type ModuleLoader struct {
	modules       map[string]*moduleRef
	moduleTypeMap map[types.ModuleType]string // type -> moduleID mapping
	mu            sync.RWMutex
}

// moduleRef 是模块的引用计数包装
type moduleRef struct {
	id       string
	manifest manifest.Manifest
	state    string // Module state: idle, running, paused, stopping, error, unloaded
	version  string
}

// NewModuleLoader 创建一个新的模块加载器
func NewModuleLoader() *ModuleLoader {
	return &ModuleLoader{
		modules:       make(map[string]*moduleRef),
		moduleTypeMap: make(map[types.ModuleType]string),
	}
}

// LoadModule 加载一个模块
func (ml *ModuleLoader) LoadModule(id string, manifest manifest.Manifest) error {
	ml.mu.Lock()
	defer ml.mu.Unlock()

	if _, exists := ml.modules[id]; exists {
		return errors.New("module already loaded: " + id)
	}

	// 验证 manifest
	if err := validateManifest(manifest); err != nil {
		return err
	}

	// 设置默认状态
	ml.modules[id] = &moduleRef{
		id:       id,
		manifest: manifest,
		state:    "idle",
		version:  manifest.Version,
	}

	// 记录类型映射
	ml.moduleTypeMap[manifest.Identity.Type] = id

	return nil
}

// UnloadModule 卸载一个模块
func (ml *ModuleLoader) UnloadModule(id string) error {
	ml.mu.Lock()
	defer ml.mu.Unlock()

	if _, exists := ml.modules[id]; !exists {
		return errors.New("module not found: " + id)
	}

	// 检查是否有其他模块依赖
	if err := ml.checkDependencies(id); err != nil {
		return err
	}

	delete(ml.modules, id)

	// 清理类型映射
	for t, mid := range ml.moduleTypeMap {
		if mid == id {
			delete(ml.moduleTypeMap, t)
		}
	}

	return nil
}

// GetModule 获取已加载模块的引用
func (ml *ModuleLoader) GetModule(id string) (*moduleRef, bool) {
	ml.mu.RLock()
	defer ml.mu.RUnlock()

	ref, exists := ml.modules[id]
	return ref, exists
}

// GetAllModules 获取所有已加载模块
func (ml *ModuleLoader) GetAllModules() []*moduleRef {
	ml.mu.RLock()
	defer ml.mu.RUnlock()

	result := make([]*moduleRef, 0, len(ml.modules))
	for _, ref := range ml.modules {
		result = append(result, ref)
	}
	return result
}

// GetModuleByType 根据类型获取模块
func (ml *ModuleLoader) GetModuleByType(t types.ModuleType) (*moduleRef, bool) {
	ml.mu.RLock()
	defer ml.mu.RUnlock()

	id, ok := ml.moduleTypeMap[t]
	if !ok {
		return nil, false
	}

	ref, exists := ml.modules[id]
	return ref, exists
}

// ValidateModuleCapability 检查模块是否具备特定能力
func (ml *ModuleLoader) ValidateModuleCapability(id string, capName string) bool {
	ml.mu.RLock()
	defer ml.mu.RUnlock()

	ref, exists := ml.modules[id]
	if !exists {
		return false
	}

	for _, cap := range ref.manifest.Capabilities {
		if cap.Name == capName {
			return true
		}
	}
	return false
}

// GetModuleCapabilityLevel 获取模块能力等级
func (ml *ModuleLoader) GetModuleCapabilityLevel(id string, capName string) int {
	ml.mu.RLock()
	defer ml.mu.RUnlock()

	ref, exists := ml.modules[id]
	if !exists {
		return 0
	}

	for _, cap := range ref.manifest.Capabilities {
		if cap.Name == capName {
			return cap.Level
		}
	}
	return 0
}

// ReportCapabilityDegradation 报告模块能力退化
// 返回受影响的能力列表
func (ml *ModuleLoader) ReportCapabilityDegradation(id string) []string {
	ml.mu.RLock()
	defer ml.mu.RUnlock()

	ref, exists := ml.modules[id]
	if !exists {
		return nil
	}

	var degraded []string
	for _, cap := range ref.manifest.Capabilities {
		// 简单的健康状态检查：如果健康状态不是healthy，则报告能力退化
		if ref.manifest.Health.Status != "healthy" {
			// 检查是否有退化条件
			degraded = append(degraded, cap.Name)
		}
	}
	return degraded
}

// validateManifest 验证 manifest 的完整性
func validateManifest(m manifest.Manifest) error {
	// 检查必需字段
	if m.Identity.Type == "" {
		return errors.New("module type is required")
	}
	if m.Identity.Name == "" {
		return errors.New("module name is required")
	}
	if m.Identity.ID == "" {
		return errors.New("module ID is required")
	}
	if m.Version == "" {
		return errors.New("version is required")
	}
	if len(m.Capabilities) == 0 {
		return errors.New("at least one capability is required")
	}
	if m.ProtocolVersion == "" {
		return errors.New("protocol version is required")
	}
	if m.SafetyLimits.MaxTemperature <= 0 {
		return errors.New("max temperature must be set")
	}
	if m.Lifecycle.State == "" {
		m.Lifecycle.State = "idle"
	}
	return nil
}

// checkDependencies 检查模块卸载时的依赖关系
func (ml *ModuleLoader) checkDependencies(id string) error {
	// 检查是否有其他模块依赖该模块
	for _, ref := range ml.modules {
		for _, cap := range ref.manifest.Capabilities {
			for _, reqMod := range cap.RequiredModules {
				if reqMod == id {
					return errors.New("cannot unload module: other modules depend on it: " + id)
				}
			}
		}
	}
	return nil
}

// checkVersionCompatibility 检查版本兼容性
func checkVersionCompatibility(current, required string) bool {
	// 简单的版本兼容性检查
	// 支持语义化版本号，检查主版本号是否兼容
	currentMajor := extractMajorVersion(current)
	requiredMajor := extractMajorVersion(required)

	if currentMajor < requiredMajor {
		return false // 当前版本太老，不兼容
	}
	if currentMajor > requiredMajor+1 {
		return false // 当前版本太新，可能有 breaking changes
	}
	return true
}

func extractMajorVersion(v string) int {
	parts := strings.Split(v, ".")
	if len(parts) == 0 {
		return 0
	}
	var major int
	fmt.Sscanf(parts[0], "%d", &major)
	return major
}

// ModuleInfo 是给工具/CLI 看的已加载模块摘要（moduleRef 字段不可导出）。
type ModuleInfo struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Version string `json:"version"`
	State   string `json:"state"`
}

// List 返回已加载模块的可读摘要（按 ID 排序，稳定输出）。
func (ml *ModuleLoader) List() []ModuleInfo {
	ml.mu.RLock()
	defer ml.mu.RUnlock()
	out := make([]ModuleInfo, 0, len(ml.modules))
	for id, ref := range ml.modules {
		out = append(out, ModuleInfo{
			ID:      id,
			Type:    string(ref.manifest.Identity.Type),
			Name:    ref.manifest.Identity.Name,
			Version: ref.version,
			State:   ref.state,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
