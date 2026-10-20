package harness

import (
	"fmt"
	"sort"
	"strings"
)

// EpisodeResult 是一条 episode（一次实验）的聚合视图，也是一条"模型设计"候选：
// 它的动作序列就是一个可复现的设计方案。
type EpisodeResult struct {
	Phase      int     `json:"phase"` // 自优化相位
	Episode    int     `json:"episode"`
	Seed       int     `json:"seed"`
	Actions    []int   `json:"actions"`
	FinalScore float64 `json:"final_score"`
	Terminated bool    `json:"terminated"`
	Steps      int     `json:"steps"`
}

// Verdict 是 hermes 式的结论：给每个设计（episode）下一个可比的标量结论，
// 而不是只堆原始分数。对齐 hermes harness 的 verdict 矩阵（consumes-all-turns /
// in-loop-only / ...）。这里按"相对天花板"的完成度分级。
func (e EpisodeResult) Verdict(ceiling float64) string {
	if e.Steps == 0 {
		return "no-data"
	}
	if e.Terminated || e.FinalScore >= ceiling*0.999 {
		return "命中" // 达到目标
	}
	if e.FinalScore >= ceiling*0.9 {
		return "接近"
	}
	if e.FinalScore >= ceiling*0.5 {
		return "中性"
	}
	return "未达标"
}

// PhaseSummary 是一条自优化相位的聚合（对齐 hermes 的"基线/对照相位"概念：
// 每个改进相位都要有对照基线，否则对比不公平）。
type PhaseSummary struct {
	Phase     int     `json:"phase"`
	Policy    string  `json:"policy"`
	NEpisodes int     `json:"n_episodes"`
	MeanScore float64 `json:"mean_score"`
	HitRate   float64 `json:"hit_rate"` // 命中率 = 该相位 Terminated 占比
}

// PhaseGrid 是"动作 × 相位"的平均Δ矩阵（对齐 hermes 的 positions×variants 实验矩阵）。
type PhaseGrid struct {
	Phases []int       `json:"phases"`
	Policy []string    `json:"policy"` // 每相位对应的策略名
	Matrix [][]float64 `json:"matrix"` // [phase][action] 平均Δ
}

// Summary 是一轮 Run（或一个 harness 实例）的聚合摘要。
type Summary struct {
	NEpisodes         int              `json:"n_episodes"`
	NSteps            int              `json:"n_steps"`
	StoreLen          int              `json:"store_len"`
	BestScore         float64          `json:"best_score"`
	MeanFinalScore    float64          `json:"mean_final_score"`
	TerminatedCount   int              `json:"terminated_count"`
	Recovered         int              `json:"recovered_count"` // 错误恢复标记的行数
	Ceiling           float64          `json:"ceiling"` // 命中阈值（= mu.TargetScore）
	ActionDim         int              `json:"action_dim"`
	ActionKeys        []string         `json:"action_keys"`
	PerActionMeanDelta []float64       `json:"per_action_mean_delta"`
	Policy            string           `json:"policy"`
	StorePath         string           `json:"store_path"`
	BestDesigns       []EpisodeResult  `json:"best_designs"`
	PhaseSummaries    []PhaseSummary   `json:"phase_summaries"`
	PhaseGrid         PhaseGrid        `json:"phase_grid"`
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
	ceil := s.Ceiling
	if ceil <= 0 {
		ceil = 1 // 退化保护：无天花板时不让 verdict 除零
	}
	fmt.Fprintf(&b, "# HUSHFUSION harness 本地自优化闭环 — 摘要\n\n")
	fmt.Fprintf(&b, "- 策略: `%s`\n", s.Policy)
	fmt.Fprintf(&b, "- episode 数: %d\n", s.NEpisodes)
	fmt.Fprintf(&b, "- 评估步数（落盘数据点）: %d\n", s.NSteps)
	fmt.Fprintf(&b, "- store 累计: %d\n", s.StoreLen)
	fmt.Fprintf(&b, "- 命中的问题（达到目标）: %d (%.1f%%)\n", s.TerminatedCount, pct(s.TerminatedCount, s.NEpisodes))
	if s.Recovered > 0 {
		fmt.Fprintf(&b, "- 错误恢复: %d 步经 panic/落盘失败恢复（闭环未中断）\n", s.Recovered)
	}
	fmt.Fprintf(&b, "- 平均分: %.4f\n", s.MeanFinalScore)
	fmt.Fprintf(&b, "- 最高分: %.4f (天花板=%.4f)\n", s.BestScore, ceil)

	// 各相位摘要（hermes 式基线/对照相位）。
	if len(s.PhaseSummaries) > 0 {
		b.WriteString("\n## 各相位摘要（基线对照）\n\n")
		b.WriteString("| 相位 | 策略 | episode | 平均分 | 命中率 |\n|---|------|---------|--------|--------|\n")
		for _, p := range s.PhaseSummaries {
			fmt.Fprintf(&b, "| P%d | `%s` | %d | %.4f | %.1f%% |\n",
				p.Phase, p.Policy, p.NEpisodes, p.MeanScore, pct(int(p.HitRate*100+0.5), 100))
		}
	}

	// 动作 × 相位 平均Δ 矩阵（hermes 式实验矩阵）。
	if len(s.PhaseGrid.Phases) > 0 && len(s.PhaseGrid.Matrix) > 0 {
		b.WriteString("\n## 动作 × 相位 平均Δ 矩阵（自调整信号）\n\n")
		b.WriteString("| 动作 |")
		for i, ph := range s.PhaseGrid.Phases {
			fmt.Fprintf(&b, " P%d(`%s`) |", ph, s.PhaseGrid.Policy[i])
		}
		b.WriteString("\n|---|")
		for range s.PhaseGrid.Phases {
			b.WriteString("---|")
		}
		b.WriteString("\n")
		for k := 0; k < s.ActionDim && k < len(s.PhaseGrid.Matrix[0]); k++ {
			fmt.Fprintf(&b, "| `%s` |", actionKey(s, k))
			for _, row := range s.PhaseGrid.Matrix {
				fmt.Fprintf(&b, " %+.4f |", row[k])
			}
			b.WriteString("\n")
		}
	}

	// 全局动作平均Δ（规则层自调整的总信号）。
	b.WriteString("\n## 动作平均 score 增量（规则层自调整信号）\n\n")
	b.WriteString("| # | 动作 | 平均Δ |\n|---|------|-------|\n")
	for k, d := range s.PerActionMeanDelta {
		fmt.Fprintf(&b, "| %d | `%s` | %+.4f |\n", k, actionKey(s, k), d)
	}

	// Top 模型设计 + hermes 式 verdict 结论。
	b.WriteString("\n## Top 模型设计（按最终分）+ verdict\n\n")
	if len(s.BestDesigns) == 0 {
		b.WriteString("_暂无 episode 数据。_\n")
	}
	for i, d := range s.BestDesigns {
		keys := make([]string, 0, len(d.Actions))
		for _, a := range d.Actions {
			keys = append(keys, actionKey(s, a))
		}
		fmt.Fprintf(&b, "%d. P%d ep%d seed%d — score=%.4f 命中=%v 步=%d — **verdict: %s**\n",
			i+1, d.Phase, d.Episode, d.Seed, d.FinalScore, d.Terminated, d.Steps, d.Verdict(ceil))
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
