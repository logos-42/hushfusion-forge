// Package world: 把 internal/rlenv 的语义原样搬到进程边界之外。
//
// 冻结契约: docs/world-protocol.md (正文 = protocol v1; **§8 = protocol v2 的全部增补**,
// 与正文冲突时以 §8 为准)。本包**不定义物理、不新增 shaping、不含学习** —— 语义的唯一
// 权威是 internal/rlenv/api.go 与它上面的设计判决层; 协议层只做搬运与校验。
//
// 两个协议版本共存:
//
//	protocol 1  逐字节等于 P0 的实现(rlenv.Env 原样搬运), 供 G18 与旧 trace;
//	protocol 2  v2 的顺序依赖(夹取 / 前提门 / 换源), 见 v2.go 与 docs/world-structure.md。
//
// 为什么存在: 引擎在 Go, agent 在 Python(headless)。本仓纪律既禁止 Python import Go,
// 也禁止"用一个 Go 二进制去验证 Go", 所以剩下唯一诚实的形状就是进程间协议: 一行请求、
// 一行响应、逐字节可比。没有它, 同一条轨迹在两边的数不一样时, 分不清是物理、是序列化、
// 还是客户端接线。
//
// 三条不许破的规则:
//
//  1. stdout 只放协议行(客户端会因为一行噪声而读到错位的响应 —— 那是静默损坏的形状);
//  2. 浮点一律最短往返(strconv.FormatFloat(v, 'g', -1, 64)), 见 json.go;
//  3. 宁可大声失败, 绝不编造数字 —— 与 rlenv.LoadPolicy 返回 ErrNoLearnedPolicy 是同
//     一条策略。环境无法给出诚实答案时, 本包返回一个冻结错误码, 而不是 0.0 / 空观测。
package world

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strconv"

	"github.com/logos-42/hushfusion-forge/internal/config"
	"github.com/logos-42/hushfusion-forge/internal/objective"
	"github.com/logos-42/hushfusion-forge/internal/physics"
	"github.com/logos-42/hushfusion-forge/internal/rlenv"
	"github.com/logos-42/hushfusion-forge/internal/runner"
)

// 已知的协议版本。ProtocolVersion 是 `forge world serve --protocol` 与交互服务的缺省值
// (§8.1: 交互服务默认 2), 也是"当前版本"的意思; v1 仍然按自己的语义服务(旧 trace 与
// G18 继续有效, §8.1); v3 是**另一条世界**(交换子世界, docs/world-commutator-candidates.md),
// 它有自己的状态/动作空间/目标函数, 不是旧世界的新模式。
const (
	ProtocolV1 = 1
	ProtocolV2 = 2
	ProtocolV3 = 3

	// ProtocolVersion 是当前版本(= 默认服务的那一个)。默认世界仍是设计参数世界(v2):
	// 换默认值会让所有既有客户端对同一行字节产生不同理解, 那不在本片的范围里。
	ProtocolVersion = ProtocolV2
)

// ProtocolSupported 报告一个协议版本是否被本 build 支持。
func ProtocolSupported(p int) bool { return p == ProtocolV1 || p == ProtocolV2 || p == ProtocolV3 }

// 冻结的错误码(契约 §3.5)。它们是协议的一部分: 客户端按码分支, 不按 message 分支。
const (
	CodeBadJSON             = "bad_json"
	CodeUnknownOp           = "unknown_op"
	CodeBadField            = "bad_field"
	CodeDimMismatch         = "dim_mismatch"
	CodeNotStarted          = "not_started"
	CodeUnsupportedProtocol = "unsupported_protocol"
	CodeInternal            = "internal"
)

// 冻结的 op 名。SetSource 是 v2 新增的消息(§8.5); 在协议 1 里它是 unknown_op。
const (
	OpHello     = "hello"
	OpReset     = "reset"
	OpStep      = "step"
	OpSetSource = "set_source"
	OpClose     = "close"
)

// OpKey 是承载 op 的字段名。
const OpKey = "op"

