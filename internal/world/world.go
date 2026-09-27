// Package world: 把 internal/rlenv 的语义原样搬到进程边界之外。
//
// 冻结契约: docs/world-protocol.md (protocol v1)。本包**不定义物理、不新增 shaping、
// 不含学习** —— 语义的唯一权威是 internal/rlenv/api.go; 协议层只做搬运与校验。
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
	"math/rand"
	"sort"
	"strconv"

	"github.com/logos-42/hushfusion-forge/internal/config"
	"github.com/logos-42/hushfusion-forge/internal/objective"
	"github.com/logos-42/hushfusion-forge/internal/physics"
	"github.com/logos-42/hushfusion-forge/internal/rlenv"
	"github.com/logos-42/hushfusion-forge/internal/runner"
)

// ProtocolVersion 是本版本唯一已知的协议版本, 也是 `forge world serve --protocol` 的
// 默认值。
const ProtocolVersion = 1

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

// 冻结的 op 名。
const (
	OpHello = "hello"
	OpReset = "reset"
	OpStep  = "step"
	OpClose = "close"
)

// World 是一条协议会话背后的设计世界。
//
// 它自己保存的只有协议层状态("有没有 reset 过"); 观测布局、动作缩放、奖励定义、
// 终止条件与裁剪顺序全部由 rlenv 决定, 本层一个都不复制。
type World struct {
	spec       config.Spec
	engine     string
	maxSteps   int
	deltaScale float64

	// obs 是 scorer 的接缝: rlenv.Reset 只返回观测, 不返回 reset 那个 design 的
	// score / feasible / design_id, 而契约 §3.2 要求 reset 的 info 与 step 的 info
	// 用同一批来源。为同一个 design 再求一次值会拿另一个 design_id(并写第二条
	// record), 把这次 episode 的 lineage 弄断; 因此在 scorer 上观察才是诚实的做法。
	obs *observedScorer
	env *rlenv.Env

	started bool
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
// maxSteps/deltaScale 留零时用 internal/config 的默认值 —— 这是房规: 任何影响分数的
// 数字只有一个家, 协议层不许自己再发明一份 20 / 0.15。
func New(scorer runner.Scorer, spec config.Spec, maxSteps int, deltaScale float64, engine string) *World {
	if scorer == nil {
		panic("world: New needs a runner.Scorer — refusing to build a world that cannot score")
	}
	if maxSteps <= 0 {
		maxSteps = config.DefaultMaxSteps
	}
	if deltaScale <= 0 {
		deltaScale = config.DefaultDeltaScale
	}
	w := &World{
		spec:       spec,
		engine:     engine,
		maxSteps:   maxSteps,
		deltaScale: deltaScale,
	}
	w.obs = &observedScorer{sc: scorer}
	w.env = rlenv.NewEnv(w.obs, spec, maxSteps, deltaScale)
	return w
}

// Spec 返回本世界正在服务的 spec。
func (w *World) Spec() config.Spec { return w.spec }

// MaxSteps / DeltaScale 是**实际生效**的值(hello 报的就是它们, 不是 flag 的原文)。
func (w *World) MaxSteps() int       { return w.env.MaxSteps }
func (w *World) DeltaScale() float64 { return w.env.DeltaScale }

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

// OpKey 是承载 op 的字段名。
const OpKey = "op"

// hello 处理握手。客户端可以在 hello 里声明它要的协议版本; 声明了不支持的版本时,
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
		if proto != ProtocolVersion {
			return errLine(CodeUnsupportedProtocol,
				fmt.Sprintf("client asked for protocol %d, this world speaks %d", proto, ProtocolVersion))
		}
	}
	return []byte(obj(
		kv{"ok", boolean(true)},
		kv{"protocol", itg(ProtocolVersion)},
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
//	x0 显式给出 → 先夹进 spec 盒子再求值(与 rlenv.Env.Reset 一致);
//	seed 给出    → 用**显式播种**的生成器在盒子内均匀取点, 绝不碰进程全局随机源
//	               (契约 §2: 未播种的随机源会让"逐字节复现"直接不成立)。
//
// 两个字段都给时以 x0 为准(冻结的错误码只覆盖"两个都缺")。
func (w *World) reset(top map[string]json.RawMessage) []byte {
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

// step 施加一个归一化增量动作。语义 = rlenv.Env.Step, 本层不加任何东西:
//
//	dx[i] = clip(a[i],-1,1) * DeltaScale * (upper[i]-lower[i]), 结果再夹进盒子;
//	reward = score(之后) − score(之前); terminated 恒为 false; truncated = (step >= max_steps);
//	已结束之后再 step 不是错误: rlenv 回 terminated=true, truncated=true, reward=0, 且
//	不再消耗求值。本层原样转发, 好让客户端能区分"又走了一步"和"世界已经结束了"。
func (w *World) step(top map[string]json.RawMessage) []byte {
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
