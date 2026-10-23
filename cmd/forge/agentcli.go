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
	"encoding/json"
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
	// workspace config → 环境变量（provider/model/base_url/key），与 TUI 一致。
	applyWorkspaceConfig(osBaseDir())
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
  --state-dir <DIR>    按会话落盘**消息栈**到 DIR/<session>.history.json（跨重启恢复）
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
	stateDir    string // 非空 ⇒ 消息栈跨重启恢复（serve/chat 都可用）
	skillsDirs  []string
	personaRoot string
	allowWrites []string
	maxSessions int
	usageW      io.Writer // 非 nil 时每次模型调用后向这里写一行用量（stderr）
	diagW       io.Writer // 非 nil 时诊断/降级信息写这里（缺省 os.Stderr；测试可捕获）
}

// diag 返回诊断输出目标（默认 stderr，绝不污染 stdout 的协议行）。
func (o agentOpts) diag() io.Writer {
	if o.diagW != nil {
		return o.diagW
	}
	return os.Stderr
}

// agentFlagSet 收集公共 flags（parse 之前拿指针，之后 opts() 解引用）。
type agentFlagSet struct {
	fs                          *flag.FlagSet
	provider, model, base       *string
	mem, state, skills, persona *string
	write                       *string
}

func newAgentFlags(fs *flag.FlagSet) agentFlagSet {
	return agentFlagSet{
		fs:       fs,
		provider: fs.String("provider", os.Getenv("HUSHFUSION_AI_PROVIDER"), "provider 名字或别名（缺省读 HUSHFUSION_AI_PROVIDER）"),
		model:    fs.String("model", "", "模型 id（空 = provider 默认）"),
		base:     fs.String("base-url", os.Getenv("HUSHFUSION_AI_BASE_URL"), "覆盖 provider BaseURL（custom/本地网关）"),
		mem:      fs.String("memory-dir", "", "按会话落盘记忆到 DIR/<session>.json（空 = 进程内）"),
		state:    fs.String("state-dir", "", "按会话落盘消息栈到 DIR/<session>.history.json（空 = 不持久化）"),
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
		stateDir:    strings.TrimSpace(*a.state),
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
	stream := fs.Bool("stream", false, "流式输出：逐增量打印（stdout 仍可管道）")
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
	return runAgentChat(ctx, o, *prompt, *stream, os.Stdout, os.Stderr)
}

// runAgentChat 是 chat 的可测内核。stream=true 时逐增量打印（仍走 out，可管道）。
func runAgentChat(ctx context.Context, o agentOpts, prompt string, stream bool, out, errW io.Writer) int {
	if o.memoryDir != "" {
		if err := os.MkdirAll(o.memoryDir, 0o700); err != nil {
			return fail("agent: 创建记忆目录 %s: %v", o.memoryDir, err)
		}
	}
	if o.stateDir != "" {
		if err := os.MkdirAll(o.stateDir, 0o700); err != nil {
			return fail("agent: 创建状态目录 %s: %v", o.stateDir, err)
		}
	}
	o.usageW = errW
	o.diagW = errW
	s := newAgentSession("default", o)
	var reply string
	var err error
	if stream {
		// 流式：增量直接写到 out（仍可被管道）；最终再补一次整段便于下游消费。
		onDelta := func(ev ai.StreamEvent) {
			if ev.Type == ai.StreamContent {
				fmt.Fprint(out, ev.Text)
			}
		}
		reply, err = s.TurnStream(ctx, prompt, onDelta)
		fmt.Fprintln(out) // 增量输出后补换行，保证 stdout 行尾干净
	} else {
		reply, err = s.Turn(ctx, prompt)
	}
	if err != nil {
		return fail("agent: %v", err)
	}
	fmt.Fprintln(out, reply)
	// 跨重启恢复：chat 也落盘消息栈，下一次 --state-dir 同一目录就能接着聊。
	if o.stateDir != "" {
		if serr := saveAgentHistory(agentStatePath(o.stateDir, "default"), s); serr != nil {
			fmt.Fprintf(errW, "forge agent: 历史落盘失败: %v\n", serr)
		}
	}
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
	if o.stateDir != "" {
		if err := os.MkdirAll(o.stateDir, 0o700); err != nil {
			return fail("agent: 创建状态目录 %s: %v", o.stateDir, err)
		}
	}
	o.maxSessions = *maxS
	o.usageW = os.Stderr
	h := agent.NewServeHandler(func(id string) *agent.Session { return newAgentSession(id, o) })
	h.MaxSessions = o.maxSessions
	// 消息栈跨重启恢复：每回合/重置后原子落盘；失败写 stderr，不影响协议行。
	if o.stateDir != "" {
		h.AfterTurn = func(id string, s *agent.Session) {
			if err := saveAgentHistory(agentStatePath(o.stateDir, id), s); err != nil {
				fmt.Fprintf(o.diag(), "forge agent: 会话 %q 历史落盘失败: %v\n", id, err)
			}
		}
	}
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
	// 预算按模型上下文窗口自动匹配；HUSHFUSION_MAX_BUDGET_TOKENS 可覆盖。
	cfg.MaxBudgetTokens = agent.BudgetFromEnv(agent.BudgetForModel(o.model, 0.6))
	// 精确 tokenizer（可选）：HUSHFUSION_TOKENIZER_CMD；未设则 len/4 估算。
	cfg.Tokenizer = agent.TokenizerFromEnv()
	chat := func(ctx context.Context, req ai.ChatRequest) (*ai.ChatResponse, error) {
		return ai.Chat(ctx, o.provider, req, ai.ClientOptions{BaseURL: o.baseURL})
	}
	s := agent.NewSession(cfg, chat, agent.NewRegistry(), agentSystemPrompt())
	// 流式端口：TurnStream 用；未注入则退化为一次性。
	streamChat := func(ctx context.Context, req ai.ChatRequest, on func(ai.StreamEvent)) (*ai.ChatResponse, error) {
		return ai.ChatStream(ctx, o.provider, req, on, ai.ClientOptions{BaseURL: o.baseURL})
	}
	s.SetStreamChat(streamChat)

	// persona（可选，opt-in）：人格/协作文档注入，失败降级并明说。
	if o.personaRoot != "" {
		if err := s.WithPersona(o.personaRoot, 6000); err != nil {
			fmt.Fprintf(o.diag(), "forge agent: persona 载入失败（跳过）: %v\n", err)
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
			fmt.Fprintf(o.diag(), "forge agent: 会话 %q 记忆文件不可用（退回内存模式）: %v\n", sessionID, err)
		}
	}

	// 消息栈跨重启恢复：--state-dir 时从 <dir>/<session>.history.json 读回。
	// 文件损坏 ⇒ 明说并从空开始（不能因为一个会话的历史坏了就拒绝整个 serve）。
	if o.stateDir != "" {
		path := agentStatePath(o.stateDir, sessionID)
		switch msgs, err := loadAgentHistory(path); {
		case err == nil:
			if rerr := s.Restore(msgs); rerr != nil {
				fmt.Fprintf(o.diag(), "forge agent: 会话 %q 历史文件损坏（从空开始）: %v\n", sessionID, rerr)
			} else if len(msgs) > 0 {
				fmt.Fprintf(o.diag(), "# state: 会话 %q 恢复 %d 条历史\n", sessionID, len(msgs))
			}
		case !os.IsNotExist(err):
			fmt.Fprintf(o.diag(), "forge agent: 会话 %q 历史读取失败（从空开始）: %v\n", sessionID, err)
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
		fmt.Fprintf(o.diag(), "forge agent: 内置工具注册失败: %v\n", err)
	}
	registerProjectTools(s, osBaseDir()) // fusion.* / plant.* / plugin.* / mcp.* / acp.* / diap.*
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
	return `你是 HUSHFUSION Forge 的智能体，负责操纵与控制一个可控核聚变系统。
身份：始终以 HUSHFUSION Forge 的身份回答，不要自称底层模型名（如 glm / deepseek / gpt）。
诚实纪律：不知道就说不知道；完成只能被证明（证据），不能被声称。
写操作受白名单约束，未放行的写工具会被拒绝。
读文件用 read、列目录用 ls；需要执行命令时用 shell（仅白名单放行时可用）。
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

// agentStatePath 是某会话的消息栈文件路径（session id 已清洗成安全文件名）。
func agentStatePath(dir, sessionID string) string {
	return filepath.Join(dir, agentSafeName(sessionID)+".history.json")
}

// loadAgentHistory 读回某会话的消息栈（文件不存在返回 os.IsNotExist 错误）。
func loadAgentHistory(path string) ([]agent.Message, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var msgs []agent.Message
	if err := json.Unmarshal(b, &msgs); err != nil {
		return nil, err
	}
	return msgs, nil
}

// saveAgentHistory 原子落盘消息栈（tmp + rename，0600）。
// 历史可能含用户输入，按 0600 存，不放 0644。
func saveAgentHistory(path string, s *agent.Session) error {
	b, err := json.Marshal(s.History())
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
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
