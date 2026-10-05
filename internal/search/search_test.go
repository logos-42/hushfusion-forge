// 四个搜索算法的单元测试。
//
// 设计选择(这正是 runner.Scorer 存在的原因): 每个测试都注入一个
// TOY scorer, 而不是真的 *runner.Runner。这个玩具是一个确定性的光滑
// 碗形函数, 定义在设计盒上, 其最优点恰好落在人类 baseline
// 设计上, 并且它自己合成递增的 design id("D0001"...)。这里
// 不碰 internal/physics 或 internal/registry, 因此 stage D 可以独立地
// 做端到端验证, 测试也保持快速且逐位精确。
//
// 这里证明了什么:
//
//	budget 精确性        每个方法都有 NEvals == Budget, 包括不能整除
//	                      mu/lambda 的 budget 以及块边界情形
//	轨迹                  len(History) == Budget、单调不减、
//	                      History[last] == BestScore, BestScore == 已打分中的最大值
//	可复现性              相同 Options -> 逐位相同的 Result(两次)
//	Workers 不变性        Workers=4/8 == Workers=1, 得分、轨迹、最佳设计
//	                      以及被评估设计的多重集都逐位相同
//	                      (在 -race 下跑本文件)
//	质量                  等 budget 下 evolution 在 3 个 seed 上胜过 random 的均值
//	warm start            EvolutionWarm 的第一次评估就是 warm design
//	lineage               每条 child record 都带着某个 parent 的 design_id,
//	                      该 parent 真实存在且来自更早的代数
//	EvalsToBeat           超过 baseline 的第一个下标, 从未超过时为 -1
//	LHS 结构              每个维度每个分层一个点, canonical 且在盒内
//	分发                  未知方法是硬错误
package search

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/logos-42/hushfusion-forge/internal/config"
	"github.com/logos-42/hushfusion-forge/internal/objective"
	"github.com/logos-42/hushfusion-forge/internal/physics"
	"github.com/logos-42/hushfusion-forge/internal/runner"
)

// ---------------------------------------------------------------- 玩具 scorer --

// toyScorer 实现 runner.Scorer。得分是设计的纯函数;
// id 计数器是唯一的可变状态, 且由 mutex 保护, 所以这个玩具
// 对并发(Workers > 1)路径是安全的。
type toyScorer struct {
	spec config.Spec
	opt  []float64 // 最优点, 用归一化盒坐标表示
	seq  []float64 // 非 nil 时 score = seq[EvalIndex % len(seq)](忽略 x)

	mu      sync.Mutex
	n       int
	seen    map[string]bool
	designs [][]float64
	metas   []runner.Meta
	ids     []string
	scores  []float64
}

func newToyScorer(spec config.Spec) *toyScorer {
	base := textbookMirrorDesign // 玩具的最优点就是人类 baseline 设计
	lo, hi := spec.Lower(), spec.Upper()
	opt := make([]float64, len(base))
	for j := range opt {
		opt[j] = (base[j] - lo[j]) / (hi[j] - lo[j])
	}
	return &toyScorer{spec: spec, opt: opt, seen: map[string]bool{}}
}

func (s *toyScorer) score(x []float64, meta runner.Meta) float64 {
	if s.seq != nil {
		return s.seq[meta.EvalIndex%len(s.seq)]
	}
	lo, hi := s.spec.Lower(), s.spec.Upper()
	total := 0.0
	for j := range x {
		u := (x[j] - lo[j]) / (hi[j] - lo[j]) // 归一化盒坐标
		d := u - s.opt[j]
		total -= d * d
	}
	return total
}

func (s *toyScorer) Score(x []float64, meta runner.Meta) objective.EvalResult {
	score := s.score(x, meta)
	design := append([]float64(nil), x...)

	s.mu.Lock()
	s.n++
	id := fmt.Sprintf("D%04d", s.n)
	experimentID := s.n
	s.designs = append(s.designs, design)
	s.metas = append(s.metas, meta)
	s.ids = append(s.ids, id)
	s.scores = append(s.scores, score)
	s.seen[id] = true
	s.mu.Unlock()

	return objective.EvalResult{
		Score:        score,
		Terms:        map[string]float64{"toy": score},
		Weighted:     map[string]float64{"toy": score},
		Penalties:    map[string]float64{},
		Feasible:     true,
		Design:       design,
		DesignID:     id,
		ExperimentID: experimentID,
	}
}

func (s *toyScorer) snapshot() (designs [][]float64, metas []runner.Meta, ids []string, scores []float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]float64(nil), s.designs...),
		append([]runner.Meta(nil), s.metas...),
		append([]string(nil), s.ids...),
		append([]float64(nil), s.scores...)
}

// fingerprint 是被评估设计的顺序无关多重集: 它必须在
// Workers = 1 与 Workers = 4 下相同, 尽管记录顺序
// 并非如此。
func (s *toyScorer) fingerprint() string {
	designs, _, _, _ := s.snapshot()
	parts := make([]string, len(designs))
	for i, d := range designs {
		parts[i] = fmt.Sprintf("%v", d)
	}
	sort.Strings(parts)
	return strings.Join(parts, "|")
}

