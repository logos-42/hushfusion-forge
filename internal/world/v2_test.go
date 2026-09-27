// v2_test.go — 协议 2 的语义测试(docs/world-protocol.md §8 + docs/world-structure.md)。
//
// 这里盯的是"顺序规则真的被实现了", 而不是"代码跑起来了":
//
//	· R2 夹取: 同一对动作, 换顺序得到不同的落点 —— 世界**真的**是顺序依赖的;
//	· R3 前提门: 在夹取区里, 把设计留在区内的动作被拒(状态不变)但仍消耗预算;
//	  把设计移出区的动作被接受;
//	· R4 换源: set_source 用同一个 θ 重新求值, 观测的源 one-hot 与三个余量随之翻转;
//	· R1/R5 预算与终止: 预算是真上限, 被拒的动作也扣预算, target 真的会终止;
//	· 26 维观测的前 19 维必须与协议 1 对同一个设计给出的 19 维**逐位相同**。
package world

import (
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"strings"

	"testing"

	"github.com/logos-42/hushfusion-forge/internal/config"
	"github.com/logos-42/hushfusion-forge/internal/design"
	"github.com/logos-42/hushfusion-forge/internal/objective"
	"github.com/logos-42/hushfusion-forge/internal/physics"
	"github.com/logos-42/hushfusion-forge/internal/rlenv"
	"github.com/logos-42/hushfusion-forge/internal/runner"
)

// v2ToyScorer 是一个可以用函数控制"阈值区状态"的假求值器。
//
// 它给 v2 需要的 metric 填上**正**的假值(B_coil_max 与 cost_proxy), 因为 v2 的余量与
// 代价归一化会去反演它们 —— 一个零值 metric 会让世界大声 panic(那是故意的)。
type v2ToyScorer struct {
	nEvals  int
	inZone  func(x []float64) bool
	bCoil   float64
	cost    float64
	lastIDs []string
}

func (s *v2ToyScorer) Score(x []float64, meta runner.Meta) objective.EvalResult {
	s.nEvals++
	bc := s.bCoil
	if bc == 0 {
		bc = 3.0
	}
	if s.inZone != nil && s.inZone(x) {
		bc = 100.0 // 远超 spec.CoilFieldLimit, 于是这个设计落在 R3 的夹取区里
	}
	cost := s.cost
	if cost == 0 {
		cost = 1.0
	}
	design := copyOf(x)
	id := fmt.Sprintf("V%04d", s.nEvals)
	s.lastIDs = append(s.lastIDs, id)
	return objective.EvalResult{
		Score:    float64(s.nEvals),
		Feasible: true,
		Design:   design,
		DesignID: id,
		Metrics:  objectiveMetrics(bc, cost),
	}
}

// objectiveMetrics 造一份带正 B_coil_max / cost_proxy 的 Metrics。
//
// 它只填 v2 语义真正读的那两个槽位: 其余保持零, 于是观测里的其它 metric 槽位是 0 ——
// 对协议层的测试足够, 且不需要造假物理(真物理由 internal/physics 自己的测试盯着)。
func objectiveMetrics(bCoil, cost float64) physics.Metrics {
	var m physics.Metrics
	m.BCoilMaxT = bCoil
	m.CostProxy = cost
	return m
}

func NewV2TestWorld(t *testing.T, sc runner.Scorer, opts Options) *World {
	t.Helper()
	if opts.Protocol == 0 {
		opts.Protocol = ProtocolV2
	}
	if opts.Engine == "" {
		opts.Engine = "test-engine"
	}
	if opts.DeltaScale == 0 {
		opts.DeltaScale = config.DefaultDeltaScale
	}
	if opts.Target == 0 {
		// 假求值器的 score 是"第几次求值"——单调递增, 所以默认 target=1.0 会在第一步就
		// 终止(那是协议行为正确的表现, 但会让别的用例失去意义)。除了专门验终止的那条
		// 用例(它显式给 target), 这里把目标放到够不着的地方。
		opts.Target = 1e9
	}
	return New(sc, testSpec(), opts)
}

// v2Hello 起一个 v2 世界并返回 hello 的解析结果。
func v2Hello(t *testing.T, w *World) map[string]any {
	t.Helper()
	obj := mustJSON(t, handle1(t, w, `{"op":"hello"}`))
	if ok, _ := obj["ok"].(bool); !ok {
		t.Fatalf("hello is not ok: %v", obj)
	}
	return obj
}

// designAt 造一个所有线圈都在同一个半径/深度、电流取给定值的起点设计。
//
// 它是**盒子内**的一个普通点, 不是被挑出来的最优点: v2 的测试只关心顺序规则的形状,
// 不关心分数高低(分数由 physics/objective 自己的测试盯着)。
func designAt(radius, z, current float64) []float64 {
	spec := testSpec()
	out := make([]float64, 0, spec.NParams())
	for i := 0; i < spec.NCoils; i++ {
		out = append(out, radius)
	}
	for i := 0; i < spec.NCoils; i++ {
		out = append(out, z+float64(i)*0.6)
	}
	for i := 0; i < spec.NCoils; i++ {
		out = append(out, current)
	}
	return out
}

