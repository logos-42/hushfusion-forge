package main

// agentcli.go —— forge agent：Agent Runtime（internal/agent）的 CLI 接线。
//
// 两个入口（与 main.go 分发注释约定的契约一致）：
//
//	forge agent chat    单发：一次对话后进程退出（默认无状态；--memory-dir 可持久化）
//	forge agent serve   stdin/stdout JSONL 会话服务器（连续对话；TUI/脚本经管道连）
//
// 纪律（对齐 runtime 侧）：
//   - stdout 只放协议行/最终回复；一切噪声（用量行、降级警告）写 stderr；
//   - 缺省全只读（AllowWrites 空）：写工具要 --allow-writes 显式放行，fail-closed；
//   - 记忆默认进程内；--memory-dir 开启按会话落盘，文件损坏退回内存并明说。

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/logos-42/hushfusion-forge/internal/agent"
	"github.com/logos-42/hushfusion-forge/internal/ai"
)

func cmdAgent(args []string) int {
	if err := ai.RegisterBuiltins(); err != nil {
		return fail("agent: 注册内置 provider: %v", err)
	}
	if len(args) == 0 {
		agentUsage(os.Stderr)
		return 2
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "serve":
		return cmdAgentServe(rest)
	case "chat":
		return cmdAgentChat(rest)
	case "help", "-h", "--help":
		agentUsage(os.Stdout)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "forge agent: unknown subcommand %q\n\n", sub)
		agentUsage(os.Stderr)
		return 2
	}
}

func agentUsage(w *os.File) {
	fmt.Fprintf(w, `usage: forge agent <serve|ask> [flags]

Agent Runtime (internal/agent) 的 CLI 接线:

  serve      stdin/stdout JSONL 会话服务器（请求 {"msg":..,"session":..} / {"quit":true}）
  chat       一次对话（--prompt 必填；stdout 只放最终回复，可管道）

公共 flags:
  --provider <name>    provider 名字/别名（缺省读 HUSHFUSION_AI_PROVIDER）
  --model <id>         模型（空 = provider 默认）
  --base-url <url>     覆盖 provider BaseURL（custom/本地网关）
  --memory-dir <DIR>   按会话落盘记忆到 DIR/<session>.json（空 = 进程内）
  --skills <DIR[,DIR]> 技能目录（命中技能按回合注入 system；skill.view 看全文）
  --persona-root <DIR> persona 文档根（SOUL.md/AGENTS.md/...；空 = 不载入）
  --allow-writes <LIST> 写工具白名单（逗号分隔；空 = 全只读，fail-closed）

serve 专用: --max-sessions N（默认 32）
`)
}

// agentOpts 是一条 forge agent 命令的装配参数。
type agentOpts struct {
	provider    string
	model       string
	baseURL     string
	memoryDir   string
	skillsDirs  []string
	personaRoot string
	allowWrites []string
	maxSessions int
	usageW      io.Writer // 非 nil 时每次模型调用后向这里写一行用量（stderr）
}

// agentFlagSet 收集公共 flags（parse 之前拿指针，之后 opts() 解引用）。
type agentFlagSet struct {
	fs                          *flag.FlagSet
	provider, model, base       *string
	mem, skills, persona, write *string
}

func newAgentFlags(fs *flag.FlagSet) agentFlagSet {
	return agentFlagSet{
		fs:       fs,
		provider: fs.String("provider", os.Getenv("HUSHFUSION_AI_PROVIDER"), "provider 名字或别名（缺省读 HUSHFUSION_AI_PROVIDER）"),
		model:    fs.String("model", "", "模型 id（空 = provider 默认）"),
		base:     fs.String("base-url", os.Getenv("HUSHFUSION_AI_BASE_URL"), "覆盖 provider BaseURL（custom/本地网关）"),
		mem:      fs.String("memory-dir", "", "按会话落盘记忆到 DIR/<session>.json（空 = 进程内）"),
		skills:   fs.String("skills", "", "技能目录（逗号分隔；命中技能按回合注入 system）"),
		persona:  fs.String("persona-root", "", "persona 文档根目录（SOUL.md/AGENTS.md/...；空 = 不载入）"),
		write:    fs.String("allow-writes", "", "写工具白名单（逗号分隔；空 = 全只读，fail-closed）"),
	}
}