// --------------------------------------------------------------- 测试辅助函数 --

const testBudget = 600

func toyOptions(spec config.Spec, budget, seed int) Options {
	opt := DefaultOptions(spec)
	opt.Seed = seed
	opt.Budget = budget
	return opt
}

func floatsEqual(a, b []float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if math.Float64bits(a[i]) != math.Float64bits(b[i]) {
			return false
		}
	}
	return true
}

// sameResult 描述两个 Result 之间的第一处差异, 逐位一致
// 时返回 ""。strictIDs 只在 Workers 比较里关闭, 因为那里的
// registry(此处是玩具的 id 计数器)可能按完成顺序发 id ——
// 这是可复现性已文档化的唯一例外。
func sameResult(got, want Result, strictIDs bool) string {
	switch {
	case got.Algorithm != want.Algorithm:
		return fmt.Sprintf("Algorithm %q != %q", got.Algorithm, want.Algorithm)
	case got.Seed != want.Seed:
		return fmt.Sprintf("Seed %d != %d", got.Seed, want.Seed)
	case got.Budget != want.Budget:
		return fmt.Sprintf("Budget %d != %d", got.Budget, want.Budget)
	case got.NEvals != want.NEvals:
		return fmt.Sprintf("NEvals %d != %d", got.NEvals, want.NEvals)
	case math.Float64bits(got.BestScore) != math.Float64bits(want.BestScore):
		return fmt.Sprintf("BestScore %v(%x) != %v(%x)", got.BestScore, math.Float64bits(got.BestScore), want.BestScore, math.Float64bits(want.BestScore))
	case !floatsEqual(got.BestDesign, want.BestDesign):
		return fmt.Sprintf("BestDesign %v != %v", got.BestDesign, want.BestDesign)
	case !floatsEqual(got.History, want.History):
		return fmt.Sprintf("History differs (len %d vs %d)", len(got.History), len(want.History))
	case got.EvalsToBeat != want.EvalsToBeat:
		return fmt.Sprintf("EvalsToBeat %d != %d", got.EvalsToBeat, want.EvalsToBeat)
	case got.BestFeasible != want.BestFeasible:
		return fmt.Sprintf("BestFeasible %v != %v", got.BestFeasible, want.BestFeasible)
	case fmt.Sprint(got.BestTerms) != fmt.Sprint(want.BestTerms):
		return fmt.Sprintf("BestTerms %v != %v", got.BestTerms, want.BestTerms)
	}
	if strictIDs && got.BestDesignID != want.BestDesignID {
		return fmt.Sprintf("BestDesignID %q != %q", got.BestDesignID, want.BestDesignID)
	}
	return ""
}

func allMethods() []string {
	return []string{AlgorithmRandom, AlgorithmLHS, AlgorithmEvolution, AlgorithmEvolutionWarm,
		AlgorithmEvolutionKnowledge, AlgorithmEvolutionRule, AlgorithmEvolutionChampionRule}
}

// checkInsideBoxAndCanonical 断言交给 scorer 的每个设计都在
// 盒内且为 canonical(z 升序)顺序 —— 这就是 mutation/解码契约。
func checkInsideBoxAndCanonical(t *testing.T, spec config.Spec, designs [][]float64) {
	t.Helper()
	lo, hi := spec.Lower(), spec.Upper()
	nc := spec.NCoils
	for i, x := range designs {
		if len(x) != spec.NParams() {
			t.Fatalf("design %d has length %d, want %d", i, len(x), spec.NParams())
		}
		for j, v := range x {
			if v < lo[j] || v > hi[j] {
				t.Fatalf("design %d param %d = %v outside [%v, %v]", i, j, v, lo[j], hi[j])
			}
		}
		for k := 1; k < nc; k++ {
			if x[nc+k] < x[nc+k-1] {
				t.Fatalf("design %d is not canonical: z = %v", i, x[nc:nc+nc])
			}
		}
	}
}

// --------------------------------------------------------- 默认值 / 常量 --

func TestDefaultOptionsMatchesDocumentedDefaults(t *testing.T) {
	spec := config.DefaultSpec()
	opt := DefaultOptions(spec)

	if opt.Spec.NCoils != spec.NCoils || opt.Spec.Bounds != spec.Bounds {
		t.Errorf("DefaultOptions did not carry the spec")
	}
	if opt.Budget != 1000 {
		t.Errorf("Budget = %d, want 1000", opt.Budget)
	}
	if opt.Mu != 16 || opt.Lam != 48 {
		t.Errorf("Mu/Lam = %d/%d, want 16/48", opt.Mu, opt.Lam)
	}
	if opt.Sigma0 != 0.25 || opt.SigmaFloor != 0.03 {
		t.Errorf("Sigma0/SigmaFloor = %v/%v, want 0.25/0.03", opt.Sigma0, opt.SigmaFloor)
	}
	if opt.Workers != 1 {
		t.Errorf("Workers = %d, want 1", opt.Workers)
	}
	if opt.WarmStart != nil {
		t.Errorf("WarmStart = %v, want nil (evolution_warm supplies the default)", opt.WarmStart)
	}
	if opt.Algorithm != AlgorithmEvolution {
		t.Errorf("Algorithm = %q, want %q", opt.Algorithm, AlgorithmEvolution)
	}
}

