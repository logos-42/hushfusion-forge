// Package ai 提供多 provider 的 AI 模型接入层（Go 版 Hermes ProviderProfile 设计）。
//
// 借鉴 hermes-agent 的 provider 架构（providers/base.py + providers/__init__.py），
// 移植到本仓的 Go 主栈（标准库 only，零第三方依赖）：
//
//	· 声明式 Profile —— 一个结构描述一个 provider 的全部接入事实
//	  （身份 / 认证环境变量 / 端点 / 协议 / 默认模型 / 请求级 quirk），
//	  transport 不再吃一堆布尔 flag（Hermes 的原话：declarative, not flags）。
//	· "更多 AI API" 的真正杠杆不是多写客户端，而是把协议归一：
//	  绝大多数 provider（OpenAI / OpenRouter / DeepSeek / Z.AI / 本地 llama.cpp /
//	  Ollama / 任何 OpenAI-compatible 代理）讲同一种 wire —— chat/completions。
//	  Hermes 35+ provider 只有少数几种 api_mode，v0.1 这里只实现
//	  chat_completions（默认），anthropic_messages 等留 APIMode 声明、
//	  transport 明确报错（fail-closed，不假装实现）。
//	· 注册表 + 别名 —— Register/Lookup 支持名字与别名（zai ↔ glm），
//	  后注册者覆盖先注册者（用户自定义 provider 可覆盖内置默认）。
//	· 凭据从不落库 —— 只从环境变量按优先级解析（API key 值不进代码/日志），
//	  与 local-credential-handoff 纪律一致。base_url 支持显式覆盖
//	  （--base-url / custom 场景），让「任意 OpenAI 兼容端点」都能接入。
//
// v0.1 诚实边界（已实现 / 未实现 分列，避免假装）：
//
//	已实现   chat_completions 单次对话（非流式）、/models 列表、quirk 钩子挂点、
//	         多 key 轮换 + provider fallback 链（被动健康标记 + 不可重试即止）
//	未实现   SSE 流式（Stream=true 明确报错）、anthropic_messages 等其它 api_mode、
//	         主动探针式 health（当前是失败被动标记，够用；Hermes 有主动探活，可选升级）
package ai

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// APIMode 是 provider 讲的 wire 协议。v0.1 只实现 chat_completions。
type APIMode string

const (
	// ModeChatCompletions 是 OpenAI-compatible 协议（默认；绝大多数产商与代理讲它）。
	ModeChatCompletions APIMode = "chat_completions"
	// ModeAnthropicMessages 是 Anthropic Messages API（/v1/messages，x-api-key）。
	// v0.1 只声明不实现：transport 遇到它返回明确错误（fail-closed）。
	ModeAnthropicMessages APIMode = "anthropic_messages"
)

// Profile 声明一个 provider 的接入事实（Go 版 ProviderProfile，只保留 v0.1 用得着的字段）。
//
// 它是纯数据 + 可选 quirk 钩子，不持有客户端、不持有密钥 —— 与 Hermes 的
// "ProviderProfile describes, AIAgent owns" 同一分层。
type Profile struct {
	// Name 是唯一标识（注册表键），全小写：zai / deepseek / openai / openrouter / anthropic / custom。
	Name string `json:"name" jsonschema:"description=唯一标识（注册表键），全小写"`
	// Aliases 是别名（Lookup 时名字或别名都命中；zai 的别名有 glm / zhipu）。
	Aliases []string `json:"aliases,omitempty" jsonschema:"description=别名（Lookup 时名字或别名都命中）"`
	// DisplayName 是给人看的名字（CLI 列表用）。
	DisplayName string `json:"display_name,omitempty" jsonschema:"description=给人看的名字（CLI 列表用）"`
	// EnvVars 是 API key 环境变量优先级列表（从前往后找，第一个非空者胜；
	// 每个 env 可含逗号分隔的多个 key，用于多 key 轮换）。
	EnvVars []string `json:"env_vars,omitempty" jsonschema:"description=API key 环境变量优先级列表（每个 env 可含逗号分隔多 key，用于多 key 轮换）"`
	// Keys 是该 provider 的显式 API key 列表（多 key 轮换 / 故障转移用；
	// 与 EnvVars 取并集、去重保序）。为空时回退到 EnvVars 顺序解析。
	Keys []string `json:"keys,omitempty" jsonschema:"description=该 provider 的显式 API key 列表（多 key 轮换/故障转移用）；为空则回退到 EnvVars 顺序解析"`
	// Fallbacks 是本 provider 所有 key 均失败时的回退 provider 名（按序尝试），
	// 用于一家 key 失效时自动切到别家（如 zai → openrouter）。
	Fallbacks []string `json:"fallbacks,omitempty" jsonschema:"description=本 provider 所有 key 均失败时的回退 provider 名（按序尝试）"`
	// BaseURL 是默认端点（不带 /chat/completions 尾巴；NewClient 可覆盖）。
	BaseURL string `json:"base_url,omitempty" jsonschema:"description=默认端点（不带 /chat/completions 尾巴）"`
	// ModelsURL 是显式模型列表端点；空则用 BaseURL+"/models"（OpenAI 兼容缺省）。
	ModelsURL string `json:"models_url,omitempty" jsonschema:"description=显式模型列表端点；空则用 BaseURL+/models"`
	// APIMode 是 wire 协议（默认 chat_completions）。
	APIMode APIMode `json:"api_mode,omitempty" jsonschema:"description=wire 协议（chat_completions 默认；anthropic_messages 仅声明）"`
	// DefaultModel 是 --model 缺省时的模型 id。
	DefaultModel string `json:"default_model,omitempty" jsonschema:"description=--model 缺省时的模型 id"`
	// DefaultAuxModel 是便宜辅助模型 id（压缩/命名等杂活；v0.1 只声明不调度）。
	DefaultAuxModel string `json:"default_aux_model,omitempty" jsonschema:"description=便宜辅助模型 id（压缩/命名等杂活）"`
	// TransformRequest 是请求级 quirk 钩子：发送前改写请求体。
	// 对标 Hermes 的 build_api_kwargs_extras / prepare_messages —— 例如
	// GLM 系要在 extra_body.thinking 里关/开思考模式。nil = 不改写。
	// 签名里的 *ChatRequest 在 transport.go 定义（同一包，无循环）。
	// 函数不可序列化，配置落库时跳过（json:"-"）。
	TransformRequest func(*ChatRequest) error `json:"-"`
}

