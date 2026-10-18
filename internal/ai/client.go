// 高层的「按名字/别名找 provider 并对话」入口 —— 供 CLI 与未来的 OS API 复用。
// 这是 Hermes runtime_provider.py 的 Go 对应：resolve provider → resolve key → call。
package ai

import (
	"context"
	"fmt"
)

// Resolve 按名字/别名找 provider；找不到报错（fail-closed：不静默回退到别的 provider）。
func Resolve(name string) (*Profile, error) {
	if name == "" {
		return nil, fmt.Errorf("ai: provider 名为空（可用: %v）", ProviderNames())
	}
	p := Lookup(name)
	if p == nil {
		return nil, fmt.Errorf("ai: 未知 provider %q（可用: %v）", name, ProviderNames())
	}
	return p, nil
}

// ProviderNames 返回全部已注册 provider 名字（错误消息用）。
func ProviderNames() []string {
	ps := List()
	names := make([]string, 0, len(ps))
	for _, p := range ps {
		names = append(names, p.Name)
	}
	return names
}

// Chat 是对外主入口：按名字解析 provider，构造客户端，发一次对话。
// opts 允许覆盖 BaseURL / APIKey（custom 代理、内部网关场景）。
func Chat(ctx context.Context, providerName string, req ChatRequest, opts ClientOptions) (*ChatResponse, error) {
	p, err := Resolve(providerName)
	if err != nil {
		return nil, err
	}
	c, err := NewClient(p, opts)
	if err != nil {
		return nil, err
	}
	return c.Chat(ctx, req)
}

// ListModels 是对外入口：按名字解析 provider，拉模型列表。
func ListModels(ctx context.Context, providerName string, opts ClientOptions) ([]string, error) {
	p, err := Resolve(providerName)
	if err != nil {
		return nil, err
	}
	c, err := NewClient(p, opts)
	if err != nil {
		return nil, err
	}
	return c.ListModels(ctx)
}
