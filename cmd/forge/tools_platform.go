package main

// tools_platform.go —— 平台能力工具化：cron / nodejs / python / git / hook /
// 子智能体集群 / env / workspace / 去中心化通信频道。
//
// 纪律：
//   - 读工具默认可用；写/执行类工具标记 Write=true，需 AllowWrites 放行；
//   - 执行命令一律带超时、限定 cwd=repoRoot、输出限量，绝不明文返回密钥；
//   - 子智能体有深度上限，防止集群无限派生。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/logos-42/hushfusion-forge/internal/agent"
	"github.com/logos-42/hushfusion-forge/internal/ai"
	"github.com/logos-42/hushfusion-forge/internal/cron"
	"github.com/logos-42/hushfusion-forge/internal/dchat"
	"github.com/logos-42/hushfusion-forge/internal/envload"
	"github.com/logos-42/hushfusion-forge/internal/subagents"
	"github.com/logos-42/hushfusion-forge/internal/workspace"
)

// ---- 全局单例（进程级；TUI/CLI 共用） ----

var (
	platformMu  sync.Mutex
	dchatMgr    *dchat.Manager
	cronStores  = map[string]*cron.Store{}
	subagentReg = map[string]*subagents.Manager{}
)

func dchatManager() (*dchat.Manager, error) {
	platformMu.Lock()
	defer platformMu.Unlock()
	if dchatMgr != nil {
		return dchatMgr, nil
	}
	m, err := dchat.NewManager()
	if err != nil {
		return nil, err
	}
	dchatMgr = m
	return m, nil
}

func cronStoreFor(base string) (*cron.Store, error) {
	key := runtimeRoot(base)
	platformMu.Lock()
	defer platformMu.Unlock()
	if s, ok := cronStores[key]; ok {
		return s, nil
	}
	s, err := cron.NewStore(filepath.Join(runtimeRoot(base), "cron.json"))
	if err != nil {
		return nil, err
	}
	cronStores[key] = s
	return s, nil
}

func subagentManagerFor(base string) *subagents.Manager {
	key := runtimeRoot(base)
	platformMu.Lock()
	defer platformMu.Unlock()
	if m, ok := subagentReg[key]; ok {
		return m
	}
	m := subagents.NewManager(4, func(ctx context.Context, t *subagents.Task) (string, error) {
		return runSubagentGoal(ctx, base, t.Goal)
	})
	subagentReg[key] = m
	return m
}

type subagentDepthKey struct{}

func subagentDepth(ctx context.Context) int {
	if v, ok := ctx.Value(subagentDepthKey{}).(int); ok {
		return v
	}
	return 0
}

// allowPlatformWrites 按环境变量放行平台写工具（缺省全部只读，fail-closed）。
func allowPlatformWrites(s *agent.Session) {
	if os.Getenv("HUSHFUSION_ALLOW_PLATFORM_WRITE") != "1" {
		return
	}
	for _, n := range []string{
		"git.commit", "node.run", "python.run", "hook.run",
		"cron.add", "cron.remove", "cron.run",
	} {
		s.AllowWrites[n] = true
	}
}

func safeRepoPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", fmt.Errorf("路径为空")
	}
	root := repoRoot()
	abs, err := filepath.Abs(filepath.Join(root, p))
	if err != nil {
		return "", err
	}
	if abs != root && !strings.HasPrefix(abs, root+string(filepath.Separator)) {
		return "", fmt.Errorf("路径越出 workspace: %q", p)
	}
	return abs, nil
}

// runPlatformCommand 执行外部命令：cwd=repoRoot、可超时、输出限量。
func runPlatformCommand(ctx context.Context, stdin string, timeout time.Duration, name string, args ...string) (string, error) {
	if timeout <= 0 || timeout > 300*time.Second {
		timeout = 60 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, name, args...)
	cmd.Dir = repoRoot()
	cmd.Env = os.Environ()
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	body := out.String()
	if len(body) > 64*1024 {
		body = body[:64*1024] + "\n…(输出已截断)"
	}
	exit := 0
	if ee, ok := err.(*exec.ExitError); ok {
		exit = ee.ExitCode()
	} else if err != nil {
		return "", err
	}
	b, merr := json.Marshal(map[string]any{
		"command": name, "args": args, "exit_code": exit, "output": body,
	})
	if merr != nil {
		return "", merr
	}
	return string(b), nil
}

