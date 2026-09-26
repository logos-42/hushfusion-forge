package runner

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/logos-42/hushfusion-forge/internal/config"
	"github.com/logos-42/hushfusion-forge/internal/objective"
	"github.com/logos-42/hushfusion-forge/internal/physics"
	"github.com/logos-42/hushfusion-forge/internal/registry"
)

// Scorer 是搜索层消费的接缝: 一旦这个断言无法编译, 每个搜索算法的单元测试就再也
// 无法注入玩具 scorer, 而正是这一点让 stage D 与 physics 层保持独立。
var _ Scorer = (*Runner)(nil)

// goldenCostRef 是人类 baseline 的欧姆代价 (CONTRACT.md §5,
// testdata/golden_baseline.json: cost_proxy = 1.791703035e12), 也就是 cost 项据以
// 归一化的那个值。
const goldenCostRef = 1.791703035e12

func openRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	reg, err := registry.Open(filepath.Join(t.TempDir(), "registry.jsonl"))
	if err != nil {
		t.Fatalf("registry.Open: %v", err)
	}
	return reg
}

// capture 运行 fn, 并把 panic 报告出来而不是让测试失败, 这样仍是骨架的依赖会被
// 报告为“尚未就绪”, 而不是用一个假求值器糊过去。
func capture(fn func()) (recovered any, completed bool) {
	completed = true
	defer func() {
		if rec := recover(); rec != nil {
			recovered, completed = rec, false
		}
	}()
	fn()
	return recovered, completed
}

func evalFixture() objective.EvalResult {
	return objective.EvalResult{
		Score:    1.25,
		Feasible: true,
		Terms: map[string]float64{
			"field": 0.0, "mirror": 0.24753, "volume": 0.780886, "ripple": 0.0, "cost": 1.0,
		},
		Weighted:  map[string]float64{"field": 0.0, "mirror": 0.123765, "volume": 0.585664, "ripple": 0.0, "cost": -1.0},
		Penalties: map[string]float64{"conductor_field": 0.0, "coil_separation": 0.0, "not_a_mirror": 0.0},
		Metrics: physics.Metrics{
			BMidT: 1.0, BThroatT: 3.536386, ZThroatM: -0.9975, MirrorRatio: 3.536386,
			VolumeGood: 0.780886, Ripple: 0.0, BCoilMaxT: 3.416271, MinCoilGapM: 0.5,
			CostProxy: goldenCostRef, CoilProximityFloorHit: false, NCoils: 4, MU0: config.MU0,
		},
		Design: []float64{0.3, 0.5, 0.5, 0.3, -1.0, -0.25, 0.25, 1.0, 1621279.24, 463222.64, 463222.64, 1621279.24},
	}
}

// ---------------------------------------------------------------------------
// 构造与 record 形状
// ---------------------------------------------------------------------------

func TestNewWiresTheRunnerFields(t *testing.T) {
	reg := openRegistry(t)
	r := New(reg, nil, "phase0")
	if r.Reg != reg {
		t.Errorf("Runner.Reg is not the registry that was passed in")
	}
	if r.Ev != nil {
		t.Errorf("Runner.Ev = %+v, want nil", r.Ev)
	}
	if r.Tag != "phase0" {
		t.Errorf("Runner.Tag = %q, want %q", r.Tag, "phase0")
	}
}