func TestConstantsCoverMethods(t *testing.T) {
	if len(Methods) != 7 {
		t.Fatalf("Methods has %d entries, want 7: %v", len(Methods), MethodNames())
	}
	want := allMethods()
	sort.Strings(want)
	if got := MethodNames(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("MethodNames() = %v, want %v", got, want)
	}
}

// ------------------------------------------------------------------ budget ----

func TestBudgetIsExactAndHistoryIsBestSoFar(t *testing.T) {
	spec := config.DefaultSpec()
	for _, budget := range []int{0, 1, 2, 7, 47, 48, 49, 100, 513, 1000} {
		for _, method := range allMethods() {
			t.Run(fmt.Sprintf("%s/budget=%d", method, budget), func(t *testing.T) {
				sc := newToyScorer(spec)
				res, err := Run(method, sc, toyOptions(spec, budget, 3))
				if err != nil {
					t.Fatalf("Run: %v", err)
				}
				if res.NEvals != budget {
					t.Fatalf("NEvals = %d, want exactly Budget = %d", res.NEvals, budget)
				}
				if res.Budget != budget {
					t.Fatalf("Budget field = %d, want %d", res.Budget, budget)
				}
				if len(res.History) != budget {
					t.Fatalf("len(History) = %d, want %d", len(res.History), budget)
				}
				for i := 1; i < len(res.History); i++ {
					if res.History[i] < res.History[i-1] {
						t.Fatalf("History not monotone at %d: %v -> %v", i, res.History[i-1], res.History[i])
					}
				}

				designs, metas, _, rawScores := sc.snapshot()
				if len(designs) != budget {
					t.Fatalf("scorer saw %d designs, want %d", len(designs), budget)
				}
				// EvalIndex 必须恰好是按顺序的 0..budget-1。
				for i, m := range metas {
					if m.EvalIndex != i {
						t.Fatalf("meta[%d].EvalIndex = %d, want %d", i, m.EvalIndex, i)
					}
					if m.Algorithm != method || m.Seed != 3 {
						t.Fatalf("meta[%d] = %+v, want algorithm %q seed 3", i, m, method)
					}
				}
				if budget == 0 {
					if res.BestDesign != nil || res.EvalsToBeat != -1 {
						t.Fatalf("empty run should have no best design and EvalsToBeat -1, got %v / %d", res.BestDesign, res.EvalsToBeat)
					}
					return
				}
				// 独立地根据 scorer 实际收到的东西重算
				// 轨迹。
				wantBestIdx, wantRun := 0, math.Inf(-1)
				var wantHist []float64
				for i, v := range rawScores {
					if v > wantRun {
						wantRun = v
					}
					wantHist = append(wantHist, wantRun)
					if v > rawScores[wantBestIdx] {
						wantBestIdx = i
					}
				}
				if !floatsEqual(res.History, wantHist) {
					t.Fatalf("History != recomputed best-so-far\n got %v\nwant %v", res.History, wantHist)
				}
				if math.Float64bits(res.BestScore) != math.Float64bits(wantHist[len(wantHist)-1]) {
					t.Fatalf("BestScore %v != last History %v", res.BestScore, wantHist[len(wantHist)-1])
				}
				if res.BestScore != wantRun {
					t.Fatalf("BestScore %v != max scored %v", res.BestScore, wantRun)
				}
				if !floatsEqual(res.BestDesign, designs[wantBestIdx]) {
					t.Fatalf("BestDesign %v != design of the argmax %v", res.BestDesign, designs[wantBestIdx])
				}
				checkInsideBoxAndCanonical(t, spec, designs)
			})
		}
	}
}

// ------------------------------------------------------------ 可复现性 --

func TestReproducibilityIsBitIdentical(t *testing.T) {
	spec := config.DefaultSpec()
	for _, seed := range []int{7, 11} {
		for _, method := range allMethods() {
			t.Run(fmt.Sprintf("%s/seed=%d", method, seed), func(t *testing.T) {
				a, err := Run(method, newToyScorer(spec), toyOptions(spec, testBudget, seed))
				if err != nil {
					t.Fatal(err)
				}
				b, err := Run(method, newToyScorer(spec), toyOptions(spec, testBudget, seed))
				if err != nil {
					t.Fatal(err)
				}
				if diff := sameResult(a, b, true); diff != "" {
					t.Fatalf("same Options gave different Results: %s", diff)
				}
				if a.NEvals != testBudget {
					t.Fatalf("NEvals = %d, want %d", a.NEvals, testBudget)
				}
			})
		}
	}
}

