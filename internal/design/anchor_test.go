// anchor_test.go —— 逐条比对 testdata/projectionphysics_anchors.json。
//
// 那个文件由 scripts/emit_pp_anchors.py 从上游 ProjectionPhysics 的**真实 artifact 文件**
// 读出来（含上游 git commit 与读取时间）。契约 §3 禁止把锚点手抄进 Go 源码当常量 ——
// 那样就变成自证；本文件是唯一允许出现上游数字的检查点，而它的数字来自文件而非源码。
//
// 容差口径（写在注释里，别让它变成"调一调就绿"的旋钮）：
//   - 上游字段本身是逐位拷贝的真值；Go 侧是**复算**（不同语言的浮点求值顺序），实测
//     相对差 ~2e-15。取 rel 1e-12：比任何真实公式错小 3 个数量级，又不会把跨语言次序差
//     误报成公式错。
//   - summary.txt 里打印过的数字（3–4 位有效数字）按**打印精度**锚定，不与 report.json 的
//     高精度字段混为一谈。
package design

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// 锚点文件的容差 (见文件头)。
const (
	relAnchor = 1e-12 // Go 复算 vs 上游 report.json 字段
	// relPrintedText 用于 summary.txt 里打印过的数字。最粗的一处是 τ₀ = 0.0360（4 位有效
	// 数字 ⟹ 半格 5e-5 ⟹ 相对 1.4e-3），所以取 5e-3：仍比任何真实公式错小三个数量级。
	relPrintedText = 5e-3
	relPrintedFine = 1e-6 // μ_max(0.999781): 打到 6 位小数
)

// anchorsPath 从测试工作目录向上找仓库根 (与 owners_test.go 同一手法)。
func anchorsPath(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		cand := filepath.Join(dir, "testdata", "projectionphysics_anchors.json")
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			t.Fatalf("仓库根 %s 下没有 testdata/projectionphysics_anchors.json"+
				"（先跑 python3 scripts/emit_pp_anchors.py）", dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("在 %s 之上找不到仓库根", dir)
		}
		dir = parent
	}
}

func loadAnchors(t *testing.T) ppAnchors {
	t.Helper()
	path := anchorsPath(t)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s: %v", path, err)
	}
	var a ppAnchors
	if err := json.Unmarshal(raw, &a); err != nil {
		t.Fatalf("解析 %s: %v", path, err)
	}
	return a
}

// anchorDiff 是锚点比对本身。拆成纯函数, 是为了让「这道门能不能变红」自己也被验一次
// （一道不可能失败的门不算门 —— 仓库的既有规矩, 见 internal/owners/owners_test.go）。
func anchorDiff(label string, got, want, relTol float64) error {
	if math.IsNaN(got) || math.IsNaN(want) {
		return fmt.Errorf("%s: NaN (got %v, want %v)", label, got, want)
	}
	if got == want {
		return nil
	}
	if math.IsInf(got, 0) || math.IsInf(want, 0) {
		return fmt.Errorf("%s: got %v, want %v（无穷不相等）", label, got, want)
	}
	rel := math.Abs(got-want) / math.Max(math.Abs(want), 1e-300)
	if rel > relTol {
		return fmt.Errorf("%s: got %.17g, want %.17g（相对差 %.3g > %g）", label, got, want, rel, relTol)
	}
	return nil
}

// checkAnchors 累积失败而不是第一条就停: 一次跑完能看见**全部**漂移。
func checkAnchors(t *testing.T, problems *[]string, label string, got, want, relTol float64) {
	t.Helper()
	if err := anchorDiff(label, got, want, relTol); err != nil {
		*problems = append(*problems, err.Error())
	}
}

func report(t *testing.T, problems []string) {
	t.Helper()
	if len(problems) > 0 {
		t.Fatalf("%d 处锚点不一致:\n  - %s", len(problems), strings.Join(problems, "\n  - "))
	}
}

// sameStrings 比两串字符串 (两边都已排序); nil 与空切片算相同。
func sameStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// --------------------------------------------------------------------------- 锚点文件自身的可信度

// TestAnchorsProvenance 先钉住「这份锚点是怎么来的」。出处写错 = 后面全部白比。
func TestAnchorsProvenance(t *testing.T) {
	a := loadAnchors(t)
	p := a.Provenance

	if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(p.UpstreamCommit) {
		t.Fatalf("upstream_commit 不是 40 位十六进制 sha: %q", p.UpstreamCommit)
	}
	if p.Generator != "scripts/emit_pp_anchors.py" {
		t.Fatalf("generator 应为 scripts/emit_pp_anchors.py, got %q", p.Generator)
	}
	if p.GeneratedAtUTC == "" || p.GeneratedAtLocal == "" {
		t.Fatalf("缺读取时间: %q / %q", p.GeneratedAtUTC, p.GeneratedAtLocal)
	}
	if len(p.SkippedFields) != 0 {
		t.Fatalf("锚点文件自报有读不到的字段 %v —— 那些字段不该被写进锚点, "+
			"而既然写了就说明这份文件不完整", p.SkippedFields)
	}
	// 上游 artifacts 部分被 git 追踪 (2026-10-07 起: 关键 report.json 经 -f 入库,
	// 实测 30 个文件) —— 出处措辞与实测一致, 不许假装"全在工作区"或"全在 git"。
	// 数值由 emit 脚本实测写入, 断言不硬编码具体数。
	_ = p.UpstreamArtifactsTrackedByGit
	want := []string{
		"artifacts/moirefield/report.json", "artifacts/moirefield/summary.txt",
		"artifacts/mudynamics/report.json", "artifacts/mudynamics/summary.txt",
		"artifacts/fusionroadmap/report.json", "artifacts/fusionroadmap/summary.txt",
		"artifacts/sinkmuopt/report.json",
	}
	if !reflect.DeepEqual(p.ReadFrom, want) {
		t.Fatalf("read_from 与上游真源清单不一致:\n got %v\nwant %v", p.ReadFrom, want)
	}
	// 出处必须写成「commit + 重跑产物」, 不许写成「上游提交里的 artifacts」(那是假出处)。
	if !strings.Contains(p.SourceOfTruth, p.UpstreamCommit) {
		t.Fatalf("source_of_truth 里没有写上游 commit: %q", p.SourceOfTruth)
	}
	if !strings.Contains(p.SourceOfTruth, "工作区重跑") {
		t.Fatalf("source_of_truth 必须说明这是工作区重跑产物, got %q", p.SourceOfTruth)
	}
	if !strings.Contains(p.CheckNote, "meta.date") {
		t.Fatalf("check_note 必须说明忽略 meta.date, got %q", p.CheckNote)
	}
}

