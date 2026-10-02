package main

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/logos-42/hushfusion-forge/internal/baseline"
	"github.com/logos-42/hushfusion-forge/internal/config"
	"github.com/logos-42/hushfusion-forge/internal/objective"
	"github.com/logos-42/hushfusion-forge/internal/physics"
	"github.com/logos-42/hushfusion-forge/internal/registry"
	"github.com/logos-42/hushfusion-forge/internal/rlenv"
	"github.com/logos-42/hushfusion-forge/internal/search"
)

// 这些测试覆盖接线本身: CLI 用来比较的冻结 schema 访问器、解析辅助函数、golden 门
// 的容差规则, 以及 CLI 写进 registry 的那条 record。它们都不求值 physics 或 search,
// 因此在其他 stage 仍是骨架时也能保持绿色。

// TestMetricAccessorCoversFrozenKeys 是一道 schema 漂移守卫: CLI 用来比较和打印的
// metric 键必须覆盖冻结的 physics.Metrics JSON tag 以及 rlenv 冻结的观测键。
func TestMetricAccessorCoversFrozenKeys(t *testing.T) {
	spec := config.DefaultSpec()
	m := physics.Metrics{
		BMidT: 1, BThroatT: 2, ZThroatM: -1, MirrorRatio: 2, VolumeGood: 0.5,
		Ripple: 0.1, BCoilMaxT: 3, MinCoilGapM: 0.4, MinClearanceM: 0.25, CostProxy: 1e12,
		CoilProximityFloorHit: true, NCoils: spec.NCoils, MU0: config.MU0,
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal metrics: %v", err)
	}
	var tags map[string]json.RawMessage
	if err := json.Unmarshal(raw, &tags); err != nil {
		t.Fatalf("unmarshal metrics: %v", err)
	}
	if len(tags) == 0 {
		t.Fatal("physics.Metrics marshalled to zero keys")
	}
	want := map[string]bool{}
	for _, k := range metricKeys {
		want[k] = true
	}
	for _, k := range metricBoolKeys {
		want[k] = true
	}
	for k := range tags {
		if !want[k] {
			t.Errorf("metric key %q is produced by physics.Metrics but not covered by the CLI accessor table", k)
		}
		if _, ok := metricValue(m, k); !ok {
			t.Errorf("metricValue(%q) does not resolve", k)
		}
	}
	for k := range want {
		if _, ok := tags[k]; !ok {
			t.Errorf("CLI accessor table lists %q which is not a physics.Metrics JSON field", k)
		}
	}
	// rlenv 追加的观测键也必须可打印。
	for _, k := range rlenv.ObsMetricKeys {
		if _, ok := metricValue(m, k); !ok {
			t.Errorf("rlenv observation key %q is not covered by metricValue", k)
		}
	}
	// 那个 bool metric 必须挺过 golden 文件所用的 float 编码。
	if v, _ := metricValue(m, "coil_proximity_floor_hit"); v != 1 {
		t.Errorf("coil_proximity_floor_hit = %v, want 1 for true", v)
	}
}

// TestRelDiffZeroGoldenRule 记录 golden 门的比较规则: 对真有量级的值用相对, 对按
// 构造为零的值用绝对 (与 0 做相对比较不是严格, 而是未定义)。
func TestRelDiffZeroGoldenRule(t *testing.T) {
	cases := []struct {
		name     string
		got      float64
		want     float64
		tol      float64
		wantPass bool
	}{
		{"exact", -0.2905708161, -0.2905708161, tolScore, true},
		{"score inside tolerance", -0.2905709161, -0.2905708161, tolScore, true},
		{"score outside tolerance", -0.2906718161, -0.2905708161, tolScore, false},
		{"zero golden with tiny absolute error", 1e-16, 0, tolMetrics, true},
		{"zero golden with 1e-3 absolute error", 1e-3, 0, tolMetrics, false},
		{"relative error just inside", 1.0000009, 1.0, tolMetrics, true},
		{"relative error far outside", 1.001, 1.0, tolMetrics, false},
	}
	for _, c := range cases {
		got := relDiff(c.got, c.want)
		pass := got <= c.tol
		if pass != c.wantPass {
			t.Errorf("%s: relDiff=%v, inside tol %v = %v, want %v", c.name, got, c.tol, pass, c.wantPass)
		}
	}
}