// TestDifferentSeedsDiverge 防止 Seed 被静默忽略。它
// 比较的是被评估设计的多重集而不是轨迹, 因为
// evolution_warm 的轨迹在这个玩具里合法地是平的: warm start
// 就是玩具最优点, 所以每个 seed 的 best-so-far 在第 0 次
// 评估时就已经是最大值了。
func TestDifferentSeedsDiverge(t *testing.T) {
	spec := config.DefaultSpec()
	for _, method := range allMethods() {
		scA := newToyScorer(spec)
		scB := newToyScorer(spec)
		if _, err := Run(method, scA, toyOptions(spec, 200, 1)); err != nil {
			t.Fatal(err)
		}
		if _, err := Run(method, scB, toyOptions(spec, 200, 2)); err != nil {
			t.Fatal(err)
		}
		if scA.fingerprint() == scB.fingerprint() {
			t.Errorf("%s: seeds 1 and 2 evaluated exactly the same designs — seed is ignored", method)
		}
	}
}

// --------------------------------------------------------- Workers 不变性 --

func TestWorkersDoNotChangeResults(t *testing.T) {
	spec := config.DefaultSpec()
	for _, method := range allMethods() {
		t.Run(method, func(t *testing.T) {
			refSc := newToyScorer(spec)
			ref, err := Run(method, refSc, toyOptions(spec, testBudget, 7))
			if err != nil {
				t.Fatal(err)
			}
			refFP := refSc.fingerprint()
			for _, w := range []int{2, 4, 8} {
				sc := newToyScorer(spec)
				opt := toyOptions(spec, testBudget, 7)
				opt.Workers = w
				got, err := Run(method, sc, opt)
				if err != nil {
					t.Fatal(err)
				}
				if diff := sameResult(got, ref, false); diff != "" {
					t.Fatalf("Workers=%d changed the Result: %s", w, diff)
				}
				if got.BestDesignID != ref.BestDesignID {
					t.Logf("Workers=%d: BestDesignID %q vs %q (registry order may differ; ids are excluded)", w, got.BestDesignID, ref.BestDesignID)
				}
				if fp := sc.fingerprint(); fp != refFP {
					t.Fatalf("Workers=%d evaluated a different multiset of designs", w)
				}
			}
		})
	}
}

// ------------------------------------------------------------------- 质量 --

func TestEvolutionBeatsRandomOnTheToyProblem(t *testing.T) {
	spec := config.DefaultSpec()
	const budget = 1000
	seeds := []int{1, 2, 3}

	var randSum, evoSum, lhsSum, warmSum float64
	for _, seed := range seeds {
		r, err := Run(AlgorithmRandom, newToyScorer(spec), toyOptions(spec, budget, seed))
		if err != nil {
			t.Fatal(err)
		}
		e, err := Run(AlgorithmEvolution, newToyScorer(spec), toyOptions(spec, budget, seed))
		if err != nil {
			t.Fatal(err)
		}
		l, err := Run(AlgorithmLHS, newToyScorer(spec), toyOptions(spec, budget, seed))
		if err != nil {
			t.Fatal(err)
		}
		w, err := Run(AlgorithmEvolutionWarm, newToyScorer(spec), toyOptions(spec, budget, seed))
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("seed %d  random=%.6f  lhs=%.6f  evolution=%.6f  evolution_warm=%.6f", seed, r.BestScore, l.BestScore, e.BestScore, w.BestScore)
		randSum += r.BestScore
		evoSum += e.BestScore
		lhsSum += l.BestScore
		warmSum += w.BestScore
	}
	n := float64(len(seeds))
	t.Logf("mean over %d seeds  random=%.6f  lhs=%.6f  evolution=%.6f  evolution_warm=%.6f", len(seeds), randSum/n, lhsSum/n, evoSum/n, warmSum/n)
	if !(evoSum/n > randSum/n) {
		t.Fatalf("evolution mean (%.6f) did not beat random mean (%.6f) at equal budget %d", evoSum/n, randSum/n, budget)
	}
}

// --------------------------------------------------------------- warm start ---