// TestAnchorComparisonCanGoRed 证明比对本身不是空转的绿。
func TestAnchorComparisonCanGoRed(t *testing.T) {
	if err := anchorDiff("same", 1.0, 1.0, relAnchor); err != nil {
		t.Fatalf("相等值被判为不一致: %v", err)
	}
	if err := anchorDiff("ulp", 1.0+1e-15, 1.0, relAnchor); err != nil {
		t.Fatalf("1 ULP 级的跨语言差异不该被判红: %v", err)
	}
	if err := anchorDiff("wrong", 1.001, 1.0, relAnchor); err == nil {
		t.Fatal("1e-3 的偏差没有被判红 —— 这道门等于不存在")
	}
	if err := anchorDiff("nan", math.NaN(), 1.0, relAnchor); err == nil {
		t.Fatal("NaN 没有被判红")
	}
	if err := anchorDiff("inf", math.Inf(1), 1.0, relAnchor); err == nil {
		t.Fatal("+Inf 没有被判红")
	}
}

// --------------------------------------------------------------------------- moirefield (M1–M8)

// TestAnchorsConfinementConstants 把本层的常数逐条对上游常数块。
func TestAnchorsConfinementConstants(t *testing.T) {
	a := loadAnchors(t)
	var p []string
	c := a.Moirefield.Constants

	checkAnchors(t, &p, "MU0", MU0, c.Mu0, relAnchor)
	checkAnchors(t, &p, "KB", KB, c.KB, 1e-15)
	checkAnchors(t, &p, "TKeV", TKeV, c.TKeV, 0)
	checkAnchors(t, &p, "Beta", Beta, c.Beta, 0)
	checkAnchors(t, &p, "NDesign", NDesign, c.NDesign, 0)
	checkAnchors(t, &p, "ARef", ARef, c.ARef, 0)
	checkAnchors(t, &p, "D0", D0, c.D0, 0)
	checkAnchors(t, &p, "Floor", Floor, c.FloorFC11, 1e-15)
	checkAnchors(t, &p, "Tau0(ARef)", Tau0(ARef), c.Tau0S, relAnchor)
	checkAnchors(t, &p, "BDeath(ARef)", BDeath(ARef), c.BDeathRefT, relAnchor)

	// 契约 §6: 本层不算排程/预算 —— 上游的人·月与 μ 阶梯不得出现在本包的判据里。
	// 这里只能钉住「锚点文件里它被明确标注为排程假设」, 以及本层没有它的字段。
	if !strings.Contains(a.Fusionroadmap.LadderNote, "排程假设") {
		t.Fatalf("μ 阶梯必须被标注为排程假设 (不进判据), got %q", a.Fusionroadmap.LadderNote)
	}
	if len(a.Fusionroadmap.Ladder) == 0 {
		t.Fatal("锚点里应有 μ 阶梯表原文 (留证用), 但它不进判据")
	}
	report(t, p)
}

// TestAnchorsDesignPoint 钉住设计密度点的四个量 (契约 §3 的四条)。
func TestAnchorsDesignPoint(t *testing.T) {
	a := loadAnchors(t)
	var p []string
	m := a.Moirefield

	checkAnchors(t, &p, "B_min(N_DESIGN)", BMin(NDesign, Beta), m.BMinDesignT, relAnchor)
	checkAnchors(t, &p, "n_design", NDesign, m.NDesignPerm3, 0)
	checkAnchors(t, &p, "X_req@design", XReq(1.6, ARef), m.XReqAtDesign, relAnchor)
	// 设计密度分支的裕度: 上游 1.479497…（契约 §3 断言 1.476±0.03）。
	checkAnchors(t, &p, "χ_μ@design", ChiMu(1.6, ARef), m.ChiMuAtDesign, relAnchor)
	if math.Abs(ChiMu(1.6, ARef)-1.476) > 0.03 {
		// 契约 §3 明写的断言容差: 用锚点值算出来的 χ_μ 必须落在这个区间里。
		p = append(p, fmt.Sprintf("χ_μ@design 不在契约 §3 的 1.476±0.03 内: %v", ChiMu(1.6, ARef)))
	}
	// 0.12 T 那条: χ_μ < 1e-3 (窗口关闭, 契约 §3 的判据)。
	if got := ChiMu(0.12, ARef); !(got < 1e-3) {
		p = append(p, fmt.Sprintf("χ_μ(0.12,0.2) 应 < 1e-3, got %v", got))
	}
	// summary.txt 的打印值 (精度低, 只作文本口径核对)。
	checkAnchors(t, &p, "summary τ₀", Tau0(ARef), m.SummaryText.Tau0S, relPrintedText)
	checkAnchors(t, &p, "summary FLOOR", Floor, m.SummaryText.FloorFC11, relPrintedText)
	checkAnchors(t, &p, "summary B_min", BMin(NDesign, Beta), m.SummaryText.BMinDesignT, relPrintedText)
	checkAnchors(t, &p, "summary B_death", BDeath(ARef), m.SummaryText.BDeathRefT, relPrintedText)
	checkAnchors(t, &p, "summary χ_μ", ChiMu(1.6, ARef), m.SummaryText.ChiMuAtDesign, relPrintedText)
	report(t, p)
}

