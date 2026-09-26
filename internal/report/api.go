// Package report：渲染人类（以及下一轮设计）真正会去读的运行产物。
//
// 冻结接口 (v0.1) — 负责人：阶段 E。
//
// 报告用中文书写、保留英文技术术语，并且必须包含一个专门的 诚实边界
// (honest-limits) 小节：模型没有表示什么、什么会推翻这个结果，以及
// 哪些主张是实测的、哪些是假设的。一份只报告胜利的报告
// 不算完成。
package report

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/logos-42/hushfusion-forge/internal/experiment"
	"github.com/logos-42/hushfusion-forge/internal/knowledge"
	"github.com/logos-42/hushfusion-forge/internal/physics"
)

// termHigherIsBetter 说明每个分数项在哪个方向上意味着「更好」。
// 它遵循 internal/objective 里冻结的分数公式：
//
//	score = + w_field*field + w_mirror*mirror + w_volume*volume
//	        - w_ripple*ripple - w_cost*cost - w_penalty*penalty
//
// 因此 field / mirror / volume 是「越高越好」，ripple / cost 是
// 「越低越好」。未知的分数项名默认按「越高越好」处理，并在报告中
// 如实列出，而不是被静默丢弃。
var termHigherIsBetter = map[string]bool{
	"field":  true,
	"mirror": true,
	"volume": true,
	"ripple": false,
	"cost":   false,
}

type direction int

const (
	dirNone direction = iota
	dirHigher
	dirLower
)

func (d direction) label() string {
	switch d {
	case dirHigher:
		return "↑ 好"
	case dirLower:
		return "↓ 好"
	default:
		return "无方向(参考)"
	}
}

// metricRow 是 §2 指标表中的一行。
type metricRow struct {
	Label string
	Dir   direction
	Get   func(physics.Metrics) float64
}

var metricRows = []metricRow{
	{"B_mid_T (midplane 体平均场)", dirNone, func(m physics.Metrics) float64 { return m.BMidT }},
	{"B_throat_T (轴上峰值场)", dirNone, func(m physics.Metrics) float64 { return m.BThroatT }},
	{"z_throat_m (峰值轴向位置)", dirNone, func(m physics.Metrics) float64 { return m.ZThroatM }},
	{"mirror_ratio", dirHigher, func(m physics.Metrics) float64 { return m.MirrorRatio }},
	{"volume_good", dirHigher, func(m physics.Metrics) float64 { return m.VolumeGood }},
	{"ripple", dirLower, func(m physics.Metrics) float64 { return m.Ripple }},
	{"B_coil_max_T", dirLower, func(m physics.Metrics) float64 { return m.BCoilMaxT }},
	{"min_coil_gap_m", dirHigher, func(m physics.Metrics) float64 { return m.MinCoilGapM }},
	{"cost_proxy", dirLower, func(m physics.Metrics) float64 { return m.CostProxy }},
}

// RenderMarkdown 渲染完整报告。
//
// 必需的小节，按顺序：
//
//	# Forge <tag> 运行报告            (标题行：机器 vs 人工基线，以及它在哪些
//	                                  分数项上赢了 / 输了)
//	## 1. 设置                        (spec、solver、预算、seeds、方法、commit)
//	## 2. 人工基线 vs 机器最优          (逐分数项的表格，两个方向都有)
//	## 3. 方法对比 (等预算)             (设计次数 / 最优性能 / 收敛速度 / 泛化)
//	## 4. 设计谱系                    (若有，列出改进最大的几条分支)
//	## 5. 知识库 rules                (前几条规则 + 路径)
//	## 6. 诚实边界                    (v0.1 没有建模的东西；什么会推翻结论；
//	                                  哪些数字是实测的)
//	## 7. 下一步 (Phase 1)            (已经就位的接口)
//
// rulePath 是规则 markdown 文件的路径，§5 会引用它。
//
// 渲染是全量的：一份没有最优设计、没有运行、没有规则或没有鲁棒性探针的
// 报告，仍然会渲染每一个小节 —— 并把缺失的东西明确点出来。
// 一份被静默缩短的报告，就是缺口变得看不见的方式。
func RenderMarkdown(rep *experiment.Report, rules []knowledge.Rule, rulePath string) string {
	if rep == nil {
		return "# Forge 运行报告\n\n_report 为空(nil):没有可渲染的内容。这不是一次成功的运行。_\n"
	}
	tag := rep.Meta.Tag
	if tag == "" {
		tag = "(untagged)"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Forge %s 运行报告\n\n", tag)
	b.WriteString(headline(rep))
	sectionSetup(&b, rep)
	sectionBaselineVsMachine(&b, rep)
	sectionMethods(&b, rep)
	sectionLineage(&b, rep)
	sectionRules(&b, rules, rulePath)
	sectionHonestLimits(&b, rep)
	sectionNext(&b, rep)
	return b.String()
}

// Write 把渲染好的 markdown 写到 path。
func Write(path, md string) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("report: create directory %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(path, []byte(md), 0o644); err != nil {
		return fmt.Errorf("report: write %s: %w", path, err)
	}
	return nil
}

