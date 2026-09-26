// Package search: 设计空间上的搜索算法。
//
// FROZEN INTERFACE (v0.1) — 负责人: stage D。
//
// 每个算法的契约都相同: 它拿到一个评估 *budget*,
// 通过一个 runner.Scorer 花掉它(因此 registry 在构造上就是完整的),
// 并返回它的 best-so-far 轨迹 —— 这是在等成本下比较算法
// 唯一诚实的方式。
//
//	random          均匀 i.i.d. 采样 —— 零假设
//	lhs             latin-hypercube(分层空间填充)采样
//	evolution       (mu+lambda) 精英进化策略, mutation 方差
//	                随已花 budget 线性退火
//	evolution_warm  同上, 但初始种群由人类 baseline
//	                播种 —— 最廉价的 *知识复用* 形式,
//	                之所以纳入, 是因为 "继承来的设计知识值钱吗?" 是一个
//	                可测量的问题, 不是口号
//
// 刻意不提供真正的 grid search: 在 D = 12 时, 每轴哪怕 5 个点
// 也是 2.4e8 次评估。lhs 是诚实的替代品, 这个替换
// 会在报告中讲明, 而不是粉饰过去。
//
// 可复现性规则: 给定相同的 Options, Result 逐位相同 ——
// 除了 Workers > 1 时 record 落进 registry 的顺序。
// 得分、轨迹与最佳设计都不允许依赖 Workers。
package search

import (
	"fmt"
	"math/rand"
	"sort"
	"strings"

	"github.com/logos-42/hushfusion-forge/internal/config"
	"github.com/logos-42/hushfusion-forge/internal/physics"
	"github.com/logos-42/hushfusion-forge/internal/runner"
)

// 算法名。FROZEN: 它们会作为 Meta.Algorithm 写进每一条 registry record,
// 也会写进 Result.Algorithm。
const (
	AlgorithmRandom        = "random"
	AlgorithmLHS           = "lhs"
	AlgorithmEvolution     = "evolution"
	AlgorithmEvolutionWarm = "evolution_warm"
)

// 文档化的默认值(由 DefaultOptions 镜像)。
const (
	DefaultBudget     = 1000
	DefaultMu         = 16
	DefaultLam        = 48
	DefaultSigma0     = 0.25
	DefaultSigmaFloor = 0.03
	DefaultWorkers    = 1
)

// Options 完整指定一次运行。Seed 与 Budget 是调用方通常
// 唯一会设置的旋钮; 其余都有文档化的默认值(DefaultOptions)。
type Options struct {
	Spec          config.Spec
	Seed          int
	Budget        int
	BaselineScore float64 // 人类 baseline 得分; 仅用于 EvalsToBeat
	Mu            int     // evolution: parent 数量
	Lam           int     // evolution: 每代 children 数量
	Sigma0        float64 // evolution: 初始 mutation 幅度(盒子的比例)
	SigmaFloor    float64 // evolution: mutation 最小幅度
	WarmStart     []float64
	Workers       int // <=1 串行; >1 时并发评估一代
	Algorithm     string
}

// DefaultOptions 镜像 Python reference 与报告中写明的设置:
// budget 1000, mu 16, lambda 48, sigma0 0.25, sigmaFloor 0.03, 1 个 worker。
//
// Seed 为 0、BaselineScore 为 0(未设置)是刻意的: seed 是一次运行的
// 参数, 而 baseline 得分属于跑人类 baseline 的那一方。
// Algorithm 是主方法 "evolution"; 每个算法会给它写下的
// record 盖上自己的规范名, 所以一个设错的字段不会
// 给 registry record 贴错标签。
func DefaultOptions(spec config.Spec) Options {
	return Options{
		Spec:       spec,
		Seed:       0,
		Budget:     DefaultBudget,
		Mu:         DefaultMu,
		Lam:        DefaultLam,
		Sigma0:     DefaultSigma0,
		SigmaFloor: DefaultSigmaFloor,
		Workers:    DefaultWorkers,
		Algorithm:  AlgorithmEvolution,
	}
}

// Result 是一次算法运行的结果。
type Result struct {
	Algorithm    string             `json:"algorithm"`
	Seed         int                `json:"seed"`
	Budget       int                `json:"budget"`
	NEvals       int                `json:"n_evals"`
	BestScore    float64            `json:"best_score"`
	BestDesign   []float64          `json:"best_design"`
	BestTerms    map[string]float64 `json:"best_terms"`
	BestMetrics  physics.Metrics    `json:"best_metrics"`
	BestDesignID string             `json:"best_design_id"`
	BestFeasible bool               `json:"best_feasible"`
	EvalsToBeat  int                `json:"evals_to_beat"` // 从未超过 baseline 时为 -1
	History      []float64          `json:"history,omitempty"`
}

