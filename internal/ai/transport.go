// transport —— 统一 wire 层（Go 版 hermes_cli/runtime_provider.py + agent/transports）。
//
// Hermes 支持 35+ provider 的关键不是 35 份客户端，而是：绝大多数 provider 讲
// OpenAI-compatible 的 chat/completions，于是「一份 transport + 每家的声明式
// Profile」就能覆盖几乎所有接入。v0.1 只实现 chat_completions（默认 APIMode），
// anthropic_messages 等其它模式在 NewClient 时明确报错 —— fail-closed，
// 不假装能打 /v1/messages。
package ai

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Message 是一条对话消息（OpenAI-compatible 的 role/content 形状）。
type Message struct {
	Role    string `json:"role"`    // system | user | assistant | tool
	Content string `json:"content"` // v0.1 只支持纯文本；multimodal 留待后续
	// ToolCalls 是 assistant 消息可携带的工具调用请求（tool_calls 协议扩展，
	// 2026-10-23 合同扩展：runtime 依赖它才能连续对话执行）。
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// ToolCallID 是 tool 角色消息回填时指向的调用 id（OpenAI 协议要求
	// tool 消息必须 content + tool_call_id，且紧跟在产生它的 assistant 消息后）。
	ToolCallID string `json:"tool_call_id,omitempty"`
}

// ToolCall 是模型的工具调用请求（OpenAI-compatible）。
type ToolCall struct {
	ID       string `json:"id"` // 调用 id（回填 tool 消息时原样带回）
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"` // JSON 字符串，执行前解析（runtime 负责校验）
	} `json:"function"`
}

// ToolSpec 声明一个可被模型调用的工具（OpenAI-compatible tools[].function；
// parameters 是 JSON Schema 子集，runtime 用它做参数校验）。
type ToolSpec struct {
	Type     string `json:"type"` // "function"
	Function struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Parameters  any    `json:"parameters,omitempty"` // JSON Schema 对象
	} `json:"function"`
}

// ChatRequest 是一次 chat/completions 请求的载荷。
type ChatRequest struct {
	Model       string     `json:"model"`
	Messages    []Message  `json:"messages"`
	Temperature *float64   `json:"temperature,omitempty"` // nil = 用 provider 默认
	MaxTokens   int        `json:"max_tokens,omitempty"`
	Stream      bool       `json:"stream,omitempty"` // v0.1: true 会明确报错（见 Chat）
	Tools       []ToolSpec `json:"tools,omitempty"`  // 2026-10-23 扩展：模型可请求工具执行
}

// Usage 定义见 usage.go（兼容多家 provider 形状 + 缓存/推理字段 + 成本估算）。

// ChatResponse 是 chat/completions 响应的最小形状。
type ChatResponse struct {
	ID           string `json:"id"`
	Model        string `json:"model"`
	Content      string `json:"content"` // choices[0].message.content
	FinishReason string `json:"finish_reason"`
	Usage        Usage  `json:"usage"`

	// ToolCalls 是 choices[0].message.tool_calls（模型请求的工具调用）。
	// 非 tool 响应为 nil。按 OpenAI 协议原样暴露给 runtime（agent 包直接读字段）。
	ToolCalls []ToolCall `json:"-"`
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

// ResolveAPIKey 返回该 provider 的第一个可用 key（向后兼容单 key 调用方）。
func ResolveAPIKey(p *Profile) string {
	ks := ResolveAPIKeys(p, "")
	if len(ks) > 0 {
		return ks[0]
	}
	return ""
}

// ResolveAPIKeys 收集该 provider 的全部可用 key（去重、去空、保序）。
// 顺序即轮换顺序：显式 APIKey > p.Keys（显式列表）> p.EnvVars（每个 env 可含
// 逗号分隔的多 key）。多 key 用于轮换 / 故障转移（学 Hermes auxiliary_client）。
func ResolveAPIKeys(p *Profile, explicit string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		for _, part := range strings.Split(s, ",") {
			k := strings.TrimSpace(part)
			if k == "" || seen[k] {
				continue
			}
			seen[k] = true
			out = append(out, k)
		}
	}
	if explicit != "" {
		add(explicit)
	}
	for _, k := range p.Keys {
		add(k)
	}
	for _, env := range p.EnvVars {
		if v := os.Getenv(env); v != "" {
			add(v)
		}
	}
	return out
}

// HTTPStatusError 携带 provider 返回的 HTTP 状态码，供上层判断是否可重试
// （触发 key 轮换 / provider 回退）。
type HTTPStatusError struct {
	Provider string
	Code     int
	Body     string
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("ai: %s HTTP %d: %s", e.Provider, e.Code, truncate(e.Body, 400))
}

// StreamEventType 区分流式事件的种类。
type StreamEventType int

