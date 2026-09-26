package report

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/logos-42/hushfusion-forge/internal/config"
	"github.com/logos-42/hushfusion-forge/internal/experiment"
	"github.com/logos-42/hushfusion-forge/internal/knowledge"
	"github.com/logos-42/hushfusion-forge/internal/physics"
	"github.com/logos-42/hushfusion-forge/internal/registry"
	"github.com/logos-42/hushfusion-forge/internal/search"
)

// frozenSections 是冻结接口所钉住的那些小节标题，顺序与 api.go
// 中声明的顺序一致。
var frozenSections = []string{
	"## 1. 设置",
	"## 2. 人工基线 vs 机器最优",
	"## 3. 方法对比 (等预算)",
	"## 4. 设计谱系",
	"## 5. 知识库 rules",
	"## 6. 诚实边界",
	"## 7. 下一步 (Phase 1)",
}

func sampleRules() []knowledge.Rule {
	return []knowledge.Rule{
		{
			RuleID: "R001", Parameter: "z_3", Term: "mirror", Rho: 0.62, SignAgreement: 1.0,
			NDesigns: 3000, NRuns: 12, DecileLow: 2.1, DecileHigh: 3.4,
			Statement:   "参数 z_3 越大,term mirror 越大:…(ρ=0.620,同号 run 占比 100%)。",
			StatementEN: "mirror rises with z_3: …",
			Scope:       "search box: 4 coils …; vacuum analytic …",
		},
		{
			RuleID: "R002", Parameter: "I_0", Term: "cost", Rho: -0.41, SignAgreement: 1.0,
			NDesigns: 3000, NRuns: 12, DecileLow: 1.4, DecileHigh: 0.8,
			Statement:   "参数 I_0 越大,term cost 越小:…",
			StatementEN: "cost falls with I_0: …",
			Scope:       "search box: 4 coils …",
		},
	}
}