func TestRecordCarriesProvenanceAndRawTerms(t *testing.T) {
	r := New(openRegistry(t), nil, "phase0")
	res := evalFixture()
	meta := Meta{Algorithm: "evolution", Seed: 7, Generation: 3, EvalIndex: 42, Parent: "D0007", Note: "child of the baseline"}

	rec := r.record(res, meta)

	if rec.Algorithm != "evolution" || rec.Seed != 7 || rec.Generation != 3 || rec.EvalIndex != 42 {
		t.Errorf("provenance not carried: %+v", rec)
	}
	if rec.Tag != "phase0" {
		t.Errorf("Tag = %q, want phase0", rec.Tag)
	}
	if rec.ParentDesign != "D0007" {
		t.Errorf("ParentDesign = %q, want D0007 (the lineage edge)", rec.ParentDesign)
	}
	if rec.Note != "child of the baseline" {
		t.Errorf("Note = %q", rec.Note)
	}
	// id 是 registry 的职责: 一个自己分配 id 的 runner 会与并行搜索发生竞争。
	if rec.ExperimentID != 0 || rec.DesignID != "" || rec.Timestamp != "" {
		t.Errorf("record must leave ExperimentID/DesignID/Timestamp unset, got %+v", rec)
	}
	if rec.Score != res.Score || rec.Feasible != res.Feasible {
		t.Errorf("score/feasible not carried: %+v", rec)
	}
	// 每个原始项都留存下来: 没有它们, record 对重新加权毫无价值。
	for k, v := range res.Terms {
		if rec.Terms[k] != v {
			t.Errorf("terms[%s] = %v, want %v", k, rec.Terms[k], v)
		}
	}
	if len(rec.Weighted) != len(res.Weighted) || len(rec.Penalties) != len(res.Penalties) {
		t.Errorf("weighted/penalties not carried: %d/%d", len(rec.Weighted), len(rec.Penalties))
	}
	if rec.Metrics.CostProxy != goldenCostRef || rec.Metrics.NCoils != 4 {
		t.Errorf("metrics not carried: %+v", rec.Metrics)
	}
	// params 是这次求值的规范 design 按三段拆分的结果。
	if len(rec.Params.RadiusM) != 4 || len(rec.Params.ZM) != 4 || len(rec.Params.CurrentA) != 4 {
		t.Fatalf("params split into %d/%d/%d, want 4/4/4",
			len(rec.Params.RadiusM), len(rec.Params.ZM), len(rec.Params.CurrentA))
	}
	if rec.Params.RadiusM[0] != 0.3 || rec.Params.ZM[0] != -1.0 || rec.Params.CurrentA[0] != 1621279.24 {
		t.Errorf("params are mis-split: %+v", rec.Params)
	}
}

func TestRecordKeepsJSONTypesStableForNilMaps(t *testing.T) {
	r := New(openRegistry(t), nil, "phase0")
	res := evalFixture()
	res.Terms, res.Weighted, res.Penalties = nil, nil, nil
	rec := r.record(res, Meta{Algorithm: "random"})
	if rec.Terms == nil || rec.Weighted == nil || rec.Penalties == nil {
		t.Fatalf("nil maps must become empty maps so the JSON type does not change to null: %+v", rec)
	}
	if len(rec.Terms) != 0 || len(rec.Weighted) != 0 || len(rec.Penalties) != 0 {
		t.Fatalf("empty maps must stay empty: %+v", rec)
	}
}

func TestDesignParamsSplit(t *testing.T) {
	design := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
	cases := []struct {
		name                string
		nCoils              int
		wantR, wantZ, wantI []float64
	}{
		{"four coils", 4, []float64{1, 2, 3, 4}, []float64{5, 6, 7, 8}, []float64{9, 10, 11, 12}},
		// 错误的 n_coils 绝不能 panic 或丢数据: 尾部会落进 current_A, record
		// 保持可诊断。
		{"absent n_coils", 0, []float64{1, 2, 3, 4}, []float64{5, 6, 7, 8}, []float64{9, 10, 11, 12}},
		{"too large n_coils", 9, []float64{1, 2, 3, 4}, []float64{5, 6, 7, 8}, []float64{9, 10, 11, 12}},
		{"short design", 4, []float64{1, 2, 3}, []float64{4, 5, 6}, []float64{7, 8, 9}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := design
			if tc.name == "short design" {
				d = design[:9]
			}
			got := designParams(d, tc.nCoils)
			if !slices.Equal(got.RadiusM, tc.wantR) {
				t.Errorf("radius = %v, want %v", got.RadiusM, tc.wantR)
			}
			if !slices.Equal(got.ZM, tc.wantZ) {
				t.Errorf("z = %v, want %v", got.ZM, tc.wantZ)
			}
			if !slices.Equal(got.CurrentA, tc.wantI) {
				t.Errorf("current = %v, want %v", got.CurrentA, tc.wantI)
			}
		})
	}
	// 退化输入同样不能 panic。
	for _, d := range [][]float64{nil, {}, {1, 2}} {
		got := designParams(d, 4)
		if got.RadiusM == nil || got.ZM == nil || got.CurrentA == nil {
			t.Fatalf("degenerate design %v produced nil slices (JSON would be null)", d)
		}
	}
}

