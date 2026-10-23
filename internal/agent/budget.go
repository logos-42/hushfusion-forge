package agent

import (
	"os"
	"strconv"
	"strings"
)

// BudgetFromEnv 允许用 HUSHFUSION_MAX_BUDGET_TOKENS 覆盖自动预算。
func BudgetFromEnv(fallback int) int {
	if v := strings.TrimSpace(os.Getenv("HUSHFUSION_MAX_BUDGET_TOKENS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1000 {
			return n
		}
	}
	return fallback
}

// ContextWindowForModel 按模型名匹配上下文窗口（token）。
// 只做保守映射；未知模型按 128k 处理，宁可早压缩也不把请求撑爆。
func ContextWindowForModel(model string) int {
	m := strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.Contains(m, "gemini"):
		return 1_000_000
	case strings.Contains(m, "claude"):
		return 200_000
	case strings.Contains(m, "gpt-4.1"):
		return 1_000_000
	case strings.Contains(m, "o1"), strings.Contains(m, "o3"), strings.Contains(m, "o4"):
		return 200_000
	case strings.Contains(m, "gpt-4o"), strings.Contains(m, "gpt-4"):
		return 128_000
	case strings.Contains(m, "deepseek"):
		return 128_000
	case strings.Contains(m, "glm"):
		return 128_000
	case strings.Contains(m, "qwen"):
		return 131_072
	case strings.Contains(m, "kimi"):
		return 256_000
	case strings.Contains(m, "llama"), strings.Contains(m, "mistral"):
		return 128_000
	default:
		return 128_000
	}
}

// BudgetForModel 返回对话历史预算：默认取上下文窗口的 60%，
// 给 system、工具 schema 和输出留余量；最低 4000。
func BudgetForModel(model string, ratio float64) int {
	if ratio <= 0 || ratio >= 1 {
		ratio = 0.6
	}
	window := ContextWindowForModel(model)
	budget := int(float64(window) * ratio)
	if budget < 4000 {
		budget = 4000
	}
	return budget
}