func resetLine(x0 []float64, regime string) string {
	line := `{"op":"reset","x0":` + floatList(x0)
	if regime != "" {
		line += `,"regime":` + regime
	}
	return line + `}`
}

// ---------------------------------------------------------------------------
// 握手: 26 维 + 一个说不清的版本必须被拒
// ---------------------------------------------------------------------------

func TestV2HelloDeclares26Dims(t *testing.T) {
	// 这里刻意**不经** NewV2TestWorld: hello 报出来的 budget/target 必须是
	// internal/config 的缺省值, 所以这个世界的 regime 缺省值不许被测试改写。
	w := New(&v2ToyScorer{}, testSpec(), Options{Protocol: ProtocolV2, Engine: "test-engine"})
	obj := v2Hello(t, w)

	if got := obj["protocol"].(float64); got != float64(ProtocolV2) {
		t.Errorf("protocol = %v, want %v", got, ProtocolV2)
	}
	actionDim := int(obj["action_dim"].(float64))
	obsDim := int(obj["observation_dim"].(float64))
	if actionDim != testSpec().NParams() {
		t.Errorf("action_dim = %d, want %d", actionDim, testSpec().NParams())
	}
	if obsDim != 26 {
		t.Errorf("observation_dim = %d, want 26 (19 v1 dims + 7 v2 dims)", obsDim)
	}

	keys, _ := obj["obs_keys_v2"].([]any)
	if len(keys) != obsDim {
		t.Fatalf("obs_keys_v2 has %d entries, observation_dim is %d — the key list must describe the vector", len(keys), obsDim)
	}
	for i, want := range V2ObsTailKeys {
		got := keys[len(keys)-len(V2ObsTailKeys)+i].(string)
		if got != want {
			t.Errorf("obs_keys_v2 tail[%d] = %q, want %q (frozen in section 8.2)", i, got, want)
		}
	}
	for i, key := range rlenv.ObsMetricKeys {
		got := keys[actionDim+i].(string)
		if got != key {
			t.Errorf("obs_keys_v2[%d] = %q, want the v1 metric key %q", actionDim+i, got, key)
		}
	}

	sources, _ := obj["sources"].([]any)
	if len(sources) != len(V2Sources) {
		t.Fatalf("sources = %v, want the %d frozen sources", sources, len(V2Sources))
	}
	for i, want := range V2Sources {
		if got := sources[i].(string); got != want {
			t.Errorf("sources[%d] = %q, want %q", i, got, want)
		}
	}
	if got := obj["budget"].(float64); got != float64(config.DefaultBudget) {
		t.Errorf("budget = %v, want config.DefaultBudget = %d", got, config.DefaultBudget)
	}
	if got := obj["target"].(float64); got != config.DefaultTarget {
		t.Errorf("target = %v, want config.DefaultTarget = %v", got, config.DefaultTarget)
	}
	zones, ok := obj["clamp_zones"].(map[string]any)
	if !ok {
		t.Fatalf("clamp_zones is missing or not an object: %v", obj["clamp_zones"])
	}
	if got := zones["coil_proximity_floor_m"].(float64); got != 0.005 {
		t.Errorf("clamp_zones.coil_proximity_floor_m = %v, want the physics floor 0.005", got)
	}
}

func obsMetricKeysForTest() []string { return rlenv.ObsMetricKeys }

// TestV1WorldRefusesProtocol2 钉住版本隔离: 一个 v1 世界不能按 v2 的语义服务
// (它给不出 26 维), 所以声明 protocol 2 的客户端必须被拒。
func TestV1WorldRefusesProtocol2(t *testing.T) {
	v1 := newTestWorld(t, &toyScorer{}, 4)
	resp := handle1(t, v1, `{"op":"hello","protocol":2}`)
	if code := errCode(t, resp); code != CodeUnsupportedProtocol {
		t.Errorf("a v1 world answered a protocol-2 hello with %q, want %q", code, CodeUnsupportedProtocol)
	}
	// v1 世界里 set_source 是 unknown_op(不是"顺手支持")。
	resp = handle1(t, v1, `{"op":"set_source","source":"MATBG_N2_par"}`)
	if code := errCode(t, resp); code != CodeUnknownOp {
		t.Errorf("a v1 world answered set_source with %q, want %q", code, CodeUnknownOp)
	}
}