// ---------------------------------------------------------------------------
// 记录 (runner 自己拥有的部分: 不需要求值器)
// ---------------------------------------------------------------------------

// TestRecordResultBackfillsTheWrittenIDs 是搜索层依赖的标准: 它拿回的 DesignID
// 就是文件里的那个, 因此它构建的 lineage 树与 registry 完全吻合。
func TestRecordResultBackfillsTheWrittenIDs(t *testing.T) {
	reg := openRegistry(t)
	r := New(reg, nil, "phase0")

	root := r.recordResult(evalFixture(), Meta{Algorithm: "human_baseline", Seed: 1, EvalIndex: 0})
	if root.ExperimentID != 1 || root.DesignID != "D0001" {
		t.Fatalf("root got (%d, %q), want (1, \"D0001\")", root.ExperimentID, root.DesignID)
	}
	child := r.recordResult(evalFixture(), Meta{Algorithm: "evolution", Seed: 1, Generation: 1, Parent: root.DesignID})
	if child.ExperimentID != 2 || child.DesignID != "D0002" {
		t.Fatalf("child got (%d, %q), want (2, \"D0002\")", child.ExperimentID, child.DesignID)
	}

	recs, err := reg.Records()
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("registry holds %d records, want 2", len(recs))
	}
	if recs[0].DesignID != root.DesignID || recs[0].ExperimentID != root.ExperimentID {
		t.Errorf("record 1 = (%d, %q), runner reported (%d, %q)",
			recs[0].ExperimentID, recs[0].DesignID, root.ExperimentID, root.DesignID)
	}
	if recs[0].Score != root.Score {
		t.Errorf("registry score %v, runner reported %v", recs[0].Score, root.Score)
	}
	if recs[1].ParentDesign != root.DesignID {
		t.Errorf("record 2 parent = %q, want %q", recs[1].ParentDesign, root.DesignID)
	}
	if recs[1].Generation != 1 {
		t.Errorf("record 2 generation = %d, want 1", recs[1].Generation)
	}
	// runner 交还回来的这条 lineage 边, 在 registry 里是一条真实的边。
	lin := reg.Lineage()
	if kids := lin[root.DesignID]; len(kids) != 1 || kids[0] != child.DesignID {
		t.Errorf("Lineage[%s] = %v, want [%s]", root.DesignID, kids, child.DesignID)
	}
	if problems, err := reg.Check(); err != nil || len(problems) != 0 {
		t.Errorf("Check = %v, %v; want a clean registry", problems, err)
	}
}

// TestRecordResultIsConcurrencySafe 是并行搜索的情形: 从 100 个 goroutine 记录的
// 100 次求值必须产出 100 条 id 唯一且连续的 record, 且每个返回的 DesignID 都必须
// 是真正被写入的那条 record 的 id (这才使 Workers > 1 时 lineage 树正确)。
func TestRecordResultIsConcurrencySafe(t *testing.T) {
	reg := openRegistry(t)
	r := New(reg, nil, "phase0")
	const n = 100

	results := make([]objective.EvalResult, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res := evalFixture()
			res.Score = float64(i) / 100
			results[i] = r.recordResult(res, Meta{Algorithm: "evolution", Seed: 7, EvalIndex: i})
		}(i)
	}
	wg.Wait()

	recs, err := reg.Records()
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	if len(recs) != n {
		t.Fatalf("registry holds %d records, want %d", len(recs), n)
	}
	byID := map[int]registry.Record{}
	for _, rec := range recs {
		if byID[rec.ExperimentID].DesignID != "" {
			t.Fatalf("experiment_id %d was written twice", rec.ExperimentID)
		}
		byID[rec.ExperimentID] = rec
	}
	seen := map[string]bool{}
	for i, res := range results {
		if res.ExperimentID <= 0 || res.DesignID == "" {
			t.Fatalf("goroutine %d got an unassigned result: %+v", i, res)
		}
		if seen[res.DesignID] {
			t.Fatalf("design_id %s was handed to two goroutines", res.DesignID)
		}
		seen[res.DesignID] = true
		rec, ok := byID[res.ExperimentID]
		if !ok {
			t.Fatalf("runner reported experiment_id %d which is not in the registry", res.ExperimentID)
		}
		if rec.DesignID != res.DesignID {
			t.Fatalf("experiment_id %d: runner reported design %s, registry says %s",
				res.ExperimentID, res.DesignID, rec.DesignID)
		}
		if rec.Score != res.Score {
			t.Fatalf("design %s: registry score %v, runner score %v", res.DesignID, rec.Score, res.Score)
		}
	}
	if problems, err := reg.Check(); err != nil || len(problems) != 0 {
		t.Fatalf("Check = %v, %v; want a clean registry", problems, err)
	}
}

