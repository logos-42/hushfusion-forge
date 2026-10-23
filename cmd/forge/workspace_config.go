package main

// workspace_config.go —— workspace 级模型配置（<os>/forge.config.json）。
//
// 用户要求「配置好 API 后，落到 workspace 环境变量 / config 文件」：
//   · /model 向导确认时写入 <os>/forge.config.json（0600）；
//   · 启动时读取该文件并注入环境变量（provider / model / base_url / API key），
//     从而 TUI 与 `forge os ai` 都能直接复用，不必每次 export。
//
// 安全边界：文件含 API key，权限 0600，且 os/forge.config.json 已加入 .gitignore；
// 终端输出仍只显掩码，绝不打印明文。

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/logos-42/hushfusion-forge/internal/ai"
)

// workspaceConfig 是落盘的模型配置。APIKey 为空表示「没有可用 key」。
type workspaceConfig struct {
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
	BaseURL  string `json:"base_url,omitempty"`
	APIKey   string `json:"api_key,omitempty"`
}

func workspaceConfigPath(base string) string {
	return filepath.Join(base, "forge.config.json")
}

// loadWorkspaceConfig 读 workspace 配置；缺失/损坏一律返回零值（不阻塞启动）。
func loadWorkspaceConfig(base string) workspaceConfig {
	var c workspaceConfig
	data, err := os.ReadFile(workspaceConfigPath(base))
	if err != nil {
		return c
	}
	_ = json.Unmarshal(data, &c)
	return c
}

// saveWorkspaceConfig 写入 workspace 配置（0600：含 key，不对外可读）。
func saveWorkspaceConfig(base string, c workspaceConfig) error {
	if err := os.MkdirAll(base, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(workspaceConfigPath(base), b, 0o600)
}

// applyWorkspaceConfig 把 config 注入进程环境变量（启动时调用，先于 envOr/NewClient）。
func applyWorkspaceConfig(base string) {
	_ = ai.RegisterBuiltins()
	c := loadWorkspaceConfig(base)
	if c.Provider == "" && c.Model == "" && c.BaseURL == "" && c.APIKey == "" {
		return
	}
	if c.Provider != "" {
		_ = os.Setenv("HUSHFUSION_AI_PROVIDER", c.Provider)
	}
	if c.Model != "" {
		_ = os.Setenv("HUSHFUSION_AI_MODEL", c.Model)
	}
	if c.BaseURL != "" {
		_ = os.Setenv("HUSHFUSION_AI_BASE_URL", c.BaseURL)
	}
	if c.APIKey != "" {
		// 按 provider 的约定变量名注入（custom → AI_CUSTOM_API_KEY）。
		if p := ai.Lookup(c.Provider); p != nil && len(p.EnvVars) > 0 {
			_ = os.Setenv(p.EnvVars[0], c.APIKey)
		} else {
			_ = os.Setenv("AI_CUSTOM_API_KEY", c.APIKey)
		}
	}
}
