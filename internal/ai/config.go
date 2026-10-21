// Package ai 的持久化配置层（学 Hermes 的版本化、可迁移 config）。
//
// 本文件落地用户清单里的「schema 标签化配置 + env 优先 + profile-per-workspace +
// 版本化迁移」四件事：
//
//  1. Config 是带 _config_version 的 schema 标签化结构（Providers 即 Profile 列表）。
//  2. 版本化迁移：LoadConfig/ParseConfig 识别旧版本（v0 用单字段 api_key_env），
//     经 MigrateConfig 的版本阶梯自动升到 CurrentConfigVersion（失败 fail-closed）。
//  3. env 优先：ApplyEnvOverrides 让环境变量覆盖配置（HUSHFUSION_AI_<NAME>_KEYS/_BASEURL），
//     env 的存在即「在配置之上覆盖」，故 env 胜。
//  4. profile-per-workspace：MergeWorkspace 把 <workspace>/.hushfusion/ai.json 合并进
//     全局配置（同名覆盖、独有追加）；ResolveConfig 串起「全局 → 工作区 → env」。
//
// 设计纪律：配置只是运行时的「输入来源之一」，与内置目录（Builtins）叠加而非替换——
// 调用方应先 RegisterBuiltins（保证内置可用），再用 RegisterConfig 让配置覆盖/增补。
// 凭据永不落库：配置只描述 env 名 / 显式 key，绝不打印 key 值。
package ai

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CurrentConfigVersion 是当前 Config schema 版本。加载旧配置时自动迁移到它。
const CurrentConfigVersion = 1

// Config 是持久化的 AI 接入配置（schema 标签化，可 JSON 序列化）。
//
// _config_version 用于版本化迁移；Providers 是 provider 配置的权威来源，
// 同名后者覆盖前者。
type Config struct {
	ConfigVersion int        `json:"_config_version" jsonschema:"description=配置 schema 版本；加载时自动迁移到当前版"`
	Default       string     `json:"default_provider,omitempty" jsonschema:"description=默认 provider 名（--provider 缺省时生效）"`
	Providers     []*Profile `json:"providers,omitempty" jsonschema:"description=provider 配置列表；同名后者覆盖前者"`
}

// ---- 加载 + 迁移 ----

// LoadConfig 从文件读取并解析配置（含版本化迁移 + 基础校验）。
func LoadConfig(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseConfig(raw)
}

