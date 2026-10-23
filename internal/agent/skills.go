package agent

// skills.go —— 技能发现 / 匹配 / 注入（学 Claude Code SkillTool：低开销、仅相关时注入）。
//
// 设计契约（docs/wiki/agent-runtime-design.md §5）：
//   - 目录合并：HUSHFUSION_SKILLS_DIR（缺省 repo_root/skills/）+ ~/.hermes/skills，
//     同名后者覆盖（学 hermes skills 分层）；
//   - SKILL.md 格式：frontmatter 必填 name / description，正文自由；
//   - 匹配 v0.1 关键词（SkillMatcher 是接口，预留 embedding 槽）；
//   - 注入预算：每个 skill 正文截断到 SkillInjectionBudget 进 system，完整正文经
//     skill.view 工具按需拉取。
//
// 诚实边界：关键词匹配是近似不是语义；注入是截断不是摘要（见设计文档 §8）。

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Skill 是索引后的单个技能。
type Skill struct {
	Name        string // frontmatter name
	Description string // frontmatter description（匹配与注入都用它）
	Path        string // SKILL.md 绝对路径
	Body        string // 完整正文（不含 frontmatter）
	Dir         string // 技能目录（含辅助文件）
}

// Frontmatter 是 SKILL.md 的 YAML 头（只解析 name / description 两个键，其余忽略）。
type Frontmatter struct {
	Name        string
	Description string
}

// parseFrontmatter 解析 SKILL.md 开头的 --- 块。解析失败返回零值（不 panic）：
// 该文件跳过索引 —— 诚实，不把坏文件当技能。
func parseFrontmatter(content string) Frontmatter {
	var fm Frontmatter
	lines := strings.Split(content, "\n")
	// 第一行必须是 ---
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return fm
	}
	for _, ln := range lines[1:] {
		t := strings.TrimSpace(ln)
		if t == "---" {
			break
		}
		if strings.HasPrefix(t, "name:") {
			fm.Name = strings.TrimSpace(strings.TrimPrefix(t, "name:"))
		} else if strings.HasPrefix(t, "description:") {
			fm.Description = strings.TrimSpace(strings.TrimPrefix(t, "description:"))
		}
	}
	return fm
}

// SkillMatcher 决定哪些技能注入（v0.1 关键词；预留 embedding 实现）。
type SkillMatcher interface {
	// Match 返回 user 消息命中的技能，按相关度降序。
	Match(msg string, skills []Skill, topK int) []Skill
}

// KeywordMatcher 是 v0.1 的关键词匹配：把 user 消息与每个 skill 的 description
// 做词频交集，命中 ≥1 个显著词即候选，按命中数降序取 topK。
//
// 显著词 = 长度 ≥3 的中/英文单词（过滤停用词表）。
type KeywordMatcher struct {
	StopWords map[string]bool
}

// NewKeywordMatcher 构造默认关键词匹配器。
func NewKeywordMatcher() *KeywordMatcher {
	return &KeywordMatcher{StopWords: defaultStopWords()}
}

var defaultStopWords = func() map[string]bool {
	w := map[string]bool{"the": true, "a": true, "an": true, "of": true, "to": true,
		"and": true, "or": true, "for": true, "in": true, "on": true, "with": true,
		"is": true, "are": true, "was": true, "be": true, "this": true, "that": true,
		"from": true, "by": true, "多": true, "的": true, "了": true, "是": true,
		"在": true, "和": true, "与": true, "一": true, "不": true, "会": true, "要": true,
		"把": true, "被": true, "就": true, "都": true, "也": true, "很": true}
	return w
}

// tokens 把文本切成候选词（保留 CJK 单词与 ≥3 字母英文词）。
// CJK 用 2-gram（bigram）：整段连续中文作为一个 token 无法与 description 匹配，
// bigram 让"长技能截断"命中 description 里的"技能/截断"。
func (k *KeywordMatcher) tokens(s string) map[string]int {
	out := map[string]int{}
	// CJK：每段连续中文再切成 2-gram。
	for _, seg := range cjkSegments(s) {
		runes := []rune(seg)
		if len(runes) == 1 {
			if !k.StopWords[seg] {
				out[seg]++
			}
			continue
		}
		for i := 0; i+1 < len(runes); i++ {
			g := string(runes[i : i+2])
			if !k.StopWords[g] {
				out[g]++
			}
		}
	}
	// 再提取英文/数字词。
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9')
	})
	for _, f := range fields {
		f = strings.ToLower(f)
		if len(f) >= 3 && !k.StopWords[f] {
			out[f]++
		}
	}
	return out
}

// cjkSegments 提取连续 CJK 字符段（"杭州天气" 一整段，不分字）。
func cjkSegments(s string) []string {
	var segs []string
	var cur strings.Builder
	for _, r := range s {
		if r >= 0x4E00 && r <= 0x9FFF {
			cur.WriteRune(r)
		} else {
			if cur.Len() > 0 {
				segs = append(segs, cur.String())
				cur.Reset()
			}
		}
	}
	if cur.Len() > 0 {
		segs = append(segs, cur.String())
	}
	return segs
}

