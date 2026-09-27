// v2.go — 世界协议 v2 的语义: 顺序依赖(夹取 / 前提门 / 换源)。
//
// 契约: docs/world-protocol.md §8 (与正文冲突时以 §8 为准) 与 docs/world-structure.md
// (判据 + 计量协议 + 预注册阈值)。判据文本在 docs/world-structure.md, 这里只实现它。
//
// # 本文件实现什么
//
// v2 让设计空间**有可被抽象的顺序依赖**, 并把这四类规则的开关状态放进观测:
//
//	R2 边界夹取  动作是当前参数上的增量, 结果夹进盒子 —— 夹取生效时
//	             clip(clip(θ+d₁)+d₂) ≠ clip(clip(θ+d₂)+d₁), 于是 A ≠ 0。
//	R3 前提门    当前设计在"夹取区"(近导线 5 mm 的 alpha2 钳位区, 或线圈峰场超天花板)
//	             ⟹ 只接受把设计移出该区的动作; 被拒的动作仍消耗一步预算。
//	R4 换场源    set_source 用另一个上游真源对同一个 θ 重新求值; 死活判据与 μ 窗口余量
//	             随源重算(观测里的 residual 与 one-hot 因此随源翻转)。
//	R1/R5 预算与终止  游戏规则声明, **不是** A 的来源(§2 表格两处都写明"不是")。
//
// **v2 不新增物理**: internal/design 与 internal/physics 一行不动; 本文件只在校验、
// 搬运与"顺序"的层面工作, 全部真量来自 physics.Metrics 与 design 的闭式解。
//
// # 三处文本解释(实现必须选一处, 选法写在这里, 不许藏在代码里)
//
//  1. **夹取区的第二条"(§8.4) 线圈峰场天花板 = spec.CoilFieldLimit**(HTS @20 K, 12 T),
//     不是场源材料的 B_cap(0.12 T / 1.6 T)。理由有三条, 全部可核验:
//     (a) §2 把参与夹取的钳位列成"近导线 5 mm 的 alpha2 钳位、线圈峰场天花板、μ 窗口
//     边界"——"B_coil_max_T 超天花板"对应的是**线圈峰场**的天花板, 而 B_coil_max 的
//     天花板在目标函数里就是 spec.CoilFieldLimit(它的导体场惩罚用同一个数);
//     (b) B_cap 读法会让这个区**恒开**: B_coil_max 含 spec.SelfField() = 3.1416 T,
//     而两个源的 B_cap 是 0.12 / 1.6 T —— 于是"只接受把设计移出该区的动作"永远无解,
//     世界被完全冻结(R3 变成空规则, R2 的"先修前提再调参"变成空话);
//     (c) 恒开的区还让"先修前提再调参 ≠ 先调参再修前提"这两条路都不存在, 与 §2 声称
//     R3 是 A 的来源直接矛盾。
//     恒开这件事不是推断而是**实测量**: 证据文件 testdata/world_structure_v2.json 的
//     zone_readings 区块记录了每次访问过的设计的 B_coil_max 与两个源的 B_cap。
//  2. **区是可退出的**: 判定用的是"候选设计是否**不在**区内"(§8.4 的"把设计移出该区")。
//     区判定用物理层自己的输出(见下一条), 不用解析捷径。
//  3. **§8.5 声称"换源会让 R3 的开关状态翻转"**: 在冻结数值下这不可能 —— 见 (1b),
//     任何把 B_cap 放进区判定的读法都让区恒开。所以本实现里 set_source 的作用落在
//     (i) 死活判据余量 / μ 窗口余量(观测的后三维里的前两维, 随源翻转)与
//     (ii) 判定表(design.Review 的口径)上, **不**落在 R3 的接受判定上。这一条必须
//     与 G19 的结果一起读: 它正是"四档里 perp/par 动力学相同"的原因。
//
// # 一条"不重复物理"的实现选择
//
// 前提门要问"候选设计还在不在区内", 而区判定需要 B_coil_max(一个真 metric)。本实现
// **总是先求值再判门**: 候选先交给 scorer(physics + objective), 用它的 Metrics 做区判定,
// 若被拒就把这一步丢掉(状态不变、response 报原状态), 那条被丢掉的求值在 registry 里带
// 着 rejected probe 的 Note。代价是多一次求值; 换来的是 §5 的"协议层不重复物理"——
// 区判定里的 B_coil_max 与近导线距离都取自 physics 自己的输出与它自己的 ProximityFloor,
// 协议层没有第二份"差不多的"公式。
package world

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand"

	"github.com/logos-42/hushfusion-forge/internal/config"
	"github.com/logos-42/hushfusion-forge/internal/design"
	"github.com/logos-42/hushfusion-forge/internal/objective"
	"github.com/logos-42/hushfusion-forge/internal/physics"
	"github.com/logos-42/hushfusion-forge/internal/rlenv"
	"github.com/logos-42/hushfusion-forge/internal/runner"
)

