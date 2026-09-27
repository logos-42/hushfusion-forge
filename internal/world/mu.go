// mu.go —— 协议 3: 「交换子世界」(候选, 见 docs/world-commutator-candidates.md)。
//
// 这是一条**新世界**, 不是旧世界的新模式: 它的状态、动作空间、目标函数都不一样, 所以它
// 走自己的协议版本 3, 而 v1/v2 的字节一个不动(G18/G20 继续盯着旧世界)。
//
// 世界形状(全部声明在 hello.declaration 里, 客户端可独立复算 —— 不是"看文档"):
//
//	状态  (v, 区域族, μ, η, 窗口余量, 预算, 源)
//	      v 是 n 格场向量, η = 1 − Q_Ω(v)/Q_Ω(v₀) 是当前增益(TD11 的桥)
//	动作  **声明的有限集合**: 每个区域一个抹平动作 flatten:<region> + 一个 update_mu
//	      线编码是 one-hot; 声明之外的向量一律拒绝(bad_field)
//	目标  score = w_mu·μ/MuCeiling + w_gain·η + w_closed·1[μ ≥ MuCeiling]
//	      三项都读**状态**(前两项读 μ、第二项读场) —— 这是硬要求: 结构必须进目标函数,
//	      而不是只进观测(那样 A 的分布会逐位相同, 量了等于没量)
//
// 为什么它有"构造性非零"的 A: 动作对 (抹平 Ω, update_mu) 的两次序
//
//	先抹平后更新: v 变常值 ⟹ Q_Ω = 0 ⟹ η = 1 ⟹ μ 一步到天花板(TD12/TD14a)
//	先更新后抹平: 增益还是 η_before ⟹ μ 只走 μ + η_before(1−μ)
//
// 两次序的**终态场相同**, 只有 μ 不同; 顺序差 = (1−μ)(1−η_before)(TD15/TD16), 因此
//
//	A = score(先抹平后更新) − score(先更新后抹平)
//	  = w_mu/MuCeiling·(1−μ)(1−η_before) + w_closed·1[先更新后抹平尚未关闭] > 0
//
// 这是**定理给的非零**, 不是阈值试出来的: 每一项的符号都由 TD12/TD15/TD16 与 FC11 决定。
package world

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand"

	"github.com/logos-42/hushfusion-forge/internal/config"
	"github.com/logos-42/hushfusion-forge/internal/design"
	"github.com/logos-42/hushfusion-forge/internal/mu"
)

// 世界种类: --world 的选择。旧世界(设计参数)是默认, 新世界必须显式点名。
const (
	WorldParams = "params" // 旧世界: 设计参数世界(协议 1/2)
	WorldMu     = "mu"     // 新世界: 交换子世界(协议 3)
)

// WorldKindSupported 报告一个世界名字是否被本 build 认识。
func WorldKindSupported(kind string) bool { return kind == WorldParams || kind == WorldMu }

// MuObsTailKeys 是 μ 世界观测尾部 8 个槽位的冻结键名(顺序 = 线编码的顺序)。
//
// 场向量的 n 个格点在前面, 键名是 field_0…field_{n-1}(n 由 declaration.n_cells 给)。
var MuObsTailKeys = []string{
	"mu",                    // μ
	"eta",                   // 当前增益 η = 1 − Q_Ω(v)/Q_Ω(v₀)(下一次 update_mu 会用的那个)
	"window_margin",         // 窗口余量 1 − μ(TD19 的单调递减量)
	"budget_remaining_norm", // 剩余预算 / 预算
	"depth_norm",            // 已接受转移数 / 预算
	"resid_mu_window",       // χ_μ(B_cap, a) − 1: 判决层的 μ 窗口余量读数(随源重算)
	"source_onehot_perp",    // 源一位有效编码, 位序 = V2Sources
	"source_onehot_par",     //
}

// MuObsKeys 返回 μ 世界的观测键名(nCells 个场格点 + 尾部 8 个)。
func MuObsKeys(nCells int) []string {
	keys := make([]string, 0, nCells+len(MuObsTailKeys))
	for i := 0; i < nCells; i++ {
		keys = append(keys, fmt.Sprintf("field_%d", i))
	}
	return append(keys, MuObsTailKeys...)
}

// MuObservationDim 是 μ 世界的观测维数。
func MuObservationDim(nCells int) int { return nCells + len(MuObsTailKeys) }

