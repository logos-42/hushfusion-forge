package harness

import (
	"fmt"
	"sort"
	"strings"
)

// EpisodeResult 是一条 episode（一次实验）的聚合视图，也是一条"模型设计"候选：
// 它的动作序列就是一个可复现的设计方案。
type EpisodeResult struct {
	Episode    int     `json:"episode"`
	Seed       int     `json:"seed"`
	Actions    []int   `json:"actions"`
	FinalScore float64 `json:"final_score"`
	Terminated bool    `json:"terminated"`
	Steps      int     `json:"steps"`
}

// Summary 是一轮 Run（或一个 harness 实例）的聚合摘要。
type Summary struct {
	NEpisodes         int       `json:"n_episodes"`
	NSteps            int       `json:"n_steps"`
	StoreLen          int       `json:"store_len"`
	BestScore         float64   `json:"best_score"`
	MeanFinalScore    float64   `json:"mean_final_score"`
	TerminatedCount   int       `json:"terminated_count"`
	ActionDim         int       `json:"action_dim"`
	ActionKeys        []string  `json:"action_keys"`
	PerActionMeanDelta []float64 `json:"per_action_mean_delta"`
	Policy            string    `json:"policy"`
	StorePath         string    `json:"store_path"`
	BestDesigns       []EpisodeResult `json:"best_designs"`
}

// topDesigns 按 final score 取前 k 条 episode（模型设计）。
func topDesigns(eps []EpisodeResult, k int) []EpisodeResult {
	cp := append([]EpisodeResult(nil), eps...)
	sort.SliceStable(cp, func(i, j int) bool {
		if cp[i].FinalScore != cp[j].FinalScore {
			return cp[i].FinalScore > cp[j].FinalScore
		}
		return cp[i].Episode < cp[j].Episode
	})
	if len(cp) > k {
		cp = cp[:k]
	}
	return cp
}

// Render 把摘要渲染成 Markdown 报告。
func (s Summary) Render() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# HUSHFUSION harness 本地自优化闭环 — 摘要\n\n")
	fmt.Fprintf(&b, "- 策略: `%s`\n", s.Policy)
	fmt.Fprintf(&b, "- episode 数: %d\n", s.NEpisodes)
	fmt.Fprintf(&b, "- 评估步数（落盘数据点）: %d\n", s.NSteps)
	fmt.Fprintf(&b, "- store 累计: %d\n", s.StoreLen)
	fmt.Fprintf(&b, "- 命中的问题（达到目标）: %d (%.1f%%)\n", s.TerminatedCount, pct(s.TerminatedCount, s.NEpisodes))
	fmt.Fprintf(&b, "- 平均分: %.4f\n", s.MeanFinalScore)
	fmt.Fprintf(&b, "- 最高分: %.4f\n", s.BestScore)

	b.WriteString("\n## 动作平均 score 增量（规则层自调整信号）\n\n")
	b.WriteString("| # | 动作 | 平均Δ |\n|---|------|-------|\n")
	for k, d := range s.PerActionMeanDelta {
		fmt.Fprintf(&b, "| %d | `%s` | %+.4f |\n", k, actionKey(s, k), d)
	}

	b.WriteString("\n## Top 模型设计（按最终分）\n\n")
	if len(s.BestDesigns) == 0 {
		b.WriteString("_暂无 episode 数据。_\n")
	}
	for i, d := range s.BestDesigns {
		keys := make([]string, 0, len(d.Actions))
		for _, a := range d.Actions {
			keys = append(keys, actionKey(s, a))
		}
		fmt.Fprintf(&b, "%d. ep%d seed%d — score=%.4f 命中=%v 步=%d\n", i+1, d.Episode, d.Seed, d.FinalScore, d.Terminated, d.Steps)
		fmt.Fprintf(&b, "   动作: %s\n", strings.Join(keys, " → "))
	}
	return b.String()
}

// actionKey 在 PerActionMeanDelta 索引与动作键之间做翻译（需 ActionDim 对齐）。
func actionKey(s Summary, k int) string {
	if k < 0 || k >= len(s.ActionKeys) {
		return fmt.Sprintf("a%d", k)
	}
	return s.ActionKeys[k]
}

func pct(n, total int) float64 {
	if total == 0 {
		return 0
	}
	return 100 * float64(n) / float64(total)
}