// V2Sources 是 v2 允许的场源, 顺序 = hello 的 sources 数组顺序(§8.2), 也是
// source_onehot 的位序。两个 key 都来自 internal/design 的场源表(单一真源),
// 协议层不另抄一份字符串。
var V2Sources = []string{design.SourceMATBGN2Perp, design.SourceMATBGN2Par}

// DefaultSource 是 v2 的缺省场源(§8.1: 默认值只住 internal/config —— 这里住不下,
// 因为 internal/config 不能 import internal/design(那会成环, design 已经 import config);
// 场源 key 也不是"影响分数的数字": 目标函数 score 与场源无关, 场源只进判定表与观测)。
const DefaultSource = design.SourceMATBGN2Perp

// V2ObsTailKeys 是 v2 观测尾部 7 个槽位的冻结键名(§8.2)。
var V2ObsTailKeys = []string{
	"budget_remaining_norm", "depth_norm",
	"resid_death", "resid_mu_window", "resid_coil_ceiling",
	"source_onehot_perp", "source_onehot_par",
}

// V2ObsKeys 是 v2 观测的 26 个键名: v1 的 19 维(12 个归一化设计维 + 7 个 metric 槽位)
// 逐位相同, 后面接 V2ObsTailKeys。§8.2 用"...19 维同 v1..."指代前 19 个 —— 它们**没有**
// 名字(v1 只给 7 个 metric 槽位起了名), 所以前 12 个归一化设计维的名字是本文件起的
// (键名只用于客户端对账; 数值布局才是契约, 见 obsV2)。
func V2ObsKeys() []string {
	spec := config.DefaultSpec()
	keys := make([]string, 0, V2ObservationDim(spec))
	groups := []struct {
		prefix string
		n      int
	}{{"r_norm_", spec.NCoils}, {"z_norm_", spec.NCoils}, {"i_norm_", spec.NCoils}}
	for _, g := range groups {
		for i := 0; i < g.n; i++ {
			keys = append(keys, fmt.Sprintf("%s%d", g.prefix, i))
		}
	}
	keys = append(keys, rlenv.ObsMetricKeys...)
	keys = append(keys, V2ObsTailKeys...)
	return keys
}

// V2ObservationDim 是 v2 的观测维度: v1 的(n_params + 7) 再加尾部 7 维(§8.2: 26)。
func V2ObservationDim(spec config.Spec) int {
	return spec.NParams() + len(rlenv.ObsMetricKeys) + len(V2ObsTailKeys)
}