// Options 是构建一个世界的全部输入。
//
// Protocol 决定语义(见包注释); MaxSteps/DeltaScale 是 v1 的截断与步长口径(协议 2 仍然
// 报它们, 但截断由 Budget 决定, §8.4); Source/Budget/Target 是 v2 的 regime 缺省值
// (§8.1: 三者也都可以由 reset 的 regime 字段逐个覆盖)。
//
// 留零表示"用 internal/config 的缺省值" —— 这是房规: 任何会被写进响应字节的数字只有
// 一个家, 协议层不许自己再发明一份 20 / 0.15 / 24 / 1.0。
type Options struct {
	Protocol   int
	MaxSteps   int
	DeltaScale float64
	Source     string
	Budget     int
	Target     float64
	Engine     string
}

// World 是一条协议会话背后的设计世界。
//
// protocol 1 走 rlenv.Env(与 P0 逐字节相同的路径); protocol 2 走 v2.go 的 episodeState。
// 两条路共用同一个 scorer, 因此"score 就是真目标函数"对两个协议同时成立 —— 这是
// docs/world-structure.md §1 对 A 的要求(不许拿整形过的 reward 当效果量)。
type World struct {
	spec       config.Spec
	engine     string
	kind       string // "" 或 "params" = 旧世界; "mu" = 交换子世界
	protocol   int
	maxSteps   int
	deltaScale float64
	source     string
	budget     int
	target     float64

	// muCfg/muEp 只有交换子世界用: 它的状态与目标函数与旧世界毫无共同之处, 所以不硬塞进
	// ep/env 那些字段里(那些字段的语义是"设计参数世界")。isMu() 是分流开关。
	muCfg muConf
	muEp  *muEpisode

	// obs 是 scorer 的接缝: rlenv.Reset 只返回观测, 不返回 reset 那个 design 的
	// score / feasible / design_id, 而契约 §3.2 要求 reset 的 info 与 step 的 info
	// 用同一批来源。为同一个 design 再求一次值会拿另一个 design_id(并写第二条
	// record), 把这次 episode 的 lineage 弄断; 因此在 scorer 上观察才是诚实的做法。
	obs *observedScorer

	// env 是协议 1 的转移(冻结语义), ep 是协议 2 的转移(§8)。
	env *rlenv.Env
	ep  *episodeState

	started bool

	// gridR/gridZ 是打分点(physics.BuildGrids 的堆叠采样点 = solver 真正求值的那些点),
	// v2 的区判定(近导线 5 mm 的 alpha2 钳位)用它。
	gridR []float64
	gridZ []float64
}

// observedScorer 记录求值器最后交给世界的 EvalResult。
//
// 它不做任何修改: meta 原样透传, 因此落进 registry 的 record、design_id 序列与直接
// 用 scorer 时逐字节相同(与 rlenv 自己的 policyScorer 用的是同一个接缝)。
type observedScorer struct {
	sc   runner.Scorer
	last objective.EvalResult
}

func (o *observedScorer) Score(x []float64, meta runner.Meta) objective.EvalResult {
	res := o.sc.Score(x, meta)
	o.last = res
	return res
}

