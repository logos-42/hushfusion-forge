package main

// tools_plugins.go —— 流程插件 / 接口：manifest 驱动的模块加载器。
// 插件目录：~/.forge/plugins/*.json（每个文件一个 manifest.Module 声明）。
// 加载只登记 manifest，不执行外部代码；执行仍需走 fail-closed 工具/沙箱。

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/logos-42/hushfusion-forge/internal/agent"
	"github.com/logos-42/hushfusion-forge/internal/modules/loader"
	"github.com/logos-42/hushfusion-forge/internal/modules/manifest"
)

func registerPluginTools(s *agent.Session, base string) {
	ml := loader.NewModuleLoader()
	pluginDir := filepath.Join(runtimeRoot(base), "plugins")
	register := func(t agent.Tool) { _ = s.RegisterTool(t) }

	register(agent.Tool{
		Spec: projectTool("plugin.list", "列出全局插件目录里的 manifest 文件", nil, nil),
		Executor: func(_ context.Context, _ string, _ map[string]any) (string, error) {
			entries, err := os.ReadDir(pluginDir)
			names := []string{}
			if err == nil {
				for _, e := range entries {
					if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
						names = append(names, strings.TrimSuffix(e.Name(), ".json"))
					}
				}
			}
			return jsonText(map[string]any{"dir": pluginDir, "plugins": names})
		},
	})

	register(agent.Tool{
		Spec: projectTool("plugin.load", "加载一个插件 manifest（按 path 或内联 manifest）",
			map[string]any{
				"path":     map[string]any{"type": "string", "description": "manifest JSON 路径"},
				"id":       map[string]any{"type": "string", "description": "模块 ID；缺省用 manifest identity.id"},
				"manifest": map[string]any{"type": "object", "description": "内联 manifest（与 path 二选一）"},
			}, nil),
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
			var m manifest.Manifest
			if err := json.Unmarshal(raw, &m); err != nil {
				return "", fmt.Errorf("plugin.load: manifest 解析失败: %w", err)
			}
			id := argString(args, "id", "")
			if id == "" {
				id = strings.TrimSpace(m.Identity.ID)
			}
			if id == "" {
				id = strings.TrimSpace(m.Identity.Name)
			}
			if id == "" {
				return "", fmt.Errorf("plugin.load: 缺模块 ID（manifest.identity.id）")
			}
			if err := ml.LoadModule(id, m); err != nil {
				return "", err
			}
			return jsonText(map[string]any{"loaded": id, "modules": ml.List()})
		},
	})

	register(agent.Tool{
		Spec: projectTool("plugin.unload", "卸载一个已加载插件",
			map[string]any{"id": map[string]any{"type": "string"}}, []string{"id"}),
		Executor: func(_ context.Context, _ string, args map[string]any) (string, error) {
			id := argString(args, "id", "")
			if id == "" {
				return "", fmt.Errorf("plugin.unload: id 为空")
			}
			if err := ml.UnloadModule(id); err != nil {
				return "", err
			}
			return jsonText(map[string]any{"unloaded": id, "modules": ml.List()})
		},
	})

	register(agent.Tool{
		Spec: projectTool("plugin.loaded", "列出当前已加载的插件", nil, nil),
		Executor: func(_ context.Context, _ string, _ map[string]any) (string, error) {
			return jsonText(ml.List())
		},
	})
}