// ---------------------------------------------------------------------------
// 依赖缺失的报告
// ---------------------------------------------------------------------------

func TestScorePanicsWhenTheRunnerHasNoDependencies(t *testing.T) {
	if rec, ok := capture(func() { New(openRegistry(t), nil, "phase0").Score([]float64{1, 2, 3}, Meta{}) }); ok {
		t.Fatalf("Score with a nil Evaluator must not silently return: %+v", rec)
	} else if !strings.Contains(rec.(string), "nil Evaluator") {
		t.Errorf("panic message %q should name the missing Evaluator", rec)
	}

	if rec, ok := capture(func() { (&Runner{}).Score([]float64{1, 2, 3}, Meta{}) }); ok {
		t.Fatalf("Score with a nil Registry must not silently return: %+v", rec)
	} else if !strings.Contains(rec.(string), "nil Registry") {
		t.Errorf("panic message %q should name the missing Registry", rec)
	}

	// 无法记录的 runner 必须大声失败而不是丢掉这次 experiment: 不可写的 registry
	// 路径会带原因 panic。
	reg := openRegistry(t)
	r := New(reg, &objective.Evaluator{}, "phase0")
	reg.Path = filepath.Join(t.TempDir(), "no-such-dir", "registry.jsonl")
	if rec, ok := capture(func() { r.recordResult(evalFixture(), Meta{Algorithm: "random"}) }); ok {
		t.Fatalf("recording into an unusable path must not silently succeed: %+v", rec)
	} else if !strings.Contains(rec.(string), "recording the experiment") {
		t.Errorf("panic message %q should explain that recording failed", rec)
	}
}

