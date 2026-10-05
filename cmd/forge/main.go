// Command forge 是 HUSHFUSION Forge 引擎的唯一入口。
//
// 它是*接线*的负责人: 其他每个包都从这里可达, 别处则无路可达。这些命令对应
// CONTRACT.md 中的验收门:
//
//	baseline    机器被要求超越的那个人, 连同它全部的原始项
//	verify      端到端集成门 (G4/G5/G8/G10 的输入, 一张 PASS/FAIL 表)
//	design      内部设计判决层: 六道门 + 上游 ProjectionPhysics 的闭式解与锚点
//	xcheck      为独立的 Python oracle 导出 Br/Bz/|B| (G5 输入)
//	run         一种算法、一个 seed、一份预算, 经过 registry (G7)
//	benchmark   跨方法与跨 seed 的等预算对比
//	rules       从 registry 中挖掘可复现的 design 规则
//	report      渲染中文运行报告, 含诚实边界章节
//	registry    检视一个 registry 并检查其完整性 (G8)
//	world       世界协议: 把 internal/rlenv 的语义搬到进程边界之外 (G18)
//	version     版本、spec 形状与冻结的 schema 事实
//
// 退出码: 0 成功, 1 某项检查失败 / 某个包报了错, 2 用法错误。任何命令上的 --help
// 都以 0 退出。
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/logos-42/hushfusion-forge/internal/baseline"
	"github.com/logos-42/hushfusion-forge/internal/config"
	"github.com/logos-42/hushfusion-forge/internal/design"
	"github.com/logos-42/hushfusion-forge/internal/experiment"
	"github.com/logos-42/hushfusion-forge/internal/knowledge"
	"github.com/logos-42/hushfusion-forge/internal/objective"
	"github.com/logos-42/hushfusion-forge/internal/physics"
	"github.com/logos-42/hushfusion-forge/internal/registry"
	"github.com/logos-42/hushfusion-forge/internal/report"
	"github.com/logos-42/hushfusion-forge/internal/rlenv"
	"github.com/logos-42/hushfusion-forge/internal/runner"
	"github.com/logos-42/hushfusion-forge/internal/search"
	"github.com/logos-42/hushfusion-forge/internal/world"
)

// Version 是引擎版本。它是 internal/config.ForgeVersion 的**别名**, 不是一个独立副本:
// 判据版本必须只有一个家, 否则 runs/ 里记的版本与 CLI 报的版本会静默分叉。
const Version = config.ForgeVersion

// buildCommit 在构建时注入:
//
//	go build -ldflags "-X main.buildCommit=$(git rev-parse HEAD)" ./cmd/forge
//
// 这里的 Go 标准库无法调起 git (本次构建的依赖集中没有 os/exec), 因此在没有注入、
// 且 FORGE_GIT_COMMIT 未设置时, 我们打印 "unknown", 而不是猜一个 commit。
var buildCommit = ""

// 相对仓库的默认值。路径从工作目录解析, 并对 golden 测试数据做向上搜索 (见
// findFile), 这样在子目录里运行时不会悄悄拿空值做比较。
const (
	defTag         = "phase0"
	defRunDir      = "runs/phase0"
	defRunTag      = "ad_hoc"
	defRunRegistry = "runs/scratch/registry.jsonl"
	// defWorldRegistry 是世界协议的注册表: 契约 §4 要求 trace 的 runs 不进仓,
	// runs/scratch/ 已被 .gitignore 忽略。被提交的证据是 trace 文件本身。
	defWorldRegistry = "runs/scratch/worldtrace/registry.jsonl"
	defXcheckOut     = "runs/scratch/field_samples.json"
	defRulesOut      = "knowledge/design_rules.md"
	defReportOut     = "runs/phase0/report.md"
	goldenBaseline   = "testdata/golden_baseline.json"
	goldenSamples    = "testdata/golden_field_samples.json"
	goldenSpec       = "testdata/golden_spec.json"
	solverName       = "analytic-vacuum-loops"
)

// 由 CONTRACT.md §5 固定的容差。放宽它们等于篡改验收标准本身, 因此它们是具名
// 常量, 永远不是 flag。
const (
	tolScore   = 1e-6 // |score - golden_score|
	tolMetrics = 1e-6 // 相对, 每个 metric
	tolTerms   = 1e-6 // 绝对, 每个原始 score 项 (见 compareBaselineEval)
	tolField   = 1e-9 // 相对, 每个 |B| 场采样点 (合同的标准)
	tolComp    = 1e-6 // 相对, 每个 Br/Bz 分量 (见 compareFieldSeries)
	tolSolvers = 1e-9 // 相对, analytic vs discrete Biot–Savart
)

var allMethods = []string{"random", "lhs", "evolution", "evolution_warm", "evolution_knowledge"}

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if len(args) == 0 {
		usage(os.Stderr)
		return 2
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "help", "-h", "--help":
		usage(os.Stdout)
		return 0
	case "version", "--version", "-V":
		return cmdVersion(rest)
	case "baseline", "base":
		return cmdBaseline(rest)
	case "verify":
		return cmdVerify(rest)
	case "design":
		return cmdDesign(rest)
	case "xcheck":
		return cmdXcheck(rest)
	case "run":
		return cmdRun(rest)
	case "benchmark", "bench":
		return cmdBenchmark(rest)
	case "rules":
		return cmdRules(rest)
	case "report":
		return cmdReport(rest)
	case "registry", "reg":
		return cmdRegistry(rest)
	case "world":
		return cmdWorld(rest)
	default:
		fmt.Fprintf(os.Stderr, "forge: unknown command %q\n\n", cmd)
		usage(os.Stderr)
		return 2
	}
}

func usage(w *os.File) {
	fmt.Fprintf(w, `usage: forge <command> [flags]

HUSHFUSION Forge v%s — the design loop: parameterise → physics → score → search → registry → rules.

commands:
  baseline    print the human baseline: design, metrics, terms, score
  verify      end-to-end integration gate (spec → baseline → golden → registry → anti-fabrication)
  design      internal design gate: six gates + upstream ProjectionPhysics closed forms
  xcheck      export Br/Bz/|B| samples for the independent Python oracle (G5 input)
  run         run one algorithm at one seed and budget, recording into the registry
  benchmark   equal-budget benchmark across methods and seeds
  rules       mine replicated design rules from a registry
  report      render the markdown run report
  registry    inspect a registry and check its integrity
  world       world protocol: serve/replay the design world (docs/world-protocol.md)
  version     print version, spec shape and frozen schema facts

exit codes: 0 ok, 1 a gate/command failed, 2 usage error.
'forge <command> --help' prints the flags of one command.
environment: FORGE_GIT_COMMIT overrides the commit reported by 'forge version'.
`, Version)
}

// ---------------------------------------------------------------------------
// flag 管道
// ---------------------------------------------------------------------------

func newFlagSet(name, synopsis, blurb string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: forge %s\n\n%s\n\nflags:\n", synopsis, blurb)
		fs.PrintDefaults()
	}
	return fs
}

// parseFlags 在调用方应继续时返回 -1, 否则返回进程退出码 (--help 为 0, 解析错误
// 或多余的位置参数为 2)。
func parseFlags(fs *flag.FlagSet, args []string) int {
	err := fs.Parse(args)
	if err == nil {
		if fs.NArg() > 0 {
			fmt.Fprintf(os.Stderr, "forge %s: unexpected argument %q\n\n", fs.Name(), fs.Arg(0))
			fs.Usage()
			return 2
		}
		return -1
	}
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	return 2
}

func fail(format string, args ...any) int {
	fmt.Fprintf(os.Stderr, "forge: error: "+format+"\n", args...)
	return 1
}

func note(format string, args ...any) { fmt.Printf(format+"\n", args...) }

// ---------------------------------------------------------------------------
// 共享辅助函数
// ---------------------------------------------------------------------------

func defaultSpec() config.Spec { return config.DefaultSpec() }

// analyticEvaluator 基于精确 (椭圆积分) solver 构建求值器, 并以人类 baseline 的
// 欧姆代价作为 cost 参考, 这样 "cost == 1.0" 就意味着“和人类设计一样贵”。
func analyticEvaluator(spec config.Spec, costRef float64) *objective.Evaluator {
	return objective.NewEvaluator(spec, physics.AnalyticSolver{}, costRef, physics.BuildGrids(spec))
}

func baselineDesign() (baseline.Baseline, error) {
	base, err := baseline.TextbookMirror(config.DefaultSpec())
	if err != nil {
		return baseline.Baseline{}, fmt.Errorf("baseline.TextbookMirror: %w", err)
	}
	return base, nil
}

// findFile 解析相对仓库的路径, 从工作目录向上搜索 (最多 6 层), 因此命令在子目录
// 里依然可用。
func findFile(rel string) (string, error) {
	if _, err := os.Stat(rel); err == nil {
		return rel, nil
	}
	dir, err := os.Getwd()
	if err != nil {
		return rel, err
	}
	for i := 0; i < 6; i++ {
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
		cand := filepath.Join(dir, rel)
		if _, err := os.Stat(cand); err == nil {
			return cand, nil
		}
	}
	return rel, fmt.Errorf("%s not found (searched upward from the working directory)", rel)
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func writeJSON(path string, v any) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func openRegistry(path string) (*registry.Registry, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	reg, err := registry.Open(path)
	if err != nil {
		return nil, fmt.Errorf("registry.Open(%s): %w", path, err)
	}
	return reg, nil
}

func openExistingRegistry(path string) (*registry.Registry, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("registry %s does not exist (run 'forge benchmark' or 'forge run' first)", path)
	}
	return openRegistry(path)
}

// topDesigns 从 registry 读 feasible 且 score 最高的前 K 个设计, 转成搜索设计向量。
// 这是 evolution_knowledge 的整代播种来源 —— 继承上一轮学到的最优, 而非人工基线。
func topDesigns(reg *registry.Registry, spec config.Spec, k int) ([][]float64, error) {
	recs, err := reg.Records()
	if err != nil {
		return nil, err
	}
	// 收集 feasible 记录, 按 score 降序
	feasible := make([]registry.Record, 0, len(recs))
	for _, r := range recs {
		if r.Feasible {
			feasible = append(feasible, r)
		}
	}
	sort.Slice(feasible, func(i, j int) bool { return feasible[i].Score > feasible[j].Score })
	if k > len(feasible) {
		k = len(feasible)
	}
	if k == 0 {
		return nil, nil
	}
	nCoils := len(feasible[0].Params.RadiusM)
	out := make([][]float64, 0, k)
	for _, r := range feasible[:k] {
		d := make([]float64, 0, 3*nCoils)
		d = append(d, r.Params.RadiusM...)
		if len(r.Params.ZM) == nCoils {
			d = append(d, r.Params.ZM...)
		}
		if len(r.Params.CurrentA) == nCoils {
			d = append(d, r.Params.CurrentA...)
		}
		if len(d) != spec.NParams() {
			continue // 结构不匹配的设计向量跳过(不应发生, 防御性)
		}
		out = append(out, d)
	}
	return out, nil
}

// designRule 是 design_rules.md 里一条规则(机器可读块)的最小形态。
type designRule struct {
	Parameter string  // 参数名, 如 "I_3" / "radius_0" / "z_1"
	Term      string  // 关联的 term, 如 "cost"
	Rho       float64 // Spearman 秩相关(最差 case 绝对值)
	Direction int     // 该参数对 score 的贡献方向: +1 越大越好, -1 越小越好, 0 未知
}

// readRules 解析 design_rules.md 的 machine-readable JSON 块, 返回规则列表。
// 规则方向根据 term 对 score 的符号权重映射(见 internal/objective): cost/ripple 是负项,
// 参数与负项正相关 ⟹ 参数越大 score 越低(direction=-1); field/mirror/volume 是正项 ⟹ 反向。
func readRules(path string) ([]designRule, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	// 规则在 ```json ... ``` 代码块里
	start := bytes.Index(b, []byte("```json\n"))
	if start < 0 {
		return nil, fmt.Errorf("no ```json block in %s", path)
	}
	start += len("```json\n")
	end := bytes.Index(b[start:], []byte("\n```"))
	if end < 0 {
		return nil, fmt.Errorf("unterminated json block in %s", path)
	}
	raw := b[start : start+end]
	var recs []struct {
		RuleID     string  `json:"rule_id"`
		Parameter  string  `json:"parameter"`
		Term       string  `json:"term"`
		Rho        float64 `json:"rho"`
		SignAgree  float64 `json:"sign_agreement"`
		DecileLow  float64 `json:"decile_low"`
		DecileHigh float64 `json:"decile_high"`
	}
	if err := json.Unmarshal(raw, &recs); err != nil {
		return nil, fmt.Errorf("parse rules json: %w", err)
	}
	// term 对 score 的符号权重(与 objective.score 一致): 负项 cost/ripple
	termSign := map[string]int{"field": +1, "mirror": +1, "volume": +1, "ripple": -1, "cost": -1}
	out := make([]designRule, 0, len(recs))
	for _, r := range recs {
		ts, ok := termSign[r.Term]
		if !ok {
			continue
		}
		// parameter 正相关且 term 负项 ⟹ 参数越大越差(direction=-1)
		dir := 0
		if r.Rho > 0 {
			dir = ts
		} else if r.Rho < 0 {
			dir = -ts
		}
		out = append(out, designRule{Parameter: r.Parameter, Term: r.Term, Rho: r.Rho, Direction: dir})
	}
	return out, nil
}

