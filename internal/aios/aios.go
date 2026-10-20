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

	"github.com/logos-42/hushfusion-forge/internal/ai"
	"github.com/logos-42/hushfusion-forge/internal/oskernel"
)

// Agent 是一个粘合到 OS 内核的 AI 操作器。
type Agent struct {
	Kernel   *oskernel.Kernel
	Provider string // ai provider 名（见 internal/ai）
	Model    string // 空 = 用 provider 默认模型
	BaseURL  string // 可选覆盖（custom 代理/网关）
	MaxCalls int    // 单次计划允许的最大写系统调用数（防失控）

	// ChatFunc 注入对话函数（默认走 internal/ai；测试注入假实现）。
	ChatFunc func(ctx context.Context, system, user string) (string, error)
}

// NewAgent 构造 AI 操作器。内置 provider 目录幂等注册（让 aios 独立可用，
// 不依赖调用方先 RegisterBuiltins）。
func NewAgent(k *oskernel.Kernel, provider, model, baseURL string) *Agent {
	_ = ai.RegisterBuiltins() // 幂等；后注册覆盖同名，内置只是兜底
	return &Agent{Kernel: k, Provider: provider, Model: model, BaseURL: baseURL, MaxCalls: 8}
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
	OK      bool
	Message string
	Err     string
}

// Report 是一次 AI 粘合运行的完整记录（可核对）。
type Report struct {
	Goal    string
	Plan    []PlanStep
	Results []ExecResult
	Notes   []string
}

// Run 读 OS 状态 → 让 AI 出计划 → 校验 → （非 dry-run）经内核执行。
func (a *Agent) Run(ctx context.Context, goal string, dryRun bool) (*Report, error) {
	if strings.TrimSpace(goal) == "" {
		return nil, fmt.Errorf("aios: goal 为空")
	}
	if a.ChatFunc == nil {
		a.ChatFunc = a.defaultChat
	}

	state, err := a.readState()
	if err != nil {
		return nil, fmt.Errorf("aios: 读 OS 状态失败: %w", err)
	}

	raw, err := a.ChatFunc(ctx, systemPrompt, buildUser(goal, state))
	if err != nil {
		return nil, fmt.Errorf("aios: AI 对话失败: %w", err)
	}

	steps, err := parsePlan(raw)
	if err != nil {
		return nil, fmt.Errorf("aios: AI 输出不是合法 JSON 计划，拒绝执行: %w", err)
	}

	rep := &Report{Goal: goal}
	max := a.MaxCalls
	if max <= 0 {
		max = 8
	}
	allowedCount := 0
	for _, s := range steps {
		ps := PlanStep{Syscall: s.Syscall, Args: s.Args}
		switch {
		case !allowedWrites[s.Syscall]:
			ps.Allowed = false
			ps.Reason = "不在 AI 允许的写系统调用集（AI 不能自称完成/翻状态）"
		case allowedCount >= max:
			ps.Allowed = false
			ps.Reason = fmt.Sprintf("超过单次计划上限 %d 步", max)
		default:
			if r := validateArgs(s.Syscall, s.Args); r != "" {
				ps.Allowed = false
				ps.Reason = r
			} else {
				ps.Allowed = true
				allowedCount++
			}
		}
		rep.Plan = append(rep.Plan, ps)
	}
	if len(rep.Plan) == 0 {
		rep.Notes = append(rep.Notes, "AI 未提出任何动作。")
	}

	if dryRun {
		rep.Notes = append(rep.Notes, "dry-run：只校验计划，不执行。")
		return rep, nil
	}

	for _, ps := range rep.Plan {
		if !ps.Allowed {
			rep.Results = append(rep.Results, ExecResult{
				Syscall: ps.Syscall, OK: false, Err: "拒绝执行: " + ps.Reason,
			})
			continue
		}
		res, err := a.Kernel.Syscall(ps.Syscall, ps.Args)
		if err != nil {
			rep.Results = append(rep.Results, ExecResult{Syscall: ps.Syscall, OK: false, Err: err.Error()})
			continue
		}
		rep.Results = append(rep.Results, ExecResult{
			Syscall: ps.Syscall, OK: res.OK, Message: res.Message,
		})
	}
	rep.Notes = append(rep.Notes, "以上为 AI 提议经内核执行的结果；科学结论/完成判定仍归 leo 或独立反馈。")
	return rep, nil
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

func (a *Agent) defaultChat(ctx context.Context, system, user string) (string, error) {
	resp, err := ai.Chat(ctx, a.Provider, ai.ChatRequest{
		Model:    a.Model,
		Messages: []ai.Message{{Role: "system", Content: system}, {Role: "user", Content: user}},
	}, ai.ClientOptions{BaseURL: a.BaseURL})
	if err != nil {
		return "", err
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

func buildUser(goal, state string) string {
	return "目标：" + goal + "\n\n当前 OS 状态（只读快照）：\n" + state +
		"\n\n请基于以上状态，给出一个 JSON 步骤数组来推进目标（只含上面允许的系统调用）。"
}