// TestV2WorldRefusesProtocol1Hello 反过来: 一个 v2 世界被要求按 v1 服务时必须拒绝 ——
// 一边说 19 维一边给 26 维是最坏的一种"看起来能跑"。
func TestV2WorldRefusesProtocol1Hello(t *testing.T) {
	v2 := NewV2TestWorld(t, &v2ToyScorer{}, Options{})
	resp := handle1(t, v2, `{"op":"hello","protocol":1}`)
	if code := errCode(t, resp); code != CodeUnsupportedProtocol {
		t.Errorf("a v2 world answered a protocol-1 hello with %q, want %q", code, CodeUnsupportedProtocol)
	}
}

// ---------------------------------------------------------------------------
// 26 维观测: 前 19 维必须与协议 1 逐位相同(防两套观测漂移)
// ---------------------------------------------------------------------------

func TestV2ObsPrefixMatchesV1(t *testing.T) {
	spec := testSpec()
	x0 := designAt(spec.Bounds.Radius[0]+0.2, spec.Bounds.Z[0]+0.2, spec.Bounds.Current[0]+1e5)

	// 同一个设计分别喂给 v1 与 v2 世界: 两者的前 19 维必须逐位相同。
	// 两个世界必须吃**同一批 metric**, 否则比的是两个假求值器的差别, 不是观测布局。
	v1 := newTestWorld(t, &v2ToyScorer{bCoil: 3, cost: 1}, 4)
	obsV1 := obsOf(t, handle1(t, v1, resetLine(x0, "")))
	v2 := NewV2TestWorld(t, &v2ToyScorer{}, Options{})
	obsV2 := obsOf(t, handle1(t, v2, resetLine(x0, "")))

	if len(obsV1) != 19 {
		t.Fatalf("the v1 observation has %d dims, want 19", len(obsV1))
	}
	if len(obsV2) != 26 {
		t.Fatalf("the v2 observation has %d dims, want 26", len(obsV2))
	}
	for i := range obsV1 {
		if obsV1[i] != obsV2[i] {
			t.Errorf("obs[%d] differs between protocols: v1=%v v2=%v — the 19 shared dims must be the same layout",
				i, obsV1[i], obsV2[i])
		}
	}
}

func obsOf(t *testing.T, resp []byte) []float64 {
	t.Helper()
	obj := mustJSON(t, resp)
	if ok, _ := obj["ok"].(bool); !ok {
		t.Fatalf("response is not ok: %s", resp)
	}
	items, _ := obj["obs"].([]any)
	out := make([]float64, 0, len(items))
	for _, it := range items {
		out = append(out, it.(float64))
	}
	return out
}

// ---------------------------------------------------------------------------
// R2 夹取: 世界真的是顺序依赖的
// ---------------------------------------------------------------------------

// TestV2ClampMakesOrderMatter 用一个贴在盒子下界的设计证明 R2:
// 同一个动作对, 先推出来再推回去 vs 反过来, 落点不同。这不是"我们加了一条规则",
// 而是 rlenv 的夹取语义在 v2 里必须真的作用在**当前**参数上。
func TestV2ClampMakesOrderMatter(t *testing.T) {
	spec := testSpec()
	lo, hi := spec.Lower(), spec.Upper()
	// 起点贴在上界**一步之内**(0.9 个 span): 往上推的那一步会撞界被夹住。
	// (夹取只有在"这一步本来会越界"时才生效 —— 从下界起步推一步落在盒子内部,
	//  两个顺序当然一样, 那是还没走到夹取的门口。)
	x0 := make([]float64, spec.NParams())
	for i := range x0 {
		x0[i] = lo[i] + 0.9*(hi[i]-lo[i])
	}

	// a: 把前四个半径推到上界方向 +1; b: 把前四个半径拉回下界方向 −1。
	a := make([]float64, spec.NParams())
	b := make([]float64, spec.NParams())
	for i := 0; i < spec.NCoils; i++ {
		a[i] = 1
		b[i] = -1
	}

	// 顺序 (a,b): a 撞上界被夹到 upper, b 再往下走一步 → upper − 一步。
	first := v2Episode(t, x0, [][]float64{a, b})
	wantFirst := hi[spec.NCoils-1] - 0 + 0 // 半径组的界
	wantFirst = 0
	for i := 0; i < spec.NCoils; i++ {
		if first[i] != hi[i]-config.DefaultDeltaScale*(hi[i]-lo[i]) {
			t.Fatalf("order (a,b): radius %d landed at %v, want %v", i, first[i], hi[i]-config.DefaultDeltaScale*(hi[i]-lo[i]))
		}
	}
	_ = wantFirst
	// 顺序 (b,a): b 先往下走(没撞界), a 再往上 → 起点往上一步, 不撞界。
	second := v2Episode(t, x0, [][]float64{b, a})
	if second[0] != x0[0] {
		t.Fatalf("order (b,a): radius 0 landed at %v, want the starting value %v", second[0], x0[0])
	}
	if first[0] == second[0] {
		t.Fatalf("both orders landed at %v — the world is not order dependent, which is exactly what R2 exists to prevent", first[0])
	}
}

