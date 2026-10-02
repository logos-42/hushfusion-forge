// Package experiment：等预算 benchmark 工具 + Phase-0 流水线。
//
// 冻结接口 (v0.1) — 负责人：阶段 E。
//
// 一共测三件事，它们回答的是不同的问题：
//
//	最优性能 (best-of-budget)  在固定成本下，最好的设计能有多好
//	收敛速度 (evals to beat)   机器需要多少设计尝试，才已经优于一名合格的
//	                          工程师 —— 衡量的是学习回路的*速率*，而不是它
//	                          的终点
//	泛化 (robustness probe)   每种方法的最优设计都在扰动后的要求下重新打分，
//	                          并与针对同一扰动重新求解过的人工基线对比。
//	                          一个只在它被搜索的那个精确盒子里获胜的设计
//	                          并没有泛化，而把这一点明说出来本身就是结果
//	                          的一部分。
//
// Report 的 JSON schema 由 Python 辅助层消费
// (python/aux/analyze.py)；键名是冻结的 (FROZEN)。
package experiment

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/logos-42/hushfusion-forge/internal/baseline"
	"github.com/logos-42/hushfusion-forge/internal/config"
	"github.com/logos-42/hushfusion-forge/internal/objective"
	"github.com/logos-42/hushfusion-forge/internal/physics"
	"github.com/logos-42/hushfusion-forge/internal/registry"
	"github.com/logos-42/hushfusion-forge/internal/runner"
	"github.com/logos-42/hushfusion-forge/internal/search"
)

// Variant 是一次要求扰动：一个名字加上若干 spec 覆盖项。
type Variant struct {
	Name     string             `json:"name"`
	Override map[string]float64 `json:"override"`
}

// SpecVariants 是泛化探针使用的六次扰动。
var SpecVariants = []Variant{
	{Name: "b_ref=0.8T", Override: map[string]float64{"b_ref": 0.8}},
	{Name: "b_ref=1.2T", Override: map[string]float64{"b_ref": 1.2}},
	{Name: "z_cell=0.60m", Override: map[string]float64{"z_cell": 0.60}},
	{Name: "z_cell=1.00m", Override: map[string]float64{"z_cell": 1.00}},
	{Name: "r_plasma=0.12m", Override: map[string]float64{"r_plasma": 0.12}},
	{Name: "r_plasma=0.18m", Override: map[string]float64{"r_plasma": 0.18}},
}

// variantKeys 是 Variant 允许覆盖的 spec 键。新增一个键属于契约变更
// （Python 辅助层会镜像这份列表）。
var variantKeys = []string{"b_ref", "z_cell", "r_plasma"}

// ApplyVariant 返回应用了该 variant 覆盖项之后的 spec。
// 支持的键：b_ref、z_cell、r_plasma。未知键必须报错，
// 而不是被静默忽略。
//
// 整个覆盖集合会在写入任何东西之前先整体校验，因此一旦报错，返回的 spec
// 保证与输入 spec 逐字节相同：即使调用方忽略了这个错误，也拿不到一个
// 被改了一半的设备。
func ApplyVariant(spec config.Spec, v Variant) (config.Spec, error) {
	keys := make([]string, 0, len(v.Override))
	for k := range v.Override {
		keys = append(keys, k)
	}
	sort.Strings(keys) // 错误文本保持确定性
	for _, k := range keys {
		if !isVariantKey(k) {
			return spec, fmt.Errorf("experiment: variant %q overrides unknown spec key %q (supported: %s)",
				v.Name, k, strings.Join(variantKeys, ", "))
		}
	}
	out := spec
	for _, k := range keys {
		switch k {
		case "b_ref":
			out.BRef = v.Override[k]
		case "z_cell":
			out.ZCell = v.Override[k]
		case "r_plasma":
			out.RPlasma = v.Override[k]
		}
	}
	return out, nil
}

