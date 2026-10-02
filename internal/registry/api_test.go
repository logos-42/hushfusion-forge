package registry

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/logos-42/hushfusion-forge/internal/physics"
)

// ---------------------------------------------------------------------------
// 测试夹具
// ---------------------------------------------------------------------------

// metricsFixture 设置全部 12 个 physics.Metrics 字段。这个数量由
// TestRecordSchemaKeySetIsFrozen 断言: registry 必须携带原始物理量, 而不只是一个
// score, 这样将来重新加权时可以不必重跑搜索就推导出来。
func metricsFixture() physics.Metrics {
	return physics.Metrics{
		BMidT:                 1.0,
		BThroatT:              3.536386,
		ZThroatM:              -0.9975,
		MirrorRatio:           3.536386,
		VolumeGood:            0.780886,
		Ripple:                0.0,
		BCoilMaxT:             3.416271,
		MinCoilGapM:           0.5,
		CostProxy:             1.791703035e12,
		CoilProximityFloorHit: false,
		NCoils:                4,
		MU0:                   1.2566370614359173e-06,
	}
}

// recordFixture 是一条完整、健康的 record: 除调用方无权选择的字段 (id、时间戳)
// 外, 每个冻结字段都已填充。
func recordFixture(score float64, algorithm string) Record {
	return Record{
		Generation: 1,
		Algorithm:  algorithm,
		Seed:       7,
		EvalIndex:  3,
		Tag:        "phase0",
		Score:      score,
		Feasible:   score > 0,
		Params: Params{
			RadiusM:  []float64{0.3, 0.5, 0.5, 0.3},
			ZM:       []float64{-1.0, -0.25, 0.25, 1.0},
			CurrentA: []float64{1621279.24, 463222.64, 463222.64, 1621279.24},
		},
		Terms:     map[string]float64{"field": 0.0, "mirror": 0.24753, "volume": 0.780886, "ripple": 0.0, "cost": 1.0},
		Weighted:  map[string]float64{"field": 0.0, "mirror": 0.123765, "volume": 0.585664, "ripple": 0.0, "cost": -1.0},
		Penalties: map[string]float64{"conductor_field": 0.0, "coil_separation": 0.0, "not_a_mirror": 0.0, "clearance": 0.0},
		Metrics:   metricsFixture(),
		Note:      "fixture",
	}
}

// rawRecordLine 把一条带显式 id 的 record 渲染成 JSONL 行 (不含换行)。
func rawRecordLine(t *testing.T, id int, designID, algorithm string, score float64) string {
	t.Helper()
	rec := recordFixture(score, algorithm)
	rec.ExperimentID = id
	rec.DesignID = designID
	rec.Timestamp = "2026-01-01T00:00:00Z"
	b, err := encodeRecord(rec)
	if err != nil {
		t.Fatalf("encodeRecord: %v", err)
	}
	return strings.TrimRight(string(b), "\n")
}

// replaceOnce 以手术方式把一处缺陷施加到一行已编码的文本上, 并在模式不存在时
// 拒绝静默地什么都不做 (那会把一道红门变成绿门)。
func replaceOnce(t *testing.T, line, old, new string) string {
	t.Helper()
	if !strings.Contains(line, old) {
		t.Fatalf("defect surgery: %q not found in %s", old, line)
	}
	return strings.Replace(line, old, new, 1)
}

func writeLines(t *testing.T, path string, lines []string, trailingNewline bool) {
	t.Helper()
	body := strings.Join(lines, "\n")
	if trailingNewline {
		body += "\n"
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func openTemp(t *testing.T) *Registry {
	t.Helper()
	path := filepath.Join(t.TempDir(), "registry.jsonl")
	reg, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%s): %v", path, err)
	}
	return reg
}

func readProblems(t *testing.T, reg *Registry) []string {
	t.Helper()
	problems, err := reg.Check()
	if err != nil {
		t.Fatalf("Check(): unexpected error: %v", err)
	}
	return problems
}

// ---------------------------------------------------------------------------
// schema parity 锚点 (Go <-> Python 字段名)
// ---------------------------------------------------------------------------