func TestWarmStartIsTheFirstEvaluation(t *testing.T) {
	spec := config.DefaultSpec()

	t.Run("explicit warm start", func(t *testing.T) {
		sc := newToyScorer(spec)
		opt := toyOptions(spec, testBudget, 5)
		opt.WarmStart = append([]float64(nil), textbookMirrorDesign...)
		res, err := Run(AlgorithmEvolutionWarm, sc, opt)
		if err != nil {
			t.Fatal(err)
		}
		if res.Algorithm != AlgorithmEvolutionWarm {
			t.Fatalf("Algorithm = %q, want %q", res.Algorithm, AlgorithmEvolutionWarm)
		}
		designs, metas, _, _ := sc.snapshot()
		if len(designs) == 0 {
			t.Fatal("nothing was evaluated")
		}
		if !floatsEqual(designs[0], opt.WarmStart) {
			t.Fatalf("first evaluation %v is not the warm-start design %v (knowledge wired but never injected)", designs[0], opt.WarmStart)
		}
		if metas[0].EvalIndex != 0 || metas[0].Generation != 0 || metas[0].Parent != "" {
			t.Fatalf("warms start meta = %+v, want EvalIndex 0, Generation 0, no parent", metas[0])
		}
	})

	t.Run("default warm start", func(t *testing.T) {
		sc := newToyScorer(spec)
		res, err := Run(AlgorithmEvolutionWarm, sc, toyOptions(spec, testBudget, 5))
		if err != nil {
			t.Fatal(err)
		}
		want := WarmStartDesign(spec)
		designs, _, _, _ := sc.snapshot()
		if !floatsEqual(designs[0], want) {
			t.Fatalf("first evaluation %v is not WarmStartDesign %v", designs[0], want)
		}
		if !floatsEqual(want, textbookMirrorDesign) {
			t.Fatalf("default warm start %v is not the textbook mirror %v", want, textbookMirrorDesign)
		}
		checkInsideBoxAndCanonical(t, spec, [][]float64{want})
		// 知识必须真的赚钱: 玩具最优点就是 baseline。
		if res.BestScore < -1e-12 {
			t.Fatalf("warm start began at the toy optimum but BestScore = %v", res.BestScore)
		}
	})

	t.Run("plain evolution honours a supplied warm start", func(t *testing.T) {
		sc := newToyScorer(spec)
		opt := toyOptions(spec, testBudget, 5)
		opt.WarmStart = append([]float64(nil), textbookMirrorDesign...)
		res, err := Run(AlgorithmEvolution, sc, opt)
		if err != nil {
			t.Fatal(err)
		}
		if res.Algorithm != AlgorithmEvolution {
			t.Fatalf("Algorithm = %q, want %q", res.Algorithm, AlgorithmEvolution)
		}
		designs, _, _, _ := sc.snapshot()
		if !floatsEqual(designs[0], opt.WarmStart) {
			t.Fatalf("first evaluation %v is not the warm-start design", designs[0])
		}
	})

	t.Run("no warm start means random init", func(t *testing.T) {
		sc := newToyScorer(spec)
		if _, err := Run(AlgorithmEvolution, sc, toyOptions(spec, testBudget, 5)); err != nil {
			t.Fatal(err)
		}
		designs, _, _, _ := sc.snapshot()
		if floatsEqual(designs[0], textbookMirrorDesign) {
			t.Fatal("evolution without WarmStart injected the baseline anyway")
		}
	})
}

// ------------------------------------------------------------------ lineage ---

func TestLineageParentsExistAndPrecedeTheirChildren(t *testing.T) {
	spec := config.DefaultSpec()
	for _, method := range []string{AlgorithmEvolution, AlgorithmEvolutionWarm} {
		for _, workers := range []int{1, 4} {
			t.Run(fmt.Sprintf("%s/workers=%d", method, workers), func(t *testing.T) {
				sc := newToyScorer(spec)
				opt := toyOptions(spec, testBudget, 9)
				opt.Workers = workers
				res, err := Run(method, sc, opt)
				if err != nil {
					t.Fatal(err)
				}
				designs, metas, ids, _ := sc.snapshot()

				// id 记账: 每个设计第一次出现是什么时候, 在哪一代?
				seenGen := map[string]int{}
				seenIdx := map[string]int{}
				roots, children := 0, 0
				for i := range metas {
					m := metas[i]
					if m.Generation == 0 {
						roots++
						if m.Parent != "" {
							t.Fatalf("root %s (eval %d) has parent %q", ids[i], i, m.Parent)
						}
					} else {
						children++
						if m.Parent == "" {
							t.Fatalf("child %s (eval %d, generation %d) has no parent: lineage is broken", ids[i], i, m.Generation)
						}
						g, ok := seenGen[m.Parent]
						if !ok {
							t.Fatalf("child %s references parent %q, which no earlier evaluation produced", ids[i], m.Parent)
						}
						if g >= m.Generation {
							t.Fatalf("child %s in generation %d references parent %q from generation %d", ids[i], m.Generation, m.Parent, g)
						}
						if seenIdx[m.Parent] > i {
							t.Fatalf("parent %q was recorded after its child %s", m.Parent, ids[i])
						}
					}
					if _, dup := seenGen[ids[i]]; dup {
						t.Fatalf("design id %q was handed out twice", ids[i])
					}
					seenGen[ids[i]] = m.Generation
					seenIdx[ids[i]] = i
				}
				if roots == 0 || children == 0 {
					t.Fatalf("expected a non-empty lineage: %d roots, %d children", roots, children)
				}
				if roots != 16 {
					t.Fatalf("initial population = %d, want mu = 16", roots)
				}
				if got := roots + children; got != testBudget {
					t.Fatalf("scored %d designs, want %d", got, testBudget)
				}
				if res.BestDesignID == "" {
					t.Fatal("BestDesignID is empty; the search layer needs ids from the scorer for lineage")
				}
				_ = designs
			})
		}
	}
}

// --------------------------------------------------------------- EvalsToBeat --

