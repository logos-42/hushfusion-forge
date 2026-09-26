// 精英 (mu+lambda) 进化策略 —— Evolution 与
// EvolutionWarm(api.go)共用的主体。两者唯一的区别是
// warm-start 设计来自哪里。
//
// 确定性说明(为什么 Workers 不能改变 Result):
//
//   - 每个 child 都在调用方 goroutine 上、按固定顺序、在任何评估
//     发生之前抽出来;
//   - 结果按逻辑评估下标存储, 绝不按完成顺序;
//   - 选择是对 (parents ++ children) 按 score 降序做稳定排序,
//     因此并列时保持逻辑顺序, 与哪个 child 先算完无关;
//   - best-so-far 轨迹是按逻辑下标顺序折叠出来的。
package search

import (
	"math"
	"math/rand"
	"sort"

	"github.com/logos-42/hushfusion-forge/internal/runner"
)

// member 是当前种群中的一个成员。
type member struct {
	design []float64 // 交给 scorer 的 canonical 设计
	score  float64
	id     string // scorer 返回的 design_id(lineage 句柄)
}

// evolutionRun 是 Evolution 的主体, warm-start 设计与算法名
// 由外部传入。warm 为空时初始种群是纯 i.i.d. 随机。
func evolutionRun(sc runner.Scorer, opt Options, name string, warm []float64) Result {
	spec := opt.Spec
	budget := budgetOf(opt)
	mu := positiveInt(opt.Mu, DefaultMu)
	lam := positiveInt(opt.Lam, DefaultLam)
	sigma0 := positive(opt.Sigma0, DefaultSigma0)
	sigmaFloor := positive(opt.SigmaFloor, DefaultSigmaFloor)

	d := spec.NParams()
	lo, hi := spec.Lower(), spec.Upper()
	rng := rand.New(rand.NewSource(int64(opt.Seed)))
	st := newRunState(sc, name, opt)

	// --- 第 0 代: 初始种群(warm start 最先抽出) --------------------------
	nInit := min(mu, budget)
	xs := make([][]float64, 0, nInit)
	for i := 0; i < nInit; i++ {
		if i == 0 && len(warm) == d {
			xs = append(xs, Canonicalise(append([]float64(nil), warm...), spec))
			continue
		}
		xs = append(xs, SampleDesign(rng, spec))
	}
	st.evalAll(xs, 0, nil)

	pop := make([]member, 0, mu)
	for i := range xs {
		pop = append(pop, member{
			design: st.evaluatedDesign(i),
			score:  st.results[i].Score,
			id:     st.results[i].DesignID,
		})
	}

	// --- 第 1.. 代: 先 children, 再做 (mu+lambda) 精英选择 --------------
	for gen := 1; st.nEvals() < budget; gen++ {
		spent := st.nEvals()
		sigma := math.Max(sigmaFloor, sigma0*(1.0-float64(spent)/float64(budget)))
		k := min(lam, budget-spent)

		children := make([][]float64, 0, k)
		parents := make([]string, 0, k)
		for j := 0; j < k; j++ {
			p := rng.Intn(len(pop))
			child := make([]float64, d)
			for t := 0; t < d; t++ {
				step := sigma * (hi[t] - lo[t]) * rng.NormFloat64()
				child[t] = pop[p].design[t] + step
			}
			children = append(children, Canonicalise(child, spec))
			parents = append(parents, pop[p].id)
		}
		st.evalAll(children, gen, parents)

		first := st.nEvals() - len(children)
		cand := make([]member, 0, len(pop)+len(children))
		cand = append(cand, pop...)
		for j := range children {
			i := first + j
			cand = append(cand, member{
				design: st.evaluatedDesign(i),
				score:  st.results[i].Score,
				id:     st.results[i].DesignID,
			})
		}
		sort.SliceStable(cand, func(a, b int) bool { return cand[a].score > cand[b].score })
		if len(cand) > mu {
			cand = cand[:mu]
		}
		pop = cand
	}
	return st.result(opt)
}