// ruleSeededDesigns 按规则方向生成 K 个初始设计: 对 direction!=0 的参数, 采样偏向
// 规则推荐的子空间(正方向取盒子上半, 负方向取下半); 其余参数全盒均匀采样。
// 这是 Phase B "Rule" 组的整代播种 —— 用规则引导, 而不是冠军点。
func ruleSeededDesigns(spec config.Spec, rules []designRule, k int, rng *rand.Rand) [][]float64 {
	// 参数索引 → 方向。参数名形如 "I_3"/"radius_0"/"z_1", 定位到 design 向量槽。
	dirBySlot := map[int]int{}
	for _, r := range rules {
		if r.Direction == 0 {
			continue
		}
		idx := paramIndexByName(r.Parameter, spec)
		if idx >= 0 {
			dirBySlot[idx] = r.Direction
		}
	}
	lo, hi := spec.Lower(), spec.Upper()
	out := make([][]float64, 0, k)
	for i := 0; i < k; i++ {
		d := make([]float64, len(lo))
		for j := range d {
			if dir, ok := dirBySlot[j]; ok && dir != 0 {
				// 偏向子空间: 正方向取 [mid, hi], 负方向取 [lo, mid]
				if dir > 0 {
					d[j] = lo[j] + (hi[j]-lo[j])*(0.5+0.5*rng.Float64())
				} else {
					d[j] = lo[j] + (hi[j]-lo[j])*(0.5*rng.Float64())
				}
			} else {
				d[j] = lo[j] + (hi[j]-lo[j])*rng.Float64()
			}
		}
		out = append(out, search.Canonicalise(d, spec))
	}
	return out
}

// paramIndexByName 把 "I_3"/"radius_0"/"z_1" 映射到 design 向量槽位。
// 设计向量布局 = [radius_0..radius_n, z_0..z_n, I_0..I_n](见 registry.Params)。
func paramIndexByName(name string, spec config.Spec) int {
	prefixes := []string{"radius", "z", "I"}
	lo, hi := spec.Lower(), spec.Upper()
	for pi, p := range prefixes {
		if len(name) > len(p) && name[:len(p)] == p && name[len(p)] == '_' {
			var idx int
			if _, err := fmt.Sscanf(name[len(p)+1:], "%d", &idx); err != nil {
				return -1
			}
			slot := pi*spec.NCoils + idx
			if slot >= 0 && slot < len(lo) {
				return slot
			}
		}
	}
	_ = hi
	return -1
}

func resolveCommit() string {
	if c := os.Getenv("FORGE_GIT_COMMIT"); c != "" {
		return c
	}
	if buildCommit != "" {
		return buildCommit
	}
	return "unknown"
}

