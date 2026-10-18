---
title: AI Provider 接入层设计（借鉴 Hermes Agent）
source: session
created: 2026-10-18
last_confirmed: 2026-10-18
audience: self
stage: crystallized
---

# AI Provider 接入层设计（借鉴 Hermes Agent）

> 状态：crystallized（2026-10-18）｜ 实现：`internal/ai/`（Go 标准库，零第三方依赖）
> 入口：`forge ai <list|chat|models>`
> 本页回答：为什么这么设计、借鉴了什么、诚实边界在哪。

## 0. 一句话

Hermes Agent 支持 35+ LLM provider（OpenAI / Anthropic / DeepSeek / GLM / 本地模型……）
靠的不是 35 份客户端，而是**一套声明式 Profile + 注册表别名 + OpenAI-compatible 默认
transport** —— 绝大多数 provider 讲同一种 wire（`chat/completions`），于是
「一份 transport + 每家一份声明」就能覆盖几乎所有接入。本仓把这一机制移植成 Go。

## 1. 借鉴了什么（读 Hermes 源码的结论）

读的是 `~/Downloads/hermes`（Hermes Agent 仓库）的：

- `providers/base.py` —— ProviderProfile 基类（声明式，dataclass）
- `providers/__init__.py` —— 注册表 / 别名 / 插件发现（bundled → user → pip）
- `hermes_cli/provider_catalog.py` —— 统一目录（凭据注册表 + profile + 展示元数据合一）
- `hermes_cli/runtime_provider.py` —— api_mode 探测与 transport 选择
- `plugins/model-providers/{zai,anthropic,custom}/` —— 每家一份声明（zai 的 thinking quirk 是样板）

三条核心机制，逐条说 Go 版对应：

### 1.1 声明式 Profile（providers/base.py → internal/ai/profile.go）

Hermes：`ProviderProfile` 一个 dataclass 描述 provider 的全部接入事实 —— 身份（name /
aliases）、展示（display_name / description）、认证（env_vars 优先级列表 / auth_type）、
端点（base_url / models_url）、协议（api_mode）、请求 quirk（fixed_temperature /
default_max_tokens / default_aux_model）、钩子（fetch_models / prepare_messages /
build_extra_body / create_client）。

Go 版：`Profile` 结构体，字段一一对应，只保留 v0.1 用得着的：

```go
type Profile struct {
    Name, DisplayName      string
    Aliases                []string
    EnvVars                []string   // API key 环境变量优先级列表
    BaseURL, ModelsURL     string
    APIMode                APIMode    // chat_completions（默认，唯一实现）| anthropic_messages（声明不实现）
    DefaultModel           string
    DefaultAuxModel        string
    TransformRequest       func(*ChatRequest) error  // quirk 钩子：发送前改写请求体
}
```

关键差异：Hermes 的 profile 有 30+ 字段，本仓只留 12 个。**不抄全量字段** ——
减法原则：v0.1 用不上的（credential pool 轮换、vision、reasoning effort……）不占位。

### 1.2 注册表 + 别名（providers/__init__.py → internal/ai/registry.go）

Hermes：`register_provider(profile)` 按 name 注册、登记 aliases；`get_provider_profile(name)`
按名字或别名查；后注册者覆盖先注册者（用户插件可覆盖内置）；懒加载三层来源。

Go 版：`Register()` / `Lookup()` / `List()`，同名后注册覆盖，别名大小写不敏感。
v0.1 只有两层来源（内置 `Builtins()` + 运行时 Register），不实现插件目录扫描 ——
本仓是单仓库单体，不需要插件加载器；要加 provider 就是加一份 `Builtins()` 里的声明。

### 1.3 协议归一（runtime_provider.py → internal/ai/transport.go）

Hermes 最关键的洞察：**api_mode 是 provider 之间真正的分水岭**。大多数产商与代理
（OpenAI / OpenRouter / DeepSeek / Z.AI / 本地 llama.cpp / Ollama / 私有网关）讲
`chat_completions`；Anthropic 讲 `/v1/messages`（`anthropic_messages`）；还有
codex_responses / bedrock_converse 等特例。35+ provider 只有 5 种 api_mode，
其中默认的 chat_completions 覆盖绝大多数。

