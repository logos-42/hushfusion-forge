// Package knowledge：把 registry 变成带证据的设计规则。
//
// 冻结接口 (v0.1) — 负责人：阶段 E。
//
// 一条规则是关于设计空间的 *可复现、带方向、可量化* 的陈述 —— 不是直觉，
// 也不是一个拟合出来的模型。挖掘流程刻意保守，因为知识库的全部意义就在于
// 下一轮设计应当能够信任它：
//
//  1. 每一个 (参数, 分数项) 对都会得到一个 Spearman 秩相关，且是按运行
//     (算法 x seed) 分别计算的，也就是建立在独立样本之上；
//  2. 候选规则必须在每一轮运行中的符号都相同 —— 这就是复现
//     检验，而上报的 confidence 就是这些运行中符号一致的
//     比例；
//  3. 方向用一个十分位对比来复述：参数处于最高十分位的那些设计，该分数项
//     的中位数对比最低十分位，这样规则携带的是量级，
//     而不只是一个符号；
//  4. 适用范围会被写下来（多少条设计、哪个搜索盒、哪个物理模型），
//     因为在真空场盒子里挖出来的规则是关于那个盒子的假设，
//     而不是自然定律。
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

// MineOpts 是挖掘阈值（默认值：150、0.20、12）。
type MineOpts struct {
	MinN      int
	MinAbsRho float64
	TopK      int
	// MinAgreement 是同号 run 的最低占比门槛(0~1)。默认 1.0 = 必须每一轮 run 都同号。
	// 降到 <1.0(如 0.8)会放宽到"多数 run 同号", 让 field/mirror/volume 这类在 high-score
	// 设计里有权衡(符号会翻转)的项也能挖出候选规则 —— 代价是每条规则带 sign_agreement
	// 明确标出强度, 使用者自行决定信不信。默认 1.0 保持历史行为逐位不变。
	MinAgreement float64
}

// 默认阈值，在 MineOpts 的某个字段留成零值时生效。
const (
	defaultMinN      = 150
	defaultMinAbsRho = 0.20
	defaultTopK      = 12
	// minRecordsPerRun 是每轮运行样本的硬下限：低于它，单轮运行的秩相关
	// 就只是噪声，而建立在「噪声恰好一致」之上的规则，正是这个包存在
	// 的意义所在要避免的那种失败模式。
	minRecordsPerRun = 20
)

// Rule 是一条挖掘出来的设计规则。JSON 键是冻结的（与 Python 辅助层
// python/aux/analyze.py 共享）。
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

// ParameterNames 是设计向量的名字：r_0..r_{K-1}、z_0..、I_0...
//
// 顺序就是 config.Spec.Lower/Upper 与 registry.Record.Params 使用的设计
// 向量顺序（先是 radius，然后 z，最后 current），因为挖出来的参数名必须
// 指向搜索层所优化的同一个槽位。
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

// Spearman 是带并列名次处理（平均名次）的秩相关系数。在测试所用的 golden
// 向量上必须复现 scipy.stats.spearmanr 到 < 1e-9 —— 实现方式：名次变换 +
// 对名次做 Pearson。
//
// 退化输入返回 0 而不是 NaN：配对样本少于 2 个，或者向量为常量（名次方差
// 为零，此时系数没有定义）。只能写成 0/0 的规则，无论如何都会被 MinAbsRho
// 过滤器丢掉，而返回 0 能让下游所有 JSON 都不含 NaN —— 否则 NaN 会毒化
// 整份报告。
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

// ranks 返回以 1 为起点的平均名次：并列的值共享它们本应占据的那些
// 名次的均值。
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
		avg := float64(i+j+2) / 2.0 // 第 i+1 .. j+1 名共享它们的均值
		for k := i; k <= j; k++ {
			out[idx[k]] = avg
		}
		i = j + 1
	}
	return out
}

// pearson 是普通的积矩相关系数，
// 为防浮点溢出会夹到 [-1, 1]。
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

// runKey 标识一个独立样本：一轮 (algorithm, seed) 运行。
type runKey struct {
	Algorithm string
	Seed      int
}

