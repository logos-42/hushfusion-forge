// Package aios 是 phase-3「AI 粘合」层：把 AI 接到 HUSHFUSION OS 宏内核上。
//
// 架构落点（docs/wiki/os-kernel-architecture.md phase-3）：AI 经 oskernel 的
// **唯一受控界面**操作决策层——读状态、按受控的系统调用集提出动作，由内核执行
// （写前不变量守卫）。这正是「AI 接入后 OS 使用 kernel 控制执行」的落地。
//
// 纪律（与 OS 同一套，且被代码强制）：
//   - AI 只「提议」，不判「成立/完成」：允许的写系统调用只有 propose/add/new/advance；
//     没有任何把任务标成 active/done 的系统调用——active→done 仍只由人经
//     ossubmit --settle 执行。内核在结构上就剥夺了 AI「自称完成」的能力。
//   - fail-closed：AI 输出不是合法 JSON 计划 ⇒ 拒绝执行；未知/越权系统调用 ⇒ 拒绝。
//   - 端口注入：ChatFunc 可注入（测试用假实现，零网络）；默认走 internal/ai。
package aios

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/logos-42/hushfusion-forge/internal/ai"
	"github.com/logos-42/hushfusion-forge/internal/oskernel"
)

// LoopPolicy 收敛 AI 自愈循环的四个预算参数（Introduce Parameter Object：
// 这四个 int 总是一起出现、一起设默认，属 Data Clumps）。
type LoopPolicy struct {
	MaxSteps    int // 自愈循环最大轮数
	JSONRetries int // 单轮 AI 输出非合法 JSON 时的最大重试
	MaxRepeats  int // 同一被拒步骤重复提议的循环检测阈值
	MaxCalls    int // 单次计划允许的最大写系统调用数（防失控）
}

// 循环预算的命名常量（Replace Magic Number with Symbolic Constant）：
// 同一语义的默认值曾散落 5 处，收拢到此。
const (
	DefaultMaxSteps    = 6
	DefaultJSONRetries = 3
	DefaultMaxRepeats  = 3
	DefaultMaxCalls    = 8
)

// DefaultLoopPolicy 返回带默认值的循环策略。
func DefaultLoopPolicy() LoopPolicy {
	return LoopPolicy{DefaultMaxSteps, DefaultJSONRetries, DefaultMaxRepeats, DefaultMaxCalls}
}

// withDefaults 把未设（<=0）的字段补默认（单一处，消灭四段同构 if<=0）。
func (p LoopPolicy) withDefaults() LoopPolicy {
	if p.MaxSteps <= 0 {
		p.MaxSteps = DefaultMaxSteps
	}
	if p.JSONRetries <= 0 {
		p.JSONRetries = DefaultJSONRetries
	}
	if p.MaxRepeats <= 0 {
		p.MaxRepeats = DefaultMaxRepeats
	}
	if p.MaxCalls <= 0 {
		p.MaxCalls = DefaultMaxCalls
	}
	return p
}

// Agent 是一个粘合到 OS 内核的 AI 操作器。
type Agent struct {
	Kernel   *oskernel.Kernel
	Provider string // ai provider 名（见 internal/ai）
	Model    string // 空 = 用 provider 默认模型
	BaseURL  string // 可选覆盖（custom 代理/网关）
	// Loop 是 AI 自愈循环预算（多步循环 + JSON 重试 + 循环检测 + 单次上限）。
	Loop LoopPolicy

	// ChatFunc 注入对话函数（默认走 internal/ai；测试注入假实现）。
	ChatFunc func(ctx context.Context, system, user string) (string, error)

	// OnUsage 可选回调：每次对话成功后收到 provider 上报的用量（TUI 实时显示用）。
	// ChatFunc 注入假实现时不会触发（假实现没有 usage 可报）。
	OnUsage func(ai.Usage)

	// ExtraSystem 追加到内置 system 提示之后（如持久记忆块、persona 文档）。
	// 空 = 只用内置提示。内置提示与追加块之间空一行，避免粘成一句。
	ExtraSystem string

	// usage 累计本次运行的用量；modelUsed 是 provider 实际回报的模型名。
	usage     ai.UsageLedger
	modelUsed string
}

// NewAgent 构造 AI 操作器。内置 provider 目录幂等注册（让 aios 独立可用，
// 不依赖调用方先 RegisterBuiltins）。
func NewAgent(k *oskernel.Kernel, provider, model, baseURL string) *Agent {
	_ = ai.RegisterBuiltins() // 幂等；后注册覆盖同名，内置只是兜底
	return &Agent{Kernel: k, Provider: provider, Model: model, BaseURL: baseURL, Loop: DefaultLoopPolicy()}
}

