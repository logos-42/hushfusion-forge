package main

// forge lsp —— internal/lsp 的 CLI 薄接入层。
//
// 设计：CLI 只负责解析参数、用 lsp.Manager/lsp.Server 把请求转成一次 LSP 查询，
// 不重复任何传输/握手/权限逻辑（那些都在 internal/lsp，已单测覆盖）。命令形态：
//
//	forge lsp list  --config servers.json
//	forge lsp definition  <file:line:col> --command gopls [--root .] [--allow *]
//	forge lsp hover       <file:line:col> --command gopls
//	forge lsp references  <file:line:col> --command gopls
//
// --config 可加载一份 lsp.ServerConfig 的 JSON 数组（复用其 json 标签），
// 这样常用的 server 集合可落文件复用。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/logos-42/hushfusion-forge/internal/lsp"
)

func cmdLSP(args []string) int {
	if len(args) == 0 {
		lspUsage(os.Stderr)
		return 2
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list":
		return lspList(rest)
	case "definition", "hover", "references":
		return lspQueryCmd(sub, rest)
	case "help", "-h", "--help":
		lspUsage(os.Stdout)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "forge lsp: unknown subcommand %q\n\n", sub)
		lspUsage(os.Stderr)
		return 2
	}
}

// lspList 打印 --config 里的 LSP server 配置。
func lspList(args []string) int {
	fs := newFlagSet("lsp list", "lsp list --config servers.json",
		"列出 LSP server 配置（lsp.ServerConfig JSON 数组）")
	configPath := fs.String("config", "", "ServerConfig JSON 数组文件路径")
	if rc := parseFlags(fs, args); rc != -1 {
		return rc
	}
	if *configPath == "" {
		return fail("lsp list: --config 必填")
	}
	cfgs, err := loadLSPServers(*configPath)
	if err != nil {
		return fail("lsp list: %v", err)
	}
	for _, c := range cfgs {
		fmt.Printf("  %-12s cmd=%-16s args=%v allow=%v root=%s\n", c.ID, c.Command, c.Args, c.Allow, c.RootURI)
	}
	return 0
}

// lspQueryCmd 启动 server 并查询一个 op（definition/hover/references）。
func lspQueryCmd(op string, args []string) int {
	blurb := fmt.Sprintf("查询 %s：forge lsp %s --pos <file:line:col> --command <server>", op, op)
	fs := newFlagSet("lsp "+op, "lsp "+op+" --pos <file:line:col> [--command X]",
		blurb+"\n--pos 用 1-based 行列（如 main.go:12:5）。")
	pos := fs.String("pos", "", "位置 file:line:col（1-based），如 main.go:12:5")
	command := fs.String("command", "", "LSP server 可执行文件（如 gopls）")
	root := fs.String("root", "", "workspace 根目录（默认 cwd），决定 file:// rootUri")
	allow := fs.String("allow", "*", "允许的操作白名单（逗号分隔；* = 全部）")
	id := fs.String("id", "", "server 逻辑 id（默认取 command 名）")
	configPath := fs.String("config", "", "可选：从 ServerConfig JSON 数组文件加载（与 flag 合并）")
	if rc := parseFlags(fs, args); rc != -1 {
		return rc
	}
	if *pos == "" {
		return fail("lsp %s: --pos 必填（如 --pos main.go:12:5）", op)
	}

	cfg := lsp.ServerConfig{Allow: splitComma(*allow)}
	if *id != "" {
		cfg.ID = *id
	}
	if *root == "" {
		if wd, err := os.Getwd(); err == nil {
			*root = wd
		}
	}
	cfg.RootURI = "file://" + *root

	if *configPath != "" {
		cfgs, err := loadLSPServers(*configPath)
		if err != nil {
			return fail("lsp %s: %v", op, err)
		}
		sel := *id
		if sel == "" {
			sel = *command
		}
		chosen := pickServer(cfgs, sel)
		if chosen == nil {
			return fail("lsp %s: --config 里找不到 server（id=%q）", op, sel)
		}
		cfg = *chosen // flag 的 allow/id/root 已先设，config 整体覆盖（含 command/args）
		if cfg.ID == "" {
			cfg.ID = *command
		}
		if cfg.Allow == nil {
			cfg.Allow = splitComma(*allow)
		}
		if cfg.RootURI == "" {
			cfg.RootURI = "file://" + *root
		}
	} else {
		if *command == "" {
			return fail("lsp %s: --command 必填（或给 --config）", op)
		}
		cfg.Command = *command
		if cfg.ID == "" {
			cfg.ID = *command
		}
	}

	uri, line, col, err := parseFilePos(*pos)
	if err != nil {
		return fail("lsp %s: %v", op, err)
	}
	ctx := context.Background()
	s, err := lsp.Start(ctx, cfg)
	if err != nil {
		return fail("lsp %s: 启动 server 失败: %v", op, err)
	}
	defer s.Close()
	out, err := lspQuery(s, op, uri, line, col)
	if err != nil {
		return fail("lsp %s: %v", op, err)
	}
	fmt.Println(out)
	return 0
}

