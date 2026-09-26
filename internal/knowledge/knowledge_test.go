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

// --- Spearman 秩相关 -------------------------------------------------------

// TestSpearmanHandComputedAnchors 用的是手算出来的系数
// （名次变换 + 对名次做 Pearson），这样实现是被钉在算术上，
// 而不是被钉在它自己身上。
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

// naiveRank 是一套 O(n^2) 的名次变换，独立于实现写成：
// rank = #(严格更小的个数) + (含自身的并列个数)/2。
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

// naivePearson 是第二套独立的 Pearson 实现。
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

// TestSpearmanEqualsPearsonOnRanks 是冻结文档所要求的性质：对随机向量
// （包括大量并列值），Spearman(x, y) 必须等于由一个独立实现算出的
// Pearson(rank(x), rank(y))。
func TestSpearmanEqualsPearsonOnRanks(t *testing.T) {
	rng := rand.New(rand.NewSource(20260101))
	for trial := 0; trial < 500; trial++ {
		n := 2 + rng.Intn(40)
		x := make([]float64, n)
		y := make([]float64, n)
		for i := range x {
			// 小整数池：保证出现大量并列名次
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
		// 对参数对称：rho(x, y) == rho(y, x)
		if math.Abs(Spearman(x, y)-Spearman(y, x)) > 1e-12 {
			t.Fatalf("Spearman is not symmetric: %v vs %v", Spearman(x, y), Spearman(y, x))
		}
		// 反转其中一个变量的顺序会翻转符号
		neg := make([]float64, n)
		for i := range y {
			neg[i] = -y[i]
		}
		if math.Abs(Spearman(x, neg)+Spearman(x, y)) > 1e-12 {
			t.Fatalf("Spearman did not flip under a sign reversal: %v vs %v", Spearman(x, neg), Spearman(x, y))
		}
		// 对 x 做严格递增变换不得改变名次顺序
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
	// 这个名字必须指向搜索所优化的同一个槽位
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

// mkRun 构造一轮包含 n 条 feasible 记录的数据。除了 `reverse` 里列出的
// 名字之外，每个参数都随记录序号上升（因此它与同样上升的分数项 `cost`
// 的 Spearman 相关恰好是 +1）；`reverse` 里的参数随序号下降
// （相关恰好是 -1）。
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

// mineFixture 是三轮存活运行（各 24 条记录），外加一轮小到不计数的运行，
// 以及一条 infeasible 记录 —— 如果允许它进来，就会破坏 r_0 的相关性。
func mineFixture(t *testing.T) []registry.Record {
	t.Helper()
	recs := []registry.Record{}
	recs = append(recs, mkRun(t, "evolution", 0, 24, nil)...)
	recs = append(recs, mkRun(t, "evolution", 1, 24, nil)...)
	// lhs 的 seed 0 里，z_1 与 cost 反相关：(z_1, cost) 这个候选规则
	// 无法复现，必须被丢掉。
	recs = append(recs, mkRun(t, "lhs", 0, 24, map[string]bool{"z_1": true})...)
	// 记录数少于 max(20, MinN/8) 的运行会被整体丢弃
	recs = append(recs, mkRun(t, "random", 0, 5, nil)...)
	// 一条 infeasible 记录，其 r_0 与其它值并列、且 cost 最高：如果有
	// infeasible 记录漏进来，r_0 的相关性就会掉到 1 以下
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

	// 12 个参数 x 1 个分数项 = 12 个候选，其中 (z_1) 这一个
	// 无法在各轮运行之间复现。
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
		// 三轮各 24 条 feasible 记录；那轮只有 5 条记录的运行，以及那条
		// infeasible 记录，都必须不被计入
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

	// 排序：|rho| 降序，id 按名次分配
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

	// 十分位对比携带量级：cost 随 r_0 上升，所以 r_0 的最高十分位
	// 必须高于最低十分位
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
	// 两轮运行；r_0 与 cost 在两者中都同向上升，但第二轮的关系更弱
	//（cost 序列在其后半段被反转），因此上报的 rho 必须是那个更小的 |rho|
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
	// 把 r_0 单独拎出来：其它所有参数都被做成常量，常量完全没有秩相关，
	// 因此不可能构成规则
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
	// 一轮存活运行 + 一轮太小的运行：复现不可能成立
	recs := append([]registry.Record{}, mkRun(t, "evolution", 0, 24, nil)...)
	recs = append(recs, mkRun(t, "random", 0, 4, nil)...)
	if rules := MineRules(recs, spec, MineOpts{}); rules != nil {
		t.Errorf("mined %d rules from a single run, want none", len(rules))
	}
	// 完全没有 feasible 的记录
	all := mkRun(t, "evolution", 0, 24, nil)
	for i := range all {
		all[i].Feasible = false
	}
	if rules := MineRules(all, spec, MineOpts{}); rules != nil {
		t.Errorf("mined %d rules with no feasible records, want none", len(rules))
	}
	// 一条记录都没有
	if rules := MineRules(nil, spec, MineOpts{}); rules != nil {
		t.Errorf("mined %d rules from no records, want none", len(rules))
	}
}

func TestMineRulesMinAbsRhoThreshold(t *testing.T) {
	spec := config.DefaultSpec()
	recs := mineFixture(t)
	// fixture 里每个候选的 |rho| 都 == 1.0，所以高于 1.0 的阈值
	// 必然把它们全部丢掉（MinAbsRho <= 0 意味着「用默认值」）
	if rules := MineRules(recs, spec, MineOpts{MinAbsRho: 1.5}); len(rules) != 0 {
		t.Errorf("mined %d rules with MinAbsRho = 1.5, want none", len(rules))
	}
	// MineOpts 的零值必须意味着文档中的默认值 (150, 0.20, 12)
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

// --- 规则往返 ---------------------------------------------------------------

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

	// 两条规则取平均，各自使用自己的槽位
	r2 := Rule{Parameter: "z_1", Term: "volume", Rho: -1.0}
	x := build(0, 1)
	slot := spec.NCoils + 1
	x[slot] = lower[slot]
	if got := RuleExpectation([]Rule{r, r2}, x, spec); math.Abs(got-0.5) > 1e-12 {
		t.Errorf("two rules: expectation = %v, want +0.5 ((0.5 + 0.5)/2)", got)
	}

	// 指向一个不存在参数的规则不会稀释这个均值
	if got := RuleExpectation([]Rule{r, {Parameter: "q_9", Rho: 1}}, build(0, 1), spec); math.Abs(got-0.5) > 1e-12 {
		t.Errorf("unknown parameter name: expectation = %v, want +0.5", got)
	}
	// 盒外位置会被夹取，而不是外推
	if got := RuleExpectation([]Rule{r}, lower, spec); math.Abs(got+0.5) > 1e-12 {
		t.Errorf("clamping: expectation = %v, want -0.5", got)
	}
	if got := RuleExpectation([]Rule{r}, nil, spec); got != 0 {
		t.Errorf("empty design: expectation = %v, want 0", got)
	}
}