// allowedWrites 是 AI 唯一被允许发起的写系统调用（对齐内核写驱动）。
// 读系统调用不在此列（AI 读状态不走"计划执行"通道）。
var allowedWrites = map[string]bool{
	"tasks.propose":       true,
	"opportunities.add":   true,
	"decisions.add":       true,
	"experiments.new":     true,
	"experiments.advance": true,
}

// PlanStep 是 AI 计划里的一步（含准入判定）。
type PlanStep struct {
	Syscall string
	Args    map[string]any
	Allowed bool
	Reason  string
}

// ExecResult 是一次系统调用的执行结果。
type ExecResult struct {
	Syscall string
	// Args 保留本次调用的参数，供 TUI 在框外逐行展示（命令/文件/skill 等）。
	Args map[string]any
	// Duration 是内核执行耗时（dry-run / 未执行时为 0）。
	Duration time.Duration
	OK       bool
	Message  string
	Err      string
}

// Report 是一次 AI 粘合运行的完整记录（可核对）。
type Report struct {
	Goal string
	// Answer 是「没有可执行动作」时补问一次普通对话得到的文字回复
	// （TUI 用于聊天式回显；规划路径为空）。
	Answer string
	// Model 是 provider **实际回报**的模型名（resp.Model）；空 = 未知。
	// 成本按它查价目（配置里的 Model 可能为空/别名）。
	Model   string
	Plan    []PlanStep
	Results []ExecResult
	Notes   []string
	// Usage 是本次运行累计的真实 token 用量（provider 上报）。
	// 注入假 ChatFunc 时为 0 —— 那是"没上报"，不是"没用 token"。
	Usage ai.UsageLedger
}

// Run 读 OS 状态 → 多步自愈循环让 AI 出计划并校正 → （非 dry-run）经内核执行。
//
// 学 Hermes 智能体循环：AI 只「提议」，内核执行；当某步被拒/执行失败（或 AI 输出
// 非合法 JSON），把原因回灌给 AI 让它自愈重提，直到全部接纳 / 返回空计划 / 达步数
// 上限 / 检测到循环 / ctx 取消。所有「自愈失败」均以 fail-closed 收场（不假装成功）。
//
// 结构（Extract Function + Guard Clauses）：Run 只留循环骨架，每轮逻辑在 runRound，
// 步骤执行在 applySteps，收敛判定在 converge——原长函数拆成若干小函数。
func (a *Agent) Run(ctx context.Context, goal string, dryRun bool) (*Report, error) {
	if strings.TrimSpace(goal) == "" {
		return nil, fmt.Errorf("aios: goal 为空")
	}
	if a.ChatFunc == nil {
		a.ChatFunc = a.defaultChat
	}
	// 每次 Run 重置用量账本（Report 只反映本次运行）。
	a.usage = ai.UsageLedger{}
	a.modelUsed = ""
	state, err := a.readState()
	if err != nil {
		return nil, fmt.Errorf("aios: 读 OS 状态失败: %w", err)
	}
	rs := &runState{
		policy:   a.Loop.withDefaults(),
		rep:      &Report{Goal: goal},
		state:    state,
		seen:     map[string]int{},
		executed: map[string]bool{},
	}
	for step := 0; step < rs.policy.MaxSteps; step++ {
		if err := ctx.Err(); err != nil {
			return rs.rep, fmt.Errorf("aios: 循环被 context 中断: %w", err)
		}
		done, err := a.runRound(ctx, rs, dryRun)
		if err != nil {
			return rs.rep, err
		}
		if done {
			break
		}
	}
	finalize(rs)
	rs.rep.Usage = a.usage
	rs.rep.Model = a.modelUsed
	if rs.rep.Model == "" {
		rs.rep.Model = a.Model // provider 没回报时退回配置值（可能为空）
	}
	return rs.rep, nil
}

// runState 是一次 Run 的自愈循环可变状态（避免在函数间传一串局部变量）。
type runState struct {
	policy      LoopPolicy
	rep         *Report
	state       string          // OS 只读快照
	history     []string        // 回灌给 AI 的反馈轮次（被拒/失败原因）
	seen        map[string]int  // (syscall,args) 归一化 → 出现次数（循环检测）
	executed    map[string]bool // 已执行的步（去重）
	jsonRetried int             // JSON 重试计数
}

