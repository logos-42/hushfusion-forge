package ai

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/logos-42/hushfusion-forge/internal/lsp"
)

// v1 配置：直接含 EnvVars 切片，版本号已为 1。
func TestParseConfigV1(t *testing.T) {
	raw := []byte(`{
  "_config_version": 1,
  "default_provider": "openrouter",
  "providers": [
    {"name":"openrouter","base_url":"https://openrouter.ai/api/v1","default_model":"openai/gpt-5","env_vars":["OPENROUTER_API_KEY"]}
  ]
}`)
	cfg, err := ParseConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConfigVersion != 1 {
		t.Fatalf("版本应为 1，got %d", cfg.ConfigVersion)
	}
	if cfg.Default != "openrouter" {
		t.Fatalf("Default = %q", cfg.Default)
	}
	if len(cfg.Providers) != 1 || cfg.Providers[0].EnvVars[0] != "OPENROUTER_API_KEY" {
		t.Fatalf("provider 解析错误: %+v", cfg.Providers)
	}
}

// v0 配置：用遗留单字段 api_key_env、无 _config_version → 应迁移到 v1 并把
// api_key_env 并入 EnvVars、版本置 1。
func TestParseConfigV0Migration(t *testing.T) {
	raw := []byte(`{
  "default_provider": "zai",
  "providers": [
    {"name":"zai","base_url":"https://api.z.ai/api/paas/v4","default_model":"glm-5.3-flash","api_key_env":"GLM_API_KEY"}
  ]
}`)
	cfg, err := ParseConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ConfigVersion != 1 {
		t.Fatalf("迁移后应到版本 1，got %d", cfg.ConfigVersion)
	}
	if len(cfg.Providers) != 1 {
		t.Fatalf("应解析出 1 个 provider，got %d", len(cfg.Providers))
	}
	p := cfg.Providers[0]
	if len(p.EnvVars) != 1 || p.EnvVars[0] != "GLM_API_KEY" {
		t.Fatalf("api_key_env 应并入 EnvVars，got %v", p.EnvVars)
	}
}

// env 优先：环境变量应覆盖配置里的同名 provider 设置。
func TestApplyEnvOverrides(t *testing.T) {
	t.Setenv("HUSHFUSION_AI_DEFAULT", "deepseek")
	t.Setenv("HUSHFUSION_AI_OPENROUTER_KEYS", "k1,k2")
	t.Setenv("HUSHFUSION_AI_OPENROUTER_BASEURL", "https://proxy.local/v1")

	cfg := &Config{
		ConfigVersion: 1,
		Default:       "openrouter",
		Providers: []*Profile{
			{Name: "openrouter", BaseURL: "https://openrouter.ai/api/v1", EnvVars: []string{"OPENROUTER_API_KEY"}},
		},
	}
	ApplyEnvOverrides(cfg)

	if cfg.Default != "deepseek" {
		t.Fatalf("env 应覆盖 Default，got %q", cfg.Default)
	}
	p := cfg.Providers[0]
	if len(p.Keys) != 2 || p.Keys[0] != "k1" || p.Keys[1] != "k2" {
		t.Fatalf("env 应覆盖 Keys，got %v", p.Keys)
	}
	if p.BaseURL != "https://proxy.local/v1" {
		t.Fatalf("env 应覆盖 BaseURL，got %q", p.BaseURL)
	}
}

// profile-per-workspace：工作区同名 provider 覆盖全局，独有 provider 追加，
// 全局 Default 在被覆盖前保留。
func TestMergeWorkspace(t *testing.T) {
	global := &Config{
		ConfigVersion: 1,
		Default:       "openrouter",
		Providers: []*Profile{
			{Name: "openrouter", BaseURL: "https://openrouter.ai/api/v1"},
			{Name: "zai", BaseURL: "https://api.z.ai/api/paas/v4"},
		},
	}
	ws := &Config{
		ConfigVersion: 1,
		Default:       "zai",
		Providers: []*Profile{
			{Name: "openrouter", BaseURL: "https://proxy.local/v1"}, // 覆盖
			{Name: "local", BaseURL: "http://127.0.0.1:11434/v1"},   // 追加
		},
	}
	merged := MergeWorkspace(global, ws)

	if merged.Default != "zai" {
		t.Fatalf("工作区 Default 应覆盖，got %q", merged.Default)
	}
	if len(merged.Providers) != 3 {
		t.Fatalf("应有 3 个 provider（2 全局 + 1 工作区独有），got %d", len(merged.Providers))
	}
	// 同名 openrouter 应被工作区覆盖
	for _, p := range merged.Providers {
		if p.Name == "openrouter" && p.BaseURL != "https://proxy.local/v1" {
			t.Fatalf("openrouter 应被工作区覆盖，got %q", p.BaseURL)
		}
	}
	// 全局 zai 仍在
	foundZai := false
	for _, p := range merged.Providers {
		if p.Name == "zai" {
			foundZai = true
		}
	}
	if !foundZai {
		t.Fatal("全局 zai 应保留")
	}
}

