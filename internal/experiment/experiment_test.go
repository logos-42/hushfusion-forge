package experiment

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/logos-42/hushfusion-forge/internal/config"
	"github.com/logos-42/hushfusion-forge/internal/physics"
	"github.com/logos-42/hushfusion-forge/internal/registry"
	"github.com/logos-42/hushfusion-forge/internal/search"
)

func writeFileRaw(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}

// --- ApplyVariant -----------------------------------------------------------

func TestApplyVariantPerturbsExactlyOneField(t *testing.T) {
	spec := config.DefaultSpec()
	cases := []struct {
		key  string
		val  float64
		want func(config.Spec) float64
	}{
		{"b_ref", 0.8, func(s config.Spec) float64 { return s.BRef }},
		{"z_cell", 0.60, func(s config.Spec) float64 { return s.ZCell }},
		{"r_plasma", 0.12, func(s config.Spec) float64 { return s.RPlasma }},
	}
	for _, c := range cases {
		out, err := ApplyVariant(spec, Variant{Name: "t", Override: map[string]float64{c.key: c.val}})
		if err != nil {
			t.Fatalf("ApplyVariant(%s): unexpected error: %v", c.key, err)
		}
		if got := c.want(out); got != c.val {
			t.Errorf("ApplyVariant(%s): field = %v, want %v", c.key, got, c.val)
		}
		want := spec
		switch c.key {
		case "b_ref":
			want.BRef = c.val
		case "z_cell":
			want.ZCell = c.val
		case "r_plasma":
			want.RPlasma = c.val
		}
		if !reflect.DeepEqual(out, want) {
			t.Errorf("ApplyVariant(%s) changed more than the one field\ngot  %+v\nwant %+v", c.key, out, want)
		}
	}
}

func TestApplyVariantUnknownKeyIsAnError(t *testing.T) {
	spec := config.DefaultSpec()
	cases := []map[string]float64{
		{"n_coils": 6},
		{"b_ref": 0.8, "nope": 1}, // 合法的键不得夹带非法的键蒙混过关
		{"BRef": 0.8},
	}
	for _, override := range cases {
		out, err := ApplyVariant(spec, Variant{Name: "bad", Override: override})
		if err == nil {
			t.Fatalf("ApplyVariant(%v) silently accepted an unknown key", override)
		}
		for k := range override {
			if k == "n_coils" || k == "nope" || k == "BRef" {
				if !strings.Contains(err.Error(), k) {
					t.Errorf("error %q does not name the offending key %q", err, k)
				}
			}
		}
		if !reflect.DeepEqual(out, spec) {
			t.Errorf("ApplyVariant(%v) mutated the spec despite the error: %+v", override, out)
		}
	}
}

