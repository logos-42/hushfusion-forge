package knowledge

import (
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/logos-42/hushfusion-forge/internal/config"
	"github.com/logos-42/hushfusion-forge/internal/registry"
)

func itoa(i int) string { return strconv.Itoa(i) }

func pad3(i int) string { return fmt.Sprintf("%03d", i) }

// --- Spearman ---------------------------------------------------------------

// TestSpearmanHandComputedAnchors are the coefficients worked out by hand
// (rank transform + Pearson on ranks), so the implementation is pinned to
// arithmetic, not to itself.
func TestSpearmanHandComputedAnchors(t *testing.T) {
	cases := []struct {
		name string
		x, y []float64
		want float64
	}{
		{"perfect positive", []float64{1, 2, 3, 4, 5}, []float64{2, 4, 6, 8, 10}, 1.0},
		{"negative", []float64{1, 2, 3, 4, 5}, []float64{5, 3, 4, 1, 2}, -0.8},
		{"ties: 4/sqrt(20)", []float64{1, 1, 2, 2}, []float64{1, 2, 3, 4}, 4 / math.Sqrt(20)},
		{"perfect negative", []float64{1, 2, 3, 4, 5}, []float64{5, 4, 3, 2, 1}, -1.0},
		{"constant x", []float64{3, 3, 3, 3}, []float64{1, 2, 3, 4}, 0.0},
		{"single pair", []float64{1, 2}, []float64{9, 7}, -1.0},
		{"n = 1", []float64{1}, []float64{1}, 0.0},
		{"empty", nil, nil, 0.0},
		{"length mismatch", []float64{1, 2, 3}, []float64{1, 2}, 0.0},
	}
	for _, c := range cases {
		got := Spearman(c.x, c.y)
		if math.Abs(got-c.want) > 1e-12 {
			t.Errorf("%s: Spearman = %.16g, want %.16g", c.name, got, c.want)
		}
	}
}

// naiveRank is an O(n^2) rank transform written independently of the
// implementation: rank = #(strictly smaller) + (ties including itself)/2.
func naiveRank(v []float64) []float64 {
	out := make([]float64, len(v))
	for i := range v {
		less, eq := 0, 0
		for j := range v {
			switch {
			case v[j] < v[i]:
				less++
			case v[j] == v[i]:
				eq++
			}
		}
		out[i] = float64(less) + (float64(eq)+1)/2
	}
	return out
}

// naivePearson is a second, independent Pearson implementation.
func naivePearson(a, b []float64) float64 {
	if len(a) != len(b) || len(a) < 2 {
		return 0
	}
	ma, mb := 0.0, 0.0
	for i := range a {
		ma += a[i]
		mb += b[i]
	}
	ma /= float64(len(a))
	mb /= float64(len(b))
	cov, va, vb := 0.0, 0.0, 0.0
	for i := range a {
		cov += (a[i] - ma) * (b[i] - mb)
		va += (a[i] - ma) * (a[i] - ma)
		vb += (b[i] - mb) * (b[i] - mb)
	}
	if va == 0 || vb == 0 {
		return 0
	}
	return cov / math.Sqrt(va*vb)
}

// TestSpearmanEqualsPearsonOnRanks is the property the frozen doc demands: with
// random vectors, including many ties, Spearman(x, y) must equal
// Pearson(rank(x), rank(y)) computed by an independent implementation.
func TestSpearmanEqualsPearsonOnRanks(t *testing.T) {
	rng := rand.New(rand.NewSource(20260101))
	for trial := 0; trial < 500; trial++ {
		n := 2 + rng.Intn(40)
		x := make([]float64, n)
		y := make([]float64, n)
		for i := range x {
			// small integer pool: guarantees a lot of tied ranks
			x[i] = float64(rng.Intn(1 + rng.Intn(6)))
			y[i] = float64(rng.Intn(1 + rng.Intn(n)))
		}
		got := Spearman(x, y)
		want := naivePearson(naiveRank(x), naiveRank(y))
		if math.Abs(got-want) > 1e-12 {
			t.Fatalf("trial %d (n=%d): Spearman = %.16g, Pearson-on-ranks = %.16g\nx=%v\ny=%v",
				trial, n, got, want, x, y)
		}
		if got < -1 || got > 1 {
			t.Fatalf("trial %d: Spearman = %v outside [-1, 1]", trial, got)
		}
	}
}