// episodeState 是 v2 一条 episode 的状态。
//
// 它刻意**不是** rlenv.Env: v2 的转移(前提门、预算、换源、代价值记账)与 v1 不同,
// 而 internal/rlenv/api.go 是冻结接口(签名/类型/JSON tag 不许动)。协议 1 仍然走
// rlenv.Env(逐字节不变, G18 盯着它), 协议 2 走这里 —— 两边共用同一个 scorer,
// 所以"score 是真目标函数"这条对两个协议都成立。
type episodeState struct {
	started bool
	over    bool // terminated 或 truncated: 之后的 step/set_source 都不推进状态
	// maxSteps 是 v1 的截断口径(protocol 1 用); v2 的截断由 budget 决定(§8.4)。
	truncated  bool
	terminated bool

	budget int // 本 episode 的步数预算(regime 可覆盖)
	target float64
	used   int // 已消耗的预算(被拒的动作也消耗, §8.4)
	depth  int // 已接受的转移数(step + set_source); 与 v1 的 info.step 同一口径

	x        []float64 // 当前设计(SI 单位, 始终在盒内)
	score    float64
	delta    float64 // 最近一次转移的 Δscore(被拒的步为 0)
	feasible bool
	designID string
	metrics  physics.Metrics

	// costPrev 是**上一步那个设计**的真 cost_proxy —— §8.4 的 R1 公式的分母。
	// 第 1 步它就是 episode 初始设计的 cost(由 reset 那次求值算出), 所以"cost_ref 不用常数"
	// (§8.4) 这条要求自动成立: 这里根本没有任何常数。
	costPrev float64

	source string

	lastRejected bool
	lastClamped  []int
}

func (e *episodeState) remaining() int {
	if e.used >= e.budget {
		return 0
	}
	return e.budget - e.used
}

// newEpisode 开始一条 episode: 求值起点设计并把它记成 lineage 的根。
func (w *World) newEpisode(x0 []float64, source string, budget int, target float64) {
	ep := &episodeState{
		started: true,
		budget:  budget,
		target:  target,
		source:  source,
		x:       w.clipDesign(x0),
		metrics: physics.Metrics{},
	}
	w.ep = ep
	res := w.scoreV2(ep.x, "", 0, "world v2 episode start")
	w.absorbV2(res)
	ep.costPrev = ep.metrics.CostProxy
	if !(ep.costPrev > 0) {
		// 前提检查: R1 的第一步就要拿这个数做分母 —— 一个非正的 cost 会让奖励变成
		// 一个没有意义的数, 那属于"世界无法诚实作答"(大声失败), 不属于"这一步很难看"。
		panic(fmt.Sprintf("world: the episode's initial design has cost_proxy %v — "+
			"refusing to divide R1's cost term by a non-positive cost", ep.costPrev))
	}
}

// absorbV2 把 scorer 实际求值的结果存为新状态(与 rlenv.absorb 同一语义: 状态是
// scorer 真正求值过的那个规范向量, 而不是我们希望它求值的那个)。
func (w *World) absorbV2(res objective.EvalResult) {
	ep := w.ep
	if len(res.Design) == len(ep.x) {
		ep.x = copyOf(res.Design)
	}
	ep.score = res.Score
	ep.feasible = res.Feasible
	ep.designID = res.DesignID
	ep.metrics = res.Metrics
}

// scoreV2 以正确的来源信息求值一个设计(scorer 的 meta 必须说得出这是谁提出的)。
func (w *World) scoreV2(x []float64, parent string, generation int, note string) objective.EvalResult {
	ep := w.ep
	idx := 0
	if ep != nil {
		idx = ep.used
	}
	return w.obs.Score(copyOf(x), runner.Meta{
		Algorithm:  algorithmWorldV2,
		Generation: generation,
		EvalIndex:  idx,
		Parent:     parent,
		Note:       note,
	})
}

// algorithmWorldV2 是 v2 写进 registry 的算法标签: 一条 record 必须说得出它是被谁
// 提出来的(与 v1 经 rlenv 落成 "rlenv" 是同一条理由)。
const algorithmWorldV2 = "world_v2"

// ---------------------------------------------------------------------------
// hello (§8.2)
// ---------------------------------------------------------------------------