func TestApplyVariantEmptyOverrideIsIdentity(t *testing.T) {
	spec := config.DefaultSpec()
	out, err := ApplyVariant(spec, Variant{Name: "none"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(out, spec) {
		t.Fatalf("empty override changed the spec: %+v", out)
	}
}

func TestSpecVariantsAreNineSingleKeyPerturbations(t *testing.T) {
	if len(SpecVariants) != 9 {
		t.Fatalf("SpecVariants has %d entries, the generalisation probe is defined with 9", len(SpecVariants))
	}
	spec := config.DefaultSpec()
	seen := map[string]bool{}
	for _, v := range SpecVariants {
		if v.Name == "" {
			t.Errorf("variant with empty name: %+v", v)
		}
		if seen[v.Name] {
			t.Errorf("duplicate variant name %q", v.Name)
		}
		seen[v.Name] = true
		if len(v.Override) != 1 {
			t.Errorf("variant %q overrides %d keys; each variant is one requirement change", v.Name, len(v.Override))
		}
		out, err := ApplyVariant(spec, v)
		if err != nil {
			t.Fatalf("variant %q: %v", v.Name, err)
		}
		changed := map[string]float64{}
		if out.BRef != spec.BRef {
			changed["b_ref"] = out.BRef
		}
		if out.ZCell != spec.ZCell {
			changed["z_cell"] = out.ZCell
		}
		if out.RPlasma != spec.RPlasma {
			changed["r_plasma"] = out.RPlasma
		}
		if out.MinClearance != spec.MinClearance {
			changed["min_clearance"] = out.MinClearance
		}
		if len(changed) != 1 {
			t.Errorf("variant %q changed %d of the 4 supported keys, want 1", v.Name, len(changed))
			continue
		}
		for k, got := range changed {
			if want := v.Override[k]; got != want {
				t.Errorf("variant %q: %s = %v, want %v", v.Name, k, got, want)
			}
		}
	}
}

// --- Aggregate --------------------------------------------------------------

func TestAggregatePerMethodAcrossSeeds(t *testing.T) {
	const baseline = -0.2905708161
	runs := []search.Result{
		{Algorithm: "evolution", Seed: 0, Budget: 1000, BestScore: -0.30, EvalsToBeat: -1},
		{Algorithm: "evolution", Seed: 1, Budget: 1000, BestScore: -0.25, EvalsToBeat: 150},
		{Algorithm: "evolution", Seed: 2, Budget: 1000, BestScore: -0.10, EvalsToBeat: 90},
		{Algorithm: "evolution_warm", Seed: 0, Budget: 1000, BestScore: -0.20, EvalsToBeat: 100},
		{Algorithm: "evolution_warm", Seed: 1, Budget: 1000, BestScore: -0.15, EvalsToBeat: 200},
		{Algorithm: "random", Seed: 0, Budget: 1000, BestScore: -0.40, EvalsToBeat: -1},
		{Algorithm: "random", Seed: 1, Budget: 1000, BestScore: -0.20, EvalsToBeat: 260},
		{Algorithm: "lhs", Seed: 0, Budget: 1000, BestScore: -0.60, EvalsToBeat: -1},
		{Algorithm: "lhs", Seed: 1, Budget: 1000, BestScore: -0.50, EvalsToBeat: -1},
	}
	agg := Aggregate(runs, baseline)
	if len(agg) != 4 {
		t.Fatalf("Aggregate returned %d methods, want 4", len(agg))
	}

	mean := func(xs []float64) float64 {
		s := 0.0
		for _, x := range xs {
			s += x
		}
		return s / float64(len(xs))
	}
	sampleStd := func(xs []float64) float64 {
		m := mean(xs)
		ss := 0.0
		for _, x := range xs {
			ss += (x - m) * (x - m)
		}
		return math.Sqrt(ss / float64(len(xs)-1))
	}

	evo := agg["evolution"]
	if evo.NSeeds != 3 {
		t.Errorf("evolution NSeeds = %d, want 3", evo.NSeeds)
	}
	if evo.Budget != 1000 {
		t.Errorf("evolution Budget = %d, want 1000", evo.Budget)
	}
	scores := []float64{-0.30, -0.25, -0.10}
	if got, want := evo.BestMean, mean(scores); math.Abs(got-want) > 1e-12 {
		t.Errorf("BestMean = %.15g, want %.15g", got, want)
	}
	if got, want := evo.BestStd, sampleStd(scores); math.Abs(got-want) > 1e-12 {
		t.Errorf("BestStd = %.15g, want %.15g (sample std, ddof=1)", got, want)
	}
	if evo.BestMin != -0.30 || evo.BestMax != -0.10 {
		t.Errorf("BestMin/Max = %v/%v, want -0.30/-0.10", evo.BestMin, evo.BestMax)
	}
	// seed 0 (-0.30) 并没有超过基线 (-0.2905708161)：比较是严格的，
	// 差一点点也仍然算没超过
	if evo.NBeatingBaseline != 2 || math.Abs(evo.FracBeatingBaseline-2.0/3.0) > 1e-12 {
		t.Errorf("evolution NBeating/Frac = %d/%v, want 2/0.666..., (-0.30 < -0.2905708161)", evo.NBeatingBaseline, evo.FracBeatingBaseline)
	}
	if math.Abs(evo.EvalsToBeatMean-120) > 1e-12 || math.Abs(evo.EvalsToBeatMedian-120) > 1e-12 {
		t.Errorf("evolution EvalsToBeat mean/median = %v/%v, want 120/120 over the 2 seeds that beat it",
			evo.EvalsToBeatMean, evo.EvalsToBeatMedian)
	}
	if evo.BaselineScore != baseline {
		t.Errorf("BaselineScore = %v, want %v", evo.BaselineScore, baseline)
	}

	// seed 个数为偶数：中位数取中间两个值的均值
	warm := agg["evolution_warm"]
	if math.Abs(warm.EvalsToBeatMean-150) > 1e-12 || math.Abs(warm.EvalsToBeatMedian-150) > 1e-12 {
		t.Errorf("evolution_warm EvalsToBeat mean/median = %v/%v, want 150/150", warm.EvalsToBeatMean, warm.EvalsToBeatMedian)
	}

	// 两个 seed 中有一个超过基线：均值只对超过基线的那些 seed 取
	rand := agg["random"]
	if rand.NSeeds != 2 || rand.NBeatingBaseline != 1 || rand.FracBeatingBaseline != 0.5 {
		t.Errorf("random NSeeds/NBeating/Frac = %d/%d/%v, want 2/1/0.5", rand.NSeeds, rand.NBeatingBaseline, rand.FracBeatingBaseline)
	}
	if rand.EvalsToBeatMean != 260 || rand.EvalsToBeatMedian != 260 {
		t.Errorf("random EvalsToBeat mean/median = %v/%v, want 260/260", rand.EvalsToBeatMean, rand.EvalsToBeatMedian)
	}

	// 从未超过基线：-1，而不是 0，也不是编造出来的数字
	lhs := agg["lhs"]
	if lhs.NBeatingBaseline != 0 || lhs.FracBeatingBaseline != 0 {
		t.Errorf("lhs NBeating/Frac = %d/%v, want 0/0", lhs.NBeatingBaseline, lhs.FracBeatingBaseline)
	}
	if lhs.EvalsToBeatMean != -1 || lhs.EvalsToBeatMedian != -1 {
		t.Errorf("lhs EvalsToBeat mean/median = %v/%v, want -1/-1", lhs.EvalsToBeatMean, lhs.EvalsToBeatMedian)
	}
}

func TestAggregateIgnoresInconsistentEvalsToBeat(t *testing.T) {
	// 一次运行如果最优分数低于基线、却声称自己越过了基线，那就是自相矛盾的
	// 输入：它不能进入收敛统计（而收敛统计正是那个说明回路学得有多快的
	// 数字）
	runs := []search.Result{
		{Algorithm: "evolution", Seed: 0, Budget: 1000, BestScore: -0.40, EvalsToBeat: 210},
		{Algorithm: "evolution", Seed: 1, Budget: 1000, BestScore: -0.20, EvalsToBeat: 100},
	}
	agg := Aggregate(runs, -0.30)["evolution"]
	if agg.NBeatingBaseline != 1 {
		t.Fatalf("NBeatingBaseline = %d, want 1", agg.NBeatingBaseline)
	}
	if agg.EvalsToBeatMean != 100 || agg.EvalsToBeatMedian != 100 {
		t.Errorf("EvalsToBeat mean/median = %v/%v, want 100/100 (the inconsistent seed is excluded)",
			agg.EvalsToBeatMean, agg.EvalsToBeatMedian)
	}
}

func TestAggregateCountsEachSeedOnce(t *testing.T) {
	runs := []search.Result{
		{Algorithm: "evolution", Seed: 0, Budget: 1000, BestScore: -0.30},
		{Algorithm: "evolution", Seed: 0, Budget: 1000, BestScore: -0.20}, // seed 0 的重复项
		{Algorithm: "evolution", Seed: 1, Budget: 1000, BestScore: -0.10},
	}
	agg := Aggregate(runs, -0.5)["evolution"]
	if agg.NSeeds != 2 {
		t.Fatalf("NSeeds = %d, want 2 (a repeated seed is one seed)", agg.NSeeds)
	}
	if math.Abs(agg.BestMean-(-0.15)) > 1e-12 {
		t.Errorf("BestMean = %v, want -0.15 (last result for a repeated seed wins)", agg.BestMean)
	}
	if agg.BestMin != -0.20 || agg.BestMax != -0.10 {
		t.Errorf("BestMin/Max = %v/%v, want -0.20/-0.10", agg.BestMin, agg.BestMax)
	}
}

func TestAggregateEmpty(t *testing.T) {
	agg := Aggregate(nil, -0.3)
	if len(agg) != 0 {
		t.Fatalf("Aggregate(nil) returned %d entries, want 0", len(agg))
	}
	agg = Aggregate([]search.Result{}, -0.3)
	if len(agg) != 0 {
		t.Fatalf("Aggregate([]) returned %d entries, want 0", len(agg))
	}
}

func TestAggregateSingleSeedHasZeroStd(t *testing.T) {
	agg := Aggregate([]search.Result{{Algorithm: "random", Seed: 7, Budget: 10, BestScore: -0.42}}, -0.3)["random"]
	if agg.NSeeds != 1 {
		t.Fatalf("NSeeds = %d, want 1", agg.NSeeds)
	}
	if agg.BestStd != 0 {
		t.Errorf("BestStd = %v, want 0 for a single seed (no spread is measurable)", agg.BestStd)
	}
	if agg.BestMean != -0.42 || agg.BestMin != -0.42 || agg.BestMax != -0.42 {
		t.Errorf("single-seed mean/min/max = %v/%v/%v, want -0.42", agg.BestMean, agg.BestMin, agg.BestMax)
	}
}

// --- Report JSON ------------------------------------------------------------

// sampleReport 构造一个填充完整的 Report 字面量，覆盖每一个冻结键。
func sampleReport() *Report {
	return &Report{
		Meta: Meta{
			Tag:       "phase0-test",
			Timestamp: "2026-01-02T03:04:05Z",
			Budget:    1000,
			Seeds:     []int{0, 1},
			Methods:   []string{"random", "evolution"},
			GitCommit: "0123456789abcdef0123456789abcdef01234567",
			Platform:  "darwin/amd64",
			GoVersion: "go1.27.1",
			Workers:   1,
		},
		Spec:    config.DefaultSpec().AsMap(),
		Solver:  "analytic",
		CostRef: 1.791703035e12,
		Baseline: BaseRec{
			Name:      "textbook_mirror",
			Note:      "Helmholtz cell + two mirror throats",
			Score:     -0.2905708161,
			Feasible:  true,
			Terms:     map[string]float64{"field": 0.0, "mirror": 0.247530, "volume": 0.780886, "ripple": 0.0, "cost": 1.0},
			Weighted:  map[string]float64{"field": 0.0, "mirror": 0.123765, "volume": 0.5856645, "ripple": 0.0, "cost": -1.0},
			Penalties: map[string]float64{"conductor_field": 0.0, "coil_separation": 0.0, "not_a_mirror": 0.0},
			Metrics: physics.Metrics{
				BMidT: 1.0, BThroatT: 3.536386, ZThroatM: -0.9975, MirrorRatio: 3.536386,
				VolumeGood: 0.780886, Ripple: 0.0, BCoilMaxT: 3.416271, MinCoilGapM: 0.5,
				CostProxy: 1.791703035e12, NCoils: 4, MU0: config.MU0,
			},
			Design:    []float64{0.3, 0.5, 0.5, 0.3, -1.0, -0.25, 0.25, 1.0, 1621279.24, 463222.64, 463222.64, 1621279.24},
			CostProxy: 1.791703035e12,
			DesignID:  "D0001",
		},
		Runs: []search.Result{
			{
				Algorithm: "evolution", Seed: 0, Budget: 1000, NEvals: 1000,
				BestScore: -0.25, BestDesignID: "D0042", BestFeasible: true,
				BestTerms:   map[string]float64{"field": 0.01, "mirror": 0.30, "volume": 0.82, "ripple": 0.0, "cost": 0.95},
				BestMetrics: physics.Metrics{BMidT: 1.02, MirrorRatio: 3.9, VolumeGood: 0.82, NCoils: 4},
				BestDesign:  []float64{0.32, 0.5, 0.5, 0.29, -1.0, -0.25, 0.25, 1.0, 1.5e6, 4.6e5, 4.6e5, 1.5e6},
				EvalsToBeat: 210,
				History:     []float64{-0.9, -0.5, -0.25},
			},
			{
				Algorithm: "random", Seed: 0, Budget: 1000, NEvals: 1000,
				BestScore: -0.4, BestDesignID: "D0099", BestFeasible: true,
				BestTerms: map[string]float64{"cost": 1.4}, EvalsToBeat: -1,
			},
		},
		History: map[string][]float64{
			"evolution/seed=0": {-0.9, -0.5, -0.25},
			"random/seed=0":    {-1.1, -0.4},
		},
		Aggregate: map[string]Agg{
			"evolution": {
				NSeeds: 1, Budget: 1000, BestMean: -0.25, BestStd: 0, BestMin: -0.25, BestMax: -0.25,
				NBeatingBaseline: 1, FracBeatingBaseline: 1, EvalsToBeatMean: 210, EvalsToBeatMedian: 210,
				BaselineScore: -0.2905708161,
			},
			"random": {
				NSeeds: 1, Budget: 1000, BestMean: -0.4, BestStd: 0, BestMin: -0.4, BestMax: -0.4,
				NBeatingBaseline: 0, FracBeatingBaseline: 0, EvalsToBeatMean: -1, EvalsToBeatMedian: -1,
				BaselineScore: -0.2905708161,
			},
		},
		Robustness: Robustness{
			Variants:           []string{"b_ref=0.8T", "z_cell=0.60m"},
			BaselinePerVariant: map[string]float64{"b_ref=0.8T": -0.28, "z_cell=0.60m": -0.31},
			PerDesign: map[string]map[string]float64{
				"evolution":      {"b_ref=0.8T": 0.01, "z_cell=0.60m": -0.02},
				"human_baseline": {"b_ref=0.8T": -0.05, "z_cell=0.60m": -0.04},
			},
			Summary: map[string]RobustSummary{
				"evolution":      {MeanDeltaVsBaseline: -0.005, WorstDeltaVsBaseline: -0.02, NVariantsWinning: 1, NVariants: 2},
				"human_baseline": {MeanDeltaVsBaseline: -0.045, WorstDeltaVsBaseline: -0.05, NVariantsWinning: 0, NVariants: 2},
			},
		},
		Best: &BestRec{
			DesignID: "D0042", Algorithm: "evolution", Seed: 0, Score: -0.25,
			Terms:    map[string]float64{"field": 0.01, "mirror": 0.30, "volume": 0.82, "ripple": 0.0, "cost": 0.95},
			Metrics:  physics.Metrics{BMidT: 1.02, MirrorRatio: 3.9, VolumeGood: 0.82, NCoils: 4},
			Design:   []float64{0.32, 0.5, 0.5, 0.29, -1.0, -0.25, 0.25, 1.0, 1.5e6, 4.6e5, 4.6e5, 1.5e6},
			Feasible: true,
		},
		RegistrySummary: registry.Summary{
			NRecords: 3, NFeasible: 3,
			PerAlgo:       map[string]int{"human_baseline": 1, "evolution": 1, "random": 1},
			BestScore:     -0.25,
			BestDesignID:  "D0042",
			BestAlgorithm: "evolution",
		},
	}
}

func TestWriteLoadReportRoundTrip(t *testing.T) {
	rep := sampleReport()
	path := filepath.Join(t.TempDir(), "nested", "results.json")
	if err := WriteJSON(rep, path); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	back, err := LoadReport(path)
	if err != nil {
		t.Fatalf("LoadReport: %v", err)
	}
	// 在 JSON 层面比较：nil 与空切片、以及嵌套在 map[string]any 里的
	// []float64，在全量往返后都能存活，但 reflect.DeepEqual 会把它们标成
	// 不同，而这里的产物契约就是 JSON 字节本身。
	orig, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("marshal original: %v", err)
	}
	again, err := json.Marshal(back)
	if err != nil {
		t.Fatalf("marshal loaded: %v", err)
	}
	if string(orig) != string(again) {
		t.Fatalf("round trip is not idempotent\noriginal: %s\nloaded:   %s", orig, again)
	}
	if back.Best == nil || back.Best.DesignID != "D0042" {
		t.Errorf("loaded best = %+v, want the recorded best design", back.Best)
	}
	if back.Baseline.Terms["cost"] != 1.0 || back.Baseline.Metrics.MirrorRatio != 3.536386 {
		t.Errorf("loaded baseline lost data: terms=%v metrics=%+v", back.Baseline.Terms, back.Baseline.Metrics)
	}
	if back.Aggregate["evolution"].EvalsToBeatMedian != 210 {
		t.Errorf("loaded aggregate lost data: %+v", back.Aggregate["evolution"])
	}
}

func TestLoadReportMissingFile(t *testing.T) {
	if _, err := LoadReport(filepath.Join(t.TempDir(), "absent.json")); err == nil {
		t.Fatal("LoadReport on a missing file returned no error")
	}
}

func TestLoadReportMalformedJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.json")
	if err := WriteJSON(sampleReport(), path); err != nil {
		t.Fatal(err)
	}
	if err := writeFileRaw(path, "{ not json"); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadReport(path); err == nil {
		t.Fatal("LoadReport on malformed JSON returned no error")
	}
}