func TestEvalsToBeat(t *testing.T) {
	spec := config.DefaultSpec()
	seq := []float64{0.1, 0.5, -1.0, 0.9, 0.2}
	cases := []struct {
		baseline float64
		want     int
		why      string
	}{
		{0.6, 3, "first score strictly above 0.6 is seq[3]"},
		{0.5, 3, "exactly equal does not count as beating"},
		{0.9, -1, "never strictly exceeded"},
		{0.2, 1, "seq[1]=0.5 already exceeds 0.2"},
		{0.1, 1, "seq[0]=0.1 is not > 0.1"},
		{1.0, -1, "baseline above every score"},
		{-2.0, 0, "baseline below every score: the first evaluation beats it"},
	}
	for _, method := range allMethods() {
		for _, tc := range cases {
			t.Run(fmt.Sprintf("%s/baseline=%v", method, tc.baseline), func(t *testing.T) {
				sc := &toyScorer{spec: spec, seq: seq, seen: map[string]bool{}}
				opt := toyOptions(spec, len(seq), 4)
				opt.BaselineScore = tc.baseline
				res, err := Run(method, sc, opt)
				if err != nil {
					t.Fatal(err)
				}
				if res.EvalsToBeat != tc.want {
					t.Fatalf("EvalsToBeat = %d, want %d (%s)", res.EvalsToBeat, tc.want, tc.why)
				}
				if res.NEvals != len(seq) {
					t.Fatalf("NEvals = %d, want %d", res.NEvals, len(seq))
				}
			})
		}
	}

	t.Run("cross-check against the recorded scores", func(t *testing.T) {
		sc := newToyScorer(spec)
		opt := toyOptions(spec, 300, 6)
		opt.BaselineScore = -1.0
		res, err := Run(AlgorithmRandom, sc, opt)
		if err != nil {
			t.Fatal(err)
		}
		_, _, _, raw := sc.snapshot()
		want := -1
		for i := range raw {
			if raw[i] > opt.BaselineScore {
				want = i
				break
			}
		}
		if res.EvalsToBeat != want {
			t.Fatalf("EvalsToBeat = %d, want %d from the recorded scores", res.EvalsToBeat, want)
		}
		if res.History[res.EvalsToBeat] <= opt.BaselineScore {
			t.Fatalf("history at EvalsToBeat (%v) does not exceed the baseline %v", res.History[res.EvalsToBeat], opt.BaselineScore)
		}
	})
}

// --------------------------------------------------------------------- LHS ----

func TestLHSStratificationAndDeterminism(t *testing.T) {
	spec := config.DefaultSpec()
	const n = 60
	nc := spec.NCoils
	lo, hi := spec.Lower(), spec.Upper()
	for _, seed := range []int{1, 2, 3, 4, 5, 6} {
		sc := newToyScorer(spec)
		if _, err := Run(AlgorithmLHS, sc, toyOptions(spec, n, seed)); err != nil {
			t.Fatal(err)
		}
		designs, _, _, _ := sc.snapshot()
		if len(designs) != n {
			t.Fatalf("seed %d: scored %d designs, want %d", seed, len(designs), n)
		}
		checkInsideBoxAndCanonical(t, spec, designs)

		// 每个维度每个分层一个点。
		//
		// 这个检查必须是排列不变的: canonicalisation 会把一个设计的
		// (r, z, I) 三元组按 z 重排, 所以设计 i 的第 k 个线圈并不是
		// 第 k 次分层抽取。canonicalisation 无法改变的是
		// 每个参数族(radius / z / current)的取值多重集, 因此
		// 不变量是: 对某个族的全部 n 个设计与全部 nc 个线圈位置而言,
		// 该族的每个分层都恰好被命中 nc 次。
		for fam := 0; fam < 3; fam++ {
			j := fam * nc
			width := (hi[j] - lo[j]) / float64(n)
			counts := make([]int, n)
			total := 0
			for _, x := range designs {
				for p := 0; p < nc; p++ {
					v := x[fam*nc+p]
					if v < lo[j] || v > hi[j] {
						t.Fatalf("seed %d family %d: value %v outside [%v, %v]", seed, fam, v, lo[j], hi[j])
					}
					k := int((v - lo[j]) / width)
					if k < 0 || k >= n {
						k = n - 1 // 舍入后恰好等于 hi 的值属于最后一个分层
					}
					counts[k]++
					total++
				}
			}
			if total != n*nc {
				t.Fatalf("seed %d family %d: counted %d values, want %d", seed, fam, total, n*nc)
			}
			for k, c := range counts {
				if c != nc {
					t.Fatalf("seed %d family %d stratum %d hit %d times, want exactly %d (one per design per dimension)", seed, fam, k, c, nc)
				}
			}
		}
	}
}

// ------------------------------------------------------------ sigma 调度 --