// Random: 均匀 i.i.d. 设计, 评估次数为 budget。
// 每次迭代: x = physics.RandomDesign(rng, spec), 其中
//
//	rng = rand.New(rand.NewSource(int64(seed)))
//
// (Go 的 RNG 流与 numpy 的不同是设计使然; 只有 physics 是
// 跨语言锚定的, 随机路径不是。)
func Random(sc runner.Scorer, opt Options) Result {
	rng := rand.New(rand.NewSource(int64(opt.Seed)))
	st := newRunState(sc, AlgorithmRandom, opt)
	batch := make([][]float64, 0, evalChunk)
	for i, n := 0, budgetOf(opt); i < n; i++ {
		batch = append(batch, SampleDesign(rng, opt.Spec))
		if len(batch) == evalChunk {
			st.evalAll(batch, 0, nil)
			batch = batch[:0]
		}
	}
	if len(batch) > 0 {
		st.evalAll(batch, 0, nil)
	}
	return st.result(opt)
}

// LHS: 每个维度每个分层一个设计; 分层顺序在每个维度上
// 由同一个带种子的 RNG 打乱, 然后映射进盒子并 canonicalise。
//
// 点 i 在维度 j 上使用分层 perm_j[i], 并在该分层内部
// 均匀采样; RNG 抽取按维度优先(先维度 j, 再全部 n
// 个分层), 所以流仅由 (seed, budget, spec) 决定。
func LHS(sc runner.Scorer, opt Options) Result {
	spec := opt.Spec
	rng := rand.New(rand.NewSource(int64(opt.Seed)))
	n := budgetOf(opt)
	d := spec.NParams()
	lo, hi := spec.Lower(), spec.Upper()

	xs := make([][]float64, n)
	for i := range xs {
		xs[i] = make([]float64, d)
	}
	for j := 0; j < d; j++ {
		perm := rng.Perm(n)
		width := (hi[j] - lo[j]) / float64(n)
		for i := 0; i < n; i++ {
			u := float64(perm[i]) + rng.Float64() // 每个(维度, 分层)抽一次
			xs[i][j] = lo[j] + u*width
		}
	}
	for i := range xs {
		xs[i] = Canonicalise(xs[i], spec)
	}

	st := newRunState(sc, AlgorithmLHS, opt)
	st.evalAll(xs, 0, nil)
	return st.result(opt)
}

// Evolution: 精英 (mu+lambda) 进化策略。
//
//   - 初始化 mu 个 parent(warm start 优先, 再补均匀随机设计)
//   - 每代: lambda 个 children, 每个是一个均匀随机 parent 的副本
//     加上 sigma * (upper-lower) * N(0,1), 裁剪到盒子, 然后 canonicalise
//   - sigma = max(SigmaFloor, Sigma0 * (1 - nEvals/budget))
//   - 选择: (parents + children) 中最好的 mu 个, 因此保证 elitism
//   - children 记录时会带上其 parent 的 design_id(lineage 边)
//
// budget 是精确的: nEvals 绝不超过 Budget。
func Evolution(sc runner.Scorer, opt Options) Result {
	return evolutionRun(sc, opt, AlgorithmEvolution, opt.WarmStart)
}

// EvolutionWarm 是 Evolution, 在没有提供 WarmStart 时
// opt.WarmStart 默认为 textbook mirror 设计。算法名: "evolution_warm"。
func EvolutionWarm(sc runner.Scorer, opt Options) Result {
	warm := opt.WarmStart
	if len(warm) == 0 {
		warm = WarmStartDesign(opt.Spec)
	}
	return evolutionRun(sc, opt, AlgorithmEvolutionWarm, warm)
}

// Methods 把方法名映射到其实现。
var Methods = map[string]func(runner.Scorer, Options) Result{
	AlgorithmRandom:        Random,
	AlgorithmLHS:           LHS,
	AlgorithmEvolution:     Evolution,
	AlgorithmEvolutionWarm: EvolutionWarm,
}

// MethodNames 返回已知方法名, 已排序(供 CLI 帮助与错误
// 消息使用)。附加辅助函数 —— 没有触碰任何冻结签名。
func MethodNames() []string {
	out := make([]string, 0, len(Methods))
	for name := range Methods {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Run 按名字分发, 遇到未知方法返回 error。
//
// 未知名字是硬错误, 绝不回退到默认算法:
// 一次运行不允许被记录在一个不存在的方法名下。
func Run(method string, sc runner.Scorer, opt Options) (Result, error) {
	fn, ok := Methods[method]
	if !ok {
		return Result{}, fmt.Errorf("search: unknown method %q (known: %s)", method, strings.Join(MethodNames(), ", "))
	}
	opt.Algorithm = method
	return fn(sc, opt), nil
}