// TestReportJSONSchemaKeysFrozen 锁定 Python 辅助层
// (python/aux/analyze.py) 所读取的键。在这里改名就是一次 schema 迁移，
// 必须变红 (red)。
func TestReportJSONSchemaKeysFrozen(t *testing.T) {
	data, err := json.Marshal(sampleReport())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	sub := func(raw json.RawMessage) map[string]json.RawMessage {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("unmarshal sub-object: %v", err)
		}
		return m
	}

	assertKeysExact(t, "report", top, []string{
		"meta", "spec", "solver", "cost_ref", "baseline", "runs", "history",
		"aggregate", "robustness", "best", "registry_summary",
	})
	assertKeysExact(t, "meta", sub(top["meta"]), []string{
		"tag", "timestamp", "budget", "seeds", "methods", "git_commit", "platform", "go_version",
		"forge_version", "workers",
	})
	assertKeysExact(t, "baseline", sub(top["baseline"]), []string{
		"name", "note", "score", "feasible", "terms", "weighted", "penalties", "metrics", "design", "cost_proxy", "design_id",
	})
	assertKeysExact(t, "best", sub(top["best"]), []string{
		"design_id", "algorithm", "seed", "score", "terms", "metrics", "design", "feasible",
	})
	assertKeysExact(t, "robustness", sub(top["robustness"]), []string{
		"variants", "baseline_score_per_variant", "per_design", "summary",
	})
	assertKeysExact(t, "registry_summary", sub(top["registry_summary"]), []string{
		"n_records", "n_feasible", "per_algorithm", "best_score", "best_design_id", "best_algorithm",
	})
	// metrics 携带阶段 A 冻结的 JSON tag
	var metrics map[string]json.RawMessage
	if err := json.Unmarshal(sub(top["baseline"])["metrics"], &metrics); err != nil {
		t.Fatal(err)
	}
	assertKeysExact(t, "baseline.metrics", metrics, []string{
		"B_mid_T", "B_throat_T", "z_throat_m", "mirror_ratio", "volume_good", "ripple",
		"B_coil_max_T", "min_coil_gap_m", "min_clearance_m", "cost_proxy",
		"coil_proximity_floor_hit", "n_coils", "mu0",
	})

	var aggregate map[string]json.RawMessage
	if err := json.Unmarshal(top["aggregate"], &aggregate); err != nil {
		t.Fatal(err)
	}
	assertKeysExact(t, "aggregate.evolution", sub(aggregate["evolution"]), []string{
		"n_seeds", "budget", "best_mean", "best_std", "best_min", "best_max",
		"n_beating_baseline", "frac_beating_baseline", "evals_to_beat_mean", "evals_to_beat_median", "baseline_score",
	})

	var robustness struct {
		Summary map[string]json.RawMessage `json:"summary"`
	}
	if err := json.Unmarshal(top["robustness"], &robustness); err != nil {
		t.Fatal(err)
	}
	assertKeysExact(t, "robustness.summary.evolution", sub(robustness.Summary["evolution"]), []string{
		"mean_delta_vs_baseline", "worst_delta_vs_baseline", "n_variants_winning", "n_variants",
	})

	// runs[] 是 search.Result，由阶段 D 冻结：这里断言的是包含关系而不是
	// 相等，这样那边做增量改动时不会把这道门变红。
	var runs []map[string]json.RawMessage
	if err := json.Unmarshal(top["runs"], &runs); err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("runs has %d entries, want 2", len(runs))
	}
	assertKeysContain(t, "runs[0]", runs[0], []string{
		"algorithm", "seed", "budget", "n_evals", "best_score", "best_design", "best_terms",
		"best_metrics", "best_design_id", "best_feasible", "evals_to_beat", "history",
	})
}