// v2Episode 跑一条 episode 并返回最终设计向量(从最后一次 obs 的前 12 维反归一化)。
//
// 观测是协议层唯一对外可见的状态, 所以这里从观测反推设计——正好也验了观测与状态一致。
func v2Episode(t *testing.T, x0 []float64, actions [][]float64) []float64 {
	t.Helper()
	w := NewV2TestWorld(t, &v2ToyScorer{}, Options{})
	resp := handle1(t, w, resetLine(x0, ""))
	if code := errCode(t, resp); code != "" {
		t.Fatalf("reset failed with %q", code)
	}
	for _, a := range actions {
		resp = handle1(t, w, `{"op":"step","action":`+floatList(a)+`}`)
		if code := errCode(t, resp); code != "" {
			t.Fatalf("step failed with %q", code)
		}
	}
	obs := obsOf(t, resp)
	spec := testSpec()
	lo, hi := spec.Lower(), spec.Upper()
	out := make([]float64, spec.NParams())
	for i := 0; i < spec.NParams(); i++ {
		out[i] = (obs[i]+1)/2*(hi[i]-lo[i]) + lo[i]
	}
	return out
}

// TestV2ClampedIndicesAreReported 报的是**撞到盒子**的坐标, 而不是"所有坐标"。
func TestV2ClampedIndicesAreReported(t *testing.T) {
	spec := testSpec()
	lo := spec.Lower()
	x0 := make([]float64, spec.NParams())
	for i := range x0 {
		x0[i] = lo[i]
	}
	w := NewV2TestWorld(t, &v2ToyScorer{}, Options{})
	if code := errCode(t, handle1(t, w, resetLine(x0, ""))); code != "" {
		t.Fatalf("reset failed with %q", code)
	}
	action := make([]float64, spec.NParams())
	for i := range action {
		action[i] = -1 // 全部往下界推 → 全部被夹
	}
	resp := handle1(t, w, `{"op":"step","action":`+floatList(action)+`}`)
	info := infoOf(t, resp)
	clamped, _ := info["clamped"].([]any)
	if len(clamped) != spec.NParams() {
		t.Fatalf("clamped = %v, want all %d parameters (every one was pushed past the lower bound)", clamped, spec.NParams())
	}
	// 反向: 一个把参数推向盒子内部的动作不该夹住任何东西。
	action = make([]float64, spec.NParams())
	for i := range action {
		action[i] = 1
	}
	resp = handle1(t, w, `{"op":"step","action":`+floatList(action)+`}`)
	clamped, _ = infoOf(t, resp)["clamped"].([]any)
	if len(clamped) != 0 {
		t.Errorf("clamped = %v, want none (the action moved into the box)", clamped)
	}
}

func infoOf(t *testing.T, resp []byte) map[string]any {
	t.Helper()
	obj := mustJSON(t, resp)
	info, ok := obj["info"].(map[string]any)
	if !ok {
		t.Fatalf("response has no info object: %s", resp)
	}
	return info
}

// ---------------------------------------------------------------------------
// R3 前提门
// ---------------------------------------------------------------------------

// TestV2PreconditionGateRejectsAndChargesBudget 是 R3 的核心:
// 在夹取区里, 留在区内的动作被拒(状态不变)但**仍然消耗一步预算**; 移出区的动作被接受。
func TestV2PreconditionGateRejectsAndChargesBudget(t *testing.T) {
	spec := testSpec()
	// 前 4 个半径的值决定"在不在区里": 我们让半径 < 中位数 ⟹ 在区里。
	inZone := func(x []float64) bool { return x[0] < 0.40 }
	sc := &v2ToyScorer{inZone: inZone}

	lo, hi := spec.Lower(), spec.Upper()
	x0 := designAt(0.30, spec.ZMid, spec.Bounds.Current[0]+4e5) // 在区内
	w := NewV2TestWorld(t, sc, Options{Budget: 4})
	if code := errCode(t, handle1(t, w, resetLine(x0, ""))); code != "" {
		t.Fatalf("reset failed with %q", code)
	}

	step := float64(config.DefaultDeltaScale) * (hi[0] - lo[0])

	// (1) 往"还在区内"的方向走 → 拒绝。
	stay := make([]float64, spec.NParams())
	stay[0] = -1
	// 0.30 − 0.135 = 0.165 < 0.40 → 仍在区内。
	resp := handle1(t, w, `{"op":"step","action":`+floatList(stay)+`}`)
	info := infoOf(t, resp)
	if rejected, _ := info["rejected"].(bool); !rejected {
		t.Fatalf("a step that stays inside the clamp zone was accepted: %v", info)
	}
	if got := info["budget_remaining"].(float64); got != 3 {
		t.Errorf("budget_remaining = %v, want 3 — a rejected action still spends one step (section 8.4)", got)
	}
	if got := info["step"].(float64); got != 0 {
		t.Errorf("step = %v, want 0 — a rejected action must not advance the design", got)
	}
	// 状态不变: 半径还是 0.30。
	obs := obsOf(t, resp)
	radius := (obs[0]+1)/2*(hi[0]-lo[0]) + lo[0]
	if math.Abs(radius-0.30) > 1e-12 {
		t.Errorf("after a rejected step the radius is %v, want 0.30 (unchanged)", radius)
	}
	_ = step

	// (2) 往"离开区"的方向走 → 接受(0.30 + 0.135 = 0.435 ≥ 0.40)。
	out := make([]float64, spec.NParams())
	out[0] = 1
	resp = handle1(t, w, `{"op":"step","action":`+floatList(out)+`}`)
	info = infoOf(t, resp)
	if rejected, _ := info["rejected"].(bool); rejected {
		t.Fatalf("a step that leaves the clamp zone was rejected: %v", info)
	}
	if got := info["step"].(float64); got != 1 {
		t.Errorf("step = %v, want 1 (one accepted transition)", got)
	}
	if got := info["budget_remaining"].(float64); got != 2 {
		t.Errorf("budget_remaining = %v, want 2", got)
	}
}