func TestSpearmanSymmetrySignFlipAndInvariance(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for trial := 0; trial < 50; trial++ {
		n := 5 + rng.Intn(20)
		x := make([]float64, n)
		y := make([]float64, n)
		for i := range x {
			x[i] = rng.NormFloat64()
			y[i] = rng.NormFloat64()
		}
		// symmetric in its arguments: rho(x, y) == rho(y, x)
		if math.Abs(Spearman(x, y)-Spearman(y, x)) > 1e-12 {
			t.Fatalf("Spearman is not symmetric: %v vs %v", Spearman(x, y), Spearman(y, x))
		}
		// reversing one variable's order flips the sign
		neg := make([]float64, n)
		for i := range y {
			neg[i] = -y[i]
		}
		if math.Abs(Spearman(x, neg)+Spearman(x, y)) > 1e-12 {
			t.Fatalf("Spearman did not flip under a sign reversal: %v vs %v", Spearman(x, neg), Spearman(x, y))
		}
		// a strictly increasing transform of x must not change the rank order
		inc := make([]float64, n)
		for i := range x {
			inc[i] = math.Exp(x[i])
		}
		if math.Abs(Spearman(inc, y)-Spearman(x, y)) > 1e-12 {
			t.Fatalf("Spearman changed under a strictly increasing transform: %v vs %v", Spearman(inc, y), Spearman(x, y))
		}
	}
}

func TestSpearmanNaNAndInfDoNotProduceNaN(t *testing.T) {
	if got := Spearman([]float64{1, math.NaN(), 3}, []float64{1, 2, 3}); got != 0 {
		t.Errorf("Spearman with NaN = %v, want 0 (never NaN)", got)
	}
}

// --- ParameterNames ---------------------------------------------------------

func TestParameterNamesMatchDesignVectorLayout(t *testing.T) {
	spec := config.DefaultSpec()
	names := ParameterNames(spec)
	if len(names) != spec.NParams() {
		t.Fatalf("ParameterNames has %d entries, spec.NParams() = %d", len(names), spec.NParams())
	}
	k := spec.NCoils
	for i := 0; i < k; i++ {
		if names[i] != "r_"+itoa(i) {
			t.Errorf("names[%d] = %q, want r_%d", i, names[i], i)
		}
		if names[k+i] != "z_"+itoa(i) {
			t.Errorf("names[%d] = %q, want z_%d", k+i, names[k+i], i)
		}
		if names[2*k+i] != "I_"+itoa(i) {
			t.Errorf("names[%d] = %q, want I_%d", 2*k+i, names[2*k+i], i)
		}
	}
	// the name must address the same slot the search optimises
	if err := checkNameAddressing(spec); err != nil {
		t.Fatal(err)
	}
}

func checkNameAddressing(spec config.Spec) error {
	lower, upper := spec.Lower(), spec.Upper()
	for i, name := range ParameterNames(spec) {
		x := make([]float64, spec.NParams())
		for j := range x {
			x[j] = 0.5 * (lower[j] + upper[j])
		}
		x[i] = upper[i]
		r := Rule{Parameter: name, Rho: 1}
		if got := RuleExpectation([]Rule{r}, x, spec); math.Abs(got-0.5) > 1e-12 {
			return fmt.Errorf("parameter %s does not address design slot %d (expectation = %v, want 0.5)", name, i, got)
		}
	}
	return nil
}

// --- MineRules --------------------------------------------------------------