func isVariantKey(k string) bool {
	for _, known := range variantKeys {
		if k == known {
			return true
		}
	}
	return false
}

// Meta 是写入报告中的运行溯源信息。
type Meta struct {
	Tag       string   `json:"tag"`
	Timestamp string   `json:"timestamp"`
	Budget    int      `json:"budget"`
	Seeds     []int    `json:"seeds"`
	Methods   []string `json:"methods"`
	GitCommit string   `json:"git_commit"`
	Platform  string   `json:"platform"`
	GoVersion string   `json:"go_version"`
	// ForgeVersion 是产出这次 run 的**判据版本**（config.ForgeVersion）。没有它,
	// 门就无法区分「同一判据下的可复现」与「换了判据所以本来就不该相同」——
	// 后者在 0.1.2 加可造性罚项时真实发生过（runs/phase0 是 0.1.0 的证据）。
	ForgeVersion string `json:"forge_version"`
	Workers      int    `json:"workers"`
}

// Agg 是按方法、跨 seed 的统计量。绝不据此做单 seed 的结论。
type Agg struct {
	NSeeds              int     `json:"n_seeds"`
	Budget              int     `json:"budget"`
	BestMean            float64 `json:"best_mean"`
	BestStd             float64 `json:"best_std"`
	BestMin             float64 `json:"best_min"`
	BestMax             float64 `json:"best_max"`
	NBeatingBaseline    int     `json:"n_beating_baseline"`
	FracBeatingBaseline float64 `json:"frac_beating_baseline"`
	EvalsToBeatMean     float64 `json:"evals_to_beat_mean"`   // 没有任何 seed 超过基线时为 -1
	EvalsToBeatMedian   float64 `json:"evals_to_beat_median"` // 没有任何 seed 超过基线时为 -1
	BaselineScore       float64 `json:"baseline_score"`
}

// BaseRec 是记录在案的人工基线。
type BaseRec struct {
	Name      string             `json:"name"`
	Note      string             `json:"note"`
	Score     float64            `json:"score"`
	Feasible  bool               `json:"feasible"`
	Terms     map[string]float64 `json:"terms"`
	Weighted  map[string]float64 `json:"weighted"`
	Penalties map[string]float64 `json:"penalties"`
	Metrics   physics.Metrics    `json:"metrics"`
	Design    []float64          `json:"design"`
	CostProxy float64            `json:"cost_proxy"`
	DesignID  string             `json:"design_id"`
}

// BestRec 是机器找到的单个最优设计。
type BestRec struct {
	DesignID  string             `json:"design_id"`
	Algorithm string             `json:"algorithm"`
	Seed      int                `json:"seed"`
	Score     float64            `json:"score"`
	Terms     map[string]float64 `json:"terms"`
	Metrics   physics.Metrics    `json:"metrics"`
	Design    []float64          `json:"design"`
	Feasible  bool               `json:"feasible"`
}

// Robustness 是泛化探针的结果。
type Robustness struct {
	Variants           []string                      `json:"variants"`
	BaselinePerVariant map[string]float64            `json:"baseline_score_per_variant"`
	PerDesign          map[string]map[string]float64 `json:"per_design"`
	Summary            map[string]RobustSummary      `json:"summary"`
}

// RobustSummary 是探针按设计汇总后的结果。
type RobustSummary struct {
	MeanDeltaVsBaseline  float64 `json:"mean_delta_vs_baseline"`
	WorstDeltaVsBaseline float64 `json:"worst_delta_vs_baseline"`
	NVariantsWinning     int     `json:"n_variants_winning"`
	NVariants            int     `json:"n_variants"`
}

