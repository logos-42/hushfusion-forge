package main

// longterm_memory.go —— 跨会话长期记忆：本地向量库（无外部依赖）。
//
// 设计取舍：
//   - 存储：全局 ~/.forge/memory/longterm.json（追加式记录，最多 2000 条，原子写）。
//     不引入 PostgreSQL/OrbitDB 这类需要服务端 / JS 运行时的依赖，保证
//     `forge os tui` 开箱即用；读写集中在 loadMemRecords/saveMemRecords 两处，
//     以后要换 Postgres/OrbitDB 只需替换这两个函数。
//   - 召回：确定性 hashed bag-of-tokens 向量（256 维，CJK bigram + 英文词），
//     cosine 相似度 + 关键词命中加权。不需要 embedding API，离线可用；
//     代价是语义近似（不是真正的语义向量）——诚实标注。
//
// 跨会话：记录只按时间/内容索引，不按 session 过滤，因此新会话能召回旧会话经验。

import (
	"encoding/json"
	"hash/fnv"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	memVecDim  = 256
	memMaxRecs = 2000
)

type memRecord struct {
	Time    string    `json:"time"`
	Session string    `json:"session"`
	Goal    string    `json:"goal"`
	Text    string    `json:"text"`
	Vector  []float32 `json:"vector"`
}

func memFilePath(base string) string { return filepath.Join(runtimeMemoryDir(base), "longterm.json") }

// memTokens 切词：CJK 用 2-gram，英文/数字用 ≥2 字符的词。
func memTokens(s string) []string {
	var out []string
	var cjk []rune
	flush := func() {
		for i := 0; i+1 < len(cjk); i++ {
			out = append(out, string(cjk[i:i+2]))
		}
		cjk = nil
	}
	for _, r := range s {
		if r >= 0x4E00 && r <= 0x9FFF {
			cjk = append(cjk, r)
		} else {
			flush()
		}
	}
	flush()
	fields := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	})
	for _, f := range fields {
		if len(f) >= 2 {
			out = append(out, f)
		}
	}
	return out
}

// embedText 生成确定性向量（hashed bag-of-tokens，L2 归一化）。
func embedText(s string) []float32 {
	v := make([]float32, memVecDim)
	for _, tok := range memTokens(s) {
		h := fnv.New32a()
		_, _ = h.Write([]byte(tok))
		v[h.Sum32()%memVecDim] += 1
	}
	var norm float32
	for _, x := range v {
		norm += x * x
	}
	if norm > 0 {
		n := float32(math.Sqrt(float64(norm)))
		for i := range v {
			v[i] /= n
		}
	}
	return v
}

func cosine(a, b []float32) float32 {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	var dot float32
	for i := 0; i < n; i++ {
		dot += a[i] * b[i]
	}
	return dot
}

func loadMemRecords(base string) []memRecord {
	b, err := os.ReadFile(memFilePath(base))
	if err != nil {
		return nil
	}
	var recs []memRecord
	if json.Unmarshal(b, &recs) != nil {
		return nil // 损坏 → 当空库，不阻塞
	}
	return recs
}

func saveMemRecords(base string, recs []memRecord) error {
	if err := os.MkdirAll(filepath.Dir(memFilePath(base)), 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(recs)
	if err != nil {
		return err
	}
	tmp := memFilePath(base) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, memFilePath(base))
}

// addMemRecord 追加一条长期记忆（目标 + 结论），超过上限丢最旧。
func addMemRecord(base, session, goal, text string) {
	goal = strings.TrimSpace(goal)
	text = strings.TrimSpace(text)
	if goal == "" && text == "" {
		return
	}
	recs := loadMemRecords(base)
	recs = append(recs, memRecord{
		Time:    time.Now().Format("2006-01-02 15:04"),
		Session: session,
		Goal:    clipSkillText(goal, 200),
		Text:    clipSkillText(text, 2000),
		Vector:  embedText(goal + "\n" + text),
	})
	if len(recs) > memMaxRecs {
		recs = recs[len(recs)-memMaxRecs:]
	}
	_ = saveMemRecords(base, recs)
}

// recallMemories 向量召回 topK（cosine + 关键词命中加权），跨会话。
func recallMemories(base, query string, k int) []memRecord {
	if strings.TrimSpace(query) == "" || k <= 0 {
		return nil
	}
	recs := loadMemRecords(base)
	if len(recs) == 0 {
		return nil
	}
	qv := embedText(query)
	qTokens := memTokens(query)
	type scored struct {
		r memRecord
		s float32
	}
	var cands []scored
	for _, r := range recs {
		s := cosine(qv, r.Vector)
		hits := 0
		hay := strings.ToLower(r.Goal + "\n" + r.Text)
		for _, t := range qTokens {
			if len(t) >= 2 && strings.Contains(hay, strings.ToLower(t)) {
				hits++
			}
		}
		if len(qTokens) > 0 {
			s += float32(hits) / float32(len(qTokens)) * 0.5
		}
		if s > 0.05 {
			cands = append(cands, scored{r, s})
		}
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].s > cands[j].s })
	if len(cands) > k {
		cands = cands[:k]
	}
	out := make([]memRecord, 0, len(cands))
	for _, c := range cands {
		out = append(out, c.r)
	}
	return out
}

func formatRecalledMemories(recs []memRecord) string {
	var b strings.Builder
	for _, r := range recs {
		b.WriteString("- [" + r.Time + "] " + clipSkillText(r.Goal, 80) + " → " + clipSkillText(r.Text, 240) + "\n")
	}
	return b.String()
}
