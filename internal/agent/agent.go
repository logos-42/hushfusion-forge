// Package agent 提供连续对话执行 Agent Runtime（docs/wiki/agent-runtime-design.md）。
//
// 设计契约：
//   - 单一 queryLoop（学 Claude Code）：CLI / TUI / serve 所有入口共用同一个 loop；
//   - 模型推理，harness 强制执行边界：本包不自己发 HTTP（走注入的 ChatFunc）、
//     不自己写 OS（写工具经 oskernel 白名单 + 内核通道）、不自己跑实验（harness
//     经注入的 Executor）；
//   - fail-closed：未知工具 / 参数非法 / 越权写 ⇒ 拒，原因回灌给模型自愈，绝不假装成功；
//   - 回合上限：每轮（一个 user 消息）最多 MaxToolTurns 次模型调用，耗尽即报错不编造。
//
// 数据流（学 Claude Code 的 9 步管道）：
//
//	assemble() （system + skills + 对话历史 + 工具 schema）→ model() → 模型输出
//	tool_calls → execute()（权限门控 → 工具执行）→ 结果回填为 tool 消息 → 回到 assemble()
//	直到模型产出最终回复（无 tool_calls）或预算耗尽。
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/logos-42/hushfusion-forge/internal/ai"
)

// Role 是消息角色（OpenAI-compatible 子集）。
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message 是 runtime 里的对话消息（OpenAI-compatible 形状，复刻 ai.Message）。
type Message struct {
	Role       Role          `json:"role"`
	Content    string        `json:"content"`
	ToolCalls  []ai.ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
}

// Config 配置一个 Agent（Introduce Parameter Object：所有循环/预算参数收拢成一块）。
type Config struct {
	// MaxToolTurns 是每个 user 消息最多允许的模型调用轮数（工具往返上限）。
	MaxToolTurns int
	// MaxBudgetTokens 是对话历史估算 token 上限（len/4 估算，预留精确 tokenizer 槽）。
	MaxBudgetTokens int
	// MaxToolResults 是单轮里最多回填的工具结果条数（防工具风暴淹没上下文）。
	MaxToolResults int
	// ModelTimeout 是单次模型调用的超时（0 = 默认 120s；防挂死）。
	ModelTimeout time.Duration
	// ModelRetries 是单次模型调用失败后的重试次数（0 = 默认 1；瞬时错误恢复）。
	ModelRetries int
	// Provider / Model / BaseURL 透传给 ai.Chat（空 Model=provider 默认）。
	Provider string
	Model    string
	BaseURL  string
	// Root 是 read/ls/shell 工具的工作区根（空 = 当前目录；越界路径一律拒绝）。
	Root string
	// Tokenizer 是可注入的 token 计数器（nil = 默认 len/4 估算，见 tokenizer.go）。
	// 注入精确实现后，预算判断与压缩触发都按真实 token 数走。
	Tokenizer Tokenizer
}

// DefaultConfig 返回带默认值的配置。
func DefaultConfig() Config {
	return Config{
		MaxToolTurns:    200,
		MaxBudgetTokens: 12000,
		MaxToolResults:  200,
		ModelTimeout:    120 * time.Second,
		ModelRetries:    1,
		Root:            ".",
	}
}

// withDefaults 把未设（<=0）的字段补默认（单一处，消灭同构 if<=0）。
func (c Config) withDefaults() Config {
	if c.MaxToolTurns <= 0 {
		c.MaxToolTurns = 200
	}
	if c.MaxBudgetTokens <= 0 {
		c.MaxBudgetTokens = 12000
	}
	if c.MaxToolResults <= 0 {
		c.MaxToolResults = 200
	}
	if c.ModelTimeout <= 0 {
		c.ModelTimeout = 120 * time.Second
	}
	if c.ModelRetries < 0 {
		c.ModelRetries = 0
	}
	if strings.TrimSpace(c.Root) == "" {
		c.Root = "."
	}
	return c
}

// ChatFunc 是一次模型调用的端口（注入假实现即零网络可测）。
// 返回模型回复文本；tool_calls 由 ai.Chat 的响应解析（扩展后）。
type ChatFunc func(ctx context.Context, req ai.ChatRequest) (*ai.ChatResponse, error)