// TestV2BudgetTruncates 预算是真上限(§8.4 的 R5), 而且是**严格**的整数上限。
func TestV2BudgetTruncates(t *testing.T) {
	spec := testSpec()
	w := NewV2TestWorld(t, &v2ToyScorer{}, Options{Budget: 3})
	// target 调高到不可能达到: 这条用例只验预算, 终止由另一条用例单独验
	// (假求值器的 score 是"第几次求值", 所以默认 target=1.0 会在第一步就终止)。
	if code := errCode(t, handle1(t, w, resetLine(designAt(0.6, spec.ZMid, 1e6), `{"budget":3,"target":1e9}`))); code != "" {
		t.Fatalf("reset failed with %q", code)
	}
	action := make([]float64, spec.NParams()) // 全零动作: 不会被夹、也不会离开区
	for i := 0; i < 3; i++ {
		resp := handle1(t, w, `{"op":"step","action":`+floatList(action)+`}`)
		obj := mustJSON(t, resp)
		wantTrunc := i == 2
		if got := obj["truncated"].(bool); got != wantTrunc {
			t.Errorf("step %d: truncated = %v, want %v", i+1, got, wantTrunc)
		}
		if got := infoOf(t, resp)["budget_remaining"].(float64); got != float64(2-i) {
			t.Errorf("step %d: budget_remaining = %v, want %v", i+1, got, 2-i)
		}
	}
	// 预算用尽后再 step: 不再是错误, 也不再推进(§3.3 的 done 语义)。
	resp := handle1(t, w, `{"op":"step","action":`+floatList(action)+`}`)
	obj := mustJSON(t, resp)
	if got := obj["terminated"].(bool); !got {
		t.Errorf("step after the budget ran out: terminated = %v, want true", got)
	}
	if got := obj["reward"].(float64); got != 0 {
		t.Errorf("step after the budget ran out: reward = %v, want 0", got)
	}
}

// TestV2TargetTerminates 终止判据是 score ≥ target(真分数, 不是整形过的 reward)。
func TestV2TargetTerminates(t *testing.T) {
	spec := testSpec()
	sc := &v2ToyScorer{} // score = 第几次求值: reset 是 1, 第一步是 2
	w := NewV2TestWorld(t, sc, Options{})
	resp := handle1(t, w, resetLine(designAt(0.6, spec.ZMid, 1e6), `{"target":1.5}`))
	if code := errCode(t, resp); code != "" {
		t.Fatalf("reset failed: %s", code)
	}
	action := make([]float64, spec.NParams())
	resp = handle1(t, w, `{"op":"step","action":`+floatList(action)+`}`)
	obj := mustJSON(t, resp)
	if got := obj["terminated"].(bool); !got {
		t.Fatalf("a step that reached score 2 ≥ target 1.5 did not terminate: %s", resp)
	}
	if got := infoOf(t, resp)["score"].(float64); got != 2 {
		t.Errorf("info.score = %v, want 2", got)
	}
}

// ---------------------------------------------------------------------------
// R4 换源
// ---------------------------------------------------------------------------