const (
	// StreamContent 是增量文本内容（delta.content）。
	StreamContent StreamEventType = iota
	// StreamToolCall 是工具调用的增量片段（delta.tool_calls，按 index 累积）。
	StreamToolCall
	// StreamDone 是流结束（flush 最终 usage / finish_reason）。
	StreamDone
)

// StreamEvent 是 ChatStream 回调吐出的最小事件（agent 层按它增量渲染）。
// 工具调用是「按 index 累积」的：Name 只在首次给，Arguments 每次追加片段。
type StreamEvent struct {
	Type      StreamEventType
	Text      string // StreamContent 用：本次增量文本
	ToolIndex int    // StreamToolCall 用：调用序号
	ToolID    string // StreamToolCall 用：已确定的调用 id（首次之后才非空前）
	ToolName  string // StreamToolCall 用：工具名（首次片段）
	ToolArgs  string // StreamToolCall 用：本次追加的参数片段
	Finish    string // StreamDone 用：finish_reason
	Usage     Usage  // StreamDone 用：最终用量（provider 仅在最后一片给）
}

// ChatStream 发一次流式 chat/completions（SSE），逐事件回调 on。
//
// on 在每次增量时同步调用（调用方据此增量渲染）；返回值是流结束后的完整
// 装配响应（content + 完整 tool_calls + usage），与 Chat 的返回值同构，
// 方便上层在流与一次性两种模式间共享后处理。on 为 nil 时仅装配不回调。
// 错误语义同 Chat，但**一旦已下发任何事件，就不再跨 key/provider 重试**
// （否则调用方会收到重复 delta）——半路失败如实返回错误。
func (c *Client) ChatStream(ctx context.Context, req ChatRequest, on func(StreamEvent)) (*ChatResponse, error) {
	if req.Model == "" {
		req.Model = c.profile.DefaultModel
	}
	if c.profile.TransformRequest != nil {
		if err := c.profile.TransformRequest(&req); err != nil {
			return nil, fmt.Errorf("ai: %s TransformRequest: %w", c.profile.Name, err)
		}
	}
	req.Stream = true
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
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ai: %s chat/completions (stream): %w", c.profile.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		return nil, &HTTPStatusError{Provider: c.profile.Name, Code: resp.StatusCode, Body: string(raw)}
	}

	out := &ChatResponse{Model: req.Model}
	var toolCalls []ToolCall
	emit := func(ev StreamEvent) {
		if on != nil {
			on(ev)
		}
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<16), 8<<20) // 单行可到 8 MiB（大参数片段）
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}
		var chunk struct {
			ID      string `json:"id"`
			Model   string `json:"model"`
			Choices []struct {
				Delta struct {
					Role      string `json:"role"`
					Content   string `json:"content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage Usage `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return nil, fmt.Errorf("ai: %s 解析 SSE 分片失败: %w (data: %s)", c.profile.Name, err, truncate(data, 200))
		}
		if chunk.ID != "" {
			out.ID = chunk.ID
		}
		if chunk.Model != "" {
			out.Model = chunk.Model
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		ch := chunk.Choices[0]
		if ch.Delta.Content != "" {
			out.Content += ch.Delta.Content
			emit(StreamEvent{Type: StreamContent, Text: ch.Delta.Content})
		}
		for _, tc := range ch.Delta.ToolCalls {
			for len(toolCalls) <= tc.Index {
				toolCalls = append(toolCalls, ToolCall{})
			}
			cur := &toolCalls[tc.Index]
			if tc.ID != "" {
				cur.ID = tc.ID
			}
			if tc.Type != "" {
				cur.Type = tc.Type
			}
			if tc.Function.Name != "" {
				cur.Function.Name = tc.Function.Name
			}
			if tc.Function.Arguments != "" {
				cur.Function.Arguments += tc.Function.Arguments
			}
			emit(StreamEvent{Type: StreamToolCall, ToolIndex: tc.Index, ToolID: cur.ID,
				ToolName: cur.Function.Name, ToolArgs: tc.Function.Arguments})
		}
		if ch.FinishReason != "" {
			out.FinishReason = ch.FinishReason
		}
		if chunk.Usage.TotalTokens > 0 { // 仅最后一片携带完整用量
			out.Usage = chunk.Usage
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("ai: %s 读取 SSE 失败: %w", c.profile.Name, err)
	}
	out.ToolCalls = toolCalls
	return out, nil
}

// ChatStream 是对外主入口（流式版）：多 key 轮换 + 回退 provider，契约同 Chat。
//
// 与 Chat 的差别：一旦某次尝试已向下发任何事件就**不再重试**（避免重复 delta），
// 半路失败直接返回错误；未下发事件前仍按 Chat 的韧性逻辑换 key/换 provider。
func ChatStream(ctx context.Context, providerName string, req ChatRequest,
	on func(StreamEvent), opts ClientOptions) (*ChatResponse, error) {
	attempts, err := buildAttempts(providerName, opts)
	if err != nil {
		return nil, err
	}
	if len(attempts) == 0 {
		return nil, fmt.Errorf("ai: provider %q 无可用 API key（显式 key / Keys / EnvVars 均为空）", providerName)
	}
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
		c, e := NewClient(a.profile, ClientOptions{BaseURL: a.baseURL, APIKey: a.key, Timeout: opts.Timeout})
		if e != nil {
			lastErr = e
			continue
		}
		delivered := false
		cb := func(ev StreamEvent) {
			delivered = true
			if on != nil {
				on(ev)
			}
		}
		resp, e := c.ChatStream(ctx, req, cb)
		if e == nil {
			kh.revive(a.provider, a.key)
			return resp, nil
		}
		lastErr = e
		if delivered {
			return nil, fmt.Errorf("ai: %s 流式在已下发事件后失败: %w", a.provider, e)
		}
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
		// fail-closed：非 2xx 必须如实报，含状态码 + 响应体（可能有 provider 错误信息）。
		return nil, &HTTPStatusError{Provider: c.profile.Name, Code: resp.StatusCode, Body: string(raw)}
	}
	var wire struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content   string     `json:"content"`
				ToolCalls []ToolCall `json:"tool_calls"`
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
		ToolCalls:    wire.Choices[0].Message.ToolCalls,
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
		return nil, &HTTPStatusError{Provider: c.profile.Name, Code: resp.StatusCode, Body: string(raw)}
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

// ---- 多 key 韧性：被动健康标记 + 轮换（学 Hermes auxiliary_client） ----
// 不主动探针；某 key 在重试窗口内失败则标记不可用（dead），成功则复活。
// key 值不持久化：以 sha256 前缀作 map key，明文 secret 不进内存结构 / 不落日志。

type keyHealth struct {
	mu        sync.Mutex
	deadUntil map[string]time.Time
}

var kh = &keyHealth{deadUntil: map[string]time.Time{}}

func healthKey(provider, key string) string {
	sum := sha256.Sum256([]byte(provider + "|" + key))
	return provider + "|" + hex.EncodeToString(sum[:4])
}

func (h *keyHealth) mark(provider, key string, d time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.deadUntil[healthKey(provider, key)] = time.Now().Add(d)
}

func (h *keyHealth) revive(provider, key string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.deadUntil, healthKey(provider, key))
}

func (h *keyHealth) alive(provider, key string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	t, ok := h.deadUntil[healthKey(provider, key)]
	return !ok || t.Before(time.Now())
}

// key 轮换的死窗（Replace Magic Number with Symbolic Constant）：某 key 因这类失败
// 被标记不可用后，多久内不再尝试；成功调用即复活。
const (
	cooldownRateLimit   = 60 * time.Second // 429 限流/额度
	cooldownAuthFailure = 5 * time.Minute  // 401/403 认证被拒（多半是 key 真失效）
	cooldownServerErr   = 15 * time.Second // 5xx 服务端抖动
	cooldownDefault     = 15 * time.Second // 网络/其它
)

// classifyErr 单一判定错误类别（Consolidate Duplicate Conditional Fragments）：
// isRetryable 与 cooldownFor 过去各自做同一段 errors.As + switch，规则必须永远一致
// （否则会「判可重试但 cooldown=0」静默错配）；这里一处 switch 同时给出两个结论。
// 不可重试（400 坏请求、context 取消）立即终止，不空转。
func classifyErr(err error) (retryable bool, cooldown time.Duration) {
	if err == nil {
		return false, 0
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false, 0
	}
	var hs *HTTPStatusError
	if errors.As(err, &hs) {
		switch {
		case hs.Code == 429:
			return true, cooldownRateLimit // 限流/额度 → 换 key
		case hs.Code == 401 || hs.Code == 403:
			return true, cooldownAuthFailure // 认证被拒 → 换 key / 换 provider
		case hs.Code >= 500:
			return true, cooldownServerErr // 服务端错 → 换 key / 回退
		default:
			return false, 0 // 4xx 其它（含 400）→ 请求本身有问题，轮换无用
		}
	}
	// 网络/超时错误（*url.Error 等实现 net.Error）一律重试。
	return true, cooldownDefault
}

// isRetryable 判定错误是否应触发 key 轮换 / provider 回退。
func isRetryable(err error) bool {
	r, _ := classifyErr(err)
	return r
}

// cooldownFor 返回某失败 key 被标记为不可用的时间窗。
func cooldownFor(err error) time.Duration {
	_, d := classifyErr(err)
	return d
}

// sleepCtx 在 d 内等待，context 取消则提前返回 false。
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