// mkRun builds one run of n feasible records. Every parameter rises with the
// record index (so its Spearman correlation with the rising term `cost` is
// exactly +1) except the names listed in `reverse`, which fall with the index
// (correlation exactly -1).
func mkRun(t *testing.T, algo string, seed, n int, reverse map[string]bool) []registry.Record {
	t.Helper()
	spec := config.DefaultSpec()
	k := spec.NCoils
	lo := spec.Bounds.Radius
	zlo, zhi := spec.Bounds.Z[0], spec.Bounds.Z[1]
	ilo, ihi := spec.Bounds.Current[0], spec.Bounds.Current[1]
	recs := make([]registry.Record, 0, n)
	for i := 0; i < n; i++ {
		f := 0.0
		if n > 1 {
			f = float64(i) / float64(n-1)
		}
		pick := func(name string, a, b float64) float64 {
			if reverse[name] {
				return b - (b-a)*f
			}
			return a + (b-a)*f
		}
		p := registry.Params{
			RadiusM:  make([]float64, k),
			ZM:       make([]float64, k),
			CurrentA: make([]float64, k),
		}
		for c := 0; c < k; c++ {
			p.RadiusM[c] = pick("r_"+itoa(c), lo[0], lo[1])
			p.ZM[c] = pick("z_"+itoa(c), zlo, zhi)
			p.CurrentA[c] = pick("I_"+itoa(c), ilo, ihi)
		}
		recs = append(recs, registry.Record{
			ExperimentID: i + 1,
			DesignID:     algo + "-" + itoa(seed) + "-" + itoa(i),
			Algorithm:    algo,
			Seed:         seed,
			EvalIndex:    i,
			Score:        -1.0 + 0.9*f,
			Feasible:     true,
			Params:       p,
			Terms:        map[string]float64{"cost": 0.5 + 0.9*f},
		})
	}
	return recs
}

// mineFixture is three surviving runs (24 records each) plus one run too small
// to count and one infeasible record that would break the r_0 correlation if it
// were allowed in.
func mineFixture(t *testing.T) []registry.Record {
	t.Helper()
	recs := []registry.Record{}
	recs = append(recs, mkRun(t, "evolution", 0, 24, nil)...)
	recs = append(recs, mkRun(t, "evolution", 1, 24, nil)...)
	// lhs seed 0 has z_1 anti-correlated with cost: the candidate rule for
	// (z_1, cost) is not replicated and must be dropped.
	recs = append(recs, mkRun(t, "lhs", 0, 24, map[string]bool{"z_1": true})...)
	// a run with fewer than max(20, MinN/8) records is dropped entirely
	recs = append(recs, mkRun(t, "random", 0, 5, nil)...)
	// one infeasible record with a tied r_0 and the highest cost: if infeasible
	// records leaked in, the r_0 correlation would drop below 1
	bad := mkRun(t, "evolution", 0, 1, nil)[0]
	bad.Feasible = false
	bad.Params.RadiusM[0] = config.DefaultSpec().Bounds.Radius[0]
	bad.Terms["cost"] = 9.0
	recs = append(recs, bad)
	return recs
}

func TestMineRulesKeepsOnlyReplicatedSigns(t *testing.T) {
	spec := config.DefaultSpec()
	recs := mineFixture(t)
	rules := MineRules(recs, spec, MineOpts{MinN: 150, MinAbsRho: 0.20, TopK: 12})

	// 12 parameters x 1 term = 12 candidates, one of which (z_1) is not
	// replicated across runs.
	if len(rules) != 11 {
		t.Fatalf("mined %d rules, want 11 (12 candidates minus the un-replicated z_1); params: %v",
			len(rules), paramsOf(rules))
	}
	for _, r := range rules {
		if r.Parameter == "z_1" {
			t.Errorf("rule for z_1 survived although one run has the opposite sign: %+v", r)
		}
		if r.Term != "cost" {
			t.Errorf("unexpected term %q", r.Term)
		}
		if math.Abs(r.Rho-1.0) > 1e-12 {
			t.Errorf("%s: rho = %.16g, want 1.0 (worst-case |rho| across runs)", r.Parameter, r.Rho)
		}
		if r.SignAgreement != 1.0 {
			t.Errorf("%s: sign agreement = %v, want 1.0", r.Parameter, r.SignAgreement)
		}
		// three runs of 24 feasible records; the 5-record run and the
		// infeasible record must not be counted
		if r.NRuns != 3 {
			t.Errorf("%s: n_runs = %d, want 3", r.Parameter, r.NRuns)
		}
		if r.NDesigns != 72 {
			t.Errorf("%s: n_designs = %d, want 72 (feasible records of surviving runs only)", r.Parameter, r.NDesigns)
		}
		if !strings.Contains(r.Scope, "search box") || !strings.Contains(r.Scope, "NO plasma") {
			t.Errorf("%s: scope note is not written down: %q", r.Parameter, r.Scope)
		}
		if r.Statement == "" || r.StatementEN == "" {
			t.Errorf("%s: missing statement (CN/EN)", r.Parameter)
		}
		if r.RuleID == "" {
			t.Errorf("%s: missing rule_id", r.Parameter)
		}
	}

	// ranking: |rho| descending, ids assigned by rank
	for i := 1; i < len(rules); i++ {
		if math.Abs(rules[i-1].Rho) < math.Abs(rules[i].Rho) {
			t.Errorf("rules are not sorted by |rho|: %v before %v", rules[i-1].Rho, rules[i].Rho)
		}
	}
	for i, r := range rules {
		if want := "R" + pad3(i+1); r.RuleID != want {
			t.Errorf("rules[%d].RuleID = %q, want %q", i, r.RuleID, want)
		}
	}

	// decile contrast carries the magnitude: cost rises with r_0, so the top
	// decile of r_0 must sit above the bottom decile
	r0 := ruleFor(t, rules, "r_0", "cost")
	if !(r0.DecileHigh > r0.DecileLow) {
		t.Errorf("r_0/cost: decile_high = %v should exceed decile_low = %v for a positive rho", r0.DecileHigh, r0.DecileLow)
	}
}