func (a agentFlagSet) opts() agentOpts {
	return agentOpts{
		provider:    strings.TrimSpace(*a.provider),
		model:       strings.TrimSpace(*a.model),
		baseURL:     strings.TrimSpace(*a.base),
		memoryDir:   strings.TrimSpace(*a.mem),
		skillsDirs:  splitCSVAgent(*a.skills),
		personaRoot: strings.TrimSpace(*a.persona),
		allowWrites: splitCSVAgent(*a.write),
	}
}

// cmdAgentChat 一次对话：构造会话 → Turn → 打印最终回复（进程退出即失忆，除非 --memory-dir）。
func cmdAgentChat(args []string) int {
	fs := newFlagSet("agent chat", `forge agent chat --provider <name> [--model <m>] [--base-url <url>] --prompt "目标或问题"`,
		"一次对话：构造会话 → 回合循环 → 打印最终回复（stdout 可管道；用量/警告走 stderr）。")
	af := newAgentFlags(fs)
	prompt := fs.String("prompt", "", "用户消息文本（必填）")
	if code := parseFlags(fs, args); code >= 0 {
		return code
	}
	o := af.opts()
	if o.provider == "" || strings.TrimSpace(*prompt) == "" {
		fmt.Fprintf(os.Stderr, "forge agent ask: --provider 与 --prompt 必填\n\n")
		fs.Usage()
		return 2
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	return runAgentChat(ctx, o, *prompt, os.Stdout, os.Stderr)
}

// runAgentChat 是 chat 的可测内核（stdout 只放最终回复）。
func runAgentChat(ctx context.Context, o agentOpts, prompt string, out, errW io.Writer) int {
	if o.memoryDir != "" {
		if err := os.MkdirAll(o.memoryDir, 0o700); err != nil {
			return fail("agent: 创建记忆目录 %s: %v", o.memoryDir, err)
		}
	}
	o.usageW = errW
	s := newAgentSession("default", o)
	reply, err := s.Turn(ctx, prompt)
	if err != nil {
		return fail("agent: %v", err)
	}
	fmt.Fprintln(out, reply)
	writeUsageLine(errW, s.Usage(), o.model)
	return 0
}

// cmdAgentServe 启动 stdio JSONL 会话服务器。
func cmdAgentServe(args []string) int {
	fs := newFlagSet("agent serve", `forge agent serve --provider <name> [--memory-dir DIR] [--skills DIR[,DIR]] [--max-sessions N]`,
		"stdin/stdout JSONL 会话服务器（连续对话；stdout 只放协议行，用量/警告写 stderr）。")
	af := newAgentFlags(fs)
	maxS := fs.Int("max-sessions", 32, "会话数上限（防泄漏）")
	if code := parseFlags(fs, args); code >= 0 {
		return code
	}
	o := af.opts()
	if o.provider == "" {
		fmt.Fprintf(os.Stderr, "forge agent serve: --provider 必填（或 export HUSHFUSION_AI_PROVIDER）\n\n")
		fs.Usage()
		return 2
	}
	if o.memoryDir != "" {
		if err := os.MkdirAll(o.memoryDir, 0o700); err != nil {
			return fail("agent: 创建记忆目录 %s: %v", o.memoryDir, err)
		}
	}
	o.maxSessions = *maxS
	o.usageW = os.Stderr
	h := agent.NewServeHandler(func(id string) *agent.Session { return newAgentSession(id, o) })
	h.MaxSessions = o.maxSessions
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if err := h.Serve(ctx, os.Stdin, os.Stdout); err != nil {
		return fail("agent serve: %v", err)
	}
	return 0
}

// newAgentSession 装配一个真会话：真模型（ai.Chat）+ 内置工具 + 可选 persona/skills/持久记忆。
// 装配失败不致命的（persona 载入、记忆文件）降级并写 stderr 明说，不静默。
func newAgentSession(sessionID string, o agentOpts) *agent.Session {
	cfg := agent.DefaultConfig()
	cfg.Provider, cfg.Model, cfg.BaseURL = o.provider, o.model, o.baseURL
	chat := func(ctx context.Context, req ai.ChatRequest) (*ai.ChatResponse, error) {
		return ai.Chat(ctx, o.provider, req, ai.ClientOptions{BaseURL: o.baseURL})
	}
	s := agent.NewSession(cfg, chat, agent.NewRegistry(), agentSystemPrompt())

	// persona（可选，opt-in）：人格/协作文档注入，失败降级并明说。
	if o.personaRoot != "" {
		if err := s.WithPersona(o.personaRoot, 6000); err != nil {
			fmt.Fprintf(os.Stderr, "forge agent: persona 载入失败（跳过）: %v\n", err)
		}
	}

	// skills（可选）：按回合动态注入；skill.view 工具看全文。
	skills := agent.NewSkills(o.skillsDirs, agent.NewKeywordMatcher())
	s.Skills = skills

	// 记忆：默认进程内；--memory-dir 按会话落盘（损坏 ⇒ 明说并退回内存）。
	if o.memoryDir != "" {
		path := filepath.Join(o.memoryDir, agentSafeName(sessionID)+".json")
		if ms, err := agent.NewMemStoreFile(path); err == nil {
			s.Memory = ms
			if n := ms.Len(); n > 0 {
				// 已有记忆并入基线 system（构造时一次）；会话内更新经 mem.* 工具。
				// 必须走 AppendSystem：直接改 s.System 会被下一回合的技能注入覆盖。
				s.AppendSystem(ms.Summary())
			}
		} else {
			fmt.Fprintf(os.Stderr, "forge agent: 会话 %q 记忆文件不可用（退回内存模式）: %v\n", sessionID, err)
		}
	}

	// 写白名单：缺省全只读；--allow-writes 显式放行。
	if len(o.allowWrites) > 0 {
		wl := map[string]bool{}
		for _, n := range o.allowWrites {
			wl[n] = true
		}
		s.AllowWrites = wl
	}

	if err := s.RegisterBuiltinTools(skills); err != nil {
		fmt.Fprintf(os.Stderr, "forge agent: 内置工具注册失败: %v\n", err)
	}
	if o.usageW != nil {
		s.OnUsage = func(u ai.Usage) {
			fmt.Fprintf(o.usageW, "# usage: prompt=%d completion=%d total=%d cached=%d\n",
				u.PromptTokens, u.CompletionTokens, u.TotalTokens, u.CachedTokens)
		}
	}
	return s
}

// agentSystemPrompt 是 forge agent 的基线指令（persona/skills 都叠加在它之上）。
func agentSystemPrompt() string {
	return `你是 HUSHFUSION Forge 的智能体（forge agent）。
诚实纪律：不知道就说不知道；完成只能被证明（证据），不能被声称。
写操作受白名单约束，未放行的写工具会被拒绝。
记忆用 mem.* 工具读写；需要技能全文时用 skill.view。`
}

// agentSafeName 把 session id 清洗成安全文件名（防路径穿越/非法字符）。
// 点号一律替换 —— ".."、"隐藏文件"、".._evil" 这类形状从一开始就不出现。
func agentSafeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	if b.Len() == 0 {
		return "default"
	}
	return b.String()
}

// splitCSV 拆逗号分隔列表（去空白去空项）。
func splitCSVAgent(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if t := strings.TrimSpace(part); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// writeUsageLine 输出一次会话的用量汇总（cost 未知显示 —，不显示 0）。
func writeUsageLine(w io.Writer, l ai.UsageLedger, model string) {
	if l.Calls == 0 {
		return
	}
	usd, known := l.Cost(model)
	cost := "—(价目未知)"
	if known {
		cost = fmt.Sprintf("$%.4f", usd)
	}
	fmt.Fprintf(w, "# usage: calls=%d prompt=%d completion=%d cached=%d total=%d cost=%s\n",
		l.Calls, l.Prompt, l.Completion, l.Cached, l.Total, cost)
}