// ParseConfig 解析配置字节（自动识别版本并迁移）。
func ParseConfig(raw []byte) (*Config, error) {
	var peek struct {
		ConfigVersion int `json:"_config_version"`
	}
	if err := json.Unmarshal(raw, &peek); err != nil {
		return nil, fmt.Errorf("ai: 配置不是合法 JSON: %w", err)
	}
	var cfg Config
	if peek.ConfigVersion <= 0 {
		// v0：provider 用单一 api_key_env 字段，需转成 EnvVars 切片。
		var old configV0
		if err := json.Unmarshal(raw, &old); err != nil {
			return nil, fmt.Errorf("ai: 解析 v0 配置失败: %w", err)
		}
		cfg = old.toV1()
	} else {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, fmt.Errorf("ai: 解析配置失败: %w", err)
		}
	}
	if err := MigrateConfig(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// configV0 是旧配置结构（provider 用单一 api_key_env）。
type configV0 struct {
	ConfigVersion int         `json:"_config_version"`
	Default       string      `json:"default_provider,omitempty"`
	Providers     []profileV0 `json:"providers,omitempty"`
}

type profileV0 struct {
	Name            string   `json:"name"`
	Aliases         []string `json:"aliases,omitempty"`
	DisplayName     string   `json:"display_name,omitempty"`
	APIKeyEnv       string   `json:"api_key_env,omitempty"` // 旧单字段
	BaseURL         string   `json:"base_url,omitempty"`
	ModelsURL       string   `json:"models_url,omitempty"`
	DefaultModel    string   `json:"default_model,omitempty"`
	DefaultAuxModel string   `json:"default_aux_model,omitempty"`
	APIMode         APIMode  `json:"api_mode,omitempty"`
	Fallbacks       []string `json:"fallbacks,omitempty"`
}

func (c configV0) toV1() Config {
	out := Config{ConfigVersion: 0, Default: c.Default}
	for _, p := range c.Providers {
		np := &Profile{
			Name:            p.Name,
			Aliases:         p.Aliases,
			DisplayName:     p.DisplayName,
			BaseURL:         p.BaseURL,
			ModelsURL:       p.ModelsURL,
			DefaultModel:    p.DefaultModel,
			DefaultAuxModel: p.DefaultAuxModel,
			APIMode:         p.APIMode,
			Fallbacks:       p.Fallbacks,
		}
		if p.APIKeyEnv != "" {
			np.EnvVars = []string{p.APIKeyEnv}
		}
		out.Providers = append(out.Providers, np)
	}
	return out
}

// MigrateConfig 把配置升到 CurrentConfigVersion（版本阶梯，逐步迁移）。
func MigrateConfig(cfg *Config) error {
	for cfg.ConfigVersion < CurrentConfigVersion {
		switch cfg.ConfigVersion {
		case 0:
			migrateV0ToV1(cfg)
			cfg.ConfigVersion = 1
		default:
			return fmt.Errorf("ai: 未知配置版本 %d（当前 %d）", cfg.ConfigVersion, CurrentConfigVersion)
		}
	}
	return nil
}

// migrateV0ToV1：v0→v1 的迁移步（api_key_env→EnvVars 已在 toV1 完成，
// 这里兜底归一化：空 EnvVars 收为 nil、空 APIMode 补默认）。
func migrateV0ToV1(cfg *Config) {
	for _, p := range cfg.Providers {
		if len(p.EnvVars) == 0 {
			p.EnvVars = nil
		}
		if p.APIMode == "" {
			p.APIMode = ModeChatCompletions
		}
	}
}

// ---- env 优先（环境变量覆盖配置） ----

// ApplyEnvOverrides 用环境变量覆盖配置（env 优先）：
//
//	HUSHFUSION_AI_DEFAULT         → 默认 provider
//	HUSHFUSION_AI_<NAME>_KEYS     → 该 provider 的显式 key 列表（逗号分隔），覆盖 Keys/EnvVars
//	HUSHFUSION_AI_<NAME>_BASEURL  → 该 provider 的 BaseURL
//
// 环境变量的存在即表示「用户要在配置之上覆盖」，故 env 胜。
func ApplyEnvOverrides(cfg *Config) {
	if v := os.Getenv("HUSHFUSION_AI_DEFAULT"); v != "" {
		cfg.Default = v
	}
	for _, p := range cfg.Providers {
		key := envKey(p.Name)
		if v := os.Getenv(key + "_KEYS"); v != "" {
			p.Keys = splitNonEmpty(v)
		}
		if v := os.Getenv(key + "_BASEURL"); v != "" {
			p.BaseURL = v
		}
	}
}

func envKey(name string) string {
	return "HUSHFUSION_AI_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
}

func splitNonEmpty(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// ---- profile-per-workspace（合并） ----

// MergeWorkspace 把工作区配置合进全局配置（不改动入参）：
// 工作区同名 provider 覆盖全局，工作区独有 provider 追加；
// 全局 Default 在两者皆空时保留。返回新配置。
func MergeWorkspace(global, ws *Config) *Config {
	if ws == nil {
		return global
	}
	merged := &Config{
		ConfigVersion: CurrentConfigVersion,
		Default:       global.Default,
		Providers:     append([]*Profile{}, global.Providers...),
	}
	if ws.Default != "" {
		merged.Default = ws.Default
	}
	byName := make(map[string]int, len(merged.Providers))
	for i, p := range merged.Providers {
		byName[p.Name] = i
	}
	for _, wp := range ws.Providers {
		if i, ok := byName[wp.Name]; ok {
			merged.Providers[i] = wp
		} else {
			merged.Providers = append(merged.Providers, wp)
			byName[wp.Name] = len(merged.Providers) - 1
		}
	}
	return merged
}

// ResolveConfig 解析最终配置：全局（HUSHFUSION_AI_CONFIG 或
// ~/.config/hushfusion/ai.json，可选）→ 工作区（<workspaceDir>/.hushfusion/ai.json，可选）
// → 合并 → 应用 env 覆盖。任一配置文件缺失都跳过（不报错），最终至少回退到内置。
func ResolveConfig(workspaceDir string) (*Config, error) {
	global := &Config{ConfigVersion: CurrentConfigVersion}
	if p := globalConfigPath(); p != "" {
		if cfg, err := LoadConfig(p); err == nil {
			global = cfg
		}
	}
	var ws *Config
	if workspaceDir != "" {
		wp := filepath.Join(workspaceDir, ".hushfusion", "ai.json")
		if cfg, err := LoadConfig(wp); err == nil {
			ws = cfg
		}
	}
	merged := MergeWorkspace(global, ws)
	ApplyEnvOverrides(merged)
	return merged, nil
}

func globalConfigPath() string {
	if p := os.Getenv("HUSHFUSION_AI_CONFIG"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "hushfusion", "ai.json")
}

// RegisterConfig 把配置里的 provider 全部注册进注册表（注册前各 Validate）。
// 这是「配置 → 运行时」的桥；调用方应先 ResolveConfig 拿到合并后配置。
func RegisterConfig(cfg *Config) error {
	for _, p := range cfg.Providers {
		if err := Register(p); err != nil {
			return fmt.Errorf("ai: 注册 provider %q 失败: %w", p.Name, err)
		}
	}
	return nil
}