// TestAnchorsSources 把 sources.go 的 7 行**逐条**对上游, 包括 label/citation 的字符串,
// 以及每行的派生量 (n_max / n_op / τ_L / X_req / χ_μ / P_rel / V_rel) 与上游自己的判决标志。
func TestAnchorsSources(t *testing.T) {
	a := loadAnchors(t)
	var p []string

	if len(a.Moirefield.Sources) != 7 {
		t.Fatalf("上游场源材料类应 7 行, 锚点文件里 %d 行", len(a.Moirefield.Sources))
	}
	table := SourceTable()
	if len(table) != len(a.Moirefield.Sources) {
		t.Fatalf("SourceTable() %d 行 vs 上游 %d 行", len(table), len(a.Moirefield.Sources))
	}

	for i, up := range a.Moirefield.Sources {
		// 行序也必须一致 (M8 总表按这个顺序打印)。
		if table[i].Key != up.Key {
			t.Fatalf("第 %d 行: SourceTable 是 %q, 上游是 %q（顺序/内容漂移）",
				i, table[i].Key, up.Key)
		}
		row, ok := LookupSource(up.Key)
		if !ok {
			t.Fatalf("LookupSource(%q) 失败", up.Key)
		}
		// 字符串: 逐字节相等 (它们本来就是上游 JSON 里的字面量)。
		if row.Label != up.Label {
			p = append(p, fmt.Sprintf("%s.label: %q != 上游 %q", up.Key, row.Label, up.Label))
		}
		if row.Citation != up.Citation {
			p = append(p, fmt.Sprintf("%s.citation: %q != 上游 %q", up.Key, row.Citation, up.Citation))
		}
		if row.BCapT != up.BCapT {
			p = append(p, fmt.Sprintf("%s.B_cap: %v != 上游 %v", up.Key, row.BCapT, up.BCapT))
		}

		b := row.BCapT
		checkAnchors(t, &p, up.Key+".n_max", NMax(b), up.NMaxPerm3, relAnchor)
		checkAnchors(t, &p, up.Key+".n_op", NOp(b), up.NOpPerm3, relAnchor)
		checkAnchors(t, &p, up.Key+".τ_L(n_op)", TauLawson(NOp(b)), up.TauLawsonS, relAnchor)
		checkAnchors(t, &p, up.Key+".X_req", XReq(b, ARef), up.XReqCap, relAnchor)
		checkAnchors(t, &p, up.Key+".χ_μ", ChiMu(b, ARef), up.ChiMu, relAnchor)
		checkAnchors(t, &p, up.Key+".P_rel", PRel(b, BRefPower), up.PRel, relAnchor)
		checkAnchors(t, &p, up.Key+".V_rel", VRel(b, BRefPower), up.VRelForSamePower, relAnchor)
		checkAnchors(t, &p, up.Key+".χ_B(需/顶)", BMin(NDesign, Beta)/b, up.ChiBRequiredOverCap, relAnchor)

		// 上游自己的判决标志必须与门的判定一致 —— 这是「门」与「上游结论」的对齐。
		if got := b < BMin(NDesign, Beta); got != up.BetaImpossible {
			p = append(p, fmt.Sprintf("%s: β 撑不住 (B<B_min) = %v, 上游 %v", up.Key, got, up.BetaImpossible))
		}
		if got := XReq(b, ARef) > Floor; got != up.Feasible {
			p = append(p, fmt.Sprintf("%s: μ 窗口开设 = %v, 上游 feasible=%v", up.Key, got, up.Feasible))
		}
		if up.M8MuFeasible != up.Feasible {
			p = append(p, fmt.Sprintf("%s: 上游 M4 feasible 与 M8 mu_feasible 自相矛盾", up.Key))
		}
		if up.M8Verdict == "" {
			p = append(p, up.Key+": 上游 M8 判决文本没进锚点")
		}
	}
	// M3 的基准自洽: 9 T 基准行的 P_rel = V_rel = 1 (契约 §3)。
	for _, up := range a.Moirefield.Sources {
		if up.BCapT != BRefPower {
			continue
		}
		checkAnchors(t, &p, "P_rel(9.0)", PRel(up.BCapT, BRefPower), 1.0, relAnchor)
		checkAnchors(t, &p, "V_rel(9.0)", VRel(up.BCapT, BRefPower), 1.0, relAnchor)
		checkAnchors(t, &p, "上游 P_rel(9.0)", up.PRel, 1.0, relAnchor)
		checkAnchors(t, &p, "上游 V_rel(9.0)", up.VRelForSamePower, 1.0, relAnchor)
	}
	report(t, p)
}

