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
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/logos-42/hushfusion-forge/internal/ai"
	"github.com/logos-42/hushfusion-forge/internal/persona"
)

// ToolEvent 是一次工具调用的观测记录（TUI 在框外逐行展示；不含结果正文）。
type ToolEvent struct {
	Name     string
	Args     map[string]any
	Duration time.Duration
	OK       bool
	Err      string
}

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
	cfg        Config
	chat       ChatFunc
	chatStream ChatStreamFunc // 流式端口；nil 时 TurnStream 退化为一次性调用
	registry   *ToolRegistry
	nesting    int // 委派嵌套深度（0 = 根会话；>0 的子会话不再含 delegate 工具）
	messages   []Message

	// AllowWrites 是本次会话的写工具白名单（工具注册时的 Write=true 才受它约束）。
	AllowWrites map[string]bool

	// System 是 system 消息（skills 注入 + 指令，装配时放在最前）。
	System string

	// Persona 是人格/协作文档块（SOUL.md/AGENTS.md 等，经 persona.Bootstrap 合成）。
	// 它比 System 更权威：装配时拼在 System 之前（人格 > 指令）。
	Persona string

	// Goal 是当前长期目标（/goal 设置）。它不写进 messages 历史，
	// 而是在每次 assemble 时作为一条 system 消息动态注入，保证不会丢。
	Goal string

	// Compacted 记录发生过上下文压缩（真实发生过，不是假装无损）。
	Compacted bool

	// Stage 是当前生命周期状态。
	Stage Stage
	// LastError 是最近一次回合的错误（Stage=error 时可读；成功回合后清空）。
	LastError string
	// Turns 是已完成的回合数（审计 / 状态展示）。
	Turns int

	// Memory 是会话内记忆（KV；跨回合保留；可文件持久化）。
	Memory *MemStore
	// Tasks 是会话内目标/待办/计划存储（长期目标执行）。
	Tasks *TaskStore

	// LastUsage 是最近一次模型调用的**真实**用量（provider 未上报 ⇒ PromptTokens=0）。
	LastUsage ai.Usage
	// OnUsage 可选回调：每次调用成功后收到真实用量（TUI/serve 实时显示）。
	OnUsage func(ai.Usage)
	// OnTool 可选回调：每次工具调用完成后收到一条观测记录（TUI 框外逐行展示）。
	OnTool func(ToolEvent)

	// ledger 是会话累计用量；lastEstimate 是上次调用前的估算（校准 tokenizer 偏差）。
	ledger       ai.UsageLedger
	lastEstimate int

	// Skills 可选：每个回合按用户消息把命中技能注入 system（动态、按需，不是一次性全塞）。
	// nil = 不做技能注入。baseSystem 是构造时的静态 system（注入基于它，不叠加）。
	Skills     *SkillsDir
	baseSystem string

	// tok 是 token 计数器（缺省 LenDiv4 估算；可注入精确实现）。
	tok Tokenizer
}

// Usage 返回会话累计的真实用量（provider 未上报则为空账本）。
func (s *Session) Usage() ai.UsageLedger { return s.ledger }

// observeTool 把一次工具调用通知给 OnTool（TUI 在框外逐行展示）。
func (s *Session) observeTool(call ai.ToolCall, dur time.Duration, err error) {
	if s.OnTool == nil {
		return
	}
	ev := ToolEvent{Name: call.Function.Name, Duration: dur, OK: err == nil}
	if strings.TrimSpace(call.Function.Arguments) != "" {
		_ = json.Unmarshal([]byte(call.Function.Arguments), &ev.Args)
	}
	if ev.Args == nil {
		ev.Args = map[string]any{}
	}
	if err != nil {
		ev.Err = err.Error()
	}
	s.OnTool(ev)
}

// recordUsage 记录一次调用的真实用量（唯一入口，避免多处漏记）。
func (s *Session) recordUsage(u ai.Usage) {
	s.LastUsage = u
	s.ledger.Add(u)
	if s.OnUsage != nil {
		s.OnUsage(u)
	}
}