// TestEmitScoredRegistryFixture 用真实的评分路径 (objective.Evaluator ->
// runner.Score -> registry) 产出一个 registry, 这样独立的 Python 检查器
// (python/aux/schema_check.py, 门 G9) 可以对真正被求值过的 record 运行, 而不是对
// 手写的夹具运行:
//
//	FORGE_FIXTURE_DIR=/tmp/forge-fix go test ./internal/runner/ -run TestEmitScoredRegistryFixture
//	python3 python/aux/schema_check.py /tmp/forge-fix/registry.jsonl
//
// 除非设置了 FORGE_FIXTURE_DIR 否则跳过; 在 stage A/B 仍是骨架期间也跳过 (以上游
// panic 为原因) —— 它从不伪造一次求值。
func TestEmitScoredRegistryFixture(t *testing.T) {
	dir := os.Getenv("FORGE_FIXTURE_DIR")
	if dir == "" {
		t.Skip("set FORGE_FIXTURE_DIR=<dir> to emit a scored registry.jsonl for the Python schema-parity gate")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	spec := config.DefaultSpec()
	var (
		grids physics.Grids
		ev    *objective.Evaluator
	)
	if p, ok := capture(func() {
		grids = physics.BuildGrids(spec)
		ev = objective.NewEvaluator(spec, physics.AnalyticSolver{}, goldenCostRef, grids)
	}); !ok {
		t.Skipf("dependency not ready: stage A/B is still a skeleton, building the evaluator panicked: %v", p)
	}
	reg, err := registry.Open(filepath.Join(dir, "registry.jsonl"))
	if err != nil {
		t.Fatalf("registry.Open: %v", err)
	}
	r := New(reg, ev, "phase0")

	parent := ""
	for i := 0; i < 3; i++ {
		x := make([]float64, spec.NParams())
		lo, hi := spec.Lower(), spec.Upper()
		for j := range x {
			x[j] = lo[j] + (hi[j]-lo[j])*(0.15+0.35*float64(i)+0.05*float64(j%3))
		}
		meta := Meta{Algorithm: "evolution", Seed: 7, Generation: i, EvalIndex: i, Parent: parent}
		var res objective.EvalResult
		if p, ok := capture(func() { res = r.Score(x, meta) }); !ok {
			t.Skipf("dependency not ready: scoring panicked upstream: %v", p)
		}
		if res.DesignID == "" {
			t.Fatalf("score %d: no design_id was assigned", i)
		}
		parent = res.DesignID
	}
	if problems, err := reg.Check(); err != nil || len(problems) != 0 {
		t.Fatalf("Check = %v, %v; want a clean registry", problems, err)
	}
	t.Logf("emitted %d scored records to %s", reg.Len(), reg.Path)
}

// TestScoreIsConcurrencySafe 真实地检验冻结文档注释中的承诺: “求值可能从多个
// goroutine 被调用 (并行搜索); 记录必须经 registry 保持串行化。” 在 stage A/B 仍是
// 骨架期间它跳过 (以上游 panic 为原因), 而当它们就绪后若有 goroutine panic 则大声
// 失败。
func TestScoreIsConcurrencySafe(t *testing.T) {
	spec := config.DefaultSpec()
	var (
		grids physics.Grids
		ev    *objective.Evaluator
	)
	if p, ok := capture(func() {
		grids = physics.BuildGrids(spec)
		ev = objective.NewEvaluator(spec, physics.AnalyticSolver{}, goldenCostRef, grids)
	}); !ok {
		t.Skipf("dependency not ready: stage A/B is still a skeleton, building the evaluator panicked: %v", p)
	}
	lo, hi := spec.Lower(), spec.Upper()
	designAt := func(i int) []float64 {
		x := make([]float64, spec.NParams())
		for j := range x {
			x[j] = lo[j] + (hi[j]-lo[j])*(0.2+0.6*float64((i*7+j*3)%100)/100)
		}
		return x
	}
	// 对一次性的 registry 做一次探针求值: 这里 panic 意味着上游路径还不可用, 而
	// goroutine 内部的 panic 无法被恢复 (它会把测试二进制一起带走)。
	probe := New(openRegistry(t), ev, "probe")
	if p, ok := capture(func() { probe.Score(designAt(0), Meta{Algorithm: "probe", Seed: 7}) }); !ok {
		t.Skipf("dependency not ready: scoring panicked upstream: %v", p)
	}

	reg := openRegistry(t)
	r := New(reg, ev, "phase0")
	const n = 100
	results := make([]objective.EvalResult, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = r.Score(designAt(i), Meta{Algorithm: "evolution", Seed: 7, EvalIndex: i})
		}(i)
	}
	wg.Wait()

	recs, err := reg.Records()
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	if len(recs) != n {
		t.Fatalf("registry holds %d records, want %d", len(recs), n)
	}
	byID := map[int]registry.Record{}
	byDesign := map[string]registry.Record{}
	for _, rec := range recs {
		if _, dup := byID[rec.ExperimentID]; dup {
			t.Fatalf("experiment_id %d was written twice", rec.ExperimentID)
		}
		byID[rec.ExperimentID] = rec
		byDesign[rec.DesignID] = rec
	}
	for i := 0; i < n; i++ {
		if byID[i+1].DesignID == "" {
			t.Fatalf("experiment_id %d is missing: the ids are not gap-free under parallel scoring", i+1)
		}
	}
	for i, res := range results {
		rec, ok := byDesign[res.DesignID]
		if !ok {
			t.Fatalf("goroutine %d reported design_id %q which is not in the registry", i, res.DesignID)
		}
		if rec.ExperimentID != res.ExperimentID {
			t.Fatalf("design %s: runner reported experiment_id %d, registry says %d",
				res.DesignID, res.ExperimentID, rec.ExperimentID)
		}
		if rec.Score != res.Score {
			t.Fatalf("design %s: registry score %v, runner score %v", res.DesignID, rec.Score, res.Score)
		}
		// 这次求值的原始项原封不动地到达了 record。
		for k, v := range res.Terms {
			if rec.Terms[k] != v {
				t.Fatalf("design %s: terms[%s] = %v in the registry, %v in the result",
					res.DesignID, k, rec.Terms[k], v)
			}
		}
	}
	if problems, err := reg.Check(); err != nil || len(problems) != 0 {
		t.Fatalf("Check = %v, %v; want a clean registry", problems, err)
	}
}