func assertKeysExact(t *testing.T, label string, got map[string]json.RawMessage, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: %d keys, want %d\ngot:  %s\nwant: %s",
			label, len(got), len(want), sortedKeys(got), strings.Join(want, ", "))
	}
	for _, k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("%s: missing frozen key %q (got %s)", label, k, sortedKeys(got))
		}
	}
}

func assertKeysContain(t *testing.T, label string, got map[string]json.RawMessage, want []string) {
	t.Helper()
	for _, k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("%s: missing key %q (got %s)", label, k, sortedKeys(got))
		}
	}
}

func sortedKeys(m map[string]json.RawMessage) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}

// --- 端到端入口点的前置条件 -------------------------------------------------

func TestRunBenchmarkRejectsNilRegistry(t *testing.T) {
	if _, err := RunBenchmark(nil, config.DefaultSpec(), Opts{}); err == nil {
		t.Fatal("RunBenchmark(nil registry) returned no error")
	}
}

func TestRunBenchmarkRejectsNonEmptyRegistry(t *testing.T) {
	reg, err := registry.Open(filepath.Join(t.TempDir(), "registry.jsonl"))
	if err != nil {
		t.Fatalf("registry.Open: %v", err)
	}
	if err := reg.Append(registry.Record{
		Algorithm: "human_baseline", Seed: 0, Score: -0.3, Feasible: true,
		Params: registry.Params{RadiusM: []float64{0.3}, ZM: []float64{0}, CurrentA: []float64{1e6}},
		Terms:  map[string]float64{"cost": 1.0},
	}); err != nil {
		t.Fatalf("registry.Append: %v", err)
	}
	if _, err := RunBenchmark(reg, config.DefaultSpec(), Opts{}); err == nil {
		t.Fatal("RunBenchmark accepted a non-empty registry; the D0001 root claim would be false")
	}
}