// New 构建一个世界。engine 是写进 hello 的引擎版本串(由 CLI 注入: 协议层不猜版本)。
//
// Protocol 留零时用 ProtocolVersion(v2)。
func New(scorer runner.Scorer, spec config.Spec, opts Options) *World {
	if scorer == nil {
		panic("world: New needs a runner.Scorer — refusing to build a world that cannot score")
	}
	if opts.Protocol == 0 {
		opts.Protocol = ProtocolVersion
	}
	if !ProtocolSupported(opts.Protocol) {
		panic(fmt.Sprintf("world: unknown protocol %d (this build serves %d, %d and %d)",
			opts.Protocol, ProtocolV1, ProtocolV2, ProtocolV3))
	}
	if opts.Protocol == ProtocolV3 {
		// 协议 3 是**另一条世界**(交换子世界)的语义: 把它安在参数世界上会造出一个
		// "报 μ 世界观测、用的是设计参数状态"的东西 —— 那正是本仓最反对的假接口。
		panic("world: protocol 3 belongs to the mu world; build it with NewMuWorld")
	}
	if opts.MaxSteps <= 0 {
		opts.MaxSteps = config.DefaultMaxSteps
	}
	if opts.DeltaScale <= 0 {
		opts.DeltaScale = config.DefaultDeltaScale
	}
	if opts.Source == "" {
		opts.Source = DefaultSource
	}
	if !isV2Source(opts.Source) {
		panic(fmt.Sprintf("world: unknown source %q (known: %s)", opts.Source, joinQuoted(V2Sources)))
	}
	if opts.Budget <= 0 {
		opts.Budget = config.DefaultBudget
	}
	if opts.Target == 0 {
		opts.Target = config.DefaultTarget
	}
	w := &World{
		spec:       spec,
		engine:     opts.Engine,
		protocol:   opts.Protocol,
		maxSteps:   opts.MaxSteps,
		deltaScale: opts.DeltaScale,
		source:     opts.Source,
		budget:     opts.Budget,
		target:     opts.Target,
	}
	w.obs = &observedScorer{sc: scorer}
	w.env = rlenv.NewEnv(w.obs, spec, opts.MaxSteps, opts.DeltaScale)

	grids := physics.BuildGrids(spec)
	w.gridR, w.gridZ = grids.StackR, grids.StackZ
	return w
}

// Spec 返回本世界正在服务的 spec。
func (w *World) Spec() config.Spec { return w.spec }

// Protocol 是本世界按哪个协议语义服务(Server 与回放都用它, 见 §8.1)。
func (w *World) Protocol() int { return w.protocol }

// MaxSteps / DeltaScale 是**实际生效**的值(hello 报的就是它们, 不是 flag 的原文)。
func (w *World) MaxSteps() int       { return w.env.MaxSteps }
func (w *World) DeltaScale() float64 { return w.env.DeltaScale }

// Budget / Target / Source 是 v2 实际生效的 regime 缺省值(hello 报的就是它们)。
func (w *World) Budget() int       { return w.budget }
func (w *World) Target() float64   { return w.target }
func (w *World) Source() string    { return w.source }
func (w *World) Sources() []string { return append([]string(nil), V2Sources...) }

// Handle 把一行请求变成一行响应(不含换行)。
//
// closed 表示这是 close(调用方随后以 0 退出); fatal 表示世界内部出错 —— 那时响应仍是
// 一个合法的 ok:false(码 internal), 但调用方**必须非零退出**(契约 §3.5): 一个已经
// 无法诚实服务的世界继续跑下去, 只会把错误变成客户端的静默损坏。
func (w *World) Handle(line []byte) (resp []byte, closed, fatal bool) {
	defer func() {
		if r := recover(); r != nil {
			resp = errLine(CodeInternal, fmt.Sprintf("world internal error: %v", r))
			closed, fatal = false, true
		}
	}()

	top, code, msg := decodeRequest(line)
	if code != "" {
		return errLine(code, msg), false, false
	}
	opRaw, ok := top[OpKey]
	if !ok {
		return errLine(CodeBadField, "missing field: op"), false, false
	}
	var op string
	if err := json.Unmarshal(opRaw, &op); err != nil {
		return errLine(CodeBadField, fmt.Sprintf("field op must be a string, got %s", clipLine(opRaw))), false, false
	}

	switch op {
	case OpHello:
		return w.hello(top), false, false
	case OpReset:
		return w.reset(top), false, false
	case OpStep:
		return w.step(top), false, false
	case OpSetSource:
		// set_source 是 v2 的消息(§8.5)。协议 1 不认识它 —— 那是 unknown_op,
		// 而不是"顺手也支持一下": 一个 v1 会话里出现换源, 说明客户端以为自己在
		// 跟另一个世界说话。
		if w.protocol != ProtocolV2 {
			return errLine(CodeUnknownOp, fmt.Sprintf("unknown op %q (protocol %d knows: %s)",
				op, w.protocol, w.knownOps())), false, false
		}
		return w.setSourceV2(top), false, false
	case OpClose:
		if code, msg := rejectUnknown(top, OpKey); code != "" {
			return errLine(code, msg), false, false
		}
		return []byte(obj(kv{"ok", boolean(true)})), true, false
	default:
		return errLine(CodeUnknownOp, fmt.Sprintf("unknown op %q (known: %s, %s, %s, %s)",
			op, OpHello, OpReset, OpStep, OpClose)), false, false
	}
}