// muConf 是 μ 世界的**世界配置**: 全部进 hello 的 declaration 与被哈希的规范 JSON。
//
// 它没有"隐藏参数": 计量脚本需要知道的一切(区域族、权重、天花板、预算、目标)都在这里,
// 且都能由客户端从 hello 独立复算。
type muConf struct {
	N               int
	Regions         []mu.Region
	Space           mu.ActionSpace
	WeightMu        float64
	WeightGain      float64
	WeightClosed    float64
	MuInitial       float64
	MuCeiling       float64
	Budget          int
	Target          float64
	Sources         []string
	MuStepDesignRef string // declaration 里的出处一行(别让读者以为这些数字是本层发明的)
}

// defaultMuConf 从 internal/config 取世界配置(房规: 影响分数的数字只有一个家)。
func defaultMuConf(budget int, target float64, source string) muConf {
	regions := mu.DeclaredRegions(config.MuFieldCells)
	srcs := append([]string(nil), V2Sources...)
	if !isV2Source(source) {
		panic(fmt.Sprintf("world: the mu world has no such source %q (known: %s)", source, joinQuoted(srcs)))
	}
	return muConf{
		N:               config.MuFieldCells,
		Regions:         regions,
		Space:           mu.DeclaredActionSpace(regions),
		WeightMu:        config.MuScoreWeightMu,
		WeightGain:      config.MuScoreWeightGain,
		WeightClosed:    config.MuScoreWeightClosed,
		MuInitial:       config.MuInitialMu,
		MuCeiling:       mu.Ceiling(),
		Budget:          budget,
		Target:          target,
		Sources:         srcs,
		MuStepDesignRef: "internal/design(MuCeiling = 1 − FLOOR, 上游 D-T 口径)",
	}
}

// muEpisode 是 μ 世界的一条 episode: (v, μ, 预算, 源) 与它的记账。
type muEpisode struct {
	v, v0               []float64
	mu                  float64
	source              string
	budget, used, depth int
	target              float64

	started    bool
	terminated bool
	truncated  bool

	score, delta float64
	lastAction   string
	lastIndex    int
}

// NewMuWorld 构建交换子世界。它不接受 scorer / spec —— 这个世界不打设计分, 它的目标函数
// 是 mu.Score(纯代数), 报出来的 score 就是那个函数的值(info.score 不做任何整形)。
func NewMuWorld(opts Options) *World {
	if opts.Budget <= 0 {
		opts.Budget = config.DefaultBudget
	}
	if opts.Target == 0 {
		// 0 = "用世界自己的解分数当靶"(见 mu.TargetScore 的说明): 拍一个整数靶会让世界在
		// 部分解处终止, 走不完 TD16 需要的两步。
		opts.Target = mu.TargetScore(config.MuScoreWeightMu, config.MuScoreWeightGain,
			config.MuScoreWeightClosed)
	}
	if opts.Source == "" {
		opts.Source = DefaultSource
	}
	if opts.Protocol == 0 {
		opts.Protocol = ProtocolV3
	}
	if opts.Protocol != ProtocolV3 {
		panic(fmt.Sprintf("world: the mu world speaks protocol %d only (asked for %d)", ProtocolV3, opts.Protocol))
	}
	w := &World{
		kind:     WorldMu,
		engine:   opts.Engine,
		protocol: ProtocolV3,
		source:   opts.Source,
		budget:   opts.Budget,
		target:   opts.Target,
		muCfg:    defaultMuConf(opts.Budget, opts.Target, opts.Source),
		muEp:     &muEpisode{},
	}
	return w
}

// Kind 报告这是哪条世界(params / mu)。
func (w *World) Kind() string {
	if w.kind == "" {
		return WorldParams
	}
	return w.kind
}

// isMu 报告本世界是不是交换子世界。
func (w *World) isMu() bool { return w.Kind() == WorldMu }

// MuConfExport 只给本包内部与 CLI 用: 世界配置的一份只读投影(测试与 trace 检查用它断言
// 声明的形状, 不拿它当第二个真源)。
func (w *World) MuConfExport() (nCells, actionDim, obsDim int, keys []string) {
	c := w.muCfg
	return c.N, c.Space.Dim(), MuObservationDim(c.N), MuObsKeys(c.N)
}

// ---------------------------------------------------------------------------
// 转移
// ---------------------------------------------------------------------------