// sampleReport 是一个手工构造、填充完整的 Report 字面量，这样渲染器
// 就能独立于 benchmark 工具链被检验。
func sampleReport() *experiment.Report {
	return &experiment.Report{
		Meta: experiment.Meta{
			Tag: "phase0-test", Timestamp: "2026-01-02T03:04:05Z", Budget: 1000,
			Seeds: []int{0, 1, 2}, Methods: []string{"random", "lhs", "evolution", "evolution_warm"},
			GitCommit: "0123456789abcdef0123456789abcdef01234567",
			Platform:  "darwin/amd64", GoVersion: "go1.27.1", Workers: 1,
		},
		Spec:    config.DefaultSpec().AsMap(),
		Solver:  "analytic",
		CostRef: 1.791703035e12,
		Baseline: experiment.BaseRec{
			Name: "textbook_mirror", Note: "Helmholtz cell + two mirror throats",
			Score: -0.2905708161, Feasible: true, DesignID: "D0001",
			Terms: map[string]float64{"field": 0.0, "mirror": 0.247530, "volume": 0.780886, "ripple": 0.0, "cost": 1.0},
			Weighted: map[string]float64{
				"field": 0.0, "mirror": 0.123765, "volume": 0.5856645, "ripple": 0.0, "cost": -1.0,
			},
			Penalties: map[string]float64{"conductor_field": 0.0, "coil_separation": 0.0, "not_a_mirror": 0.0},
			Metrics: physics.Metrics{
				BMidT: 1.0, BThroatT: 3.536386, ZThroatM: -0.9975, MirrorRatio: 3.536386,
				VolumeGood: 0.780886, Ripple: 0.0, BCoilMaxT: 3.416271, MinCoilGapM: 0.5,
				CostProxy: 1.791703035e12, NCoils: 4, MU0: config.MU0,
			},
			Design:    []float64{0.3, 0.5, 0.5, 0.3, -1.0, -0.25, 0.25, 1.0, 1621279.24, 463222.64, 463222.64, 1621279.24},
			CostProxy: 1.791703035e12,
		},
		Runs: []search.Result{
			{
				Algorithm: "evolution", Seed: 0, Budget: 1000, NEvals: 1000,
				BestScore: -0.24, BestDesignID: "D0042", BestFeasible: true,
				BestTerms:   map[string]float64{"field": 0.02, "mirror": 0.31, "volume": 0.83, "ripple": 0.0, "cost": 0.96},
				BestMetrics: physics.Metrics{BMidT: 1.05, MirrorRatio: 4.1, VolumeGood: 0.83, NCoils: 4},
				BestDesign:  []float64{0.32, 0.5, 0.5, 0.29, -1.0, -0.25, 0.25, 1.0, 1.5e6, 4.6e5, 4.6e5, 1.5e6},
				EvalsToBeat: 210,
			},
			{
				Algorithm: "random", Seed: 0, Budget: 1000, NEvals: 1000,
				BestScore: -0.41, BestDesignID: "D0099", BestFeasible: true,
				BestTerms:   map[string]float64{"field": -0.05, "mirror": 0.10, "volume": 0.70, "ripple": 0.02, "cost": 1.3},
				EvalsToBeat: -1,
			},
		},
		History: map[string][]float64{"evolution/seed=0": {-0.9, -0.5, -0.24}},
		Aggregate: map[string]experiment.Agg{
			"evolution": {
				NSeeds: 3, Budget: 1000, BestMean: -0.22, BestStd: 0.03, BestMin: -0.25, BestMax: -0.19,
				NBeatingBaseline: 3, FracBeatingBaseline: 1, EvalsToBeatMean: 180, EvalsToBeatMedian: 190,
				BaselineScore: -0.2905708161,
			},
			"random": {
				NSeeds: 3, Budget: 1000, BestMean: -0.38, BestStd: 0.05, BestMin: -0.44, BestMax: -0.34,
				NBeatingBaseline: 0, FracBeatingBaseline: 0, EvalsToBeatMean: -1, EvalsToBeatMedian: -1,
				BaselineScore: -0.2905708161,
			},
		},
		Robustness: experiment.Robustness{
			Variants:           []string{"b_ref=0.8T", "b_ref=1.2T", "z_cell=0.60m", "z_cell=1.00m", "r_plasma=0.12m", "r_plasma=0.18m"},
			BaselinePerVariant: map[string]float64{"b_ref=0.8T": -0.28, "b_ref=1.2T": -0.30, "z_cell=0.60m": -0.31, "z_cell=1.00m": -0.27, "r_plasma=0.12m": -0.29, "r_plasma=0.18m": -0.285},
			PerDesign: map[string]map[string]float64{
				"evolution":      {"b_ref=0.8T": 0.02, "b_ref=1.2T": -0.01},
				"human_baseline": {"b_ref=0.8T": -0.04, "b_ref=1.2T": -0.06},
			},
			Summary: map[string]experiment.RobustSummary{
				"evolution":      {MeanDeltaVsBaseline: 0.004, WorstDeltaVsBaseline: -0.01, NVariantsWinning: 4, NVariants: 6},
				"human_baseline": {MeanDeltaVsBaseline: -0.05, WorstDeltaVsBaseline: -0.06, NVariantsWinning: 0, NVariants: 6},
			},
		},
		Best: &experiment.BestRec{
			DesignID: "D0042", Algorithm: "evolution", Seed: 0, Score: -0.24, Feasible: true,
			Terms:   map[string]float64{"field": 0.02, "mirror": 0.31, "volume": 0.83, "ripple": 0.0, "cost": 0.96},
			Metrics: physics.Metrics{BMidT: 1.05, MirrorRatio: 4.1, VolumeGood: 0.83, Ripple: 0.0, BCoilMaxT: 3.1, MinCoilGapM: 0.4, CostProxy: 1.6e12, NCoils: 4},
			Design:  []float64{0.32, 0.5, 0.5, 0.29, -1.0, -0.25, 0.25, 1.0, 1.5e6, 4.6e5, 4.6e5, 1.5e6},
		},
		RegistrySummary: registry.Summary{
			NRecords: 4, NFeasible: 4,
			PerAlgo:   map[string]int{"human_baseline": 1, "evolution": 1, "random": 1},
			BestScore: -0.24, BestDesignID: "D0042", BestAlgorithm: "evolution",
		},
	}
}

func TestRenderMarkdownHasAllFrozenSectionsInOrder(t *testing.T) {
	md := RenderMarkdown(sampleReport(), sampleRules(), "runs/phase0-test/knowledge/rules.md")
	if !strings.HasPrefix(md, "# Forge phase0-test 运行报告") {
		t.Fatalf("report does not start with the frozen headline:\n%s", firstLines(md, 3))
	}
	prev := 0
	for _, sec := range frozenSections {
		i := strings.Index(md, sec)
		if i < 0 {
			t.Fatalf("section %q is missing from the report", sec)
		}
		if i < prev {
			t.Fatalf("section %q appears out of order", sec)
		}
		prev = i
	}
}