// tokenScale 是「真实 prompt_tokens / 估算 token」的校准系数。
//
// 为什么需要：estimateTokens 用 len/4，对中文（1 字≈1~2 token，UTF-8 3 字节）
// 会**严重低估**，导致压缩永远不触发、上下文一路撑爆 provider 上限。首次真调用
// 之后用实测比例修正估算，比换一个重 tokenizer 便宜得多。
//
// 荒谬值（<0.1 或 >20）不采信 —— 防脏数据把预算拉爆。
func (s *Session) tokenScale() float64 {
	if s.LastUsage.PromptTokens > 0 && s.lastEstimate > 0 {
		r := float64(s.LastUsage.PromptTokens) / float64(s.lastEstimate)
		if r >= 0.1 && r <= 20 {
			return r
		}
	}
	return 1
}

// NewSession 构造一个会话。
func NewSession(cfg Config, chat ChatFunc, reg *ToolRegistry, system string) *Session {
	return &Session{
		cfg:         cfg.withDefaults(),
		chat:        chat,
		registry:    reg,
		messages:    []Message{},
		System:      system,
		baseSystem:  system,
		AllowWrites: map[string]bool{}, // 缺省全只读；调用方可加白名单
		Stage:       StageIdle,
		Memory:      NewMemStore(),
		Tasks:       NewTaskStore(),
		tok:         orLenDiv4(cfg.Tokenizer),
	}
}

// SetStreamChat 注入流式模型端口。不调用时 TurnStream 退化为一次性调用
// （仍给最终一条 StreamContent，调用方无需区分实现）。
func (s *Session) SetStreamChat(f ChatStreamFunc) { s.chatStream = f }

// SetNesting 设置委派嵌套深度（子会话 = 父 +1）。>0 的会话不注册 delegate 工具，
// 从结构上杜绝「委派里再委派 → 自递归」的失控。
func (s *Session) SetNesting(n int) { s.nesting = n }

// orLenDiv4 缺省计数器（nil 接口用一个空实现的估算器兜底）。
func orLenDiv4(t Tokenizer) Tokenizer {
	if t == nil {
		return LenDiv4{}
	}
	return t
}

// TokenizerName 返回当前计数器的标识（审计/展示用；能看出是否精确）。
func (s *Session) TokenizerName() string { return orLenDiv4(s.tok).Name() }

// countText 计一段文本：注入的 tokenizer 失败则回退 len/4（不中断对话）。
func (s *Session) countText(text string) int {
	if t := s.tok; t != nil {
		if n, ok := t.Count(text); ok && n >= 0 {
			return n
		}
	}
	return len(text) / 4
}

// countMessages 计一轮对话历史（runtime Message 形状）。
func (s *Session) countMessages(msgs []Message) int {
	n := 0
	for _, m := range msgs {
		n += s.countText(m.Content)
		for _, tc := range m.ToolCalls {
			n += s.countText(tc.Function.Name) + s.countText(tc.Function.Arguments)
		}
	}
	return n
}

// countAI 计 assemble 后的请求体（含 system，另加每条消息的结构开销 4）。
func (s *Session) countAI(msgs []ai.Message) int {
	n := 0
	for _, m := range msgs {
		n += s.countText(m.Content) + 4
		for _, tc := range m.ToolCalls {
			n += s.countText(tc.Function.Name) + s.countText(tc.Function.Arguments)
		}
	}
	return n
}

// History 返回当前消息栈的浅拷贝（测试 / 审计用）。
func (s *Session) History() []Message { return append([]Message(nil), s.messages...) }