// --- 标题行 -----------------------------------------------------------------

func headline(rep *experiment.Report) string {
	var b strings.Builder
	base := rep.Baseline.Score
	if rep.Best == nil {
		fmt.Fprintf(&b, "**结论一句话**: 本批**没有产生任何机器设计**(runs 为空),人工基线 score = %s。\n", num(base))
		b.WriteString("在这种情形下不存在\"机器 vs 人\"的结论,缺少的部分不在这里补。\n\n")
		return b.String()
	}
	delta := rep.Best.Score - base
	verdict := "机器**超过**人工基线"
	if delta <= 0 {
		verdict = "机器**没有超过**人工基线"
	}
	fmt.Fprintf(&b, "**结论一句话**: 机器最优 score = %s(%s / seed %d),人工基线 score = %s,Δ = %s —— %s。\n\n",
		num(rep.Best.Score), rep.Best.Algorithm, rep.Best.Seed, num(base), signed(delta), verdict)
	if delta <= 0 {
		b.WriteString("> 输就是输:本批预算 / 方法 / 搜索盒下机器没赢,这条结论按原样报告,不做挑选、不换指标、不换 seed。\n\n")
	}

	won, lost, tied, unknown := compareTerms(rep.Baseline.Terms, rep.Best.Terms)
	fmt.Fprintf(&b, "- 逐项**赢**的项: %s\n", listOrNone(won))
	fmt.Fprintf(&b, "- 逐项**输**的项: %s\n", listOrNone(lost))
	if len(tied) > 0 {
		fmt.Fprintf(&b, "- 打平: %s\n", strings.Join(tied, ", "))
	}
	if len(unknown) > 0 {
		fmt.Fprintf(&b, "- 方向未声明的 term(按\"越大越好\"判断,已在 §6 标注): %s\n", strings.Join(unknown, ", "))
	}
	if p := rep.Baseline.Penalties; len(p) > 0 && !rep.Baseline.Feasible {
		b.WriteString("- 注意: **人工基线本身被判定为 infeasible**(约束残差 > 0),见 §2 的 penalties;基线不可行的比较需要在 §6 里打折。\n")
	}
	if !rep.Best.Feasible {
		b.WriteString("- 注意: **机器最优设计被判定为 infeasible**;它的 score 里含 penalty 扣分。\n")
	}
	if r, ok := rep.Robustness.Summary[rep.Best.Algorithm]; ok {
		fmt.Fprintf(&b, "- 泛化(§3): 该 design 在 %d 个扰动变体上平均 Δ = %s,最差 Δ = %s,赢 %d/%d 个变体。\n",
			r.NVariants, signed(r.MeanDeltaVsBaseline), signed(r.WorstDeltaVsBaseline), r.NVariantsWinning, r.NVariants)
	}
	b.WriteString("\n")
	return b.String()
}

