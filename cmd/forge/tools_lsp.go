package main

// tools_lsp.go —— 把 internal/lsp 接成模型可调用的工具。
//
// 配置来源（按优先级）：
//  1. <os>/forge.config.json 的 "lsp_servers"（workspace 覆盖）；
//  2. ai.ResolveConfig(workspace) 的 lsp_servers（全局 / .hushfusion/ai.json）；
//  3. 若都没有，自动探测本机已安装的 gopls / typescript-language-server /
//     pyright-langserver / rust-analyzer（只探测，不自动安装）。
//
// 权限：每个 server 的 Allow 白名单仍由 internal/lsp 做 fail-closed；
// 空 Allow = 全部拒绝。自动探测出来的 server 默认允许 definition/hover/references。

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/logos-42/hushfusion-forge/internal/agent"
	"github.com/logos-42/hushfusion-forge/internal/ai"
	"github.com/logos-42/hushfusion-forge/internal/lsp"
)

func projectWorkspaceRoot(base string) string {
	if abs, err := filepath.Abs(base); err == nil {
		return filepath.Dir(abs)
	}
	return filepath.Dir(base)
}

// lspConfigsFor 合并 workspace / 全局配置，并在完全没配时探测本机 server。
func lspConfigsFor(base string) []lsp.ServerConfig {
	root := projectWorkspaceRoot(base)
	var out []lsp.ServerConfig
	index := map[string]int{}

	add := func(c lsp.ServerConfig, override bool) {
		if strings.TrimSpace(c.ID) == "" {
			c.ID = c.Command
		}
		if c.ID == "" {
			return
		}
		if i, ok := index[c.ID]; ok {
			if override {
				out[i] = c
			}
			return
		}
		index[c.ID] = len(out)
		out = append(out, c)
	}

	// 全局 / .hushfusion/ai.json
	if cfg, err := ai.ResolveConfig(root); err == nil {
		for _, c := range cfg.LSPServers {
			if c != nil {
				add(*c, false)
			}
		}
	}
	// workspace 覆盖
	for _, c := range loadWorkspaceConfig(base).LSPServers {
		add(c, true)
	}

	// 自动探测（只在完全没有配置时）
	if len(out) == 0 {
		cands := []struct {
			id, cmd string
			args    []string
		}{
			{"go", "gopls", nil},
			{"typescript", "typescript-language-server", []string{"--stdio"}},
			{"python", "pyright-langserver", []string{"--stdio"}},
			{"rust", "rust-analyzer", nil},
		}
		for _, c := range cands {
			if p, ok := lookPathExtended(c.cmd); ok {
				add(lsp.ServerConfig{
					ID: c.id, Command: p, Args: c.args,
					Allow: []string{lsp.OpDefinition, lsp.OpHover, lsp.OpReferences},
				}, false)
			}
		}
	}

	for i := range out {
		if strings.TrimSpace(out[i].RootURI) == "" {
			out[i].RootURI = "file://" + filepath.ToSlash(root)
		}
	}
	return out
}

type lspRuntime struct {
	base    string
	mu      sync.Mutex
	started bool
	mgr     *lsp.Manager
	cfgs    []lsp.ServerConfig
	errs    map[string]string
}

var (
	lspMu   sync.Mutex
	lspRuns = map[string]*lspRuntime{}
)

// lookPathExtended 先查 PATH，再查 Go 的 GOPATH/bin（gopls 常装在那里但不在 PATH）。
func lookPathExtended(cmd string) (string, bool) {
	if p, err := exec.LookPath(cmd); err == nil {
		return p, true
	}
	if filepath.IsAbs(cmd) {
		if info, err := os.Stat(cmd); err == nil && !info.IsDir() {
			return cmd, true
		}
		return "", false
	}
	gopath := os.Getenv("GOPATH")
	if gopath == "" {
		if home, err := os.UserHomeDir(); err == nil {
			gopath = filepath.Join(home, "go")
		}
	}
	if gopath != "" {
		p := filepath.Join(gopath, "bin", cmd)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p, true
		}
	}
	return "", false
}

func lspRuntimeFor(base string) *lspRuntime {
	key := projectWorkspaceRoot(base)
	lspMu.Lock()
	defer lspMu.Unlock()
	if r := lspRuns[key]; r != nil {
		return r
	}
	r := &lspRuntime{base: base, errs: map[string]string{}}
	lspRuns[key] = r
	return r
}

func (r *lspRuntime) ensure() (*lsp.Manager, []lsp.ServerConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started {
		return r.mgr, r.cfgs
	}
	r.started = true
	r.cfgs = lspConfigsFor(r.base)
	r.mgr = lsp.NewManager()
	for _, cfg := range r.cfgs {
		s, err := lsp.Start(context.Background(), cfg)
		if err != nil {
			r.errs[cfg.ID] = err.Error()
			continue
		}
		r.mgr.Attach(s)
	}
	return r.mgr, r.cfgs
}

func (r *lspRuntime) pick(want string) (*lsp.Server, error) {
	mgr, cfgs := r.ensure()
	if mgr == nil || len(cfgs) == 0 {
		return nil, fmt.Errorf("没有可用的 LSP server：在 %s 加 lsp_servers，或安装 gopls（go install golang.org/x/tools/gopls@latest）", workspaceConfigPath(r.base))
	}
	if want != "" {
		if s := mgr.Server(want); s != nil {
			return s, nil
		}
		if e := r.errs[want]; e != "" {
			return nil, fmt.Errorf("lsp server %q 启动失败: %s", want, e)
		}
		return nil, fmt.Errorf("lsp server %q 未加载（可用：%s）", want, strings.Join(cfgIDs(cfgs), ", "))
	}
	if len(cfgs) == 1 {
		if s := mgr.Server(cfgs[0].ID); s != nil {
			return s, nil
		}
		return nil, fmt.Errorf("lsp server %q 启动失败: %s", cfgs[0].ID, r.errs[cfgs[0].ID])
	}
	return nil, fmt.Errorf("有多个 LSP server，请用 server 指定：%s", strings.Join(cfgIDs(cfgs), ", "))
}