// Restore 用持久化的历史替换当前消息栈（跨重启恢复用）。
//
// 只校验角色合法（system 不进栈，由构造时的 System/Persona 承担）；tool_calls 与
// tool 回填的配对**不在这里重排** —— 历史怎么存的就怎么还，篡改过的历史会在下一
// 回合被 provider 明确拒绝，而不是我们悄悄"修好"成另一种对话。
func (s *Session) Restore(msgs []Message) error {
	clean := make([]Message, 0, len(msgs))
	for i, m := range msgs {
		switch m.Role {
		case RoleUser, RoleAssistant, RoleTool:
			clean = append(clean, m)
		default:
			return fmt.Errorf("agent: 恢复历史第 %d 条角色非法 %q", i, m.Role)
		}
	}
	s.messages = clean
	return nil
}

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

// estimateAIMessages 估算 assemble 后请求体的 token（含 system，与 estimateTokens
// 同口径 len/4）。用于和 provider 真实 prompt_tokens 对比，校准 tokenizer 偏差。
func estimateAIMessages(msgs []ai.Message) int {
	n := 0
	for _, m := range msgs {
		n += len(m.Content)/4 + 4
		for _, tc := range m.ToolCalls {
			n += len(tc.Function.Name)/4 + len(tc.Function.Arguments)/4
		}
	}
	return n
}

// compact 渐进压缩：
//   - 预算削减：单条工具结果超长直接截断（不删，保审计）；
//   - 摘要压缩：仍超预算 → 把最早的一段交给模型做摘要，保留摘要 + 最近消息；
//   - 摘要失败时才退回“丢最旧 + 诚实通知”，绝不假装无损。
//
// 返回被压缩掉的消息数（0 = 没压）。
func (s *Session) compact(ctx context.Context) int {
	// 阶段1：单条 tool 结果截断（预算削减，开销最低第一步）。
	for i := range s.messages {
		m := &s.messages[i]
		if m.Role == RoleTool && len(m.Content) > toolResultBudget {
			m.Content = m.Content[:toolResultBudget] + " …(截断)"
		}
	}
	budget := s.cfg.MaxBudgetTokens
	if budget <= 0 || len(s.messages) < 2 {
		return 0
	}
	// 预算判定用「估算 × 校准系数」：系数来自上一次真实 prompt_tokens / 当次估算，
	// 让中文这类被低估的上下文也能按**真实**预算压缩。
	if s.scaledCount(s.messages) <= budget {
		return 0
	}
	// 找到必须丢掉的前缀：保留的后缀尽量装进预算；至少保住最后一条。
	cut := 0
	for cut < len(s.messages)-1 && s.scaledCount(s.messages[cut:]) > budget {
		cut++
	}
	if cut <= 0 {
		return 0
	}
	// 不要把 tool 消息单独留在队首（它必须紧跟在带 tool_calls 的 assistant 后面）。
	for cut < len(s.messages)-1 && s.messages[cut].Role == RoleTool {
		cut++
	}
	dropped := append([]Message(nil), s.messages[:cut]...)
	s.messages = s.messages[cut:]

	summary, _ := s.summarize(ctx, dropped, budget)
	if strings.TrimSpace(summary) != "" {
		s.messages = append([]Message{{
			Role:    RoleUser,
			Content: "## 压缩摘要（较早轮次）\n" + summary + "\n\n" + compactNotice(),
		}}, s.messages...)
	} else {
		s.messages = append([]Message{{Role: RoleUser, Content: compactNotice()}}, s.messages...)
	}
	s.Compacted = true
	return len(dropped)
}

// Compress 手动压缩：把除最近 keepRecent 条以外的历史摘要掉。
// 返回被压缩的消息数、摘要文本；没有可压内容时返回 0。
func (s *Session) Compress(ctx context.Context, keepRecent int) (int, string, error) {
	if keepRecent < 1 {
		keepRecent = 4
	}
	if len(s.messages) <= keepRecent+1 {
		return 0, "", nil
	}
	cut := len(s.messages) - keepRecent
	for cut < len(s.messages)-1 && s.messages[cut].Role == RoleTool {
		cut++
	}
	dropped := append([]Message(nil), s.messages[:cut]...)
	summary, err := s.summarize(ctx, dropped, s.cfg.MaxBudgetTokens)
	if err != nil {
		return 0, "", err
	}
	if strings.TrimSpace(summary) == "" {
		summary = "（模型未返回摘要）"
	}
	s.messages = append([]Message{{
		Role:    RoleUser,
		Content: "## 压缩摘要（手动 /compress）\n" + summary + "\n\n" + compactNotice(),
	}}, s.messages[cut:]...)
	s.Compacted = true
	return len(dropped), summary, nil
}