// muApply 施加第 k 个声明动作。
//
//	flatten:<region>  v ← P_A v(TD1 的抹平; μ 不动)
//	update_mu         η ← 1 − Q_Ω(v)/Q_Ω(v₀)(TD11 的桥), μ ← μ + η(1−μ)(TD1)
func (w *World) muApply(k int) {
	ep, c := w.muEp, w.muCfg
	switch c.Space.Kinds[k] {
	case mu.KindFlatten:
		region, ok := mu.RegionByKey(c.Regions, c.Space.Regions[k])
		if !ok {
			panic(fmt.Sprintf("world: declared action %q names an unknown region %q",
				c.Space.Keys[k], c.Space.Regions[k]))
		}
		ep.v = mu.Flatten(ep.v, region.Cells)
	case mu.KindUpdateMu:
		eta := mu.GainOf(ep.v, ep.v0, mu.AllCells(c.N))
		ep.mu = mu.MuStep(ep.mu, eta)
	default:
		panic(fmt.Sprintf("world: declared action %d has unknown kind %q", k, c.Space.Kinds[k]))
	}
	ep.lastAction = c.Space.Keys[k]
	ep.lastIndex = k
}

// muScore 是目标函数(声明在 hello.declaration.score 里, 客户端可独立复算)。
func (w *World) muScore() float64 {
	c := w.muCfg
	ep := w.muEp
	s := c.WeightMu*(ep.mu/c.MuCeiling) +
		c.WeightGain*mu.GainOf(ep.v, ep.v0, mu.AllCells(c.N))
	if ep.mu >= c.MuCeiling {
		s += c.WeightClosed
	}
	return s
}

// muNewEpisode 按 (seed | x0) 开始一条新 episode。
//
// 场没有种子就现生一条(标准库 rand + seed, 与旧世界的 seeded start 同一做法); x0 优先。
func (w *World) muNewEpisode(x0 []float64, seed *int, source string, budget int, target float64) {
	c := &w.muCfg
	if source == "" {
		source = w.source
	}
	if !isV2Source(source) {
		panic(fmt.Sprintf("world: the mu world has no such source %q", source))
	}
	if budget <= 0 {
		budget = w.budget
	}
	if target == 0 {
		target = w.target
	}
	v := make([]float64, c.N)
	if x0 != nil {
		if len(x0) != c.N {
			panic("world: muNewEpisode got a field vector of the wrong length")
		}
		copy(v, x0)
	} else {
		s := 0
		if seed != nil {
			s = *seed
		}
		rng := rand.New(rand.NewSource(int64(s)))
		for i := range v {
			v[i] = rng.NormFloat64()
		}
	}
	// 参照场自己没有起伏时 TD11 的桥没有定义(0/0)。这时大声失败, 而不是返回一个假 η。
	if mu.FluctuationEnergy(v, mu.AllCells(c.N)) <= 0 {
		panic(fmt.Sprintf("world: the mu world refuses a field with no fluctuation "+
			"(Q_Omega(v0) = %v): the bridge eta = 1 − Q/Q0 (TD11) has no meaning there",
			mu.FluctuationEnergy(v, mu.AllCells(c.N))))
	}
	w.muEp = &muEpisode{
		v:      v,
		v0:     append([]float64(nil), v...),
		mu:     c.MuInitial,
		source: source,
		budget: budget,
		target: target,
	}
	ep := w.muEp
	ep.started = true
	ep.score = w.muScore()
	ep.delta = 0
	ep.lastAction = ""
	ep.lastIndex = -1
}

// ---------------------------------------------------------------------------
// 协议 3 的消息
// ---------------------------------------------------------------------------

// helloV3 是协议 3 的握手: 维度 + 观测键 + **世界声明**(带 sha256)。
//
// 声明里既有动作空间也有目标函数 —— 客户端据此复算 score、判断哪些动作对可交换, 不需要
// 第二份文档; 而 sha256 让"两边说的是同一个世界"变成可跨语言检查的事实。
func (w *World) helloV3() []byte {
	c := w.muCfg
	return []byte(obj(
		kv{"ok", boolean(true)},
		kv{"protocol", itg(ProtocolV3)},
		kv{"world_kind", str(WorldMu)},
		kv{"action_dim", itg(c.Space.Dim())},
		kv{"observation_dim", itg(MuObservationDim(c.N))},
		kv{"obs_keys_v3", strArr(MuObsKeys(c.N))},
		kv{"budget", itg(c.Budget)},
		kv{"target", flt(c.Target)},
		kv{"declaration", muDeclarationJSON(c)},
		kv{"engine", obj(kv{"version", str(w.engine)})},
	))
}