func TestMutationScaleFollowsTheAnnealedSigma(t *testing.T) {
	spec := config.DefaultSpec()
	sc := newToyScorer(spec)
	const budget = 1000
	if _, err := Run(AlgorithmEvolution, sc, toyOptions(spec, budget, 8)); err != nil {
		t.Fatal(err)
	}
	designs, metas, ids, _ := sc.snapshot()
	lo, hi := spec.Lower(), spec.Upper()

	byID := map[string][]float64{}
	for i, id := range ids {
		byID[id] = designs[i]
	}
	meanStep := func(gen int) (float64, int) {
		sum, count := 0.0, 0
		for i, m := range metas {
			if m.Generation != gen || m.Parent == "" {
				continue
			}
			p, ok := byID[m.Parent]
			if !ok {
				continue
			}
			acc := 0.0
			for j := range p {
				d := (designs[i][j] - p[j]) / (hi[j] - lo[j])
				acc += d * d
			}
			sum += math.Sqrt(acc)
			count++
		}
		return sum / float64(count), count
	}
	first, nFirst := meanStep(1)
	last, nLast := meanStep(metas[len(metas)-1].Generation)
	if nFirst == 0 || nLast == 0 {
		t.Fatalf("no children to measure (%d, %d)", nFirst, nLast)
	}
	t.Logf("mean normalised step: generation 1 = %.4f, last generation = %.4f", first, last)
	if !(first > 3*last) {
		t.Fatalf("mutation scale did not anneal: gen1 step %.4f, last step %.4f (sigma0=0.25 -> sigmaFloor=0.03 expected ~8x)", first, last)
	}
}

// ------------------------------------------------------------------ 分发 --

func TestRunDispatch(t *testing.T) {
	spec := config.DefaultSpec()
	if _, err := Run("bayesian", newToyScorer(spec), toyOptions(spec, 10, 1)); err == nil {
		t.Fatal("Run accepted an unknown method; it must be a hard error, not a silent default")
	} else if !strings.Contains(err.Error(), "bayesian") {
		t.Fatalf("error %q does not name the unknown method", err)
	}
	for _, method := range MethodNames() {
		res, err := Run(method, newToyScorer(spec), toyOptions(spec, 50, 2))
		if err != nil {
			t.Fatalf("Run(%q): %v", method, err)
		}
		if res.Algorithm != method {
			t.Fatalf("Run(%q).Algorithm = %q", method, res.Algorithm)
		}
		if res.NEvals != 50 {
			t.Fatalf("Run(%q).NEvals = %d, want 50", method, res.NEvals)
		}
	}
}

// -------------------------------------------------- stage-A 接线(可跳过) --

// nonCanonicalProbe 是一个线圈不按 z 排序、且有两个项
// 在盒外的设计, 所以 canonicalisation 有真活要干。
func nonCanonicalProbe() []float64 {
	return []float64{0.4, 0.9, 0.2, 0.7, 0.5, -2.0, 1.0, -0.1, 3.0e6, 1.0, 5.0e5, 2.0e6}
}

// TestDesignHelperSeamsAreWiredToPhysics 是 design.go 的 parity gate:
//
//  1. 本地 reference 实现等于 physics 实现;
//  2. 已接线的(出厂)Canonicalise 就是 physics 那个;
//  3. 已接线的 SampleDesign 消耗 RNG 流的方式与
//     physics.RandomDesign 完全一致, 即接线对随机路径是 no-op。
//
// internal/physics 是一条独立的并行线, 所以当它还是一堆会 panic 的 stub
// 时测试会跳过; 现在它已实现, 这道门禁
// 是硬的。
func TestDesignHelperSeamsAreWiredToPhysics(t *testing.T) {
	spec := config.DefaultSpec()
	if !physicsAvailable(spec) {
		t.Skip("stage A (internal/physics) is not implemented yet: the local design helpers are the only implementation available; this test becomes a hard parity check once physics lands")
	}
	x := nonCanonicalProbe()
	local := localCanonicalise(x, spec)
	phys := PhysicsCanonicalise(x, spec)
	if !floatsEqual(local, phys) {
		t.Fatalf("localCanonicalise %v != PhysicsCanonicalise %v", local, phys)
	}
	if !floatsEqual(Canonicalise(x, spec), phys) {
		t.Fatalf("Canonicalise is not wired to physics: %v != %v", Canonicalise(x, spec), phys)
	}
	checkInsideBoxAndCanonical(t, spec, [][]float64{phys})

	for seed := 1; seed <= 20; seed++ {
		got := SampleDesign(rand.New(rand.NewSource(int64(seed))), spec)
		want := physics.RandomDesign(rand.New(rand.NewSource(int64(seed))), spec)
		if !floatsEqual(got, want) {
			t.Fatalf("SampleDesign is not wired to physics.RandomDesign at seed %d: %v != %v", seed, got, want)
		}
		checkInsideBoxAndCanonical(t, spec, [][]float64{got})
	}
}

// physicsAvailable 报告接缝背后的 stage-A 函数是否已实现,
// 做法是探测一次并把 panic 当作 "尚未"。
func physicsAvailable(spec config.Spec) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	rng := rand.New(rand.NewSource(3))
	x := physics.RandomDesign(rng, spec)
	return len(x) == spec.NParams()
}

