package main

// tools_plugins.go —— 插件接口的模型侧入口。
//
// 统一的插件接口在 internal/modules/plugin：
//
//	type Plugin interface {
//	    Manifest() manifest.Manifest
//	    Actions() []string
//	    Init(ctx, host) error
//	    Execute(ctx, action, params) (any, error)
//	    Close() error
//	}
//
// 内置 Go 插件用 plugin.Func；外部进程插件用 plugin.External（每行一个 JSON）。
// 插件目录：~/.forge/plugins/*.json。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/logos-42/hushfusion-forge/internal/agent"
	"github.com/logos-42/hushfusion-forge/internal/modules/plugin"
)

type pluginHost struct{}

func (pluginHost) Logf(string, ...any) {}

// 插件注册表跨轮次复用：TUI 每轮都会装配一次会话，不能每轮重新加载/启动插件。
var (
	pluginMu   sync.Mutex
	pluginRegs = map[string]*plugin.Registry{}
)

func pluginRegistryFor(base string) *plugin.Registry {
	key := runtimeRoot(base)
	pluginMu.Lock()
	defer pluginMu.Unlock()
	if r := pluginRegs[key]; r != nil {
		return r
	}
	r := plugin.NewRegistry()
	pluginRegs[key] = r
	return r
}

func registerPluginTools(s *agent.Session, base string) {
	reg := pluginRegistryFor(base)
	pluginDir := filepath.Join(runtimeRoot(base), "plugins")
	register := func(t agent.Tool) { _ = s.RegisterTool(t) }

	register(agent.Tool{
		Spec: projectTool("plugin.spec", "返回插件接口契约与外部进程 JSON 协议", nil, nil),
		Executor: func(_ context.Context, _ string, _ map[string]any) (string, error) {
			return jsonText(map[string]any{
				"interface": "plugin.Plugin",
				"methods": []string{
					"Manifest() manifest.Manifest",
					"Actions() []string",
					"Init(ctx context.Context, host Host) error",
					"Execute(ctx context.Context, action string, params map[string]any) (any, error)",
					"Close() error",
				},
				"external_protocol": map[string]any{
					"transport": "stdin/stdout，每行一个 JSON",
					"request":   `{"action":"...","params":{...}}`,
					"response":  `{"ok":true,"result":...} 或 {"ok":false,"error":"..."}`,
				},
				"manifest": map[string]any{
					"flat":   `{"identity":{"id":"my-plugin","name":"My Plugin","type":"simulation"},"version":"1.0","command":"python3","args":["plugin.py"],"actions":["run"]}`,
					"nested": `{"manifest":{"identity":{"id":"my-plugin","name":"My Plugin","type":"simulation"},"version":"1.0"},"command":"python3","args":["plugin.py"],"actions":["run"]}`,
				},
				"dir": pluginDir,
			})
		},
	})

	register(agent.Tool{
		Spec: projectTool("plugin.list", "列出插件目录里的 manifest（不加载）", nil, nil),
		Executor: func(_ context.Context, _ string, _ map[string]any) (string, error) {
			entries, err := os.ReadDir(pluginDir)
			items := []map[string]any{}
			if err == nil {
				for _, e := range entries {
					if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
						continue
					}
					path := filepath.Join(pluginDir, e.Name())
					b, rerr := os.ReadFile(path)
					if rerr != nil {
						continue
					}
					f, perr := plugin.ParseFile(b)
					if perr != nil {
						items = append(items, map[string]any{"file": e.Name(), "error": perr.Error()})
						continue
					}
					items = append(items, map[string]any{
						"file": e.Name(), "id": f.Manifest.Identity.ID, "name": f.Manifest.Identity.Name,
						"version": f.Manifest.Version, "actions": f.Actions, "command": f.Command,
					})
				}
			}
			return jsonText(map[string]any{"dir": pluginDir, "plugins": items, "loaded": reg.List()})
		},
	})

	register(agent.Tool{
		Spec: projectTool("plugin.load", "加载一个插件（外部 command 或仅 manifest；写动作，需白名单）",
			map[string]any{
				"path":     map[string]any{"type": "string", "description": "manifest JSON 路径（相对插件目录或绝对路径）"},
				"id":       map[string]any{"type": "string", "description": "模块 ID；缺省用 manifest identity.id"},
				"manifest": map[string]any{"type": "object", "description": "内联插件声明（与 path 二选一）"},
			}, nil),
		Write: true,
		Executor: func(_ context.Context, _ string, args map[string]any) (string, error) {
			var raw []byte
			if p := argString(args, "path", ""); p != "" {
				if !filepath.IsAbs(p) {
					p = filepath.Join(pluginDir, p)
				}
				b, err := os.ReadFile(p)
				if err != nil {
					return "", fmt.Errorf("plugin.load: %w", err)
				}
				raw = b
			} else if m, ok := args["manifest"].(map[string]any); ok {
				b, err := json.Marshal(m)
				if err != nil {
					return "", err
				}
				raw = b
			} else {
				return "", fmt.Errorf("plugin.load: 需要 path 或 manifest")
			}
			f, err := plugin.ParseFile(raw)
			if err != nil {
				return "", err
			}
			id := argString(args, "id", "")
			if id == "" {
				id = strings.TrimSpace(f.Manifest.Identity.ID)
			}
			if id == "" {
				id = strings.TrimSpace(f.Manifest.Identity.Name)
			}
			if id == "" {
				return "", fmt.Errorf("plugin.load: 缺模块 ID（manifest.identity.id）")
			}
			// 显式 id 优先（允许同一 manifest 以不同实例名加载）。
			f.Manifest.Identity.ID = id

			var p plugin.Plugin
			if strings.TrimSpace(f.Command) != "" {
				p = plugin.NewExternal(f)
			} else {
				// 仅声明、无执行入口的插件：登记进目录，但调用时 fail-closed。
				p = &plugin.Func{
					M: f.Manifest, Acts: f.Actions,
					Run: func(context.Context, string, map[string]any) (any, error) {
						return nil, fmt.Errorf("plugin %q 只有 manifest，没有 command；请补 command/args", id)
					},
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := reg.Register(ctx, pluginHost{}, p); err != nil {
				return "", err
			}
			return jsonText(map[string]any{"loaded": id, "external": p != nil && f.Command != "", "plugins": reg.List()})
		},
	})

	register(agent.Tool{
		Spec: projectTool("plugin.call", "调用插件的某个 action（外部执行，需白名单）",
			map[string]any{
				"id":     map[string]any{"type": "string"},
				"action": map[string]any{"type": "string"},
				"params": map[string]any{"type": "object", "description": "action 参数对象"},
			}, []string{"id", "action"}),
		Write: true,
		Executor: func(ctx context.Context, _ string, args map[string]any) (string, error) {
			id := argString(args, "id", "")
			action := argString(args, "action", "")
			if id == "" || action == "" {
				return "", fmt.Errorf("plugin.call: id/action 不能为空")
			}
			params := map[string]any{}
			if v, ok := args["params"].(map[string]any); ok {
				params = v
			}
			out, err := reg.Execute(ctx, id, action, params)
			if err != nil {
				return "", err
			}
			return jsonText(map[string]any{"plugin": id, "action": action, "result": out})
		},
	})

	register(agent.Tool{
		Spec: projectTool("plugin.unload", "卸载并关闭插件（写动作，需白名单）",
			map[string]any{"id": map[string]any{"type": "string"}}, []string{"id"}),
		Write: true,
		Executor: func(_ context.Context, _ string, args map[string]any) (string, error) {
			id := argString(args, "id", "")
			if id == "" {
				return "", fmt.Errorf("plugin.unload: id 为空")
			}
			if err := reg.Unload(id); err != nil {
				return "", err
			}
			return jsonText(map[string]any{"unloaded": id, "plugins": reg.List()})
		},
	})

	register(agent.Tool{
		Spec: projectTool("plugin.loaded", "列出当前已加载插件及其 actions", nil, nil),
		Executor: func(_ context.Context, _ string, _ map[string]any) (string, error) {
			return jsonText(reg.List())
		},
	})
}