func TestRobustnessProbeRejectsUnknownVariantKey(t *testing.T) {
	spec := config.DefaultSpec()
	designs := map[string][]float64{"d": make([]float64, spec.NParams())}
	_, err := RobustnessProbe(spec, designs, []Variant{{Name: "bad", Override: map[string]float64{"n_coils": 6}}})
	if err == nil {
		t.Fatal("RobustnessProbe silently ignored an unknown variant key")
	}
	if !strings.Contains(err.Error(), "n_coils") {
		t.Errorf("error %q does not name the offending key", err)
	}
}

func TestRobustnessProbeRejectsWrongLengthDesign(t *testing.T) {
	spec := config.DefaultSpec()
	designs := map[string][]float64{"short": make([]float64, spec.NParams()-1)}
	_, err := RobustnessProbe(spec, designs, SpecVariants[:1])
	if err == nil {
		t.Fatal("RobustnessProbe accepted a design vector of the wrong length")
	}
	if !strings.Contains(err.Error(), "short") {
		t.Errorf("error %q does not name the offending design", err)
	}
}

// TestGitCommitFormatOnly 说明溯源信息是尽力而为的：在本检出目录内它必须是
// 完整的 sha，而且绝不能是编造的值。
func TestGitCommitIsAShaOrEmpty(t *testing.T) {
	got := gitCommit()
	if got == "" {
		t.Skip("not inside a git checkout; empty is the honest answer")
	}
	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(got) {
		t.Fatalf("gitCommit = %q, want a 40-hex sha", got)
	}
}