// Report 是写入 runs/<tag>/results.json 的完整产物。
type Report struct {
	Meta            Meta                 `json:"meta"`
	Spec            map[string]any       `json:"spec"`
	Solver          string               `json:"solver"`
	CostRef         float64              `json:"cost_ref"`
	Baseline        BaseRec              `json:"baseline"`
	Runs            []search.Result      `json:"runs"`
	History         map[string][]float64 `json:"history"`
	Aggregate       map[string]Agg       `json:"aggregate"`
	Robustness      Robustness           `json:"robustness"`
	Best            *BestRec             `json:"best"`
	RegistrySummary registry.Summary     `json:"registry_summary"`
	RLEnvReference  map[string]any       `json:"rl_env_reference,omitempty"`
}

// Opts 配置一次 benchmark 运行。
type Opts struct {
	Budget   int
	Seeds    []int
	Methods  []string
	Tag      string
	Workers  int
	Baseline *baseline.Baseline
	Progress func(method string, seed int, res search.Result, seconds float64)
}

const (
	// defaultBudget 与 search.DefaultOptions 以及报告中标明的设置一致
	//（每次运行 1000 次评估）。
	defaultBudget = 1000
	// humanBaselineAlgorithm 是人工基线记录时所用的算法名，
	// 这样「哪条分支改进了人工基线？」才是可回答的。
	humanBaselineAlgorithm = "human_baseline"
	// warmStartMethod 是唯一消费 Opts.WarmStart 的方法；benchmark
	// 会把人工设计向量交给它（知识复用）。
	warmStartMethod = "evolution_warm"
)

// 当 Opts 把它们留空时，会使用 defaultMethods / defaultSeeds。
// 三个 seed 是让离散度变得有意义的最小值；报告拒绝单 seed 的结论。
var (
	defaultMethods = []string{"random", "lhs", "evolution", "evolution_warm"}
	defaultSeeds   = []int{0, 1, 2}
)