// ChatStreamFunc 是流式模型调用的端口（增量事件通过 on 回调；on 可为 nil）。
// 返回值与 ChatFunc 同构（完整装配响应），便于流/非流两套后处理共享。
type ChatStreamFunc func(ctx context.Context, req ai.ChatRequest, on func(ai.StreamEvent)) (*ai.ChatResponse, error)

// ToolExecutor 执行一次工具调用（参数已校验为 JSON 对象）。返回结果文本回填给模型。
type ToolExecutor func(ctx context.Context, name string, args map[string]any) (string, error)

// Tool 是注册到 ToolRegistry 的工具声明。
type Tool struct {
	Spec     ai.ToolSpec  // 模型看到的 schema
	Executor ToolExecutor // 执行器
	Write    bool         // true=写动作（需白名单，见 Register）
}

// ToolRegistry 是工具注册表：名字 → 声明，提供 fail-closed 校验与调度。
type ToolRegistry struct {
	tools map[string]Tool
}

// NewRegistry 构造空注册表。
func NewRegistry() *ToolRegistry {
	return &ToolRegistry{tools: map[string]Tool{}}
}

// Register 注册一个工具（同名覆盖 — 后注册者优先，学 hermes 注册表）。
// 返回 error 仅当参数非法（空名字/空 Executor）。
func (r *ToolRegistry) Register(t Tool) error {
	if strings.TrimSpace(t.Spec.Function.Name) == "" {
		return errors.New("agent: 工具名不能为空")
	}
	if t.Executor == nil {
		return fmt.Errorf("agent: 工具 %q 的 Executor 为 nil", t.Spec.Function.Name)
	}
	if t.Spec.Type == "" {
		t.Spec.Type = "function"
	}
	r.tools[t.Spec.Function.Name] = t
	return nil
}

// RegisterTool 注册宿主提供的项目工具（fusion / plant / plugin / mcp / acp / diap）。
// 与内置工具走同一注册表、同一 fail-closed 调度，便于 TUI 与 CLI 共用。
func (s *Session) RegisterTool(t Tool) error { return s.registry.Register(t) }

// ToolSpecs 返回当前注册的全部工具 schema（宿主自检 / 测试用）。
func (s *Session) ToolSpecs() []ai.ToolSpec { return s.registry.Specs() }

// Specs 返回模型可见的工具 schema 列表（model() 注入点）。
func (r *ToolRegistry) Specs() []ai.ToolSpec {
	out := make([]ai.ToolSpec, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t.Spec)
	}
	return out
}

// Names 返回全部已注册工具名（错误消息 / 调试用）。
func (r *ToolRegistry) Names() []string {
	out := make([]string, 0, len(r.tools))
	for n := range r.tools {
		out = append(out, n)
	}
	return out
}

// Execute 执行一次工具调用：fail-closed 三闸（未知工具 / 参数解析失败 / 写工具越权）。
// 返回结果文本或明确错误（错误也会被回灌给模型自愈 — 但不假装成功）。
func (r *ToolRegistry) Execute(ctx context.Context, call ai.ToolCall, allowWrites map[string]bool) (string, error) {
	name := call.Function.Name
	t, ok := r.tools[name]
	if !ok {
		return "", fmt.Errorf("未知工具 %q（可用：%v）", name, r.Names())
	}
	// 写门 fail-closed：白名单缺失（nil）= 拒绝一切写，绝不放行。
	if t.Write && !allowWrites[name] {
		return "", fmt.Errorf("工具 %q 是写动作，不在本次会话的白名单（AI 只能提议，不能自主写）", name)
	}
	var args map[string]any
	if strings.TrimSpace(call.Function.Arguments) != "" {
		if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
			return "", fmt.Errorf("工具 %q 的参数不是合法 JSON：%v", name, err)
		}
	}
	if args == nil {
		args = map[string]any{}
	}
	out, err := t.Executor(ctx, name, args)
	if err != nil {
		return "", fmt.Errorf("工具 %q 执行失败：%v", name, err)
	}
	return out, nil
}