// MineRules 从 registry 记录中挖掘可复现的规则。
//
// 只有 feasible 的记录计入；记录数少于 max(20, MinN/8) 的运行会被丢弃；
// 至少要活下来 2 轮运行。候选规则只有在每一轮存活的运行中 Spearman 符号
// 都相同、且最坏情况 |rho| 至少为 MinAbsRho 时才被保留；规则按 |rho|
// 排序，返回前 TopK 条。
//
// 下面这些约定之所以写明，是因为 Python 辅助层会重新推导它们：
//
//   - MinN 通过每轮下限 max(20, MinN/8) 进入，整数除法；
//   - rho 是各存活运行之间最坏情况的 |rho|（与每一轮运行的符号一致），
//     所以上报的数字是最弱的证据，而不是最好的；
//   - sign_agreement 是携带多数符号的运行所占比例。因为这里把「一致」
//     当作硬性准入条件，每一条返回的规则都有 sign_agreement == 1.0 ——
//     该字段在 JSON 里是作为明确的复现证据存在的，
//     而不是一个会变化的质量分；
//   - 一轮运行如果 |rho| 恰好为 0（参数或分数项为常量），就没有符号，
//     因此会杀掉这个候选规则；
//   - decile_low / decile_high 是参数处于最低 / 最高十分位的那些设计的
//     分数项中位数，在存活的运行之间合并计算
//     （十分位大小 = max(1, n/10)）；
//   - 规则按 |rho| 降序、然后参数、然后分数项排序，
//     因此输出是确定性的。
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
	if opt.MinAgreement <= 0 {
		opt.MinAgreement = 1.0
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
			agreeFrac := float64(agree) / float64(len(rhos))
			if majority == 0 || agreeFrac < opt.MinAgreement {
				continue // 未在足够比例的 run 中以同一符号复现
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

// collectPair 从一组记录里取出某一个参数的值和某一个分数项的值。缺少该
// 分数项的记录会被跳过（一轮运行从未记录的分数项，无法支撑关于那一轮
// 运行的规则）。
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

// paramValue 从记录的具名参数数组中读取设计向量的第 i 个槽位。
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

// termNames 是这些记录所携带的分数项键去重排序后的并集。
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

// majoritySign 返回共同的符号 (+1 / -1) 以及携带它的条目数。
// 只要有一个零条目（|rho| == 0，即没有符号），一致就不可能成立，
// 于是返回 majority 0。
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

// decileContrast 返回参数最低十分位与最高十分位的分数项中位数。
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

// boxScope 描述仅由 spec 就能确定的那部分适用范围：搜索盒、评估窗口和
// 物理模型。一条没有适用范围的规则不是知识，
// 只是传闻。
func boxScope(spec config.Spec) string {
	return fmt.Sprintf(
		"search box: %d coils, radius [%.3g,%.3g] m, z [%.3g,%.3g] m, current [%.3g,%.3g] A; b_ref=%.3g T, mirror_ref=%.3g; "+
			"vacuum analytic circular-filament magnetostatics (NO plasma, NO conductor/eddy model)",
		spec.NCoils, spec.Bounds.Radius[0], spec.Bounds.Radius[1], spec.Bounds.Z[0], spec.Bounds.Z[1],
		spec.Bounds.Current[0], spec.Bounds.Current[1], spec.BRef, spec.MirrorRef)
}

// runScope 把挖掘样本的信息补进 boxScope。只有挖掘器知道这些数字，
// 这也是写下来的规则要逐条携带它们的原因。
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

// WriteRulesMD 写出人类可读的知识库：规则表、适用范围说明和一个机器
// 可读的 JSON 块。
//
// 版式：表头 (tag、timestamp、输入规模) → 判定口径 → Scope → 规则表 →
// 机器可读块，其中恰好有一个 ```json 围栏承载 rules 数组。LoadRules 读取
// 那个块；围栏缺失是错误，绝不返回空规则列表。
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

// LoadRules 从内嵌的 ```json 块中把规则读回来。
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

// extractJSONBlock 返回第一个 ```json 围栏的主体。
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

// RuleExpectation 是从规则里粗略推出的线性先验：对每条规则，取其 rho
// 乘以该参数相对盒子中心归一化后的位置，再对所有规则取平均。Phase 1 用它
// 来给候选设计加偏置；它被刻意做得简单，这样它的贡献可以在消融实验里
// 被度量（以及被移除）。
//
// 具体地：u = (x[i] - lower[i]) / (upper[i] - lower[i]) 夹到 [0,1]，
// contribution = rho * (u - 0.5)，结果是所有「其参数确实能映射到 x 的某个
// 槽位」的规则取均值。
// 没有任何可应用的规则时返回 0。
//
// 已知的简化（为 Phase 1 标注，此处不修，因为冻结签名已经定义了公式）：
// 目标函数里该分数项的符号被忽略了。对于正权重的分数项
// (field/mirror/volume)，正的 rho 确实意味着「参数越大分数越好」，但
// 对 ripple/cost，正的 rho 意味着相反。在这个 hook 接收权重之前，
// 这个先验只在正权重分数项上方向上成立。
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
