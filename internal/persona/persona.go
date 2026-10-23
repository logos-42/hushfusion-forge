// Package persona 负责"这个 agent 是谁、按什么规矩办事"的文档层：
// 发现（discover）→ 定序（priority）→ 合成（compose，受预算截断）→ 注入（bootstrap）。
//
// 学 Hermes 的 AGENTS.md / SOUL.md 注入，但按本仓纪律：
//
//   - 文档是普通文件，可 git diff、可人读、无数据库。
//   - 优先级小的更权威：预算不够时先保高优先级文档（ persona > agents > rules ）。
//   - 截断必须留痕：被截的文档在块内标注 "[…截断]"，不静默吞内容。
//   - 空目录不是错误：没有文档就没有 bootstrap（返回空串），调用方照常跑。
//   - 目录/超大字/二进制都不当成文档（fail-safe 跳过）。
package persona

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Kind 是文档类型。
type Kind string

const (
	KindPersona Kind = "persona" // SOUL.md：这个 agent 是谁
	KindAgents  Kind = "agents"  // AGENTS.md：协作规约
	KindRules   Kind = "rules"   // .cursorrules / CLAUDE.md：编码/行为规则
	KindMemory  Kind = "memory"  // MEMORY.md：长期事实
)

// Doc 是一份被发现的文档。
type Doc struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Kind     Kind   `json:"kind"`
	Priority int    `json:"priority"` // 小 = 更权威
	Content  string `json:"content"`
	Bytes    int    `json:"bytes"`
	// Truncated 标记合成时被截断。
	Truncated bool `json:"truncated,omitempty"`
}

// candidates 是仓内公认文档名 → (Kind, Priority)。
// 顺序即优先级：persona 最权威，rules 次之，memory 最后。
var candidates = []struct {
	File     string
	Kind     Kind
	Priority int
}{
	{"SOUL.md", KindPersona, 10},
	{"AGENTS.md", KindAgents, 20},
	{"CLAUDE.md", KindRules, 30},
	{".cursorrules", KindRules, 40},
	{"MEMORY.md", KindMemory, 50},
}

// maxDocBytes 是单份文档的读取上限（防误读巨型文件）；超出即截断并留痕。
const maxDocBytes = 64 * 1024

// Discover 在 root 下发现公认文档（按优先级升序返回）。
func Discover(root string) ([]Doc, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("persona: root 不能为空")
	}
	var out []Doc
	for _, c := range candidates {
		p := filepath.Join(root, c.File)
		st, err := os.Stat(p)
		if err != nil || st.IsDir() {
			continue // 不存在或是目录：跳过（不是错误）
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			continue // 读不了：跳过而非 brick
		}
		d := Doc{
			Name: c.File, Path: p, Kind: c.Kind, Priority: c.Priority,
			Content: string(raw), Bytes: len(raw),
		}
		if d.Bytes > maxDocBytes {
			d.Content = string(raw[:maxDocBytes])
			d.Truncated = true
		}
		out = append(out, d)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Priority < out[j].Priority })
	return out, nil
}

// Compose 把文档合成一个 bootstrap 块（受 budget 字符约束；预算不够按优先级丢后面）。
func Compose(docs []Doc, budget int) string {
	if len(docs) == 0 {
		return ""
	}
	if budget <= 0 {
		budget = 8000 // 默认预算
	}
	sorted := append([]Doc{}, docs...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Priority < sorted[j].Priority })

	var b strings.Builder
	used := 0 // 已用字符数（rune，CJK 友好——预算语义是"字符"不是"字节"）
	for _, d := range sorted {
		header := fmt.Sprintf("\n## %s（%s）\n", d.Name, d.Kind)
		body := strings.TrimSpace(d.Content)
		footer := ""
		if d.Truncated {
			footer = fmt.Sprintf("\n[…%s 已在读取时截断，原 %d 字节…]", d.Name, d.Bytes)
		}
		block := header + body + footer + "\n"
		if used+runeLen(block) > budget {
			// 预算不够：高优先级已进，低优先级整份丢弃并留痕
			note := fmt.Sprintf("\n[…预算不足，%s（%s）未注入…]", d.Name, d.Kind)
			if used+runeLen(note) <= budget {
				b.WriteString(note)
			}
			break
		}
		b.WriteString(block)
		used += runeLen(block)
	}
	if b.Len() == 0 {
		return ""
	}
	return "# Agent Bootstrap\n" + b.String()
}

// Bootstrap = Discover + Compose（一步到位的注入块）。
func Bootstrap(root string, budget int) (string, error) {
	docs, err := Discover(root)
	if err != nil {
		return "", err
	}
	return Compose(docs, budget), nil
}

// runeLen 返回字符数（CJK 一字计一，与预算语义一致）。
func runeLen(s string) int { return len([]rune(s)) }

// WithExtra 在 bootstrap 块后追加一段运行时人格描述（如 /persona 设定的）。
func WithExtra(block, extra string) string {
	extra = strings.TrimSpace(extra)
	if extra == "" {
		return block
	}
	if block == "" {
		return "# Agent Bootstrap\n\n## persona（运行时）\n" + extra + "\n"
	}
	return block + "\n## persona（运行时）\n" + extra + "\n"
}