func TestV2SetSourceRevaluesAndFlipsObservables(t *testing.T) {
	spec := testSpec()
	sc := &v2ToyScorer{}
	w := NewV2TestWorld(t, sc, Options{Budget: 8})
	resp := handle1(t, w, resetLine(designAt(0.6, spec.ZMid, 1e6), ""))
	if code := errCode(t, resp); code != "" {
		t.Fatalf("reset failed: %s", code)
	}
	info := infoOf(t, resp)
	if got := info["source"].(string); got != DefaultSource {
		t.Fatalf("reset source = %q, want the default %q", got, DefaultSource)
	}
	before := obsOf(t, resp)
	beforeID := info["design_id"].(string)
	beforeResid := residualsOf(t, info)

	// 换到另一个源。
	resp = handle1(t, w, `{"op":"set_source","source":"`+design.SourceMATBGN2Par+`"}`)
	if code := errCode(t, resp); code != "" {
		t.Fatalf("set_source failed: %s", code)
	}
	info = infoOf(t, resp)
	if got := info["source"].(string); got != design.SourceMATBGN2Par {
		t.Errorf("source after set_source = %q, want %q", got, design.SourceMATBGN2Par)
	}
	// §8.5: 用新源对同一个 θ **重新求值** ⟹ 新的 design_id, 而 score 不变。
	if got := info["design_id"].(string); got == beforeID {
		t.Errorf("design_id did not change (%s): set_source must re-evaluate the same design", got)
	}
	if got, want := info["score"].(float64), infoOf(t, resp)["score"].(float64); got != want {
		t.Errorf("score is not stable across set_source: %v vs %v", got, want)
	}
	if got := info["step"].(float64); got != 1 {
		t.Errorf("step = %v, want 1: set_source is an accepted transition and spends budget", got)
	}
	if got := info["budget_remaining"].(float64); got != 7 {
		t.Errorf("budget_remaining = %v, want 7 (set_source spends one step)", got)
	}

	after := obsOf(t, resp)
	afterResid := residualsOf(t, info)
	if len(beforeResid) != 3 || len(afterResid) != 3 {
		t.Fatalf("residuals must be a 3-vector: before=%v after=%v", beforeResid, afterResid)
	}
	if beforeResid[0] == afterResid[0] {
		t.Errorf("resid_death did not change across sources: %v", beforeResid[0])
	}
	if beforeResid[1] == afterResid[1] {
		t.Errorf("resid_mu_window did not change across sources: %v", beforeResid[1])
	}
	// 设计维与 metric 维不该因为换源而动(同一个 θ), 源 one-hot 必须翻转。
	for i := 0; i < spec.NParams()+len(rlenv.ObsMetricKeys); i++ {
		if before[i] != after[i] {
			t.Errorf("obs[%d] changed (%v → %v) although set_source keeps the same design", i, before[i], after[i])
		}
	}
	if after[26-2] != 0 || after[26-1] != 1 {
		t.Errorf("after set_source MATBG_N2_par the one-hot is perp=%v par=%v, want perp=0 par=1",
			after[26-2], after[26-1])
	}
	if after[26-2] == before[26-2] || after[26-1] == before[26-1] {
		t.Errorf("the source one-hot did not flip: before=%v,%v after=%v,%v",
			before[26-2], before[26-1], after[26-2], after[26-1])
	}
}

// TestV2ResidualsMatchDesignClosedForms 对三个余量做独立复算: 它们必须等于
// internal/design 的闭式解, 一个都不许是协议层现场编的。
func TestV2ResidualsMatchDesignClosedForms(t *testing.T) {
	spec := testSpec()
	sc := &v2ToyScorer{bCoil: 6.0}
	w := NewV2TestWorld(t, sc, Options{})
	resp := handle1(t, w, resetLine(designAt(0.6, spec.ZMid, 1e6), ""))
	info := infoOf(t, resp)
	got := residualsOf(t, info)

	src, ok := design.LookupSource(DefaultSource)
	if !ok {
		t.Fatalf("the default source is not in the design source table")
	}
	want := [3]float64{
		src.BCapT/design.BDeath(design.ARef) - 1,
		design.ChiMu(src.BCapT, design.ARef) - 1,
		math.Min(spec.CoilFieldLimit, src.BCapT)/6.0 - 1,
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("residuals[%d] = %v, want %v (design's own closed form)", i, got[i], want[i])
		}
	}
}

func residualsOf(t *testing.T, info map[string]any) []float64 {
	t.Helper()
	items, ok := info["residuals"].([]any)
	if !ok {
		t.Fatalf("info has no residuals array: %v", info)
	}
	out := make([]float64, 0, len(items))
	for _, it := range items {
		out = append(out, it.(float64))
	}
	return out
}

// TestV2SetSourceRejectsUnknownSource 合法值只有 hello 的 sources 里那两个。
func TestV2SetSourceRejectsUnknownSource(t *testing.T) {
	spec := testSpec()
	w := NewV2TestWorld(t, &v2ToyScorer{}, Options{})
	if code := errCode(t, handle1(t, w, resetLine(designAt(0.6, spec.ZMid, 1e6), ""))); code != "" {
		t.Fatalf("reset failed with %q", code)
	}
	for _, line := range []string{
		`{"op":"set_source","source":"MATBG_N2_sideways"}`,
		`{"op":"set_source"}`,
		`{"op":"set_source","source":7}`,
		`{"op":"set_source","source":"MATBG_N2_par","extra":1}`,
	} {
		if code := errCode(t, handle1(t, w, line)); code == "" {
			t.Errorf("set_source accepted %s", line)
		}
	}
}

// ---------------------------------------------------------------------------
// reset 的 regime(§8.3)
// ---------------------------------------------------------------------------