// muDeclarationJSON 是 hello 里那份世界声明(规范 JSON 体 + 末尾 sha256)。
//
// 与 spec 的约定一致: sha256 自己**不参与**哈希(那是循环的), 所以它只作为最后一个键附上,
// 客户端可以复用 canonical_spec_json() 那条既有路径来独立复算。
func muDeclarationJSON(c muConf) raw {
	body := muDeclarationCanonical(c)
	if len(body) == 0 || body[len(body)-1] != '}' {
		panic("world: mu declaration canonical JSON is not an object")
	}
	return raw(body[:len(body)-1] + `,"sha256":` + string(str(muDeclarationSHA256(c))) + "}")
}

// muDeclarationCanonical 是**规范 JSON**(键按字节序, 浮点最短往返), 也就是被哈希的那个对象。
//
// 键序是字节序而不是"我抄的顺序": action_space < budget < kind < mu_ceiling < mu_initial <
// n_cells < regions < score < sources < target。有一条单测递归检查每一层的键都是有序的,
// 所以以后加键时不会悄悄破坏跨语言复算。
func muDeclarationCanonical(c muConf) string {
	regions := make([]raw, 0, len(c.Regions))
	for _, r := range c.Regions {
		regions = append(regions, obj(
			kv{"cells", intArr(r.Cells)},
			kv{"key", str(r.Key)},
		))
	}
	vectors := make([]raw, 0, len(c.Space.Vectors))
	for _, vec := range c.Space.Vectors {
		vectors = append(vectors, rawArr(vec))
	}
	actionSpace := obj(
		kv{"keys", strArr(c.Space.Keys)},
		kv{"kind", str("declared_finite_set")},
		kv{"primitives", strArr([]string{string(mu.KindFlatten), string(mu.KindUpdateMu)})},
		kv{"regions", strArr(c.Space.Regions)},
		kv{"vectors", arr(vectors)},
	)
	score := obj(
		kv{"formula", str("score = w_mu*(mu/mu_ceiling) + w_gain*(1 - Q_Omega(v)/Q_Omega(v0)) " +
			"+ w_closed*1[mu >= mu_ceiling]")},
		kv{"kind", str("mu_window_v1")},
		kv{"mu_ceiling_source", str(c.MuStepDesignRef)},
		kv{"terms", strArr([]string{"mu_progress", "gain", "window_closed"})},
		kv{"weights", obj(
			kv{"closed", flt(c.WeightClosed)},
			kv{"gain", flt(c.WeightGain)},
			kv{"mu", flt(c.WeightMu)},
		)},
	)
	return string(obj(
		kv{"action_space", actionSpace},
		kv{"budget", itg(c.Budget)},
		kv{"kind", str("mu_window_v1")},
		kv{"mu_ceiling", flt(c.MuCeiling)},
		kv{"mu_initial", flt(c.MuInitial)},
		kv{"n_cells", itg(c.N)},
		kv{"regions", arr(regions)},
		kv{"score", score},
		kv{"sources", strArr(c.Sources)},
		kv{"target", flt(c.Target)},
	))
}

// muDeclarationSHA256 是声明规范 JSON 的 sha256(声明里没有 sha256 字段, 所以不循环)。
func muDeclarationSHA256(c muConf) string {
	sum := sha256.Sum256([]byte(muDeclarationCanonical(c)))
	return hex.EncodeToString(sum[:])
}