// splitList 解析一个逗号分隔的 flag 值 ("0,1,2" / "random,lhs"), 去掉空白并丢弃
// 空字段。
func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func splitInts(s string) ([]int, error) {
	var out []int
	for _, part := range splitList(s) {
		var v int
		if _, err := fmt.Sscanf(part, "%d", &v); err != nil {
			return nil, fmt.Errorf("cannot parse %q as an integer", part)
		}
		out = append(out, v)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// metrics 访问 (冻结的 JSON tag; 这里缺一个键就是 schema 漂移 bug)
// ---------------------------------------------------------------------------

var metricKeys = []string{
	"B_mid_T", "B_throat_T", "z_throat_m", "mirror_ratio", "volume_good",
	"ripple", "B_coil_max_T", "min_coil_gap_m", "min_clearance_m", "cost_proxy", "n_coils", "mu0",
}

// metricBoolKeys 是非 float 的 metrics。
var metricBoolKeys = []string{"coil_proximity_floor_hit"}

func metricValue(m physics.Metrics, key string) (float64, bool) {
	switch key {
	case "B_mid_T":
		return m.BMidT, true
	case "B_throat_T":
		return m.BThroatT, true
	case "z_throat_m":
		return m.ZThroatM, true
	case "mirror_ratio":
		return m.MirrorRatio, true
	case "volume_good":
		return m.VolumeGood, true
	case "ripple":
		return m.Ripple, true
	case "B_coil_max_T":
		return m.BCoilMaxT, true
	case "min_coil_gap_m":
		return m.MinCoilGapM, true
	case "cost_proxy":
		return m.CostProxy, true
	case "n_coils":
		return float64(m.NCoils), true
	case "min_clearance_m":
		return m.MinClearanceM, true
	case "mu0":
		return m.MU0, true
	case "coil_proximity_floor_hit":
		if m.CoilProximityFloorHit {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

// relDiff 是 golden 门使用的比较: 对真有量级的值用相对, 对按构造为零的值用绝对
// (与 0 做相对比较不是严格, 而是未定义)。
func relDiff(got, want float64) float64 {
	den := math.Max(math.Abs(want), 1e-9)
	if den == 0 {
		den = 1e-9
	}
	return math.Abs(got-want) / den
}

func metricMap(m physics.Metrics) map[string]float64 {
	out := map[string]float64{}
	for _, k := range metricKeys {
		if v, ok := metricValue(m, k); ok {
			out[k] = v
		}
	}
	for _, k := range metricBoolKeys {
		if v, ok := metricValue(m, k); ok {
			out[k] = v
		}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func printMetrics(m physics.Metrics, label string) {
	note("%s:", label)
	for _, k := range metricKeys {
		if v, ok := metricValue(m, k); ok {
			note("  %-24s %v", k, v)
		}
	}
	note("  %-24s %v", "coil_proximity_floor_hit", m.CoilProximityFloorHit)
}

func printTermTable(terms, weighted, penalties map[string]float64) {
	note("terms (raw):")
	for _, k := range sortedKeys(terms) {
		note("  %-24s %v", k, terms[k])
	}
	note("weighted:")
	for _, k := range sortedKeys(weighted) {
		note("  %-24s %v", k, weighted[k])
	}
	note("penalties:")
	if len(penalties) == 0 {
		note("  (none)")
	}
	for _, k := range sortedKeys(penalties) {
		note("  %-24s %v", k, penalties[k])
	}
}

func printDesign(design []float64) {
	n := len(design) / 3
	note("design (canonical, r|z|I), %d coils:", n)
	if n <= 0 {
		note("  (empty)")
		return
	}
	note("  %-4s %-14s %-14s %-16s", "#", "radius_m", "z_m", "current_A")
	for i := 0; i < n; i++ {
		note("  %-4d %-14v %-14v %-16v", i, design[i], design[n+i], design[2*n+i])
	}
	note("  r = %v", design[:n])
	note("  z = %v", design[n:2*n])
	note("  I = %v", design[2*n:])
}

// ---------------------------------------------------------------------------
// baseline —— 人类 baseline
// ---------------------------------------------------------------------------

func cmdBaseline(args []string) int {
	fs := newFlagSet("baseline", "baseline [--json] [--golden PATH]",
		"Print the human baseline (Helmholtz-like cell + mirror throats): its design,\n"+
			"every physics metric, every raw score term and the composite score.")
	asJSON := fs.Bool("json", false, "emit the baseline evaluation as JSON (machine readable)")
	golden := fs.String("golden", goldenBaseline, "golden baseline file to diff against (empty string to skip)")
	if code := parseFlags(fs, args); code >= 0 {
		return code
	}

	spec := defaultSpec()
	base, err := baselineDesign()
	if err != nil {
		return fail("%v", err)
	}
	ev := analyticEvaluator(spec, base.Cost)
	res := ev.Evaluate(base.Design)

	if *asJSON {
		payload := map[string]any{
			"name":      base.Name,
			"note":      base.Note,
			"design":    base.Design,
			"coils":     base.Coils,
			"cost_ref":  base.Cost,
			"score":     res.Score,
			"feasible":  res.Feasible,
			"terms":     res.Terms,
			"weighted":  res.Weighted,
			"penalties": res.Penalties,
			"metrics":   metricMap(res.Metrics),
			"spec":      spec.AsMap(),
		}
		b, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			return fail("marshal baseline: %v", err)
		}
		os.Stdout.Write(append(b, '\n'))
		return 0
	}

	note("human baseline: %s", base.Name)
	note("note: %s", base.Note)
	note("coils: %d", len(base.Coils))
	printDesign(base.Design)
	note("cost_ref (ohmic cost proxy) = %v", base.Cost)
	printMetrics(res.Metrics, "metrics")
	printTermTable(res.Terms, res.Weighted, res.Penalties)
	note("score = %v", res.Score)
	note("feasible = %v", res.Feasible)

	if *golden != "" {
		path, ferr := findFile(*golden)
		if ferr != nil {
			note("golden: %v (skipped)", ferr)
			return 0
		}
		var gold goldenBaselineFile
		if rerr := readJSON(path, &gold); rerr != nil {
			note("golden: %v (skipped)", rerr)
			return 0
		}
		d := math.Abs(res.Score - gold.Score)
		verdict := "within"
		if d > tolScore {
			verdict = "OUTSIDE"
		}
		note("%s: golden score = %v, |delta| = %v (%s tol %v) — run 'forge verify' for the full gate",
			path, gold.Score, d, verdict, tolScore)
	}
	return 0
}

// ---------------------------------------------------------------------------
// verify —— 端到端集成门
// ---------------------------------------------------------------------------

type goldenBaselineFile struct {
	Name      string             `json:"name"`
	Note      string             `json:"note"`
	Coils     []physics.Coil     `json:"coils"`
	CostProxy float64            `json:"cost_proxy"`
	CostRef   float64            `json:"cost_ref"`
	Design    []float64          `json:"design"`
	Feasible  bool               `json:"feasible"`
	Metrics   map[string]float64 `json:"metrics"`
	Penalties map[string]float64 `json:"penalties"`
	Score     float64            `json:"score"`
	Terms     map[string]float64 `json:"terms"`
	Weighted  map[string]float64 `json:"weighted"`
}

type goldenSample struct {
	Design     []float64 `json:"design"`
	DesignName string    `json:"design_name"`
	Solver     string    `json:"solver"`
	PointsR    []float64 `json:"points_r"`
	PointsZ    []float64 `json:"points_z"`
	Br         []float64 `json:"br"`
	Bz         []float64 `json:"bz"`
	BMag       []float64 `json:"b_mag"`
}

type goldenSamplesFile struct {
	Samples []goldenSample `json:"samples"`
}

type gate struct {
	name string
	ok   bool
	msg  string
}

// runGate 运行一道门, 把尚未实现的 stage 抛出的 panic 转成一行 FAIL 而不是一个
// 栈回溯: 门的意义是大声说出缺哪个 stage, 而不是把评审者的终端搞崩。
func runGate(name string, fn func() (string, error)) (g gate) {
	g.name = name
	defer func() {
		if r := recover(); r != nil {
			g.ok = false
			g.msg = fmt.Sprintf("FAIL: panic from an upstream package: %v", r)
		}
	}()
	msg, err := fn()
	if err != nil {
		g.ok = false
		g.msg = err.Error()
		return g
	}
	g.ok = true
	g.msg = msg
	return g
}

type verifyState struct {
	spec    config.Spec
	base    baseline.Baseline
	res     objective.EvalResult
	gold    goldenBaselineFile
	samples goldenSamplesFile
	specKey map[string]any

	// fieldGotBr 按 golden 采样下标保存算出的 Br 序列, 这样门的消息可以引用它最差
	// 偏差背后的真实数字。
	fieldGotBr map[int][]float64
}

func cmdVerify(args []string) int {
	fs := newFlagSet("verify", "verify [--verbose]",
		"Run the end-to-end integration gate and print a PASS/FAIL table:\n"+
			"  spec schema parity, baseline solve, golden score/metrics/terms, golden field\n"+
			"  samples, analytic-vs-discrete solver agreement, registry write/read round-trip\n"+
			"  with lineage integrity, the anti-fabrication gate (LoadPolicy must fail), and\n"+
			"  the rlenv observation layout.\n"+
			"Any FAIL exits non-zero.")
	verbose := fs.Bool("verbose", false, "print the full message of every gate, not just the first line")
	if code := parseFlags(fs, args); code >= 0 {
		return code
	}

	st := &verifyState{spec: defaultSpec()}
	note("forge verify — end-to-end integration gate (v%s)", Version)
	note("spec: n_coils=%d n_params=%d b_ref=%v mirror_ref=%v",
		st.spec.NCoils, st.spec.NParams(), st.spec.BRef, st.spec.MirrorRef)
	note("tolerances (CONTRACT.md §5): score %v, metrics/terms rel %v, field rel %v, solvers rel %v",
		tolScore, tolMetrics, tolField, tolSolvers)
	note("")

	gates := []gate{
		runGate("load testdata/golden_*.json", st.loadGolden),
		runGate("spec ↔ golden_spec schema parity", st.gateSpecParity),
		runGate("baseline solve (TextbookMirror)", st.gateBaselineSolve),
		runGate("baseline evaluation vs golden score+terms", st.gateGoldenScore),
		runGate("baseline metrics vs golden metrics", st.gateGoldenMetrics),
		runGate("golden field samples (analytic solver)", st.gateGoldenFields),
		runGate("analytic vs discrete Biot–Savart", st.gateSolvers),
		runGate("registry write → read-back consistency + lineage", st.gateRegistry),
		runGate("rlenv: LoadPolicy must fail (G10 anti-fabrication)", st.gateNoLearnedPolicy),
		runGate("rlenv: observation layout + episode lineage chain", st.gateRLEnvLayout),
	}

	nPass, nFail := 0, 0
	for i, g := range gates {
		status := "[PASS]"
		if !g.ok {
			status = "[FAIL]"
			nFail++
		} else {
			nPass++
		}
		head := strings.SplitN(g.msg, "\n", 2)[0]
		note("%s %2d. %-52s %s", status, i+1, g.name, head)
		if *verbose && strings.Contains(g.msg, "\n") {
			for _, line := range strings.Split(g.msg, "\n")[1:] {
				note("            %s", line)
			}
		}
	}
	note("")
	if nFail == 0 {
		note("verify: PASS (%d/%d gates)", nPass, len(gates))
		return 0
	}
	note("verify: FAIL (%d/%d gates, %d failed)", nPass, len(gates), nFail)
	return 1
}

func (st *verifyState) loadGolden() (string, error) {
	paths := map[string]string{}
	for _, rel := range []string{goldenBaseline, goldenSamples, goldenSpec} {
		p, err := findFile(rel)
		if err != nil {
			return "", err
		}
		paths[rel] = p
	}
	if err := readJSON(paths[goldenBaseline], &st.gold); err != nil {
		return "", err
	}
	if err := readJSON(paths[goldenSamples], &st.samples); err != nil {
		return "", err
	}
	if err := readJSON(paths[goldenSpec], &st.specKey); err != nil {
		return "", err
	}
	if len(st.samples.Samples) == 0 {
		return "", fmt.Errorf("%s contains no samples", paths[goldenSamples])
	}
	specKeys := 0
	if obj, ok := st.specKey["spec"].(map[string]any); ok {
		specKeys = len(obj)
	}
	return fmt.Sprintf("baseline score %v, %d field-sample designs, golden spec: %d spec keys + provenance",
		st.gold.Score, len(st.samples.Samples), specKeys), nil
}

// keySetsMatch 比较嵌套的键集合; 每个 golden 键都必须在 Go 侧存在 (任一方向缺键
// 都是 schema 破坏)。
func keySetsMatch(golden, goMap map[string]any, label string) error {
	for _, k := range sortedKeys(golden) {
		if _, ok := goMap[k]; !ok {
			return fmt.Errorf("%s: key %q present in the golden spec but absent from Spec.AsMap()", label, k)
		}
	}
	for _, k := range sortedKeys(goMap) {
		if _, ok := golden[k]; !ok {
			return fmt.Errorf("%s: key %q produced by Spec.AsMap() but absent from the golden spec", label, k)
		}
	}
	return nil
}

func (st *verifyState) gateSpecParity() (string, error) {
	// testdata/golden_spec.json 是 {"provenance": {...}, "spec": {...}}; parity 门
	// 关心的是 spec 对象本身。
	goldenSpec, ok := st.specKey["spec"].(map[string]any)
	if !ok {
		return "", errors.New(`golden spec has no "spec" object`)
	}
	got := st.spec.AsMap()
	if err := keySetsMatch(goldenSpec, got, "spec"); err != nil {
		return "", err
	}
	for _, nested := range []string{"bounds", "weights"} {
		goldenNested, ok := goldenSpec[nested].(map[string]any)
		if !ok {
			return "", fmt.Errorf("golden spec has no %q object", nested)
		}
		goNested, ok := got[nested].(map[string]any)
		if !ok {
			return "", fmt.Errorf("Spec.AsMap() has no %q object", nested)
		}
		if err := keySetsMatch(goldenNested, goNested, nested); err != nil {
			return "", err
		}
	}
	prov := ""
	if p, ok := st.specKey["provenance"].(map[string]any); ok {
		prov = fmt.Sprintf(" (golden provenance: python %v, numpy %v)", p["python"], p["numpy"])
	}
	return fmt.Sprintf("%d spec keys, bounds/weights nested keys match%s", len(got), prov), nil
}

func (st *verifyState) gateBaselineSolve() (string, error) {
	base, err := baselineDesign()
	if err != nil {
		return "", err
	}
	if len(base.Design) != st.spec.NParams() {
		return "", fmt.Errorf("baseline design has %d parameters, want %d", len(base.Design), st.spec.NParams())
	}
	if base.Cost <= 0 {
		return "", fmt.Errorf("baseline cost reference must be positive, got %v", base.Cost)
	}
	st.base = base
	// golden 文件里的 cost_ref 是 baseline 自身的欧姆代价。
	if st.gold.CostRef > 0 {
		if d := relDiff(base.Cost, st.gold.CostRef); d > tolMetrics {
			return "", fmt.Errorf("baseline cost %v vs golden cost_ref %v (rel diff %.3g > %v)",
				base.Cost, st.gold.CostRef, d, tolMetrics)
		}
	}
	return fmt.Sprintf("name=%s cost_ref=%v (golden rel diff %.2g)", base.Name, base.Cost,
		relDiff(base.Cost, st.gold.CostRef)), nil
}

// compareBaselineEval 就是 golden 比较本身, 从门里拆出来, 这样它可以在不进行任何
// 求值的情况下被测试 (并被弄红)。
//
// 容差规则: 综合 score 在 tolScore 上按绝对比较 (合同的标准)。原始项在 tolTerms 上
// 按绝对比较而不是相对, 因为 log10 项在接近 1 处病态: field 项是
// log10(B_mid/B_ref), 而 B_mid 是通过把电流解到约 1e-10 相对精度得到的, 因此一个
// 完全正确的实现也会落在距 golden 值 9.6e-17 约 3e-11 的位置 —— 在一个绝对量级为
// 1e-16 的项上, 这是 3e-2 的相对误差。绝对标准仍然能抓住任何真实偏差 (项误差 1e-3
// 就会失败), 而不会把浮点噪声读成不一致。
func compareBaselineEval(res objective.EvalResult, gold goldenBaselineFile) (string, error) {
	dScore := math.Abs(res.Score - gold.Score)
	if dScore > tolScore {
		return "", fmt.Errorf("score %v vs golden %v: |delta| %.3g > %v", res.Score, gold.Score, dScore, tolScore)
	}
	worstKey, worst := "", 0.0
	for _, k := range sortedKeys(gold.Terms) {
		want := gold.Terms[k]
		got, ok := res.Terms[k]
		if !ok {
			return "", fmt.Errorf("term %q present in golden but missing from EvalResult.Terms", k)
		}
		if d := math.Abs(got - want); d > worst {
			worstKey, worst = k, d
		}
	}
	if worst > tolTerms {
		return "", fmt.Errorf("term %q absolute diff %.3g > %v", worstKey, worst, tolTerms)
	}
	if res.Feasible != gold.Feasible {
		return "", fmt.Errorf("baseline feasibility %v vs golden %v", res.Feasible, gold.Feasible)
	}
	return fmt.Sprintf("score %v (|delta| %.2g), worst term %s |diff| %.2g, feasible %v",
		res.Score, dScore, worstKey, worst, res.Feasible), nil
}

// compareBaselineMetrics 比较 golden 文件携带的每个 metric, 并对 Go 侧不产出的
// golden metric 失败 (schema 漂移)。
func compareBaselineMetrics(m physics.Metrics, goldenMetrics map[string]float64) (string, error) {
	got := metricMap(m)
	worstKey, worst := "", 0.0
	checked := 0
	for _, k := range sortedKeys(goldenMetrics) {
		want := goldenMetrics[k]
		v, ok := got[k]
		if !ok {
			return "", fmt.Errorf("metric %q present in golden but not produced by physics.Metrics", k)
		}
		checked++
		if d := relDiff(v, want); d > worst {
			worstKey, worst = k, d
		}
	}
	if worst > tolMetrics {
		return "", fmt.Errorf("metric %q rel diff %.3g > %v", worstKey, worst, tolMetrics)
	}
	return fmt.Sprintf("%d metrics within rel %v, worst %s rel %.2g", checked, tolMetrics, worstKey, worst), nil
}

func (st *verifyState) gateGoldenScore() (string, error) {
	if len(st.base.Design) == 0 {
		return "", errors.New("no baseline available (earlier gate failed)")
	}
	ev := analyticEvaluator(st.spec, st.base.Cost)
	res := ev.Evaluate(st.base.Design)
	st.res = res
	return compareBaselineEval(res, st.gold)
}

func (st *verifyState) gateGoldenMetrics() (string, error) {
	if len(st.base.Design) == 0 {
		return "", errors.New("no baseline evaluation available (earlier gate failed)")
	}
	return compareBaselineMetrics(st.res.Metrics, st.gold.Metrics)
}

// fieldDiff 是在一个 design 的场序列中发现的最差相对偏差。
type fieldDiff struct {
	Mag, Br, Bz         float64
	MagAt, BrAt, BzAt   int
	MagSample, BrSample int
}

// compareFieldSeries 把一个 design 算出的场与其 golden 序列比较。
//
// 合同的 1e-9 标准是针对 |B| (AnalyticSolver.Magnitude) 的, 这道门也正是在它之上
// 失败。笛卡尔分量在 tolComp 上比较: B_r 在轴上为零, 且在轴上是由几乎相等的项相减
// 算出的, 因此近轴点带有远高于相对 1e-9 的条件数地板 —— 在 r=0.02 m 处 Br 出现
// 1e-8 的偏离是算术, 不是场算错。相对 1e-6 的分量误差确实会失败, 那才是抓住真实
// bug 的判据。
func compareFieldSeries(s goldenSample, gotMag, gotBr, gotBz []float64) (fieldDiff, error) {
	var d fieldDiff
	if len(gotMag) != len(s.BMag) || len(gotBr) != len(s.Br) || len(gotBz) != len(s.Bz) {
		return d, fmt.Errorf("design %s: solver returned %d/%d/%d values, golden has %d/%d/%d",
			s.DesignName, len(gotMag), len(gotBr), len(gotBz), len(s.BMag), len(s.Br), len(s.Bz))
	}
	for i := range gotMag {
		if r := relDiff(gotMag[i], s.BMag[i]); r > d.Mag {
			d.Mag, d.MagAt = r, i
		}
		if r := relDiff(gotBr[i], s.Br[i]); r > d.Br {
			d.Br, d.BrAt = r, i
		}
		if r := relDiff(gotBz[i], s.Bz[i]); r > d.Bz {
			d.Bz, d.BzAt = r, i
		}
	}
	if d.Mag > tolField {
		return d, fmt.Errorf("design %s: |B| rel %.3g > %v at point %d (r=%v z=%v: got %v, golden %v)",
			s.DesignName, d.Mag, tolField, d.MagAt, s.PointsR[d.MagAt], s.PointsZ[d.MagAt],
			gotMag[d.MagAt], s.BMag[d.MagAt])
	}
	if d.Br > tolComp {
		return d, fmt.Errorf("design %s: Br rel %.3g > %v (component tolerance) at point %d (r=%v z=%v: got %v, golden %v)",
			s.DesignName, d.Br, tolComp, d.BrAt, s.PointsR[d.BrAt], s.PointsZ[d.BrAt], gotBr[d.BrAt], s.Br[d.BrAt])
	}
	if d.Bz > tolComp {
		return d, fmt.Errorf("design %s: Bz rel %.3g > %v (component tolerance) at point %d (r=%v z=%v: got %v, golden %v)",
			s.DesignName, d.Bz, tolComp, d.BzAt, s.PointsR[d.BzAt], s.PointsZ[d.BzAt], gotBz[d.BzAt], s.Bz[d.BzAt])
	}
	return d, nil
}

func (st *verifyState) gateGoldenFields() (string, error) {
	worst := fieldDiff{}
	nPoints := 0
	solver := physics.AnalyticSolver{}
	for si, s := range st.samples.Samples {
		if len(s.PointsR) != len(s.PointsZ) {
			return "", fmt.Errorf("sample %d: %d r-points vs %d z-points", si, len(s.PointsR), len(s.PointsZ))
		}
		coils, err := physics.VectorToCoils(s.Design, st.spec)
		if err != nil {
			return "", fmt.Errorf("sample %d (%s): VectorToCoils: %w", si, s.DesignName, err)
		}
		mag := solver.Magnitude(coils, s.PointsR, s.PointsZ)
		br, bz := physics.CoilsetField(coils, s.PointsR, s.PointsZ)
		d, err := compareFieldSeries(s, mag, br, bz)
		if err != nil {
			return "", err
		}
		if st.fieldGotBr == nil {
			st.fieldGotBr = map[int][]float64{}
		}
		st.fieldGotBr[si] = br
		nPoints += len(mag)
		if d.Mag > worst.Mag {
			worst.Mag, worst.MagAt, worst.MagSample = d.Mag, d.MagAt, si
		}
		if d.Br > worst.Br {
			worst.Br, worst.BrAt, worst.BrSample = d.Br, d.BrAt, si
		}
		if d.Bz > worst.Bz {
			worst.Bz, worst.BzAt = d.Bz, d.BzAt
		}
	}
	ws := st.samples.Samples[worst.MagSample]
	bs := st.samples.Samples[worst.BrSample]
	// 在最差分量偏差旁打印 golden 值: 当 golden 场在那里恰好为零 (一个对称点) 时,
	// “相对”偏差实际上是一个约 1e-17 的绝对偏差, 也应该那样读。
	return fmt.Sprintf("%d designs × %d points: max rel |B| %.2g (tol %v) [%s at r=%v z=%v], max rel Br %.2g [%s at r=%v z=%v: %v vs golden %v], Bz %.2g (components tol %v)",
		len(st.samples.Samples), nPoints, worst.Mag, tolField, ws.DesignName,
		ws.PointsR[worst.MagAt], ws.PointsZ[worst.MagAt],
		worst.Br, bs.DesignName, bs.PointsR[worst.BrAt], bs.PointsZ[worst.BrAt],
		st.fieldGotBr[worst.BrSample][worst.BrAt], bs.Br[worst.BrAt],
		worst.Bz, tolComp), nil
}

func (st *verifyState) gateSolvers() (string, error) {
	// 在第一个 golden design 的锚点上交叉校验两个独立实现 (远离导体之处, 离散求和
	// 收敛最快)。
	s := st.samples.Samples[0]
	coils, err := physics.VectorToCoils(s.Design, st.spec)
	if err != nil {
		return "", err
	}
	analytic := physics.AnalyticSolver{}
	discrete := physics.DiscreteSolver{NSeg: 512}
	a := analytic.Magnitude(coils, s.PointsR, s.PointsZ)
	d := discrete.Magnitude(coils, s.PointsR, s.PointsZ)
	if len(a) != len(d) {
		return "", fmt.Errorf("solvers disagree on output length: %d vs %d", len(a), len(d))
	}
	worst := 0.0
	for i := range a {
		if rd := relDiff(d[i], a[i]); rd > worst {
			worst = rd
		}
	}
	if worst > tolSolvers {
		return "", fmt.Errorf("analytic vs discrete max rel diff %.3g > %v (nSeg=%d)", worst, tolSolvers, discrete.NSeg)
	}
	return fmt.Sprintf("%d points, max rel %.2g (nSeg=%d)", len(a), worst, discrete.NSeg), nil
}

func (st *verifyState) gateRegistry() (string, error) {
	if len(st.base.Design) == 0 || len(st.res.Terms) == 0 {
		return "", errors.New("no baseline evaluation available (earlier gate failed)")
	}
	dir, err := os.MkdirTemp("", "forge-verify-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "registry.jsonl")

	reg, err := openRegistry(path)
	if err != nil {
		return "", err
	}
	if reg.Len() != 0 {
		return "", fmt.Errorf("fresh registry reports Len()=%d, want 0", reg.Len())
	}
	wantExp, wantDesign := reg.NextIDs()
	if wantExp != 1 || wantDesign == "" {
		return "", fmt.Errorf("NextIDs() on an empty registry = (%d, %q), want (1, non-empty)", wantExp, wantDesign)
	}

	rec := baselineRecord(st.res, st.base)
	if err := reg.Append(rec); err != nil {
		return "", fmt.Errorf("Append: %w", err)
	}
	if reg.Len() != 1 {
		return "", fmt.Errorf("after one Append, Len()=%d, want 1", reg.Len())
	}

	recs, err := reg.Records()
	if err != nil {
		return "", fmt.Errorf("Records: %w", err)
	}
	if len(recs) != 1 {
		return "", fmt.Errorf("read back %d records, want 1", len(recs))
	}
	got := recs[0]
	if got.ExperimentID != wantExp {
		return "", fmt.Errorf("read-back ExperimentID=%d, want %d (gap-free ids)", got.ExperimentID, wantExp)
	}
	if got.DesignID != wantDesign {
		return "", fmt.Errorf("read-back DesignID=%q, want %q", got.DesignID, wantDesign)
	}
	if math.Abs(got.Score-st.res.Score) > 1e-12 {
		return "", fmt.Errorf("read-back score %v != evaluated score %v", got.Score, st.res.Score)
	}
	if diff := relDiff(got.Metrics.BMidT, st.res.Metrics.BMidT); diff > 1e-12 {
		return "", fmt.Errorf("read-back B_mid_T %v != %v", got.Metrics.BMidT, st.res.Metrics.BMidT)
	}
	for _, k := range sortedKeys(st.res.Terms) {
		if math.Abs(got.Terms[k]-st.res.Terms[k]) > 1e-12 {
			return "", fmt.Errorf("read-back term %q = %v != %v", k, got.Terms[k], st.res.Terms[k])
		}
	}
	if len(got.Params.RadiusM) != st.spec.NCoils || len(got.Params.ZM) != st.spec.NCoils ||
		len(got.Params.CurrentA) != st.spec.NCoils {
		return "", fmt.Errorf("read-back params: %d/%d/%d arrays, want %d each",
			len(got.Params.RadiusM), len(got.Params.ZM), len(got.Params.CurrentA), st.spec.NCoils)
	}

	// 第二条 record 作为子节点: lineage 完整性必须挺过往返。
	child := rec
	child.ParentDesign = got.DesignID
	child.Generation = got.Generation + 1
	if err := reg.Append(child); err != nil {
		return "", fmt.Errorf("Append child: %w", err)
	}
	problems, err := reg.Check()
	if err != nil {
		return "", fmt.Errorf("Check: %w", err)
	}
	if len(problems) != 0 {
		return "", fmt.Errorf("registry integrity problems: %v", problems)
	}
	fam := reg.Lineage()
	if len(fam[got.DesignID]) != 1 {
		return "", fmt.Errorf("lineage: %s has %d children, want 1", got.DesignID, len(fam[got.DesignID]))
	}
	return fmt.Sprintf("%d records, ids gap-free from 1, parent link intact, Check() clean", reg.Len()), nil
}

// baselineRecord 把一次已求值的 baseline 变成一条 registry record。
func baselineRecord(res objective.EvalResult, base baseline.Baseline) registry.Record {
	design := base.Design
	k := len(design) / 3
	params := registry.Params{
		RadiusM:  append([]float64(nil), design[:k]...),
		ZM:       append([]float64(nil), design[k:2*k]...),
		CurrentA: append([]float64(nil), design[2*k:]...),
	}
	return registry.Record{
		Algorithm:  "human_baseline",
		Seed:       0,
		Generation: 0,
		EvalIndex:  0,
		Tag:        "forge_verify",
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		Score:      res.Score,
		Feasible:   res.Feasible,
		Params:     params,
		Terms:      res.Terms,
		Weighted:   res.Weighted,
		Penalties:  res.Penalties,
		Metrics:    res.Metrics,
		Note:       "human baseline recorded by 'forge verify' round-trip gate",
	}
}

func (st *verifyState) gateNoLearnedPolicy() (string, error) {
	for _, p := range []string{"", "policy.json", "checkpoints/ppo.bin"} {
		err := rlenv.LoadPolicy(p)
		if err == nil {
			return "", fmt.Errorf("LoadPolicy(%q) returned nil — a learned policy must never be fabricated", p)
		}
		if !errors.Is(err, rlenv.ErrNoLearnedPolicy) {
			return "", fmt.Errorf("LoadPolicy(%q) = %v, want ErrNoLearnedPolicy", p, err)
		}
	}
	if !strings.Contains(rlenv.ErrNoLearnedPolicy.Error(), "Phase 1") {
		return "", errors.New("ErrNoLearnedPolicy does not name Phase 1")
	}
	return "LoadPolicy fails with ErrNoLearnedPolicy for every input (no learned policy exists)", nil
}

func (st *verifyState) gateRLEnvLayout() (string, error) {
	env := rlenv.NewEnv(nullScorer{}, st.spec, 0, 0)
	wantAction := st.spec.NParams()
	wantObs := wantAction + len(rlenv.ObsMetricKeys)
	if env.ActionDim() != wantAction {
		return "", fmt.Errorf("ActionDim=%d, want %d", env.ActionDim(), wantAction)
	}
	if env.ObservationDim() != wantObs {
		return "", fmt.Errorf("ObservationDim=%d, want %d", env.ObservationDim(), wantObs)
	}
	if len(rlenv.ObsMetricKeys) != len(rlenv.ObsMetricRefs) {
		return "", fmt.Errorf("%d metric keys vs %d refs", len(rlenv.ObsMetricKeys), len(rlenv.ObsMetricRefs))
	}

	// 端到端: 一条经过真实 runner 求值的 episode 必须作为一条连通的 lineage 链落进
	// registry (env → runner → registry)。
	if len(st.base.Design) == 0 {
		return "", errors.New("no baseline design available (earlier gate failed)")
	}
	dir, err := os.MkdirTemp("", "forge-verify-rlenv-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	reg, err := openRegistry(filepath.Join(dir, "registry.jsonl"))
	if err != nil {
		return "", err
	}
	ev := analyticEvaluator(st.spec, st.base.Cost)
	rn := runner.New(reg, ev, "forge_verify_rlenv")

	const steps = 3
	ep := rlenv.NewEnv(rn, st.spec, steps, 0.15)
	ep.Reset(st.base.Design)
	action := make([]float64, ep.ActionDim())
	rngState := uint64(12345)
	for i := 0; i < steps; i++ {
		for j := range action {
			// xorshift64: 确定, 且不需要额外 import
			rngState ^= rngState << 13
			rngState ^= rngState >> 7
			rngState ^= rngState << 17
			action[j] = 2*(float64(rngState%1_000_000)/1_000_000) - 1
		}
		_, _, terminated, truncated, _ := ep.Step(action)
		if terminated {
			return "", fmt.Errorf("episode reported terminated at step %d: the design space has no absorbing state", i)
		}
		if truncated && i != steps-1 {
			return "", fmt.Errorf("episode truncated at step %d of %d", i, steps)
		}
	}
	recs, err := reg.Records()
	if err != nil {
		return "", err
	}
	if len(recs) != steps+1 {
		return "", fmt.Errorf("episode wrote %d records, want %d", len(recs), steps+1)
	}
	for i := 1; i < len(recs); i++ {
		if recs[i].ParentDesign != recs[i-1].DesignID {
			return "", fmt.Errorf("record %d parent %q != previous design %q (lineage must be a chain)",
				i, recs[i].ParentDesign, recs[i-1].DesignID)
		}
	}
	if recs[0].ParentDesign != "" {
		return "", fmt.Errorf("episode root has parent %q, want none", recs[0].ParentDesign)
	}
	problems, err := reg.Check()
	if err != nil {
		return "", err
	}
	if len(problems) != 0 {
		return "", fmt.Errorf("episode registry problems: %v", problems)
	}
	return fmt.Sprintf("action_dim=%d obs_dim=%d; %d-step episode wrote a connected lineage chain",
		wantAction, wantObs, steps), nil
}

// nullScorer 为从不求值的结构性检查满足 runner.Scorer。
type nullScorer struct{}

func (nullScorer) Score(x []float64, meta runner.Meta) objective.EvalResult {
	return objective.EvalResult{Design: x}
}

// ---------------------------------------------------------------------------
// design —— 内部设计判决层
// ---------------------------------------------------------------------------

// designVerdict 是一条门在表格里的判定文字。
func designVerdict(g design.Gate) string {
	switch {
	case g.Unknown:
		return "unknown"
	case g.Pass:
		return "PASS"
	default:
		return "FAIL"
	}
}

// fmtNum 让表格里的数字可读且够精确; 非有限值不打印成 "NaN" 了事。
func fmtNum(v float64) string {
	switch {
	case math.IsNaN(v):
		return "NaN"
	case math.IsInf(v, 1):
		return "+Inf"
	case math.IsInf(v, -1):
		return "-Inf"
	}
	return strconv.FormatFloat(v, 'g', 10, 64)
}

// boxCentreDesign 是 spec 派生的确定性参考设计: 每一维取搜索盒的中点, 再走一遍
// vectorToCoils/coilsToVector 的规范形式。定义与 internal/search 的 fallback 一致
// (mirror 的比例只对 4 个线圈有定义, 那里非 4 线圈时同样退到盒子中心)。
//
// 它不是"某台装置", 只是一份确定性的、由 spec 完全决定的线圈组, 好让 --design spec
// 这条路径可复现、可回归。
func boxCentreDesign(spec config.Spec) []float64 {
	lo, hi := spec.Lower(), spec.Upper()
	x := make([]float64, len(lo))
	for i := range x {
		x[i] = 0.5 * (lo[i] + hi[i])
	}
	coils, err := physics.VectorToCoils(x, spec)
	if err != nil {
		return x
	}
	return physics.CoilsToVector(coils)
}

// windowVerdict 只报契约 §5 里那两条「有没有解」的门 —— 装置设计的死活问题。
// 单独拎出来是因为它和总判决不是同一件事: 总判决还要看导体场与可造性。
func windowVerdict(s design.Scope) string {
	keys := []string{design.GateDeath, design.GateMuWindow}
	for _, k := range keys {
		g, ok := s.GateByKey(k)
		if !ok {
			return "unknown（门缺失）"
		}
		if g.Unknown {
			return "unknown（读不到场源）"
		}
		if !g.Pass {
			return "FAIL —— μ 窗口关闭, 无解 (不是「更难」)"
		}
	}
	return "PASS —— 该场源在此尺度上有解"
}

func cmdDesign(args []string) int {
	fs := newFlagSet("design",
		"design [--a A] [--source KEY] [--eta E] [--mu0 MU] [--design baseline|spec] [--json]",
		"Run the internal design gate layer (docs/design-layer.md) on one coil design:\n"+
			"the six gates of the upstream ProjectionPhysics design space (field_min / death /\n"+
			"mu_window / coil_load / buildable / steps) plus FC5 reported as unknown. Prints the\n"+
			"gate table (key / Chinese name / formula / computed value / threshold / verdict /\n"+
			"provenance), the overall verdict and the source-material table.\n\n"+
			"Everything upstream-shaped comes from internal/design, whose numbers are anchored to\n"+
			"the upstream artifact files by testdata/projectionphysics_anchors.json (never copied\n"+
			"into Go source by hand).\n\n"+
			"Exit status 1 means at least one decisive gate failed — that is a verdict, not an error.")
	a := fs.Float64("a", design.ARef,
		"constraint-region scale a in metres (upstream A_REF); B_death ∝ 1/a")
	source := fs.String("source", design.SourceMATBGN2Par,
		"field-source material key (see the source table below)")
	eta := fs.Float64("eta", 0.05, "gain of the mu state equation, eta ∈ [0,2)")
	mu0 := fs.Float64("mu0", 0.0, "initial mu ∈ [0,1)")
	which := fs.String("design", "baseline",
		"which coil design to gate: baseline (internal/baseline human design) | spec (box centre)")
	asJSON := fs.Bool("json", false, "emit input, design, metrics, gates and the source table as JSON")
	if code := parseFlags(fs, args); code >= 0 {
		return code
	}

	if !(*a > 0) || math.IsInf(*a, 0) || math.IsNaN(*a) {
		return fail("--a must be a positive finite length in metres, got %v", *a)
	}
	if !(*eta >= 0 && *eta < 2) {
		return fail("--eta must be in [0,2) (the state equation is a model choice, not a law), got %v", *eta)
	}
	if !(*mu0 >= 0 && *mu0 < 1) {
		return fail("--mu0 must be in [0,1) (mu = 1 is not reachable in finite steps, TD8), got %v", *mu0)
	}
	src, ok := design.LookupSource(*source)
	if !ok {
		keys := make([]string, 0, len(design.SourceTable()))
		for _, s := range design.SourceTable() {
			keys = append(keys, s.Key)
		}
		return fail("unknown --source %q (known: %s)", *source, strings.Join(keys, ", "))
	}
	if *which != "baseline" && *which != "spec" {
		return fail("--design must be baseline or spec, got %q", *which)
	}

	spec := defaultSpec()
	base, err := baselineDesign()
	if err != nil {
		return fail("%v", err)
	}
	ev := analyticEvaluator(spec, base.Cost)

	designName := base.Name
	dv := base.Design
	if *which == "spec" {
		designName = "spec_box_centre (every dimension at the midpoint of the search box)"
		dv = boxCentreDesign(spec)
	}
	res := ev.Evaluate(dv)

	in := design.Input{DeviceScaleM: *a, SourceKey: *source, Eta: *eta, Mu0: *mu0}
	scope := design.Review(spec, res.Metrics, in)

	if *asJSON {
		payload := map[string]any{
			"design":        designName,
			"design_vector": dv,
			"score":         res.Score,
			"feasible":      res.Feasible,
			"metrics":       metricMap(res.Metrics),
			"input":         in,
			"scope":         scope,
			"sources":       design.SourceTable(),
		}
		b, merr := json.MarshalIndent(payload, "", "  ")
		if merr != nil {
			return fail("marshal design scope: %v", merr)
		}
		os.Stdout.Write(append(b, '\n'))
		if !scope.AllDecisivePass {
			return 1
		}
		return 0
	}

	note("forge design — 内部设计判决层 (docs/design-layer.md; 上游 ProjectionPhysics)")
	note("设计: %s", designName)
	note("  score=%v feasible=%v coils=%d", res.Score, res.Feasible, res.Metrics.NCoils)
	note("输入 (不是设计变量): a=%.6g m  场源=%s (B_cap=%s T)  η=%v  μ₀=%v",
		in.DeviceScaleM, src.Key, fmtNum(src.BCapT), in.Eta, in.Mu0)
	note("上游闭式解: TAU0(a)=%s s  B_min(N_DESIGN)=%s T  B_death(a)=%s T",
		fmtNum(scope.Tau0S), fmtNum(scope.BMinT), fmtNum(scope.BDeathT))
	note("            n_max(B_cap)=%s m⁻³  n_op(B_cap)=%s m⁻³  χ_μ(B_cap,a)=%s",
		fmtNum(scope.NMaxCap), fmtNum(scope.NOpCap), fmtNum(scope.ChiMuCap))
	note("            P_rel=%s  V_rel=%s (相对 B_ref=%s T 满 β 基准)  μ 天花板 1−FLOOR=%s",
		fmtNum(scope.PRelCap), fmtNum(scope.VRelCap), fmtNum(design.BRefPower), fmtNum(scope.MuCeiling))
	note("")

	note("六道门 (契约 §5) + FC5 单列 unknown:")
	note("  %-11s %-9s %-14s %-16s %-14s %-8s %s",
		"key", "中文名", "实算值", "阈值", "判定", "出处", "公式")
	for _, g := range scope.Gates {
		note("  %-11s %-9s %-14s %-16s %-8s %-8s %s",
			g.Key, g.Label, fmtNum(g.Value), fmtNum(g.Ref), designVerdict(g), g.Provenance, g.Formula)
	}
	note("")

	verdict := "FAIL"
	if scope.AllDecisivePass {
		verdict = "PASS"
	}
	note("总判决: %s (计入总判决的是五条门 %s; steps 报值不判生死, fc5_locked 一律 unknown)",
		verdict, strings.Join(design.DecisiveGateKeys, "/"))
	note("  通过 %d, 不过 %d, unknown %d (共 %d 条)", scope.Passed, scope.Failed, scope.Unknown, len(scope.Gates))
	note("  窗口判决 (death + mu_window, 这两条才是「这个场源在 a 下有没有解」): %s",
		windowVerdict(scope))
	note("  χ_μ>1 ⟹ 可行; χ_μ<1 ⟹ μ 窗口关闭 = **无解**, 不是「更难」(契约 §5)")
	if scope.SourceKnown {
		note("  关闭步: 解析 n_close=%s 步; 整数首次关闭=%d 步 (永不关闭 = -1) —— 整数语义 = ceil(解析值); 上游 N15 字段与解析复算的数值见 testdata/projectionphysics_anchors.json",
			fmtNum(scope.CloseStepAnalytic), scope.CloseStepInt)
	}
	for _, n := range scope.Notes {
		note("  note: %s", n)
	}
	note("")

	note("判决量上下文 (定义即口径; 本层只报「怎么测」, 不做预测):")
	note("  D1 R_ci(μ₀)=%s   锁定因子 1/√(1−μ₀)=%s   μ_min(δ=1e-4)=%s",
		fmtNum(scope.RciMu0), fmtNum(scope.LockingFactor), fmtNum(design.MuMinFromDelta(1e-4)))
	note("  D2 Λ = τ_E 增益 / S* 惩罚: unknown —— FC5 预言 ≡1, 几何捕获 Λ>1 未证 (不进判决)")
	note("  D3 μ ∝ P^k, 跨 4 个数量级所需功率倍数: k=1 → %s | k=0.5 → %s | k=0.25 → %s | k=0.1 → %s",
		fmtNum(design.PowerMultiple(1, 4)), fmtNum(design.PowerMultiple(0.5, 4)),
		fmtNum(design.PowerMultiple(0.25, 4)), fmtNum(design.PowerMultiple(0.1, 4)))
	note("")

	note("场源材料类对照表 (a=%s m; 判决口径与上面三条门一致):", fmtNum(in.DeviceScaleM))
	note("  %-15s %-30s %-8s %-12s %-12s %-12s %-10s %s",
		"key", "中文名", "B_cap/T", "n_op/m⁻³", "P_rel", "V_rel", "χ_μ", "判决")
	for _, s := range design.SourceTable() {
		chi := design.ChiMu(s.BCapT, in.DeviceScaleM)
		v := "可用"
		if !(s.BCapT >= scope.BDeathT) {
			v = "无解（μ 窗口关闭）"
		} else if !(chi > 1) {
			v = "窗口关闭"
		}
		note("  %-15s %-30s %-8s %-12s %-12s %-12s %-10s %s",
			s.Key, s.Label, fmtNum(s.BCapT), fmtNum(design.NOp(s.BCapT)),
			fmtNum(design.PRel(s.BCapT, design.BRefPower)),
			fmtNum(design.VRel(s.BCapT, design.BRefPower)), fmtNum(chi), v)
	}
	note("")
	note("诚实边界 (契约 §6): 本层不算 μ（μ 的主动产生 = 第二输入缺口）；Forge 的场模型仍是")
	note("真空圆环丝电流的精确静磁学（无 plasma/无 β 修正/无平衡），本层只是把它喂进上游的门；")
	note("不算排程与预算；fc5_locked 一律 unknown；本层是模型选择 + 上游已证条目的代入，真但平凡。")

	if !scope.AllDecisivePass {
		return 1
	}
	return 0
}

// ---------------------------------------------------------------------------
// xcheck —— 供 Python oracle 独立重算的导出
// ---------------------------------------------------------------------------

type xcheckSample struct {
	// 字段顺序与 testdata/golden_field_samples.json 一致 (按字母序)。
	BMag       []float64 `json:"b_mag"`
	Br         []float64 `json:"br"`
	Bz         []float64 `json:"bz"`
	Design     []float64 `json:"design"`
	DesignName string    `json:"design_name"`
	PointsR    []float64 `json:"points_r"`
	PointsZ    []float64 `json:"points_z"`
	Solver     string    `json:"solver"`
}

type xcheckFile struct {
	Samples []xcheckSample `json:"samples"`
}

// xcheckPoints 复刻 golden 采样布局: 轴上的点 (闭式解的 r = 0 分支)、近轴点、
// 分层的内部点, 以及远在线圈盒之外的远场探针。
func xcheckPoints() (r, z []float64) {
	r = []float64{0.0, 0.0, 0.0, 0.0, 0.02, 0.05, 0.10, 0.15}
	z = []float64{0.0, 0.25, 0.75, 1.35, -0.05, 0.0, 0.05, 0.10}
	// 20 个确定的内部点
	state := uint64(0x5EEDF00D)
	next := func() float64 {
		state ^= state << 13
		state ^= state >> 7
		state ^= state << 17
		return float64(state%1_000_000) / 1_000_000
	}
	for i := 0; i < 20; i++ {
		r = append(r, 1.25*next())
		z = append(z, -1.5+3.0*next())
	}
	// 远场探针
	r = append(r, 0.79, 0.81, 1.19, 1.21)
	z = append(z, 0.0, 0.3, -0.6, 0.9)
	return r, z
}

// designInBox 报告一个 design 向量是否一开始就在搜索盒内 (盒外的 design 会被
// VectorToCoils 裁剪, 即被当作另一台机器来求值)。
func designInBox(design, lo, hi []float64) bool {
	for i := range design {
		if design[i] < lo[i] || design[i] > hi[i] {
			return false
		}
	}
	return true
}

// probe 是一个 design 加上它被求值所在的采样点。
type probe struct {
	name     string
	design   []float64
	ptsR     []float64
	ptsZ     []float64
	inBox    bool // design 是否原样进来 (相对于被裁剪到盒内)
	fromGold bool
}

func cmdXcheck(args []string) int {
	fs := newFlagSet("xcheck", "xcheck [--out FILE] [--set probes|golden] [--designs N] [--points]",
		"Export Br, Bz and |B| for several designs × sample points in exactly the JSON\n"+
			"schema of testdata/golden_field_samples.json, so that python/aux/oracle.py can\n"+
			"recompute them independently (acceptance gate G5 input).\n\n"+
			"--set probes (default): the frozen golden baseline design, a deterministic in-box\n"+
			"  perturbation, and the box centre, all on the same 32-point layout.\n"+
			"--set golden: the exact designs AND points of testdata/golden_field_samples.json,\n"+
			"  which the oracle already has — so Go, the oracle and the frozen file can be put\n"+
			"  side by side on identical inputs.")
	out := fs.String("out", defXcheckOut, "output JSON file")
	set := fs.String("set", "probes", "which designs to export: probes | golden")
	nDesigns := fs.Int("designs", 3, "number of probe designs (--set probes only), 1..3")
	dumpPoints := fs.Bool("points", false, "also print the sample point list")
	if code := parseFlags(fs, args); code >= 0 {
		return code
	}

	spec := defaultSpec()
	lo, hi := spec.Lower(), spec.Upper()
	var probes []probe

	switch *set {
	case "probes":
		goldPath, err := findFile(goldenBaseline)
		if err != nil {
			return fail("%v", err)
		}
		var gold goldenBaselineFile
		if err := readJSON(goldPath, &gold); err != nil {
			return fail("read %s: %v", goldPath, err)
		}
		if len(gold.Design) != spec.NParams() {
			return fail("golden design has %d parameters, want %d", len(gold.Design), spec.NParams())
		}
		n := *nDesigns
		if n < 1 || n > 3 {
			return fail("--designs must be 1..3, got %d", n)
		}
		ptsR, ptsZ := xcheckPoints()
		add := func(name string, design []float64) {
			inBox := true
			for i := range design {
				if design[i] < lo[i] {
					design[i], inBox = lo[i], false
				}
				if design[i] > hi[i] {
					design[i], inBox = hi[i], false
				}
			}
			probes = append(probes, probe{name: name, design: design, ptsR: ptsR, ptsZ: ptsZ, inBox: inBox})
		}

		add("golden:"+gold.Name, append([]float64(nil), gold.Design...))
		if n >= 2 {
			pert := append([]float64(nil), gold.Design...)
			k := spec.NCoils
			for i := 0; i < k; i++ { // 半径 +5%
				pert[i] *= 1.05
			}
			for i := 0; i < k; i++ { // z 压缩 5%
				pert[k+i] *= 0.95
			}
			for i := 0; i < k; i++ { // 电流 +8%
				pert[2*k+i] *= 1.08
			}
			add("golden_perturbed_r+5%_z-5%_I+8%", pert)
		}
		if n >= 3 {
			centre := make([]float64, spec.NParams())
			for i := range centre {
				centre[i] = 0.5 * (lo[i] + hi[i])
			}
			add("box_centre", centre)
		}
	case "golden":
		goldPath, err := findFile(goldenSamples)
		if err != nil {
			return fail("%v", err)
		}
		var gold goldenSamplesFile
		if err := readJSON(goldPath, &gold); err != nil {
			return fail("read %s: %v", goldPath, err)
		}
		if len(gold.Samples) == 0 {
			return fail("%s contains no samples", goldPath)
		}
		for _, s := range gold.Samples {
			if len(s.Design) != spec.NParams() {
				return fail("golden sample %q has %d parameters, want %d",
					s.DesignName, len(s.Design), spec.NParams())
			}
			if len(s.PointsR) != len(s.PointsZ) {
				return fail("golden sample %q: %d r-points vs %d z-points",
					s.DesignName, len(s.PointsR), len(s.PointsZ))
			}
			probes = append(probes, probe{
				name: s.DesignName, design: append([]float64(nil), s.Design...),
				ptsR: s.PointsR, ptsZ: s.PointsZ,
				inBox: designInBox(s.Design, lo, hi), fromGold: true,
			})
		}
	default:
		return fail("--set must be probes or golden, got %q", *set)
	}

	solver := physics.AnalyticSolver{}
	samples := make([]xcheckSample, 0, len(probes))
	totalPoints := 0
	for i, p := range probes {
		coils, err := physics.VectorToCoils(p.design, spec)
		if err != nil {
			return fail("design %d (%s): VectorToCoils: %v", i+1, p.name, err)
		}
		br, bz := physics.CoilsetField(coils, p.ptsR, p.ptsZ)
		mag := solver.Magnitude(coils, p.ptsR, p.ptsZ)
		if len(br) != len(p.ptsR) || len(bz) != len(p.ptsR) || len(mag) != len(p.ptsR) {
			return fail("design %d: solver returned %d/%d/%d values for %d points",
				i+1, len(br), len(bz), len(mag), len(p.ptsR))
		}
		samples = append(samples, xcheckSample{
			Design: p.design, DesignName: p.name, Solver: solverName,
			PointsR: p.ptsR, PointsZ: p.ptsZ, Br: br, Bz: bz, BMag: mag,
		})
		totalPoints += len(p.ptsR)
	}

	if err := writeJSON(*out, xcheckFile{Samples: samples}); err != nil {
		return fail("write %s: %v", *out, err)
	}
	note("xcheck: set=%s, wrote %d designs (%d point evaluations) to %s",
		*set, len(samples), totalPoints, *out)
	note("  schema: %s", "identical key set to testdata/golden_field_samples.json")
	note("  %-4s %-40s %-8s %-16s %-16s %s", "#", "design", "points", "max|B|", "min|B|", "note")
	for i, d := range samples {
		mn, mx := math.Inf(1), math.Inf(-1)
		for _, v := range d.BMag {
			mn = math.Min(mn, v)
			mx = math.Max(mx, v)
		}
		tag := ""
		switch {
		case !probes[i].inBox:
			// VectorToCoils 会裁剪到盒内: 要说出来, 因为那时导出的 design 并不是写下来的
			// 那个 design。
			tag = "OUT OF BOX — evaluated as the clipped design"
		case probes[i].fromGold:
			tag = "golden design + golden points (direct 3-way comparison)"
		}
		note("  %-4d %-40s %-8d %-16g %-16g %s", i+1, d.DesignName, len(d.PointsR), mx, mn, tag)
	}
	note("  solver field: %q", solverName)
	if *dumpPoints {
		note("  points of design 1 (%d):", len(samples[0].PointsR))
		for i := range samples[0].PointsR {
			note("    r=%-12v z=%-12v", samples[0].PointsR[i], samples[0].PointsZ[i])
		}
	}
	note("  the Python oracle recomputes Br/Bz/|B| for these designs and points from scratch (G5).")
	return 0
}

// ---------------------------------------------------------------------------
// run / benchmark —— 单次运行与基准测试
// ---------------------------------------------------------------------------

func cmdRun(args []string) int {
	fs := newFlagSet("run", "run --method M --seed S --budget N [--registry PATH] [--tag T] [--workers N] [--out FILE]",
		"Run one search algorithm through the registry at an equal budget and print the\n"+
			"result: best score, best design, terms and convergence. Reproducibility gate G7:\n"+
			"the same method/seed/budget must print an identical best_score.")
	method := fs.String("method", "evolution", "search algorithm: random | lhs | evolution | evolution_warm")
	seed := fs.Int("seed", 0, "random seed")
	budget := fs.Int("budget", 1000, "evaluation budget")
	regPath := fs.String("registry", defRunRegistry, "registry JSONL to append to")
	tag := fs.String("tag", defRunTag, "run tag written into every record")
	workers := fs.Int("workers", 1, "evaluation workers (>1 evaluates a generation concurrently)")
	out := fs.String("out", "", "optional path to write the search.Result as JSON")
	if code := parseFlags(fs, args); code >= 0 {
		return code
	}
	if *budget <= 0 {
		return fail("--budget must be positive, got %d", *budget)
	}
	spec := defaultSpec()

	base, err := baselineDesign()
	if err != nil {
		return fail("%v", err)
	}
	ev := analyticEvaluator(spec, base.Cost)
	baseScore := ev.Evaluate(base.Design).Score

	reg, err := openRegistry(*regPath)
	if err != nil {
		return fail("%v", err)
	}

	opt := runOptions(spec, *method, *seed, *budget, *workers, baseScore, base.Design)

	rn := runner.New(reg, ev, *tag)
	before := reg.Len()
	start := time.Now()
	res, err := search.Run(*method, rn, opt)
	if err != nil {
		return fail("search.Run(%s): %v", *method, err)
	}
	elapsed := time.Since(start)

	note("run: method=%s seed=%d budget=%d workers=%d", res.Algorithm, res.Seed, res.Budget, *workers)
	note("registry: %s (%d records before, %d after this run)", *regPath, before, reg.Len())
	note("human baseline score: %v", baseScore)
	note("elapsed: %.2fs", elapsed.Seconds())
	note("evals: %d (budget %d)", res.NEvals, *budget)
	note("best_design_id=%s", res.BestDesignID)
	note("best_feasible=%v", res.BestFeasible)
	note("evals_to_beat=%d", res.EvalsToBeat)
	// 机器可读的那一行: 同一个 seed 跑两次 'forge run' 必须打印同一个字符串 (G7)。
	note("best_score=%v", res.BestScore)
	note("delta_vs_human=%v", res.BestScore-baseScore)
	if len(res.BestTerms) > 0 {
		note("best_terms:")
		for _, k := range sortedKeys(res.BestTerms) {
			note("  %-24s %v", k, res.BestTerms[k])
		}
	}
	printDesign(res.BestDesign)

	if *out != "" {
		if err := writeJSON(*out, res); err != nil {
			return fail("write %s: %v", *out, err)
		}
		note("result JSON: %s", *out)
	}
	return 0
}

// runOptions 为一次 run 构建搜索选项。
//
// 人类 baseline 的 design 只在 evolution_warm 时作为 WarmStart 注入。同时跑
// evolution 与 evolution_warm 的原因, 是要衡量继承来的 design 知识买到了什么; 如果
// 连冷变体也注入 baseline, 就会悄悄删掉这个对比, 让两种方法变成同一个 (stage E 的
// harness 采用同样的规则)。
func runOptions(spec config.Spec, method string, seed, budget, workers int,
	baselineScore float64, warmDesign []float64) search.Options {
	opt := search.DefaultOptions(spec)
	opt.Algorithm = method
	opt.Seed = seed
	opt.Budget = budget
	opt.Workers = workers
	opt.BaselineScore = baselineScore
	if method == search.AlgorithmEvolutionWarm {
		opt.WarmStart = append([]float64(nil), warmDesign...)
	}
	return opt
}

func cmdBenchmark(args []string) int {
	fs := newFlagSet("benchmark", "benchmark [--budget N] [--seeds 0,1,2] [--methods random,lhs,evolution,evolution_warm] [--out DIR] [--tag T] [--workers N]",
		"Run every method at every seed with an identical budget, aggregate across seeds\n"+
			"(never a single-seed claim), probe generalisation under perturbed requirements\n"+
			"and write runs/<tag>/results.json plus the registry it came from.")
	budget := fs.Int("budget", 1000, "evaluation budget per (method, seed)")
	seeds := fs.String("seeds", "0,1,2", "comma-separated seeds")
	methods := fs.String("methods", strings.Join(allMethods, ","), "comma-separated methods")
	out := fs.String("out", defRunDir, "run directory: registry.jsonl and results.json land here")
	tag := fs.String("tag", "", "run tag (default: the base name of --out)")
	workers := fs.Int("workers", 1, "evaluation workers")
	knowledgeDir := fs.String("knowledge", "", "registry dir to seed evolution_knowledge from (reads feasible top-K designs by score)")
	ruleFile := fs.String("rule", "", "design_rules.md to seed evolution_rule / evolution_champion_rule from")
	target := fs.Float64("target", 0.0, "target score for Evals-to-Target report (0 = disabled)")
	if code := parseFlags(fs, args); code >= 0 {
		return code
	}
	if *budget <= 0 {
		return fail("--budget must be positive, got %d", *budget)
	}
	seedList, err := splitInts(*seeds)
	if err != nil {
		return fail("--seeds: %v", err)
	}
	if len(seedList) == 0 {
		return fail("--seeds must list at least one seed")
	}
	methodList := splitList(*methods)
	if len(methodList) == 0 {
		return fail("--methods must list at least one method")
	}
	for _, m := range methodList {
		if _, ok := search.Methods[m]; !ok {
			return fail("unknown method %q (known: %s)", m, strings.Join(searchMethodNames(), ", "))
		}
	}
	runTag := *tag
	if runTag == "" {
		runTag = filepath.Base(*out)
		if runTag == "." || runTag == string(filepath.Separator) || runTag == "" {
			runTag = defTag
		}
	}

	spec := defaultSpec()
	base, err := baselineDesign()
	if err != nil {
		return fail("%v", err)
	}
	ev := analyticEvaluator(spec, base.Cost)
	baseScore := ev.Evaluate(base.Design).Score

	regPath := filepath.Join(*out, "registry.jsonl")
	reg, err := openRegistry(regPath)
	if err != nil {
		return fail("%v", err)
	}

	note("benchmark: tag=%s budget=%d seeds=%v methods=%v workers=%d", runTag, *budget, seedList, methodList, *workers)
	note("human baseline score: %v", baseScore)
	note("registry: %s", regPath)
	// 知识复用(evolution_knowledge): 从 --knowledge 指定的 registry 读 feasible 且
	// score 最高的前 K 个设计作为整代播种。K 取 mu(默认 16) —— 铺满首代。
	var knowledgeWarm, ruleWarm, champRuleWarm [][]float64
	if *knowledgeDir != "" {
		kpath := filepath.Join(*knowledgeDir, "registry.jsonl")
		kreg, err := openRegistry(kpath)
		if err != nil {
			return fail("--knowledge: %v", err)
		}
		know, err := topDesigns(kreg, spec, 16)
		if err != nil {
			return fail("--knowledge: %v", err)
		}
		if len(know) == 0 {
			return fail("--knowledge: no feasible designs in %s", kpath)
		}
		knowledgeWarm = know
		note("--knowledge: seeding %d designs from %s", len(know), kpath)
	}
	// 规则引导(evolution_rule / evolution_champion_rule): 从 --rule 指定的
	// design_rules.md 读规则, 生成规则偏置子空间的初始种群。
	if *ruleFile != "" {
		rules, err := readRules(*ruleFile)
		if err != nil {
			return fail("--rule: %v", err)
		}
		if len(rules) == 0 {
			return fail("--rule: no usable rules in %s (need known term sign)", *ruleFile)
		}
		rng := rand.New(rand.NewSource(0))
		ruleWarm = ruleSeededDesigns(spec, rules, 16, rng)
		champRuleWarm = append(append([][]float64(nil), knowledgeWarm...), ruleWarm...)
		note("--rule: %d rules, seeding %d rule designs (champion_rule gets %d total)",
			len(rules), len(ruleWarm), len(champRuleWarm))
	}
	start := time.Now()
	rep, err := experiment.RunBenchmark(reg, spec, experiment.Opts{
		Budget:           *budget,
		Seeds:            seedList,
		Methods:          methodList,
		Tag:              runTag,
		Workers:          *workers,
		Baseline:         &base,
		KnowledgeWarm:    knowledgeWarm,
		RuleWarm:         ruleWarm,
		ChampionRuleWarm: champRuleWarm,
		Progress: func(method string, seed int, res search.Result, seconds float64) {
			note("  %-16s seed=%-4d evals=%-6d best_score=%-24v %.1fs",
				method, seed, res.NEvals, res.BestScore, seconds)
		},
	})
	if err != nil {
		return fail("RunBenchmark: %v", err)
	}

	resultsPath := filepath.Join(*out, "results.json")
	if err := experiment.WriteJSON(rep, resultsPath); err != nil {
		return fail("write %s: %v", resultsPath, err)
	}

	note("")
	note("%-16s %-6s %-24s %-12s %-8s %-8s %-8s %-16s", "method", "seeds", "best_mean", "best_std", "n_beat", "frac", "to_beat", "budget")
	for _, m := range sortedKeys(rep.Aggregate) {
		a := rep.Aggregate[m]
		note("%-16s %-6d %-24v %-12g %-8d %-8.3f %-8.0f %-16d",
			m, a.NSeeds, a.BestMean, a.BestStd, a.NBeatingBaseline, a.FracBeatingBaseline,
			a.EvalsToBeatMean, a.Budget)
	}
	note("")
	note("baseline: %s score=%v feasible=%v design_id=%s",
		rep.Baseline.Name, rep.Baseline.Score, rep.Baseline.Feasible, rep.Baseline.DesignID)
	if *target > 0 {
		// Evals-to-Target: 每个 (method,seed) 的 best-so-far 首次达到 target 的评估数, 跨 seed 平均。
		note("")
		note("Evals-to-Target(%.3f): 首次达到 target 的评估数, 跨 seed 平均(- = 未达)", *target)
		note("%-16s %-12s", "method", "evals_to_target_mean")
		for _, m := range sortedKeys(rep.Aggregate) {
			evs := []float64{}
			for _, s := range seedList {
				h := rep.History[fmt.Sprintf("%s/seed=%d", m, s)]
				for i, v := range h {
					if v >= *target {
						evs = append(evs, float64(i+1))
						break
					}
				}
			}
			mean := -1.0
			if len(evs) > 0 {
				sm := 0.0
				for _, e := range evs {
					sm += e
				}
				mean = sm / float64(len(evs))
			}
			note("%-16s %-12.0f", m, mean)
		}
	}
	if rep.Best != nil {
		note("best: %s score=%v algorithm=%s seed=%d feasible=%v",
			rep.Best.DesignID, rep.Best.Score, rep.Best.Algorithm, rep.Best.Seed, rep.Best.Feasible)
		note("delta_vs_human=%v", rep.Best.Score-rep.Baseline.Score)
	} else {
		note("best: (none reported)")
	}
	note("registry summary: %d records, %d feasible, best=%v (%s, %s)",
		rep.RegistrySummary.NRecords, rep.RegistrySummary.NFeasible, rep.RegistrySummary.BestScore,
		rep.RegistrySummary.BestDesignID, rep.RegistrySummary.BestAlgorithm)
	note("elapsed: %.2fs", time.Since(start).Seconds())
	note("results JSON: %s", resultsPath)
	note("next: forge rules --registry %s --out %s", regPath, defRulesOut)
	note("next: forge report --results %s --out %s", resultsPath, filepath.Join(*out, "report.md"))
	return 0
}

func searchMethodNames() []string {
	names := make([]string, 0, len(search.Methods))
	for k := range search.Methods {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// methodListOrNone 列出已注册的算法, 或者在 stage D 还没填充 search.Methods 时
// 说明这一点 (诚实的“还没有”胜过一行空白)。
func methodListOrNone() string {
	names := searchMethodNames()
	if len(names) == 0 {
		return "(none registered yet: search.Methods is empty)"
	}
	return strings.Join(names, ", ")
}

// ---------------------------------------------------------------------------
// rules / report / registry / version —— 其余子命令
// ---------------------------------------------------------------------------

func cmdRules(args []string) int {
	fs := newFlagSet("rules", "rules --registry PATH --out PATH [--min-n N] [--min-abs-rho R] [--top-k K]",
		"Mine replicated design rules from a registry: a rule survives only if the\n"+
			"Spearman sign of (parameter, score term) is identical in every run, and it is\n"+
			"reported with its decile contrast and its scope.")
	regPath := fs.String("registry", filepath.Join(defRunDir, "registry.jsonl"), "registry JSONL to mine")
	out := fs.String("out", defRulesOut, "markdown knowledge base to write")
	minN := fs.Int("min-n", 150, "minimum designs per run for a run to count")
	minRho := fs.Float64("min-abs-rho", 0.20, "minimum worst-case |rho| for a rule to be kept")
	topK := fs.Int("top-k", 12, "maximum number of rules to keep")
	tag := fs.String("tag", "", "tag recorded in the knowledge base (default: the registry's directory)")
	if code := parseFlags(fs, args); code >= 0 {
		return code
	}
	spec := defaultSpec()

	reg, err := openExistingRegistry(*regPath)
	if err != nil {
		return fail("%v", err)
	}
	recs, err := reg.Records()
	if err != nil {
		return fail("registry.Records: %v", err)
	}
	if len(recs) == 0 {
		return fail("registry %s holds no records", *regPath)
	}
	runTag := *tag
	if runTag == "" {
		runTag = filepath.Base(filepath.Dir(*regPath))
	}

	rules := knowledge.MineRules(recs, spec, knowledge.MineOpts{
		MinN: *minN, MinAbsRho: *minRho, TopK: *topK,
	})
	if err := knowledge.WriteRulesMD(rules, *out, spec, len(recs), runTag); err != nil {
		return fail("knowledge.WriteRulesMD: %v", err)
	}

	note("rules: %d mined from %d records (%s)", len(rules), len(recs), *regPath)
	note("thresholds: min_n=%d min_abs_rho=%v top_k=%d", *minN, *minRho, *topK)
	if len(rules) > 0 {
		note("%-8s %-10s %-10s %-10s %-10s %-8s %-8s", "rule", "parameter", "term", "rho", "agree", "n_design", "n_runs")
		for _, r := range rules {
			note("%-8s %-10s %-10s %-10.4f %-10.3f %-8d %-8d",
				r.RuleID, r.Parameter, r.Term, r.Rho, r.SignAgreement, r.NDesigns, r.NRuns)
		}
	} else {
		note("no rule passed the replication test — that is a result, not a failure: report it as such.")
	}
	note("knowledge base: %s", *out)
	return 0
}

func cmdReport(args []string) int {
	fs := newFlagSet("report", "report --results PATH --out PATH [--rules PATH]",
		"Render the markdown run report (Chinese, English technical terms kept) including\n"+
			"the mandatory 诚实边界 honest-limits section.")
	results := fs.String("results", filepath.Join(defRunDir, "results.json"), "experiment report JSON to render")
	out := fs.String("out", defReportOut, "markdown report to write")
	rulesPath := fs.String("rules", "", "rules markdown to reference and load (default: knowledge/design_rules.md if present)")
	if code := parseFlags(fs, args); code >= 0 {
		return code
	}

	rep, err := experiment.LoadReport(*results)
	if err != nil {
		return fail("experiment.LoadReport(%s): %v", *results, err)
	}

	path := *rulesPath
	if path == "" {
		if _, statErr := os.Stat(defRulesOut); statErr == nil {
			path = defRulesOut
		}
	}
	var rules []knowledge.Rule
	if path != "" {
		rules, err = knowledge.LoadRules(path)
		if err != nil {
			return fail("knowledge.LoadRules(%s): %v", path, err)
		}
	}

	md := report.RenderMarkdown(rep, rules, path)
	if err := report.Write(*out, md); err != nil {
		return fail("report.Write(%s): %v", *out, err)
	}
	note("report: %s (%d bytes)", *out, len(md))
	note("rules referenced: %s (%d rules)", pathOrNone(path), len(rules))
	note("baseline score: %v", rep.Baseline.Score)
	if rep.Best != nil {
		note("best score:     %v (delta %v)", rep.Best.Score, rep.Best.Score-rep.Baseline.Score)
	}
	return 0
}

func pathOrNone(p string) string {
	if p == "" {
		return "(none)"
	}
	return p
}

func cmdRegistry(args []string) int {
	fs := newFlagSet("registry", "registry [--registry PATH] [--check=true]",
		"Inspect a registry: record counts, per-algorithm counts, best design, and (with\n"+
			"--check) the integrity gate: ids sequential from 1, required fields present,\n"+
			"every parent_design referencing an existing design. Problems exit non-zero.")
	regPath := fs.String("registry", filepath.Join(defRunDir, "registry.jsonl"), "registry JSONL to inspect")
	check := fs.Bool("check", true, "run the integrity gate and exit non-zero on problems")
	if code := parseFlags(fs, args); code >= 0 {
		return code
	}

	reg, err := openExistingRegistry(*regPath)
	if err != nil {
		return fail("%v", err)
	}
	sum := reg.Summary()
	note("registry: %s", *regPath)
	note("records:  %d", sum.NRecords)
	note("feasible: %d", sum.NFeasible)
	note("best:     %v (%s, %s)", sum.BestScore, sum.BestDesignID, sum.BestAlgorithm)
	if len(sum.PerAlgo) > 0 {
		note("per algorithm:")
		for _, a := range sortedKeys(sum.PerAlgo) {
			note("  %-16s %d", a, sum.PerAlgo[a])
		}
	}
	if branches := reg.BranchImprovement(); len(branches) > 0 {
		top := branches
		if len(top) > 5 {
			top = top[:5]
		}
		note("top branch improvements:")
		note("  %-10s %-8s %-24s %-24s %-12s", "design", "children", "parent_score", "best_child", "gain")
		for _, b := range top {
			note("  %-10s %-8d %-24v %-24v %-12v", b.DesignID, b.NChildren, b.ParentScore, b.BestChildScore, b.Gain)
		}
	}
	if !*check {
		return 0
	}
	problems, err := reg.Check()
	if err != nil {
		return fail("registry.Check: %v", err)
	}
	if len(problems) > 0 {
		note("")
		note("registry --check: FAIL (%d problem(s))", len(problems))
		for _, p := range problems {
			note("  - %s", p)
		}
		return 1
	}
	note("")
	note("registry --check: PASS (ids sequential from 1, required fields present, all parents resolved)")
	return 0
}

func cmdVersion(args []string) int {
	fs := newFlagSet("version", "version", "Print version, build and frozen-schema facts.")
	if code := parseFlags(fs, args); code >= 0 {
		return code
	}
	spec := defaultSpec()
	env := rlenv.NewEnv(nullScorer{}, spec, 0, 0)
	note("forge %s", Version)
	note("  module      github.com/logos-42/hushfusion-forge")
	note("  go          %s (%s/%s)", runtime.Version(), runtime.GOOS, runtime.GOARCH)
	commit := resolveCommit()
	note("  commit      %s", commit)
	if commit == "unknown" {
		note("              (set FORGE_GIT_COMMIT or build with -ldflags \"-X main.buildCommit=<sha>\")")
	}
	note("  spec        n_coils=%d n_params=%d b_ref=%v mirror_ref=%v (internal/config DefaultSpec)",
		spec.NCoils, spec.NParams(), spec.BRef, spec.MirrorRef)
	note("  rlenv       action_dim=%d observation_dim=%d (design + %d metric slots)",
		env.ActionDim(), env.ObservationDim(), len(rlenv.ObsMetricKeys))
	note("  rlenv       learned policy: none — LoadPolicy returns ErrNoLearnedPolicy (Phase 1)")
	note("  registry    schema v0.1, %d required fields: %s",
		len(registry.RequiredFields), strings.Join(registry.RequiredFields, ", "))
	note("  methods     %s", methodListOrNone())
	note("  golden      score anchor %v, field samples vs Python oracle rel < %v", -0.2905708160753513, tolField)
	return 0
}

// ---------------------------------------------------------------------------
// world —— 世界协议 (docs/world-protocol.md, 门 G18)
// ---------------------------------------------------------------------------

// worldTag 是写进 registry 的 algorithm 标签: 一条 trace 的每条 record 都必须说得出
// 它是被谁提出来的。
const worldTag = "world_protocol"

func cmdWorld(args []string) int {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "forge world: missing subcommand (known: serve)\n\n")
		worldUsage(os.Stderr)
		return 2
	}
	switch args[0] {
	case "serve":
		return cmdWorldServe(args[1:])
	case "help", "-h", "--help":
		worldUsage(os.Stdout)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "forge world: unknown subcommand %q (known: serve)\n\n", args[0])
		worldUsage(os.Stderr)
		return 2
	}
}

func worldUsage(w *os.File) {
	fmt.Fprint(w, `usage: forge world serve [--world params|mu] [--protocol 1] [--max-steps n] [--delta-scale s] [--record FILE] [--replay FILE]

Serve a design world over the JSONL line protocol frozen in docs/world-protocol.md,
or replay a recorded trace byte for byte against a fresh world.

--world params (default) is the design-parameter world: internal/rlenv for protocol 1
and the §8 sequence semantics for protocol 2. This command adds no physics, no shaping
and no learning.

--world mu is the commutator world (protocol 3, candidate — docs/world-commutator-candidates.md):
state = (field vector, region family, mu, eta, window margin, budget, source);
actions = the declared finite set {flatten:<region>} ∪ {update_mu}; score reads mu AND
the field. Its only field sources are the same two material classes.
stdout carries protocol lines only; every human log goes to stderr.

Two protocol versions are served. Protocol 1 is the original single-step semantics
(frozen, kept for old traces and G18). Protocol 2 adds the ordering rules of the
protocol's section 8: the boundary clamp, the precondition gate and the source switch
(see docs/world-structure.md). The interactive default is 2.

Every process starts from an empty scratch registry (runs/scratch/worldtrace/), because
design_id is assigned by the registry and a leftover record would turn D0001 into D0042 —
the same request sequence must give the same response bytes (contract section 2).
The submitted evidence is the trace file, not that runs/ directory (contract section 4).

flags:
  --protocol n     protocol version to serve, 1 or 2 (default 2; an unknown version is refused)
  --max-steps n    protocol 1 episode length (default: internal/config.DefaultMaxSteps)
  --delta-scale s  normalised action scale (default: internal/config.DefaultDeltaScale)
  --source S       protocol 2 default field source (overridable per reset by regime.source)
  --budget n       protocol 2 default step budget (default: internal/config.DefaultBudget)
  --target x       protocol 2 default termination target; for --world mu the default is the world's
                   own solved score (mu.TargetScore), not internal/config.DefaultTarget
  --record FILE    append every request/response pair to FILE as a trace (one object per line)
  --replay FILE    feed the trace's requests to a fresh world and compare byte for byte

--replay ignores --protocol: a trace is replayed under the protocol its own hello
response declares, so a v1 trace keeps verifying v1 semantics.

exit codes: 0 close/EOF, 1 a fatal error (already replied ok:false), 2 usage error.
`)
}

func cmdWorldServe(args []string) int {
	fs := newFlagSet("world serve", "world serve [--world params|mu] [--protocol n] [--max-steps n] [--delta-scale s] [--source S] [--budget n] [--target x] [--record FILE] [--replay FILE]",
		"Serve the design world (internal/rlenv) over the frozen JSONL line protocol, or\n"+
			"replay a recorded trace byte for byte. stdout carries protocol lines only;\n"+
			"any human log goes to stderr.")
	worldKind := fs.String("world", world.WorldParams,
		"world to serve: params (design parameters, protocols 1/2) or mu (commutator world, protocol 3)")
	protocol := fs.Int("protocol", world.ProtocolVersion, "protocol version to serve (1 or 2)")
	maxSteps := fs.Int("max-steps", config.DefaultMaxSteps, "protocol 1 episode length (steps to truncation)")
	deltaScale := fs.Float64("delta-scale", config.DefaultDeltaScale, "normalised action scale")
	source := fs.String("source", world.DefaultSource, "protocol 2 default field source")
	budget := fs.Int("budget", config.DefaultBudget, "protocol 2 default step budget")
	target := fs.Float64("target", config.DefaultTarget, "protocol 2 default termination target")
	record := fs.String("record", "", "trace file to record (one {\"req\":..,\"resp\":..} object per line)")
	replay := fs.String("replay", "", "trace file to replay and verify byte for byte")
	if code := parseFlags(fs, args); code >= 0 {
		return code
	}
	if *record != "" && *replay != "" {
		return fail("--record and --replay cannot be combined: a replay is not a session to record")
	}
	if !world.WorldKindSupported(*worldKind) {
		return fail("--world %s is not a known world (known: %s, %s)",
			*worldKind, world.WorldParams, world.WorldMu)
	}
	explicit := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { explicit[f.Name] = true })

	// §8.1: --replay 忽略 --protocol, 以 trace 自己的 hello 响应里的 protocol 字段为准。
	// 猜错版本不是"回放失败", 而是"回放的是另一套语义" —— 那样报出来的差异一行都不可信。
	// 同一条纪律现在也管 --world: 一条协议 3 的 trace 必须在协议 3 的世界里回放。
	if *replay != "" {
		traced, err := world.TraceProtocol(*replay)
		if err != nil {
			return fail("%v", err)
		}
		if traced != *protocol {
			fmt.Fprintf(os.Stderr, "forge world: --replay overrides --protocol %d with the trace's own protocol %d\n",
				*protocol, traced)
		}
		*protocol = traced
		tracedKind := world.WorldParams
		if traced == world.ProtocolV3 {
			tracedKind = world.WorldMu
		}
		if explicit["world"] && *worldKind != tracedKind {
			return fail("--world %s contradicts the trace: its protocol %d is the %s world",
				*worldKind, traced, tracedKind)
		}
		*worldKind = tracedKind
	}

	// 世界与协议版本是一对一绑定的: 选错了不是"参数不对", 而是把两个世界混为一谈。
	// 所以这里**大声拒绝**并且不动默认值(默认世界仍是设计参数世界 v2)。
	muWorld := *worldKind == world.WorldMu
	if muWorld && explicit["protocol"] && *protocol != world.ProtocolV3 {
		return fail("--world mu speaks protocol %d only (got --protocol %d)", world.ProtocolV3, *protocol)
	}
	if !muWorld && *protocol == world.ProtocolV3 {
		return fail("protocol %d belongs to the mu world: run with --world mu (or drop --protocol)", world.ProtocolV3)
	}
	if muWorld {
		// 交换子世界不打设计分: 它没有 spec、不建注册表、不建求值器 —— 它的目标函数是
		// internal/mu 的纯代数(声明在 hello.declaration.score 里)。世界报的 score 就是
		// 那个函数的值, 这里不许再包一层 reward。
		*protocol = world.ProtocolV3
		// --target 的缺省是**世界自己的解分数**(0 = 由世界解析, 见 mu.TargetScore):
		// 参数世界的 1.0 在 μ 世界里是一次部分解, 会在两步测量走完之前终止 episode。
		muTarget := *target
		if !explicit["target"] {
			muTarget = 0
		}
		mw := world.NewMuWorld(world.Options{
			Protocol: world.ProtocolV3,
			Source:   *source,
			Budget:   *budget,
			Target:   muTarget,
			Engine:   Version,
		})
		nCells, actionDim, obsDim, _ := mw.MuConfExport()
		fmt.Fprintf(os.Stderr, "forge world serve: world=mu protocol=%d cells=%d action_dim=%d observation_dim=%d "+
			"source=%s budget=%d target=%v engine=%s\n",
			*protocol, nCells, actionDim, obsDim, mw.Source(), mw.Budget(), mw.Target(), Version)
		srv := world.NewServer(mw, *protocol, os.Stderr)
		if *replay != "" {
			return srv.Replay(*replay, os.Stderr)
		}
		if *record == "" {
			return srv.Serve(os.Stdin, os.Stdout, nil)
		}
		rec, err := world.NewRecorder(*record)
		if err != nil {
			return fail("%v", err)
		}
		code := srv.Serve(os.Stdin, os.Stdout, rec)
		if err := rec.Close(); err != nil {
			return fail("cannot close the trace %s: %v", *record, err)
		}
		fmt.Fprintf(os.Stderr, "forge world: recorded %s\n", *record)
		return code
	}

	spec := defaultSpec()
	base, err := baselineDesign()
	if err != nil {
		return fail("%v", err)
	}
	ev := analyticEvaluator(spec, base.Cost)

	// 每个 serve 进程都从**空注册表**开始。
	//
	// 契约 §2 说世界是确定性的: "同一段请求序列必须产出同一段响应序列, 逐字节相同"。
	// design_id 由注册表分配, 而注册表是只追加的 —— 如果它留着上一次会话的记录, 同一段
	// 请求就会给出 D0042 而不是 D0001, 逐字节复现立刻不成立。因此这里的注册表是**这次
	// 会话的** scratch 副本(目录落在 runs/scratch/, 已被 .gitignore 忽略), 而契约 §4
	// 也正好把"被提交的证据"定义成 trace 文件本身, 不是那次跑的 runs/ 目录。
	if err := os.Remove(defWorldRegistry); err != nil && !os.IsNotExist(err) {
		return fail("cannot start from an empty registry %s: %v", defWorldRegistry, err)
	}
	fmt.Fprintf(os.Stderr, "forge world: starting from an empty registry (%s) — design_id must not depend on leftovers\n",
		defWorldRegistry)
	reg, err := openRegistry(defWorldRegistry)
	if err != nil {
		return fail("%v", err)
	}
	rn := runner.New(reg, ev, worldTag)

	if *maxSteps <= 0 || *deltaScale <= 0 {
		fmt.Fprintf(os.Stderr, "forge world: non-positive --max-steps/--delta-scale fall back to the "+
			"internal/config defaults (%d / %v)\n", config.DefaultMaxSteps, config.DefaultDeltaScale)
	}
	if *budget <= 0 || *target != config.DefaultTarget {
		fmt.Fprintf(os.Stderr, "forge world: --budget/--target are the protocol 2 regime defaults (%d / %v)\n",
			config.DefaultBudget, config.DefaultTarget)
	}
	// 未知版本**不在启动时拒绝**: 契约 §3.5 把"refuse"定义成协议层的一行响应
	// (unsupported_protocol) + 非零退出, 所以这里仍然建一个世界, 只是让 Server 带着
	// 那个未知号 —— 它会拒掉第一行请求。那种情况下的世界语义永远不会被用到, 于是按
	// v1(参考的单步语义)建, 免得给一个不存在的版本编一套语义。
	buildProtocol := *protocol
	if !world.ProtocolSupported(buildProtocol) {
		fmt.Fprintf(os.Stderr, "forge world: protocol %d is not a known version (this build serves %d and %d); "+
			"the first request will be refused with %s\n",
			*protocol, world.ProtocolV1, world.ProtocolV2, world.CodeUnsupportedProtocol)
		buildProtocol = world.ProtocolV1
	}
	w := world.New(rn, spec, world.Options{
		Protocol:   buildProtocol,
		MaxSteps:   *maxSteps,
		DeltaScale: *deltaScale,
		Source:     *source,
		Budget:     *budget,
		Target:     *target,
		Engine:     Version,
	})
	srv := world.NewServer(w, *protocol, os.Stderr)
	fmt.Fprintf(os.Stderr, "forge world serve: protocol=%d max_steps=%d delta_scale=%v source=%s budget=%d target=%v registry=%s\n",
		*protocol, w.MaxSteps(), w.DeltaScale(), w.Source(), w.Budget(), w.Target(), defWorldRegistry)

	if *replay != "" {
		// 报告走 stderr: stdout 只放协议行。回放不下发任何协议行, 退出码才是判据。
		return srv.Replay(*replay, os.Stderr)
	}
	if *record == "" {
		return srv.Serve(os.Stdin, os.Stdout, nil)
	}

	rec, err := world.NewRecorder(*record)
	if err != nil {
		return fail("%v", err)
	}
	code := srv.Serve(os.Stdin, os.Stdout, rec)
	if err := rec.Close(); err != nil {
		return fail("cannot close the trace %s: %v", *record, err)
	}
	fmt.Fprintf(os.Stderr, "forge world: recorded %s\n", *record)
	return code
}
