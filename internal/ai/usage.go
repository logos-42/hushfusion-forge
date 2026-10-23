package ai

// usage.go —— 把 provider 上报的 token 用量变成可读的「缓存命中率 / 成本」。
//
// 三条纪律：
//  1. **只报 provider 真给了的数**。缺就是 0，并用 CacheReported / Cost 的 known
//     区分「没命中」与「没上报」——不拿 0 冒充「没有缓存」。
//  2. **价目不硬编码猜测**。默认价目表为空（Cost 返回 known=false），由调用方用
//     SetPrice 或 HUSHFUSION_AI_PRICE_{INPUT,OUTPUT,CACHED}_PER_M 注入真实价格。
//     编一个价目会产出**假成本**，比「未知」更糟（会进报表、被当决策依据）。
//  3. 缓存命中按更低的输入价计费（provider 普遍如此）；未给缓存价时按输入价算，
//     是**偏保守的高估**，并在注释里说清楚。

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"sync"
)

// Usage 是 token 用量（provider 返回；缺省为 0，不猜测）。
//
// 兼容多家形状（见 UnmarshalJSON）：
//
//	OpenAI   {prompt_tokens, completion_tokens, total_tokens,
//	          prompt_tokens_details:{cached_tokens},
//	          completion_tokens_details:{reasoning_tokens}}
//	DeepSeek {prompt_cache_hit_tokens, prompt_cache_miss_tokens, ...}
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	// CachedTokens 是命中 provider 侧 prompt cache 的输入 token 数。
	CachedTokens int `json:"cached_tokens,omitempty"`
	// ReasoningTokens 是推理模型内部思考的 token（计入 completion）。
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`
	// CacheReported 报告 provider 是否**上报过**缓存字段。
	// 区分「命中了 0 个」与「根本没上报」——两者语义完全不同。
	CacheReported bool `json:"-"`
}

// UnmarshalJSON 兼容多家 provider 的 usage 形状；认不出的字段忽略（不为未知形状报错）。
func (u *Usage) UnmarshalJSON(b []byte) error {
	var w struct {
		PromptTokens         int `json:"prompt_tokens"`
		CompletionTokens     int `json:"completion_tokens"`
		TotalTokens          int `json:"total_tokens"`
		PromptCacheHitTokens int `json:"prompt_cache_hit_tokens"`
		PromptTokensDetails  *struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
		CompletionTokensDetails *struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		} `json:"completion_tokens_details"`
	}
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	u.PromptTokens = w.PromptTokens
	u.CompletionTokens = w.CompletionTokens
	u.TotalTokens = w.TotalTokens
	// provider 没给总量时按和补齐（算术，不是猜测）
	if u.TotalTokens == 0 && (w.PromptTokens > 0 || w.CompletionTokens > 0) {
		u.TotalTokens = w.PromptTokens + w.CompletionTokens
	}
	if w.PromptCacheHitTokens > 0 {
		u.CachedTokens = w.PromptCacheHitTokens
		u.CacheReported = true
	}
	if w.PromptTokensDetails != nil {
		u.CachedTokens = w.PromptTokensDetails.CachedTokens
		u.CacheReported = true // 字段存在即视为上报（哪怕是 0）
	}
	if w.CompletionTokensDetails != nil {
		u.ReasoningTokens = w.CompletionTokensDetails.ReasoningTokens
	}
	return nil
}

// CacheHitRate 返回缓存命中率（0..1）；PromptTokens==0 时返回 0。
// 注意：CacheReported==false 时这个 0 表示「无从得知」，不是「0 命中」。
func (u Usage) CacheHitRate() float64 {
	if u.PromptTokens <= 0 {
		return 0
	}
	c := u.CachedTokens
	if c < 0 {
		c = 0
	}
	if c > u.PromptTokens {
		c = u.PromptTokens // 防御脏数据：不让命中数超过输入数
	}
	return float64(c) / float64(u.PromptTokens)
}

// Price 是每 100 万 token 的美元价格。
type Price struct {
	InputPerM  float64 `json:"input_per_m"`  // 普通输入
	OutputPerM float64 `json:"output_per_m"` // 输出
	// CachedInputPerM 是命中缓存的输入价；0 表示按 InputPerM 计（偏保守的高估）。
	CachedInputPerM float64 `json:"cached_input_per_m,omitempty"`
}

var (
	priceMu sync.RWMutex
	prices  = map[string]Price{}
)

// SetPrice 注册/覆盖某模型的价目（model 按精确名或前缀匹配，见 PriceFor）。
func SetPrice(model string, p Price) {
	priceMu.Lock()
	defer priceMu.Unlock()
	prices[strings.TrimSpace(model)] = p
}

// ClearPrices 清空价目表（测试用）。
func ClearPrices() {
	priceMu.Lock()
	defer priceMu.Unlock()
	prices = map[string]Price{}
}

// PriceFor 查模型价目：显式表（精确 → 前缀最长匹配）> env 覆盖 > 未知。
//
// env：HUSHFUSION_AI_PRICE_INPUT_PER_M / _OUTPUT_PER_M / _CACHED_PER_M
// 未设 INPUT 或 OUTPUT 视为未知（不给半个价目）。
func PriceFor(model string) (Price, bool) {
	m := strings.TrimSpace(model)
	priceMu.RLock()
	p, ok := prices[m]
	if ok {
		priceMu.RUnlock()
		return p, true
	}
	// 前缀匹配（"openai/gpt-5-2026-01" → "openai/gpt-5"）：取最长命中的前缀。
	best := ""
	for k := range prices {
		if k != "" && strings.HasPrefix(m, k) && len(k) > len(best) {
			best = k
		}
	}
	if best != "" {
		p = prices[best]
		priceMu.RUnlock()
		return p, true
	}
	priceMu.RUnlock()

	if in, ok1 := envFloat("HUSHFUSION_AI_PRICE_INPUT_PER_M"); ok1 {
		if out, ok2 := envFloat("HUSHFUSION_AI_PRICE_OUTPUT_PER_M"); ok2 {
			cp, _ := envFloat("HUSHFUSION_AI_PRICE_CACHED_PER_M")
			return Price{InputPerM: in, OutputPerM: out, CachedInputPerM: cp}, true
		}
	}
	return Price{}, false
}

func envFloat(name string) (float64, bool) {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return 0, false
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 {
		return 0, false
	}
	return f, true
}

// Cost 估算一次用量的成本（USD）。known=false 表示价目未知 —— 此时**不要**把 0
// 当成本展示（应显示「—」），否则就是在报表里造假。
func (u Usage) Cost(model string) (usd float64, known bool) {
	p, ok := PriceFor(model)
	if !ok {
		return 0, false
	}
	cached := u.CachedTokens
	if cached < 0 {
		cached = 0
	}
	if cached > u.PromptTokens {
		cached = u.PromptTokens
	}
	uncached := u.PromptTokens - cached
	cachedPrice := p.CachedInputPerM
	if cachedPrice == 0 {
		cachedPrice = p.InputPerM // 未给缓存价：按输入价算（高估，不低报）
	}
	usd = float64(uncached)/1e6*p.InputPerM +
		float64(cached)/1e6*cachedPrice +
		float64(u.CompletionTokens)/1e6*p.OutputPerM
	return usd, true
}

// UsageLedger 累加多次调用的用量（一次 agent 回合 / 一次 oneshot 运行 / 一次 TUI 会话）。
// 累加是必要的：单次响应的 usage 只是一次调用的量，成本与命中率要看整段。
type UsageLedger struct {
	Calls         int
	Prompt        int
	Completion    int
	Total         int
	Cached        int
	CacheReported bool
	Reasoning     int
}

// Add 累加一次用量。
func (l *UsageLedger) Add(u Usage) {
	l.Calls++
	l.Prompt += u.PromptTokens
	l.Completion += u.CompletionTokens
	l.Total += u.TotalTokens
	l.Cached += u.CachedTokens
	l.Reasoning += u.ReasoningTokens
	if u.CacheReported {
		l.CacheReported = true
	}
}

// Usage 返回聚合后的 usage 视图（可直接传给 Cost / CacheHitRate）。
func (l *UsageLedger) Usage() Usage {
	return Usage{
		PromptTokens: l.Prompt, CompletionTokens: l.Completion,
		TotalTokens: l.Total, CachedTokens: l.Cached,
		ReasoningTokens: l.Reasoning, CacheReported: l.CacheReported,
	}
}

// CacheHitRate 聚合命中率。
func (l *UsageLedger) CacheHitRate() float64 { return l.Usage().CacheHitRate() }

// Cost 聚合成本（known=false ⇒ 价目未知，展示时用「—」）。
func (l *UsageLedger) Cost(model string) (float64, bool) { return l.Usage().Cost(model) }

// Merged 返回两本账之和（便于把子任务的用量并进总账）。
func (l UsageLedger) Merged(o UsageLedger) UsageLedger {
	return UsageLedger{
		Calls:  l.Calls + o.Calls,
		Prompt: l.Prompt + o.Prompt, Completion: l.Completion + o.Completion,
		Total: l.Total + o.Total, Cached: l.Cached + o.Cached,
		Reasoning:     l.Reasoning + o.Reasoning,
		CacheReported: l.CacheReported || o.CacheReported,
	}
}