// MergeWorkspace(nil) 应原样返回全局。
func TestMergeWorkspaceNilWS(t *testing.T) {
	global := &Config{ConfigVersion: 1, Providers: []*Profile{{Name: "zai"}}}
	merged := MergeWorkspace(global, nil)
	if len(merged.Providers) != 1 || merged.Providers[0].Name != "zai" {
		t.Fatal("nil 工作区应原样返回全局")
	}
}

// ResolveConfig 串起「全局文件 → 工作区文件 → env」，端到端验证合并。
func TestResolveConfig(t *testing.T) {
	dir := t.TempDir()
	wsDir := filepath.Join(dir, "repo")
	if err := os.MkdirAll(filepath.Join(wsDir, ".hushfusion"), 0o755); err != nil {
		t.Fatal(err)
	}
	globalPath := filepath.Join(dir, "ai.json")
	wsPath := filepath.Join(wsDir, ".hushfusion", "ai.json")

	global := `{"_config_version":1,"default_provider":"openrouter","providers":[{"name":"openrouter","base_url":"https://openrouter.ai/api/v1"}]}`
	ws := `{"_config_version":1,"providers":[{"name":"openrouter","base_url":"https://proxy.local/v1"},{"name":"local","base_url":"http://127.0.0.1:11434/v1"}]}`
	if err := os.WriteFile(globalPath, []byte(global), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wsPath, []byte(ws), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUSHFUSION_AI_CONFIG", globalPath)
	t.Setenv("HUSHFUSION_AI_OPENROUTER_BASEURL", "https://env.override/v1")

	cfg, err := ResolveConfig(wsDir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Default != "openrouter" {
		t.Fatalf("Default 应来自全局，got %q", cfg.Default)
	}
	if len(cfg.Providers) != 2 {
		t.Fatalf("应有 2 个 provider，got %d", len(cfg.Providers))
	}
	for _, p := range cfg.Providers {
		if p.Name == "openrouter" && p.BaseURL != "https://env.override/v1" {
			t.Fatalf("env 应最后覆盖 BaseURL，got %q", p.BaseURL)
		}
	}
}

// RegisterConfig 应把配置 provider 注册进注册表（含校验）。
func TestRegisterConfig(t *testing.T) {
	cfg := &Config{
		ConfigVersion: 1,
		Providers: []*Profile{
			{Name: "cfgtest", BaseURL: "https://cfg.test/v1", EnvVars: []string{"CFG_TEST_KEY"}},
		},
	}
	if err := RegisterConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if Lookup("cfgtest") == nil {
		t.Fatal("cfgtest 应已被注册")
	}
}

// MergeWorkspace 应合并 lsp_servers（按 id 覆盖 + 独有追加）。
func TestMergeWorkspaceLSPServers(t *testing.T) {
	global := &Config{
		ConfigVersion: 1,
		LSPServers: []*lsp.ServerConfig{
			{ID: "go", Command: "gopls", RootURI: "file:///g"},
		},
	}
	ws := &Config{
		ConfigVersion: 1,
		LSPServers: []*lsp.ServerConfig{
			{ID: "go", Command: "gopls-custom", RootURI: "file:///ws"}, // 同名覆盖
			{ID: "py", Command: "pylsp"},                               // 独有追加
		},
	}
	m := MergeWorkspace(global, ws)
	if len(m.LSPServers) != 2 {
		t.Fatalf("lsp_servers 应 2 个，got %d", len(m.LSPServers))
	}
	byID := map[string]*lsp.ServerConfig{}
	for _, s := range m.LSPServers {
		byID[s.ID] = s
	}
	if byID["go"].Command != "gopls-custom" || byID["go"].RootURI != "file:///ws" {
		t.Fatalf("go 应被工作区覆盖: %+v", byID["go"])
	}
	if byID["py"].Command != "pylsp" {
		t.Fatalf("py 应追加: %+v", byID["py"])
	}
}

// ResolveConfig 应把全局 + 工作区的 lsp_servers 都合并进来。
func TestResolveConfigLSPServers(t *testing.T) {
	dir := t.TempDir()
	globalPath := filepath.Join(dir, "global.json")
	wsPath := filepath.Join(dir, ".hushfusion")
	_ = os.MkdirAll(wsPath, 0o755)
	if err := os.WriteFile(globalPath, []byte(`{
  "_config_version": 1,
  "lsp_servers": [{"id":"go","command":"gopls"}]
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wsPath, "ai.json"), []byte(`{
  "_config_version": 1,
  "lsp_servers": [{"id":"py","command":"pylsp"}]
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HUSHFUSION_AI_CONFIG", globalPath)
	cfg, err := ResolveConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.LSPServers) != 2 {
		t.Fatalf("lsp_servers 应合并为 2 个，got %d: %+v", len(cfg.LSPServers), cfg.LSPServers)
	}
}