// Match 实现 SkillMatcher。
func (k *KeywordMatcher) Match(msg string, skills []Skill, topK int) []Skill {
	if topK <= 0 {
		topK = 3
	}
	msgTokens := k.tokens(msg)
	type scored struct {
		s    Skill
		hits int
	}
	var cands []scored
	for _, sk := range skills {
		hits := 0
		for tok := range msgTokens {
			if k.StopWords[tok] {
				continue
			}
			if strings.Contains(strings.ToLower(sk.Description), strings.ToLower(tok)) ||
				strings.Contains(strings.ToLower(sk.Name), strings.ToLower(tok)) {
				hits++
			}
		}
		if hits > 0 {
			cands = append(cands, scored{sk, hits})
		}
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].hits > cands[j].hits })
	out := make([]Skill, 0, topK)
	for i := 0; i < len(cands) && i < topK; i++ {
		out = append(out, cands[i].s)
	}
	return out
}

// SkillsDir 是技能索引（目录 → 技能）。
type SkillsDir struct {
	dirs    []string
	skills  []Skill
	matcher SkillMatcher
	// SkillInjectionBudget 是单技能注入 system 的字符上限。
	SkillInjectionBudget int
}

// NewSkills 构造技能索引；dirs 是（按优先级）技能目录列表，同名后者覆盖。
// 目录不存在不报错（跳过 — 技能可选的，不是硬依赖）。
func NewSkills(dirs []string, matcher SkillMatcher) *SkillsDir {
	if matcher == nil {
		matcher = NewKeywordMatcher()
	}
	s := &SkillsDir{dirs: dirs, matcher: matcher, SkillInjectionBudget: 800}
	s.scan()
	return s
}

// scan 扫描所有目录并合并（同名后者覆盖 — 后目录优先）。
func (s *SkillsDir) scan() {
	byName := map[string]Skill{}
	order := []string{}
	for _, dir := range s.dirs {
		if dir == "" {
			continue
		}
		filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return nil // 目录不可读 → 跳过（技能可选）
			}
			if d.IsDir() {
				return nil
			}
			if d.Name() != "SKILL.md" {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return nil
			}
			content := string(b)
			fm := parseFrontmatter(content)
			if fm.Name == "" {
				return nil // frontmatter 坏了 → 不索引（诚实）
			}
			body := content
			if idx := strings.Index(content, "---\n"); idx >= 0 {
				// 去掉第一段 frontmatter（--- ... ---）
				rest := content[idx+4:]
				if end := strings.Index(rest, "---\n"); end >= 0 {
					body = rest[end+4:]
				}
			}
			sk := Skill{
				Name:        fm.Name,
				Description: fm.Description,
				Path:        path,
				Body:        body,
				Dir:         filepath.Dir(path),
			}
			if _, ok := byName[sk.Name]; !ok {
				order = append(order, sk.Name)
			}
			byName[sk.Name] = sk // 后面的目录覆盖前面的
			return nil
		})
	}
	s.skills = make([]Skill, 0, len(byName))
	for _, n := range order {
		s.skills = append(s.skills, byName[n])
	}
}

// All 返回全部已索引技能。
func (s *SkillsDir) All() []Skill { return append([]Skill(nil), s.skills...) }

// Lookup 按名字取技能。
func (s *SkillsDir) Lookup(name string) (Skill, bool) {
	for _, sk := range s.skills {
		if sk.Name == name {
			return sk, true
		}
	}
	return Skill{}, false
}

// Match 按当前 matcher 返回命中技能（topK 内）。
func (s *SkillsDir) Match(msg string, topK int) []Skill {
	return s.matcher.Match(msg, s.skills, topK)
}

// Inject 把命中技能渲染进 system 提示（追加在末尾；截断到预算，诚实标注）。
// 返回空串 = 无命中。
func (s *SkillsDir) Inject(msg, system string) string {
	hits := s.Match(msg, 3)
	if len(hits) == 0 {
		return system
	}
	var b strings.Builder
	b.WriteString(system)
	if system != "" && !strings.HasSuffix(system, "\n") {
		b.WriteString("\n")
	}
	b.WriteString("\n—— 相关技能（按需取用；完整正文经 skill.view 工具）——\n")
	for _, sk := range hits {
		body := strings.TrimSpace(sk.Body)
		if s.SkillInjectionBudget > 0 && len(body) > s.SkillInjectionBudget {
			body = body[:s.SkillInjectionBudget] + " …(截断，可用 skill.view 看全文)"
		}
		b.WriteString("\n<skill name=\"" + sk.Name + "\">\n" + body + "\n</skill>\n")
	}
	return b.String()
}

// ErrSkillNotFound 是 skill.view 工具查无此技能的哨兵错误。
var ErrSkillNotFound = errors.New("agent: 技能不存在")
