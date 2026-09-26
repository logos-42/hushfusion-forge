// Package knowledge: turn a registry into design rules, with their evidence.
//
// FROZEN INTERFACE (v0.1) — owner: stage E.
//
// A rule is a *replicated, direction-bearing, quantified* statement about the
// design space — not a hunch and not a fitted model. The mining procedure is
// deliberately conservative because the whole point of the knowledge base is
// that the next design round should be able to trust it:
//
//  1. every (parameter, score-term) pair gets a Spearman rank correlation
//     computed PER RUN (algorithm x seed), i.e. on independent samples;
//  2. a candidate rule must have the SAME SIGN in every run — that is the
//     replication test, and the reported confidence is the fraction of runs that
//     agree;
//  3. direction is restated with a decile contrast: median of the term for
//     designs in the top decile of the parameter vs the bottom decile, so the
//     rule carries a magnitude and not just a sign;
//  4. the scope is written down (how many designs, which search box, which
//     physics model) because a rule mined in a vacuum-field box is a hypothesis
//     about that box, not a law of nature.
package knowledge

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/logos-42/hushfusion-forge/internal/config"
	"github.com/logos-42/hushfusion-forge/internal/registry"
)

// MineOpts are the mining thresholds (defaults: 150, 0.20, 12).
type MineOpts struct {
	MinN      int
	MinAbsRho float64
	TopK      int
}

// Default thresholds, applied when a field of MineOpts is left zero.
const (
	defaultMinN      = 150
	defaultMinAbsRho = 0.20
	defaultTopK      = 12
	// minRecordsPerRun is the hard floor on the per-run sample: below this the
	// rank correlation of one run is noise, and a rule built on noise that
	// happens to agree is exactly the failure mode this package exists to avoid.
	minRecordsPerRun = 20
)

// Rule is one mined design rule. JSON keys are FROZEN (shared with the Python
// auxiliary layer, python/aux/analyze.py).
type Rule struct {
	RuleID        string  `json:"rule_id"`
	Parameter     string  `json:"parameter"`
	Term          string  `json:"term"`
	Rho           float64 `json:"rho"`
	SignAgreement float64 `json:"sign_agreement"`
	NDesigns      int     `json:"n_designs"`
	NRuns         int     `json:"n_runs"`
	DecileLow     float64 `json:"decile_low"`
	DecileHigh    float64 `json:"decile_high"`
	Statement     string  `json:"statement"`
	StatementEN   string  `json:"statement_en"`
	Scope         string  `json:"scope"`
}

// ParameterNames are the design-vector names: r_0..r_{K-1}, z_0.., I_0...
//
// The order is the design-vector order used by config.Spec.Lower/Upper and by
// registry.Record.Params (radius, then z, then current), because the mined
// parameter name must address the same slot the search layer optimises.
func ParameterNames(spec config.Spec) []string {
	k := spec.NCoils
	if k <= 0 {
		return nil
	}
	out := make([]string, 0, 3*k)
	for i := 0; i < k; i++ {
		out = append(out, fmt.Sprintf("r_%d", i))
	}
	for i := 0; i < k; i++ {
		out = append(out, fmt.Sprintf("z_%d", i))
	}
	for i := 0; i < k; i++ {
		out = append(out, fmt.Sprintf("I_%d", i))
	}
	return out
}

// Spearman is the rank correlation coefficient with tie handling (average
// ranks). Must reproduce scipy.stats.spearmanr to < 1e-9 on the golden vectors
// used by the tests — implement rank transform + Pearson on ranks.
//
// Degenerate input returns 0 rather than NaN: fewer than 2 paired samples, or a
// constant vector (zero rank variance, where the coefficient is undefined).
// A rule that can only be stated as 0/0 will be dropped by the MinAbsRho filter
// either way, and 0 keeps every downstream JSON free of NaN — which would
// otherwise poison the whole report.
func Spearman(x, y []float64) float64 {
	if len(x) != len(y) || len(x) < 2 {
		return 0
	}
	for i := range x {
		if math.IsNaN(x[i]) || math.IsNaN(y[i]) {
			return 0
		}
	}
	return pearson(ranks(x), ranks(y))
}

// ranks returns 1-based average ranks: tied values share the mean of the ranks
// they would have occupied.
func ranks(v []float64) []float64 {
	n := len(v)
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return v[idx[a]] < v[idx[b]] })
	out := make([]float64, n)
	for i := 0; i < n; {
		j := i
		for j+1 < n && v[idx[j+1]] == v[idx[i]] {
			j++
		}
		avg := float64(i+j+2) / 2.0 // ranks i+1 .. j+1 share their mean
		for k := i; k <= j; k++ {
			out[idx[k]] = avg
		}
		i = j + 1
	}
	return out
}