// TestSplitListAndInts 覆盖 CLI 依赖的 flag 值解析。
func TestSplitListAndInts(t *testing.T) {
	got := splitList(" random , lhs ,, evolution ")
	want := []string{"random", "lhs", "evolution"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("splitList = %v, want %v", got, want)
	}
	if n := len(splitList(" , , ")); n != 0 {
		t.Errorf("splitList of separators only = %d entries, want 0", n)
	}
	ints, err := splitInts("0, 1,2")
	if err != nil {
		t.Fatalf("splitInts: %v", err)
	}
	if len(ints) != 3 || ints[0] != 0 || ints[1] != 1 || ints[2] != 2 {
		t.Errorf("splitInts = %v, want [0 1 2]", ints)
	}
	if _, err := splitInts("0,seven"); err == nil {
		t.Error("splitInts accepted a non-integer seed")
	}
}

// TestFindFileSearchesUpward 证明 golden 路径解析从一个包子目录 (cmd/forge) 也能
// 工作, 且文件缺失是一个错误, 而不是一次空比较。
func TestFindFileSearchesUpward(t *testing.T) {
	path, err := findFile(goldenBaseline)
	if err != nil {
		t.Fatalf("findFile(%q): %v", goldenBaseline, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("findFile returned a path that does not exist: %v", err)
	}
	if _, err := findFile("definitely/not/here.json"); err == nil {
		t.Error("findFile accepted a path that does not exist anywhere")
	}
}

// TestBaselineRecordShape 检查 CLI 写出的 record: design 向量按线圈拆分, 且到达
// registry 的 JSON 中包含每一个 registry 必需字段 (schema-parity 门的 Go 那一半)。
func TestBaselineRecordShape(t *testing.T) {
	spec := config.DefaultSpec()
	k := spec.NCoils
	design := make([]float64, spec.NParams())
	for i := range design {
		design[i] = float64(i + 1)
	}
	res := objective.EvalResult{
		Score:    -0.2905708161,
		Terms:    map[string]float64{objective.TermField: 0, objective.TermCost: 1},
		Weighted: map[string]float64{objective.TermField: 0, objective.TermCost: -1},
		Penalties: map[string]float64{
			objective.PenConductorField: 0, objective.PenCoilSeparation: 0, objective.PenNotAMirror: 0,
		},
		Feasible: true,
		Metrics:  physics.Metrics{BMidT: 1, NCoils: k, MU0: config.MU0},
	}
	rec := baselineRecord(res, baseline.Baseline{Name: "textbook_mirror", Design: design, Cost: 1e12})

	if len(rec.Params.RadiusM) != k || len(rec.Params.ZM) != k || len(rec.Params.CurrentA) != k {
		t.Fatalf("params split = %d/%d/%d arrays, want %d each",
			len(rec.Params.RadiusM), len(rec.Params.ZM), len(rec.Params.CurrentA), k)
	}
	if rec.Params.RadiusM[0] != design[0] || rec.Params.ZM[0] != design[k] || rec.Params.CurrentA[0] != design[2*k] {
		t.Fatalf("params split does not follow r | z | I")
	}
	if rec.Algorithm != "human_baseline" {
		t.Errorf("baseline algorithm = %q, want human_baseline (it is its own algorithm in the report)", rec.Algorithm)
	}
	if rec.Score != res.Score {
		t.Errorf("record score %v != evaluated score %v", rec.Score, res.Score)
	}

	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatalf("unmarshal record: %v", err)
	}
	var missing []string
	for _, f := range registry.RequiredFields {
		if _, ok := keys[f]; !ok {
			missing = append(missing, f)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("the record the CLI writes is missing required fields: %v", missing)
	}
}

// TestXcheckSchemaMatchesGolden 是交给 oracle 的交接中的 schema 那一半: 导出的场
// 采样的 JSON 键必须与 testdata/golden_field_samples.json 的键完全一致, 否则
// python/aux/oracle.py 无法重算它们 (验收门 G5)。
func TestXcheckSchemaMatchesGolden(t *testing.T) {
	path, err := findFile(goldenSamples)
	if err != nil {
		t.Fatalf("findFile(%q): %v", goldenSamples, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden samples: %v", err)
	}
	var golden map[string]any
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatalf("unmarshal golden samples: %v", err)
	}
	goldenSamplesList, ok := golden["samples"].([]any)
	if !ok || len(goldenSamplesList) == 0 {
		t.Fatal("golden field samples have no samples array")
	}
	goldenFirst, ok := goldenSamplesList[0].(map[string]any)
	if !ok {
		t.Fatal("golden field samples: first entry is not an object")
	}

	mine := xcheckFile{Samples: []xcheckSample{{
		BMag: []float64{1}, Br: []float64{1}, Bz: []float64{1},
		Design: []float64{1}, DesignName: "d",
		PointsR: []float64{0}, PointsZ: []float64{0}, Solver: solverName,
	}}}
	encoded, err := json.Marshal(mine)
	if err != nil {
		t.Fatalf("marshal xcheck sample: %v", err)
	}
	var roundTripped map[string]any
	if err := json.Unmarshal(encoded, &roundTripped); err != nil {
		t.Fatalf("unmarshal xcheck sample: %v", err)
	}
	mineFirst := roundTripped["samples"].([]any)[0].(map[string]any)

	if len(mineFirst) != len(goldenFirst) {
		t.Errorf("xcheck sample has %d keys, golden has %d", len(mineFirst), len(goldenFirst))
	}
	for k := range goldenFirst {
		if _, ok := mineFirst[k]; !ok {
			t.Errorf("golden sample key %q missing from the xcheck export", k)
		}
	}
	for k := range mineFirst {
		if _, ok := goldenFirst[k]; !ok {
			t.Errorf("xcheck export key %q is not in the golden schema", k)
		}
	}

	// 与锚点文件相同的点数预算, 这样两份导出可以逐行对比。
	ptsR, ptsZ := xcheckPoints()
	if len(ptsR) != len(ptsZ) {
		t.Fatalf("xcheck points: %d r vs %d z", len(ptsR), len(ptsZ))
	}
	goldenPts := goldenFirst["points_r"].([]any)
	if len(ptsR) != len(goldenPts) {
		t.Errorf("xcheck exports %d points, the golden anchor file has %d", len(ptsR), len(goldenPts))
	}
	for i := range ptsR {
		if ptsR[i] < 0 {
			t.Errorf("xcheck point %d has a negative radius %v", i, ptsR[i])
		}
	}
}

// metricsFromGolden 把 golden 的 metrics 对象映射到 physics.Metrics, 这样比较函数
// 可以直接被喂入锚点值本身。
func metricsFromGolden(m map[string]float64) physics.Metrics {
	return physics.Metrics{
		BMidT: m["B_mid_T"], BThroatT: m["B_throat_T"], ZThroatM: m["z_throat_m"],
		MirrorRatio: m["mirror_ratio"], VolumeGood: m["volume_good"], Ripple: m["ripple"],
		BCoilMaxT: m["B_coil_max_T"], MinCoilGapM: m["min_coil_gap_m"], CostProxy: m["cost_proxy"],
		MinClearanceM:         m["min_clearance_m"],
		CoilProximityFloorHit: m["coil_proximity_floor_hit"] != 0,
		NCoils:                int(m["n_coils"]), MU0: m["mu0"],
	}
}

// TestGoldenComparisonIsGreenOnGoldenAndRedOnDeviation 把 golden 值本身喂进验收门
// 使用的比较: 它必须是绿的, 并且一旦 score 或某个 metric 的偏差超过合同容差就必须
// 变红。(不会变红的门不是门 —— 而这里是拆出来的比较, 运行它不需要 physics。)
func TestGoldenComparisonIsGreenOnGoldenAndRedOnDeviation(t *testing.T) {
	st := &verifyState{}
	if _, err := st.loadGolden(); err != nil {
		t.Fatalf("loadGolden: %v", err)
	}
	if st.gold.Score == 0 || len(st.gold.Metrics) == 0 || len(st.gold.Terms) == 0 {
		t.Fatal("golden baseline file did not load its score/metrics/terms")
	}

	exact := objective.EvalResult{
		Score:    st.gold.Score,
		Terms:    st.gold.Terms,
		Metrics:  metricsFromGolden(st.gold.Metrics),
		Feasible: st.gold.Feasible,
	}
	msg, err := compareBaselineEval(exact, st.gold)
	if err != nil {
		t.Fatalf("comparison rejected the golden values themselves: %v", err)
	}
	t.Logf("green on golden: %s", msg)
	if _, err := compareBaselineMetrics(exact.Metrics, st.gold.Metrics); err != nil {
		t.Fatalf("metric comparison rejected the golden metrics themselves: %v", err)
	}

	// score 上偏离 1e-4 必须变红。
	badScore := exact
	badScore.Score += 1e-4
	if _, err := compareBaselineEval(badScore, st.gold); err == nil {
		t.Error("score comparison stayed green on a 1e-4 deviation")
	}
	// 缺失的项必须变红, 而不是被悄悄跳过。
	badTerms := exact
	badTerms.Terms = map[string]float64{}
	if _, err := compareBaselineEval(badTerms, st.gold); err == nil {
		t.Error("term comparison stayed green with every term missing")
	}
	// 真有量级的 metric 上 1e-3 的相对偏差必须变红。
	if _, ok := st.gold.Metrics["B_throat_T"]; !ok {
		t.Fatal("golden metrics lost B_throat_T")
	}
	bad := map[string]float64{}
	for k, v := range st.gold.Metrics {
		bad[k] = v
	}
	bad["B_throat_T"] *= 1.001
	if _, err := compareBaselineMetrics(metricsFromGolden(bad), st.gold.Metrics); err == nil {
		t.Error("metric comparison stayed green on a 1e-3 relative deviation")
	}
	// golden 文件中钉为零的 metric 按绝对比较: 1e-3 算作真实偏差, 而不是“相对于
	// 虚无”。
	if st.gold.Metrics["ripple"] != 0 {
		t.Fatalf("this test assumes the golden ripple is 0, got %v", st.gold.Metrics["ripple"])
	}
	bad["ripple"] = 1e-3
	if _, err := compareBaselineMetrics(metricsFromGolden(bad), st.gold.Metrics); err == nil {
		t.Error("metric comparison stayed green on a 1e-3 absolute deviation of a zero-valued metric")
	}
}

// TestParseFlagsExitCodes 钉住 CLI 的退出码契约: --help 是成功, 坏 flag 或多出的
// 位置参数是用法错误。
func TestParseFlagsExitCodes(t *testing.T) {
	fs := newFlagSet("t", "t", "test flag set")
	_ = fs.Bool("check", true, "a bool flag")
	if code := parseFlags(fs, []string{"--help"}); code != 0 {
		t.Errorf("parseFlags(--help) = %d, want 0", code)
	}
	fs = newFlagSet("t", "t", "test flag set")
	if code := parseFlags(fs, []string{"--nope"}); code != 2 {
		t.Errorf("parseFlags(--nope) = %d, want 2", code)
	}
	fs = newFlagSet("t", "t", "test flag set")
	if code := parseFlags(fs, []string{"stray"}); code != 2 {
		t.Errorf("parseFlags(stray) = %d, want 2", code)
	}
	fs = newFlagSet("t", "t", "test flag set")
	_ = fs.Bool("check", true, "a bool flag")
	if code := parseFlags(fs, []string{"--check=false"}); code != -1 {
		t.Errorf("parseFlags(--check=false) = %d, want -1 (continue)", code)
	}
}

// TestWarmStartIsOnlyInjectedForTheWarmMethod 守护冷热对比:
// 'forge run --method evolution' 必须跑冷变体, 否则所测的“继承 design 知识之价值”
// 会按构造为零。
func TestWarmStartIsOnlyInjectedForTheWarmMethod(t *testing.T) {
	spec := config.DefaultSpec()
	warm := make([]float64, spec.NParams())
	for i := range warm {
		warm[i] = float64(i)
	}
	for _, m := range allMethods {
		opt := runOptions(spec, m, 7, 123, 2, -0.2905708161, warm)
		if opt.Algorithm != m || opt.Seed != 7 || opt.Budget != 123 || opt.Workers != 2 {
			t.Errorf("%s: options were not passed through: %+v", m, opt)
		}
		if opt.BaselineScore != -0.2905708161 {
			t.Errorf("%s: BaselineScore = %v, want the baseline score", m, opt.BaselineScore)
		}
		if m == search.AlgorithmEvolutionWarm {
			if len(opt.WarmStart) != len(warm) {
				t.Fatalf("%s: WarmStart = %v, want the caller's design", m, opt.WarmStart)
			}
			for i := range warm {
				if opt.WarmStart[i] != warm[i] {
					t.Fatalf("%s: WarmStart[%d] = %v, want %v", m, i, opt.WarmStart[i], warm[i])
				}
			}
			continue
		}
		if opt.WarmStart != nil {
			t.Errorf("%s: WarmStart = %v, want nil (the baseline must not seed a cold method)",
				m, opt.WarmStart)
		}
	}

	// 这个决定必须跟随冻结的 algorithm 名称, 而不是一个字面量。
	opt := runOptions(spec, search.AlgorithmEvolution, 0, 10, 1, 0, warm)
	if opt.WarmStart != nil {
		t.Errorf("evolution got a warm start: %v", opt.WarmStart)
	}
}

// TestVersionCommandRuns 是最廉价的一次接线冒烟测试: 'forge version' 会触碰
// config、rlenv、registry 与 search, 但不求值任何东西。
func TestVersionCommandRuns(t *testing.T) {
	if code := cmdVersion(nil); code != 0 {
		t.Fatalf("cmdVersion = %d, want 0", code)
	}
}