// TestScoreEndToEnd 针对真实的 objective.Evaluator 与 physics.Solver 演练
// runner.Score 自己拥有的真实路径 —— 求值、分配 id、写入 record、回填
// DesignID/ExperimentID。
//
// Stage A (physics) 与 stage B (objective) 是两条并行的独立线, 写作时仍是会 panic
// 的骨架。它们未就绪时本测试会以上游 panic 为原因 SKIP: 它不 mock 求值器, 也不
// 假装这个接缝已被验证。等它们落地, 同一段代码就会真实运行。
func TestScoreEndToEnd(t *testing.T) {
	spec := config.DefaultSpec()
	var (
		grids physics.Grids
		ev    *objective.Evaluator
	)
	if p, ok := capture(func() {
		grids = physics.BuildGrids(spec)
		ev = objective.NewEvaluator(spec, physics.AnalyticSolver{}, goldenCostRef, grids)
	}); !ok {
		t.Skipf("dependency not ready: stage A/B is still a skeleton, building the evaluator panicked: %v", p)
	}

	reg := openRegistry(t)
	r := New(reg, ev, "phase0")
	x := []float64{0.4, 0.5, 0.5, 0.4, -0.8, -0.2, 0.2, 0.8, 1.0e6, 4.0e5, 4.0e5, 1.0e6}

	var root objective.EvalResult
	if p, ok := capture(func() {
		root = r.Score(x, Meta{Algorithm: "random", Seed: 7, EvalIndex: 0, Note: "root"})
	}); !ok {
		t.Skipf("dependency not ready: scoring panicked upstream: %v", p)
	}
	if root.ExperimentID != 1 || root.DesignID != "D0001" {
		t.Fatalf("root scored as (%d, %q), want (1, \"D0001\")", root.ExperimentID, root.DesignID)
	}

	recs, err := reg.Records()
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("registry holds %d records, want 1", len(recs))
	}
	if recs[0].Score != root.Score {
		t.Errorf("registry score %v, runner returned %v", recs[0].Score, root.Score)
	}
	if recs[0].Algorithm != "random" || recs[0].Tag != "phase0" || recs[0].Note != "root" {
		t.Errorf("provenance not recorded: %+v", recs[0])
	}
	if recs[0].Timestamp == "" {
		t.Errorf("timestamp was not assigned by the registry")
	}
	if len(recs[0].Params.RadiusM) != spec.NCoils || len(recs[0].Params.ZM) != spec.NCoils || len(recs[0].Params.CurrentA) != spec.NCoils {
		t.Errorf("params were not recorded: %+v", recs[0].Params)
	}
	if len(recs[0].Terms) == 0 || len(recs[0].Penalties) == 0 {
		t.Errorf("raw terms/penalties missing from the record: %+v", recs[0])
	}

	var child objective.EvalResult
	if p, ok := capture(func() {
		child = r.Score(x, Meta{Algorithm: "evolution", Seed: 7, Generation: 1, EvalIndex: 1, Parent: root.DesignID})
	}); !ok {
		t.Skipf("dependency not ready: scoring a child panicked upstream: %v", p)
	}
	if child.DesignID != "D0002" {
		t.Fatalf("child design_id = %q, want D0002", child.DesignID)
	}
	lin := reg.Lineage()
	kids := lin[root.DesignID]
	if len(kids) != 1 || kids[0] != child.DesignID {
		t.Fatalf("Lineage[%s] = %v, want [%s] — the search layer builds the tree from these ids",
			root.DesignID, kids, child.DesignID)
	}
	if problems, err := reg.Check(); err != nil || len(problems) != 0 {
		t.Fatalf("Check = %v, %v; want a clean registry", problems, err)
	}
}
