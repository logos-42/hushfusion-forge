// 高层的「按名字/别名找 provider 并对话」入口 —— 供 CLI 与未来的 OS API 复用。
// 这是 Hermes runtime_provider.py 的 Go 对应：resolve provider → resolve key → call。
package ai

import (
	"context"
	"fmt"
	"strings"
	"time"
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

// Chat 是对外主入口：按名字解析 provider，多 key 轮换 + 回退 provider 地发对话。
//
// 韧性（学 Hermes auxiliary_client）：某 key 失败（429/401/5xx/网络）→ 换下一个
// key / 下一个回退 provider；不可重试错误（400/取消）立即终止。所有尝试尽数失败
// 才返回汇总错误。单 key / 无回退时退化为一次直发——契约对 aios 透明不变。
func Chat(ctx context.Context, providerName string, req ChatRequest, opts ClientOptions) (*ChatResponse, error) {
	attempts, err := buildAttempts(providerName, opts)
	if err != nil {
		return nil, err
	}
	if len(attempts) == 0 {
		return nil, fmt.Errorf("ai: provider %q 无可用 API key（显式 key / Keys / EnvVars 均为空）", providerName)
	}
	// 若至少有一个 key 仍健康 → 跳过被标记死掉的 key（避免无谓重试）；
	// 若全部都已死 → 仍全部尝试，不坐视永久宕机。
	anyAlive := false
	for _, a := range attempts {
		if kh.alive(a.provider, a.key) {
			anyAlive = true
			break
		}
	}
	var lastErr error
	for _, a := range attempts {
		if !kh.alive(a.provider, a.key) && anyAlive {
			continue
		}
		c, e := NewClient(a.profile, ClientOptions{
			BaseURL: a.baseURL, APIKey: a.key, Timeout: opts.Timeout,
		})
		if e != nil {
			lastErr = e
			continue
		}
		resp, e := c.Chat(ctx, req)
		if e == nil {
			kh.revive(a.provider, a.key)
			return resp, nil
		}
		lastErr = e
		if !isRetryable(e) {
			return nil, fmt.Errorf("ai: %s 不可重试错误: %w", a.provider, e)
		}
		kh.mark(a.provider, a.key, cooldownFor(e))
		if !sleepCtx(ctx, 300*time.Millisecond) {
			return nil, fmt.Errorf("ai: 重试被 context 中断: %w", ctx.Err())
		}
	}
	return nil, fmt.Errorf("ai: provider %q 全部 key/回退均失败: %w", providerName, lastErr)
}

// attempt 是一次 (provider, key) 发送尝试。
type attempt struct {
	provider string
	profile  *Profile
	key      string
	baseURL  string
}

// buildAttempts 展开成 (provider, key) 尝试序列：主 provider 的全部 key，
// 然后其 Fallbacks 里每个 provider 的全部 key。opts.APIKey 仅对主 provider 生效。
func buildAttempts(primary string, opts ClientOptions) ([]attempt, error) {
	if primary == "" {
		return nil, fmt.Errorf("ai: provider 名为空（可用: %v）", ProviderNames())
	}
	p := Lookup(primary)
	if p == nil {
		return nil, fmt.Errorf("ai: 未知 provider %q（可用: %v）", primary, ProviderNames())
	}
	names := append([]string{primary}, p.Fallbacks...)
	var out []attempt
	for i, name := range names {
		pp := Lookup(name)
		if pp == nil {
			continue // 回退名不存在 → 容错跳过，不致命
		}
		explicit := ""
		if i == 0 {
			explicit = opts.APIKey
		}
		keys := ResolveAPIKeys(pp, explicit)
		if len(keys) == 0 {
			continue
		}
		base := strings.TrimSuffix(pp.BaseURL, "/")
		if i == 0 && opts.BaseURL != "" {
			base = strings.TrimSuffix(opts.BaseURL, "/")
		}
		for _, k := range keys {
			out = append(out, attempt{provider: name, profile: pp, key: k, baseURL: base})
		}
	}
	return out, nil
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