// RunBenchmark 先记录人工基线，再以完全相同的预算在每一个 seed 上运行每种
// 方法，然后做聚合并探测泛化能力。
//
// 基线以它自己的算法名 ("human_baseline") 记录，并成为设计 D0001 —— 谱系树
// 的根，所以「哪条分支改进了人工基线？」是可以回答的；这正是把基线记成
// 独立算法的原因。
//
// 实现说明（阶段 E）：
//
//   - registry 必须为空：D0001 作为根这一主张，是对产物本身的断言，
//     只有当基线是第一条被追加的记录时才成立。非空 registry 会被响亮
//     地拒绝，而不是产出一份谱系主张为假的报告；
//   - 基线与每一个机器设计都走同一个 Evaluator 打分
//     (cost_ref = 基线的欧姆成本)，因此这个比较是同口径的；
//   - 各次运行按顺序执行（opt.Workers 只在一个运行内部并行）—— 无论如何，
//     墙上时间都由物理计算主导，而确定性的顺序让 runs/ 在不同次调用之间
//     保持可比。
func RunBenchmark(reg *registry.Registry, spec config.Spec, opt Opts) (*Report, error) {
	if reg == nil {
		return nil, errors.New("experiment: RunBenchmark needs an open registry")
	}
	if n := reg.Len(); n != 0 {
		return nil, fmt.Errorf("experiment: registry already holds %d records; the benchmark records the human baseline as the lineage root D0001 and needs an empty registry (use a fresh path)", n)
	}

	methods := append([]string(nil), opt.Methods...)
	if len(methods) == 0 {
		methods = append([]string(nil), defaultMethods...)
	}
	seeds := append([]int(nil), opt.Seeds...)
	if len(seeds) == 0 {
		seeds = append([]int(nil), defaultSeeds...)
	}
	budget := opt.Budget
	if budget <= 0 {
		budget = defaultBudget
	}
	workers := opt.Workers
	if workers < 1 {
		workers = 1
	}

	base := opt.Baseline
	if base == nil {
		b, err := baseline.TextbookMirror(spec)
		if err != nil {
			return nil, fmt.Errorf("experiment: solving the human baseline: %w", err)
		}
		base = &b
	}
	baseDesign, err := designVectorOf(*base, spec)
	if err != nil {
		return nil, err
	}

	solver := defaultSolver()
	grids := physics.BuildGrids(spec)
	ev := objective.NewEvaluator(spec, solver, base.Cost, grids)
	sc := runner.New(reg, ev, opt.Tag)

	bres := sc.Score(baseDesign, runner.Meta{
		Algorithm: humanBaselineAlgorithm,
		Seed:      0,
		EvalIndex: 0,
		Note:      "human baseline, re-scored by the same evaluator as every machine design",
	})
	baseName := base.Name
	if baseName == "" {
		baseName = humanBaselineAlgorithm
	}
	baseRec := BaseRec{
		Name:      baseName,
		Note:      base.Note,
		Score:     bres.Score,
		Feasible:  bres.Feasible,
		Terms:     bres.Terms,
		Weighted:  bres.Weighted,
		Penalties: bres.Penalties,
		Metrics:   bres.Metrics,
		Design:    bres.Design,
		CostProxy: bres.Metrics.CostProxy,
		DesignID:  bres.DesignID,
	}
	if len(baseRec.Design) == 0 {
		baseRec.Design = baseDesign
	}
	if baseRec.CostProxy == 0 {
		baseRec.CostProxy = base.Cost
	}

	rep := &Report{
		Meta: Meta{
			Tag:          opt.Tag,
			Timestamp:    time.Now().UTC().Format(time.RFC3339),
			Budget:       budget,
			Seeds:        seeds,
			Methods:      methods,
			GitCommit:    gitCommit(),
			Platform:     runtime.GOOS + "/" + runtime.GOARCH,
			GoVersion:    runtime.Version(),
			ForgeVersion: config.ForgeVersion,
			Workers:      workers,
		},
		Spec:      spec.AsMap(),
		Solver:    solver.Name(),
		CostRef:   base.Cost,
		Baseline:  baseRec,
		History:   map[string][]float64{},
		Aggregate: map[string]Agg{},
	}

	runs := make([]search.Result, 0, len(methods)*len(seeds))
	for _, m := range methods {
		for _, s := range seeds {
			o := search.DefaultOptions(spec)
			o.Spec = spec
			o.Seed = s
			o.Budget = budget
			o.Algorithm = m
			o.Workers = workers
			o.BaselineScore = baseRec.Score
			if m == warmStartMethod {
				o.WarmStart = append([]float64(nil), baseDesign...)
			}
			started := time.Now()
			res, err := search.Run(m, sc, o)
			if err != nil {
				return nil, fmt.Errorf("experiment: method %q seed %d: %w", m, s, err)
			}
			if opt.Progress != nil {
				opt.Progress(m, s, res, time.Since(started).Seconds())
			}
			runs = append(runs, res)
			rep.History[fmt.Sprintf("%s/seed=%d", m, s)] = res.History
		}
	}
	rep.Runs = runs
	rep.Aggregate = Aggregate(runs, baseRec.Score)

	// 每种方法跨其各 seed 的最优设计，都在扰动后的要求下重新打分；记录在案的
	// 人工设计一同参与，充当那个近乎零点的参照（它由 variant 目标函数重新
	// 打分，而不是重新求解）。
	designs := map[string][]float64{humanBaselineAlgorithm: baseDesign}
	bestRun := map[string]search.Result{}
	for _, r := range runs {
		if b, ok := bestRun[r.Algorithm]; !ok || r.BestScore > b.BestScore {
			bestRun[r.Algorithm] = r
		}
	}
	for _, m := range methods {
		if b, ok := bestRun[m]; ok && len(b.BestDesign) > 0 {
			designs[m] = b.BestDesign
		}
	}
	rob, err := RobustnessProbe(spec, designs, SpecVariants)
	if err != nil {
		return nil, fmt.Errorf("experiment: robustness probe: %w", err)
	}
	rep.Robustness = rob

	if len(runs) > 0 {
		b := runs[0]
		for _, r := range runs[1:] {
			if r.BestScore > b.BestScore {
				b = r
			}
		}
		rep.Best = &BestRec{
			DesignID:  b.BestDesignID,
			Algorithm: b.Algorithm,
			Seed:      b.Seed,
			Score:     b.BestScore,
			Terms:     b.BestTerms,
			Metrics:   b.BestMetrics,
			Design:    b.BestDesign,
			Feasible:  b.BestFeasible,
		}
	}

	rep.RegistrySummary = reg.Summary()
	return rep, nil
}