// resetMu 开始一条 μ episode。
//
//	{"op":"reset","seed":12345}
//	{"op":"reset","x0":[16 个格点],"regime":{"source":"MATBG_N2_par","budget":12,"target":1.0}}
//
// 允许的字段与 v2 的 regime 同名同义(源/预算/目标), 少一个 spec —— 这个世界没有设计参数。
func (w *World) resetMu(top map[string]json.RawMessage) []byte {
	if code, msg := rejectUnknown(top, OpKey, "x0", "seed", "regime"); code != "" {
		return errLine(code, msg)
	}
	var x0 []float64
	if rawX0, ok := top["x0"]; ok {
		v, code, msg := decodeVector(rawX0, "x0")
		if code != "" {
			return errLine(code, msg)
		}
		if len(v) != w.muCfg.N {
			return errLine(CodeDimMismatch, fmt.Sprintf("x0 has %d cells, this world's field has %d",
				len(v), w.muCfg.N))
		}
		for _, x := range v {
			if !isFinite(x) {
				return errLine(CodeBadField, "x0 must be finite")
			}
		}
		x0 = v
	}
	var seedPtr *int
	if rawSeed, ok := top["seed"]; ok {
		s, code, msg := decodeInt(rawSeed, "seed")
		if code != "" {
			return errLine(code, msg)
		}
		seedPtr = &s
	}
	if x0 == nil && seedPtr == nil {
		return errLine(CodeBadField, "reset needs x0 or seed: both are missing")
	}

	source, budget, target := w.source, w.budget, w.target
	if rawRegime, ok := top["regime"]; ok {
		regime, code, msg := decodeObject(rawRegime, "regime")
		if code != "" {
			return errLine(code, msg)
		}
		if code, msg := rejectUnknown(regime, "source", "budget", "target", "x0"); code != "" {
			return errLine(code, msg)
		}
		if rawSrc, ok := regime["source"]; ok {
			s, code, msg := decodeString(rawSrc, "regime.source")
			if code != "" {
				return errLine(code, msg)
			}
			if !isV2Source(s) {
				return errLine(CodeBadField, fmt.Sprintf("unknown source %q (known: %s)", s, joinQuoted(V2Sources)))
			}
			source = s
		}
		if rawBudget, ok := regime["budget"]; ok {
			b, code, msg := decodeInt(rawBudget, "regime.budget")
			if code != "" {
				return errLine(code, msg)
			}
			if b <= 0 {
				return errLine(CodeBadField, fmt.Sprintf("regime.budget must be positive, got %d", b))
			}
			budget = b
		}
		if rawTarget, ok := regime["target"]; ok {
			tv, code, msg := decodeFloat(rawTarget, "regime.target")
			if code != "" {
				return errLine(code, msg)
			}
			target = tv
		}
		if rawX0, ok := regime["x0"]; ok && x0 == nil {
			v, code, msg := decodeVector(rawX0, "regime.x0")
			if code != "" {
				return errLine(code, msg)
			}
			if len(v) != w.muCfg.N {
				return errLine(CodeDimMismatch, fmt.Sprintf("regime.x0 has %d cells, this world's field has %d",
					len(v), w.muCfg.N))
			}
			for _, x := range v {
				if !isFinite(x) {
					return errLine(CodeBadField, "regime.x0 must be finite")
				}
			}
			x0 = v
		}
	}

	w.muNewEpisode(x0, seedPtr, source, budget, target)
	return []byte(obj(
		kv{"ok", boolean(true)},
		kv{"obs", rawArr(w.obsV3())},
		kv{"info", w.infoV3()},
	))
}

// stepMu 施加一个**声明的**动作。
//
// 动作是声明式动作空间里的一个线编码(one-hot); 不在声明里、或维数不对的向量一律
// bad_field —— 世界不能"顺手也接受一下"一个它没有的动作。
func (w *World) stepMu(top map[string]json.RawMessage) []byte {
	if code, msg := rejectUnknown(top, OpKey, "action"); code != "" {
		return errLine(code, msg)
	}
	rawAction, ok := top["action"]
	if !ok {
		return errLine(CodeBadField, "missing field: action")
	}
	action, code, msg := decodeVector(rawAction, "action")
	if code != "" {
		return errLine(code, msg)
	}
	if len(action) != w.muCfg.Space.Dim() {
		return errLine(CodeDimMismatch, fmt.Sprintf("action has %d values, this world declares %d actions",
			len(action), w.muCfg.Space.Dim()))
	}
	for _, a := range action {
		if !isFinite(a) {
			return errLine(CodeBadField, "action values must be finite")
		}
	}
	ep := w.muEp
	if !ep.started {
		return errLine(CodeNotStarted, "step before reset: this episode has no state yet")
	}
	if ep.terminated || ep.truncated {
		return errLine(CodeBadField, fmt.Sprintf(
			"episode is over (terminated=%v truncated=%v): refusing to step a closed episode",
			ep.terminated, ep.truncated))
	}
	k, ok := w.muCfg.Space.Index(action)
	if !ok {
		return errLine(CodeBadField, fmt.Sprintf(
			"action %v is not one of the %d declared actions (%s)",
			action, w.muCfg.Space.Dim(), joinQuoted(w.muCfg.Space.Keys)))
	}

	prev := ep.score
	w.muApply(k)
	ep.depth++
	ep.used++
	ep.score = w.muScore()
	ep.delta = ep.score - prev
	if ep.score >= ep.target {
		ep.terminated = true
	} else if ep.used >= ep.budget {
		ep.truncated = true
	}
	return []byte(obj(
		kv{"ok", boolean(true)},
		kv{"obs", rawArr(w.obsV3())},
		kv{"info", w.infoV3()},
	))
}

