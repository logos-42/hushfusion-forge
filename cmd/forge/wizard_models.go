package main

// wizard_models.go —— /model 向导的内置模型目录与「最近使用」记录。
//
// 设计目标（对齐用户要求）：
//   · 模型型号「内置」：不要求用户手打，向导里 ↑↓ 选即可；少数常用型号排在最前。
//   · 置顶顺序：最近使用 → 常用（内置目录）→ 已解析（/models 拉回的）→ 默认。
//   · 最近使用落盘（os/sessions/recent-models.json，gitignored；best-effort）。
//
// 诚实边界：内置型号来自各厂商公开文档里的常见 id，不是从网络现编的；某
// provider 若不在目录里，就用它的 DefaultModel + /models 实际解析结果兜底。

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/logos-42/hushfusion-forge/internal/ai"
)

// commonModels 是每个 provider 的内置常用模型（按注册表规范名）。
// 只收录广为人知、稳定的模型 id；不确定的交给 DefaultModel 或 /models。
var commonModels = map[string][]string{
	"zai":       {"glm-4.6", "glm-4.5", "glm-4.5-flash", "glm-4-plus", "glm-4-flash"},
	"deepseek":  {"deepseek-chat", "deepseek-reasoner", "deepseek-v3", "deepseek-r1"},
	"openai":    {"gpt-5", "gpt-5-mini", "gpt-4o", "gpt-4o-mini", "o3", "o4-mini"},
	"anthropic": {"claude-sonnet-4.6", "claude-opus-4.1", "claude-3-5-sonnet", "claude-3-5-haiku"},
	"openrouter": {
		"openai/gpt-5", "anthropic/claude-sonnet-4.6", "google/gemini-2.5-pro",
		"deepseek/deepseek-chat", "meta-llama/llama-3.3-70b-instruct",
	},
	"gemini":         {"gemini-2.5-pro", "gemini-2.5-flash", "gemini-1.5-pro", "gemini-1.5-flash"},
	"xai":            {"grok-4", "grok-3", "grok-2"},
	"mistral":        {"mistral-large-latest", "mistral-small-latest", "codestral-latest", "open-mistral-nemo"},
	"cohere":         {"command-r-plus", "command-r", "command-light"},
	"groq":           {"llama-3.3-70b-versatile", "llama-3.1-8b-instant", "qwen-2.5-32b"},
	"together":       {"meta-llama/Llama-3.3-70B-Instruct-Turbo", "Qwen/Qwen2.5-72B-Instruct", "deepseek-ai/DeepSeek-V3"},
	"fireworks":      {"accounts/fireworks/models/llama-v3p3-70b-instruct", "accounts/fireworks/models/deepseek-v3"},
	"deepinfra":      {"meta-llama/Llama-3.3-70B-Instruct", "deepseek-ai/DeepSeek-V3"},
	"novita":         {"meta-llama/llama-3.3-70b-instruct", "deepseek/deepseek-v3"},
	"upstage":        {"solar-pro", "solar-mini"},
	"arcee":          {"meta-llama/Llama-3.3-70B-Instruct"},
	"nvidia":         {"meta/llama-3.3-70b-instruct", "nvidia/llama-3.1-nemotron-70b-instruct"},
	"huggingface":    {"Qwen/Qwen2.5-72B-Instruct", "meta-llama/Llama-3.3-70B-Instruct"},
	"siliconflow":    {"deepseek-ai/DeepSeek-V3", "Qwen/Qwen2.5-72B-Instruct", "meta-llama/Llama-3.3-70B-Instruct"},
	"moonshot":       {"moonshot-v1-8k", "moonshot-v1-32k", "moonshot-v1-128k"},
	"minimax":        {"MiniMax-M3", "abab6.5s-chat"},
	"stepfun":        {"step-2-16k", "step-1-8k"},
	"volcengine-ark": {"doubao-seed-1.6", "doubao-pro-32k"},
	"baidu-qianfan":  {"ERNIE-4.0-8K-Latest", "ERNIE-3.5-8K"},
	"baichuan":       {"Baichuan4-Turbo", "Baichuan3-Turbo"},
	"lingyi":         {"yi-lightning", "yi-large"},
	"hunyuan":        {"hunyuan-turbo", "hunyuan-pro"},
	"zhipu":          {"glm-4.6", "glm-4-plus"},
	"ai21":           {"jamba-1.5-large", "jamba-1.5-mini"},
	"perplexity":     {"sonar", "sonar-pro", "sonar-reasoning"},
}

// recentModelsPath 是「最近使用」记录文件（os/sessions/ 已被 .gitignore 忽略）。
func recentModelsPath(base string) string {
	return filepath.Join(base, "sessions", "recent-models.json")
}

// loadRecentModels 读某 os 根下的最近使用映射（provider→模型 id，最新的在前）。
// 文件缺失/损坏一律返回空表（不让坏文件卡住向导）。
func loadRecentModels(base string) map[string][]string {
	data, err := os.ReadFile(recentModelsPath(base))
	if err != nil {
		return map[string][]string{}
	}
	var m map[string][]string
	if json.Unmarshal(data, &m) != nil || m == nil {
		return map[string][]string{}
	}
	return m
}

// recordRecentModel 把 (provider, model) 记到队首（去重、最多 8 条）并 best-effort 落盘。
func recordRecentModel(base, provider, model string) {
	if provider == "" || model == "" {
		return
	}
	all := loadRecentModels(base)
	out := []string{model}
	for _, x := range all[provider] {
		if x != model {
			out = append(out, x)
		}
	}
	if len(out) > 8 {
		out = out[:8]
	}
	all[provider] = out
	dir := filepath.Join(base, "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	if b, err := json.MarshalIndent(all, "", "  "); err == nil {
		_ = os.WriteFile(recentModelsPath(base), b, 0o644) // best-effort：写失败不影响会话
	}
}

// builtinModels 返回 provider 的内置常用模型（目录 + DefaultModel，去重保序）。
func builtinModels(p *ai.Profile) []string {
	if p == nil {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	add := func(id string) {
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, id)
	}
	for _, id := range commonModels[p.Name] {
		add(id)
	}
	add(p.DefaultModel) // 默认模型永远可选
	return out
}

// buildWizardModelList 组合向导模型列表：最近使用 → 内置常用 → 已解析 → 默认。
// 已解析 = 从 provider /models 实际拉回的模型（异步补全，可能为空）。
func buildWizardModelList(base string, p *ai.Profile, resolved []string) []string {
	if p == nil {
		return resolved
	}
	var out []string
	seen := map[string]bool{}
	add := func(id string) {
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, id)
	}
	for _, id := range loadRecentModels(base)[p.Name] {
		add(id)
	}
	if p.Name == "custom" || p.Name == "local" {
		// 自定义/本地端点：真实 /models 结果优先，占位 DefaultModel 垫底。
		for _, id := range resolved {
			add(id)
		}
		for _, id := range builtinModels(p) {
			add(id)
		}
	} else {
		for _, id := range builtinModels(p) {
			add(id)
		}
		for _, id := range resolved {
			add(id)
		}
	}
	return out
}