// designVectorOf 返回人工基线的规范设计向量：它记录了 Design 时就用它，
// 否则用其线圈的编码。
func designVectorOf(b baseline.Baseline, spec config.Spec) ([]float64, error) {
	if len(b.Design) == spec.NParams() {
		return append([]float64(nil), b.Design...), nil
	}
	if len(b.Coils) > 0 {
		if x := physics.CoilsToVector(b.Coils); len(x) == spec.NParams() {
			return x, nil
		}
	}
	return nil, fmt.Errorf("experiment: human baseline %q decoded to %d parameters, spec wants %d",
		b.Name, len(b.Design), spec.NParams())
}

// defaultSolver 是解析（精确圆电流丝）求解器：也就是 CLI 的 `forge verify`
// 拿 testdata/golden_field_samples.json 做锚定的那个求解器，也是分数定义
// 所用的那一个。DiscreteSolver 只是独立交叉校验，不是 benchmark 的求解器。
func defaultSolver() physics.Solver { return physics.AnalyticSolver{} }

// Aggregate 计算按方法、跨 seed 的统计量。
//
// 统计基于去重后的 seed，并按 seed 排序以保证可复现；重复出现的 seed 只
// 计一次（该 seed 的最后一条结果生效），因此重复运行不会把 n_seeds 灌大。
// 下面这些约定之所以写明，是因为 Python 辅助层会重新计算它们：
//
//	best_std                 样本标准差 (ddof = 1)；n < 2 时为 0
//	best_min / best_max      在 seed 之间取
//	n_beating_baseline       满足 best_score > baseline_score 的 seed 数（严格）
//	evals_to_beat_*          只对确实超过基线的那些 seed 取均值/中位数
//	                         (best_score > baseline_score 且 evals_to_beat >= 0)；
//	                         没有任何 seed 超过时为 -1
//	median                   偶数个时取中间两个值的均值
func Aggregate(runs []search.Result, baselineScore float64) map[string]Agg {
	byMethod := map[string][]search.Result{}
	for _, r := range runs {
		byMethod[r.Algorithm] = append(byMethod[r.Algorithm], r)
	}
	out := make(map[string]Agg, len(byMethod))
	for method, rs := range byMethod {
		bySeed := map[int]search.Result{}
		seeds := make([]int, 0, len(rs))
		for _, r := range rs {
			if _, dup := bySeed[r.Seed]; !dup {
				seeds = append(seeds, r.Seed)
			}
			bySeed[r.Seed] = r
		}
		sort.Ints(seeds)

		agg := Agg{NSeeds: len(seeds), BaselineScore: baselineScore}
		scores := make([]float64, 0, len(seeds))
		evals := make([]float64, 0, len(seeds))
		for _, s := range seeds {
			r := bySeed[s]
			scores = append(scores, r.BestScore)
			if r.Budget > agg.Budget {
				agg.Budget = r.Budget
			}
			// 「是否超过基线」这个判定只做一次，且只看 score：一次运行如果分数低于
			// 基线却上报了 evals_to_beat，那就是自相矛盾的输入，而把它放进收敛统计
			// 会灌大那个唯一说明回路学得多快的数字。
			beats := r.BestScore > baselineScore
			if beats {
				agg.NBeatingBaseline++
			}
			if r.EvalsToBeat >= 0 && beats {
				evals = append(evals, float64(r.EvalsToBeat))
			}
		}
		agg.BestMean = mean(scores)
		agg.BestStd = stdSample(scores)
		agg.BestMin, agg.BestMax = minMax(scores)
		if agg.NSeeds > 0 {
			agg.FracBeatingBaseline = float64(agg.NBeatingBaseline) / float64(agg.NSeeds)
		}
		agg.EvalsToBeatMean, agg.EvalsToBeatMedian = -1, -1
		if len(evals) > 0 {
			agg.EvalsToBeatMean = mean(evals)
			agg.EvalsToBeatMedian = median(evals)
		}
		out[method] = agg
	}
	return out
}