func TestRenderMarkdownHonestLimitsSectionIsSubstantive(t *testing.T) {
	md := RenderMarkdown(sampleReport(), sampleRules(), "")
	body, ok := between(md, "## 6. 诚实边界", "## 7. 下一步")
	if !ok {
		t.Fatal("could not isolate the honest-limits section")
	}
	if len(body) < 1200 {
		t.Fatalf("诚实边界 section is only %d chars; it must name the gaps, not gesture at them", len(body))
	}
	for _, must := range []string{
		"没有建模", // v0.1 没有建模的东西
		"没有 plasma",
		"推翻",   // 什么会推翻这个结果
		"实测",   // 哪些数字是实测的
		"假设",   // 哪些是假设的
		"ddof", // 统计约定被明确写出来
		"RuleExpectation",
	} {
		if !strings.Contains(body, must) {
			t.Errorf("诚实边界 is missing the required content %q", must)
		}
	}
}

func TestRenderMarkdownReportsBothDirections(t *testing.T) {
	md := RenderMarkdown(sampleReport(), sampleRules(), "rules.md")
	// §2 必须带上分数项表格，两个方向都要写清楚
	for _, must := range []string{"field", "mirror", "volume", "ripple", "cost", "↑ 好", "↓ 好", "谁更好"} {
		if !strings.Contains(md, must) {
			t.Errorf("report is missing %q (a report that only shows wins is not finished)", must)
		}
	}
	if !strings.Contains(md, "结论一句话") {
		t.Error("headline verdict is missing")
	}
	// 这里机器赢了（机器 -0.24 优于人工 -0.2906）
	if !strings.Contains(md, "机器**超过**人工基线") {
		t.Error("headline does not state the win")
	}
}

func TestRenderMarkdownStatesLossWhenMachineLoses(t *testing.T) {
	rep := sampleReport()
	rep.Best.Score = -0.42 // 比基线更差
	md := RenderMarkdown(rep, nil, "")
	if !strings.Contains(md, "机器**没有超过**人工基线") {
		t.Error("a losing batch is not reported as a loss")
	}
	if !strings.Contains(md, "输就是输") {
		t.Error("a losing batch is not stated plainly")
	}
	// 而逐分数项表格里必须体现出人工在某个地方赢了
	if !strings.Contains(md, "| 人 |") && !strings.Contains(md, "| 机器 |") {
		t.Error("the term table does not compare the two sides")
	}
}

func TestRenderMarkdownWithoutRunsOrBestOrRules(t *testing.T) {
	rep := &experiment.Report{
		Meta:     experiment.Meta{Tag: "empty"},
		Baseline: experiment.BaseRec{Name: "textbook_mirror", Score: -0.2905708161},
	}
	md := RenderMarkdown(rep, nil, "")
	for _, sec := range frozenSections {
		if !strings.Contains(md, sec) {
			t.Errorf("section %q missing from an empty report", sec)
		}
	}
	for _, must := range []string{"没有产生任何机器设计", "机器最优: 无", "本批没有通过复制检验的规则", "没有泛化探针结果"} {
		if !strings.Contains(md, must) {
			t.Errorf("empty report does not state %q explicitly", must)
		}
	}
	// 规则切片为 nil、最优设计为 nil 时不得 panic，并且指标表必须用
	// 破折号渲染，而不是用零
	if strings.Contains(md, "| B_mid_T (midplane 体平均场) | 0 | 0 |") {
		t.Error("missing machine metrics rendered as zeros instead of dashes")
	}
}

func TestRenderMarkdownNilReportDoesNotPanic(t *testing.T) {
	md := RenderMarkdown(nil, nil, "")
	if !strings.Contains(md, "report 为空") {
		t.Errorf("nil report rendered as %q, want an explicit statement", md)
	}
}

func TestRenderMarkdownReferencesRulePath(t *testing.T) {
	md := RenderMarkdown(sampleReport(), sampleRules(), "runs/phase0-test/knowledge/rules.md")
	if !strings.Contains(md, "runs/phase0-test/knowledge/rules.md") {
		t.Error("§5 does not point at the machine-readable rules file")
	}
	if !strings.Contains(md, "R001") || !strings.Contains(md, "z_3") {
		t.Error("§5 does not carry the mined rules")
	}
}

func TestWrite(t *testing.T) {
	md := RenderMarkdown(sampleReport(), sampleRules(), "rules.md")
	path := filepath.Join(t.TempDir(), "nested", "REPORT.md")
	if err := Write(path, md); err != nil {
		t.Fatalf("Write: %v", err)
	}
	back, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(back) != md {
		t.Fatal("written report differs from the rendered markdown")
	}
}

// --- 辅助函数 ---------------------------------------------------------------

func between(s, start, end string) (string, bool) {
	i := strings.Index(s, start)
	if i < 0 {
		return "", false
	}
	rest := s[i+len(start):]
	j := strings.Index(rest, end)
	if j < 0 {
		return "", false
	}
	return rest[:j], true
}

func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