// knownOps 是本协议版本认识的 op 列表(错误消息里报出来, 且**逐字节稳定**:
// 消息是响应字节的一部分, 同一行畸形请求必须永远得到同一行响应)。
func (w *World) knownOps() string {
	if w.protocol == ProtocolV2 {
		return fmt.Sprintf("%s, %s, %s, %s, %s", OpHello, OpReset, OpStep, OpSetSource, OpClose)
	}
	return fmt.Sprintf("%s, %s, %s, %s", OpHello, OpReset, OpStep, OpClose)
}

// hello 处理握手。客户端可以在 hello 里声明它要的协议版本; 声明了本世界不服务的版本时,
// 世界立刻以 unsupported_protocol 大声拒绝, 而不是按自己的版本继续跑(那会让两边对
// 同一行字节的理解不同)。
func (w *World) hello(top map[string]json.RawMessage) []byte {
	if code, msg := rejectUnknown(top, OpKey, "protocol"); code != "" {
		return errLine(code, msg)
	}
	if rawProto, ok := top["protocol"]; ok {
		var proto int
		if err := json.Unmarshal(rawProto, &proto); err != nil {
			return errLine(CodeBadField, fmt.Sprintf("field protocol must be an integer, got %s", clipLine(rawProto)))
		}
		if proto != w.protocol {
			return errLine(CodeUnsupportedProtocol,
				fmt.Sprintf("client asked for protocol %d, this world speaks %d", proto, w.protocol))
		}
	}
	if w.protocol == ProtocolV3 {
		return w.helloV3()
	}
	if w.protocol == ProtocolV2 {
		return w.helloV2()
	}
	return []byte(obj(
		kv{"ok", boolean(true)},
		kv{"protocol", itg(ProtocolV1)},
		kv{"action_dim", itg(w.env.ActionDim())},
		kv{"observation_dim", itg(w.env.ObservationDim())},
		kv{"obs_metric_keys", strArr(rlenv.ObsMetricKeys)},
		kv{"obs_metric_refs", rawArr(rlenv.ObsMetricRefs)},
		kv{"max_steps", itg(w.env.MaxSteps)},
		kv{"delta_scale", flt(w.env.DeltaScale)},
		kv{"spec", specJSON(w.spec)},
		kv{"engine", obj(kv{"version", str(w.engine)})},
	))
}

// reset 开始一条新 episode。重复 reset 是合法的。
//
//	协议 1: x0 优先于 seed; 先夹进 spec 盒子再求值(与 rlenv.Env.Reset 一致);
//	协议 2: 多一个可选的 regime 对象(§8.3), 见 resetV2。
func (w *World) reset(top map[string]json.RawMessage) []byte {
	if w.protocol == ProtocolV3 {
		return w.resetMu(top)
	}
	if w.protocol == ProtocolV2 {
		return w.resetV2(top)
	}
	if code, msg := rejectUnknown(top, OpKey, "x0", "seed"); code != "" {
		return errLine(code, msg)
	}
	x0Raw, hasX0 := top["x0"]
	seedRaw, hasSeed := top["seed"]
	if !hasX0 && !hasSeed {
		return errLine(CodeBadField, "reset needs x0 or seed: both are missing")
	}

	var x0 []float64
	if hasX0 {
		vals, code, msg := decodeVector(x0Raw, "x0")
		if code != "" {
			return errLine(code, msg)
		}
		if len(vals) != w.env.ActionDim() {
			return errLine(CodeDimMismatch,
				fmt.Sprintf("x0 has %d entries, action_dim is %d", len(vals), w.env.ActionDim()))
		}
		x0 = vals
	} else {
		var seed int64
		if err := json.Unmarshal(seedRaw, &seed); err != nil {
			return errLine(CodeBadField, fmt.Sprintf("field seed must be an integer, got %s", clipLine(seedRaw)))
		}
		x0 = physics.RandomDesign(rand.New(rand.NewSource(seed)), w.spec)
	}

	obs := w.env.Reset(x0)
	w.started = true

	// reset 的 info 来自刚刚被 rlenv 吸收的那次求值(见 observedScorer): score 是
	// reset 那个 design 的分数, delta_score 按定义为 0, step 为 0。
	res := w.obs.last
	return []byte(obj(
		kv{"ok", boolean(true)},
		kv{"obs", rawArr(obs)},
		kv{"info", infoJSON(rlenv.Info{
			Score:      res.Score,
			DeltaScore: 0,
			Feasible:   res.Feasible,
			DesignID:   res.DesignID,
			Step:       0,
		})},
	))
}

