package main

// tools_project.go —— 把项目里的聚变仿真、装置、插件、MCP/ACP/DIAP 能力
// 注册成 agent 可调用的工具。所有工具走 agent.ToolRegistry 的 fail-closed 闸门；
// 外部协议（MCP/ACP）统一从 ~/.forge 下的配置读取，未配置时返回明确错误而不是静默降级。

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/logos-42/hushfusion-forge/internal/agent"
	"github.com/logos-42/hushfusion-forge/internal/ai"
)

// registerProjectTools 在 TUI 与 agent CLI 装配会话时统一调用。
func registerProjectTools(s *agent.Session, base string) {
	registerFusionTools(s, base)
	registerPluginTools(s, base)
	registerMCPTools(s, base)
	registerLSPTools(s, base)
	registerWalletTools(s, base)
	registerProtocolTools(s, base)
}

// projectTool 构造一个 OpenAI-compatible function schema。
func projectTool(name, desc string, props map[string]any, required []string) ai.ToolSpec {
	if props == nil {
		props = map[string]any{}
	}
	ts := ai.ToolSpec{Type: "function"}
	ts.Function.Name = name
	ts.Function.Description = desc
	params := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		params["required"] = required
	}
	ts.Function.Parameters = params
	return ts
}

func jsonText(v any) (string, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func argString(args map[string]any, key, def string) string {
	if v, ok := args[key].(string); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return def
}

func argInt(args map[string]any, key string, def int) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return int(n)
		}
	}
	return def
}

func argStrings(args map[string]any, key string) []string {
	arr, ok := args[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, v := range arr {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, strings.TrimSpace(s))
		}
	}
	return out
}

func argFloats(args map[string]any, key string) ([]float64, error) {
	arr, ok := args[key].([]any)
	if !ok {
		return nil, fmt.Errorf("%s 必须是数字数组", key)
	}
	out := make([]float64, len(arr))
	for i, v := range arr {
		f, ok := v.(float64)
		if !ok {
			return nil, fmt.Errorf("%s[%d] 不是数字", key, i)
		}
		out[i] = f
	}
	return out, nil
}