// runRound 执行一轮 AI 回合：问一次 → 解析 → 校验/执行 → 判定是否继续自愈。
// 返回 done=true 表示收敛（Run 应停止循环）。
func (a *Agent) runRound(ctx context.Context, rs *runState, dryRun bool) (done bool, err error) {
	raw, err := a.ChatFunc(ctx, a.systemPrompt(), buildUser(rs.rep.Goal, rs.state, rs.history))
	if err != nil {
		return false, fmt.Errorf("aios: AI 对话失败: %w", err)
	}
	steps, perr := parsePlan(raw)
	if perr != nil {
		return a.retryJSON(rs, perr)
	}
	rejections, progress, loopErr := a.applySteps(rs, steps, dryRun)
	if loopErr != nil {
		return false, loopErr // 循环检测 → fail-closed
	}
	return converge(rs, steps, rejections, progress)
}

// retryJSON 处理「AI 输出非合法 JSON」：附解析错误回灌、重问，直至预算耗尽。
func (a *Agent) retryJSON(rs *runState, perr error) (done bool, err error) {
	rs.jsonRetried++
	if rs.jsonRetried > rs.policy.JSONRetries {
		return false, fmt.Errorf("aios: AI 连续 %d 次未输出合法 JSON 步骤数组，拒绝执行: %w", rs.jsonRetried, perr)
	}
	rs.history = append(rs.history, fmt.Sprintf("⚠️ 你上一轮的回复不是合法 JSON 步骤数组（解析错误：%s）。请只输出 JSON 数组，不要解释、不要围栏之外多余文字。", perr))
	rs.rep.Notes = append(rs.rep.Notes, "AI 输出非合法 JSON：已要求重试并附错误原因")
	return false, nil // 继续自愈循环
}

// applySteps 校验并执行本轮步骤；返回被拒/失败原因（供回灌）与是否有进展。
func (a *Agent) applySteps(rs *runState, steps []rawStep, dryRun bool) (rejections []string, progress bool, err error) {
	for _, s := range steps {
		key := stepKey(s)
		rs.seen[key]++
		if rs.seen[key] >= rs.policy.MaxRepeats {
			return nil, false, fmt.Errorf("aios: 检测到循环——步骤 %q 被重复提议 %d 次且无法自愈，fail-closed", s.Syscall, rs.seen[key])
		}
		ps := a.classifyStep(s, rs.policy.MaxCalls, countAllowed(rs.rep))
		if rs.seen[key] == 1 { // 去重：每份独特提案只在 Plan 记一次
			rs.rep.Plan = append(rs.rep.Plan, ps)
		}
		if !ps.Allowed {
			rejections = append(rejections, fmt.Sprintf("- %s 被拒：%s（参数：%v）", s.Syscall, ps.Reason, s.Args))
			if rs.seen[key] == 1 {
				rs.rep.Results = append(rs.rep.Results, ExecResult{Syscall: s.Syscall, Args: s.Args, OK: false, Err: "拒绝执行: " + ps.Reason})
			}
			continue
		}
		if rs.executed[key] {
			continue // 已执行过，跳过去重
		}
		if !dryRun {
			start := time.Now()
			res, e := a.Kernel.Syscall(s.Syscall, s.Args)
			dur := time.Since(start)
			if e != nil {
				rejections = append(rejections, fmt.Sprintf("- %s 执行失败：%s（参数：%v）", s.Syscall, e.Error(), s.Args))
				if rs.seen[key] == 1 {
					rs.rep.Results = append(rs.rep.Results, ExecResult{Syscall: s.Syscall, Args: s.Args, Duration: dur, OK: false, Err: e.Error()})
				}
				continue
			}
			if rs.seen[key] == 1 {
				rs.rep.Results = append(rs.rep.Results, ExecResult{Syscall: s.Syscall, Args: s.Args, Duration: dur, OK: res.OK, Message: res.Message})
			}
		}
		rs.executed[key] = true
		progress = true
	}
	return rejections, progress, nil
}

// converge 判定本轮是否收敛（Decompose Conditional：四个分支一目了然）。
func converge(rs *runState, steps []rawStep, rejections []string, progress bool) (done bool, err error) {
	switch {
	case len(rejections) > 0:
		// 自愈：回灌被拒/失败原因，让 AI 下一轮修正（只输出待修正步骤，或 []）。
		rs.history = append(rs.history, "以下步骤被内核拒绝或执行失败，请修正后重新提议（只输出待修正的步骤数组；若已无修正空间，输出 []）：\n"+strings.Join(rejections, "\n"))
		rs.rep.Notes = append(rs.rep.Notes, "部分步骤被拒/失败：已回灌原因让 AI 自愈")
		return false, nil
	case len(steps) == 0:
		rs.rep.Notes = append(rs.rep.Notes, "AI 返回空计划，收敛。")
		return true, nil
	case !progress:
		rs.rep.Notes = append(rs.rep.Notes, "无新可执行动作（已执行的步被重复提议），收敛。")
		return true, nil
	default:
		// 本轮全部接纳且有进展 → 收敛（不无限追问；多轮推进由自愈触发）。
		rs.rep.Notes = append(rs.rep.Notes, "本轮计划全部接纳，收敛。")
		return true, nil
	}
}