// SetBudget 更新对话历史预算（/budget 命令；自动压缩下一轮生效）。
func (s *Session) SetBudget(n int) {
	if n > 0 {
		s.cfg.MaxBudgetTokens = n
	}
}

// Budget 返回当前对话历史预算。
func (s *Session) Budget() int { return s.cfg.MaxBudgetTokens }

func (s *Session) scaledCount(msgs []Message) int {
	return int(float64(s.countMessages(msgs)) * s.tokenScale())
}

const compactSummaryPrompt = `你是上下文压缩器。把下面的“较早对话”压缩成一份可继续工作的摘要：
- 保留：目标、已确认的决策与依据、关键证据/数字、改动过的文件路径、未完成任务；
- 删除：寒暄、重复内容、已失效的中间尝试；
- 不编造，不添加原文没有的事实；
- 用中文，300-600 字，直接输出摘要正文。`

// summarize 调用当前模型做摘要；失败返回 error，由 compact 回退到丢消息。
func (s *Session) summarize(ctx context.Context, msgs []Message, budget int) (string, error) {
	if s.chat == nil {
		return "", errors.New("agent: 没有可用的模型调用，无法生成摘要")
	}
	req := ai.ChatRequest{
		Model: s.cfg.Model,
		Messages: []ai.Message{
			{Role: "system", Content: compactSummaryPrompt},
			{Role: "user", Content: formatTranscript(msgs)},
		},
		MaxTokens: 600,
	}
	resp, err := s.chat(ctx, req)
	if err != nil {
		return "", err
	}
	if resp == nil {
		return "", errors.New("agent: 摘要模型返回 nil")
	}
	text := strings.TrimSpace(resp.Content)
	maxChars := 2000
	if budget > 0 && budget*3 < maxChars {
		maxChars = budget * 3
	}
	if maxChars < 120 {
		maxChars = 120
	}
	return clipRunes(text, maxChars), nil
}

// formatTranscript 把待压缩消息拼成文本；超长时保留头尾，避免摘要请求本身撑爆。
func formatTranscript(msgs []Message) string {
	var b strings.Builder
	for _, m := range msgs {
		content := strings.TrimSpace(m.Content)
		if content == "" && len(m.ToolCalls) == 0 {
			continue
		}
		if len([]rune(content)) > 1500 {
			content = clipRunes(content, 1500)
		}
		b.WriteString("[" + string(m.Role) + "] " + content + "\n")
		if len(m.ToolCalls) > 0 {
			var names []string
			for _, tc := range m.ToolCalls {
				names = append(names, tc.Function.Name)
			}
			b.WriteString("  (tool_calls: " + strings.Join(names, ", ") + ")\n")
		}
	}
	out := b.String()
	const maxTranscript = 24000
	r := []rune(out)
	if len(r) > maxTranscript {
		half := maxTranscript / 2
		out = string(r[:half]) + "\n…(中间省略)…\n" + string(r[len(r)-half:])
	}
	return out
}

func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

const toolResultBudget = 2000 // 单条工具结果截断上限（字符）

// AppendSystem 把一块静态文本并入基线 system（构造期用：记忆摘要、运行时告示）。
// 之后每个回合的技能注入都基于更新后的基线 —— 直接改 s.System 会被下一回合的
// 技能注入覆盖掉（baseSystem 才是持久基线），所以必须走这里。
func (s *Session) AppendSystem(block string) {
	if strings.TrimSpace(block) == "" {
		return
	}
	s.baseSystem = strings.TrimSpace(s.baseSystem) + "\n\n" + strings.TrimSpace(block)
	s.System = s.baseSystem
}