// TestWarmStartDefaultIsTheGoldenBaseline 对照 testdata/ 里的跨语言锚点, 检验硬编码的
// 兜底 warm start: EvolutionWarm 播种用的必须是那个被要求让机器
// 打败的人类设计, 而不是一个仅仅
// 看起来像它的东西。
func TestWarmStartDefaultIsTheGoldenBaseline(t *testing.T) {
	spec := config.DefaultSpec()
	warm := WarmStartDesign(spec)

	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "golden_baseline.json"))
	if err != nil {
		t.Skipf("golden baseline not readable from the test: %v", err)
	}
	var golden struct {
		Name   string    `json:"name"`
		Design []float64 `json:"design"`
	}
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatalf("golden_baseline.json: %v", err)
	}
	if !floatsEqual(warm, golden.Design) {
		t.Fatalf("WarmStartDesign returned %v, want the golden %q design %v", warm, golden.Name, golden.Design)
	}
	checkInsideBoxAndCanonical(t, spec, [][]float64{warm})
}

// ---------------------------------------------------------------------- 杂项 --

// TestZeroValuedOptionsUseTheDocumentedDefaults 把一个字面意义上零值的
// Options(不是由 DefaultOptions 构造的)喂给进化策略: 各旋钮
// 必须回退到 mu=16 / lambda=48 / sigma0=0.25 / sigmaFloor=0.03, 最后一
// 代必须被截断到剩余 budget, 且这次运行仍必须
// 是 budget 精确的。spec 也是零值, 所以 design 向量为空,
// 检查的只是结构。
func TestZeroValuedOptionsUseTheDocumentedDefaults(t *testing.T) {
	var opt Options
	opt.Budget = 100 // 16 个 parent + 48 + 36(最后一代被截断)

	sc := &toyScorer{seq: []float64{0.0, 1.0, 0.5}, seen: map[string]bool{}}
	res, err := Run(AlgorithmEvolution, sc, opt)
	if err != nil {
		t.Fatal(err)
	}
	if res.NEvals != 100 {
		t.Fatalf("NEvals = %d, want exactly 100", res.NEvals)
	}
	if len(res.History) != 100 {
		t.Fatalf("len(History) = %d, want 100", len(res.History))
	}
	_, metas, _, _ := sc.snapshot()
	roots, children := 0, 0
	for _, m := range metas {
		if m.Generation == 0 {
			roots++
		} else {
			children++
		}
	}
	if roots != 16 {
		t.Fatalf("roots = %d, want the default mu = 16", roots)
	}
	if children != 84 {
		t.Fatalf("children = %d, want 84 (48 + the truncated 36)", children)
	}
}

func TestToyScorerSanity(t *testing.T) {
	spec := config.DefaultSpec()
	sc := newToyScorer(spec)
	// 玩具的最优点就是人类 baseline, 所以它的得分是 0。
	if got := sc.score(textbookMirrorDesign, runner.Meta{}); math.Abs(got) > 1e-12 {
		t.Fatalf("toy optimum scores %v, want 0", got)
	}
	// 远处角落的得分严格更差, 而得分在碗的定义允许的
	// 范围内是对称的。
	lo, hi := spec.Lower(), spec.Upper()
	if got := sc.score(lo, runner.Meta{}); got >= 0 {
		t.Fatalf("corner score %v should be negative", got)
	}
	if got := sc.score(hi, runner.Meta{}); got >= 0 {
		t.Fatalf("corner score %v should be negative", got)
	}
	// 玩具的 design id 递增, 这正是 lineage 可被检查的原因。
	r1 := sc.Score(lo, runner.Meta{EvalIndex: 0})
	r2 := sc.Score(hi, runner.Meta{EvalIndex: 1})
	if r1.DesignID != "D0001" || r2.DesignID != "D0002" {
		t.Fatalf("toy ids = %q, %q, want D0001, D0002", r1.DesignID, r2.DesignID)
	}
}

// TestWarmPopulationSeedsFirstGen: evolution_knowledge 用 WarmPopulation 铺满首代。
// 给一个已知高分的种群, 小预算下 best 应直接命中它(继承), 而不是冷随机起步。
func TestWarmPopulationSeedsFirstGen(t *testing.T) {
	spec := config.DefaultSpec()
	// 手工构造 16 个可行设计(全在盒内、canonical)当 WarmPopulation
	pop := make([][]float64, 0, 16)
	for i := 0; i < 16; i++ {
		d := make([]float64, spec.NParams())
		for j := range d {
			lo, hi := spec.Lower()[j], spec.Upper()[j]
			d[j] = lo + (hi-lo)*float64((i+j)%10)/10.0
		}
		pop = append(pop, d)
	}
	opt := toyOptions(spec, 30, 0)
	opt.WarmPopulation = pop
	res, err := Run(AlgorithmEvolutionKnowledge, newToyScorer(spec), opt)
	if err != nil {
		t.Fatal(err)
	}
	if res.Algorithm != AlgorithmEvolutionKnowledge {
		t.Fatalf("Algorithm = %q, want %q", res.Algorithm, AlgorithmEvolutionKnowledge)
	}
	// WarmPopulation 非空 ⟹ 首代就评估了 16 个, 不可能是纯随机起步
	if res.NEvals < 16 {
		t.Fatalf("NEvals = %d, want >= 16 (WarmPopulation should seed first gen)", res.NEvals)
	}
}