func TestMineRulesTopK(t *testing.T) {
	spec := config.DefaultSpec()
	recs := mineFixture(t)
	rules := MineRules(recs, spec, MineOpts{MinN: 150, MinAbsRho: 0.20, TopK: 3})
	if len(rules) != 3 {
		t.Fatalf("mined %d rules with TopK=3, want 3", len(rules))
	}
	if rules[0].RuleID != "R001" || rules[2].RuleID != "R003" {
		t.Errorf("rule ids = %q..%q, want R001..R003", rules[0].RuleID, rules[2].RuleID)
	}
}

func TestMineRulesReportsWorstCaseAbsRho(t *testing.T) {
	spec := config.DefaultSpec()
	// two runs; r_0 rises with cost in both, but the second run's relation is
	// weaker (the cost sequence is reversed over its second half), so the
	// reported rho must be the smaller |rho|
	run0 := mkRun(t, "evolution", 0, 20, nil)
	run1 := mkRun(t, "evolution", 1, 20, nil)
	half := len(run1) / 2
	vals := make([]float64, half)
	for i := 0; i < half; i++ {
		vals[i] = run1[i+half].Terms["cost"]
	}
	for i := 0; i < half; i++ {
		run1[i+half].Terms["cost"] = vals[half-1-i]
	}
	// single out r_0: every other parameter is made constant, which has no
	// rank correlation at all and so cannot form a rule
	for _, recs := range [][]registry.Record{run0, run1} {
		for i := range recs {
			for c := 1; c < spec.NCoils; c++ {
				recs[i].Params.RadiusM[c] = 0.5
				recs[i].Params.ZM[c] = 0
				recs[i].Params.CurrentA[c] = 1e6
			}
			for c := 0; c < spec.NCoils; c++ {
				recs[i].Params.ZM[c] = 0
				recs[i].Params.CurrentA[c] = 1e6
			}
		}
	}
	recs := append(append([]registry.Record{}, run0...), run1...)

	rho0 := Spearman(paramColumn(run0, 0), termColumn(run0, "cost"))
	rho1 := Spearman(paramColumn(run1, 0), termColumn(run1, "cost"))
	if math.Abs(rho0-1.0) > 1e-12 {
		t.Fatalf("fixture broken: run0 rho = %v, want 1.0", rho0)
	}
	if rho1 <= 0 || rho1 >= 1 {
		t.Fatalf("fixture broken: run1 rho = %v, want a same-signed but weaker correlation", rho1)
	}

	rules := MineRules(recs, spec, MineOpts{MinN: 150, MinAbsRho: 0.05, TopK: 12})
	if len(rules) != 1 {
		t.Fatalf("mined %d rules, want exactly 1 (r_0/cost); got %+v", len(rules), rules)
	}
	got := rules[0]
	if got.Parameter != "r_0" || got.Term != "cost" {
		t.Fatalf("rule = %s/%s, want r_0/cost", got.Parameter, got.Term)
	}
	want := math.Min(math.Abs(rho0), math.Abs(rho1))
	if math.Abs(got.Rho-want) > 1e-12 {
		t.Errorf("rho = %.16g, want %.16g (the worst-case |rho|, not the best)", got.Rho, want)
	}
	if got.NRuns != 2 || got.NDesigns != 40 {
		t.Errorf("n_runs/n_designs = %d/%d, want 2/40", got.NRuns, got.NDesigns)
	}
}