// ---- hooks ----

type hookDef struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Command     string            `json:"command"`
	Args        []string          `json:"args,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
}

type hookFile struct {
	Hooks []hookDef `json:"hooks"`
}

func hooksPath(base string) string {
	if p := strings.TrimSpace(os.Getenv("HUSHFUSION_HOOKS")); p != "" {
		return p
	}
	return filepath.Join(runtimeRoot(base), "hooks.json")
}

func loadHooks(base string) (hookFile, error) {
	var hf hookFile
	b, err := os.ReadFile(hooksPath(base))
	if err != nil {
		return hf, fmt.Errorf("hook: 读取 %s 失败（配置 hook 后重试）: %w", hooksPath(base), err)
	}
	if err := json.Unmarshal(b, &hf); err != nil {
		return hf, fmt.Errorf("hook: %s 不是 JSON: %w", hooksPath(base), err)
	}
	return hf, nil
}

func registerPlatformTools(s *agent.Session, base string) {
	register := func(t agent.Tool) { _ = s.RegisterTool(t) }

	// ---- env / workspace ----
	register(agent.Tool{
		Spec: projectTool("env.status", "加载并查看 workspace .env（密钥脱敏，不返回值）", nil, nil),
		Executor: func(_ context.Context, _ string, _ map[string]any) (string, error) {
			res, err := envload.Load(repoRoot())
			if err != nil {
				return "", err
			}
			return jsonText(res)
		},
	})

	register(agent.Tool{
		Spec: projectTool("workspace.tree", "按分类加载 workspace 索引（project/design/acceptance/evidence/receipts/tools/artifacts/runs/python/manifests/scripts/knowledge）",
			map[string]any{"per_category": map[string]any{"type": "integer", "description": "每类最多返回多少个文件，缺省 20"}}, nil),
		Executor: func(_ context.Context, _ string, args map[string]any) (string, error) {
			idx := workspace.Scan(repoRoot())
			per := argInt(args, "per_category", 20)
			out := map[string]any{"root": idx.Root, "categories": []any{}, "errors": idx.Errors}
			cats := []any{}
			for _, c := range idx.Categories {
				files := c.Files
				if len(files) > per {
					files = files[:per]
				}
				cats = append(cats, map[string]any{
					"name": c.Name, "path": c.Path, "total": len(c.Files), "files": files,
				})
			}
			out["categories"] = cats
			return jsonText(out)
		},
	})

	// ---- 去中心化通信 ----
	register(agent.Tool{
		Spec: projectTool("dchat.channels", "列出支持的去中心化通信频道（iroh / hyperswarm / orbitdb）", nil, nil),
		Executor: func(_ context.Context, _ string, _ map[string]any) (string, error) {
			return jsonText(map[string]any{"channels": dchat.Channels})
		},
	})
	register(agent.Tool{
		Spec: projectTool("dchat.status", "查看去中心化频道状态（可用性 / peerId）",
			map[string]any{"channel": map[string]any{"type": "string"}}, []string{"channel"}),
		Executor: func(ctx context.Context, _ string, args map[string]any) (string, error) {
			m, err := dchatManager()
			if err != nil {
				return "", err
			}
			st, err := m.Status(ctx, argString(args, "channel", ""))
			if err != nil {
				return "", err
			}
			return jsonText(st)
		},
	})
	register(agent.Tool{
		Spec: projectTool("dchat.send", "通过去中心化频道发送消息（topic + body）",
			map[string]any{
				"channel": map[string]any{"type": "string"},
				"topic":   map[string]any{"type": "string"},
				"body":    map[string]any{"type": "string"},
			}, []string{"channel", "topic", "body"}),
		Executor: func(ctx context.Context, _ string, args map[string]any) (string, error) {
			m, err := dchatManager()
			if err != nil {
				return "", err
			}
			res, err := m.Send(ctx, argString(args, "channel", ""), argString(args, "topic", ""), argString(args, "body", ""))
			if err != nil {
				return "", err
			}
			return jsonText(res)
		},
	})
	register(agent.Tool{
		Spec: projectTool("dchat.recv", "接收去中心化频道最近消息",
			map[string]any{
				"channel": map[string]any{"type": "string"},
				"topic":   map[string]any{"type": "string"},
				"limit":   map[string]any{"type": "integer", "description": "1–100，缺省 20"},
			}, []string{"channel", "topic"}),
		Executor: func(ctx context.Context, _ string, args map[string]any) (string, error) {
			m, err := dchatManager()
			if err != nil {
				return "", err
			}
			res, err := m.Receive(ctx, argString(args, "channel", ""), argString(args, "topic", ""), argInt(args, "limit", 20))
			if err != nil {
				return "", err
			}
			return jsonText(res)
		},
	})

	// ---- nodejs / python ----
	register(agent.Tool{
		Spec: projectTool("node.run", "运行 Node.js：path 或 code 二选一（写动作，需白名单）",
			map[string]any{
				"path":        map[string]any{"type": "string", "description": "workspace 内 .mjs/.js 文件"},
				"code":        map[string]any{"type": "string", "description": "内联 JS 代码"},
				"timeout_sec": map[string]any{"type": "integer", "description": "超时秒数，缺省 60，最大 300"},
			}, nil),
		Write: true,
		Executor: func(ctx context.Context, _ string, args map[string]any) (string, error) {
			path := argString(args, "path", "")
			code := argString(args, "code", "")
			if path == "" && code == "" {
				return "", fmt.Errorf("node.run: path 或 code 必填")
			}
			if path != "" && code != "" {
				return "", fmt.Errorf("node.run: path 与 code 只能选一个")
			}
			timeout := time.Duration(argInt(args, "timeout_sec", 60)) * time.Second
			bin := envOr("NODE_BIN", "node")
			if path != "" {
				abs, err := safeRepoPath(path)
				if err != nil {
					return "", err
				}
				return runPlatformCommand(ctx, "", timeout, bin, abs)
			}
			return runPlatformCommand(ctx, code, timeout, bin, "--input-type=module", "-e", code)
		},
	})

	register(agent.Tool{
		Spec: projectTool("python.run", "运行 Python：path 或 code 二选一（写动作，需白名单）",
			map[string]any{
				"path":        map[string]any{"type": "string", "description": "workspace 内 .py 文件"},
				"code":        map[string]any{"type": "string", "description": "内联 Python 代码"},
				"timeout_sec": map[string]any{"type": "integer", "description": "超时秒数，缺省 60，最大 300"},
			}, nil),
		Write: true,
		Executor: func(ctx context.Context, _ string, args map[string]any) (string, error) {
			path := argString(args, "path", "")
			code := argString(args, "code", "")
			if path == "" && code == "" {
				return "", fmt.Errorf("python.run: path 或 code 必填")
			}
			if path != "" && code != "" {
				return "", fmt.Errorf("python.run: path 与 code 只能选一个")
			}
			timeout := time.Duration(argInt(args, "timeout_sec", 60)) * time.Second
			bin := envOr("PYTHON_BIN", "python3")
			if path != "" {
				abs, err := safeRepoPath(path)
				if err != nil {
					return "", err
				}
				return runPlatformCommand(ctx, "", timeout, bin, abs)
			}
			return runPlatformCommand(ctx, code, timeout, bin, "-c", code)
		},
	})

	// ---- git ----
	register(agent.Tool{
		Spec: projectTool("git.status", "git status --short（仓库概览）", nil, nil),
		Executor: func(ctx context.Context, _ string, _ map[string]any) (string, error) {
			return runPlatformCommand(ctx, "", 30*time.Second, "git", "status", "--short")
		},
	})
	register(agent.Tool{
		Spec: projectTool("git.diff", "git diff（可指定 path）",
			map[string]any{"path": map[string]any{"type": "string"}}, nil),
		Executor: func(ctx context.Context, _ string, args map[string]any) (string, error) {
			p := argString(args, "path", "")
			if p != "" {
				if _, err := safeRepoPath(p); err != nil {
					return "", err
				}
				return runPlatformCommand(ctx, "", 30*time.Second, "git", "diff", "--", p)
			}
			return runPlatformCommand(ctx, "", 30*time.Second, "git", "diff")
		},
	})
	register(agent.Tool{
		Spec: projectTool("git.log", "git log（最近 N 条）",
			map[string]any{"limit": map[string]any{"type": "integer", "description": "缺省 10"}}, nil),
		Executor: func(ctx context.Context, _ string, args map[string]any) (string, error) {
			n := argInt(args, "limit", 10)
			if n < 1 || n > 100 {
				n = 10
			}
			return runPlatformCommand(ctx, "", 30*time.Second, "git", "log", "--oneline", fmt.Sprintf("-%d", n))
		},
	})
	register(agent.Tool{
		Spec: projectTool("git.commit", "git add + commit（写动作，需白名单）",
			map[string]any{
				"message": map[string]any{"type": "string"},
				"paths":   map[string]any{"type": "array", "description": "可选：只提交这些路径"},
			}, []string{"message"}),
		Write: true,
		Executor: func(ctx context.Context, _ string, args map[string]any) (string, error) {
			msg := argString(args, "message", "")
			if strings.TrimSpace(msg) == "" {
				return "", fmt.Errorf("git.commit: message 不能为空")
			}
			paths := argStrings(args, "paths")
			for _, p := range paths {
				if _, err := safeRepoPath(p); err != nil {
					return "", err
				}
			}
			addArgs := []string{"add"}
			if len(paths) == 0 {
				addArgs = append(addArgs, "-A")
			} else {
				addArgs = append(addArgs, paths...)
			}
			if _, err := runPlatformCommand(ctx, "", 60*time.Second, "git", addArgs...); err != nil {
				return "", err
			}
			return runPlatformCommand(ctx, "", 60*time.Second, "git", "commit", "-m", msg)
		},
	})
	register(agent.Tool{
		Spec: projectTool("git.branch", "git branch --show-current", nil, nil),
		Executor: func(ctx context.Context, _ string, _ map[string]any) (string, error) {
			return runPlatformCommand(ctx, "", 30*time.Second, "git", "branch", "--show-current")
		},
	})

	// ---- hooks ----
	register(agent.Tool{
		Spec: projectTool("hook.list", "列出已配置 hook（~/.forge/hooks.json）", nil, nil),
		Executor: func(_ context.Context, _ string, _ map[string]any) (string, error) {
			hf, err := loadHooks(base)
			if err != nil {
				return "", err
			}
			return jsonText(hf)
		},
	})
	register(agent.Tool{
		Spec: projectTool("hook.run", "按名字运行已配置 hook（fail-closed：只跑配置里的命令）",
			map[string]any{"name": map[string]any{"type": "string"}}, []string{"name"}),
		Write: true,
		Executor: func(ctx context.Context, _ string, args map[string]any) (string, error) {
			name := argString(args, "name", "")
			hf, err := loadHooks(base)
			if err != nil {
				return "", err
			}
			var h *hookDef
			for i := range hf.Hooks {
				if hf.Hooks[i].Name == name {
					h = &hf.Hooks[i]
					break
				}
			}
			if h == nil {
				return "", fmt.Errorf("hook: 未配置 %q", name)
			}
			if strings.TrimSpace(h.Command) == "" {
				return "", fmt.Errorf("hook: %q 没有 command", name)
			}
			cctx, cancel := context.WithTimeout(ctx, 120*time.Second)
			defer cancel()
			cmd := exec.CommandContext(cctx, h.Command, h.Args...)
			cmd.Dir = repoRoot()
			cmd.Env = append(os.Environ(), envPairs(h.Env)...)
			var out bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &out
			runErr := cmd.Run()
			exit := 0
			if ee, ok := runErr.(*exec.ExitError); ok {
				exit = ee.ExitCode()
			} else if runErr != nil {
				return "", runErr
			}
			body := out.String()
			if len(body) > 64*1024 {
				body = body[:64*1024] + "…(截断)"
			}
			return jsonText(map[string]any{"hook": name, "exit_code": exit, "output": body})
		},
	})

	// ---- cron ----
	register(agent.Tool{
		Spec: projectTool("cron.list", "列出内部 cron 任务（~/.forge/cron.json）", nil, nil),
		Executor: func(_ context.Context, _ string, _ map[string]any) (string, error) {
			st, err := cronStoreFor(base)
			if err != nil {
				return "", err
			}
			jobs, err := st.List()
			if err != nil {
				return "", err
			}
			return jsonText(jobs)
		},
	})
	register(agent.Tool{
		Spec: projectTool("cron.add", "新增 cron 任务（写动作，需白名单）",
			map[string]any{
				"scheme":  map[string]any{"type": "string", "description": "5 段 cron 或 @daily"},
				"prompt":  map[string]any{"type": "string"},
				"goal":    map[string]any{"type": "string"},
				"name":    map[string]any{"type": "string"},
				"enabled": map[string]any{"type": "boolean"},
			}, []string{"scheme", "prompt"}),
		Write: true,
		Executor: func(_ context.Context, _ string, args map[string]any) (string, error) {
			st, err := cronStoreFor(base)
			if err != nil {
				return "", err
			}
			j := cron.Job{
				Name:     argString(args, "name", ""),
				Schedule: argString(args, "scheme", ""),
				Prompt:   argString(args, "prompt", ""),
				Goal:     argString(args, "goal", ""),
				Enabled:  true,
			}
			if v, ok := args["enabled"].(bool); ok {
				j.Enabled = v
			}
			added, err := st.Add(j)
			if err != nil {
				return "", err
			}
			return jsonText(added)
		},
	})
	register(agent.Tool{
		Spec: projectTool("cron.remove", "删除 cron 任务（写动作，需白名单）",
			map[string]any{"id": map[string]any{"type": "string"}}, []string{"id"}),
		Write: true,
		Executor: func(_ context.Context, _ string, args map[string]any) (string, error) {
			st, err := cronStoreFor(base)
			if err != nil {
				return "", err
			}
			if err := st.Remove(argString(args, "id", "")); err != nil {
				return "", err
			}
			return jsonText(map[string]any{"removed": argString(args, "id", "")})
		},
	})
	register(agent.Tool{
		Spec: projectTool("cron.run", "立即执行到点的 cron 任务（派生子智能体；可指定 id）",
			map[string]any{"id": map[string]any{"type": "string", "description": "可选：只跑这一个"}}, nil),
		Write: true,
		Executor: func(ctx context.Context, _ string, args map[string]any) (string, error) {
			st, err := cronStoreFor(base)
			if err != nil {
				return "", err
			}
			jobs, err := st.List()
			if err != nil {
				return "", err
			}
			id := argString(args, "id", "")
			mgr := subagentManagerFor(base)
			spawned := []string{}
			for _, j := range jobs {
				if !j.Enabled || (id != "" && j.ID != id) {
					continue
				}
				if id == "" && !j.NextRun.IsZero() && j.NextRun.After(time.Now()) {
					continue // 没到点
				}
				if _, err := mgr.SpawnFrom(ctx, "cron:"+j.Name, j.Prompt); err != nil {
					return "", err
				}
				if err := st.MarkRun(j.ID, time.Now()); err != nil {
					return "", err
				}
				spawned = append(spawned, j.ID)
				if id != "" {
					break
				}
			}
			return jsonText(map[string]any{"spawned": spawned})
		},
	})

	// ---- 子智能体集群 ----
	register(agent.Tool{
		Spec: projectTool("subagents.spawn", "派生一个子智能体执行目标（并发上限 4；子智能体不能再派生）",
			map[string]any{
				"name": map[string]any{"type": "string"},
				"goal": map[string]any{"type": "string"},
			}, []string{"goal"}),
		Executor: func(ctx context.Context, _ string, args map[string]any) (string, error) {
			if subagentDepth(ctx) > 0 {
				return "", fmt.Errorf("subagents: 子智能体不能再派生集群（深度上限）")
			}
			t, err := subagentManagerFor(base).SpawnFrom(ctx, argString(args, "name", ""), argString(args, "goal", ""))
			if err != nil {
				return "", err
			}
			return jsonText(t)
		},
	})
	register(agent.Tool{
		Spec: projectTool("subagents.list", "列出子智能体集群任务", nil, nil),
		Executor: func(_ context.Context, _ string, _ map[string]any) (string, error) {
			m := subagentManagerFor(base)
			return jsonText(map[string]any{"running": m.Running(), "max": m.Max(), "tasks": m.List()})
		},
	})
	register(agent.Tool{
		Spec: projectTool("subagents.result", "查看子智能体结果",
			map[string]any{"id": map[string]any{"type": "string"}}, []string{"id"}),
		Executor: func(_ context.Context, _ string, args map[string]any) (string, error) {
			t, err := subagentManagerFor(base).Result(argString(args, "id", ""))
			if err != nil {
				return "", err
			}
			return jsonText(t)
		},
	})
	register(agent.Tool{
		Spec: projectTool("subagents.stop", "停止一个子智能体",
			map[string]any{"id": map[string]any{"type": "string"}}, []string{"id"}),
		Executor: func(_ context.Context, _ string, args map[string]any) (string, error) {
			if err := subagentManagerFor(base).Stop(argString(args, "id", "")); err != nil {
				return "", err
			}
			return jsonText(map[string]any{"stopped": argString(args, "id", "")})
		},
	})
}

func envPairs(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out
}

// runSubagentGoal 是子智能体的实际执行器：独立 session、独立上下文、带深度上限。
func runSubagentGoal(ctx context.Context, base, goal string) (string, error) {
	depth := subagentDepth(ctx)
	if depth >= 2 {
		return "", fmt.Errorf("subagents: 深度上限 2，拒绝继续派生")
	}
	ctx = context.WithValue(ctx, subagentDepthKey{}, depth+1)
	applyWorkspaceConfig(base)

	provider := envOr("HUSHFUSION_AI_PROVIDER", "zai")
	model := envOr("HUSHFUSION_AI_MODEL", "")
	baseURL := os.Getenv("HUSHFUSION_AI_BASE_URL")
	cfg := agent.DefaultConfig()
	cfg.Provider, cfg.Model, cfg.BaseURL = provider, model, baseURL
	cfg.MaxBudgetTokens = agent.BudgetForModel(model, 0.6)
	cfg.Root = repoRoot()
	chat := func(ctx context.Context, req ai.ChatRequest) (*ai.ChatResponse, error) {
		return ai.Chat(ctx, provider, req, ai.ClientOptions{BaseURL: baseURL})
	}
	s := agent.NewSession(cfg, chat, agent.NewRegistry(), agentSystemPrompt())
	skills := agent.NewSkills([]string{globalSkillsDir(base), projectSkillsDir(base)}, agent.NewKeywordMatcher())
	s.Skills = skills
	_ = s.RegisterBuiltinTools(skills)
	registerProjectTools(s, base)
	if os.Getenv("HUSHFUSION_ALLOW_SHELL") == "1" {
		s.AllowWrites["shell"] = true
	}
	allowPlatformWrites(s)
	return s.Turn(ctx, goal)
}
