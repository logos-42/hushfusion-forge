// Package app 为 Forge CLI 与内部包之间的应用层共享层。
//
// 它只放"纯应用逻辑": 文件查找、JSON 读写、registry 打开与记录构造、
// spec variant 检索、规则读取与种群播种、metric 访问器、采样点布局、
// 搜索选项组装等。这里不含 flag 解析、不含命令分发、不含 os.Exit，
// 也不对外打印（print* 留在 cmd/forge，属于 CLI 层）。
//
// 目标是让 cmd/forge/main.go 只做接线：flag → 调用 → 打印。
// internal/app 被 main 和各 internal/* 包复用，自身绝不 import main。
package app

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/logos-42/hushfusion-forge/internal/baseline"
	"github.com/logos-42/hushfusion-forge/internal/config"
	"github.com/logos-42/hushfusion-forge/internal/experiment"
	"github.com/logos-42/hushfusion-forge/internal/objective"
	"github.com/logos-42/hushfusion-forge/internal/physics"
	"github.com/logos-42/hushfusion-forge/internal/registry"
	"github.com/logos-42/hushfusion-forge/internal/search"
)

// DefaultSpec 返回冻结的默认 spec，即 config.DefaultSpec 的别名。
// 判据版本只有一个家 (internal/config.ForgeVersion)，这里不复制版本号。
func DefaultSpec() config.Spec { return config.DefaultSpec() }

// AnalyticEvaluator 构造用于 verify / 基线比较的解析求值器。
func AnalyticEvaluator(spec config.Spec, costRef float64) *objective.Evaluator {
	solver := physics.AnalyticSolver{}
	grids := physics.BuildGrids(spec)
	return objective.NewEvaluator(spec, solver, costRef, grids)
}

// BaselineDesign 解出人工基线设计 (TextbookMirror)。
func BaselineDesign(spec config.Spec) (baseline.Baseline, error) {
	return baseline.TextbookMirror(spec)
}

// FindFile 向上搜索工作目录及其祖先目录中的 rel，找到首个存在的文件并
// 返回其绝对路径；找不到则返回 rel 本身携带错误（绝不静默拿空值比较）。
func FindFile(rel string) (string, error) {
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
		if candExists(filepath.Join(dir, rel)) {
			return filepath.Join(dir, rel), nil
		}
	}
	return rel, fmt.Errorf("%s not found (searched upward from the working directory)", rel)
}

func candExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// ReadJSON 读取并反序列化 JSON 文件。
func ReadJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// WriteJSON 以缩进 JSON 写入文件，必要时创建父目录。
func WriteJSON(path string, v any) error {
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

// OpenRegistry 打开 (必要时创建) path 处的 registry。
func OpenRegistry(path string) (*registry.Registry, error) {
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

// OpenExistingRegistry 打开已存在的 registry；缺失是错误，而非空 registry。
func OpenExistingRegistry(path string) (*registry.Registry, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("registry %s does not exist (run 'forge benchmark' or 'forge run' first)", path)
	}
	return OpenRegistry(path)
}

// FindVariant 在 experiment.SpecVariants 里按名字查找一个扰动。
func FindVariant(name string) (experiment.Variant, bool) {
	for _, v := range experiment.SpecVariants {
		if v.Name == name {
			return v, true
		}
	}
	return experiment.Variant{}, false
}

// VariantNames 返回所有可用扰动的名字。
func VariantNames() string {
	names := make([]string, 0, len(experiment.SpecVariants))
	for _, v := range experiment.SpecVariants {
		names = append(names, v.Name)
	}
	return strings.Join(names, ", ")
}

// TopDesigns 从 registry 读 feasible 且 score 最高的前 K 个设计，转成搜索
// 设计向量。这是 evolution_knowledge 的整代播种来源 —— 继承上一轮学到的
// 最优，而非人工基线。
func TopDesigns(reg *registry.Registry, spec config.Spec, k int) ([][]float64, error) {
	recs, err := reg.Records()
	if err != nil {
		return nil, err
	}
	type scored struct {
		rec  registry.Record
		keys []float64
	}
	var cand []scored
	for _, r := range recs {
		if !r.Feasible {
			continue
		}
		keys := make([]float64, 0, 3*spec.NCoils)
		keys = append(keys, r.Params.RadiusM...)
		keys = append(keys, r.Params.ZM...)
		keys = append(keys, r.Params.CurrentA...)
		if len(keys) != spec.NParams() {
			continue
		}
		cand = append(cand, scored{rec: r, keys: keys})
	}
	sort.Slice(cand, func(i, j int) bool {
		return cand[i].rec.Score > cand[j].rec.Score
	})
	out := make([][]float64, 0, min(k, len(cand)))
	for i := 0; i < min(k, len(cand)); i++ {
		out = append(out, cand[i].keys)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// 规则读取与种群播种（design_rules.md → 搜索先验）
// ---------------------------------------------------------------------------

// designRule 是 design_rules.md 里一条规则(机器可读块)的最小形态。
type designRule struct {
	Parameter string
	Term      string
	Rho       float64
	Direction int
}

// ReadRules 把 design_rules.md 中 ```json ... ``` 代码块里的规则解析为
// []designRule。term 对 score 的符号权重与 objective.score 一致。
func ReadRules(path string) ([]designRule, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	start := bytesIndex(b, []byte("```json\n"))
	if start < 0 {
		return nil, fmt.Errorf("no ```json block in %s", path)
	}
	start += len("```json\n")
	end := bytesIndex(b[start:], []byte("\n```"))
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
	termSign := map[string]int{"field": +1, "mirror": +1, "volume": +1, "ripple": -1, "cost": -1}
	out := make([]designRule, 0, len(recs))
	for _, r := range recs {
		ts, ok := termSign[r.Term]
		if !ok {
			continue
		}
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

// RuleSeededDesigns 按规则方向生成 K 个初始设计: 对 direction!=0 的参数, 采样
// 偏向规则推荐的子空间; 其余参数全盒均匀采样。这是 Phase B "Rule" 组的整代
// 播种 —— 用规则引导, 而不是冠军点。
func RuleSeededDesigns(spec config.Spec, rules []designRule, k int, rng *rand.Rand) [][]float64 {
	dirBySlot := map[int]int{}
	for _, r := range rules {
		if r.Direction == 0 {
			continue
		}
		idx := ParamIndexByName(r.Parameter, spec)
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

// ParamIndexByName 把 "I_3"/"radius_0"/"z_1" 映射到 design 向量槽位。
// 设计向量布局 = [radius_0..radius_n, z_0..z_n, I_0..I_n] (见 registry.Params)。
func ParamIndexByName(name string, spec config.Spec) int {
	prefixes := []string{"radius", "z", "I"}
	lo, _ := spec.Lower(), spec.Upper()
	for pi, p := range prefixes {
		if len(name) > len(p) && name[:len(p)] == p && name[len(p)] == '_' {
			idx, err := strconv.Atoi(name[len(p)+1:])
			if err != nil {
				return -1
			}
			slot := pi*spec.NCoils + idx
			if slot >= 0 && slot < len(lo) {
				return slot
			}
		}
	}
	return -1
}

// bytesIndex 返回 needle 在 hay 中首次出现的索引，未找到返回 -1。
// 受限复刻，避免为这一处单独引入 bytes。
func bytesIndex(hay, needle []byte) int {
	n := len(needle)
	if n == 0 {
		return 0
	}
	for i := 0; i+n <= len(hay); i++ {
		match := true
		for j := 0; j < n; j++ {
			if hay[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

// ---------------------------------------------------------------------------
// metrics 访问 (冻结的 JSON tag; 这里缺一个键就是 schema 漂移 bug)
// ---------------------------------------------------------------------------

// MetricKeys 是 CLI 用来比较和打印的 metric 键，必须覆盖 physics.Metrics 的
// 全部 JSON 标签。
var MetricKeys = []string{
	"B_mid_T", "B_throat_T", "z_throat_m", "mirror_ratio", "volume_good",
	"ripple", "B_coil_max_T", "min_coil_gap_m", "min_clearance_m", "cost_proxy", "n_coils", "mu0",
}

// MetricBoolKeys 是非 float 的 metrics。
var MetricBoolKeys = []string{"coil_proximity_floor_hit"}

// MetricValue 按键名取 physics.Metrics 的原始量；bool 按 0/1 返回，以挺过
// golden 文件里的 float 编码。
func MetricValue(m physics.Metrics, key string) (float64, bool) {
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
	case "min_clearance_m":
		return m.MinClearanceM, true
	case "cost_proxy":
		return m.CostProxy, true
	case "n_coils":
		return float64(m.NCoils), true
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

// MetricMap 把 physics.Metrics 展平为 map[string]float64，供 golden 比对。
func MetricMap(m physics.Metrics) map[string]float64 {
	out := make(map[string]float64, len(MetricKeys)+len(MetricBoolKeys))
	for _, k := range MetricKeys {
		if v, ok := MetricValue(m, k); ok {
			out[k] = v
		}
	}
	for _, k := range MetricBoolKeys {
		if v, ok := MetricValue(m, k); ok {
			out[k] = v
		}
	}
	return out
}

// SortedKeys 返回 map 的有序键列表。
func SortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// RelDiff 返回 |got-want| / max(1, |want|)；对按构造为零的 golden 值，退化为
// 绝对差（与 0 做相对比较是未定义，不是严格判据）。
func RelDiff(got, want float64) float64 {
	d := math.Abs(got - want)
	scale := math.Abs(want)
	if scale < 1 {
		scale = 1
	}
	return d / scale
}

// SplitList 解析一个逗号分隔的 flag 值，去掉空白并丢弃空字段。
func SplitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// SplitInts 解析逗号分隔的整数列表（种子）。
func SplitInts(s string) ([]int, error) {
	var out []int
	for _, part := range SplitList(s) {
		v, err := strconv.Atoi(part)
		if err != nil {
			return nil, fmt.Errorf("cannot parse %q as an integer", part)
		}
		out = append(out, v)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// 设计向量工具
// ---------------------------------------------------------------------------

// DesignInBox 报告一个 design 向量是否一开始就在搜索盒内（盒外的 design 会被
// VectorToCoils 裁剪，即被当作另一台机器来求值）。
func DesignInBox(design, lo, hi []float64) bool {
	for i := range design {
		if design[i] < lo[i] || design[i] > hi[i] {
			return false
		}
	}
	return true
}

// BoxCentreDesign 是 spec 派生的确定性参考设计: 每一维取搜索盒的中点，再走
// 一遍 VectorToCoils/CoilsToVector 的规范形式。它不是"某台装置"，只是一份
// 由 spec 完全决定的线圈组，好让 --design spec 这条路径可复现、可回归。
func BoxCentreDesign(spec config.Spec) []float64 {
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

// BaselineRecord 把一次基线求值打包成 registry 可写入的 Record。
func BaselineRecord(res objective.EvalResult, base baseline.Baseline) registry.Record {
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

// ---------------------------------------------------------------------------
// xcheck 导出：供 Python oracle 独立重算的采样点
// ---------------------------------------------------------------------------

// XcheckPoints 复刻 golden 采样布局: 轴上的点 (闭式解的 r = 0 分支)、近轴点、
// 分层的内部点, 以及远在线圈盒之外的远场探针。
func XcheckPoints() (r, z []float64) {
	r = []float64{0.0, 0.0, 0.0, 0.0, 0.02, 0.05, 0.10, 0.15}
	z = []float64{0.0, 0.25, 0.75, 1.35, -0.05, 0.0, 0.05, 0.10}
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
	r = append(r, 0.79, 0.81, 1.19, 1.21)
	z = append(z, 0.0, 0.3, -0.6, 0.9)
	return r, z
}

// ---------------------------------------------------------------------------
// 搜索选项组装（一次 run 的全部参数，供 cmdRun / cmdBenchmark 复用）
// ---------------------------------------------------------------------------

// RunOptions 为一次 run 构建搜索选项。人类 baseline 的 design 只在
// evolution_warm 时作为 WarmStart 注入，避免把冷变体也变成基线 seeding。
func RunOptions(spec config.Spec, method string, seed, budget, workers int,
	baselineScore float64, warmDesign []float64, mu, lam int, sigma0 float64) search.Options {
	opt := search.DefaultOptions(spec)
	opt.Algorithm = method
	opt.Seed = seed
	opt.Budget = budget
	opt.Workers = workers
	opt.BaselineScore = baselineScore
	opt.Mu = mu
	opt.Lam = lam
	opt.Sigma0 = sigma0
	if method == search.AlgorithmEvolutionWarm {
		opt.WarmStart = append([]float64(nil), warmDesign...)
	}
	return opt
}

// AllMethods 返回已注册的搜索算法名（有序）。
func AllMethods() []string {
	names := make([]string, 0, len(search.Methods))
	for k := range search.Methods {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}