Go 版 v0.1：只实现 `chat_completions`（默认 APIMode），`anthropic_messages` 等
在 `NewClient` 时**明确报错**（fail-closed，不假装能打 /v1/messages）。接口留了
`APIMode` 枚举和 `TransformRequest` 钩子 —— 以后要加 Anthropic 就是加一个 transport，
不是推倒重来。

## 2. 本仓实现（internal/ai/ 文件布局）

| 文件 | 角色 | 对应 Hermes |
|:--|:--|:--|
| `profile.go` | Profile 声明 + 内置目录 Builtins() + Validate | providers/base.py + plugins/model-providers/* |
| `registry.go` | Register / Lookup / List + 别名解析 | providers/__init__.py |
| `transport.go` | chat/completions 客户端（请求/响应/错误）+ ListModels + key 解析 | runtime_provider.py + agent/transports |
| `client.go` | 高层入口 Resolve → NewClient → Chat/ListModels | hermes_cli/runtime_provider.py |

CLI：`forge ai list` / `forge ai chat --provider <name> --prompt <text>` /
`forge ai models --provider <name>`（cmd/forge/main.go cmdAI*）。

## 3. 密钥纪律（local-credential-handoff 延伸）

- API key 只从环境变量解析（`Profile.EnvVars` 优先级列表，第一个非空者胜）。
- 值不进代码、不进 CLI 参数、不进日志 —— `forge ai chat` 读 `$GLM_API_KEY` 等，
  进程从环境拿，命令行只传 provider/model/prompt。
- `--api-key-env <VAR>` 允许覆盖默认环境变量名（自定义 provider 场景），
  复制的 profile 只改 EnvVars，不动注册表全局状态。
- 真调用验证时用脚本从 `~/.hermes/.env` 读 key 注入进程环境（值只活在进程里）。

## 4. 测试（internal/ai/ai_test.go，httptest 假端点，零真实网络）

覆盖（14 条）：
- 请求形状：POST {base}/chat/completions、Authorization: Bearer、body 的 model/messages
- 响应解析：choices[0].message.content / finish_reason / usage
- fail-closed：HTTP 401/403 → 报错含状态码；空 /models → 报错不返回空列表冒充
- 未实现边界：anthropic_messages 模式 → NewClient 报错；stream=true → 报错
- 注册表：别名（zai↔glm）、后注册覆盖、未知名字返回 nil
- key 解析：环境变量优先级（第一个非空者胜）、无 key → NewClient 报错
- base_url 覆盖：custom 场景

## 5. 真调用验证（2026-10-18 实测）

- `forge ai chat --provider zai --base-url https://llm.goaichat.top/v1 --model deepseek-v4-flash "只回复两个字：通了"`
  → 返回「通了」，usage 134 tokens（prompt 113 + completion 21）。**真打通**。
- `forge ai models --provider openrouter` → 列出模型（deepseek / elorian / xingchen……）。
- `forge ai chat --provider openrouter --model openai/gpt-5-mini` → HTTP 403 区域限制，
  transport 如实报错（fail-closed 行为正确，不是 bug）。

## 6. 诚实边界（v0.1 明确不做，缺了不假装）

| 能力 | Hermes 有 | 本仓 v0.1 | 说明 |
|:--|:--|:--|:--|
| chat/completions 非流式 | ✅ | ✅ | 已实现 |
| /models 列表 | ✅ | ✅ | 已实现 |
| SSE 流式 | ✅ | ❌（stream=true 报错） | 下一迭代；接口留了 Stream 字段 |
| anthropic_messages 等其它 api_mode | ✅ | ❌（NewClient 报错） | 只声明 APIMode，不假装 |
| 凭据池多 key 轮换 + fallback 链 | ✅ | ❌ | 单 key 先跑通闭环再谈轮换 |
| 插件目录发现（用户自定义 provider 落盘） | ✅ | ❌（运行时 Register） | 单仓库不需要插件加载器 |
| vision / reasoning effort / aux 模型调度 | ✅ | ❌ | 用不上就不占位 |

## 7. 下一步（未冻结，候选）

- 把 `forge os` 的 API 服务器（HTTP REST）接到本层 ——「AI 接入」指的不只是
  CLI 对话：OS 决策层任务可以由任意 provider 的模型驱动（自设进度 / 自补测试）。
- SSE 流式（agent 长回复体验）。
- anthropic_messages transport（需要时再加，机制已留好）。
- 多 key 轮换（provider 限流时自动换 key，对标 Hermes credential pool）。