// helloV2 是协议 2 的握手响应: v1 的字段一个不动, 后面接 §8.2 的新字段。
func (w *World) helloV2() []byte {
	return []byte(obj(
		kv{"ok", boolean(true)},
		kv{"protocol", itg(ProtocolV2)},
		kv{"action_dim", itg(w.spec.NParams())},
		kv{"observation_dim", itg(V2ObservationDim(w.spec))},
		kv{"obs_metric_keys", strArr(rlenv.ObsMetricKeys)},
		kv{"obs_metric_refs", rawArr(rlenv.ObsMetricRefs)},
		kv{"obs_keys_v2", strArr(w.v2ObsKeys())},
		kv{"budget", itg(w.budget)},
		kv{"target", flt(w.target)},
		kv{"delta_scale", flt(w.deltaScale)},
		kv{"max_steps", itg(w.maxSteps)},
		kv{"sources", strArr(V2Sources)},
		kv{"clamp_zones", obj(kv{"coil_proximity_floor_m", flt(physics.ProximityFloor)})},
		kv{"spec", specJSON(w.spec)},
		kv{"engine", obj(kv{"version", str(w.engine)})},
	))
}

// v2ObsKeys 是给当前 spec 的 26 个键名(设计维数与 spec 的 n_coils 绑定)。
func (w *World) v2ObsKeys() []string {
	keys := make([]string, 0, V2ObservationDim(w.spec))
	k := w.spec.NCoils
	for i := 0; i < k; i++ {
		keys = append(keys, fmt.Sprintf("r_norm_%d", i))
	}
	for i := 0; i < k; i++ {
		keys = append(keys, fmt.Sprintf("z_norm_%d", i))
	}
	for i := 0; i < k; i++ {
		keys = append(keys, fmt.Sprintf("i_norm_%d", i))
	}
	keys = append(keys, rlenv.ObsMetricKeys...)
	keys = append(keys, V2ObsTailKeys...)
	return keys
}

// ---------------------------------------------------------------------------
// reset (§8.3)
// ---------------------------------------------------------------------------

// resetV2 开始一条 v2 episode。
//
//	{"op":"reset","seed":12345,"regime":{"source":"MATBG_N2_par","budget":8,"target":1.0,"x0":[...]}}
//
// x0 与 regime 同时给出时 x0 优先(§8.3), 先夹进盒子再求值 —— 与 v1 一致。
func (w *World) resetV2(top map[string]json.RawMessage) []byte {
	if code, msg := rejectUnknown(top, OpKey, "x0", "seed", "regime"); code != "" {
		return errLine(code, msg)
	}

	source, budget, target := w.source, w.budget, w.target
	var regimeX0 []float64
	hasRegimeX0 := false

	if rawRegime, ok := top["regime"]; ok {
		regime, code, msg := decodeObject(rawRegime, "regime")
		if code != "" {
			return errLine(code, msg)
		}
		if code, msg := rejectUnknown(regime, "source", "budget", "target", "x0"); code != "" {
			return errLine(code, msg)
		}
		if raw, ok := regime["source"]; ok {
			s, code, msg := decodeString(raw, "regime.source")
			if code != "" {
				return errLine(code, msg)
			}
			if !isV2Source(s) {
				return errLine(CodeBadField, fmt.Sprintf("regime.source %q is not one of the supported sources %s",
					s, joinQuoted(V2Sources)))
			}
			source = s
		}
		if raw, ok := regime["budget"]; ok {
			n, code, msg := decodeInt(raw, "regime.budget")
			if code != "" {
				return errLine(code, msg)
			}
			if n <= 0 {
				return errLine(CodeBadField, fmt.Sprintf("regime.budget must be a positive integer, got %d", n))
			}
			budget = n
		}
		if raw, ok := regime["target"]; ok {
			v, code, msg := decodeFloat(raw, "regime.target")
			if code != "" {
				return errLine(code, msg)
			}
			if !isFinite(v) {
				return errLine(CodeBadField, fmt.Sprintf("regime.target must be a finite number, got %v", v))
			}
			target = v
		}
		if raw, ok := regime["x0"]; ok {
			vals, code, msg := decodeVector(raw, "regime.x0")
			if code != "" {
				return errLine(code, msg)
			}
			if len(vals) != w.spec.NParams() {
				return errLine(CodeDimMismatch,
					fmt.Sprintf("regime.x0 has %d entries, action_dim is %d", len(vals), w.spec.NParams()))
			}
			regimeX0, hasRegimeX0 = vals, true
		}
	}

	x0Raw, hasX0 := top["x0"]
	seedRaw, hasSeed := top["seed"]
	if !hasX0 && !hasRegimeX0 && !hasSeed {
		return errLine(CodeBadField, "reset needs x0 or seed: both are missing")
	}

	var x0 []float64
	switch {
	case hasX0:
		vals, code, msg := decodeVector(x0Raw, "x0")
		if code != "" {
			return errLine(code, msg)
		}
		if len(vals) != w.spec.NParams() {
			return errLine(CodeDimMismatch,
				fmt.Sprintf("x0 has %d entries, action_dim is %d", len(vals), w.spec.NParams()))
		}
		x0 = vals
	case hasRegimeX0:
		x0 = regimeX0
	default:
		var seed int64
		if err := json.Unmarshal(seedRaw, &seed); err != nil {
			return errLine(CodeBadField, fmt.Sprintf("field seed must be an integer, got %s", clipLine(seedRaw)))
		}
		x0 = physics.RandomDesign(rand.New(rand.NewSource(seed)), w.spec)
	}

	w.newEpisode(x0, source, budget, target)
	return []byte(obj(
		kv{"ok", boolean(true)},
		kv{"obs", rawArr(w.obsV2())},
		kv{"info", w.infoV2()},
	))
}