// frozenRecordKeys 是 api.go 中声明、并与 Python 参考实现共享的 record schema。
// 它是刻意手工写出来的: 这就是 parity 锚点, 所以当某个 struct tag 被改名时它必须
// 失败, 并且它绝不能从它正在检查的那个 struct 推导出来。
var frozenRecordKeys = []string{
	"experiment_id",
	"design_id",
	"parent_design",
	"generation",
	"algorithm",
	"seed",
	"eval_index",
	"tag",
	"timestamp",
	"score",
	"feasible",
	"params",
	"terms",
	"weighted",
	"penalties",
	"metrics",
	"note",
}

func TestRecordSchemaKeySetIsFrozen(t *testing.T) {
	rec := recordFixture(-0.2905708161, "random")
	rec.ExperimentID = 1
	rec.DesignID = "D0001"
	rec.ParentDesign = "D0000"
	rec.Timestamp = "2026-01-01T00:00:00Z"

	extra, missing := keyDiff(jsonKeys(t, rec), frozenRecordKeys)
	if len(extra) != 0 || len(missing) != 0 {
		t.Fatalf("record key set differs from the frozen schema:\n  extra:   %v\n  missing: %v",
			extra, missing)
	}

	// 嵌套对象同样是互换格式的一部分。
	paramsExtra, paramsMissing := keyDiff(jsonKeys(t, rec.Params), []string{"radius_m", "z_m", "current_A"})
	if len(paramsExtra) != 0 || len(paramsMissing) != 0 {
		t.Fatalf("params key set differs:\n  extra:   %v\n  missing: %v", paramsExtra, paramsMissing)
	}

	// 12 个原始 metrics: 整件事的意义在于 record 事后可以重新评分、重新加权,
	// 而这仅凭一个 score 是做不到的。
	metricKeys := jsonKeys(t, rec.Metrics)
	if len(metricKeys) != 13 {
		t.Fatalf("metrics must carry 13 raw quantities, got %d: %v", len(metricKeys), metricKeys)
	}
	metricsExtra, metricsMissing := keyDiff(metricKeys, []string{
		"B_mid_T", "B_throat_T", "z_throat_m", "mirror_ratio", "volume_good", "ripple",
		"B_coil_max_T", "min_coil_gap_m", "min_clearance_m", "cost_proxy", "coil_proximity_floor_hit", "n_coils", "mu0",
	})
	if len(metricsExtra) != 0 || len(metricsMissing) != 0 {
		t.Fatalf("metrics key set differs from the frozen cross-language schema:\n  extra:   %v\n  missing: %v",
			metricsExtra, metricsMissing)
	}

	// term 的键在 objective/api.go 中冻结, 并出现在每条 record 里。
	termExtra, termMissing := keyDiff(jsonKeys(t, rec.Terms), []string{"field", "mirror", "volume", "ripple", "cost"})
	if len(termExtra) != 0 || len(termMissing) != 0 {
		t.Fatalf("terms key set differs:\n  extra:   %v\n  missing: %v", termExtra, termMissing)
	}

	penExtra, penMissing := keyDiff(jsonKeys(t, rec.Penalties), []string{"conductor_field", "coil_separation", "not_a_mirror", "clearance"})
	if len(penExtra) != 0 || len(penMissing) != 0 {
		t.Fatalf("penalties key set differs:\n  extra:   %v\n  missing: %v", penExtra, penMissing)
	}
}

// TestRecordOmitemptyKeysAreExactlyTheTwoOptionalOnes 钉住唯一一处 record 合法地
// 比 schema 键更少的地方: parent_design 与 note 是 omitempty (根节点没有父节点,
// 普通求值没有 note)。除此之外任何键消失都是 schema 破坏。
func TestRecordOmitemptyKeysAreExactlyTheTwoOptionalOnes(t *testing.T) {
	rec := recordFixture(0.5, "random")
	rec.Note = "" // 正是调用方设置的 note 让这个键出现
	got := jsonKeys(t, rec)
	extra, missing := keyDiff(got, frozenRecordKeys)
	if len(extra) != 0 {
		t.Fatalf("unexpected keys in a minimal record: %v", extra)
	}
	want := []string{"parent_design", "note"}
	sort.Strings(want)
	sort.Strings(missing)
	if strings.Join(missing, ",") != strings.Join(want, ",") {
		t.Fatalf("omitempty must drop exactly %v, dropped %v", want, missing)
	}
}