// TestAnchorsDeathScan 钉死死活判据: 每个 a 的 B_death 与「哪几条材料线被关死」。
//
// 这一条最强: 它要求门的**判定集合**与上游的 CLOSED 列表逐个元素相同。
func TestAnchorsDeathScan(t *testing.T) {
	a := loadAnchors(t)
	var p []string

	if len(a.Moirefield.DeathScan) < 5 {
		t.Fatalf("上游 B_death 扫描应至少 5 个 a, 锚点文件里 %d 个", len(a.Moirefield.DeathScan))
	}
	byA := map[float64]float64{}
	for _, row := range a.Moirefield.DeathScan {
		checkAnchors(t, &p, fmt.Sprintf("B_death(%v)", row.AM), BDeath(row.AM), row.BDeathT, relAnchor)
		byA[row.AM] = row.BDeathT

		var closed, open []string
		for _, s := range SourceTable() {
			if s.BCapT < BDeath(row.AM) {
				closed = append(closed, s.Key)
			} else {
				open = append(open, s.Key)
			}
		}
		sort.Strings(closed)
		sort.Strings(open)
		// 注意: JSON 里的空列表是**非 nil** 的空切片, 所以不能用 reflect.DeepEqual
		// 直接比对 (nil vs 空切片会被判不等, 而那不是真实差异)。
		if !sameStrings(closed, row.ClosedSources) {
			p = append(p, fmt.Sprintf("a=%v 关闭的场源: %v, 上游 %v", row.AM, closed, row.ClosedSources))
		}
		if !sameStrings(open, row.OpenSources) {
			p = append(p, fmt.Sprintf("a=%v 开放的场源: %v, 上游 %v", row.AM, open, row.OpenSources))
		}
	}
	// 契约 §3 的 1/a 标度 (容差按契约: 2.0 用 2%, 4.0 用 3%), 全部由锚点值相除得到。
	if v1, v2, ok := byA[0.1], byA[0.2], true; ok {
		if r := v1 / v2; math.Abs(r-2.0) > 0.02 {
			p = append(p, fmt.Sprintf("B_death(0.1)/B_death(0.2) = %v, 契约 §3 要求 2.0±2%%", r))
		}
	}
	if v1, v2, ok := byA[0.05], byA[0.2], true; ok {
		if r := v1 / v2; math.Abs(r-4.0) > 0.03 {
			p = append(p, fmt.Sprintf("B_death(0.05)/B_death(0.2) = %v, 契约 §3 要求 4.0±3%%", r))
		}
	}
	report(t, p)
}

// TestAnchorsMoirefieldHonesty 上游自写的诚实边界必须原文进锚点, 一条不许少。
func TestAnchorsMoirefieldHonesty(t *testing.T) {
	a := loadAnchors(t)
	if len(a.Moirefield.Honesty) != 5 {
		t.Fatalf("上游 moirefield honesty 是 5 条, 锚点里 %d 条", len(a.Moirefield.Honesty))
	}
	for i, h := range a.Moirefield.Honesty {
		if strings.TrimSpace(h) == "" {
			t.Fatalf("honesty[%d] 是空的", i)
		}
	}
	if a.Moirefield.HonestyNote == "" {
		t.Fatal("honesty_note 不许为空 (要说清这是上游口径, 不是本仓库的结论)")
	}
}

// --------------------------------------------------------------------------- mudynamics (TD19–TD21)

// TestAnchorsCloseStep 钉死关闭步: 上游整数字段、解析复算、以及两条递推见证。
//
// 这是最容易被含糊过去的一处: 解析值 164.2411… 与整数步 165 说的不是同一件事。
func TestAnchorsCloseStep(t *testing.T) {
	a := loadAnchors(t)
	var p []string
	n := a.Mudynamics.NClose

	if n.UpstreamFieldInt <= 0 {
		t.Fatalf("上游整数关闭步必须为正, got %d", n.UpstreamFieldInt)
	}
	if n.SummaryTxtInt != n.UpstreamFieldInt {
		// summary.txt 的 N11 行与 report.json 的 N15 字段必须一致 (两处口径同一个数)。
		t.Fatalf("summary.txt 的 n*=%d 与 report.json 的 %d 不一致 —— 上游两处口径打架",
			n.SummaryTxtInt, n.UpstreamFieldInt)
	}
	if n.MuThresholdPrinted <= 0 || n.Eta <= 0 || n.Eta >= 1 {
		t.Fatalf("锚点里的阈值/η 不合理: μ≥%v η=%v", n.MuThresholdPrinted, n.Eta)
	}

	// 1) 解析值: Go 复算必须复现 emit 脚本的独立复算 (容差 1e-3)。
	checkAnchors(t, &p, "n_close 解析值", WindowCloseStep(n.AnalyticRecomputeMu0, n.Eta, Floor),
		n.AnalyticRecompute, 1e-3)

	// 2) 整数语义: FirstClosedStep = ceil(解析值), 并且必须**等于上游那个整数字段**。
	got := FirstClosedStep(n.AnalyticRecomputeMu0, n.Eta, Floor)
	if got != n.UpstreamFieldInt {
		p = append(p, fmt.Sprintf("FirstClosedStep = %d, 上游字段是 %d（ceil 语义与上游不一致）",
			got, n.UpstreamFieldInt))
	}
	if got != int(math.Ceil(n.AnalyticRecompute)) {
		p = append(p, fmt.Sprintf("FirstClosedStep = %d 不等于 ceil(%v)", got, n.AnalyticRecompute))
	}
	// 解析值是实数、且在 (n*−1, n*] 里 —— 别把 165 直接写成常量蒙过去。
	if !(n.AnalyticRecompute > float64(n.UpstreamFieldInt-1) &&
		n.AnalyticRecompute <= float64(n.UpstreamFieldInt)) {
		p = append(p, fmt.Sprintf("解析值 %v 不在 (%d, %d] 内",
			n.AnalyticRecompute, n.UpstreamFieldInt-1, n.UpstreamFieldInt))
	}

	// 3) 递推见证: 上一步没关、这一步关了 (用本包的闭式解逐位算, 不看答案)。
	if reason := 1 - MuAfterSteps(n.AnalyticRecomputeMu0, n.Eta, n.UpstreamFieldInt-1); reason <= Floor {
		p = append(p, fmt.Sprintf("第 %d 步就该关了 (1−μ=%.9e ≤ FLOOR=%.9e), 与上游 n*=%d 矛盾",
			n.UpstreamFieldInt-1, reason, Floor, n.UpstreamFieldInt))
	}
	if closed := 1 - MuAfterSteps(n.AnalyticRecomputeMu0, n.Eta, n.UpstreamFieldInt); closed > Floor {
		p = append(p, fmt.Sprintf("第 %d 步还没关 (1−μ=%.9e > FLOOR=%.9e), 与上游 n*=%d 矛盾",
			n.UpstreamFieldInt, closed, Floor, n.UpstreamFieldInt))
	}
	// 锚点文件必须自己说清两个来源不同 (upstream_field vs analytic_recompute)。
	if !strings.Contains(n.AnalyticRecomputeNote, "整数") ||
		!strings.Contains(n.AnalyticRecomputeSrc, "emit 脚本") {
		p = append(p, "n_close 没有写清「上游字段 vs 解析复算」的来源区分")
	}
	report(t, p)
}