// SetGoal 设置长期目标；空串表示清除。每轮 assemble 动态注入，不占历史消息。
func (s *Session) SetGoal(goal string) { s.Goal = strings.TrimSpace(goal) }

// assemble 组装一次模型调用的消息（学 Claude Code 的 assemble() 注入点）：
// system（persona 优先，随后 skills/指令）在最前，工具 schema 附在请求上，
// 对话历史原样。
func (s *Session) assemble() []ai.Message {
	out := make([]ai.Message, 0, 1+len(s.messages))
	sys := s.systemText()
	if strings.TrimSpace(s.Goal) != "" {
		goal := "## 当前长期目标（每轮都要对齐）\n" + s.Goal +
			"\n\n推进目标时每轮只做可验证的一步；完成时在最终回复末尾单独一行输出 `GOAL_DONE: <证据>`。"
		if strings.TrimSpace(sys) == "" {
			sys = goal
		} else {
			sys = strings.TrimSpace(sys) + "\n\n" + goal
		}
	}
	if strings.TrimSpace(sys) != "" {
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
	// 技能注入：按本条用户消息匹配（动态）。基于 baseSystem 重算，避免多轮反复叠加。
	if s.Skills != nil {
		s.System = s.Skills.Inject(userMsg, s.baseSystem)
	}
	if s.compact(ctx) > 0 {
		// 压缩发生：追加说明（去重 —— 模型知道一次即可，重复追加只会吃预算）。
		// 说明作为最后一条 user 消息的追加（不插在队首，避免打破 system→user 顺序）。
		if !s.hasNotice() {
			s.messages = append(s.messages, Message{Role: RoleUser, Content: compactNotice()})
		}
	}
	// 失败回滚点（base = 当前 user 消息之后）：回合中途 fail-closed（模型调用失败 /
	// 工具往返上限 / ctx 中断）时，已 append 的 assistant/tool 配对是**不完整的失败轮次**，
	// 留在消息栈会让下一回合把过期工具结果喂给模型（配对存储反而放大污染）。
	// 回滚 = 截断回 base，只保留 user（+压缩通知），下一回合从干净状态重试。
	base := len(s.messages)
	failTurn := func(err error) (string, error) {
		s.messages = s.messages[:base]
		return "", s.fail(err)
	}

	for turn := 0; turn < s.cfg.MaxToolTurns; turn++ {
		if err := ctx.Err(); err != nil {
			return failTurn(errors.New("回合被 context 中断: " + err.Error()))
		}
		// 模型调用：超时 + 失败重试（瞬时错误恢复；超时不等重试，直接失败）。
		resp, err := s.chatWithRetry(ctx)
		if err != nil {
			return failTurn(errors.New("模型调用失败: " + err.Error()))
		}
		if resp == nil {
			return failTurn(errors.New("模型返回 nil 响应"))
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
			start := time.Now()
			result, err := s.registry.Execute(ctx, call, s.AllowWrites)
			s.observeTool(call, time.Since(start), err)
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
	return failTurn(errors.New(fmt.Sprintf("工具往返达到上限 %d 轮仍未产出最终回复（fail-closed，不编造完成）", s.cfg.MaxToolTurns)))
}

// TurnStream 是 Turn 的流式版：模型回复与工具调用的增量通过 onDelta 实时回调。
// 后处理（工具执行、预算、压缩、回滚）与 Turn 完全一致，只是模型调用换成流式。
// onDelta 可为 nil（等价 Turn）。流式端口由 SetStreamChat 注入；未注入时
// 退化为一次性调用，仍发最终一条 StreamContent。
func (s *Session) TurnStream(ctx context.Context, userMsg string, onDelta func(ai.StreamEvent)) (string, error) {
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
	if s.Skills != nil {
		s.System = s.Skills.Inject(userMsg, s.baseSystem)
	}
	if s.compact(ctx) > 0 {
		if !s.hasNotice() {
			s.messages = append(s.messages, Message{Role: RoleUser, Content: compactNotice()})
		}
	}
	base := len(s.messages)
	failTurn := func(err error) (string, error) {
		s.messages = s.messages[:base]
		return "", s.fail(err)
	}

	for turn := 0; turn < s.cfg.MaxToolTurns; turn++ {
		if err := ctx.Err(); err != nil {
			return failTurn(errors.New("回合被 context 中断: " + err.Error()))
		}
		resp, err := s.streamOnce(ctx, onDelta)
		if err != nil {
			return failTurn(errors.New("模型调用失败: " + err.Error()))
		}
		if resp == nil {
			return failTurn(errors.New("模型返回 nil 响应"))
		}
		assistantMsg := Message{Role: RoleAssistant, Content: resp.Content}
		toolCalls := resp.ToolCalls
		if len(toolCalls) == 0 {
			s.messages = append(s.messages, assistantMsg)
			s.Stage = StageDone
			s.Turns++
			return resp.Content, nil
		}
		assistantMsg.ToolCalls = toolCalls
		s.messages = append(s.messages, assistantMsg)
		if len(toolCalls) > s.cfg.MaxToolResults {
			toolCalls = toolCalls[:s.cfg.MaxToolResults]
		}
		for _, call := range toolCalls {
			start := time.Now()
			result, e := s.registry.Execute(ctx, call, s.AllowWrites)
			s.observeTool(call, time.Since(start), e)
			content := result
			if e != nil {
				content = "工具失败（回灌给模型自愈）: " + e.Error()
			}
			s.messages = append(s.messages, Message{
				Role:       RoleTool,
				Content:    content,
				ToolCallID: call.ID,
			})
		}
	}
	return failTurn(errors.New(fmt.Sprintf("工具往返达到上限 %d 轮仍未产出最终回复（fail-closed，不编造完成）", s.cfg.MaxToolTurns)))
}

// streamOnce 是 chatWithRetry 的流式版：转发增量事件给 onDelta，并记录真实用量。
func (s *Session) streamOnce(ctx context.Context, onDelta func(ai.StreamEvent)) (*ai.ChatResponse, error) {
	msgs := s.assemble()
	s.lastEstimate = s.countAI(msgs)
	// 没注入流式端口 ⇒ 退化：一次性调用，把最终内容作为一条增量发出来。
	f := s.chatStream
	if f == nil {
		f = func(c context.Context, req ai.ChatRequest, on func(ai.StreamEvent)) (*ai.ChatResponse, error) {
			resp, err := s.chat(c, req)
			if err != nil {
				return nil, err
			}
			if on != nil {
				on(ai.StreamEvent{Type: ai.StreamContent, Text: resp.Content})
			}
			return resp, nil
		}
	}
	resp, err := f(ctx, ai.ChatRequest{
		Model:    s.cfg.Model,
		Messages: msgs,
		Tools:    s.registry.Specs(),
	}, onDelta)
	if err == nil && resp != nil {
		s.recordUsage(resp.Usage)
	}
	return resp, err
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
		msgs := s.assemble()
		// 记录调用前的计数（精确 tokenizer 或估算），供 tokenScale 校准偏差。
		s.lastEstimate = s.countAI(msgs)
		resp, err := s.chat(callCtx, ai.ChatRequest{
			Model:    s.cfg.Model,
			Messages: msgs,
			Tools:    s.registry.Specs(),
		})
		cancel()
		if err == nil {
			if resp != nil {
				s.recordUsage(resp.Usage) // 真实用量入账（provider 未上报则全 0）
			}
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
		if m.Role == RoleUser && (m.Content == compactNotice() ||
			strings.Contains(m.Content, "压缩丢弃") || strings.Contains(m.Content, "压缩摘要")) {
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
	s.System = s.baseSystem // 技能注入跟随回合重算，reset 回到静态基线
}
