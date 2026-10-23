package agent

// loop.go —— 连续对话的回合循环（学 Claude Code queryLoop 的 Go 版）。
//
// 单回合（一个 user 消息）的循环：
//
//	for turn := 0; turn < MaxToolTurns; turn++ {
//	    resp := chat(model, assemble(messages))   // model()
//	    if len(resp.tool_calls) == 0 { return resp }  // 最终回复
//	    for each call: result := registry.Execute(call)   // execute()
//	    messages += assistant(tool_calls) + tool(results)  // 回填
//	}
//	若工具往返耗尽 → fail-closed 报错（不编造"已经完成"）。
//
// 设计要点：
//   - assistant 消息与其 tool 消息**配对**存储，回放保持 OpenAI 协议顺序；
//   - 多 tool_calls 并行执行，结果按调用 id 回填（工具风暴有 MaxToolResults 上限）；
//   - 工具失败不回滚、不自动重试：错误文本原样回灌给模型自愈（学 aios 的 history 回灌）。

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/logos-42/hushfusion-forge/internal/ai"
	"github.com/logos-42/hushfusion-forge/internal/persona"
)

// Stage 是会话生命周期状态（供 TUI / serve 展示；任何时刻只在一个状态）。
type Stage int

const (
	// StageIdle 空闲：等待用户输入。
	StageIdle Stage = iota
	// StageThinking 模型调用中（含工具往返）。
	StageThinking
	// StageDone 回合完成（有最终回复）。
	StageDone
	// StageError 回合失败（fail-closed 报错；会话保留可继续）。
	StageError
)

func (st Stage) String() string {
	switch st {
	case StageIdle:
		return "idle"
	case StageThinking:
		return "thinking"
	case StageDone:
		return "done"
	case StageError:
		return "error"
	}
	return "unknown"
}

// Session 是一次连续对话的状态：消息栈 + 关联的 registry/chat/白名单。
//
// 端口注入：Chat 与 Tools 在构造时注入（测试用假实现 / 假工具，零网络），
// AllowWrites 是本次会话允许 AI 自主发起的写工具白名单（缺省空 = 全部只读）。
type Session struct {
	cfg      Config
	chat     ChatFunc
	registry *ToolRegistry
	messages []Message

	// AllowWrites 是本次会话的写工具白名单（工具注册时的 Write=true 才受它约束）。
	AllowWrites map[string]bool

	// System 是 system 消息（skills 注入 + 指令，装配时放在最前）。
	System string

	// Persona 是人格/协作文档块（SOUL.md/AGENTS.md 等，经 persona.Bootstrap 合成）。
	// 它比 System 更权威：装配时拼在 System 之前（人格 > 指令）。
	Persona string

	// Compacted 记录发生过上下文压缩（真实发生过，不是假装无损）。
	Compacted bool

	// Stage 是当前生命周期状态。
	Stage Stage
	// LastError 是最近一次回合的错误（Stage=error 时可读；成功回合后清空）。
	LastError string
	// Turns 是已完成的回合数（审计 / 状态展示）。
	Turns int

	// Memory 是会话内记忆（KV；跨回合保留；预留持久化）。
	Memory *MemStore
	// Tasks 是会话内目标/待办/计划存储（长期目标执行）。
	Tasks *TaskStore
}

// NewSession 构造一个会话。
func NewSession(cfg Config, chat ChatFunc, reg *ToolRegistry, system string) *Session {
	return &Session{
		cfg:         cfg.withDefaults(),
		chat:        chat,
		registry:    reg,
		messages:    []Message{},
		System:      system,
		AllowWrites: map[string]bool{}, // 缺省全只读；调用方可加白名单
		Stage:       StageIdle,
		Memory:      NewMemStore(),
		Tasks:       NewTaskStore(),
	}
}

// History 返回当前消息栈的浅拷贝（测试 / 审计用）。
func (s *Session) History() []Message { return append([]Message(nil), s.messages...) }

// Len 返回消息条数。
func (s *Session) Len() int { return len(s.messages) }

// CompactedReported 是否发生过压缩。
func (s *Session) CompactedReported() bool { return s.Compacted }

// estimateTokens 用 len/4 估算文本 token（诚实边界：不是 provider tokenizer，
// 精确估算预留槽；见设计文档 §3）。
func estimateTokens(msgs []Message) int {
	n := 0
	for _, m := range msgs {
		n += len(m.Content) / 4
		for _, tc := range m.ToolCalls {
			n += len(tc.Function.Name)/4 + len(tc.Function.Arguments)/4
		}
	}
	return n
}

