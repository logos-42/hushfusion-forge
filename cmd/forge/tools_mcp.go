package main

// tools_mcp.go —— 把 internal/mcp 的 stdio 客户端接成模型可调用的工具。
// 配置：~/.forge/mcp.json（或 HUSHFUSION_MCP_CONFIG）
//
//	{"servers":[{"name":"fs","command":"npx","args":["-y","@modelcontextprotocol/server-filesystem","/tmp"],
//	             "env":["KEY=VALUE"],"allow":["read_file","list_directory"]}]}
//
// Allow 为空 = 全部拒绝（fail-closed）；"*" = 全允许。权限门在 mcp.Client 内。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/logos-42/hushfusion-forge/internal/agent"
	"github.com/logos-42/hushfusion-forge/internal/mcp"
)

type mcpStdio struct {
	r io.Reader
	w io.WriteCloser
}

func (p *mcpStdio) Read(b []byte) (int, error)  { return p.r.Read(b) }
func (p *mcpStdio) Write(b []byte) (int, error) { return p.w.Write(b) }
func (p *mcpStdio) Close() error                { return p.w.Close() }

type mcpRuntime struct {
	base    string
	mu      sync.Mutex
	clients map[string]*mcp.Client
	procs   map[string]*exec.Cmd
}

func newMCPRuntime(base string) *mcpRuntime {
	return &mcpRuntime{base: base, clients: map[string]*mcp.Client{}, procs: map[string]*exec.Cmd{}}
}

func (r *mcpRuntime) configPath() string {
	if p := os.Getenv("HUSHFUSION_MCP_CONFIG"); p != "" {
		return p
	}
	return filepath.Join(runtimeRoot(r.base), "mcp.json")
}

func (r *mcpRuntime) servers() ([]mcp.ServerConfig, string, error) {
	path := r.configPath()
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, path, err
	}
	var cfg struct {
		Servers []mcp.ServerConfig `json:"servers"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, path, fmt.Errorf("mcp: 配置解析失败: %w", err)
	}
	return cfg.Servers, path, nil
}

func (r *mcpRuntime) client(name string) (*mcp.Client, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c := r.clients[name]; c != nil {
		return c, nil
	}
	servers, _, err := r.servers()
	if err != nil {
		return nil, err
	}
	var cfg *mcp.ServerConfig
	for i := range servers {
		if servers[i].Name == name {
			cfg = &servers[i]
			break
		}
	}
	if cfg == nil {
		return nil, fmt.Errorf("mcp: 未配置 server %q", name)
	}
	if cfg.Command == "" {
		return nil, fmt.Errorf("mcp: server %q 缺 command", name)
	}
	cmd := exec.Command(cfg.Command, cfg.Args...)
	cmd.Env = append(os.Environ(), cfg.Env...)
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("mcp: 启动 %q 失败: %w", name, err)
	}
	c := mcp.NewClient(*cfg, &mcpStdio{r: stdout, w: stdin})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.WaitForInit(ctx); err != nil {
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("mcp: server %q 初始化失败: %w", name, err)
	}
	r.clients[name] = c
	r.procs[name] = cmd
	return c, nil
}

// MCP 子进程跨轮次复用，避免每轮重复拉起。
var (
	mcpMu       sync.Mutex
	mcpRuntimes = map[string]*mcpRuntime{}
)

func mcpRuntimeFor(base string) *mcpRuntime {
	key := runtimeRoot(base)
	mcpMu.Lock()
	defer mcpMu.Unlock()
	if r := mcpRuntimes[key]; r != nil {
		return r
	}
	r := newMCPRuntime(base)
	mcpRuntimes[key] = r
	return r
}

func registerMCPTools(s *agent.Session, base string) {
	rt := mcpRuntimeFor(base)
	register := func(t agent.Tool) { _ = s.RegisterTool(t) }

	register(agent.Tool{
		Spec: projectTool("mcp.servers", "列出已配置的 MCP server（未配置时给出配置路径）", nil, nil),
		Executor: func(_ context.Context, _ string, _ map[string]any) (string, error) {
			servers, path, err := rt.servers()
			if err != nil {
				return jsonText(map[string]any{"configured": false, "path": path, "error": err.Error()})
			}
			list := make([]map[string]any, 0, len(servers))
			for _, sv := range servers {
				list = append(list, map[string]any{
					"name": sv.Name, "command": sv.Command, "args": sv.Args, "allow": sv.Allow,
				})
			}
			return jsonText(map[string]any{"configured": true, "path": path, "servers": list})
		},
	})

	register(agent.Tool{
		Spec: projectTool("mcp.tools", "列出一个 MCP server 暴露的工具",
			map[string]any{"server": map[string]any{"type": "string"}}, []string{"server"}),
		Executor: func(_ context.Context, _ string, args map[string]any) (string, error) {
			name := argString(args, "server", "")
			if name == "" {
				return "", fmt.Errorf("mcp.tools: server 为空")
			}
			c, err := rt.client(name)
			if err != nil {
				return "", err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			tools, err := c.Tools(ctx)
			if err != nil {
				return "", err
			}
			return jsonText(tools)
		},
	})

	register(agent.Tool{
		Spec: projectTool("mcp.call", "调用一个 MCP server 工具（受 allow 白名单约束；外部执行，需写白名单）",
			map[string]any{
				"server":    map[string]any{"type": "string"},
				"tool":      map[string]any{"type": "string"},
				"arguments": map[string]any{"type": "object", "description": "工具参数对象"},
			}, []string{"server", "tool"}),
		Write: true,
		Executor: func(ctx context.Context, _ string, args map[string]any) (string, error) {
			name := argString(args, "server", "")
			tool := argString(args, "tool", "")
			if name == "" || tool == "" {
				return "", fmt.Errorf("mcp.call: server/tool 不能为空")
			}
			callArgs := map[string]any{}
			if v, ok := args["arguments"].(map[string]any); ok {
				callArgs = v
			}
			c, err := rt.client(name)
			if err != nil {
				return "", err
			}
			out, err := c.CallTool(ctx, tool, callArgs)
			if err != nil {
				return "", err
			}
			return out, nil
		},
	})
}