// ---------------------------------------------------------------------------
// step (§8.4)
// ---------------------------------------------------------------------------

// stepV2 施加一个归一化增量动作。
//
// 顺序:
//  1. R2 夹取: dx[i] = clip(a[i],-1,1)·delta_scale·(upper-lower), 再夹进盒子(candidate);
//  2. 求值 candidate(见文件头"不重复物理"那一段);
//  3. R3 前提门: 当前设计在区内而 candidate 仍在区内 ⟹ 拒绝 —— 状态不变、rejected=true、
//     仍消耗一步预算;
//  4. 接受: 状态 = scorer 真正求值过的规范向量, depth+1, 记预算, R1 的 reward。
func (w *World) stepV2(top map[string]json.RawMessage) []byte {
	if code, msg := rejectUnknown(top, OpKey, "action"); code != "" {
		return errLine(code, msg)
	}
	actionRaw, ok := top["action"]
	if !ok {
		return errLine(CodeBadField, "missing field: action")
	}
	if w.ep == nil || !w.ep.started {
		return errLine(CodeNotStarted, "step before reset: this world has no current design")
	}
	action, code, msg := decodeVector(actionRaw, "action")
	if code != "" {
		return errLine(code, msg)
	}
	if len(action) != w.spec.NParams() {
		return errLine(CodeDimMismatch,
			fmt.Sprintf("action has %d entries, action_dim is %d", len(action), w.spec.NParams()))
	}

	ep := w.ep
	if ep.over {
		// 已结束之后再 step 不是错误(§3.3): 报 terminated=true/truncated=true/reward=0,
		// 且**不再消耗求值与预算**。
		ep.lastRejected = false
		ep.lastClamped = nil
		ep.delta = 0
		return []byte(obj(
			kv{"ok", boolean(true)},
			kv{"obs", rawArr(w.obsV2())},
			kv{"reward", flt(0)},
			kv{"terminated", boolean(true)},
			kv{"truncated", boolean(true)},
			kv{"info", w.infoV2()},
		))
	}

	candidate, clamped := w.advance(ep.x, action)
	res := w.scoreV2(candidate, ep.designID, ep.depth+1, fmt.Sprintf("world v2 step %d", ep.depth+1))

	inZone := w.inClampZone(ep.x, ep.metrics)
	outOfZone := !w.inClampZone(res.Design, res.Metrics)
	rejected := inZone && !outOfZone

	ep.lastRejected = rejected
	ep.lastClamped = clamped
	ep.used++

	if rejected {
		// 被拒的动作不改状态、不给分: score/delta/design_id 都停在原处。
		ep.delta = 0
		// 上面那次求值是"被丢掉的探针", 它在 registry 里的 Note 说得出这一点
		// (见 scoreV2 的调用点)。
	} else {
		before := ep.score
		w.absorbV2(res)
		ep.depth++
		ep.delta = ep.score - before
		cost := ep.metrics.CostProxy
		// §8.4 的 R1: reward = Δscore − λ_cost · max(0, Δcost_proxy) / cost(θ_prev)。
		// 分母用**上一步那个设计**的真 cost(公式在 §8.4 与 world-structure §2 两处都这么写);
		// 第 1 步的 θ_prev 就是 reset 那个设计, 所以第一步的分母也等于 episode 初始 cost。
		// (注意 §8.4 紧接公式的那句"cost_ref 不用常数: 用该 episode 初始设计的真 cost"与
		//  公式本身是两回事 —— 它只差在第 2 步之后。这里按公式实现; 而且 reward 不进任何门:
		//  A 只读真目标函数 score(§1), 所以这个歧义不影响 G19 的判定。)
		if !(ep.costPrev > 0) {
			panic(fmt.Sprintf("world: the previous design's cost_proxy is %v — refusing to divide R1's "+
				"cost term by a non-positive cost", ep.costPrev))
		}
		ep.delta -= config.LambdaCost * math.Max(0, cost-ep.costPrev) / ep.costPrev
		ep.costPrev = cost
		if ep.score >= ep.target {
			ep.terminated = true
			ep.over = true
		}
	}
	if ep.used >= ep.budget {
		ep.truncated = true
		ep.over = true
	}

	return []byte(obj(
		kv{"ok", boolean(true)},
		kv{"obs", rawArr(w.obsV2())},
		kv{"reward", flt(ep.delta)},
		kv{"terminated", boolean(ep.terminated)},
		kv{"truncated", boolean(ep.truncated)},
		kv{"info", w.infoV2()},
	))
}

