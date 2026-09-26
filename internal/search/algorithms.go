// api.go 里各搜索算法共用的机制。
//
// 有两条不变量在这里强制, 而不是在每个算法里:
//
//   - budget 是精确的: 只有当 budget 还剩槽位时
//     才会产出设计(见那些 min(...) 守卫), 所以恒有 NEvals == Budget;
//   - 结果绝不依赖 Options.Workers: 结果按逻辑评估
//     下标存储, RNG 只在调用方 goroutine 上推进,
//     选择排序在一个按逻辑排好序的候选切片上做稳定排序。
//     并发只可能改变 Scorer 记录的顺序, 这正是
//     api.go 中已文档化的例外。
package search

import (
	"math"
	"sync"
	"sync/atomic"

	"github.com/logos-42/hushfusion-forge/internal/objective"
	"github.com/logos-42/hushfusion-forge/internal/runner"
)

// evalChunk 是每个并发批次交给 scorer 的设计数量。
// 它是常量, 所以不可能让结果依赖 Workers。
const evalChunk = 512

// runState 按逻辑(EvalIndex)顺序累积一次运行的所有评估。
type runState struct {
	sc      runner.Scorer
	name    string
	seed    int
	workers int

	results []objective.EvalResult // 以 EvalIndex 为下标
	designs [][]float64            // 提交的设计, 以 EvalIndex 为下标
}

func newRunState(sc runner.Scorer, name string, opt Options) *runState {
	return &runState{
		sc:      sc,
		name:    name,
		seed:    opt.Seed,
		workers: opt.Workers,
		results: make([]objective.EvalResult, 0, max(opt.Budget, 0)),
		designs: make([][]float64, 0, max(opt.Budget, 0)),
	}
}

func (s *runState) nEvals() int { return len(s.results) }

// evalAll 按顺序以 evalChunk 为块给 xs 打分, 并按同样的逻辑顺序
// 追加结果。parents 可选: 非 nil 时 parents[i] 成为
// xs[i] 的 lineage 边。
func (s *runState) evalAll(xs [][]float64, gen int, parents []string) {
	for start := 0; start < len(xs); start += evalChunk {
		end := min(start+evalChunk, len(xs))
		metas := make([]runner.Meta, end-start)
		for i := start; i < end; i++ {
			parent := ""
			if parents != nil {
				parent = parents[i]
			}
			metas[i-start] = runner.Meta{
				Algorithm:  s.name,
				Seed:       s.seed,
				Generation: gen,
				EvalIndex:  s.nEvals() + (i - start),
				Parent:     parent,
			}
		}
		results := s.pass(xs[start:end], metas)
		s.results = append(s.results, results...)
		s.designs = append(s.designs, xs[start:end]...)
	}
}

// pass 评估一个批次。Workers <= 1 时内联执行; 更多 worker 会
// 从共享计数器取下标, 因此每个结果仍写在它自己的
// 逻辑槽位里, 结果与调度无关。
func (s *runState) pass(xs [][]float64, metas []runner.Meta) []objective.EvalResult {
	out := make([]objective.EvalResult, len(xs))
	if s.workers <= 1 || len(xs) <= 1 {
		for i := range xs {
			out[i] = s.sc.Score(xs[i], metas[i])
		}
		return out
	}
	var next atomic.Int64
	next.Store(-1)
	var wg sync.WaitGroup
	for w := 0; w < s.workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1))
				if i >= len(xs) {
					return
				}
				out[i] = s.sc.Score(xs[i], metas[i])
			}
		}()
	}
	wg.Wait()
	return out
}

// evaluatedDesign 是在 EvalIndex i 实际被评估的 canonical 向量:
// scorer 若回传了它就用回传的, 否则用提交的那个。
func (s *runState) evaluatedDesign(i int) []float64 {
	if d := s.results[i].Design; len(d) > 0 {
		return append([]float64(nil), d...)
	}
	return append([]float64(nil), s.designs[i]...)
}

// result 把评估折叠成冻结的 Result: best-so-far 轨迹、
// 最佳评估(并列时取最早, 因此是确定性的), 以及第一个
// 超过 baseline 的评估的下标。
func (s *runState) result(opt Options) Result {
	res := Result{
		Algorithm:   s.name,
		Seed:        s.seed,
		Budget:      opt.Budget,
		NEvals:      len(s.results),
		EvalsToBeat: -1,
		History:     make([]float64, 0, len(s.results)),
	}
	best := -1
	run := math.Inf(-1)
	for i := range s.results {
		r := &s.results[i]
		if best < 0 || r.Score > s.results[best].Score {
			best = i
		}
		if r.Score > run {
			run = r.Score
		}
		res.History = append(res.History, run)
		if res.EvalsToBeat < 0 && r.Score > opt.BaselineScore {
			res.EvalsToBeat = i
		}
	}
	if best >= 0 {
		b := &s.results[best]
		res.BestScore = b.Score
		res.BestTerms = b.Terms
		res.BestMetrics = b.Metrics
		res.BestDesignID = b.DesignID
		res.BestFeasible = b.Feasible
		res.BestDesign = s.evaluatedDesign(best)
	}
	return res
}

func budgetOf(opt Options) int { return max(opt.Budget, 0) }

// positive 给未设置(<= 0)的 evolution 旋钮套用文档默认值,
// 使零值的 Options 表现得像 DefaultOptions。
func positive(v, def float64) float64 {
	if v <= 0 {
		return def
	}
	return v
}

func positiveInt(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}