// compareTerms 依据冻结的分数方向，把分数项键分成 won / lost / tied。
func compareTerms(base, best map[string]float64) (won, lost, tied, unknown []string) {
	seen := map[string]bool{}
	keys := make([]string, 0, len(base)+len(best))
	for k := range base {
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	for k := range best {
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		if _, ok := termHigherIsBetter[k]; !ok {
			unknown = append(unknown, k)
		}
		bv, okb := base[k]
		mv, okm := best[k]
		if !okb || !okm {
			continue
		}
		if mv == bv {
			tied = append(tied, k)
			continue
		}
		higherBetter := true
		if d, ok := termHigherIsBetter[k]; ok {
			higherBetter = d
		}
		if (mv > bv) == higherBetter {
			won = append(won, k)
		} else {
			lost = append(lost, k)
		}
	}
	return won, lost, tied, unknown
}

// --- §1 设置 ----------------------------------------------------------------

func sectionSetup(b *strings.Builder, rep *experiment.Report) {
	b.WriteString("## 1. 设置\n\n")
	m := rep.Meta
	b.WriteString("| 项 | 值 |\n|---|---|\n")
	fmt.Fprintf(b, "| tag | %s |\n", orDash(m.Tag))
	fmt.Fprintf(b, "| timestamp (UTC) | %s |\n", orDash(m.Timestamp))
	fmt.Fprintf(b, "| budget (每次 run 的评估上限) | %d |\n", m.Budget)
	fmt.Fprintf(b, "| seeds | %s |\n", intList(m.Seeds))
	fmt.Fprintf(b, "| methods | %s |\n", strings.Join(m.Methods, ", "))
	fmt.Fprintf(b, "| workers | %d |\n", m.Workers)
	fmt.Fprintf(b, "| solver | %s |\n", orDash(rep.Solver))
	fmt.Fprintf(b, "| cost_ref (人工基线欧姆代价) | %s |\n", num(rep.CostRef))
	fmt.Fprintf(b, "| git commit | %s |\n", shortCommit(m.GitCommit))
	fmt.Fprintf(b, "| platform | %s |\n", orDash(m.Platform))
	fmt.Fprintf(b, "| go version | %s |\n", orDash(m.GoVersion))
	fmt.Fprintf(b, "| 登记的设计数 (registry) | %d(其中 feasible %d) |\n",
		rep.RegistrySummary.NRecords, rep.RegistrySummary.NFeasible)
	fmt.Fprintf(b, "| registry best | %s (%s) |\n\n",
		num(rep.RegistrySummary.BestScore), orDash(rep.RegistrySummary.BestDesignID))

	b.WriteString("spec(`config.Spec`,唯一真源):\n\n")
	b.WriteString("| key | value |\n|---|---|\n")
	for _, line := range specLines(rep.Spec) {
		b.WriteString(line)
	}
	b.WriteString("\n")
	if len(rep.RegistrySummary.PerAlgo) > 0 {
		algos := make([]string, 0, len(rep.RegistrySummary.PerAlgo))
		for a := range rep.RegistrySummary.PerAlgo {
			algos = append(algos, a)
		}
		sort.Strings(algos)
		b.WriteString("registry 记录分布: ")
		parts := make([]string, 0, len(algos))
		for _, a := range algos {
			parts = append(parts, fmt.Sprintf("%s=%d", a, rep.RegistrySummary.PerAlgo[a]))
		}
		b.WriteString(strings.Join(parts, ", "))
		b.WriteString("\n\n")
	}
}

// --- §2 人工基线 vs 机器最优 -------------------------------------------------

func sectionBaselineVsMachine(b *strings.Builder, rep *experiment.Report) {
	b.WriteString("## 2. 人工基线 vs 机器最优\n\n")
	base := rep.Baseline
	fmt.Fprintf(b, "人工基线: **%s** — %s(design %s,%d 个线圈)\n\n",
		orDash(base.Name), orDash(base.Note), orDash(base.DesignID), base.Metrics.NCoils)
	if rep.Best == nil {
		b.WriteString("**机器最优: 无。** 本批没有产生任何 run 结果,因此这一节只能给基线一侧;缺失的一侧不补数字。\n\n")
	}

	b.WriteString("### 2.1 目标项 (score 的组成)\n\n")
	b.WriteString("| term | 人工基线 | 机器最优 | Δ (机器 − 人工) | 方向 | 谁更好 |\n|---|---|---|---|---|---|\n")
	termKeys := unionKeys(base.Terms, bestTerms(rep))
	if len(termKeys) == 0 {
		b.WriteString("| _无 term 记录_ | | | | | |\n")
	}
	for _, k := range termKeys {
		hv, okb := base.Terms[k]
		mv, okm := bestTerms(rep)[k]
		dir := termHigherIsBetter[k]
		dirTxt := "↑ 好"
		if !dir {
			dirTxt = "↓ 好"
		}
		if _, known := termHigherIsBetter[k]; !known {
			dirTxt = "未声明"
			dir = true
		}
		who := "—"
		delta := mv - hv
		if okb && okm {
			switch {
			case delta == 0:
				who = "打平"
			case (delta > 0) == dir:
				who = "机器"
			default:
				who = "人"
			}
		}
		cell := func(v float64, ok bool) string {
			if !ok {
				return "—"
			}
			return num(v)
		}
		fmt.Fprintf(b, "| %s | %s | %s | %s | %s | %s |\n",
			k, cell(hv, okb), cell(mv, okm), cell(delta, okb && okm), dirTxt, who)
	}
	fmt.Fprintf(b, "| **score** | **%s** | **%s** | **%s** | **↑ 好** | **%s** |\n\n",
		num(base.Score), bestScore(rep), bestDelta(rep), whoWinsScore(rep))

	b.WriteString("### 2.2 物理 metrics(不是目标项,是证据)\n\n")
	b.WriteString("| metric | 人工基线 | 机器最优 | Δ | 方向 |\n|---|---|---|---|---|\n")
	for _, row := range metricRows {
		hv := row.Get(base.Metrics)
		var mv float64
		ok := rep.Best != nil
		if ok {
			mv = row.Get(rep.Best.Metrics)
		}
		mcell := "—"
		dcell := "—"
		if ok {
			mcell = num(mv)
			dcell = signed(mv - hv)
		}
		fmt.Fprintf(b, "| %s | %s | %s | %s | %s |\n", row.Label, num(hv), mcell, dcell, row.Dir.label())
	}
	b.WriteString("\n### 2.3 约束残差 (penalties)\n\n")
	if len(base.Penalties) == 0 && (rep.Best == nil || len(rep.Best.Terms) == 0) {
		b.WriteString("_没有记录 penalties(基线侧为空)。这本身就是一个缺口:约束是否被违反必须可从 registry 复算。_\n\n")
	} else {
		b.WriteString("| penalty | 人工基线 | 机器最优 |\n|---|---|---|\n")
		keys := unionKeys(base.Penalties, nil)
		for _, k := range keys {
			v := base.Penalties[k]
			fmt.Fprintf(b, "| %s | %s | 见 registry.jsonl(机器最优记录携带同一字段) |\n", k, signed(v))
		}
		fmt.Fprintf(b, "\n人工基线 feasible = %v", base.Feasible)
		if rep.Best != nil {
			fmt.Fprintf(b, ",机器最优 feasible = %v", rep.Best.Feasible)
		}
		b.WriteString("。\n\n")
	}
	if len(base.Design) > 0 {
		fmt.Fprintf(b, "人工基线 design 向量 (r…, z…, I…): `%s`\n\n", vec(base.Design))
	}
	if rep.Best != nil && len(rep.Best.Design) > 0 {
		fmt.Fprintf(b, "机器最优 design 向量: `%s`\n\n", vec(rep.Best.Design))
	}
}

// --- §3 方法对比 -------------------------------------------------------------

func sectionMethods(b *strings.Builder, rep *experiment.Report) {
	b.WriteString("## 3. 方法对比 (等预算)\n\n")
	if len(rep.Aggregate) == 0 {
		b.WriteString("**没有任何方法产生聚合结果**(runs 为空),这一节无法给出对比 —— 缺失本身如实报告。\n\n")
	}
	methods := make([]string, 0, len(rep.Aggregate))
	for m := range rep.Aggregate {
		methods = append(methods, m)
	}
	sort.Slice(methods, func(i, j int) bool {
		a, c := rep.Aggregate[methods[i]], rep.Aggregate[methods[j]]
		if a.BestMean != c.BestMean {
			return a.BestMean > c.BestMean
		}
		return methods[i] < methods[j]
	})

	b.WriteString("### 3.1 最优性能 (best-of-budget) 与收敛速度 (evals-to-beat)\n\n")
	b.WriteString("| method | n_seeds | budget | best_mean | best_std | best_min | best_max | 赢过基线的 seed | frac | evals_to_beat mean | median |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, m := range methods {
		a := rep.Aggregate[m]
		fmt.Fprintf(b, "| %s | %d | %d | %s | %s | %s | %s | %d | %.2f | %s | %s |\n",
			m, a.NSeeds, a.Budget, num(a.BestMean), num(a.BestStd), num(a.BestMin), num(a.BestMax),
			a.NBeatingBaseline, a.FracBeatingBaseline, evals(a.EvalsToBeatMean), evals(a.EvalsToBeatMedian))
	}
	b.WriteString("\n判定口径(可被 Python 辅助层复算):\n\n")
	fmt.Fprintf(b, "- 基线分数 `baseline_score = %s`(同一次评估口径;高于它才算赢,严格大于)。\n", num(rep.Baseline.Score))
	b.WriteString("- `best_std` 是**样本标准差 (ddof = 1)**;跨 seed 统计,同一 seed 重复出现只计一次。\n")
	b.WriteString("- `evals_to_beat` 是首次超过基线的评估序号,**只对赢过基线的 seed 取 mean / median**;`-1` 表示一个 seed 都没赢过。\n")
	b.WriteString("- **单 seed 结论无效**:本表所有数字都是跨 seed 统计,报告不从中挑最好看的 seed。\n\n")

	b.WriteString("### 3.2 泛化 (robustness probe)\n\n")
	if len(rep.Robustness.Summary) == 0 {
		b.WriteString("_没有泛化探针结果。注意:一个只在被搜索的那个盒子里赢的设计,尚未被证明泛化。_\n\n")
	} else {
		fmt.Fprintf(b, "变体 (%d 个): %s\n\n", len(rep.Robustness.Variants), strings.Join(rep.Robustness.Variants, ", "))
		b.WriteString("每个变体都把**人工基线重新求解**为那个变体的零点(`baseline.TextbookMirror` 在扰动后的 spec 下重解 cell current,\n")
		b.WriteString("再用该变体自己的 cost_ref 打分),所以 Δ > 0 的含义是\"仍然优于**为新需求重新设计的人**\",不是\"优于原基线\"。\n\n")
		b.WriteString("| design | mean Δ vs 重新求解的基线 | worst Δ | 赢的变体数 | 变体总数 |\n|---|---|---|---|---|\n")
		names := make([]string, 0, len(rep.Robustness.Summary))
		for d := range rep.Robustness.Summary {
			names = append(names, d)
		}
		sort.Strings(names)
		for _, d := range names {
			s := rep.Robustness.Summary[d]
			fmt.Fprintf(b, "| %s | %s | %s | %d | %d |\n",
				d, signed(s.MeanDeltaVsBaseline), signed(s.WorstDeltaVsBaseline), s.NVariantsWinning, s.NVariants)
		}
		b.WriteString("\n变体零点(重新求解的人工基线 score):\n\n| variant | baseline score |\n|---|---|\n")
		vnames := make([]string, 0, len(rep.Robustness.BaselinePerVariant))
		for v := range rep.Robustness.BaselinePerVariant {
			vnames = append(vnames, v)
		}
		sort.Strings(vnames)
		for _, v := range vnames {
			fmt.Fprintf(b, "| %s | %s |\n", v, num(rep.Robustness.BaselinePerVariant[v]))
		}
		b.WriteString("\n逐 design / 逐变体的 Δ 全表在 `results.json` 的 `robustness.per_design`。\n")
		b.WriteString("**原始设计向量没有变**:泛化探针只重打分,不重新搜索;一个设计在被扰动后的盒子里可能已经越界。\n\n")
	}

	b.WriteString("### 3.3 关于 grid search 的替代说明\n\n")
	b.WriteString("本批**没有真正的 grid search**:设计向量 D = 12,即使每轴只取 5 个点也是 5^12 ≈ 2.4e8 次评估,超出任何等预算比较。\n")
	b.WriteString("用 `lhs`(latin-hypercube,分层空间填充)作为诚实的替代品 —— 这是替代,不是等价,不做掩饰。\n\n")
}

// --- §4 设计谱系 -------------------------------------------------------------

func sectionLineage(b *strings.Builder, rep *experiment.Report) {
	b.WriteString("## 4. 设计谱系\n\n")
	b.WriteString("谱系是一条**树**,不是列表:每条记录带 `design_id` / `parent_design` / `generation`,因此\"哪个分支贡献了最多改进\"是可答的。\n\n")
	fmt.Fprintf(b, "根节点: **D0001 = human_baseline**(人工基线,design %s)。\n\n", orDash(rep.Baseline.DesignID))
	if len(rep.Runs) == 0 {
		b.WriteString("_本批没有 runs,没有机器子节点。_\n\n")
		return
	}
	b.WriteString("| algorithm | seed | best design_id | best score |\n|---|---|---|---|\n")
	for _, r := range rep.Runs {
		fmt.Fprintf(b, "| %s | %d | %s | %s |\n", r.Algorithm, r.Seed, orDash(r.BestDesignID), num(r.BestScore))
	}
	b.WriteString("\n**本报告 v0.1 没有逐分支的增益表**:`experiment.Report` 的字段里没有 lineage 表(冻结 schema),")
	b.WriteString("完整父链与 `registry.BranchImprovement()` 的 branch gain 在 `registry.jsonl` 那一侧,不在这个文件里。\n")
	b.WriteString("上表只列出每条 run 的 best design_id,可以据此在 registry 里沿 `parent_design` 回溯。这是缺口,已在 §6 记录。\n\n")
}

// --- §5 知识库 ---------------------------------------------------------------

func sectionRules(b *strings.Builder, rules []knowledge.Rule, rulePath string) {
	b.WriteString("## 5. 知识库 rules\n\n")
	if len(rules) == 0 {
		b.WriteString("**本批没有通过复制检验的规则。** 判定口径是:每个 run((algorithm, seed)) 单独算 Spearman,")
		b.WriteString("只有**所有 run 同号**且最差 |ρ| 达阈值的候选才留下 —— 关系不稳定时宁可没有规则,也不写成规则。\n")
		if rulePath != "" {
			fmt.Fprintf(b, "机器可读的(空)规则文件: `%s`\n\n", rulePath)
		} else {
			b.WriteString("\n")
		}
		return
	}
	b.WriteString("| rule_id | parameter | term | rho (worst-case) | sign_agreement | decile_low | decile_high | n_designs | n_runs |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|\n")
	for _, r := range rules {
		fmt.Fprintf(b, "| %s | %s | %s | %.3f | %.2f | %s | %s | %d | %d |\n",
			r.RuleID, r.Parameter, r.Term, r.Rho, r.SignAgreement, num(r.DecileLow), num(r.DecileHigh), r.NDesigns, r.NRuns)
	}
	b.WriteString("\n陈述:\n\n")
	for _, r := range rules {
		fmt.Fprintf(b, "- **%s**: %s\n", r.RuleID, r.Statement)
	}
	if rulePath != "" {
		fmt.Fprintf(b, "\n完整表格、scope 与机器可读 ```json 块: `%s`\n\n", rulePath)
	} else {
		b.WriteString("\n_规则文件路径未提供(rulePath 为空),机器可读块不可达。_\n\n")
	}
}

// --- §6 诚实边界 -------------------------------------------------------------

func sectionHonestLimits(b *strings.Builder, rep *experiment.Report) {
	b.WriteString("## 6. 诚实边界\n\n")
	b.WriteString("这一节的作用是让读者知道,上面的数字**在什么范围内**才算数。只报赢的部分不是报告。\n\n")

	b.WriteString("### 6.1 v0.1 没有建模的东西\n\n")
	b.WriteString("- **没有 plasma**:没有压力、没有抗磁性响应、没有平衡、没有有限 beta、没有杂质辐射、没有中子 —— 场模型是真空静磁 (`config.Spec` 的 fidelity 声明)。\n")
	b.WriteString("- **线圈是理想圆环电流丝**:没有导体截面、绕组包细节、匝数分布、电缆与接头、支撑结构、冷屏、热辐射。\n")
	b.WriteString("- `B_coil_max` = 其它线圈在该线圈位置处的场 + `spec.SelfField()`,后者是无限大电流板近似 `mu0*j*t/2`,不是 winding-pack / FEM 结果。\n")
	b.WriteString("- `cost_proxy = Σ I²r` 是**欧姆代价代理**,不是成本:不含 HTS 带材价格、低温制冷功率、电源、结构重量、装配与失超保护。\n")
	b.WriteString("- 没有热分析、没有 quench 分析、没有应力分析、没有匝间绝缘与工程可制造性约束(可绕制半径、支撑几何、公差)。\n")
	b.WriteString("- 没有 plasma 与线圈的耦合:设计\"对 plasma 更好\"只能通过真空场指标(mirror_ratio / volume_good / ripple)间接表达。\n")
	b.WriteString("- 没有真正的 grid search(见 §3.3):D = 12 时穷举不可行,用 `lhs` 替代,替代关系如实写出。\n")
	if rep.Meta.Budget > 0 {
		fmt.Fprintf(b, "- 预算 %d 次评估/run 是经验值,不是收敛性证明;搜索盒(`bounds`)之外的更优设计既没被找到,也没被排除。\n", rep.Meta.Budget)
	} else {
		b.WriteString("- 预算是一次经验值,不是收敛性证明;搜索盒(`bounds`)之外的更优设计既没被找到,也没被排除。\n")
	}
	b.WriteString("- **没有学习型策略**:`internal/rlenv` 里未实现的策略必须显式报错(合同 §6 的 G10 门),所以本报告不含任何 RL 结果。\n\n")

	b.WriteString("### 6.2 什么会推翻本结论\n\n")
	b.WriteString("- 泛化探针里 worst Δ < 0(见 §3.2):只在原盒子里赢的设计没有泛化,原结论随之作废。\n")
	b.WriteString("- 领先幅度小于跨 seed 的 `best_std`(见 §3.1):差异落在搜索噪声内,不能当成结论。\n")
	b.WriteString("- 领先**只来自 cost 项**而 field / mirror / volume 没有改善:那是\"买到了更便宜的磁体\",不是更好的设计 —— §2 的逐项表就是为了一眼看穿这种情形。\n")
	b.WriteString("- 人工基线在解码后被搜索盒裁剪:比较的就不是同一个设计(由 `TestBaselineInsideSearchBox` 专门守门)。\n")
	b.WriteString("- 解析解与独立实现的离散 Biot-Savart 求和不一致,或 Python oracle(scipy)与 Go 的相对差 > 1e-9 / score > 1e-6(合同 §6 的 G5):数值栈不可信,所有 field 数字作废。\n")
	b.WriteString("- 同 seed 两次运行 `best_score` 不一致(合同 §6 的 G7):随机性叙述是假的。\n")
	b.WriteString("- registry 完整性检查(`registry --check`)或 schema parity(`python/aux/schema_check.py`,合同 §6 的 G8/G9)失败:本报告的 runs / aggregate 无法被独立复算,结论退化为自述。\n\n")

	b.WriteString("### 6.3 哪些数字是实测、哪些是假设\n\n")
	b.WriteString("**实测**(由 solver / evaluator 算出,可从 `registry.jsonl` 逐条复算):\n\n")
	b.WriteString("- score、terms、weighted、penalties、metrics(每个被评估的设计都逐条落库);\n")
	b.WriteString("- `runs[].history` 与 `evals_to_beat` 是搜索过程的直接记录;\n")
	b.WriteString("- §3.2 的 Δ 是对**给定设计向量**(不重新搜索)重新评估得到;\n")
	b.WriteString("- `best_of_budget` / `mean` / `std` / `min` / `max` 只是上面前两类的算术。\n")
	if rep.Best != nil {
		fmt.Fprintf(b, "\n本批实测的机器最优 score = %s(design %s, %s seed %d)。\n", num(rep.Best.Score), orDash(rep.Best.DesignID), rep.Best.Algorithm, rep.Best.Seed)
	}
	b.WriteString("\n**假设**(不是测出来的,换掉会换赢家):\n\n")
	b.WriteString("- 权重 `w_field / w_mirror / w_volume / w_ripple / w_cost / w_penalty` 是工程判断,不是物理;\n")
	b.WriteString("- `b_ref` / `mirror_ref` / `confine_factor` 与采样窗口(`n_axis` / `n_vol_r` / `n_vol_z` / `z_mid` / `z_cell` / `z_axis_max`)是评估口径;\n")
	b.WriteString("- `coil_field_limit` / `j_eng` / `t_pack` 是设计裕度假设,直接决定哪些设计被判 infeasible;\n")
	b.WriteString("- `bounds`(搜索盒)本身是假设;`cost_ref` = 人工基线欧姆代价是使 cost 项读数为 1.0 的归一化约定;\n")
	b.WriteString("- 泛化探针里的\"重新求解的人工基线\"是同一比例几何下重解 cell current 的 `baseline.TextbookMirror`,不是重新做一轮人类设计;\n")
	b.WriteString("- 统计口径:方差用样本定义(ddof = 1);Spearman 按 (algorithm, seed) 每个 run 单独计算、并列名次取平均秩;规则表的 `sign_agreement` 因为\"同号才保留\"的硬过滤恒为 1.0,它是复制证据而不是质量分数;\n")
	b.WriteString("- `RuleExpectation` 目前**没有计入 term 在目标函数里的符号**(ripple / cost 为负权重),因此它对这两个 term 的方向先验是有偏的;Phase 1 修复。\n\n")

	b.WriteString("### 6.4 覆盖范围\n\n")
	fmt.Fprintf(b, "- 本报告只覆盖 tag = `%s`、seeds = %s、methods = %s 的组合;没跑的 seed 与方法不做任何声明。\n",
		orDash(rep.Meta.Tag), intList(rep.Meta.Seeds), strings.Join(rep.Meta.Methods, ", "))
	b.WriteString("- 所有方法级数字都是跨 seed 统计;**单 seed 结论在本项目里无效**。\n")
	if rep.RLEnvReference == nil {
		b.WriteString("- `rl_env_reference` 为空:本批没有把 run 登记为 RL 环境参考轨迹。\n")
	}
	b.WriteString("\n")
}

// --- §7 下一步 ---------------------------------------------------------------

func sectionNext(b *strings.Builder, rep *experiment.Report) {
	b.WriteString("## 7. 下一步 (Phase 1)\n\n")
	b.WriteString("已经**在接口里就位**的钩子(不需要改冻结签名就能接上):\n\n")
	b.WriteString("- `registry` 的 `parent_design` / `BranchImprovement()`:改进谱系已经是数据,不再需要重跑;\n")
	b.WriteString("- `runner.Scorer` 接口:新算法只要接受一个 scorer 就能进等预算对比;\n")
	b.WriteString("- `search.Options.WarmStart`:知识复用的最小形式(拿一个设计起跑)已经可测 —— 本批的 `evolution_warm` 就是它;\n")
	b.WriteString("- `knowledge.RuleExpectation`:规则 → 提议先验的线性映射已经有了可调用形式;\n")
	b.WriteString("- `internal/rlenv`:RL 环境的缝合点,且**未实现的策略必须显式报错**(不许返回编造数值)。\n\n")
	b.WriteString("具体下一步:\n\n")
	b.WriteString("1. 把 `RuleExpectation` 接进提案层(需要 `search` 暴露一个规则先验钩子,`Options.WarmStart` 只能带一个设计向量,带不了规则集合);\n")
	b.WriteString("2. 做\"规则先验 ±\"的 ablation:先验必须能被关掉,否则它的贡献不可测;\n")
	b.WriteString("3. 把 branch gain 表并入报告(需要主线决定:加 `Report` 字段,还是单独出一张表);\n")
	b.WriteString("4. 修 `RuleExpectation` 的 term 符号问题(见 §6.3),并让复现阈值可参数化(`MineOpts` 增字段属冻结签名变更,由主线决定);\n")
	b.WriteString("5. 增加第二个物理模型(winding-pack / 有限 beta)作为**模型不确定性**的对照 —— 只在一个模型里赢,说明不了工程上赢。\n\n")
	if rep.Best == nil {
		b.WriteString("> 前置事项:本批没有 runs,先让 harness 跑出完整的 A/B/C/D 依赖链,再谈 Phase 1。\n")
	}
}

// --- 格式化辅助函数 ----------------------------------------------------------

// num 以 10 位有效数字渲染一个浮点数：足以看出分数差值，
// 又短到能让表格保持可读。
func num(v float64) string {
	return strconv.FormatFloat(v, 'g', 10, 64)
}

func signed(v float64) string {
	if v > 0 {
		return "+" + num(v)
	}
	return num(v)
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

func shortCommit(c string) string {
	if len(c) > 12 {
		return c[:12]
	}
	return orDash(c)
}

func evals(v float64) string {
	if v < 0 {
		return "— (从未赢过)"
	}
	return num(v)
}

func intList(seeds []int) string {
	if len(seeds) == 0 {
		return "—"
	}
	parts := make([]string, 0, len(seeds))
	for _, s := range seeds {
		parts = append(parts, strconv.Itoa(s))
	}
	return strings.Join(parts, ", ")
}

func vec(x []float64) string {
	parts := make([]string, 0, len(x))
	for _, v := range x {
		parts = append(parts, num(v))
	}
	return strings.Join(parts, ", ")
}

func listOrNone(xs []string) string {
	if len(xs) == 0 {
		return "无"
	}
	return strings.Join(xs, ", ")
}

func unionKeys(a, b map[string]float64) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(a)+len(b))
	for k := range a {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	for k := range b {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func bestTerms(rep *experiment.Report) map[string]float64 {
	if rep.Best == nil {
		return nil
	}
	return rep.Best.Terms
}

func bestScore(rep *experiment.Report) string {
	if rep.Best == nil {
		return "—"
	}
	return num(rep.Best.Score)
}

func bestDelta(rep *experiment.Report) string {
	if rep.Best == nil {
		return "—"
	}
	return signed(rep.Best.Score - rep.Baseline.Score)
}

func whoWinsScore(rep *experiment.Report) string {
	if rep.Best == nil {
		return "—"
	}
	switch d := rep.Best.Score - rep.Baseline.Score; {
	case d > 0:
		return "机器"
	case d < 0:
		return "人"
	default:
		return "打平"
	}
}

// specLines 把 spec map 渲染成 markdown 表格行，键是排好序的，
// 这样两份报告就可以做 diff。
func specLines(spec map[string]any) []string {
	if len(spec) == 0 {
		return []string{"| _未提供 spec_ | |\n"}
	}
	keys := make([]string, 0, len(spec))
	for k := range spec {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, fmt.Sprintf("| %s | %s |\n", k, renderValue(spec[k])))
	}
	return out
}

func renderValue(v any) string {
	switch t := v.(type) {
	case nil:
		return "—"
	case float64:
		return num(t)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case bool:
		return strconv.FormatBool(t)
	case string:
		return t
	case []float64:
		return vec(t)
	case []any:
		parts := make([]string, 0, len(t))
		for _, e := range t {
			parts = append(parts, renderValue(e))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s=%s", k, renderValue(t[k])))
		}
		return strings.Join(parts, ", ")
	default:
		return fmt.Sprintf("%v", t)
	}
}