// RobustnessProbe 在扰动后的要求下给设计重新打分，参照物是针对每个 variant
// 重新求解过的人工基线（因此零点始终是「一个为新要求重新设计的人」）。
//
// 对每个设计和 variant，上报的数值是
//
//	per_design[design][variant] = score(design, variant) - score(human_variant)
//
// 其中 score(human_variant) 是人工设计针对扰动后 spec 重新求解的结果
// （即 variant 下的 baseline.TextbookMirror，由 variant 目标函数和 variant
// 自己的成本参考打分）。因此正的 delta 意味着「仍然优于一个为新要求重新
// 设计过的人」。
//
// 记录在案的那份基线设计本身也会在每个 variant 下被打分（用 "human_baseline"
// 这个键传进来）；它并没有为扰动重新设计过，所以它的 delta 通常是负的，
// 而这种反差正是要看的点。
func RobustnessProbe(spec config.Spec, designs map[string][]float64, variants []Variant) (Robustness, error) {
	out := Robustness{
		Variants:           []string{},
		BaselinePerVariant: map[string]float64{},
		PerDesign:          map[string]map[string]float64{},
		Summary:            map[string]RobustSummary{},
	}
	names := make([]string, 0, len(designs))
	for name := range designs {
		names = append(names, name)
	}
	sort.Strings(names)

	// 先做输入校验，而且它是纯的：一个坏的 variant 键，或者长度不对的
	// 设计向量，必须在碰到任何物理计算之前就失败。
	vspecs := make([]config.Spec, len(variants))
	for i, v := range variants {
		s, err := ApplyVariant(spec, v)
		if err != nil {
			return out, err
		}
		vspecs[i] = s
	}
	for _, name := range names {
		if len(designs[name]) != spec.NParams() {
			return out, fmt.Errorf("design %q has %d parameters, spec wants %d", name, len(designs[name]), spec.NParams())
		}
	}

	solver := defaultSolver()
	for i, v := range variants {
		vspec := vspecs[i]
		human, err := baseline.TextbookMirror(vspec)
		if err != nil {
			return out, fmt.Errorf("variant %q: re-solving the human baseline: %w", v.Name, err)
		}
		hx, err := designVectorOf(human, vspec)
		if err != nil {
			return out, fmt.Errorf("variant %q: %w", v.Name, err)
		}
		ev := objective.NewEvaluator(vspec, solver, human.Cost, physics.BuildGrids(vspec))
		humanScore := ev.Score(hx)

		out.Variants = append(out.Variants, v.Name)
		out.BaselinePerVariant[v.Name] = humanScore
		for _, name := range names {
			delta := ev.Score(designs[name]) - humanScore
			if out.PerDesign[name] == nil {
				out.PerDesign[name] = map[string]float64{}
			}
			out.PerDesign[name][v.Name] = delta
			sum := out.Summary[name]
			if sum.NVariants == 0 {
				sum.WorstDeltaVsBaseline = delta
			} else if delta < sum.WorstDeltaVsBaseline {
				sum.WorstDeltaVsBaseline = delta
			}
			sum.MeanDeltaVsBaseline += delta
			sum.NVariants++
			if delta > 0 {
				sum.NVariantsWinning++
			}
			out.Summary[name] = sum
		}
	}
	for name, sum := range out.Summary {
		if sum.NVariants > 0 {
			sum.MeanDeltaVsBaseline /= float64(sum.NVariants)
		}
		out.Summary[name] = sum
	}
	return out, nil
}