// pearson is the plain product-moment correlation, clamped to [-1, 1] against
// floating-point overshoot.
func pearson(a, b []float64) float64 {
	n := len(a)
	if n < 2 || len(b) != n {
		return 0
	}
	var ma, mb float64
	for i := 0; i < n; i++ {
		ma += a[i]
		mb += b[i]
	}
	ma /= float64(n)
	mb /= float64(n)
	var cov, va, vb float64
	for i := 0; i < n; i++ {
		da, db := a[i]-ma, b[i]-mb
		cov += da * db
		va += da * da
		vb += db * db
	}
	if va <= 0 || vb <= 0 {
		return 0
	}
	r := cov / math.Sqrt(va*vb)
	if r > 1 {
		return 1
	}
	if r < -1 {
		return -1
	}
	return r
}

// runKey identifies one independent sample: one (algorithm, seed) run.
type runKey struct {
	Algorithm string
	Seed      int
}

// MineRules mines replicated rules from registry records.
//
// Only feasible records count; runs with fewer than max(20, MinN/8) records are
// dropped; at least 2 runs must survive. A candidate keeps its rule only if the
// Spearman sign is identical in every surviving run and the worst-case |rho| is
// at least MinAbsRho; rules are ranked by |rho| and the top TopK are returned.
//
// Conventions, stated because the Python auxiliary layer re-derives them:
//
//   - MinN enters through the per-run floor max(20, MinN/8), integer division;
//   - rho is the WORST-CASE |rho| across surviving runs (same sign as every
//     run), so the reported number is the weakest evidence, not the best;
//   - sign_agreement is the fraction of runs carrying the majority sign. Because
//     unanimity is a hard admission filter here, every returned rule has
//     sign_agreement == 1.0 — the field is carried in the JSON as explicit
//     replication evidence, not as a varying quality score;
//   - a run whose |rho| is exactly 0 (constant parameter or constant term) has
//     no sign and therefore kills the candidate;
//   - decile_low / decile_high are the median term of the designs in the
//     lowest / highest decile of the parameter, pooled over the surviving runs
//     (decile size = max(1, n/10));
//   - rules are sorted by |rho| descending, then parameter, then term, so the
//     output is deterministic.
func MineRules(recs []registry.Record, spec config.Spec, opt MineOpts) []Rule {
	if opt.MinN <= 0 {
		opt.MinN = defaultMinN
	}
	if opt.MinAbsRho <= 0 {
		opt.MinAbsRho = defaultMinAbsRho
	}
	if opt.TopK <= 0 {
		opt.TopK = defaultTopK
	}
	perRunMin := opt.MinN / 8
	if perRunMin < minRecordsPerRun {
		perRunMin = minRecordsPerRun
	}

	byRun := map[runKey][]registry.Record{}
	keys := make([]runKey, 0, 8)
	for _, r := range recs {
		if !r.Feasible {
			continue
		}
		k := runKey{Algorithm: r.Algorithm, Seed: r.Seed}
		if _, seen := byRun[k]; !seen {
			keys = append(keys, k)
		}
		byRun[k] = append(byRun[k], r)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Algorithm != keys[j].Algorithm {
			return keys[i].Algorithm < keys[j].Algorithm
		}
		return keys[i].Seed < keys[j].Seed
	})

	surviving := make([]runKey, 0, len(keys))
	pooled := make([]registry.Record, 0, len(recs))
	algorithms := make([]string, 0, len(keys))
	for _, k := range keys {
		if len(byRun[k]) < perRunMin {
			continue
		}
		surviving = append(surviving, k)
		pooled = append(pooled, byRun[k]...)
		found := false
		for _, a := range algorithms {
			if a == k.Algorithm {
				found = true
				break
			}
		}
		if !found {
			algorithms = append(algorithms, k.Algorithm)
		}
	}
	if len(surviving) < 2 {
		return nil
	}

	names := ParameterNames(spec)
	if len(names) == 0 {
		return nil
	}
	terms := termNames(pooled)
	if len(terms) == 0 {
		return nil
	}
	scope := runScope(spec, len(pooled), len(surviving), algorithms)

	kept := make([]Rule, 0, len(names)*len(terms))
	for pi, pname := range names {
		for _, term := range terms {
			rhos := make([]float64, 0, len(surviving))
			for _, k := range surviving {
				xs, ys := collectPair(byRun[k], pi, term)
				rhos = append(rhos, Spearman(xs, ys))
			}
			majority, agree := majoritySign(rhos)
			if majority == 0 || agree != len(rhos) {
				continue // not replicated with one sign in every run
			}
			minAbs := math.Abs(rhos[0])
			for _, r := range rhos[1:] {
				if a := math.Abs(r); a < minAbs {
					minAbs = a
				}
			}
			if minAbs < opt.MinAbsRho {
				continue
			}
			xsAll, ysAll := collectPair(pooled, pi, term)
			low, high := decileContrast(xsAll, ysAll)
			rho := float64(majority) * minAbs
			kept = append(kept, Rule{
				Parameter:     pname,
				Term:          term,
				Rho:           rho,
				SignAgreement: float64(agree) / float64(len(rhos)),
				NDesigns:      len(pooled),
				NRuns:         len(surviving),
				DecileLow:     low,
				DecileHigh:    high,
				Statement:     statementCN(pname, term, rho, low, high, len(pooled), len(surviving)),
				StatementEN:   statementEN(pname, term, rho, low, high, len(pooled), len(surviving)),
				Scope:         scope,
			})
		}
	}

	sort.Slice(kept, func(i, j int) bool {
		ai, aj := math.Abs(kept[i].Rho), math.Abs(kept[j].Rho)
		if ai != aj {
			return ai > aj
		}
		if kept[i].Parameter != kept[j].Parameter {
			return kept[i].Parameter < kept[j].Parameter
		}
		return kept[i].Term < kept[j].Term
	})
	if len(kept) > opt.TopK {
		kept = kept[:opt.TopK]
	}
	for i := range kept {
		kept[i].RuleID = fmt.Sprintf("R%03d", i+1)
	}
	return kept
}