// TestAnchorsMuDynamicsBooleans 上游 N15 的四条布尔见证必须被 Go 侧真的验出来。
func TestAnchorsMuDynamicsBooleans(t *testing.T) {
	a := loadAnchors(t)
	n := a.Mudynamics.NClose
	const mu0, eta = 0.0, 0.05
	// 用锚点里的 η (而不是写死 0.05), 这样上游改 η 时本测试跟着走。
	muStar := n.Eta
	_ = eta

	if a.Mudynamics.WindowMarginStrictlyDecreasing {
		prev := 1 - MuAfterSteps(mu0, muStar, 0)
		for k := 1; k <= 400; k++ {
			got := 1 - MuAfterSteps(mu0, muStar, k)
			if !(got < prev) {
				t.Fatalf("TD19 不成立: 第 %d 步的窗口余量没有严格收窄 (%v vs %v)", k, got, prev)
			}
			prev = got
		}
	}
	if a.Mudynamics.LockingFactorWellDefined {
		for k := 0; k <= 400; k++ {
			mu := MuAfterSteps(mu0, muStar, k)
			if !(1-mu > 0) {
				t.Fatalf("TD8 不成立: 第 %d 步 1−μ = %v ≤ 0", k, 1-mu)
			}
		}
	}
	if a.Mudynamics.LockingFactorStrictlyIncreasing {
		prev := LockingFactor(MuAfterSteps(mu0, muStar, 0))
		for k := 1; k <= 400; k++ {
			got := LockingFactor(MuAfterSteps(mu0, muStar, k))
			if !(got > prev) {
				t.Fatalf("TD20 不成立: 第 %d 步锁定因子没有严格递增 (%v vs %v)", k, got, prev)
			}
			prev = got
		}
	}
	// 解析式与 FC11b 阈值判据等价: X_req/FLOOR > 1 ⟺ 1−μ > FLOOR。
	if a.Mudynamics.ThresholdEquivalentToFC11b {
		for _, mu := range []float64{0.5, 0.9, 0.99, 0.999} {
			if !((1-mu > Floor) == (MuMinFromDelta(0) < mu)) {
				t.Fatalf("阈值判据不等价于 1−μ > FLOOR: μ=%v", mu)
			}
		}
	}
	if a.Mudynamics.TotalStepsBelowOne <= 0 {
		t.Fatalf("上游总步数见证不合理: %d", a.Mudynamics.TotalStepsBelowOne)
	}
	if len(a.Mudynamics.Honesty) != 5 {
		t.Fatalf("上游 mudynamics honest 是 5 条, 锚点里 %d 条", len(a.Mudynamics.Honesty))
	}
	// 状态方程与闭式解必须与上游原文的措辞对上 (口径漂移的第一道警报)。
	if !strings.Contains(a.Mudynamics.StateEquation, "μ") ||
		!strings.Contains(a.Mudynamics.ClosedForm, "(1 − η)") {
		t.Fatalf("状态方程/闭式解文本与上游不一致: %q / %q",
			a.Mudynamics.StateEquation, a.Mudynamics.ClosedForm)
	}
}

// --------------------------------------------------------------------------- fusionroadmap (D1/D2/D3)