func TestV2RegimeOverridesAndValidation(t *testing.T) {
	spec := testSpec()
	x0 := designAt(0.6, spec.ZMid, 1e6)
	cases := []struct {
		name    string
		regime  string
		wantErr string
		check   func(t *testing.T, info map[string]any)
	}{
		{
			name:   "source",
			regime: `{"source":"MATBG_N2_par"}`,
			check: func(t *testing.T, info map[string]any) {
				if got := info["source"].(string); got != design.SourceMATBGN2Par {
					t.Errorf("source = %q, want the regime's %q", got, design.SourceMATBGN2Par)
				}
			},
		},
		{
			name:   "budget",
			regime: `{"budget":8}`,
			check: func(t *testing.T, info map[string]any) {
				if got := info["budget_remaining"].(float64); got != 8 {
					t.Errorf("budget_remaining = %v, want the regime's 8", got)
				}
			},
		},
		{
			name:   "target",
			regime: `{"target":2.5}`,
			check: func(t *testing.T, info map[string]any) {
				// target 不直接出现在 info 里, 但它必须被接受并生效: 第一次 step 到 score 2
				// (< 2.5) 就不该终止。
				if got := info["score"].(float64); got != 1 {
					t.Errorf("score = %v, want 1", got)
				}
			},
		},
		{
			name:    "unknown source",
			regime:  `{"source":"MATBG_N2_sideways"}`,
			wantErr: CodeBadField,
		},
		{
			name:    "zero budget",
			regime:  `{"budget":0}`,
			wantErr: CodeBadField,
		},
		{
			name:    "negative budget",
			regime:  `{"budget":-3}`,
			wantErr: CodeBadField,
		},
		{
			name:    "unknown regime field",
			regime:  `{"seeds":1}`,
			wantErr: CodeBadField,
		},
		{
			name:    "regime is not an object",
			regime:  `[1,2,3]`,
			wantErr: CodeBadField,
		},
		{
			name:    "x0 has the wrong dim",
			regime:  `{"x0":[1,2,3]}`,
			wantErr: CodeDimMismatch,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := NewV2TestWorld(t, &v2ToyScorer{}, Options{})
			resp := handle1(t, w, resetLine(x0, tc.regime))
			if tc.wantErr != "" {
				if code := errCode(t, resp); code != tc.wantErr {
					t.Fatalf("regime %s: code = %q, want %q", tc.regime, code, tc.wantErr)
				}
				return
			}
			if code := errCode(t, resp); code != "" {
				t.Fatalf("regime %s: unexpected %q", tc.regime, code)
			}
			tc.check(t, infoOf(t, resp))
		})
	}
}

// TestV2ResetNeedsAPoint: 三个都给不出来时必须大声失败, 而不是自己编一个起点。
func TestV2ResetNeedsAPoint(t *testing.T) {
	w := NewV2TestWorld(t, &v2ToyScorer{}, Options{})
	if code := errCode(t, handle1(t, w, `{"op":"reset"}`)); code != CodeBadField {
		t.Errorf("reset with nothing: code = %q, want %q", code, CodeBadField)
	}
	if code := errCode(t, handle1(t, w, `{"op":"step","action":[0,0,0,0,0,0,0,0,0,0,0,0]}`)); code != CodeNotStarted {
		t.Errorf("step before reset: code = %q, want %q", code, CodeNotStarted)
	}
	if code := errCode(t, handle1(t, w, `{"op":"set_source","source":"MATBG_N2_par"}`)); code != CodeNotStarted {
		t.Errorf("set_source before reset: code = %q, want %q", code, CodeNotStarted)
	}
}

// TestV2SeedResetIsDeterministic: seed 路径必须逐字节可复现(不能用进程全局随机源)。
func TestV2SeedResetIsDeterministic(t *testing.T) {
	first := handle1(t, NewV2TestWorld(t, &v2ToyScorer{}, Options{}), `{"op":"reset","seed":12345}`)
	second := handle1(t, NewV2TestWorld(t, &v2ToyScorer{}, Options{}), `{"op":"reset","seed":12345}`)
	if string(first) != string(second) {
		t.Errorf("two resets with the same seed gave different bytes:\n%s\n%s", first, second)
	}
	other := handle1(t, NewV2TestWorld(t, &v2ToyScorer{}, Options{}), `{"op":"reset","seed":12346}`)
	if string(first) == string(other) {
		t.Errorf("two different seeds gave identical bytes — the seed is not connected")
	}
}

// ---------------------------------------------------------------------------
// 一处文本解释的钉法: R3 的"材料天花板"到底是谁的天花板
// ---------------------------------------------------------------------------