// collectPair pulls one parameter's values and one term's values out of a set of
// records. Records missing the term are skipped (a term that a run never
// recorded cannot support a rule about that run).
func collectPair(recs []registry.Record, paramIndex int, term string) ([]float64, []float64) {
	xs := make([]float64, 0, len(recs))
	ys := make([]float64, 0, len(recs))
	for _, r := range recs {
		x, ok := paramValue(r, paramIndex)
		if !ok {
			continue
		}
		y, ok := r.Terms[term]
		if !ok {
			continue
		}
		xs = append(xs, x)
		ys = append(ys, y)
	}
	return xs, ys
}

// paramValue reads design-vector slot i out of a record's named parameter arrays.
func paramValue(r registry.Record, i int) (float64, bool) {
	k := len(r.Params.RadiusM)
	switch {
	case i < k:
		return r.Params.RadiusM[i], true
	case i < 2*k && len(r.Params.ZM) == k:
		return r.Params.ZM[i-k], true
	case i < 3*k && len(r.Params.CurrentA) == k:
		return r.Params.CurrentA[i-2*k], true
	}
	return 0, false
}

// termNames is the sorted union of the term keys the records carry.
func termNames(recs []registry.Record) []string {
	seen := map[string]bool{}
	for _, r := range recs {
		for t := range r.Terms {
			seen[t] = true
		}
	}
	out := make([]string, 0, len(seen))
	for t := range seen {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// majoritySign returns the common sign (+1 / -1) and how many entries carry it.
// A zero entry (|rho| == 0, i.e. no sign) makes unanimity impossible and yields
// majority 0.
func majoritySign(rhos []float64) (int, int) {
	pos, neg := 0, 0
	for _, r := range rhos {
		switch {
		case r > 0:
			pos++
		case r < 0:
			neg++
		}
	}
	switch {
	case pos == len(rhos):
		return 1, pos
	case neg == len(rhos):
		return -1, neg
	}
	return 0, 0
}

// decileContrast returns the median term of the lowest and of the highest decile
// of the parameter.
func decileContrast(xs, ys []float64) (low, high float64) {
	n := len(xs)
	if n == 0 || n != len(ys) {
		return 0, 0
	}
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return xs[order[a]] < xs[order[b]] })
	k := n / 10
	if k < 1 {
		k = 1
	}
	bottomVals := make([]float64, 0, k)
	topVals := make([]float64, 0, k)
	for i := 0; i < k; i++ {
		bottomVals = append(bottomVals, ys[order[i]])
		topVals = append(topVals, ys[order[n-1-i]])
	}
	return median(bottomVals), median(topVals)
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	cp := append([]float64(nil), xs...)
	sort.Float64s(cp)
	n := len(cp)
	if n%2 == 1 {
		return cp[n/2]
	}
	return 0.5 * (cp[n/2-1] + cp[n/2])
}