func TestMineRulesRefusesSingleRunOrNoFeasibleRecords(t *testing.T) {
	spec := config.DefaultSpec()
	// one surviving run + one too small: replication is impossible
	recs := append([]registry.Record{}, mkRun(t, "evolution", 0, 24, nil)...)
	recs = append(recs, mkRun(t, "random", 0, 4, nil)...)
	if rules := MineRules(recs, spec, MineOpts{}); rules != nil {
		t.Errorf("mined %d rules from a single run, want none", len(rules))
	}
	// nothing feasible at all
	all := mkRun(t, "evolution", 0, 24, nil)
	for i := range all {
		all[i].Feasible = false
	}
	if rules := MineRules(all, spec, MineOpts{}); rules != nil {
		t.Errorf("mined %d rules with no feasible records, want none", len(rules))
	}
	// no records at all
	if rules := MineRules(nil, spec, MineOpts{}); rules != nil {
		t.Errorf("mined %d rules from no records, want none", len(rules))
	}
}

func TestMineRulesMinAbsRhoThreshold(t *testing.T) {
	spec := config.DefaultSpec()
	recs := mineFixture(t)
	// every candidate in the fixture has |rho| == 1.0, so a threshold above 1.0
	// must drop all of them (MinAbsRho <= 0 would mean "use the default")
	if rules := MineRules(recs, spec, MineOpts{MinAbsRho: 1.5}); len(rules) != 0 {
		t.Errorf("mined %d rules with MinAbsRho = 1.5, want none", len(rules))
	}
	// the zero value of MineOpts must mean the documented defaults (150, 0.20, 12)
	def := MineRules(recs, spec, MineOpts{})
	explicit := MineRules(recs, spec, MineOpts{MinN: 150, MinAbsRho: 0.20, TopK: 12})
	if !reflect.DeepEqual(def, explicit) {
		t.Errorf("zero-value MineOpts does not mean the documented defaults:\nzero:     %+v\nexplicit: %+v", def, explicit)
	}
}

func paramsOf(rules []Rule) []string {
	out := make([]string, 0, len(rules))
	for _, r := range rules {
		out = append(out, r.Parameter+"/"+r.Term)
	}
	return out
}

func ruleFor(t *testing.T, rules []Rule, param, term string) Rule {
	t.Helper()
	for _, r := range rules {
		if r.Parameter == param && r.Term == term {
			return r
		}
	}
	t.Fatalf("no rule for %s/%s in %v", param, term, paramsOf(rules))
	return Rule{}
}

func paramColumn(recs []registry.Record, i int) []float64 {
	out := make([]float64, 0, len(recs))
	for _, r := range recs {
		v, _ := paramValue(r, i)
		out = append(out, v)
	}
	return out
}

func termColumn(recs []registry.Record, term string) []float64 {
	out := make([]float64, 0, len(recs))
	for _, r := range recs {
		out = append(out, r.Terms[term])
	}
	return out
}

// --- rules round trip -------------------------------------------------------