// finalize 收尾：补齐报告的说明性 notes。
func finalize(rs *runState) {
	if len(rs.rep.Plan) == 0 {
		rs.rep.Notes = append(rs.rep.Notes, "AI 未提出任何动作。")
	}
	rs.rep.Notes = append(rs.rep.Notes, "以上为 AI 提议经内核执行的结果；科学结论/完成判定仍归 leo 或独立反馈。")
}

// classifyStep 判定单步是否准入（对齐 allowedWrites + MaxCalls + validateArgs）。
func (a *Agent) classifyStep(s rawStep, maxCalls, allowedSoFar int) PlanStep {
	ps := PlanStep{Syscall: s.Syscall, Args: s.Args}
	switch {
	case !allowedWrites[s.Syscall]:
		ps.Allowed = false
		ps.Reason = "不在 AI 允许的写系统调用集（AI 不能自称完成/翻状态）"
	case allowedSoFar >= maxCalls:
		ps.Allowed = false
		ps.Reason = fmt.Sprintf("超过单次计划上限 %d 步", maxCalls)
	default:
		if r := validateArgs(s.Syscall, s.Args); r != "" {
			ps.Allowed = false
			ps.Reason = r
		} else {
			ps.Allowed = true
		}
	}
	return ps
}

// stepKey 归一化一个步骤（syscall + 参数），用于去重与循环检测。
func stepKey(s rawStep) string {
	b, _ := json.Marshal(s.Args)
	return s.Syscall + "|" + string(b)
}

// countAllowed 统计报告里已接纳的步数（用于 MaxCalls 上限）。
func countAllowed(rep *Report) int {
	n := 0
	for _, p := range rep.Plan {
		if p.Allowed {
			n++
		}
	}
	return n
}

// readState 经内核读系统调用取决策层状态（AI 的"感官"只经内核）。
func (a *Agent) readState() (string, error) {
	res, err := a.Kernel.Syscall("dashboard", nil)
	if err != nil {
		return "", err
	}
	s, _ := res.Data.(string)
	return s, nil
}

// systemPrompt 返回实际 system 文本：内置提示 + 可选 ExtraSystem（记忆块等）。
func (a *Agent) systemPrompt() string {
	if strings.TrimSpace(a.ExtraSystem) == "" {
		return systemPrompt
	}
	return systemPrompt + "\n\n" + a.ExtraSystem
}

func (a *Agent) defaultChat(ctx context.Context, system, user string) (string, error) {
	resp, err := ai.Chat(ctx, a.Provider, ai.ChatRequest{
		Model:    a.Model,
		Messages: []ai.Message{{Role: "system", Content: system}, {Role: "user", Content: user}},
	}, ai.ClientOptions{BaseURL: a.BaseURL})
	if err != nil {
		return "", err
	}
	// 真实用量只在真调用路径上存在：累计进账本 + 上报模型名 + 回调。
	a.usage.Add(resp.Usage)
	if resp.Model != "" {
		a.modelUsed = resp.Model
	}
	if a.OnUsage != nil {
		a.OnUsage(resp.Usage)
	}
	return resp.Content, nil
}

// ---- 计划解析与校验 ----

type rawStep struct {
	Syscall string         `json:"syscall"`
	Args    map[string]any `json:"args"`
}

// parsePlan 从 AI 文本里抽出 JSON 数组计划（容忍 ```json 围栏与前后杂音）。
func parsePlan(raw string) ([]rawStep, error) {
	s := strings.TrimSpace(raw)
	if i := strings.Index(s, "```"); i >= 0 {
		s = stripFence(s)
	}
	start := strings.Index(s, "[")
	end := strings.LastIndex(s, "]")
	if start < 0 || end < 0 || end <= start {
		return nil, fmt.Errorf("找不到 JSON 数组（AI 应只输出步骤数组）")
	}
	var steps []rawStep
	if err := json.Unmarshal([]byte(s[start:end+1]), &steps); err != nil {
		return nil, err
	}
	return steps, nil
}