// Builtins 返回内置 provider 目录（注册表种子）。
//
// 选取原则：leo 实际在用的（zai/GLM）、仓库凭据池里有的（deepseek、
// openrouter）、最通用的（openai）、协议对照样板（anthropic，仅声明）、
// 任意端点逃生口（custom）与本地开源模型（local，ollama / llama.cpp 兼容口）。
func Builtins() []*Profile {
	return []*Profile{
		{
			Name: "zai", Aliases: []string{"glm", "zhipu", "z-ai"},
			DisplayName: "Z.AI (GLM)", EnvVars: []string{"GLM_API_KEY", "ZAI_API_KEY"},
			BaseURL: "https://api.z.ai/api/paas/v4", DefaultModel: "glm-5.3-flash",
			DefaultAuxModel: "glm-4.5-flash",
		},
		{
			Name: "deepseek", Aliases: []string{"ds"},
			DisplayName: "DeepSeek", EnvVars: []string{"DEEPSEEK_API_KEY"},
			BaseURL: "https://api.deepseek.com/v1", DefaultModel: "deepseek-chat",
		},
		{
			Name: "openai", Aliases: []string{"open-ai"},
			DisplayName: "OpenAI", EnvVars: []string{"OPENAI_API_KEY"},
			BaseURL: "https://api.openai.com/v1", DefaultModel: "gpt-5",
			DefaultAuxModel: "gpt-5-mini",
		},
		{
			Name: "openrouter", Aliases: []string{"router"},
			DisplayName: "OpenRouter", EnvVars: []string{"OPENROUTER_API_KEY"},
			BaseURL: "https://openrouter.ai/api/v1", ModelsURL: "https://openrouter.ai/api/v1/models",
			DefaultModel: "openai/gpt-5",
		},
		{
			// 协议对照样板：anthropic 的 wire 不是 chat/completions。
			// v0.1 只声明 APIMode，transport 见它即明确报错 —— 诚实边界，
			// 不假装能打 /v1/messages。
			Name: "anthropic", Aliases: []string{"claude"},
			DisplayName: "Anthropic", EnvVars: []string{"ANTHROPIC_API_KEY"},
			BaseURL: "https://api.anthropic.com", APIMode: ModeAnthropicMessages,
			DefaultModel: "claude-sonnet-4.6",
		},
		{
			// 逃生口：任意 OpenAI-compatible 端点（内部代理 / 网关 / 私有部署）。
			// 用 --base-url 覆盖 BaseURL，--api-key-env 指定自己的 key 环境变量。
			Name: "custom", Aliases: []string{"proxy"},
			DisplayName: "Custom (OpenAI-compatible)", EnvVars: []string{"AI_CUSTOM_API_KEY"},
			BaseURL: "http://127.0.0.1:8000/v1", DefaultModel: "custom-model",
		},
		{
			Name: "local", Aliases: []string{"ollama", "llamacpp", "llama"},
			DisplayName: "Local (Ollama / llama.cpp compatible)", EnvVars: []string{"AI_LOCAL_API_KEY"},
			BaseURL: "http://127.0.0.1:11434/v1", DefaultModel: "qwen3:8b",
		},
	}
}

// Validate 检查 Profile 是否可用（注册前与 Lookup 命中后各查一次）。
func (p *Profile) Validate() error {
	if p == nil {
		return errors.New("ai: profile 为 nil")
	}
	name := strings.TrimSpace(p.Name)
	if name == "" {
		return errors.New("ai: profile.Name 为空")
	}
	if strings.ToLower(name) != name {
		return fmt.Errorf("ai: profile.Name %q 必须全小写（注册表键）", name)
	}
	if strings.TrimSpace(p.BaseURL) == "" {
		return fmt.Errorf("ai: profile %q 的 BaseURL 为空", name)
	}
	if _, err := url.Parse(p.BaseURL); err != nil {
		return fmt.Errorf("ai: profile %q 的 BaseURL 非法: %w", name, err)
	}
	if p.APIMode == "" {
		p.APIMode = ModeChatCompletions
	}
	switch p.APIMode {
	case ModeChatCompletions, ModeAnthropicMessages:
	default:
		return fmt.Errorf("ai: profile %q 的 APIMode %q 未注册", name, p.APIMode)
	}
	return nil
}