func TestWriteRulesMDLoadRulesRoundTrip(t *testing.T) {
	spec := config.DefaultSpec()
	rules := MineRules(mineFixture(t), spec, MineOpts{MinN: 150, MinAbsRho: 0.20, TopK: 4})
	if len(rules) == 0 {
		t.Fatal("fixture produced no rules; the round trip would be vacuous")
	}
	path := filepath.Join(t.TempDir(), "nested", "rules.md")
	if err := WriteRulesMD(rules, path, spec, 101, "phase0-test"); err != nil {
		t.Fatalf("WriteRulesMD: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	md := string(raw)
	for _, want := range []string{"phase0-test", "| rule_id |", "## Scope", "## 判定口径", "```json", "search box", "101 条 registry record"} {
		if !strings.Contains(md, want) {
			t.Errorf("rules markdown is missing %q", want)
		}
	}
	back, err := LoadRules(path)
	if err != nil {
		t.Fatalf("LoadRules: %v", err)
	}
	if !reflect.DeepEqual(back, rules) {
		t.Fatalf("round trip changed the rules\nwant %+v\ngot  %+v", rules, back)
	}
}

func TestWriteRulesMDEmptyAndLoadRules(t *testing.T) {
	spec := config.DefaultSpec()
	path := filepath.Join(t.TempDir(), "rules.md")
	if err := WriteRulesMD(nil, path, spec, 0, ""); err != nil {
		t.Fatalf("WriteRulesMD(nil): %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "本批没有通过复制检验") {
		t.Error("an empty rule set is not stated explicitly in the markdown")
	}
	if !strings.Contains(string(raw), "```json") {
		t.Error("the machine-readable block is missing for an empty rule set")
	}
	back, err := LoadRules(path)
	if err != nil {
		t.Fatalf("LoadRules on an empty rule file: %v", err)
	}
	if len(back) != 0 {
		t.Fatalf("LoadRules returned %d rules, want 0", len(back))
	}
}

func TestLoadRulesWithoutJSONBlockIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.md")
	if err := os.WriteFile(path, []byte("# rules\n\nno fenced block here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRules(path); err == nil {
		t.Fatal("LoadRules accepted a file with no embedded ```json block")
	}
	if _, err := LoadRules(filepath.Join(t.TempDir(), "absent.md")); err == nil {
		t.Fatal("LoadRules accepted a missing file")
	}
}

func TestLoadRulesRejectsGarbageJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.md")
	if err := os.WriteFile(path, []byte("```json\n{not a list}\n```\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRules(path); err == nil {
		t.Fatal("LoadRules accepted malformed JSON")
	}
}

// --- RuleExpectation --------------------------------------------------------

func TestRuleExpectationLinearPrior(t *testing.T) {
	spec := config.DefaultSpec()
	lower, upper := spec.Lower(), spec.Upper()
	build := func(slot int, frac float64) []float64 {
		x := make([]float64, spec.NParams())
		for i := range x {
			x[i] = 0.5 * (lower[i] + upper[i])
		}
		x[slot] = lower[slot] + frac*(upper[slot]-lower[slot])
		return x
	}
	r := Rule{Parameter: "r_0", Term: "volume", Rho: 1.0}

	if got := RuleExpectation(nil, build(0, 1), spec); got != 0 {
		t.Errorf("no rules: expectation = %v, want 0", got)
	}
	if got := RuleExpectation([]Rule{r}, build(0, 1), spec); math.Abs(got-0.5) > 1e-12 {
		t.Errorf("parameter at the box maximum: expectation = %v, want +0.5", got)
	}
	if got := RuleExpectation([]Rule{r}, build(0, 0), spec); math.Abs(got+0.5) > 1e-12 {
		t.Errorf("parameter at the box minimum: expectation = %v, want -0.5", got)
	}
	if got := RuleExpectation([]Rule{r}, build(0, 0.5), spec); math.Abs(got) > 1e-12 {
		t.Errorf("parameter at the box centre: expectation = %v, want 0", got)
	}

	// two rules average, and each uses its own slot
	r2 := Rule{Parameter: "z_1", Term: "volume", Rho: -1.0}
	x := build(0, 1)
	slot := spec.NCoils + 1
	x[slot] = lower[slot]
	if got := RuleExpectation([]Rule{r, r2}, x, spec); math.Abs(got-0.5) > 1e-12 {
		t.Errorf("two rules: expectation = %v, want +0.5 ((0.5 + 0.5)/2)", got)
	}

	// a rule naming a parameter that does not exist does not dilute the mean
	if got := RuleExpectation([]Rule{r, {Parameter: "q_9", Rho: 1}}, build(0, 1), spec); math.Abs(got-0.5) > 1e-12 {
		t.Errorf("unknown parameter name: expectation = %v, want +0.5", got)
	}
	// out-of-box position clamps instead of extrapolating
	if got := RuleExpectation([]Rule{r}, lower, spec); math.Abs(got+0.5) > 1e-12 {
		t.Errorf("clamping: expectation = %v, want -0.5", got)
	}
	if got := RuleExpectation([]Rule{r}, nil, spec); got != 0 {
		t.Errorf("empty design: expectation = %v, want 0", got)
	}
}