// lspQuery 在已就绪的 server 上执行一个 op（定义给测试可复用纯逻辑部分）。
func lspQuery(s *lsp.Server, op, uri string, line, col int) (string, error) {
	ctx := context.Background()
	switch op {
	case "definition":
		locs, err := s.Definition(ctx, uri, line, col)
		if err != nil {
			return "", err
		}
		return formatLSP(locs), nil
	case "hover":
		return s.Hover(ctx, uri, line, col)
	case "references":
		locs, err := s.References(ctx, uri, line, col, true)
		if err != nil {
			return "", err
		}
		return formatLSP(locs), nil
	}
	return "", fmt.Errorf("未知 op %q", op)
}

// parseFilePos 把 "path:line:col"（1-based）解析成 file:// URI 与 0-based 行列。
func parseFilePos(s string) (uri string, line, col int, err error) {
	i1 := strings.LastIndex(s, ":")
	if i1 < 0 {
		return "", 0, 0, fmt.Errorf("格式应为 path:line:col")
	}
	i2 := strings.LastIndex(s[:i1], ":")
	if i2 < 0 {
		return "", 0, 0, fmt.Errorf("格式应为 path:line:col（需两个冒号）")
	}
	path := s[:i2]
	line, err = strconv.Atoi(strings.TrimSpace(s[i2+1 : i1]))
	if err != nil {
		return "", 0, 0, fmt.Errorf("行号非法: %w", err)
	}
	col, err = strconv.Atoi(strings.TrimSpace(s[i1+1:]))
	if err != nil {
		return "", 0, 0, fmt.Errorf("列号非法: %w", err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", 0, 0, err
	}
	return "file://" + abs, line - 1, col - 1, nil
}

func splitComma(s string) []string {
	out := make([]string, 0)
	for _, p := range strings.Split(s, ",") {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func loadLSPServers(path string) ([]lsp.ServerConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfgs []lsp.ServerConfig
	if err := json.Unmarshal(raw, &cfgs); err != nil {
		return nil, fmt.Errorf("解析 %s: %w", path, err)
	}
	return cfgs, nil
}

func pickServer(cfgs []lsp.ServerConfig, id string) *lsp.ServerConfig {
	if id == "" && len(cfgs) > 0 {
		return &cfgs[0]
	}
	for i := range cfgs {
		if cfgs[i].ID == id {
			return &cfgs[i]
		}
	}
	return nil
}

func formatLSP(locs []lsp.Location) string {
	if len(locs) == 0 {
		return "（无结果）"
	}
	var b strings.Builder
	for _, l := range locs {
		fmt.Fprintf(&b, "%s:%d:%d\n", strings.TrimPrefix(l.URI, "file://"), l.Range.Start.Line+1, l.Range.Start.Character+1)
	}
	return strings.TrimRight(b.String(), "\n")
}

func lspUsage(w *os.File) {
	fmt.Fprintf(w, `usage: forge lsp <list|definition|hover|references> [flags]

LSP server 查询（internal/lsp，复用 MCP 的 init 门 + 权限门）：

  list                  列出 --config 文件里的 LSP server 配置
  definition  --pos P   跳转到符号定义（需 --command）
  hover       --pos P   取符号悬停信息
  references  --pos P   找符号引用

--pos P = path:line:col（1-based）。示例：
  forge lsp definition --pos main.go:12:5 --command gopls
  forge lsp list --config .hushfusion/lsp.json
`)
}