func stripFence(s string) string {
	// 去掉 ```lang ... ``` 围栏，保留中间内容。
	if i := strings.Index(s, "```"); i >= 0 {
		s = s[i+3:]
		if nl := strings.Index(s, "\n"); nl >= 0 {
			s = s[nl+1:]
		}
		if j := strings.LastIndex(s, "```"); j >= 0 {
			s = s[:j]
		}
	}
	return s
}

var taskIDRe = regexp.MustCompile(`^T-\d{2}$`)

// validateArgs 对计划步骤做轻量前置校验（内核仍是运行时权威，双保险）。
func validateArgs(syscall string, args map[string]any) string {
	switch syscall {
	case "tasks.propose":
		id, _ := args["id"].(string)
		if !taskIDRe.MatchString(id) {
			return "tasks.propose 需要合法 id（T-NN）"
		}
		for _, k := range []string{"i", "e", "r", "c"} {
			if !inRange(args[k], 1, 5) {
				return "tasks.propose 的 " + k + " 需在 1..5"
			}
		}
		if t, _ := args["title"].(string); strings.TrimSpace(t) == "" {
			return "tasks.propose 需要 title"
		}
	case "opportunities.add":
		if c, _ := args["chance"].(string); strings.TrimSpace(c) == "" {
			return "opportunities.add 需要 chance"
		}
	case "decisions.add":
		if t, _ := args["text"].(string); strings.TrimSpace(t) == "" {
			return "decisions.add 需要 text"
		}
		if b, _ := args["basis"].(string); strings.TrimSpace(b) == "" {
			return "decisions.add 需要 basis（依据）"
		}
	case "experiments.new":
		if t, _ := args["tag"].(string); strings.TrimSpace(t) == "" {
			return "experiments.new 需要 tag"
		}
		if q, _ := args["q"].(string); strings.TrimSpace(q) == "" {
			return "experiments.new 需要 q（研究问题）"
		}
	case "experiments.advance":
		if !inRange(args["state"], 1, 5) {
			return "experiments.advance 的 state 需在 1..5"
		}
		if t, _ := args["tag"].(string); strings.TrimSpace(t) == "" {
			return "experiments.advance 需要 tag"
		}
		if t, _ := args["text"].(string); strings.TrimSpace(t) == "" {
			return "experiments.advance 需要 text"
		}
	}
	return ""
}

func inRange(v any, lo, hi int) bool {
	switch n := v.(type) {
	case float64:
		return n >= float64(lo) && n <= float64(hi)
	case int:
		return n >= lo && n <= hi
	case string:
		// 允许 "3" 形式
		var f float64
		if _, err := fmt.Sscanf(n, "%g", &f); err == nil {
			return f >= float64(lo) && f <= float64(hi)
		}
	}
	return false
}

// ---- 提示词 ----

const systemPrompt = `你是 HUSHFUSION OS 的操作助手。OS 是一个决策层宏内核，你只能通过一组受控的「系统调用」提出动作，由内核执行并做不变量守卫。

你【唯一】可以发起的写系统调用（输出为 JSON 数组，每项 {"syscall":..., "args":{...}}）：
- tasks.propose      args: {id:"T-NN", title, i,e,r,c(各1-5整数), deps?}   新增一条 proposed 任务
- opportunities.add  args: {chance, source?, match?, next?, status?, date?}  追加一条机会
- decisions.add      args: {text, basis, alt?, consequence?, status?}        追加一条【提议】决策（不是最终结论）
- experiments.new    args: {tag, q}                                          新增实验（仅态1 问题）
- experiments.advance args: {tag, state(1-5), text}                          推进实验某态（不许跳态）

纪律（强制）：
1. 你没有把任何任务标成 active/done 的能力；"完成"只能由人经 ossubmit --settle 判定。你只提议。
2. 你不可以宣称"理论成立/已证明/完成"；科学结论只属于人或独立反馈。你的 decisions.add 只是记录提议。
3. 只输出 JSON 数组，不要解释、不要 markdown 围栏之外的多余文字。若无需动作，输出 []。
4. 依据不足时保守：宁可少提议，也不要编造人选/数据/结论。`

func buildUser(goal, state string, history []string) string {
	u := "目标：" + goal + "\n\n当前 OS 状态（只读快照）：\n" + state +
		"\n\n请基于以上状态，给出一个 JSON 步骤数组来推进目标（只含上面允许的系统调用）。"
	if len(history) > 0 {
		u += "\n\n—— 之前的轮次反馈（请据此修正你的提议）：\n" + strings.Join(history, "\n\n")
	}
	return u
}