func cfgIDs(cfgs []lsp.ServerConfig) []string {
	out := make([]string, 0, len(cfgs))
	for _, c := range cfgs {
		out = append(out, c.ID)
	}
	return out
}

func lspFileURI(root, file string) string {
	if strings.HasPrefix(file, "file://") {
		return file
	}
	if !filepath.IsAbs(file) {
		file = filepath.Join(root, file)
	}
	return "file://" + filepath.ToSlash(file)
}

// lspPosition 把模型给的 1-based line/column 转成 LSP 的 0-based。
func lspPosition(args map[string]any) (int, int) {
	line := argInt(args, "line", 1)
	col := argInt(args, "column", 1)
	if line < 1 {
		line = 1
	}
	if col < 1 {
		col = 1
	}
	return line - 1, col - 1
}

func registerLSPTools(s *agent.Session, base string) {
	rt := lspRuntimeFor(base)
	root := projectWorkspaceRoot(base)
	register := func(t agent.Tool) { _ = s.RegisterTool(t) }

	register(agent.Tool{
		Spec: projectTool("lsp.status", "查看 LSP 配置、已启动 server 与启动错误", nil, nil),
		Executor: func(_ context.Context, _ string, _ map[string]any) (string, error) {
			cfgs := lspConfigsFor(base)
			rt.mu.Lock()
			running := []string{}
			if rt.mgr != nil {
				for _, c := range cfgs {
					if rt.mgr.Server(c.ID) != nil {
						running = append(running, c.ID)
					}
				}
			}
			errs := map[string]string{}
			for k, v := range rt.errs {
				errs[k] = v
			}
			rt.mu.Unlock()
			return jsonText(map[string]any{
				"workspace": root,
				"config":    workspaceConfigPath(base),
				"servers":   cfgs,
				"running":   running,
				"errors":    errs,
				"hint":      `在 os/forge.config.json 加："lsp_servers":[{"id":"go","command":"gopls","allow":["definition","hover","references"]}]`,
			})
		},
	})

	definition := func(ctx context.Context, _ string, args map[string]any) (string, error) {
		file := argString(args, "file", "")
		if file == "" {
			file = argString(args, "uri", "")
		}
		if file == "" {
			return "", fmt.Errorf("需要 file（或 uri）")
		}
		srv, err := rt.pick(argString(args, "server", ""))
		if err != nil {
			return "", err
		}
		qctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		line, col := lspPosition(args)
		locs, err := srv.Definition(qctx, lspFileURI(root, file), line, col)
		if err != nil {
			return "", err
		}
		return jsonText(locs)
	}

	register(agent.Tool{
		Spec: projectTool("lsp.definition", "跳转到符号定义（file + 1-based line/column）",
			map[string]any{
				"file":   map[string]any{"type": "string", "description": "文件路径（相对 workspace 或绝对）"},
				"line":   map[string]any{"type": "integer", "description": "1-based 行号"},
				"column": map[string]any{"type": "integer", "description": "1-based 列号"},
				"server": map[string]any{"type": "string", "description": "可选：多 server 时指定 id"},
			}, []string{"file", "line", "column"}),
		Executor: definition,
	})

	register(agent.Tool{
		Spec: projectTool("lsp.hover", "取符号悬停信息（类型/文档）",
			map[string]any{
				"file":   map[string]any{"type": "string"},
				"line":   map[string]any{"type": "integer"},
				"column": map[string]any{"type": "integer"},
				"server": map[string]any{"type": "string"},
			}, []string{"file", "line", "column"}),
		Executor: func(ctx context.Context, _ string, args map[string]any) (string, error) {
			file := argString(args, "file", "")
			if file == "" {
				return "", fmt.Errorf("需要 file")
			}
			srv, err := rt.pick(argString(args, "server", ""))
			if err != nil {
				return "", err
			}
			qctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			line, col := lspPosition(args)
			return srv.Hover(qctx, lspFileURI(root, file), line, col)
		},
	})

	register(agent.Tool{
		Spec: projectTool("lsp.references", "找符号引用（含声明）",
			map[string]any{
				"file":   map[string]any{"type": "string"},
				"line":   map[string]any{"type": "integer"},
				"column": map[string]any{"type": "integer"},
				"server": map[string]any{"type": "string"},
			}, []string{"file", "line", "column"}),
		Executor: func(ctx context.Context, _ string, args map[string]any) (string, error) {
			file := argString(args, "file", "")
			if file == "" {
				return "", fmt.Errorf("需要 file")
			}
			srv, err := rt.pick(argString(args, "server", ""))
			if err != nil {
				return "", err
			}
			qctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			line, col := lspPosition(args)
			locs, err := srv.References(qctx, lspFileURI(root, file), line, col, true)
			if err != nil {
				return "", err
			}
			return jsonText(locs)
		},
	})

	register(agent.Tool{
		Spec: projectTool("lsp.caps", "查看 LSP server 在 initialize 时汇报的能力",
			map[string]any{"server": map[string]any{"type": "string"}}, nil),
		Executor: func(ctx context.Context, _ string, args map[string]any) (string, error) {
			srv, err := rt.pick(argString(args, "server", ""))
			if err != nil {
				return "", err
			}
			qctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			caps, err := srv.Caps(qctx)
			if err != nil {
				return "", err
			}
			return jsonText(caps)
		},
	})
}