// advance 是 §8.4 的动作语义: 归一化增量加在**当前**参数上, 结果夹进 spec 盒子。
// clamped 是被夹住的坐标下标(哪些参数撞到了盒子) —— 它进 info.clamped。
//
// 动作先夹到 [-1,1], 再乘 delta_scale·span, 最后夹进盒子: 与 rlenv.Env.Step 逐位同序。
func (w *World) advance(x, action []float64) (next []float64, clamped []int) {
	lo, hi := w.spec.Lower(), w.spec.Upper()
	next = make([]float64, len(x))
	for i := range x {
		a := clip(action[i], -1, 1)
		v := x[i] + a*w.deltaScale*(hi[i]-lo[i])
		c := clip(v, lo[i], hi[i])
		if c != v {
			clamped = append(clamped, i)
		}
		next[i] = c
	}
	return next, clamped
}

// inClampZone 判断一个设计是否处在 R3 的"夹取区"(§8.4):
//
//	(i)  某个打分点到某圈导线的距离 < physics.ProximityFloor(5e-3 m) —— 也就是
//	     物理层 loopField 的 alpha2 钳位正在生效;
//	(ii) B_coil_max_T 超过线圈峰场天花板 spec.CoilFieldLimit(见文件头解释 1)。
//
// 两个判据都取自物理层自己的量: (i) 用的是同一个 ProximityFloor 与同一批打分点
// (physics.BuildGrids 的堆叠采样点 = solver 真正求值的那些点), (ii) 用的是 scorer
// 报出来的 B_coil_max_T。协议层在这里没有第二份公式。
func (w *World) inClampZone(x []float64, m physics.Metrics) bool {
	if m.BCoilMaxT > w.spec.CoilFieldLimit {
		return true
	}
	coils, err := physics.VectorToCoils(x, w.spec)
	if err != nil {
		panic(fmt.Sprintf("world: cannot decode a design while checking the clamp zone: %v", err))
	}
	return w.sampleTouchesWire(coils)
}

