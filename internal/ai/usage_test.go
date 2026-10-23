package ai

import (
	"encoding/json"
	"math"
	"testing"
)

func TestUsageUnmarshalOpenAIShape(t *testing.T) {
	raw := `{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120,
	         "prompt_tokens_details":{"cached_tokens":60},
	         "completion_tokens_details":{"reasoning_tokens":5}}`
	var u Usage
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatal(err)
	}
	if u.PromptTokens != 100 || u.CompletionTokens != 20 || u.TotalTokens != 120 {
		t.Fatalf("基础字段错: %+v", u)
	}
	if u.CachedTokens != 60 || !u.CacheReported {
		t.Fatalf("嵌套 cached_tokens 应解析且标记已上报: %+v", u)
	}
	if u.ReasoningTokens != 5 {
		t.Fatalf("reasoning_tokens 应解析: %+v", u)
	}
	if got := u.CacheHitRate(); math.Abs(got-0.6) > 1e-9 {
		t.Fatalf("命中率应 0.6, got %v", got)
	}
}

func TestUsageUnmarshalDeepSeekShape(t *testing.T) {
	raw := `{"prompt_tokens":50,"completion_tokens":10,"total_tokens":60,
	         "prompt_cache_hit_tokens":30,"prompt_cache_miss_tokens":20}`
	var u Usage
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatal(err)
	}
	if u.CachedTokens != 30 || !u.CacheReported {
		t.Fatalf("DeepSeek 形状应解析缓存命中: %+v", u)
	}
}

// 显式 0 命中（字段存在但为 0）要算「已上报」，不能与「没上报」混同。
func TestUsageCacheReportedDistinguishesZeroFromMissing(t *testing.T) {
	var zero Usage
	if err := json.Unmarshal([]byte(`{"prompt_tokens":10,"prompt_tokens_details":{"cached_tokens":0}}`), &zero); err != nil {
		t.Fatal(err)
	}
	if !zero.CacheReported {
		t.Fatal("字段存在 ⇒ 已上报（哪怕 0）")
	}

	var missing Usage
	if err := json.Unmarshal([]byte(`{"prompt_tokens":10}`), &missing); err != nil {
		t.Fatal(err)
	}
	if missing.CacheReported {
		t.Fatal("字段缺失 ⇒ 未上报")
	}
	if missing.CacheHitRate() != 0 {
		t.Fatal("未上报时命中率按 0 返回（调用方须看 CacheReported）")
	}
}

// provider 没给 total 时按和补齐（算术，不是猜）。
func TestUsageTotalFilledFromSum(t *testing.T) {
	var u Usage
	if err := json.Unmarshal([]byte(`{"prompt_tokens":7,"completion_tokens":3}`), &u); err != nil {
		t.Fatal(err)
	}
	if u.TotalTokens != 10 {
		t.Fatalf("总量应按和补齐 10, got %d", u.TotalTokens)
	}
}

// 未知形状不报错（向前兼容新 provider 的扩展字段）。
func TestUsageUnmarshalUnknownFieldsIgnored(t *testing.T) {
	var u Usage
	raw := `{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2,"brand_new_field":{"x":1}}`
	if err := json.Unmarshal([]byte(raw), &u); err != nil {
		t.Fatalf("未知字段不应报错: %v", err)
	}
	if u.PromptTokens != 1 {
		t.Fatalf("已知字段应照常解析: %+v", u)
	}
}

// 脏数据防御：命中数超过输入数时按输入数封顶，不产出负成本。
func TestUsageCacheHitRateClamped(t *testing.T) {
	u := Usage{PromptTokens: 10, CachedTokens: 99}
	if got := u.CacheHitRate(); got != 1 {
		t.Fatalf("命中率应封顶 1, got %v", got)
	}
	u2 := Usage{PromptTokens: 10, CachedTokens: -5}
	if got := u2.CacheHitRate(); got != 0 {
		t.Fatalf("负数应归 0, got %v", got)
	}
}

// 价目未知 ⇒ known=false（不许把 0 当成本）。
func TestCostUnknownWithoutPrice(t *testing.T) {
	ClearPrices()
	u := Usage{PromptTokens: 1000, CompletionTokens: 1000}
	if _, known := u.Cost("some-unknown-model"); known {
		t.Fatal("无价目时应 known=false")
	}
}

func TestCostWithExplicitPrice(t *testing.T) {
	ClearPrices()
	defer ClearPrices()
	SetPrice("test/model", Price{InputPerM: 2, OutputPerM: 8})
	u := Usage{PromptTokens: 1_000_000, CompletionTokens: 500_000}
	usd, known := u.Cost("test/model")
	if !known {
		t.Fatal("有价目应 known=true")
	}
	// 1M 输入 @2 + 0.5M 输出 @8 = 2 + 4 = 6
	if math.Abs(usd-6) > 1e-9 {
		t.Fatalf("成本应 6 USD, got %v", usd)
	}
}

