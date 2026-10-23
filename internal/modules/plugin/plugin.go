// Package plugin 定义 HUSHFUSION Forge 的统一插件接口与注册表。
//
// 设计目标：
//   - 一个接口（Plugin）覆盖内置 Go 插件与外部进程插件；
//   - 插件只通过声明式 manifest + action 暴露能力，不直接碰内核状态；
//   - 注册表并发安全，卸载时调用 Close，失败不静默；
//   - 外部插件协议极简：stdin/stdout 每行一个 JSON：
//     请求 {"action":"...","params":{...}}
//     响应 {"ok":true,"result":...} / {"ok":false,"error":"..."}
package plugin

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/logos-42/hushfusion-forge/internal/modules/manifest"
)

// Host 是插件可用的宿主能力（由装配方注入；nil 表示只跑纯逻辑）。
type Host interface {
	Logf(format string, args ...any)
}

// Plugin 是所有插件必须实现的唯一接口。
type Plugin interface {
	// Manifest 返回插件声明（ID/类型/版本/能力/安全边界）。
	Manifest() manifest.Manifest
	// Actions 返回可调用的动作名（模型据此选择 action）。
	Actions() []string
	// Init 初始化（外部进程在此拉起）；必须幂等或明确报错。
	Init(ctx context.Context, host Host) error
	// Execute 执行一个动作；params 是 JSON 对象。
	Execute(ctx context.Context, action string, params map[string]any) (any, error)
	// Close 释放资源（杀进程/关管道）；可重复调用。
	Close() error
}

// Info 是给模型 / CLI 看的插件摘要。
type Info struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Version string   `json:"version"`
	Type    string   `json:"type"`
	Actions []string `json:"actions"`
	State   string   `json:"state"`
}

type entry struct {
	p     Plugin
	state string
}

// Registry 是插件注册表（并发安全）。
type Registry struct {
	mu    sync.RWMutex
	items map[string]*entry
}

func NewRegistry() *Registry {
	return &Registry{items: map[string]*entry{}}
}

// Register 注册并初始化一个插件；同名覆盖（先 Close 旧实例）。
func (r *Registry) Register(ctx context.Context, host Host, p Plugin) error {
	if p == nil {
		return errors.New("plugin: nil plugin")
	}
	id := p.Manifest().Identity.ID
	if id == "" {
		id = p.Manifest().Identity.Name
	}
	if id == "" {
		return errors.New("plugin: manifest 缺 identity.id 或 identity.name")
	}
	if err := p.Init(ctx, host); err != nil {
		return fmt.Errorf("plugin %q: init 失败: %w", id, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if old := r.items[id]; old != nil {
		_ = old.p.Close()
	}
	r.items[id] = &entry{p: p, state: "ready"}
	return nil
}

// Unload 关闭并移除插件。
func (r *Registry) Unload(id string) error {
	r.mu.Lock()
	e := r.items[id]
	delete(r.items, id)
	r.mu.Unlock()
	if e == nil {
		return fmt.Errorf("plugin: 未注册 %q", id)
	}
	if err := e.p.Close(); err != nil {
		return fmt.Errorf("plugin %q: close 失败: %w", id, err)
	}
	return nil
}

// Get 返回插件实例。
func (r *Registry) Get(id string) (Plugin, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e := r.items[id]
	if e == nil {
		return nil, false
	}
	return e.p, true
}

// List 返回已注册插件摘要（按 ID 排序）。
func (r *Registry) List() []Info {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Info, 0, len(r.items))
	for id, e := range r.items {
		m := e.p.Manifest()
		out = append(out, Info{
			ID:      id,
			Name:    m.Identity.Name,
			Version: m.Version,
			Type:    string(m.Identity.Type),
			Actions: e.p.Actions(),
			State:   e.state,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Execute 调用一个插件的动作。
func (r *Registry) Execute(ctx context.Context, id, action string, params map[string]any) (any, error) {
	p, ok := r.Get(id)
	if !ok {
		return nil, fmt.Errorf("plugin: 未注册 %q", id)
	}
	if action == "" {
		return nil, errors.New("plugin: action 不能为空")
	}
	return p.Execute(ctx, action, params)
}

// CloseAll 关闭全部插件（进程退出前调用）。
func (r *Registry) CloseAll() {
	r.mu.Lock()
	items := r.items
	r.items = map[string]*entry{}
	r.mu.Unlock()
	for _, e := range items {
		_ = e.p.Close()
	}
}

// Func 是内置 Go 插件适配器：用函数实现 Execute。
type Func struct {
	M    manifest.Manifest
	Acts []string
	Run  func(ctx context.Context, action string, params map[string]any) (any, error)
}

func (f *Func) Manifest() manifest.Manifest { return f.M }
func (f *Func) Actions() []string           { return append([]string{}, f.Acts...) }
func (f *Func) Init(context.Context, Host) error {
	if f.Run == nil {
		return errors.New("plugin: Func.Run 为 nil")
	}
	return nil
}
func (f *Func) Execute(ctx context.Context, action string, params map[string]any) (any, error) {
	return f.Run(ctx, action, params)
}
func (f *Func) Close() error { return nil }