func TestRequiredFieldsAreFrozenSchemaKeys(t *testing.T) {
	for _, f := range RequiredFields {
		found := false
		for _, k := range frozenRecordKeys {
			if k == f {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("RequiredFields contains %q which is not part of the frozen schema", f)
		}
	}
}

// ---------------------------------------------------------------------------
// Check(): 门必须能够变红
// ---------------------------------------------------------------------------

func TestCheckHealthyRegistryIsClean(t *testing.T) {
	reg := openTemp(t)
	for i, algo := range []string{"human_baseline", "random", "evolution"} {
		if err := reg.Append(recordFixture(float64(i)*0.1, algo)); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	if problems := readProblems(t, reg); len(problems) != 0 {
		t.Fatalf("healthy registry reported problems: %v", problems)
	}
}

// TestCheckDetectsEachDefect 是“不会变红的门不是门”的测试: 每一类缺陷都被注入
// 到一个本来健康的文件里, 必须被报告, 恰好一次, 且消息要点出问题所在。
func TestCheckDetectsEachDefect(t *testing.T) {
	base := func(t *testing.T) []string {
		return []string{
			rawRecordLine(t, 1, "D0001", "human_baseline", -0.29),
			rawRecordLine(t, 2, "D0002", "random", 0.10),
			rawRecordLine(t, 3, "D0003", "evolution", 0.20),
			rawRecordLine(t, 4, "D0004", "evolution", 0.30),
		}
	}

	cases := []struct {
		name    string
		mutate  func(t *testing.T, lines []string) []string
		wantSub string
	}{
		{
			name:    "healthy control",
			mutate:  func(_ *testing.T, lines []string) []string { return lines },
			wantSub: "",
		},
		{
			name: "missing required field",
			mutate: func(t *testing.T, lines []string) []string {
				// 从 record 2 中删除 "seed"
				lines[1] = replaceOnce(t, lines[1], `"seed":7,`, ``)
				return lines
			},
			wantSub: "missing required field(s): seed",
		},
		{
			name: "experiment_id gap",
			mutate: func(t *testing.T, lines []string) []string {
				lines[2] = replaceOnce(t, lines[2], `"experiment_id":3`, `"experiment_id":5`)
				return lines
			},
			wantSub: "experiment_id 5 is not sequential",
		},
		{
			name: "experiment_id duplicate",
			mutate: func(t *testing.T, lines []string) []string {
				lines[2] = replaceOnce(t, lines[2], `"experiment_id":3`, `"experiment_id":2`)
				return lines
			},
			wantSub: "duplicate of the record on line 2",
		},
		{
			name: "experiment_id out of range",
			mutate: func(t *testing.T, lines []string) []string {
				lines[0] = replaceOnce(t, lines[0], `"experiment_id":1`, `"experiment_id":0`)
				return lines
			},
			wantSub: "is out of range",
		},
		{
			name: "dangling parent_design",
			mutate: func(t *testing.T, lines []string) []string {
				lines[3] = replaceOnce(t, lines[3], `"design_id":"D0004",`, `"design_id":"D0004","parent_design":"D9999",`)
				return lines
			},
			wantSub: `parent_design "D9999" does not exist`,
		},
		{
			name: "duplicate design_id",
			mutate: func(t *testing.T, lines []string) []string {
				lines[3] = replaceOnce(t, lines[3], `"design_id":"D0004"`, `"design_id":"D0002"`)
				return lines
			},
			wantSub: `duplicate design_id "D0002"`,
		},
		{
			name: "empty design_id",
			mutate: func(t *testing.T, lines []string) []string {
				lines[3] = replaceOnce(t, lines[3], `"design_id":"D0004"`, `"design_id":""`)
				return lines
			},
			wantSub: "design_id is empty",
		},
		{
			name: "corrupt interior line",
			mutate: func(t *testing.T, lines []string) []string {
				lines[1] = lines[1][:len(lines[1])/2]
				return lines
			},
			wantSub: "not valid JSON",
		},
		{
			name: "truncated final line is tolerated",
			mutate: func(t *testing.T, lines []string) []string {
				lines = append(lines, `{"experiment_id":5,"design_id":"D000`)
				return lines
			},
			wantSub: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "registry.jsonl")
			lines := tc.mutate(t, base(t))
			writeLines(t, path, lines, true)

			// 刻意不调用 Open(): 即使 Open() 还没来得及修复, Check() 也必须容忍一个
			// 尾部不可读的文件。
			reg := &Registry{Path: path}
			problems := readProblems(t, reg)
			if tc.wantSub == "" {
				if len(problems) != 0 {
					t.Fatalf("expected no problems, got: %v", problems)
				}
				return
			}
			if len(problems) != 1 {
				t.Fatalf("expected exactly 1 problem, got %d: %v", len(problems), problems)
			}
			if !strings.Contains(problems[0], tc.wantSub) {
				t.Fatalf("problem %q does not mention %q", problems[0], tc.wantSub)
			}
		})
	}
}