// 缓存命中按更低的缓存价计费。
func TestCostWithCacheDiscount(t *testing.T) {
	ClearPrices()
	defer ClearPrices()
	SetPrice("cached/model", Price{InputPerM: 10, OutputPerM: 0, CachedInputPerM: 1})
	u := Usage{PromptTokens: 1_000_000, CachedTokens: 1_000_000}
	usd, known := u.Cost("cached/model")
	if !known {
		t.Fatal("应 known")
	}
	if math.Abs(usd-1) > 1e-9 {
		t.Fatalf("全命中应按缓存价 1 USD, got %v", usd)
	}
	// 未给缓存价 ⇒ 按输入价（高估，不低报）
	SetPrice("nocache/model", Price{InputPerM: 10})
	u2 := Usage{PromptTokens: 1_000_000, CachedTokens: 1_000_000}
	if usd2, _ := u2.Cost("nocache/model"); math.Abs(usd2-10) > 1e-9 {
		t.Fatalf("未给缓存价应按输入价高估 10, got %v", usd2)
	}
}

// 前缀匹配：带日期的模型名复用基础价目。
func TestCostPrefixMatch(t *testing.T) {
	ClearPrices()
	defer ClearPrices()
	SetPrice("openai/gpt-5", Price{InputPerM: 1, OutputPerM: 2})
	if _, known := (Usage{}).Cost("openai/gpt-5-2026-01-01"); !known {
		t.Fatal("前缀匹配应命中基础价目")
	}
}

// env 覆盖。
func TestCostEnvOverride(t *testing.T) {
	ClearPrices()
	t.Setenv("HUSHFUSION_AI_PRICE_INPUT_PER_M", "3")
	t.Setenv("HUSHFUSION_AI_PRICE_OUTPUT_PER_M", "9")
	u := Usage{PromptTokens: 1_000_000, CompletionTokens: 1_000_000}
	usd, known := u.Cost("any-model")
	if !known {
		t.Fatal("env 给了价目应 known")
	}
	if math.Abs(usd-12) > 1e-9 {
		t.Fatalf("应按 env 价目 12, got %v", usd)
	}
}

// 只给 INPUT 不给 OUTPUT ⇒ 视为未知（不给半个价目编成本）。
func TestCostEnvPartialIsUnknown(t *testing.T) {
	ClearPrices()
	t.Setenv("HUSHFUSION_AI_PRICE_INPUT_PER_M", "3")
	t.Setenv("HUSHFUSION_AI_PRICE_OUTPUT_PER_M", "")
	if _, known := (Usage{}).Cost("any-model"); known {
		t.Fatal("半个价目应视为未知")
	}
}

// 账本累加：成本与命中率看整段，不是单次。
func TestUsageLedgerAccumulates(t *testing.T) {
	ClearPrices()
	defer ClearPrices()
	SetPrice("m", Price{InputPerM: 1, OutputPerM: 1})

	var l UsageLedger
	l.Add(Usage{PromptTokens: 100, CompletionTokens: 10, TotalTokens: 110, CachedTokens: 50, CacheReported: true})
	l.Add(Usage{PromptTokens: 100, CompletionTokens: 10, TotalTokens: 110, CachedTokens: 50})
	if l.Calls != 2 || l.Prompt != 200 || l.Completion != 20 || l.Total != 220 {
		t.Fatalf("累加错: %+v", l)
	}
	if l.Cached != 100 || !l.CacheReported {
		t.Fatalf("缓存累加/已上报标记错: %+v", l)
	}
	if got := l.CacheHitRate(); math.Abs(got-0.5) > 1e-9 {
		t.Fatalf("聚合命中率应 0.5, got %v", got)
	}
	usd, known := l.Cost("m")
	if !known {
		t.Fatal("应 known")
	}
	// 200 输入 + 20 输出，@1/M = 0.00022
	if math.Abs(usd-0.00022) > 1e-12 {
		t.Fatalf("聚合成本应 0.00022, got %v", usd)
	}
}

func TestUsageLedgerMerged(t *testing.T) {
	a := UsageLedger{Calls: 1, Prompt: 10, Cached: 5, CacheReported: true}
	b := UsageLedger{Calls: 2, Prompt: 20, Completion: 3, Total: 23}
	m := a.Merged(b)
	if m.Calls != 3 || m.Prompt != 30 || m.Completion != 3 || m.Total != 23 || m.Cached != 5 {
		t.Fatalf("合并错: %+v", m)
	}
	if !m.CacheReported {
		t.Fatal("任一已上报 ⇒ 合并后已上报")
	}
}
