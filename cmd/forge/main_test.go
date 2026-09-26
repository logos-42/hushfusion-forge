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
)

// These tests cover the wiring itself: the frozen schema accessors the CLI
// compares through, the parsing helpers, the tolerance rule of the golden gates
// and the record the CLI writes into the registry. None of them evaluates
// physics or search, so they stay green while other stages are still skeletons.

// TestMetricAccessorCoversFrozenKeys is a schema-drift guard: the metric keys the
// CLI compares and prints must cover the frozen physics.Metrics JSON tags and the
// frozen observation keys of rlenv.
func TestMetricAccessorCoversFrozenKeys(t *testing.T) {
	spec := config.DefaultSpec()
	m := physics.Metrics{
		BMidT: 1, BThroatT: 2, ZThroatM: -1, MirrorRatio: 2, VolumeGood: 0.5,
		Ripple: 0.1, BCoilMaxT: 3, MinCoilGapM: 0.4, CostProxy: 1e12,
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
	// The observation keys rlenv appends must be printable too.
	for _, k := range rlenv.ObsMetricKeys {
		if _, ok := metricValue(m, k); !ok {
			t.Errorf("rlenv observation key %q is not covered by metricValue", k)
		}
	}
	// The bool metric must survive the float encoding used in the golden file.
	if v, _ := metricValue(m, "coil_proximity_floor_hit"); v != 1 {
		t.Errorf("coil_proximity_floor_hit = %v, want 1 for true", v)
	}
}

// TestRelDiffZeroGoldenRule documents the comparison rule of the golden gates:
// relative for values of real size, absolute for values that are zero by
// construction (a relative comparison against 0 is undefined, not strict).
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

// TestSplitListAndInts covers the flag-value parsing the CLI relies on.
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

// TestFindFileSearchesUpward proves the golden-path resolution works from a
// package subdirectory (cmd/forge) and that a missing file is an error, not an
// empty comparison.
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

// TestBaselineRecordShape checks the record the CLI writes: the design vector is
// split per coil, and every registry required field is present in the JSON that
// reaches the registry (the schema-parity gate's Go half).
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

// TestXcheckSchemaMatchesGolden is the schema half of the oracle hand-off: the
// JSON keys of the exported field samples must be exactly the keys of
// testdata/golden_field_samples.json, otherwise python/aux/oracle.py cannot
// recompute them (acceptance gate G5).
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

	// Same point budget as the anchor file, so the two exports are comparable
	// row by row.
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

// metricsFromGolden maps the golden metrics object onto physics.Metrics, so the
// comparison functions can be fed the anchor values themselves.
func metricsFromGolden(m map[string]float64) physics.Metrics {
	return physics.Metrics{
		BMidT: m["B_mid_T"], BThroatT: m["B_throat_T"], ZThroatM: m["z_throat_m"],
		MirrorRatio: m["mirror_ratio"], VolumeGood: m["volume_good"], Ripple: m["ripple"],
		BCoilMaxT: m["B_coil_max_T"], MinCoilGapM: m["min_coil_gap_m"], CostProxy: m["cost_proxy"],
		CoilProximityFloorHit: m["coil_proximity_floor_hit"] != 0,
		NCoils:                int(m["n_coils"]), MU0: m["mu0"],
	}
}

// TestGoldenComparisonIsGreenOnGoldenAndRedOnDeviation feeds the golden values
// themselves through the comparison the acceptance gate uses: it must be green,
// and it must go red as soon as the score or a metric is off by more than the
// contract's tolerance. (A gate that cannot go red is not a gate — and this is
// the split-out comparison, so it needs no physics to run.)
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

	// 1e-4 off on the score must be red.
	badScore := exact
	badScore.Score += 1e-4
	if _, err := compareBaselineEval(badScore, st.gold); err == nil {
		t.Error("score comparison stayed green on a 1e-4 deviation")
	}
	// A missing term must be red, not silently skipped.
	badTerms := exact
	badTerms.Terms = map[string]float64{}
	if _, err := compareBaselineEval(badTerms, st.gold); err == nil {
		t.Error("term comparison stayed green with every term missing")
	}
	// A 1e-3 relative deviation on a metric of real size must be red.
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
	// A metric that the golden file pins at zero is compared absolutely: 1e-3
	// counts as a real deviation, not as "relative to nothing".
	if st.gold.Metrics["ripple"] != 0 {
		t.Fatalf("this test assumes the golden ripple is 0, got %v", st.gold.Metrics["ripple"])
	}
	bad["ripple"] = 1e-3
	if _, err := compareBaselineMetrics(metricsFromGolden(bad), st.gold.Metrics); err == nil {
		t.Error("metric comparison stayed green on a 1e-3 absolute deviation of a zero-valued metric")
	}
}

// TestParseFlagsExitCodes pins the CLI's exit-code contract: --help is success,
// a bad flag or a stray positional argument is a usage error.
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

// TestVersionCommandRuns is the cheapest smoke test of the wiring: 'forge
// version' touches config, rlenv, registry and search without evaluating
// anything.
func TestVersionCommandRuns(t *testing.T) {
	if code := cmdVersion(nil); code != 0 {
		t.Fatalf("cmdVersion = %d, want 0", code)
	}
}