// compact 渐进压缩（学 Claude Code 5 层压缩的最简两段）：
//   - 预算削减：单条工具结果超长直接截断（不删，保审计）；
//   - 历史裁剪：仍超预算 → 丢最旧轮次（v0.1 无智能摘要，诚实标记 Compacted）。
//
// 返回被压缩掉的消息数（0 = 没压）。
func (s *Session) compact() int {
	removed := 0
	// 阶段1：单条 tool 结果截断（预算削减，开销最低第一步）。
	for i := range s.messages {
		m := &s.messages[i]
		if m.Role == RoleTool && len(m.Content) > toolResultBudget {
			m.Content = m.Content[:toolResultBudget] + " …(截断)"
		}
	}
	// 阶段2：整体估算超预算 → 从最旧逐条丢（保留最后一条 = 当前 user 消息）。
	// 注意：system 不占 messages 栈（是 Session.System 单独字段，assemble 时注入），
	// 所以这里所有消息都可丢，只是必须保住最后一条让模型有东西可回应。
	for estimateTokens(s.messages) > s.cfg.MaxBudgetTokens && len(s.messages) > 1 {
		dropped := false
		for i := 0; i < len(s.messages)-1; i++ {
			m := s.messages[i]
			j := i
			// 丢 assistant 时，连带它后面紧跟的 tool 回填一起丢（保持配对完整性）。
			if m.Role == RoleAssistant && i+1 < len(s.messages) && s.messages[i+1].Role == RoleTool {
				j = i + 1
			}
			// 不能越过最后一条（当前 user）。
			if j > len(s.messages)-2 {
				j = len(s.messages) - 2
			}
			if j < i {
				continue
			}
			s.messages = append(s.messages[:i], s.messages[j+1:]...)
			removed += j - i + 1
			dropped = true
			break
		}
		if !dropped {
			break
		}
	}
	if removed > 0 {
		s.Compacted = true
	}
	return removed
}

const toolResultBudget = 2000 // 单条工具结果截断上限（字符）

// assemble 组装一次模型调用的消息（学 Claude Code 的 assemble() 注入点）：
// system（persona 优先，随后 skills/指令）在最前，工具 schema 附在请求上，
// 对话历史原样。
func (s *Session) assemble() []ai.Message {
	out := make([]ai.Message, 0, 1+len(s.messages))
	if sys := s.systemText(); sys != "" {
		out = append(out, ai.Message{Role: "system", Content: sys})
	}
	for _, m := range s.messages {
		am := ai.Message{Role: string(m.Role), Content: m.Content}
		if len(m.ToolCalls) > 0 {
			am.ToolCalls = m.ToolCalls
		}
		am.ToolCallID = m.ToolCallID
		out = append(out, am)
	}
	return out
}

// systemText 合成最终 system 文本：persona（人格/协约）优先于 System（指令）。
// 单一来源 —— 避免"persona 注入了但 assemble 忘了拼"的静默失效。
func (s *Session) systemText() string {
	switch {
	case s.Persona == "":
		return s.System
	case s.System == "":
		return s.Persona
	default:
		return s.Persona + "\n\n" + s.System
	}
}

// WithPersona 从 root 发现 persona/agent 文档（SOUL.md/AGENTS.md/…）并注入。
// root 为空用 cwd；目录里没有任何文档不是错误（此时 Persona 保持空）。
// 注入是"锦上添花"：失败由调用方决定是否致命，本方法只如实回报。
func (s *Session) WithPersona(root string, budget int) error {
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	block, err := persona.Bootstrap(root, budget)
	if err != nil {
		return fmt.Errorf("agent: 载入 persona 文档失败: %w", err)
	}
	s.Persona = block
	return nil
}