// step 施加一个归一化增量动作。协议 1 的语义 = rlenv.Env.Step, 本层不加任何东西:
//
//	dx[i] = clip(a[i],-1,1) * DeltaScale * (upper[i]-lower[i]), 结果再夹进盒子;
//	reward = score(之后) − score(之前); terminated 恒为 false; truncated = (step >= max_steps);
//	已结束之后再 step 不是错误: rlenv 回 terminated=true, truncated=true, reward=0, 且
//	不再消耗求值。本层原样转发, 好让客户端能区分"又走了一步"和"世界已经结束了"。
func (w *World) step(top map[string]json.RawMessage) []byte {
	if w.protocol == ProtocolV3 {
		return w.stepMu(top)
	}
	if w.protocol == ProtocolV2 {
		return w.stepV2(top)
	}
	if code, msg := rejectUnknown(top, OpKey, "action"); code != "" {
		return errLine(code, msg)
	}
	actionRaw, ok := top["action"]
	if !ok {
		return errLine(CodeBadField, "missing field: action")
	}
	if !w.started {
		// 未 reset 就 step 是错误, 不是空操作: 一个没有当前 design 的世界给出的
		// 每一步都只能是编造的。
		return errLine(CodeNotStarted, "step before reset: this world has no current design")
	}
	action, code, msg := decodeVector(actionRaw, "action")
	if code != "" {
		return errLine(code, msg)
	}
	if len(action) != w.env.ActionDim() {
		return errLine(CodeDimMismatch,
			fmt.Sprintf("action has %d entries, action_dim is %d", len(action), w.env.ActionDim()))
	}

	obs, reward, terminated, truncated, info := w.env.Step(action)
	return []byte(obj(
		kv{"ok", boolean(true)},
		kv{"obs", rawArr(obs)},
		kv{"reward", flt(reward)},
		kv{"terminated", boolean(terminated)},
		kv{"truncated", boolean(truncated)},
		kv{"info", infoJSON(info)},
	))
}

// infoJSON 渲染冻结的 info 字段(契约 §4): 键名与 rlenv.Info 的 JSON tag 逐字相同,
// 不增删、不改名、不把 delta_score 与 score 混用。
func infoJSON(info rlenv.Info) raw {
	return obj(
		kv{"score", flt(info.Score)},
		kv{"delta_score", flt(info.DeltaScore)},
		kv{"feasible", boolean(info.Feasible)},
		kv{"design_id", str(info.DesignID)},
		kv{"step", itg(info.Step)},
	)
}

// errLine 渲染冻结的错误响应(契约 §3.5)。
func errLine(code, message string) []byte {
	return []byte(obj(
		kv{"ok", boolean(false)},
		kv{"error", obj(kv{"code", str(code)}, kv{"message", str(message)})},
	))
}

// decodeRequest 把一行文本解成一个 JSON 对象。非对象(数组、标量、null、语法错)一律
// bad_json: 客户端发来的东西不是本协议定义的消息。
func decodeRequest(line []byte) (map[string]json.RawMessage, string, string) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(line, &top); err != nil {
		return nil, CodeBadJSON, fmt.Sprintf("request line is not a JSON object: %v", err)
	}
	if top == nil {
		return nil, CodeBadJSON, "request line is not a JSON object (got null)"
	}
	return top, "", ""
}

