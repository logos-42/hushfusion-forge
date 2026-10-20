package harness

import (
	"math"
	"path/filepath"
	"testing"
)

// newTestHarness 用临时 store 造一个 Harness（OML 关闭，纯规则层，便于确定性测试）。
func newTestHarness(t *testing.T, opts Options) *Harness {
	t.Helper()
	opts.StorePath = filepath.Join(t.TempDir(), "store.jsonl")
	if opts.Seed == 0 {
		opts.Seed = 1
	}
	if opts.PolicyName == "" {
		opts.PolicyName = "heuristic"
	}
	h, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return h
}

func TestNewOpensStore(t *testing.T) {
	h := newTestHarness(t, Options{})
	if h.Space.Dim() != 6 {
		t.Fatalf("期望 6 个声明动作 (5 flatten + 1 update_mu), 实际 %d", h.Space.Dim())
	}
	if len(h.Regions) != 5 {
		t.Fatalf("期望 5 个声明区域, 实际 %d", len(h.Regions))
	}
}

func TestRunAppendsDataAndStopsOnTermination(t *testing.T) {
	h := newTestHarness(t, Options{PolicyName: "random"})
	sum, err := h.Run(50, 24)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if sum.NEpisodes != 50 {
		t.Fatalf("期望 50 episode, 实际 %d", sum.NEpisodes)
	}
	if h.Store.Len() == 0 {
		t.Fatal("期望 store 有落盘记录")
	}
	if len(sum.PerActionMeanDelta) != 6 {
		t.Fatalf("期望 6 个动作的平均Δ, 实际 %d", len(sum.PerActionMeanDelta))
	}
	// 最优 2 步策略 (flatten:omega 后 update_mu) 应能在预算内命中目标。
	// 随机 50 条里至少偶尔命中, 不强求; 但 BestScore 应 > 初值 0 附近。
	if sum.BestScore <= 0 {
		t.Fatalf("BestScore 应 > 0, 实际 %.4f", sum.BestScore)
	}
}

func TestSelfAdjustShiftsHeuristicWeights(t *testing.T) {
	h := newTestHarness(t, Options{PolicyName: "heuristic"})
	if _, err := h.Run(40, 24); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if h.heuristic.W == nil {
		t.Fatal("运行前启发式权重应已初始化")
	}
	before := make([][]float64, len(h.heuristic.W))
	for a := range h.heuristic.W {
		before[a] = append([]float64(nil), h.heuristic.W[a]...)
	}
	if err := h.SelfAdjust(); err != nil {
		t.Fatalf("SelfAdjust: %v", err)
	}
	changed := false
	for a := range h.heuristic.W {
		for i := range h.heuristic.W[a] {
			if math.Abs(h.heuristic.W[a][i]-before[a][i]) > 1e-9 {
				changed = true
				break
			}
		}
		if changed {
			break
		}
	}
	if !changed {
		t.Fatal("SelfAdjust 后启发式线性权重应被重算")
	}
}

func TestSummarizeReconstructsFromStore(t *testing.T) {
	h := newTestHarness(t, Options{PolicyName: "random"})
	if _, err := h.Run(30, 24); err != nil {
		t.Fatalf("Run: %v", err)
	}
	s, err := h.Summarize()
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if s.NEpisodes != 30 {
		t.Fatalf("期望 30 episode, 实际 %d", s.NEpisodes)
	}
	if len(s.BestDesigns) == 0 {
		t.Fatal("期望至少有 top 设计")
	}
	// 重新开一个 Harness 读同一 store, 应累加 (不重复跑, 但能重建)。
	h2, err := New(Options{StorePath: h.Store.path, Seed: 1})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	s2, err := h2.Summarize()
	if err != nil {
		t.Fatalf("reopen Summarize: %v", err)
	}
	if s2.StoreLen != s.StoreLen {
		t.Fatalf("重新打开后 store 长度应一致: %d vs %d", s2.StoreLen, s.StoreLen)
	}
}

func TestObsDimIsFieldPlus5(t *testing.T) {
	if ObsDim() != 16+5 {
		t.Fatalf("期望 ObsDim=21, 实际 %d", ObsDim())
	}
}
