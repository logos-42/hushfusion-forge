// Package envload 负责加载 .env 并保护环境变量。
//
// 纪律：
//   - 只加载 workspace 内的 .env（默认 <root>/.env 与 <root>/os/.env），
//     解析 symlink 后仍必须位于 root 内，拒绝路径穿越；
//   - 绝不覆盖已存在的环境变量（真实 shell/env 优先）；
//   - 秘密值永不打印：Status 只返回 key + masked 值；
//   - 文件过大/行数过多/键名非法 → 明确报错，不静默吞掉。
package envload

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	maxFileBytes = 64 * 1024
	maxLines     = 500
)

var keyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Entry 是一条已加载/已存在的环境变量状态（值一律脱敏）。
type Entry struct {
	Key    string `json:"key"`
	State  string `json:"state"` // loaded | exists | skipped
	Masked string `json:"masked,omitempty"`
	Source string `json:"source,omitempty"`
}

// Result 是加载结果。
type Result struct {
	Root    string   `json:"root"`
	Files   []string `json:"files"`
	Entries []Entry  `json:"entries"`
}

// Load 从 root 下发现 .env 并加载（不覆盖已有 env）。
func Load(root string) (*Result, error) {
	res := &Result{Root: root}
	seen := map[string]bool{}
	for _, rel := range []string{".env", filepath.Join("os", ".env")} {
		p := filepath.Join(root, rel)
		real, err := filepath.EvalSymlinks(p)
		if err != nil {
			continue // 没有这个文件是正常情况
		}
		rootReal, rerr := filepath.EvalSymlinks(root)
		if rerr != nil {
			rootReal = root
		}
		if !strings.HasPrefix(real, rootReal+string(filepath.Separator)) && real != rootReal {
			return nil, fmt.Errorf("envload: %s 解析后越出 workspace", rel)
		}
		info, err := os.Stat(real)
		if err != nil || info.IsDir() {
			continue
		}
		if info.Size() > maxFileBytes {
			return nil, fmt.Errorf("envload: %s 超过 %d 字节", rel, maxFileBytes)
		}
		f, err := os.Open(real)
		if err != nil {
			return nil, err
		}
		src := filepath.ToSlash(rel)
		res.Files = append(res.Files, src)
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 4096), 8192)
		n := 0
		for sc.Scan() && n < maxLines {
			n++
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			line = strings.TrimPrefix(line, "export ")
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				return nil, fmt.Errorf("envload: %s 第 %d 行不是 KEY=VALUE", src, n)
			}
			k = strings.TrimSpace(k)
			if !keyRe.MatchString(k) {
				return nil, fmt.Errorf("envload: %s 第 %d 行键名非法: %q", src, n, k)
			}
			v = strings.Trim(strings.TrimSpace(v), `"'`)
			if seen[k] {
				continue
			}
			seen[k] = true
			e := Entry{Key: k, Source: src}
			if old, ok := os.LookupEnv(k); ok {
				e.State = "exists"
				if old != "" {
					e.Masked = mask(k)
				}
			} else {
				if err := os.Setenv(k, v); err != nil {
					_ = f.Close()
					return nil, fmt.Errorf("envload: 设置 %s 失败: %w", k, err)
				}
				e.State = "loaded"
				e.Masked = mask(k)
			}
			res.Entries = append(res.Entries, e)
		}
		if err := sc.Err(); err != nil {
			_ = f.Close()
			return nil, err
		}
		_ = f.Close()
	}
	sort.Slice(res.Entries, func(i, j int) bool { return res.Entries[i].Key < res.Entries[j].Key })
	return res, nil
}

// Mask 返回脱敏值：秘密一律 "***"，非秘密给长度标识，不明文返回。
func Mask(key, value string) string {
	if isSecret(key) || value == "" {
		return "***"
	}
	return fmt.Sprintf("<%d chars>", len(value))
}

func mask(key string) string { return Mask(key, "x") }

func isSecret(key string) bool {
	k := strings.ToUpper(key)
	for _, s := range []string{"KEY", "SECRET", "TOKEN", "PASSWORD", "PASS", "CREDENTIAL", "AUTH"} {
		if strings.Contains(k, s) {
			return true
		}
	}
	return false
}
