// 注册表 + 别名解析（Go 版 providers/__init__.py 的精简核心）。
//
// Hermes 的注册表支持三层来源（bundled 插件 / 用户插件 / pip 入口点），
// 同名后注册覆盖先注册。本仓 v0.1 简化成两层：内置目录（Builtins）+ 运行时
// Register（用户自定义 / 测试注入），覆盖语义保留 —— 后注册者胜。
package ai

import (
	"sort"
	"strings"
	"sync"
)

// registry 是名字 → Profile 的注册表；aliases 是别名 → 名字。
// 读写都上锁：CLI 可能在请求处理中动态注册 provider。
var (
	registryMu sync.RWMutex
	registry   = map[string]*Profile{}
	aliases    = map[string]string{}
)

// Register 注册一个 provider（后注册者覆盖同名先注册者 —— 用户自定义可覆盖内置）。
// 注册时同步登记别名；别名冲突同样以后注册者胜。
func Register(p *Profile) error {
	if err := p.Validate(); err != nil {
		return err
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[p.Name] = p
	for _, a := range p.Aliases {
		aliases[strings.ToLower(a)] = p.Name
	}
	return nil
}

// RegisterBuiltins 把内置目录注册进注册表（幂等：同名再注册只是覆盖成内置值）。
func RegisterBuiltins() error {
	for _, p := range Builtins() {
		if err := Register(p); err != nil {
			return err
		}
	}
	return nil
}

// Lookup 按名字或别名找 provider（大小写不敏感）。找不到返回 nil。
// 别名解析与 Hermes 一致：先查注册表名字，再查别名映射。
func Lookup(name string) *Profile {
	if name == "" {
		return nil
	}
	n := strings.ToLower(strings.TrimSpace(name))
	registryMu.RLock()
	defer registryMu.RUnlock()
	if p, ok := registry[n]; ok {
		return p
	}
	if canonical, ok := aliases[n]; ok {
		return registry[canonical]
	}
	return nil
}

// List 返回全部已注册 provider（按注册先后；名字排序对 CLI 展示更稳）。
func List() []*Profile {
	registryMu.RLock()
	defer registryMu.RUnlock()
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	// 稳定顺序：内置在前的字典序，避免每次调用顺序漂移。
	sortStrings(names)
	out := make([]*Profile, 0, len(names))
	for _, n := range names {
		out = append(out, registry[n])
	}
	return out
}

// sortStrings 就地字典序排序。
func sortStrings(s []string) { sort.Strings(s) }