// Turn 处理一个 user 消息：整轮循环直到模型最终回复或预算耗尽。
//
// 返回最终回复文本（角色 assistant，无 tool_calls）。任何工具往返界的 fail-closed
// 都以 error 返回 —— 调用方（CLI/TUI/serve）决定怎么展示，绝不假装完成。
func (s *Session) Turn(ctx context.Context, userMsg string) (string, error) {
	if strings.TrimSpace(userMsg) == "" {
		return "", errors.New("agent: user 消息为空")
	}
	s.Stage = StageThinking
	s.LastError = ""
	defer func() {
		if s.Stage == StageThinking {
			s.Stage = StageIdle
		}
	}()
	s.messages = append(s.messages, Message{Role: RoleUser, Content: userMsg})
	if s.compact() > 0 {
		// 压缩发生：追加说明（去重 —— 模型知道一次即可，重复追加只会吃预算）。
		// 说明作为最后一条 user 消息的追加（不插在队首，避免打破 system→user 顺序）。
		if !s.hasNotice() {
			s.messages = append(s.messages, Message{Role: RoleUser, Content: compactNotice()})
		}
	}

	for turn := 0; turn < s.cfg.MaxToolTurns; turn++ {
		if err := ctx.Err(); err != nil {
			return "", s.fail(errors.New("回合被 context 中断: " + err.Error()))
		}
		// 模型调用：超时 + 失败重试（瞬时错误恢复；超时不等重试，直接失败）。
		resp, err := s.chatWithRetry(ctx)
		if err != nil {
			return "", s.fail(errors.New("模型调用失败: " + err.Error()))
		}
		if resp == nil {
			return "", s.fail(errors.New("模型返回 nil 响应"))
		}
		assistantMsg := Message{Role: RoleAssistant, Content: resp.Content}
		// tool_calls 直接读响应字段（ai.Chat 扩展后，OpenAI 协议）。
		toolCalls := resp.ToolCalls
		if len(toolCalls) == 0 {
			// 最终回复：落栈返回。
			s.messages = append(s.messages, assistantMsg)
			s.Stage = StageDone
			s.Turns++
			return resp.Content, nil
		}
		// 有工具调用：assistant 落栈（带 tool_calls），执行并回填 tool 消息。
		assistantMsg.ToolCalls = toolCalls
		s.messages = append(s.messages, assistantMsg)
		if len(toolCalls) > s.cfg.MaxToolResults {
			toolCalls = toolCalls[:s.cfg.MaxToolResults]
		}
		for _, call := range toolCalls {
			result, err := s.registry.Execute(ctx, call, s.AllowWrites)
			content := result
			if err != nil {
				content = "工具失败（回灌给模型自愈）: " + err.Error()
			}
			s.messages = append(s.messages, Message{
				Role:       RoleTool,
				Content:    content,
				ToolCallID: call.ID,
			})
		}
		// 循环继续 → 模型看到工具结果，决定下一步（继续调工具或最终回复）。
	}
	return "", s.fail(errors.New(fmt.Sprintf("工具往返达到上限 %d 轮仍未产出最终回复（fail-closed，不编造完成）", s.cfg.MaxToolTurns)))
}

// chatWithRetry 带超时与瞬时重试的模型调用。
// 超时（ModelTimeout）直接失败（挂死不重试）；网络/5xx 等错误按 ModelRetries 重试。
func (s *Session) chatWithRetry(ctx context.Context) (*ai.ChatResponse, error) {
	var lastErr error
	attempts := 1 + s.cfg.ModelRetries
	for i := 0; i < attempts; i++ {
		callCtx := ctx
		cancel := func() {}
		if s.cfg.ModelTimeout > 0 {
			callCtx, cancel = context.WithTimeout(ctx, s.cfg.ModelTimeout)
		}
		resp, err := s.chat(callCtx, ai.ChatRequest{
			Model:    s.cfg.Model,
			Messages: s.assemble(),
			Tools:    s.registry.Specs(),
		})
		cancel()
		if err == nil {
			return resp, nil
		}
		lastErr = err
		// 超时 / context 取消 → 不重试（挂死或用户中断，重试只会更慢）。
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return nil, err
		}
	}
	return nil, lastErr
}

// fail 把错误记录到会话（Stage=error + LastError），返回带 agent 前缀的错误。
func (s *Session) fail(err error) error {
	s.Stage = StageError
	s.LastError = err.Error()
	return fmt.Errorf("agent: %v", err)
}

// hasNotice 消息栈里是否已有压缩说明。
func (s *Session) hasNotice() bool {
	for _, m := range s.messages {
		if m.Role == RoleUser && m.Content == compactNotice() {
			return true
		}
	}
	return false
}

// compactNotice 是压缩后的说明（诚实边界：模型必须知道它看不到最早轮次）。
// 保持短小 —— 它本身也占 token 预算（len/4 估算，中文字符按 3 字节算）。
func compactNotice() string {
	return "（较早轮次已被压缩丢弃）"
}

// Reset 清空会话（保留 system 与白名单）。
func (s *Session) Reset() {
	s.messages = nil
	s.Compacted = false
}