// boxScope describes the part of the scope that follows from the spec alone:
// the search box, the evaluation window and the physics model. A rule without a
// scope is not knowledge, it is a rumour.
func boxScope(spec config.Spec) string {
	return fmt.Sprintf(
		"search box: %d coils, radius [%.3g,%.3g] m, z [%.3g,%.3g] m, current [%.3g,%.3g] A; b_ref=%.3g T, mirror_ref=%.3g; "+
			"vacuum analytic circular-filament magnetostatics (NO plasma, NO conductor/eddy model)",
		spec.NCoils, spec.Bounds.Radius[0], spec.Bounds.Radius[1], spec.Bounds.Z[0], spec.Bounds.Z[1],
		spec.Bounds.Current[0], spec.Bounds.Current[1], spec.BRef, spec.MirrorRef)
}

// runScope adds the mining sample to boxScope. Only the miner knows these
// numbers, which is why the written rules carry them per rule.
func runScope(spec config.Spec, nDesigns, nRuns int, algorithms []string) string {
	algos := strings.Join(algorithms, ",")
	if algos == "" {
		algos = "-"
	}
	return fmt.Sprintf("%s; feasible records only; %d designs over %d runs ((algorithm,seed) pairs: %s); "+
		"rules are claims about this box, not laws of nature", boxScope(spec), nDesigns, nRuns, algos)
}

func statementCN(param, term string, rho, low, high float64, nDesigns, nRuns int) string {
	dir := "越大"
	if rho < 0 {
		dir = "越小"
	}
	return fmt.Sprintf("参数 %s 越大,term %s %s:参数最低十分位时 term 中位数 %.6g,最高十分位 %.6g"+
		"(Spearman ρ=%.3f 为各 run 中最差绝对值,同号 run 占比 %.0f%%,%d designs / %d runs)。",
		param, term, dir, low, high, rho, 100.0, nDesigns, nRuns)
}

func statementEN(param, term string, rho, low, high float64, nDesigns, nRuns int) string {
	dir := "rises with"
	if rho < 0 {
		dir = "falls with"
	}
	return fmt.Sprintf("%s %s %s: median %s = %.6g in the lowest decile of %s vs %.6g in the highest "+
		"(Spearman rho=%.3f, worst-case over runs; sign agreement 100%% of runs; n=%d designs over %d runs).",
		term, dir, param, term, low, param, high, rho, nDesigns, nRuns)
}

// WriteRulesMD writes the human-readable knowledge base with the rules table,
// the scope note and a machine-readable JSON block.
//
// Layout: header (tag, timestamp, input size) → 判定口径 → Scope → Rules table →
// 机器可读块 with exactly one ```json fence holding the rules array. LoadRules
// reads that block; a missing fence is an error, never an empty rule list.
func WriteRulesMD(rules []Rule, path string, spec config.Spec, nRecords int, tag string) error {
	var b strings.Builder
	if tag == "" {
		tag = "(untagged)"
	}
	fmt.Fprintf(&b, "# Forge knowledge rules — tag %s\n\n", tag)
	fmt.Fprintf(&b, "- 生成时间 (UTC): %s\n", time.Now().UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "- 输入: %d 条 registry record(仅 feasible 参与挖掘)\n", nRecords)
	fmt.Fprintf(&b, "- 规则数: %d\n\n", len(rules))
	b.WriteString("## 判定口径 (mining criterion)\n\n")
	b.WriteString("每条候选规则在**每个 run((algorithm, seed)) 上单独**计算 Spearman 秩相关(并列名次取平均秩);\n")
	b.WriteString("只有**所有 run 同号**的候选才保留,报告的 ρ 取各 run 中**绝对值最小**的那个(worst-case,不是最好看的那个),\n")
	b.WriteString("置信度 = 同号 run 占比。run 内 feasible 记录少于 max(20, MinN/8) 的 run 被剔除,存活 run 少于 2 个则不产出任何规则。\n")
	b.WriteString("方向用 decile 对比复述:参数最低 / 最高十分位时 term 的中位数。\n\n")
	b.WriteString("## Scope\n\n")
	b.WriteString(boxScope(spec) + "\n\n")
	fmt.Fprintf(&b, "本文件从 %d 条 registry record 中挖掘;每条规则自己的 scope 字段记录它的挖掘样本\n", nRecords)
	b.WriteString("(n_designs / n_runs / 参与的 (algorithm,seed) 集合)。\n\n")
	b.WriteString("## Rules\n\n")
	if len(rules) == 0 {
		b.WriteString("本批没有通过复制检验(sign replication)的规则。原因通常是样本量不足、搜索预算太小、\n")
		b.WriteString("或参数与 term 的关系在不同 run 上确实不稳定 —— 这三种情况都不该被写成规则。\n\n")
	} else {
		b.WriteString("| rule_id | parameter | term | rho | sign_agreement | decile_low | decile_high | n_designs | n_runs |\n")
		b.WriteString("|---|---|---|---|---|---|---|---|---|\n")
		for _, r := range rules {
			fmt.Fprintf(&b, "| %s | %s | %s | %.3f | %.2f | %.6g | %.6g | %d | %d |\n",
				r.RuleID, r.Parameter, r.Term, r.Rho, r.SignAgreement, r.DecileLow, r.DecileHigh, r.NDesigns, r.NRuns)
		}
		b.WriteString("\n### 逐条陈述\n\n")
		for _, r := range rules {
			fmt.Fprintf(&b, "- **%s** (%s / %s): %s\n", r.RuleID, r.Parameter, r.Term, r.Statement)
			fmt.Fprintf(&b, "  - EN: %s\n", r.StatementEN)
			fmt.Fprintf(&b, "  - scope: %s\n", r.Scope)
		}
		b.WriteString("\n")
	}
	b.WriteString("## 机器可读块 (machine-readable)\n\n")
	b.WriteString("```json\n")
	payload := rules
	if payload == nil {
		payload = []Rule{}
	}
	blob, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("knowledge: marshal rules: %w", err)
	}
	b.Write(blob)
	b.WriteString("\n```\n")

	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("knowledge: create rules directory %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("knowledge: write rules %s: %w", path, err)
	}
	return nil
}