// obsV3 渲染 μ 世界的观测:
//
//	[ field_0 … field_{n-1} | mu | eta | window_margin | budget_remaining_norm |
//	  depth_norm | resid_mu_window | source_onehot_perp | source_onehot_par ]
//
// 前 n 维是**原始场**(不做归一化: 抹平算子的语义住在原始尺度上, 归一化会改变 Q 的比值)。
// 后 8 维把 μ 世界自己关心的事报全(μ、增益、窗口余量、预算、判决层读数、源)。
func (w *World) obsV3() []float64 {
	ep := w.muEp
	omega := mu.AllCells(w.muCfg.N)
	out := make([]float64, 0, MuObservationDim(w.muCfg.N))
	out = append(out, ep.v...)
	eta := mu.GainOf(ep.v, ep.v0, omega)
	resid := w.residualsForSource(ep.source)[1]
	out = append(out,
		ep.mu,
		eta,
		mu.WindowMargin(ep.mu),
		float64(ep.budget-ep.used)/float64(ep.budget),
		float64(ep.depth)/float64(ep.budget),
		resid,
	)
	out = append(out, w.sourceOneHot(ep.source)...)
	return out
}

// infoV3 渲染 μ 世界的信息: score 就是目标函数的值, 外加这个世界自己的状态读数。
//
// 报出来的量都是**真量**: score 与 delta_score 由 mu.Score 直接给出(没有整形), eta 是
// TD11 的桥, closed 是 FC11 的天花板判定。
func (w *World) infoV3() raw {
	ep := w.muEp
	c := w.muCfg
	omega := mu.AllCells(c.N)
	return obj(
		kv{"score", flt(ep.score)},
		kv{"delta_score", flt(ep.delta)},
		kv{"step", itg(ep.depth)},
		kv{"depth", itg(ep.depth)},
		kv{"budget_remaining", itg(ep.budget - ep.used)},
		kv{"terminated", boolean(ep.terminated)},
		kv{"truncated", boolean(ep.truncated)},
		kv{"action", str(ep.lastAction)},
		kv{"action_index", itg(ep.lastIndex)},
		kv{"mu", flt(ep.mu)},
		kv{"eta", flt(mu.GainOf(ep.v, ep.v0, omega))},
		kv{"window_margin", flt(mu.WindowMargin(ep.mu))},
		kv{"mu_ceiling", flt(c.MuCeiling)},
		kv{"closed", boolean(ep.mu >= c.MuCeiling)},
		kv{"fluctuation_energy", flt(mu.FluctuationEnergy(ep.v, omega))},
		kv{"source", str(ep.source)},
	)
}

// residualsForSource 是判决层的读数(μ 窗口余量那一条随源重算, 见 v2.go 的 residuals)。
//
// 这里只取第 1 条(χ_μ − 1); 死活判据与导体天花板与本世界的状态无关, 不在这里现编。
func (w *World) residualsForSource(source string) [3]float64 {
	src, ok := design.LookupSource(source)
	if !ok {
		panic(fmt.Sprintf("world: residuals asked for an unknown source %q", source))
	}
	return [3]float64{
		src.BCapT/design.BDeath(design.ARef) - 1,
		design.ChiMu(src.BCapT, design.ARef) - 1,
		0, // 第三条(导体天花板)要 metrics, 本世界没有设计求值 —— 不在这里造一个
	}
}

// muFieldFromSeed 是**测试与 trace 记录**用的场生成器(与 muNewEpisode 同一段逻辑的显式版本),
// 保证黄金 trace 与回放能独立造出同一批数。
func muFieldFromSeed(seed, n int) []float64 {
	rng := rand.New(rand.NewSource(int64(seed)))
	v := make([]float64, n)
	for i := range v {
		v[i] = rng.NormFloat64()
	}
	return v
}
