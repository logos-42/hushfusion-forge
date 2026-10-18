// transport —— 统一 wire 层（Go 版 hermes_cli/runtime_provider.py + agent/transports）。
//
// Hermes 支持 35+ provider 的关键不是 35 份客户端，而是：绝大多数 provider 讲
// OpenAI-compatible 的 chat/completions，于是「一份 transport + 每家的声明式
// Profile」就能覆盖几乎所有接入。v0.1 只实现 chat_completions（默认 APIMode），
// anthropic_messages 等其它模式在 NewClient 时明确报错 —— fail-closed，
// 不假装能打 /v1/messages。
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Message 是一条对话消息（OpenAI-compatible 的 role/content 形状）。
type Message struct {
	Role    string `json:"role"`    // system | user | assistant
	Content string `json:"content"` // v0.1 只支持纯文本；multimodal 留待后续
}

// ChatRequest 是一次 chat/completions 请求的载荷。
type ChatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Temperature *float64  `json:"temperature,omitempty"` // nil = 用 provider 默认
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Stream      bool      `json:"stream,omitempty"` // v0.1: true 会明确报错（见 Chat）
}

// Usage 是 token 用量（provider 返回；缺省为 0，不猜测）。
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ChatResponse 是 chat/completions 响应的最小形状。
type ChatResponse struct {
	ID           string `json:"id"`
	Model        string `json:"model"`
	Content      string `json:"content"` // choices[0].message.content
	FinishReason string `json:"finish_reason"`
	Usage        Usage  `json:"usage"`
}

// Client 是一个 provider 的会话客户端。NewClient 解析 API key；Client.Chat 发请求。
type Client struct {
	profile *Profile
	baseURL string
	apiKey  string
	http    *http.Client
}

// ClientOptions 覆盖 NewClient 的默认值。
type ClientOptions struct {
	// BaseURL 覆盖 profile.BaseURL（custom / 内部代理场景）。
	BaseURL string
	// APIKey 显式给 key；为空则按 profile.EnvVars 从环境变量解析。
	APIKey string
	// Timeout 覆盖默认 60s。
	Timeout time.Duration
}

// NewClient 构造 provider 客户端：校验 profile、解析 key、锁定端点。
// key 解析优先级：显式 APIKey > 环境变量（profile.EnvVars 顺序）。解析不到 = 报错，
// 绝不用空 key 发请求（fail-closed）。
func NewClient(p *Profile, opts ClientOptions) (*Client, error) {
	if p == nil {
		return nil, errors.New("ai: NewClient 需要非 nil profile")
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	baseURL := strings.TrimSuffix(p.BaseURL, "/")
	if opts.BaseURL != "" {
		baseURL = strings.TrimSuffix(opts.BaseURL, "/")
	}
	key := opts.APIKey
	if key == "" {
		key = ResolveAPIKey(p)
	}
	if key == "" {
		return nil, fmt.Errorf("ai: provider %q 没有 API key（显式 APIKey 或环境变量 %v 均为空）",
			p.Name, p.EnvVars)
	}
	if p.APIMode != ModeChatCompletions {
		// 诚实边界：只实现了 chat_completions。别的模式明确报错，不假装。
		return nil, fmt.Errorf("ai: provider %q 的 APIMode %q 未实现（v0.1 只支持 chat_completions）",
			p.Name, p.APIMode)
	}
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	return &Client{profile: p, baseURL: baseURL, apiKey: key, http: &http.Client{Timeout: timeout}}, nil
}

// ResolveAPIKey 按 profile.EnvVars 顺序从环境变量解析 key；全空返回 ""。
func ResolveAPIKey(p *Profile) string {
	for _, env := range p.EnvVars {
		if v := os.Getenv(env); v != "" {
			return v
		}
	}
	return ""
}

// Chat 发一次 chat/completions 请求（非流式）。Stream=true 明确报错：
// v0.1 没实现 SSE，不许假装支持。
func (c *Client) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	if req.Model == "" {
		req.Model = c.profile.DefaultModel
	}
	if req.Stream {
		return nil, errors.New("ai: stream=true 未实现（v0.1 只支持非流式 chat/completions）")
	}
	// quirk 钩子：provider 在发送前改写请求体（如 GLM 的 thinking）。
	if c.profile.TransformRequest != nil {
		if err := c.profile.TransformRequest(&req); err != nil {
			return nil, fmt.Errorf("ai: %s TransformRequest: %w", c.profile.Name, err)
		}
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("ai: marshal request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ai: new request: %w", err)
	}
	c.auth(httpReq)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ai: %s chat/completions: %w", c.profile.Name, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20)) // 8 MiB 上限，防吞内存
	if err != nil {
		return nil, fmt.Errorf("ai: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		// fail-closed：非 2xx 必须如实报，含状态码 + 响应体摘要（可能有 provider 错误信息）。
		return nil, fmt.Errorf("ai: %s chat/completions: HTTP %d: %s",
			c.profile.Name, resp.StatusCode, truncate(string(raw), 400))
	}
	var wire struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage Usage `json:"usage"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, fmt.Errorf("ai: %s unmarshal response: %w (body: %s)",
			c.profile.Name, err, truncate(string(raw), 200))
	}
	if len(wire.Choices) == 0 {
		return nil, fmt.Errorf("ai: %s 响应没有 choices（body: %s）", c.profile.Name, truncate(string(raw), 200))
	}
	return &ChatResponse{
		ID: wire.ID, Model: wire.Model,
		Content:      wire.Choices[0].Message.Content,
		FinishReason: wire.Choices[0].FinishReason,
		Usage:        wire.Usage,
	}, nil
}

// ListModels 拉 provider 的模型列表（GET {base}/models；OpenAI-compatible 缺省）。
// 请求失败或响应形状不认识 = 报错，不返回空列表冒充「没有模型」。
func (c *Client) ListModels(ctx context.Context) ([]string, error) {
	url := c.baseURL + "/models"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("ai: new request: %w", err)
	}
	c.auth(httpReq)
	httpReq.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ai: %s /models: %w", c.profile.Name, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("ai: read /models: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ai: %s /models: HTTP %d: %s",
			c.profile.Name, resp.StatusCode, truncate(string(raw), 400))
	}
	var wire struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, fmt.Errorf("ai: %s unmarshal /models: %w (body: %s)",
			c.profile.Name, err, truncate(string(raw), 200))
	}
	ids := make([]string, 0, len(wire.Data))
	for _, m := range wire.Data {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("ai: %s /models 返回 0 个模型（body: %s）", c.profile.Name, truncate(string(raw), 200))
	}
	return ids, nil
}

// auth 按 provider 注入认证头。v0.1 只有 bearer（OpenAI 兼容）；anthropic 未实现。
func (c *Client) auth(r *http.Request) {
	r.Header.Set("Authorization", "Bearer "+c.apiKey)
}

// truncate 截断长文本到 max 字符（错误消息里带 body 摘要用，避免刷屏）。
func truncate(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