func TestCheckReportsAllDefectsAtOnce(t *testing.T) {
	lines := []string{
		rawRecordLine(t, 1, "D0001", "human_baseline", -0.29),
		rawRecordLine(t, 4, "D0002", "random", 0.10),   // 空洞 + 缺失字段 + 悬空父节点
		rawRecordLine(t, 5, "D0002", "evolution", 0.2), // 重复的 design_id
		`{"experiment_id":6,"design_id":"D000`,         // 损坏的中间行
		rawRecordLine(t, 7, "D0004", "evolution", 0.3),
	}
	lines[1] = replaceOnce(t, lines[1], `"seed":7,`, ``)
	lines[1] = replaceOnce(t, lines[1], `"design_id":"D0002",`, `"design_id":"D0002","parent_design":"D0404",`)

	path := filepath.Join(t.TempDir(), "registry.jsonl")
	writeLines(t, path, lines, true)

	problems := readProblems(t, &Registry{Path: path})
	for _, want := range []string{
		"missing required field(s): seed",
		"experiment_id 4 is not sequential",
		`parent_design "D0404" does not exist`,
		`duplicate design_id "D0002"`,
		"not valid JSON",
	} {
		found := false
		for _, p := range problems {
			if strings.Contains(p, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Check() missed %q; got %v", want, problems)
		}
	}
}

// ---------------------------------------------------------------------------
// lineage / 分支改进
// ---------------------------------------------------------------------------

// TestLineageAndBranchImprovement 手工构建一棵小树:
//
//	D0001 (human, 0.00, infeasible)
//	├── D0002 (evolution, 0.40) ── D0004 (evolution, 3.00)
//	└── D0003 (random, 0.20)    ── D0005 (evolution, 0.30)
//	D9999 (nowhere) ← D0006 (ghost, -1.00)   悬空边
//
// 并检查“哪条分支带来的改进最多?”这个基元的回答确实给出
// gain = 最佳子节点得分 - 父节点得分, 且按降序排列。
func TestLineageAndBranchImprovement(t *testing.T) {
	reg := openTemp(t)
	tree := []struct {
		parent string
		score  float64
		algo   string
		feas   bool
	}{
		{"", 0.00, "human_baseline", false},
		{"D0001", 0.40, "evolution", true},
		{"D0001", 0.20, "random", true},
		{"D0002", 3.00, "evolution", true},
		{"D0003", 0.30, "evolution", true},
		{"D9999", -1.00, "ghost", false},
	}
	for _, n := range tree {
		rec := recordFixture(n.score, n.algo)
		rec.ParentDesign = n.parent
		rec.Feasible = n.feas
		if err := reg.Append(rec); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	lin := reg.Lineage()
	wantLin := map[string][]string{
		"D0001": {"D0002", "D0003"},
		"D0002": {"D0004"},
		"D0003": {"D0005"},
		"D9999": {"D0006"},
	}
	if len(lin) != len(wantLin) {
		t.Fatalf("Lineage has %d parents, want %d: %v", len(lin), len(wantLin), lin)
	}
	for parent, kids := range wantLin {
		got := lin[parent]
		if strings.Join(got, ",") != strings.Join(kids, ",") {
			t.Errorf("Lineage[%s] = %v, want %v", parent, got, kids)
		}
	}

	rows := reg.BranchImprovement()
	wantRows := []BranchRow{
		{DesignID: "D0002", NChildren: 1, ParentScore: 0.40, BestChildScore: 3.00, Gain: 2.60},
		{DesignID: "D0001", NChildren: 2, ParentScore: 0.00, BestChildScore: 0.40, Gain: 0.40},
		{DesignID: "D0003", NChildren: 1, ParentScore: 0.20, BestChildScore: 0.30, Gain: 0.10},
	}
	if len(rows) != len(wantRows) {
		t.Fatalf("BranchImprovement returned %d rows, want %d: %+v", len(rows), len(wantRows), rows)
	}
	for i, want := range wantRows {
		got := rows[i]
		if got.DesignID != want.DesignID || got.NChildren != want.NChildren {
			t.Errorf("row %d = %+v, want %+v", i, got, want)
			continue
		}
		// gain 是得分的差, 所以用容差比较而不是 struct 相等 (在二进制浮点里
		// 0.3-0.2 并不等于 0.1)。
		for _, pair := range []struct {
			name    string
			got, wx float64
		}{
			{"parent_score", got.ParentScore, want.ParentScore},
			{"best_child_score", got.BestChildScore, want.BestChildScore},
			{"gain", got.Gain, want.Gain},
		} {
			if math.Abs(pair.got-pair.wx) > 1e-12 {
				t.Errorf("row %d %s = %v, want %v", i, pair.name, pair.got, pair.wx)
			}
		}
	}
	// 悬空的 lineage 边绝不能为一个没有 record 的 design 造出一行。
	for _, r := range rows {
		if r.DesignID == "D9999" {
			t.Errorf("BranchImprovement invented a row for the missing design D9999")
		}
	}
	// 增益有高有低, 这才使排序可被观察到。
	if rows[0].Gain <= rows[len(rows)-1].Gain {
		t.Errorf("rows are not sorted by gain descending: %+v", rows)
	}
	// 重复调用必须一致 (顺序确定, 不泄漏 map 遍历顺序)。
	again := reg.BranchImprovement()
	if len(again) != len(rows) {
		t.Fatalf("BranchImprovement changed length between calls: %d vs %d", len(rows), len(again))
	}
	for i := range rows {
		if rows[i] != again[i] {
			t.Errorf("BranchImprovement is not deterministic: %+v vs %+v", rows[i], again[i])
		}
	}

	// 这条悬空边正是完整性门存在的目的。
	problems := readProblems(t, reg)
	if len(problems) != 1 || !strings.Contains(problems[0], `parent_design "D9999" does not exist`) {
		t.Fatalf("Check() must flag the dangling edge, got: %v", problems)
	}
}

func TestBranchImprovementOnEmptyAndFlatRegistry(t *testing.T) {
	reg := openTemp(t)
	if rows := reg.BranchImprovement(); len(rows) != 0 {
		t.Fatalf("empty registry must have no branch rows, got %+v", rows)
	}
	// 三个根节点, 没有 lineage: 这棵树是三片叶子, 因此没有什么可比较, 也不允许
	// 造出任何一行。
	for i := 0; i < 3; i++ {
		if err := reg.Append(recordFixture(float64(i), "random")); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	if rows := reg.BranchImprovement(); len(rows) != 0 {
		t.Fatalf("a lineage-free registry must have no branch rows, got %+v", rows)
	}
	if lin := reg.Lineage(); len(lin) != 0 {
		t.Fatalf("a lineage-free registry must have an empty Lineage, got %v", lin)
	}
}

// ---------------------------------------------------------------------------
// 查询
// ---------------------------------------------------------------------------

func TestBestAndSummary(t *testing.T) {
	reg := openTemp(t)
	fixtures := []struct {
		parent string
		score  float64
		algo   string
		feas   bool
	}{
		{"", 0.00, "human_baseline", false},
		{"D0001", 0.40, "evolution", true},
		{"D0001", 0.20, "random", true},
		{"D0002", 3.00, "evolution", true},
		{"D0003", 0.30, "evolution", true},
	}
	for _, f := range fixtures {
		rec := recordFixture(f.score, f.algo)
		rec.ParentDesign = f.parent
		rec.Feasible = f.feas
		if err := reg.Append(rec); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	if best, ok := reg.Best(false, ""); !ok || best.DesignID != "D0004" || best.Score != 3.0 {
		t.Errorf("Best(any) = %+v, %v; want D0004 @ 3.0", best, ok)
	}
	if best, ok := reg.Best(true, ""); !ok || best.DesignID != "D0004" {
		t.Errorf("Best(feasible) = %+v, %v; want D0004", best, ok)
	}
	// human_baseline 在这里不可行, 所以限定到它必须找不到任何东西。
	if best, ok := reg.Best(true, "human_baseline"); ok {
		t.Errorf("Best(feasible, human_baseline) = %+v, want no record", best)
	}
	if best, ok := reg.Best(false, "human_baseline"); !ok || best.DesignID != "D0001" {
		t.Errorf("Best(any, human_baseline) = %+v, %v; want D0001", best, ok)
	}
	if best, ok := reg.Best(false, "nope"); ok {
		t.Errorf("Best(unknown algorithm) = %+v, want no record", best)
	}

	perAlgo := reg.BestPerAlgorithm()
	if len(perAlgo) != 3 {
		t.Fatalf("BestPerAlgorithm returned %d algorithms, want 3: %v", len(perAlgo), perAlgo)
	}
	if got := perAlgo["evolution"]; got.DesignID != "D0004" || got.Score != 3.0 {
		t.Errorf("best evolution = %+v, want D0004 @ 3.0", got)
	}
	if got := perAlgo["random"]; got.DesignID != "D0003" {
		t.Errorf("best random = %+v, want D0003", got)
	}

	sum := reg.Summary()
	if sum.NRecords != 5 || sum.NFeasible != 4 {
		t.Errorf("Summary counts = %d records / %d feasible, want 5 / 4", sum.NRecords, sum.NFeasible)
	}
	if sum.PerAlgo["evolution"] != 3 || sum.PerAlgo["random"] != 1 || sum.PerAlgo["human_baseline"] != 1 {
		t.Errorf("Summary.PerAlgo = %v", sum.PerAlgo)
	}
	if sum.BestDesignID != "D0004" || sum.BestAlgorithm != "evolution" || sum.BestScore != 3.0 {
		t.Errorf("Summary best = %s/%s/%v, want D0004/evolution/3.0", sum.BestDesignID, sum.BestAlgorithm, sum.BestScore)
	}
}

func TestQueriesOnEmptyRegistry(t *testing.T) {
	reg := openTemp(t)
	if best, ok := reg.Best(false, ""); ok || best != nil {
		t.Fatalf("Best on an empty registry = %+v, %v; want nil, false", best, ok)
	}
	if got := reg.BestPerAlgorithm(); len(got) != 0 || got == nil {
		t.Fatalf("BestPerAlgorithm on an empty registry = %v, want an empty map", got)
	}
	if got := reg.Lineage(); len(got) != 0 || got == nil {
		t.Fatalf("Lineage on an empty registry = %v, want an empty map", got)
	}
	sum := reg.Summary()
	if sum.NRecords != 0 || sum.NFeasible != 0 || sum.BestDesignID != "" || sum.BestAlgorithm != "" || sum.BestScore != 0 {
		t.Fatalf("Summary on an empty registry = %+v, want the zero value", sum)
	}
	if sum.PerAlgo == nil {
		t.Fatalf("Summary.PerAlgo must be an empty map, not nil (stable JSON type)")
	}
	if problems := readProblems(t, reg); len(problems) != 0 {
		t.Fatalf("an empty registry is healthy, got: %v", problems)
	}
	recs, err := reg.Records()
	if err != nil || len(recs) != 0 {
		t.Fatalf("Records on an empty registry = %v, %v; want empty, nil", recs, err)
	}
	if recs == nil {
		t.Fatalf("Records must return a non-nil slice so JSON renders [] and not null")
	}
}

// TestRecordsOnMissingFileIsAnEmptyRegistry 记录这样一件事: 一个文件在其之下被
// 删除的 registry 读起来是空的, 而不是报错。
func TestRecordsOnMissingFileIsAnEmptyRegistry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gone.jsonl")
	reg := &Registry{Path: path}
	recs, err := reg.Records()
	if err != nil || len(recs) != 0 {
		t.Fatalf("Records on a missing file = %v, %v; want empty, nil", recs, err)
	}
	if problems := readProblems(t, reg); len(problems) != 0 {
		t.Fatalf("Check on a missing file = %v, want no problems", problems)
	}
}

// ---------------------------------------------------------------------------
// 辅助函数
// ---------------------------------------------------------------------------

func jsonKeys(t *testing.T, v any) []string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("json.Unmarshal(%s): %v", b, err)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func keyDiff(got, want []string) (extra, missing []string) {
	wantSet := make(map[string]bool, len(want))
	for _, w := range want {
		wantSet[w] = true
	}
	gotSet := make(map[string]bool, len(got))
	for _, g := range got {
		gotSet[g] = true
		if !wantSet[g] {
			extra = append(extra, g)
		}
	}
	for _, w := range want {
		if !gotSet[w] {
			missing = append(missing, w)
		}
	}
	sort.Strings(extra)
	sort.Strings(missing)
	return extra, missing
}