// TestZoneReadingWithTheSourceCapWouldFreezeTheWorld 钉住本实现为什么把 R3 的第二条判据读成
// "超**线圈峰场**天花板 spec.CoilFieldLimit"(导体材料, HTS @20 K = 12 T), 而不是"超该场源的
// 材料极限 B_cap"。
//
// 理由不是偏好, 而是可核验的退化: `B_coil_max_T` 的定义里含 spec.SelfField() = 3.1416 T,
// 而两个上游真源的 B_cap 是 0.12 T / 1.6 T(docs/world-structure.md §2 点名的那两个源)。
// 于是"B_coil_max_T 超 B_cap"对**任何**设计都成立 ⟹ "只接受把设计移出该区的动作"永远无解
// ⟹ 世界被完全冻结、R3 退化成一条空规则, 而 §2 还写着 R3 是 A 的来源。
//
// 这个退化不是推断: 证据文件 testdata/world_structure_v2.json 记录了计量过程中 925 个被访问
// 过的设计, 其中 B_coil_max_T 的最小值(3.2 T)就已经远高于两个源的 B_cap, 超阈比例是 1.000/1.000。
// 所以本实现读作 spec.CoilFieldLimit; 采用 B_cap 读法会让 G19 直接落到零档(A ≡ 0)。
func TestZoneReadingWithTheSourceCapWouldFreezeTheWorld(t *testing.T) {
	spec := testSpec()
	self := spec.SelfField()
	if !(self > 0) {
		t.Fatalf("spec.SelfField() = %v, want a positive self field", self)
	}
	for _, key := range V2Sources {
		src, ok := design.LookupSource(key)
		if !ok {
			t.Fatalf("source %q is not in the design source table", key)
		}
		if !(src.BCapT < self) {
			t.Errorf("source %s: B_cap %v is not below the self field %v — the degeneracy argument "+
				"no longer holds, so the zone reading has to be re-examined", key, src.BCapT, self)
		}
	}

	// B_coil_max_T ≥ SelfField 对任何设计都成立(它按定义就是邻居场之和 + SelfField):
	// 抽几个设计实测一遍, 免得上面那段话变成"只写在注释里的推理"。
	grids := physics.BuildGrids(spec)
	for i := 0; i <= 4; i++ {
		rng := rand.New(rand.NewSource(int64(i)))
		cs, err := physics.VectorToCoils(physics.RandomDesign(rng, spec), spec)
		if err != nil {
			t.Fatalf("VectorToCoils: %v", err)
		}
		m := physics.MetricsFor(cs, spec, grids, physics.AnalyticSolver{})
		if !(m.BCoilMaxT >= self) {
			t.Errorf("a random design scored B_coil_max_T = %v < SelfField = %v — the metric no longer "+
				"includes the self field, so the zone clause must be re-read", m.BCoilMaxT, self)
		}
	}
}

// ---------------------------------------------------------------------------
// 协议版本与回放
// ---------------------------------------------------------------------------

func TestTraceProtocolReadsTheHelloResponse(t *testing.T) {
	proto, err := TraceProtocol(filepath.Join("..", "..", "testdata", "world_trace_golden.jsonl"))
	if err != nil {
		t.Fatalf("TraceProtocol on the v1 golden trace: %v", err)
	}
	if proto != ProtocolV1 {
		t.Errorf("the v1 golden trace declares protocol %v, want %v", proto, ProtocolV1)
	}
}

// TestV2GoldenTraceReplayedByTheGate: v2 黄金 trace 的逐字节回放由门 G20 通过 CLI 跑
// (它建的是**真**求值器); 单测这里只钉住"它声明的版本确实是 v2", 因为协议版本决定
// 该由哪套语义回放它 —— 那是回放能不能算证据的前提。
func TestV2GoldenTraceDeclaresProtocol2(t *testing.T) {
	path := filepath.Join("..", "..", "testdata", "world_trace_golden_v2.jsonl")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("the v2 golden trace has not been recorded yet (%v)", err)
	}
	proto, err := TraceProtocol(path)
	if err != nil {
		t.Fatalf("TraceProtocol on the v2 golden trace: %v", err)
	}
	if proto != ProtocolV2 {
		t.Errorf("the v2 golden trace declares protocol %v, want %v", proto, ProtocolV2)
	}
}

// TestV1GoldenTraceIsRefusedByAV2World: 版本不符时 Replay 必须拒绝, 而不是打印一堆
// 无从解释的差异(那会把"回放的是另一套语义"伪装成"世界改坏了")。
func TestV1GoldenTraceIsRefusedByAV2World(t *testing.T) {
	w := NewV2TestWorld(t, &v2ToyScorer{}, Options{})
	var log strings.Builder
	code := NewServer(w, ProtocolV2, &log).Replay(filepath.Join("..", "..", "testdata", "world_trace_golden.jsonl"), &log)
	if code == 0 {
		t.Fatalf("a v1 trace replayed green against a v2 world:\n%s", log.String())
	}
	if !strings.Contains(log.String(), "protocol 1") {
		t.Errorf("the refusal does not name the version mismatch:\n%s", log.String())
	}
}