// sampleTouchesWire 报告是否存在一个打分点落在某圈导线的 ProximityFloor 之内。
//
// 点到圆环导线(半径 a, 轴向 z_c)的距离平方正是闭式解里的 alpha2:
//
//	alpha2 = a² + r² + Δz² − 2·a·r = (a−r)² + Δz²
//
// 所以"alpha2 被钳住"与这里的距离判定是同一个不等式, 不是两个"差不多"的判据。
func (w *World) sampleTouchesWire(coils []physics.Coil) bool {
	floor2 := physics.ProximityFloor * physics.ProximityFloor
	for _, c := range coils {
		for i, r := range w.gridR {
			dr := c.Radius - r
			dz := c.Z - w.gridZ[i]
			if dr*dr+dz*dz < floor2 {
				return true
			}
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// set_source (§8.5)
// ---------------------------------------------------------------------------

// setSourceV2 用新场源对**同一个 θ** 重新求值。消耗 1 步预算; 预算用尽 ⟹ episode 变成
// truncated(下一次 step 会报 truncated=true, §8.5)。
//
// 重新求值是字面要求(§8.5"用新源对同一个 θ 重新求值"): 它落一条新的 registry record
// (同一个设计、新的 design_id), 而 score 不变 —— 目标函数与场源无关这件事本身是
// 判据的一部分(A 只读真目标函数, §1)。
func (w *World) setSourceV2(top map[string]json.RawMessage) []byte {
	if code, msg := rejectUnknown(top, OpKey, "source"); code != "" {
		return errLine(code, msg)
	}
	rawSource, ok := top["source"]
	if !ok {
		return errLine(CodeBadField, "missing field: source")
	}
	source, code, msg := decodeString(rawSource, "source")
	if code != "" {
		return errLine(code, msg)
	}
	if !isV2Source(source) {
		return errLine(CodeBadField, fmt.Sprintf("unknown source %q (hello.sources lists %s)",
			source, joinQuoted(V2Sources)))
	}
	if w.ep == nil || !w.ep.started {
		return errLine(CodeNotStarted, "set_source before reset: this world has no current design")
	}

	ep := w.ep
	ep.lastRejected = false
	ep.lastClamped = nil
	ep.delta = 0
	if !ep.over {
		res := w.scoreV2(ep.x, ep.designID, ep.depth+1, fmt.Sprintf("world v2 set_source %s", source))
		w.absorbV2(res)
		ep.source = source
		ep.depth++
		ep.used++
		ep.costPrev = ep.metrics.CostProxy
		if ep.used >= ep.budget {
			ep.truncated = true
			ep.over = true
		}
	}
	return []byte(obj(
		kv{"ok", boolean(true)},
		kv{"obs", rawArr(w.obsV2())},
		kv{"info", w.infoV2()},
	))
}

// ---------------------------------------------------------------------------
// 观测与 info
// ---------------------------------------------------------------------------

// obsV2 渲染 v2 的 26 维观测(§8.2 / §2):
//
//	[ v1 的 19 维 | budget_remaining_norm | depth_norm | resid_death | resid_mu_window |
//	  resid_coil_ceiling | source_onehot_perp | source_onehot_par ]
//
// 前 19 维必须与协议 1 对同一个设计给出的 19 维**逐位相同**(G18 的旧 trace 与
// world_test.go 的 TestV2ObsPrefixMatchesV1 一起盯着这条)。
func (w *World) obsV2() []float64 {
	ep := w.ep
	lo, hi := w.spec.Lower(), w.spec.Upper()
	out := make([]float64, 0, V2ObservationDim(w.spec))
	for i := range ep.x {
		span := hi[i] - lo[i]
		if span <= 0 {
			panic(fmt.Sprintf("world: degenerate bound span (upper-lower=%v) at parameter %d", span, i))
		}
		out = append(out, clip(2*(ep.x[i]-lo[i])/span-1, -1, 1))
	}
	for i, key := range rlenv.ObsMetricKeys {
		ref := rlenv.ObsMetricRefs[i]
		if ref == 0 {
			ref = 1
		}
		v := rlenv.ObsMetricValue(ep.metrics, key)
		if !isFinite(v) {
			panic(fmt.Sprintf("world: metric %q is not finite (%v) — refusing to hand a policy a NaN observation",
				key, v))
		}
		out = append(out, v/ref)
	}
	resid := w.residuals(ep.metrics, ep.source)
	out = append(out,
		float64(ep.remaining())/float64(ep.budget),
		float64(ep.depth)/float64(ep.budget),
	)
	out = append(out, resid[:]...)
	out = append(out, w.sourceOneHot(ep.source)...)
	return out
}

// infoV2 渲染 v2 的 info: v1 的 5 个键(键名与 rlenv.Info 的 JSON tag 逐字相同)在前,
// §8.4 的增补在后。
//
//	step 与 depth 是同一个口径(已接受的转移数)且必须相等: step 是 v1 冻结的键,
//	depth 是 §8.4 点名要报的键, 两者都报出来, 免得客户端猜。
func (w *World) infoV2() raw {
	ep := w.ep
	resid := w.residuals(ep.metrics, ep.source)
	return obj(
		kv{"score", flt(ep.score)},
		kv{"delta_score", flt(ep.delta)},
		kv{"feasible", boolean(ep.feasible)},
		kv{"design_id", str(ep.designID)},
		kv{"step", itg(ep.depth)},
		kv{"rejected", boolean(ep.lastRejected)},
		kv{"budget_remaining", itg(ep.remaining())},
		kv{"depth", itg(ep.depth)},
		kv{"residuals", rawArr(resid[:])},
		kv{"source", str(ep.source)},
		kv{"clamped", intArr(ep.lastClamped)},
	)
}

// residuals 是三个**真**约束余量(§2: 死活判据余量 / μ 窗口余量 / 导体场天花板余量),
// 全部来自 internal/design 的闭式解 + spec 的导体场天花板, 一律**归一化**成"比值减一"
// (正号 = 门开着), 因为这三条门在判定层里的判据就是比值:
//
//	0 resid_death        B_cap / B_death(a) − 1      (design.BDeath, a = design.ARef)
//	1 resid_mu_window    χ_μ(B_cap, a) − 1           (design.ChiMu > 1 就是门开)
//	2 resid_coil_ceiling min(spec.CoilFieldLimit, B_cap) / B_coil_max − 1
//	                     (design.Review 的 coil_load 门的那个天花板, 所以它随源重算)
//
// 前两条只依赖场源(与设计无关) —— 这正是 §8.5 说的"死活判据与 μ 窗口余量随源重算";
// 第三条既依赖源也依赖设计。三者都是**真量**: 没有一个是这里现场发明的约束。
func (w *World) residuals(m physics.Metrics, source string) [3]float64 {
	src, ok := design.LookupSource(source)
	if !ok {
		panic(fmt.Sprintf("world: residuals asked for an unknown source %q", source))
	}
	if !(m.BCoilMaxT > 0) {
		panic(fmt.Sprintf("world: B_coil_max_T is not positive (%v) — refusing to invert it for a residual",
			m.BCoilMaxT))
	}
	ceiling := math.Min(w.spec.CoilFieldLimit, src.BCapT)
	return [3]float64{
		src.BCapT/design.BDeath(design.ARef) - 1,
		design.ChiMu(src.BCapT, design.ARef) - 1,
		ceiling/m.BCoilMaxT - 1,
	}
}

// sourceOneHot 是源的一位有效编码, 位序 = V2Sources(§8.2 的 sources 数组顺序)。
func (w *World) sourceOneHot(source string) []float64 {
	out := make([]float64, len(V2Sources))
	for i, s := range V2Sources {
		if s == source {
			out[i] = 1
		}
	}
	return out
}

// isV2Source 报告一个 key 是否是 v2 允许的场源(§8.5: 合法值只有 hello 的 sources)。
func isV2Source(s string) bool {
	for _, known := range V2Sources {
		if known == s {
			return true
		}
	}
	return false
}

// clipDesign 复制 x 并把它夹进 spec 盒子(与 rlenv.Env.Reset 的先夹后算一致)。
func (w *World) clipDesign(x []float64) []float64 {
	lo, hi := w.spec.Lower(), w.spec.Upper()
	out := make([]float64, len(x))
	for i := range x {
		out[i] = clip(x[i], lo[i], hi[i])
	}
	return out
}