// TestAnchorsVerdictQuantities 钉死 D1/D3 与 μ 天花板。
func TestAnchorsVerdictQuantities(t *testing.T) {
	a := loadAnchors(t)
	var p []string
	f := a.Fusionroadmap

	// D1: μ_min(δ) = δ/(1+δ) 与 R_ci = 1/(1−μ)−1 互为逆, 逐个 δ 对上游。
	for label, row := range f.MuMinFromDelta {
		delta, err := strconv.ParseFloat(strings.TrimPrefix(label, "δ="), 64)
		if err != nil {
			t.Fatalf("锚点里的 δ 标签解析不了: %q", label)
		}
		checkAnchors(t, &p, label+".μ_min", MuMinFromDelta(delta), row.MuMin, relAnchor)
		checkAnchors(t, &p, label+".R_ci(μ_min)", Rci(row.MuMin), row.RCiMuMin, relAnchor)
	}
	// D1 的签名对照: R_ci(μ) 与 τ_E 增益 = 1/√(1−μ)。
	if len(f.RciAtMu) == 0 {
		t.Fatal("锚点里没有 D1 的 μ → 签名对照表")
	}
	for label, row := range f.RciAtMu {
		mu, err := strconv.ParseFloat(strings.TrimPrefix(label, "μ="), 64)
		if err != nil {
			t.Fatalf("锚点里的 μ 标签解析不了: %q", label)
		}
		checkAnchors(t, &p, label+".R_ci", Rci(mu), row.RCi, relAnchor)
		checkAnchors(t, &p, label+".τ_E 增益", LockingFactor(mu), row.TauEGain, relAnchor)
	}

	// D3: 功率倍数 = (10^decades)^(1/k)。decades = 4 是上游那条曲线的口径。
	if len(f.PowerMultiple) != 4 {
		t.Fatalf("上游 D3 给了 4 个 k, 锚点里 %d 个", len(f.PowerMultiple))
	}
	for label, want := range f.PowerMultiple {
		k, err := strconv.ParseFloat(strings.TrimPrefix(label, "k="), 64)
		if err != nil {
			t.Fatalf("锚点里的 k 标签解析不了: %q", label)
		}
		checkAnchors(t, &p, label+" 功率倍数", PowerMultiple(k, 4), want, relAnchor)
	}
	// 每推进一个数量级那一列 = 上式开 4 次方。
	for label, want := range f.PowerMultiplePerDecade {
		k, err := strconv.ParseFloat(strings.TrimPrefix(label, "k="), 64)
		if err != nil {
			t.Fatalf("锚点里的 k 标签解析不了: %q", label)
		}
		checkAnchors(t, &p, label+" 每数量级", math.Pow(PowerMultiple(k, 4), 0.25), want, relAnchor)
	}

	// D2: FC5 预言与几何捕获主张都要在锚点里, 且后者必须被标注为未证。
	if !strings.Contains(f.FC5Prediction, "Λ") || !strings.Contains(f.GeometricCaptureClaim, "未证") {
		p = append(p, fmt.Sprintf("D2 的两条假说没写清 (预言=%q, 主张=%q)",
			f.FC5Prediction, f.GeometricCaptureClaim))
	}
	if len(f.LambdaHypotheses) == 0 {
		p = append(p, "锚点里没有 D2 的 Λ 数值")
	}

	// μ 天花板 = 1 − FLOOR, 对上游 summary.txt 的打印值 (0.999781, 6 位小数)。
	checkAnchors(t, &p, "μ 天花板 (1−FLOOR)", 1-Floor, f.SummaryText.MuMaxFC11DT, relPrintedFine)
	// summary.txt 的 δ=1e-4 行。
	checkAnchors(t, &p, "summary δ=1e-4", 1e-4, f.SummaryText.MuMinDelta, 0)
	checkAnchors(t, &p, "summary μ_min(1e-4)", MuMinFromDelta(1e-4), f.SummaryText.MuMin, relPrintedText)
	// summary 里的 k 表与 report.json 的 k 表必须同值。
	if len(f.SummaryText.PowerMultiple) != len(f.PowerMultiple) {
		p = append(p, fmt.Sprintf("summary 的 k 表 %d 行 vs report.json 的 %d 行",
			len(f.SummaryText.PowerMultiple), len(f.PowerMultiple)))
	}
	for label, want := range f.SummaryText.PowerMultiple {
		checkAnchors(t, &p, "summary "+label, want, f.PowerMultiple[label], relPrintedText)
	}
	report(t, p)
}

// TestAnchorsFusionHonestyAndLadder 上游诚实边界的原文与「阶梯不进判据」的标注。
func TestAnchorsFusionHonestyAndLadder(t *testing.T) {
	a := loadAnchors(t)
	f := a.FusionRoadmapHonesty()
	if len(f) != 4 {
		t.Fatalf("上游 fusionroadmap 诚实边界是 4 条, 锚点里 %d 条", len(f))
	}
	for i, h := range f {
		if strings.TrimSpace(h) == "" {
			t.Fatalf("诚实边界[%d] 是空的", i)
		}
	}
	// μ 阶梯 (排程假设) 只许出现在锚点里留证, 不许进判据 —— 本包的常量里没有它的位置。
	if len(a.Fusionroadmap.Ladder) != 8 {
		t.Fatalf("上游阶梯表应 8 行 (G0..G7), 锚点里 %d 行", len(a.Fusionroadmap.Ladder))
	}
	for _, row := range a.Fusionroadmap.Ladder {
		if row.Stage == "" || row.PersonMonths <= 0 {
			t.Fatalf("阶梯行不完整: %+v", row)
		}
	}
	if !strings.Contains(a.Fusionroadmap.LadderNote, "不得使用") {
		t.Fatalf("阶梯必须被标注为「本层不得使用」, got %q", a.Fusionroadmap.LadderNote)
	}
}