// WriteJSON 以带缩进的 JSON 写出报告。父目录会被创建，
// 因此可以直接写 runs/<tag>/results.json。
func WriteJSON(rep *Report, path string) error {
	data, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return fmt.Errorf("experiment: marshal report: %w", err)
	}
	data = append(data, '\n')
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("experiment: create report directory %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("experiment: write report %s: %w", path, err)
	}
	return nil
}

// LoadReport 把报告读回来。
func LoadReport(path string) (*Report, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rep Report
	if err := json.Unmarshal(data, &rep); err != nil {
		return nil, fmt.Errorf("experiment: parse report %s: %w", path, err)
	}
	return &rep, nil
}

// --- 统计辅助函数 -----------------------------------------------------------

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := 0.0
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

// stdSample 是无偏样本标准差 (ddof = 1)。
func stdSample(xs []float64) float64 {
	if len(xs) < 2 {
		return 0
	}
	m := mean(xs)
	ss := 0.0
	for _, x := range xs {
		d := x - m
		ss += d * d
	}
	return math.Sqrt(ss / float64(len(xs)-1))
}

// median 先复制再排序；偶数个时取中间两个值的均值。
func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	cp := append([]float64(nil), xs...)
	sort.Float64s(cp)
	n := len(cp)
	if n%2 == 1 {
		return cp[n/2]
	}
	return 0.5 * (cp[n/2-1] + cp[n/2])
}

func minMax(xs []float64) (lo, hi float64) {
	if len(xs) == 0 {
		return 0, 0
	}
	lo, hi = xs[0], xs[0]
	for _, x := range xs[1:] {
		if x < lo {
			lo = x
		}
		if x > hi {
			hi = x
		}
	}
	return lo, hi
}

// --- 溯源辅助函数 -----------------------------------------------------------

// gitCommit 尽力而为地取本次运行所在检出目录的 HEAD sha：它从工作目录
// 向上查找并解析 .git/HEAD（既支持 .git 目录，也支持 worktree 使用的
// "gitdir: ..." 文件）。它从不调用外部命令，并且在运行不在 git 检出目录
// 内时返回 ""，而不是编造一个值 —— 溯源块里一个伪造的 commit hash 比
// 空值更糟。
func gitCommit() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for i := 0; i < 64; i++ {
		git := filepath.Join(dir, ".git")
		if info, err := os.Stat(git); err == nil {
			gitDir := git
			if !info.IsDir() {
				body, err := os.ReadFile(git)
				if err != nil {
					return ""
				}
				rest, ok := strings.CutPrefix(strings.TrimSpace(string(body)), "gitdir:")
				if !ok {
					return ""
				}
				gitDir = strings.TrimSpace(rest)
				if !filepath.IsAbs(gitDir) {
					gitDir = filepath.Join(dir, gitDir)
				}
			}
			head, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
			if err != nil {
				return ""
			}
			return resolveGitHead(gitDir, strings.TrimSpace(string(head)))
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
	return ""
}

func resolveGitHead(gitDir, head string) string {
	ref, ok := strings.CutPrefix(head, "ref:")
	if !ok {
		return head // detached HEAD: HEAD 本身就已经是 sha
	}
	ref = strings.TrimSpace(ref)
	if body, err := os.ReadFile(filepath.Join(gitDir, filepath.FromSlash(ref))); err == nil {
		return strings.TrimSpace(string(body))
	}
	if body, err := os.ReadFile(filepath.Join(gitDir, "packed-refs")); err == nil {
		for _, line := range strings.Split(string(body), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "^") {
				continue
			}
			if f := strings.Fields(line); len(f) == 2 && f[1] == ref {
				return f[0]
			}
		}
	}
	return ""
}