// decodeVector 解一个浮点向量字段。null 是类型错, 不是"空向量": 契约要求缺字段/类型错
// 大声失败。
func decodeVector(rawVal json.RawMessage, field string) ([]float64, string, string) {
	var out []float64
	if err := json.Unmarshal(rawVal, &out); err != nil {
		return nil, CodeBadField, fmt.Sprintf("field %s must be an array of numbers: %v", field, err)
	}
	if out == nil {
		return nil, CodeBadField, fmt.Sprintf("field %s must be an array of numbers (got null)", field)
	}
	return out, "", ""
}

// decodeObject 解一个嵌套对象字段(v2 的 regime)。
func decodeObject(rawVal json.RawMessage, field string) (map[string]json.RawMessage, string, string) {
	var out map[string]json.RawMessage
	if err := json.Unmarshal(rawVal, &out); err != nil {
		return nil, CodeBadField, fmt.Sprintf("field %s must be a JSON object: %v", field, err)
	}
	if out == nil {
		return nil, CodeBadField, fmt.Sprintf("field %s must be a JSON object (got null)", field)
	}
	return out, "", ""
}

// decodeString 解一个字符串字段。
func decodeString(rawVal json.RawMessage, field string) (string, string, string) {
	var out string
	if err := json.Unmarshal(rawVal, &out); err != nil {
		return "", CodeBadField, fmt.Sprintf("field %s must be a string, got %s", field, clipLine(rawVal))
	}
	return out, "", ""
}

// decodeInt 解一个整数字段。1.5 与 "3" 都是类型错(契约只接受整数)。
func decodeInt(rawVal json.RawMessage, field string) (int, string, string) {
	var out int
	if err := json.Unmarshal(rawVal, &out); err != nil {
		return 0, CodeBadField, fmt.Sprintf("field %s must be an integer, got %s", field, clipLine(rawVal))
	}
	return out, "", ""
}

// decodeFloat 解一个浮点字段。
func decodeFloat(rawVal json.RawMessage, field string) (float64, string, string) {
	var out float64
	if err := json.Unmarshal(rawVal, &out); err != nil {
		return 0, CodeBadField, fmt.Sprintf("field %s must be a number, got %s", field, clipLine(rawVal))
	}
	return out, "", ""
}

// --- 小辅助函数(v1 与 v2 共用) ---

// clip 把 v 夹进 [lo, hi]。
func clip(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// copyOf 复制一个向量(状态不许与调用方的切片共享内存)。
func copyOf(x []float64) []float64 {
	out := make([]float64, len(x))
	copy(out, x)
	return out
}

// isFinite 报告 v 是不是一个有限数(非有限的数不许被写进响应: 见 json.go 的 flt)。
func isFinite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// rejectUnknown 拒绝协议未定义的字段。
//
// 这条规则是"宁可大声失败"的直接推论: 未知字段一定来自一个本世界不理解的客户端(或
// 一个拼错的键), 静默忽略它就等于让客户端相信一个世界没有接受的设置生效了。消息里的
// 键名排序, 好让同一个畸形请求永远得到同一行响应。
func rejectUnknown(top map[string]json.RawMessage, allowed ...string) (string, string) {
	var unknown []string
	for k := range top {
		known := false
		for _, a := range allowed {
			if k == a {
				known = true
				break
			}
		}
		if !known {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) == 0 {
		return "", ""
	}
	sort.Strings(unknown)
	return CodeBadField, fmt.Sprintf("unknown field(s) %s (this protocol defines: %s)",
		strconv.Quote(joinQuoted(unknown)), joinQuoted(allowed))
}

func joinQuoted(keys []string) string {
	out := ""
	for i, k := range keys {
		if i > 0 {
			out += " "
		}
		out += strconv.Quote(k)
	}
	return out
}

// clipLine 把一段原始报文截短, 好让错误消息能安全地贴进 JSON 响应。
func clipLine(b []byte) string {
	const max = 80
	s := string(b)
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}