// TestSinkMuAnchors 钉死 CR9 自抹平的**上游数字**（sinkmuopt 报告）：
// 工作点 / 对比结果逐位对锚点文件，Go 复算与上游数字一致。
// 与 design_test.go 的 TestSinkMuSelfFlattening（性质断言）互补：
// 那边证「自抹平更快/递增/衰减」，这边证「我们的数与上游 report.json 逐位一致」。
func TestSinkMuAnchors(t *testing.T) {
	a := loadAnchors(t)
	s := a.Sinkmuopt

	// 工作点
	wp := s.Workpoint
	if err := anchorDiff("sinkmuopt.mu0", wp.Mu0, 0.15, relAnchor); err != nil {
		t.Error(err)
	}
	if err := anchorDiff("sinkmuopt.eta_ext", wp.EtaExt, 0.05, relAnchor); err != nil {
		t.Error(err)
	}
	if err := anchorDiff("sinkmuopt.lam", wp.Lam, 0.3, relAnchor); err != nil {
		t.Error(err)
	}
	// η_sink(0.3) = 2*0.3 − 0.3² = 0.51
	if err := anchorDiff("sinkmuopt.eta_sink", wp.EtaSink, 0.51, relAnchor); err != nil {
		t.Error(err)
	}
	// Go 复算 SinkEta(lam) 必须等于上游报告的 η_sink。
	if err := anchorDiff("SinkEta(lam) vs 上游", SinkEta(wp.Lam), wp.EtaSink, relAnchor); err != nil {
		t.Error(err)
	}

	// 对比结果: C1 132→85, 快 47 步; 外部依赖下降。
	c := s.Compare
	if c.NWorkOld != 132 || c.NWorkNew != 85 || c.WindowSave != 47 {
		t.Errorf("上游对比结果应为 132→85/47, got %d→%d/%d", c.NWorkOld, c.NWorkNew, c.WindowSave)
	}
	if !c.LessExtDep {
		t.Error("上游报告: 外部 RMF 减半 + 自抹平仍达工作窗口 ⟹ LessExtDep 应为 true")
	}
	// Go 复算: 到达 μ=0.999 的步数必须复现上游 132→85。
	mu0, etaExt, lam, muWork := wp.Mu0, wp.EtaExt, wp.Lam, 0.999
	const maxSteps = 100000
	oldSteps := SinkMuWorkSteps(mu0, etaExt, 0, muWork, maxSteps)
	newSteps := SinkMuWorkSteps(mu0, etaExt, lam, muWork, maxSteps)
	if oldSteps != c.NWorkOld {
		t.Errorf("SinkMuWorkSteps(无自抹平) = %d, 上游报告 %d", oldSteps, c.NWorkOld)
	}
	if newSteps != c.NWorkNew {
		t.Errorf("SinkMuWorkSteps(+自抹平 λ=0.3) = %d, 上游报告 %d", newSteps, c.NWorkNew)
	}

	// checks: C1/C2/C3/C4 全通过, 且 4 条都在。
	for _, k := range []string{"C1", "C2", "C3", "C4"} {
		ck, ok := s.Checks[k]
		if !ok {
			t.Fatalf("sinkmuopt checks 缺 %s", k)
		}
		if !ck.Pass {
			t.Errorf("上游 %s 应为通过, 锚点里 pass=false", k)
		}
		if strings.TrimSpace(ck.Detail) == "" {
			t.Errorf("%s 的细节为空", k)
		}
	}

	// 诚实边界 4 条, 非空。
	if len(s.Honesty) != 4 {
		t.Errorf("上游 sinkmuopt 诚实边界应为 4 条, 锚点里 %d 条", len(s.Honesty))
	}
	for i, h := range s.Honesty {
		if strings.TrimSpace(h) == "" {
			t.Errorf("sinkmuopt 诚实边界[%d] 为空", i)
		}
	}
}

// --------------------------------------------------------------------------- 锚点文件的 JSON 结构

type ppAnchors struct {
	Provenance    ppProvenance    `json:"provenance"`
	Moirefield    ppMoirefield    `json:"moirefield"`
	Mudynamics    ppMudynamics    `json:"mudynamics"`
	Fusionroadmap ppFusionroadmap `json:"fusionroadmap"`
	Sinkmuopt     ppSinkmuopt     `json:"sinkmuopt"`
}

type ppProvenance struct {
	Generator                     string   `json:"generator"`
	UpstreamRoot                  string   `json:"upstream_root"`
	UpstreamCommit                string   `json:"upstream_commit"`
	UpstreamCommitSubject         string   `json:"upstream_commit_subject"`
	UpstreamArtifactsTrackedByGit int      `json:"upstream_artifacts_tracked_by_git"`
	GeneratedAtUTC                string   `json:"generated_at_utc"`
	GeneratedAtLocal              string   `json:"generated_at_local"`
	ReadFrom                      []string `json:"read_from"`
	SkippedFields                 []string `json:"skipped_fields"`
	SourceOfTruth                 string   `json:"source_of_truth"`
	CheckNote                     string   `json:"check_note"`
	ToleranceNote                 string   `json:"tolerance_note"`
}

type ppConstants struct {
	Mu0        float64 `json:"mu0"`
	KB         float64 `json:"k_B"`
	TKeV       float64 `json:"T_keV"`
	Beta       float64 `json:"beta"`
	NDesign    float64 `json:"n_design"`
	ARef       float64 `json:"a_ref"`
	D0         float64 `json:"D0"`
	Tau0S      float64 `json:"tau0_s"`
	FloorFC11  float64 `json:"floor_FC11"`
	BDeathRefT float64 `json:"B_death_ref_T"`
}

type ppSource struct {
	Key                 string  `json:"key"`
	Label               string  `json:"label"`
	Citation            string  `json:"citation"`
	BCapT               float64 `json:"B_cap_T"`
	NMaxPerm3           float64 `json:"n_max_perm3"`
	NOpPerm3            float64 `json:"n_op_perm3"`
	ChiBRequiredOverCap float64 `json:"chi_B_required_over_cap"`
	BetaImpossible      bool    `json:"beta_impossible"`
	TauLawsonS          float64 `json:"tau_lawson_s"`
	XReqCap             float64 `json:"X_req_cap"`
	ChiMu               float64 `json:"chi_mu"`
	Feasible            bool    `json:"feasible"`
	PRel                float64 `json:"p_rel"`
	VRelForSamePower    float64 `json:"V_rel_for_same_power"`
	M8MuFeasible        bool    `json:"m8_mu_feasible"`
	M8Verdict           string  `json:"m8_verdict"`
}

type ppDeathRow struct {
	AM            float64  `json:"a_m"`
	BDeathT       float64  `json:"B_death_T"`
	ClosedSources []string `json:"closed_sources"`
	OpenSources   []string `json:"open_sources"`
}

type ppMoireSummaryText struct {
	Tau0S         float64 `json:"tau0_s"`
	FloorFC11     float64 `json:"floor_FC11"`
	BMinDesignT   float64 `json:"b_min_design_T"`
	BDeathRefT    float64 `json:"b_death_ref_T"`
	ChiMuAtDesign float64 `json:"chi_mu_at_design"`
}