// LoadRules reads rules back out of the embedded ```json block.
func LoadRules(path string) ([]Rule, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, err := extractJSONBlock(string(data))
	if err != nil {
		return nil, fmt.Errorf("knowledge: %s: %w", path, err)
	}
	var rules []Rule
	if err := json.Unmarshal([]byte(block), &rules); err != nil {
		return nil, fmt.Errorf("knowledge: %s: parsing embedded rules JSON: %w", path, err)
	}
	return rules, nil
}

// extractJSONBlock returns the body of the first ```json fence.
func extractJSONBlock(md string) (string, error) {
	const fence = "```json"
	i := strings.Index(md, fence)
	if i < 0 {
		return "", errors.New("no embedded ```json block")
	}
	rest := md[i+len(fence):]
	j := strings.Index(rest, "```")
	if j < 0 {
		return "", errors.New("unterminated ```json block")
	}
	return strings.TrimSpace(rest[:j]), nil
}

// RuleExpectation is a crude linear prior from the rules: for each rule, its
// rho times the parameter's normalised position away from the box centre,
// averaged over rules. Phase 1 uses it to bias proposals; it is intentionally
// simple so its contribution is measurable (and removable) in an ablation.
//
// Concretely: u = (x[i] - lower[i]) / (upper[i] - lower[i]) clamped to [0,1],
// contribution = rho * (u - 0.5), and the result is the mean over the rules
// whose parameter actually resolves to a slot of x. Returns 0 when there is
// nothing to apply.
//
// KNOWN SIMPLIFICATION (flagged for Phase 1, not fixed here because the frozen
// signature defines the formula): the term's sign in the objective is ignored.
// For the positively weighted terms (field/mirror/volume) a positive rho really
// does mean "more parameter, better score", but for ripple/cost a positive rho
// means the opposite. Until the hook takes the weights, this prior is only
// directionally sound for the positive-weight terms.
func RuleExpectation(rules []Rule, x []float64, spec config.Spec) float64 {
	if len(rules) == 0 || len(x) == 0 {
		return 0
	}
	index := map[string]int{}
	for i, name := range ParameterNames(spec) {
		index[name] = i
	}
	lower, upper := spec.Lower(), spec.Upper()
	sum, used := 0.0, 0
	for _, r := range rules {
		i, ok := index[r.Parameter]
		if !ok || i >= len(x) || i >= len(lower) || i >= len(upper) {
			continue
		}
		span := upper[i] - lower[i]
		if span <= 0 {
			continue
		}
		u := (x[i] - lower[i]) / span
		if u < 0 {
			u = 0
		}
		if u > 1 {
			u = 1
		}
		sum += r.Rho * (u - 0.5)
		used++
	}
	if used == 0 {
		return 0
	}
	return sum / float64(used)
}