type ppMoirefield struct {
	ReportJSON    string             `json:"report_json"`
	SummaryTxt    string             `json:"summary_txt"`
	Constants     ppConstants        `json:"constants"`
	NDesignPerm3  float64            `json:"n_design_perm3"`
	BMinDesignT   float64            `json:"b_min_design_T"`
	XReqAtDesign  float64            `json:"x_req_at_design"`
	ChiMuAtDesign float64            `json:"chi_mu_at_design"`
	FloorFC11     float64            `json:"floor_FC11"`
	Tau0S         float64            `json:"tau0_s"`
	BDeathRefT    float64            `json:"b_death_ref_T"`
	Sources       []ppSource         `json:"sources"`
	DeathScan     []ppDeathRow       `json:"death_scan"`
	Honesty       []string           `json:"honesty"`
	HonestyNote   string             `json:"honesty_note"`
	SummaryText   ppMoireSummaryText `json:"summary_text"`
}

type ppNClose struct {
	UpstreamFieldInt      int     `json:"upstream_field_int"`
	UpstreamFieldPath     string  `json:"upstream_field_path"`
	SummaryTxtInt         int     `json:"summary_txt_int"`
	MuThresholdPrinted    float64 `json:"mu_threshold_printed"`
	Eta                   float64 `json:"eta"`
	EtaSource             string  `json:"eta_source"`
	AnalyticRecompute     float64 `json:"analytic_recompute"`
	AnalyticRecomputeMu0  float64 `json:"analytic_recompute_mu0"`
	AnalyticRecomputeSrc  string  `json:"analytic_recompute_source"`
	AnalyticRecomputeNote string  `json:"analytic_recompute_note"`
}

type ppMudynamics struct {
	ReportJSON                      string   `json:"report_json"`
	SummaryTxt                      string   `json:"summary_txt"`
	StateEquation                   string   `json:"state_equation"`
	ClosedForm                      string   `json:"closed_form"`
	NClose                          ppNClose `json:"n_close"`
	WindowMarginStrictlyDecreasing  bool     `json:"window_margin_strictly_decreasing"`
	LockingFactorWellDefined        bool     `json:"locking_factor_well_defined"`
	LockingFactorStrictlyIncreasing bool     `json:"locking_factor_strictly_increasing"`
	ThresholdEquivalentToFC11b      bool     `json:"threshold_equivalent_to_FC11b"`
	TotalStepsBelowOne              int      `json:"total_steps_below_one"`
	Honesty                         []string `json:"honesty"`
	HonestyNote                     string   `json:"honesty_note"`
}

type ppMuMin struct {
	MuMin    float64 `json:"mu_min"`
	RCiMuMin float64 `json:"R_ci_mu_min"`
}

type ppRci struct {
	RCi      float64 `json:"R_ci"`
	TauEGain float64 `json:"tauE_gain"`
}

type ppLambda struct {
	FC5              float64 `json:"FC5"`
	GeometricCapture float64 `json:"geometric_capture"`
}

type ppLadderRow struct {
	Stage         string  `json:"阶段"`
	Months        string  `json:"月区间"`
	MuTarget      float64 `json:"μ 目标"`
	DeviceScale   string  `json:"装置尺度"`
	PersonMonths  float64 `json:"人·月"`
	CumulativePct float64 `json:"累计人·月占比 [%]"`
}

type ppFusionSummaryText struct {
	MuMaxFC11DT   float64            `json:"mu_max_FC11_DT"`
	MuMinDelta    float64            `json:"mu_min_delta"`
	MuMin         float64            `json:"mu_min"`
	PowerMultiple map[string]float64 `json:"power_multiple"`
}

type ppFusionroadmap struct {
	ReportJSON             string              `json:"report_json"`
	SummaryTxt             string              `json:"summary_txt"`
	MuMinFromDelta         map[string]ppMuMin  `json:"mu_min_from_delta"`
	RciAtMu                map[string]ppRci    `json:"rci_at_mu"`
	LambdaHypotheses       map[string]ppLambda `json:"lambda_hypotheses"`
	FC5Prediction          string              `json:"fc5_prediction"`
	GeometricCaptureClaim  string              `json:"geometric_capture_claim"`
	PowerMultiple          map[string]float64  `json:"power_multiple"`
	PowerMultiplePerDecade map[string]float64  `json:"power_multiple_per_decade"`
	Ladder                 []ppLadderRow       `json:"ladder"`
	LadderNote             string              `json:"ladder_note"`
	Honesty                []string            `json:"honesty"`
	HonestyNote            string              `json:"honesty_note"`
	SummaryText            ppFusionSummaryText `json:"summary_text"`
}

// FusionRoadmapHonesty 只是让上面那条测试读起来短一点。
func (a ppAnchors) FusionRoadmapHonesty() []string { return a.Fusionroadmap.Honesty }

type ppSinkWorkpoint struct {
	Mu0     float64 `json:"mu0"`
	EtaExt  float64 `json:"eta_ext"`
	Lam     float64 `json:"lam"`
	EtaSink float64 `json:"eta_sink"`
}

type ppSinkCompare struct {
	NJudgeOld  int  `json:"n_judge_old"`
	NJudgeNew  int  `json:"n_judge_new"`
	NWorkOld   int  `json:"n_work_old"`
	NWorkNew   int  `json:"n_work_new"`
	WindowSave int  `json:"工作窗口节省步数"`
	LessExtDep bool `json:"对外部依赖下降（η_ext 减半仍达工作窗口）"`
}

type ppSinkCheck struct {
	Pass   bool   `json:"通过"`
	Detail string `json:"细节"`
}

type ppSinkmuopt struct {
	ReportJSON  string                 `json:"report_json"`
	Workpoint   ppSinkWorkpoint        `json:"工作点"`
	Compare     ppSinkCompare          `json:"对比结果"`
	Checks      map[string]ppSinkCheck `json:"checks"`
	Honesty     []string               `json:"honesty"`
	HonestyNote string                 `json:"honesty_note"`
}
