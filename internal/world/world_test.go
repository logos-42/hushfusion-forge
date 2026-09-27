// world_test.go — 协议层的门。
//
// 这些测试刻意不碰 physics: 它们注入一个玩具 scorer, 只验协议层自己的判据 ——
// 诚实失败路径、冻结的错误码、逐字节的 trace 格式、最短往返的浮点、以及
// "done 之后再 step" 的语义。跨语言的证据在 G18(Go 回放 + Python 客户端回放)。
package world

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/logos-42/hushfusion-forge/internal/config"
	"github.com/logos-42/hushfusion-forge/internal/objective"
	"github.com/logos-42/hushfusion-forge/internal/rlenv"
	"github.com/logos-42/hushfusion-forge/internal/runner"
)

// ---------------------------------------------------------------------------
// 测试替身
// ---------------------------------------------------------------------------

// toyScorer 是一个确定性的假求值器: score 就是"第几次求值", 因此每一步的
// Δscore 恰好是 1, design_id 是 D0001, D0002, ...
type toyScorer struct {
	nEvals int
	panic  bool
}

func (t *toyScorer) Score(x []float64, meta runner.Meta) objective.EvalResult {
	if t.panic {
		panic("toyScorer: deliberate explosion")
	}
	t.nEvals++
	design := make([]float64, len(x))
	copy(design, x)
	return objective.EvalResult{
		Score:    float64(t.nEvals),
		Feasible: true,
		Design:   design,
		DesignID: fmt.Sprintf("D%04d", t.nEvals),
	}
}

func testSpec() config.Spec { return config.DefaultSpec() }

func newTestWorld(t *testing.T, sc runner.Scorer, maxSteps int) *World {
	t.Helper()
	// 这些用例盯着 **协议 1** 的语义(冻结的 v1 行为、19 维观测、v1 黄金 trace 的回放),
	// 所以这里显式构建一个 v1 世界。协议 2 的用例在 v2_test.go, 用 NewV2TestWorld。
	return New(sc, testSpec(), Options{
		Protocol:   ProtocolV1,
		MaxSteps:   maxSteps,
		DeltaScale: config.DefaultDeltaScale,
		Engine:     "test-engine",
	})
}

// ---------------------------------------------------------------------------
// 小工具
// ---------------------------------------------------------------------------

func mustJSON(t *testing.T, line []byte) map[string]any {
	t.Helper()
	var obj map[string]any
	if err := json.Unmarshal(line, &obj); err != nil {
		t.Fatalf("response is not a JSON object: %v\n%s", err, line)
	}
	return obj
}

// errCode 返回错误响应里的码; 响应是 ok:true 时返回 ""。
func errCode(t *testing.T, resp []byte) string {
	t.Helper()
	obj := mustJSON(t, resp)
	if ok, _ := obj["ok"].(bool); ok {
		return ""
	}
	errObj, _ := obj["error"].(map[string]any)
	code, _ := errObj["code"].(string)
	if code == "" {
		t.Fatalf("an ok:false response without a frozen error code: %s", resp)
	}
	if msg, _ := errObj["message"].(string); msg == "" {
		t.Errorf("error %q has an empty message: %s", code, resp)
	}
	return code
}

// handle1 把 Handle 的第二个/第三个返回值丢掉: 大多数用例只关心响应本身。
func handle1(t *testing.T, w *World, line string) []byte {
	t.Helper()
	resp, _, _ := w.Handle([]byte(line))
	return resp
}

// stepAction 造一个全零动作(长度 = action_dim)。
func zeroAction(spec config.Spec) []float64 { return make([]float64, spec.NParams()) }

// ---------------------------------------------------------------------------
// 默认值: 房规 —— 影响分数的数字只有一个家
// ---------------------------------------------------------------------------

// TestDefaultsComeFromConfig 钉住两件事: 本包的默认值来自 internal/config(协议层不许
// 自己发明一份 20 / 0.15), 以及 internal/rlenv 在留零时确实取自 config —— 它已经直接
// 引用那两个常量, 所以这条断言盯的是**接线**(零值是否走到 config), 而不是两个数字
// 碰巧相等。
func TestDefaultsComeFromConfig(t *testing.T) {
	w := New(&toyScorer{}, testSpec(), Options{Engine: "test-engine"})
	if w.MaxSteps() != config.DefaultMaxSteps {
		t.Errorf("max_steps default = %d, want config.DefaultMaxSteps = %d", w.MaxSteps(), config.DefaultMaxSteps)
	}
	if w.DeltaScale() != config.DefaultDeltaScale {
		t.Errorf("delta_scale default = %v, want config.DefaultDeltaScale = %v", w.DeltaScale(), config.DefaultDeltaScale)
	}

	// v2 的三个 regime 缺省值同理: 零值必须走到 config(协议层不许自己有第二份 24/1.0)。
	if w.Budget() != config.DefaultBudget {
		t.Errorf("budget default = %d, want config.DefaultBudget = %d", w.Budget(), config.DefaultBudget)
	}
	if w.Target() != config.DefaultTarget {
		t.Errorf("target default = %v, want config.DefaultTarget = %v", w.Target(), config.DefaultTarget)
	}
	if w.Protocol() != ProtocolVersion {
		t.Errorf("protocol default = %d, want ProtocolVersion = %d", w.Protocol(), ProtocolVersion)
	}
	if ProtocolVersion != ProtocolV2 {
		t.Errorf("ProtocolVersion = %d, want %d: the interactive default is protocol 2 (protocol section 8.1)",
			ProtocolVersion, ProtocolV2)
	}

	// rlenv 用零值表示"用默认": 它的默认值必须与 config 逐位相同。
	rlenvDefault := rlenv.NewEnv(&toyScorer{}, testSpec(), 0, 0)
	if rlenvDefault.MaxSteps != config.DefaultMaxSteps {
		t.Errorf("rlenv defaultMaxSteps = %d but config.DefaultMaxSteps = %d — rlenv must take its default from config",
			rlenvDefault.MaxSteps, config.DefaultMaxSteps)
	}
	if rlenvDefault.DeltaScale != config.DefaultDeltaScale {
		t.Errorf("rlenv defaultDeltaScale = %v but config.DefaultDeltaScale = %v — rlenv must take its default from config",
			rlenvDefault.DeltaScale, config.DefaultDeltaScale)
	}
}

// ---------------------------------------------------------------------------
// 握手
// ---------------------------------------------------------------------------

// TestHelloHashIsStableAndPinned 钉住 spec.sha256 的两件事:
//
//  1. 它是"spec 的规范 JSON(键排序 + 最短往返浮点)"的 sha256 —— 这里把那段规范 JSON
//     的**字面量**写死在测试里, 因此任何一次键序、分隔符或浮点格式的漂移都会变红;
//  2. 同一份 spec 每次都得到同一个串。
//
// 客户端(Python)复算的就是同一段字节: 见 python/aux/world_client.py 的
// canonical_spec_json。这条哈希是跨语言握手自校验的锚点。
func TestHelloHashIsStableAndPinned(t *testing.T) {
	const canonical = `{"lower":[0.1,0.1,0.1,0.1,-1.2,-1.2,-1.2,-1.2,10000,10000,10000,10000],` +
		`"n_coils":4,"n_params":12,` +
		`"upper":[1,1,1,1,1.2,1.2,1.2,1.2,2.5e+06,2.5e+06,2.5e+06,2.5e+06]}`
	const digest = "7cbf60e8eae081781bd7bdd2f204f57805049b7e1a27139608aa99a94179e2b3"

	spec := testSpec()
	if got := SpecCanonicalJSON(spec); got != canonical {
		t.Errorf("canonical spec JSON drifted:\n got %s\nwant %s", got, canonical)
	}
	sum := sha256.Sum256([]byte(canonical))
	if got := hex.EncodeToString(sum[:]); got != digest {
		t.Fatalf("the pinned digest does not match its own input: %s", got)
	}
	if got := SpecSHA256(spec); got != digest {
		t.Errorf("SpecSHA256 = %s, want %s", got, digest)
	}

	// hello 里带的必须是同一个串, 且可重复。
	w := newTestWorld(t, &toyScorer{}, 4)
	firstRaw, _, _ := w.Handle([]byte(`{"op":"hello"}`))
	secondRaw, _, _ := w.Handle([]byte(`{"op":"hello"}`))
	first := mustJSON(t, firstRaw)
	specObj, _ := first["spec"].(map[string]any)
	if specObj == nil {
		t.Fatalf("hello has no spec object: %v", first)
	}
	if got, _ := specObj["sha256"].(string); got != digest {
		t.Errorf("hello spec.sha256 = %s, want %s", got, digest)
	}
	if string(firstRaw) != string(secondRaw) {
		t.Errorf("hello is not idempotent:\n%s\n%s", firstRaw, secondRaw)
	}

	// 一个不同的 spec 必须给出不同的哈希(否则这个哈希没有在描述 spec)。
	other := testSpec()
	other.NCoils = 3
	if SpecSHA256(other) == digest {
		t.Error("a 3-coil spec hashed to the 4-coil digest")
	}
}

// TestHelloDescribesTheEnvironment 检查握手把世界的真实形状报了出来, 且客户端做
// 自校验所需的每一个字段都在: obs_metric_keys/refs 成对, observation_dim 与它们一致。
func TestHelloDescribesTheEnvironment(t *testing.T) {
	w := newTestWorld(t, &toyScorer{}, 7)
	hello, _, _ := w.Handle([]byte(`{"op":"hello"}`))
	obj := mustJSON(t, hello)

	for _, key := range []string{"ok", "protocol", "action_dim", "observation_dim", "obs_metric_keys",
		"obs_metric_refs", "max_steps", "delta_scale", "spec", "engine"} {
		if _, ok := obj[key]; !ok {
			t.Errorf("hello is missing %q", key)
		}
	}
	if got := obj["protocol"].(float64); got != float64(ProtocolV1) {
		t.Errorf("protocol = %v, want %v", got, ProtocolV1)
	}
	if got := obj["max_steps"].(float64); got != 7 {
		t.Errorf("max_steps = %v, want 7 (the value this world was built with)", got)
	}
	actionDim := obj["action_dim"].(float64)
	obsDim := obj["observation_dim"].(float64)
	keys := obj["obs_metric_keys"].([]any)
	refs := obj["obs_metric_refs"].([]any)
	if len(keys) != len(refs) {
		t.Errorf("%d metric keys vs %d refs", len(keys), len(refs))
	}
	if obsDim != actionDim+float64(len(keys)) {
		t.Errorf("observation_dim = %v, want action_dim(%v) + %d", obsDim, actionDim, len(keys))
	}
	if actionDim != float64(testSpec().NParams()) {
		t.Errorf("action_dim = %v, want NParams = %d", actionDim, testSpec().NParams())
	}
	for i, r := range refs {
		if r.(float64) == 0 {
			t.Errorf("obs_metric_refs[%d] is 0 — the client would divide by it", i)
		}
	}
}

// ---------------------------------------------------------------------------
// 诚实失败路径(契约 §3.5)
// ---------------------------------------------------------------------------

// TestHonestFailures 逐条覆盖冻结的错误码。每一条都是"世界无法给出诚实答案"的时刻:
// 宁可回一个错误码, 也不许回 0.0 / 空观测 / 默认值。
func TestHonestFailures(t *testing.T) {
	dim := testSpec().NParams()

	cases := []struct {
		name string
		pre  []string // 先跑这些请求(合法的), 用来把世界推进到某个状态
		line string
		want string
	}{
		{name: "not a JSON object", line: "this is not json", want: CodeBadJSON},
		{name: "JSON array", line: `[{"op":"hello"}]`, want: CodeBadJSON},
		{name: "empty line", line: "", want: CodeBadJSON},
		{name: "missing op", line: `{}`, want: CodeBadField},
		{name: "op is not a string", line: `{"op":7}`, want: CodeBadField},
		{name: "unknown op", line: `{"op":"observe"}`, want: CodeUnknownOp},
		{name: "unknown field", line: `{"op":"hello","n_steps":3}`, want: CodeBadField},
		{name: "hello declares another protocol", line: `{"op":"hello","protocol":2}`, want: CodeUnsupportedProtocol},
		{name: "hello with a non-integer protocol", line: `{"op":"hello","protocol":"1"}`, want: CodeBadField},
		{name: "step before reset", line: stepReq(dim), want: CodeNotStarted},
		{name: "reset without x0 and seed", line: `{"op":"reset"}`, want: CodeBadField},
		{name: "reset with a short x0", line: `{"op":"reset","x0":[1,2]}`, want: CodeDimMismatch},
		{name: "reset with a null x0", line: `{"op":"reset","x0":null}`, want: CodeBadField},
		{name: "reset with a non-numeric x0", line: `{"op":"reset","x0":["a"]}`, want: CodeBadField},
		{name: "reset with a non-integer seed", line: `{"op":"reset","seed":1.5}`, want: CodeBadField},
		{
			name: "step without action", pre: []string{`{"op":"reset","seed":1}`},
			line: `{"op":"step"}`, want: CodeBadField,
		},
		{
			name: "step with a short action", pre: []string{`{"op":"reset","seed":1}`},
			line: `{"op":"step","action":[1,2]}`, want: CodeDimMismatch,
		},
		{
			name: "step with a null action", pre: []string{`{"op":"reset","seed":1}`},
			line: `{"op":"step","action":null}`, want: CodeBadField,
		},
		{
			name: "close with an extra field", pre: []string{`{"op":"reset","seed":1}`},
			line: `{"op":"close","now":true}`, want: CodeBadField,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newTestWorld(t, &toyScorer{}, 4)
			for _, pre := range c.pre {
				if code := errCode(t, handle1(t, w, pre)); code != "" {
					t.Fatalf("setup request %s failed with %s", pre, code)
				}
			}
			resp, closed, fatal := w.Handle([]byte(c.line))
			if got := errCode(t, resp); got != c.want {
				t.Fatalf("line %q → code %q, want %q", c.line, got, c.want)
			}
			if closed {
				t.Errorf("an error must not close the session (line %q)", c.line)
			}
			if fatal {
				t.Errorf("a bad request is the client's error, not a fatal world error (line %q)", c.line)
			}
			// 一条坏请求不许推动世界状态: 修好之后 reset 依然要能用。
			if code := errCode(t, handle1(t, w, `{"op":"reset","seed":2}`)); code != "" {
				t.Errorf("the world no longer accepts a valid reset after %q: %s", c.line, code)
			}
		})
	}
}

// stepReq 造一个长度正确的 step 请求。
func stepReq(dim int) string {
	action := make([]string, dim)
	for i := range action {
		action[i] = "0"
	}
	return `{"op":"step","action":[` + strings.Join(action, ",") + `]}`
}

// TestInternalPanicBecomesAFatalError 覆盖 internal: 世界内部 panic 必须变成一个错误
// 响应, 并且**必须**让调用方非零退出(契约 §3.5)。
func TestInternalPanicBecomesAFatalError(t *testing.T) {
	w := newTestWorld(t, &toyScorer{panic: true}, 4)
	resp, closed, fatal := w.Handle([]byte(`{"op":"reset","seed":1}`))
	if got := errCode(t, resp); got != CodeInternal {
		t.Fatalf("a panicking scorer produced code %q, want %q", got, CodeInternal)
	}
	if !fatal {
		t.Error("an internal panic must be fatal (the caller must exit non-zero)")
	}
	if closed {
		t.Error("an internal panic is not a close")
	}
}

// TestNonFiniteScorerIsRefused 是"绝不编造数字"的协议层那一半: 非有限的 score 不许被
// 写成一个 0.0 或 null; 它会让世界走 internal 路径并让进程非零退出。
func TestNonFiniteScorerIsRefused(t *testing.T) {
	w := newTestWorld(t, nanScorer{}, 4)
	resp, _, fatal := w.Handle([]byte(`{"op":"reset","seed":1}`))
	if got := errCode(t, resp); got != CodeInternal {
		t.Fatalf("a non-finite score produced code %q, want %q\n%s", got, CodeInternal, resp)
	}
	if !fatal {
		t.Error("a NaN score must be fatal, not a job for the next step")
	}
}

type nanScorer struct{}

func (nanScorer) Score(x []float64, meta runner.Meta) objective.EvalResult {
	return objective.EvalResult{Score: math.NaN(), Design: x, DesignID: "D0001"}
}

// ---------------------------------------------------------------------------
// step 的语义(契约 §3.3)
// ---------------------------------------------------------------------------

// TestStepSemanticsAndDoneAfterDone 把契约 §3.3 的三条语义一次钉住:
// reward = Δscore(没有 shaping)、terminated 恒 false、truncated = (step >= max_steps),
// 以及**已结束之后再 step**: 不是错误, 回 terminated=true/truncated=true/reward=0,
// 且**不再消耗求值**。
func TestStepSemanticsAndDoneAfterDone(t *testing.T) {
	sc := &toyScorer{}
	w := newTestWorld(t, sc, 2)

	resetResp := mustJSON(t, handle1(t, w, `{"op":"reset","x0":`+boxedDesign(t)+`}`))
	resetInfo, _ := resetResp["info"].(map[string]any)
	if resetInfo["step"].(float64) != 0 {
		t.Errorf("reset info.step = %v, want 0", resetInfo["step"])
	}
	if resetInfo["delta_score"].(float64) != 0 {
		t.Errorf("reset info.delta_score = %v, want 0", resetInfo["delta_score"])
	}
	if resetInfo["design_id"].(string) != "D0001" {
		t.Errorf("reset info.design_id = %v, want D0001", resetInfo["design_id"])
	}
	if sc.nEvals != 1 {
		t.Fatalf("reset spent %d evaluations, want 1", sc.nEvals)
	}

	action := zeroAction(testSpec())
	reward, prevStep := 0.0, 0.0
	for i := 1; i <= 2; i++ {
		obj := mustJSON(t, handle1(t, w, `{"op":"step","action":`+floatList(action)+`}`))
		info, _ := obj["info"].(map[string]any)
		if obj["terminated"].(bool) {
			t.Errorf("step %d reported terminated=true: the design space has no absorbing state", i)
		}
		if got, want := obj["truncated"].(bool), i == 2; got != want {
			t.Errorf("step %d truncated = %v, want %v (max_steps=2)", i, got, want)
		}
		reward = obj["reward"].(float64)
		if reward != info["delta_score"].(float64) {
			t.Errorf("step %d: reward %v != info.delta_score %v", i, reward, info["delta_score"])
		}
		if reward != 1 {
			t.Errorf("step %d: reward = %v, want 1 (the toy scorer is monotone; any shaping would show here)", i, reward)
		}
		stepNow := info["step"].(float64)
		if stepNow != prevStep+1 {
			t.Errorf("step %d: info.step = %v, want %v", i, stepNow, prevStep+1)
		}
		prevStep = stepNow
	}
	evalsAfterEpisode := sc.nEvals

	// 第 3 次 step: episode 已结束。
	// 注意这里直接把它当成一步(不是错误): 已结束之后再 step 是合法的, 只是不再推进。
	obj := mustJSON(t, handle1(t, w, `{"op":"step","action":`+floatList(action)+`}`))
	if !obj["terminated"].(bool) || !obj["truncated"].(bool) {
		t.Errorf("a step after done must report terminated=true and truncated=true, got %v", obj)
	}
	if obj["reward"].(float64) != 0 {
		t.Errorf("a step after done must reward 0, got %v", obj["reward"])
	}
	info, _ := obj["info"].(map[string]any)
	if info["delta_score"].(float64) != 0 {
		t.Errorf("a step after done must report delta_score 0, got %v", info["delta_score"])
	}
	if info["step"].(float64) != prevStep {
		t.Errorf("a step after done must not advance info.step: %v != %v", info["step"], prevStep)
	}
	if sc.nEvals != evalsAfterEpisode {
		t.Errorf("a step after done spent %d extra evaluations (want 0): it must not consume evaluation",
			sc.nEvals-evalsAfterEpisode)
	}

	// 结束之后再 reset 是合法的: 它开始一条新的 episode。
	resetResp = mustJSON(t, handle1(t, w, `{"op":"reset","x0":`+boxedDesign(t)+`}`))
	resetInfo, _ = resetResp["info"].(map[string]any)
	if resetInfo["step"].(float64) != 0 {
		t.Errorf("a fresh episode starts at step %v, want 0", resetInfo["step"])
	}
	if obj := mustJSON(t, handle1(t, w, `{"op":"step","action":`+floatList(action)+`}`)); obj["truncated"].(bool) {
		t.Error("the fresh episode is already truncated after one step")
	}
}

// TestResetWithSeedIsExplicitAndDeterministic 证明 seed 路径只用显式播种的生成器:
// 同一个 seed 在任何进程里都必须给出同一段观测, 不同 seed 给出不同起点。
func TestResetWithSeedIsExplicitAndDeterministic(t *testing.T) {
	a := newTestWorld(t, &toyScorer{}, 4)
	b := newTestWorld(t, &toyScorer{}, 4)
	c := newTestWorld(t, &toyScorer{}, 4)

	first, _, _ := a.Handle([]byte(`{"op":"reset","seed":12345}`))
	second, _, _ := b.Handle([]byte(`{"op":"reset","seed":12345}`))
	other, _, _ := c.Handle([]byte(`{"op":"reset","seed":12346}`))

	if string(first) != string(second) {
		t.Errorf("the same seed gave different observations:\n%s\n%s", first, second)
	}
	if string(first) == string(other) {
		t.Error("two different seeds gave the same start")
	}

	// 显式 x0 与 seed 同时给出时以 x0 为准(冻结契约只把"两个都缺"列为错误)。
	x0Resp, _, _ := a.Handle([]byte(`{"op":"reset","x0":` + boxedDesign(t) + `,"seed":999}`))
	explicit, _, _ := c.Handle([]byte(`{"op":"reset","x0":` + boxedDesign(t) + `}`))
	if string(x0Resp) != string(explicit) {
		t.Errorf("x0 must win when both x0 and seed are present:\n%s\n%s", x0Resp, explicit)
	}
}

// TestResetClipsX0IntoTheBox 证明"先夹进 spec 盒子再求值": 盒外的点与它被夹住之后的点
// 是同一个 design(观测相同)。
func TestResetClipsX0IntoTheBox(t *testing.T) {
	spec := testSpec()
	hi := spec.Upper()
	out := make([]float64, spec.NParams())
	clipped := make([]float64, spec.NParams())
	for i := range out {
		out[i] = hi[i] * 10 // 每一维都越上界
		clipped[i] = hi[i]  // 夹住之后应当落在上界
	}

	a := newTestWorld(t, &toyScorer{}, 4)
	b := newTestWorld(t, &toyScorer{}, 4)
	fromOut, _, _ := a.Handle([]byte(`{"op":"reset","x0":` + floatList(out) + `}`))
	fromClipped, _, _ := b.Handle([]byte(`{"op":"reset","x0":` + floatList(clipped) + `}`))
	if string(fromOut) != string(fromClipped) {
		t.Errorf("an out-of-box x0 was not clipped to the box:\n%s\n%s", fromOut, fromClipped)
	}
}

// boxedDesign 是盒子中心(每个维度都在盒内)。
func boxedDesign(t *testing.T) string {
	t.Helper()
	spec := testSpec()
	lo, hi := spec.Lower(), spec.Upper()
	x := make([]float64, len(lo))
	for i := range x {
		x[i] = 0.5 * (lo[i] + hi[i])
	}
	return floatList(x)
}

// floatList 是测试自己写请求用的浮点数组(不走协议层的编码器: 请求是客户端的字节)。
func floatList(x []float64) string {
	out := make([]string, 0, len(x))
	for _, v := range x {
		out = append(out, strconv.FormatFloat(v, 'g', -1, 64))
	}
	return "[" + strings.Join(out, ",") + "]"
}

// ---------------------------------------------------------------------------
// 数值(契约 §2)
// ---------------------------------------------------------------------------

// TestFloatEncodingIsShortestRoundTrip 钉住"最短往返": parse(serialize(v)) == v 逐位
// 成立, 且禁止 %.6g / float32 / 定点之类的写法。
func TestFloatEncodingIsShortestRoundTrip(t *testing.T) {
	vals := []float64{
		0, 1, -1, 0.1, -1.2, 0.15, 12, 1e4, 2.5e6, 1e8, 3.536386, 0.780886,
		1.791703035e12, 0.2905708160753513, 1e-6, 1e-7, 1e20, 1e21, 123456.0, 999999.0,
		1234567.8, 0.0001, 0.00001, math.Pi, 5e-324, math.MaxFloat64,
	}
	for _, v := range vals {
		enc := string(flt(v))
		back, err := strconv.ParseFloat(enc, 64)
		if err != nil {
			t.Fatalf("flt(%v) = %q does not parse back: %v", v, enc, err)
		}
		if math.Float64bits(back) != math.Float64bits(v) {
			t.Errorf("flt(%v) = %q round-trips to %v — not bit-exact", v, enc, back)
		}
		// %.6g / 定点字符串 / float32 中转都会在这里留下痕迹: 它们要么带 '%', 要么
		// 把最短往返的位数丢掉(上面那个逐位比较抓的正是后者)。
		if strings.ContainsAny(enc, "% ") {
			t.Errorf("flt(%v) = %q carries formatting that is not a canonical JSON number", v, enc)
		}
	}

	// Go 的 'g'/prec=-1 的形态: 指数形式当且仅当十进制指数 < -4 或 >= 6。
	shapes := map[float64]string{
		1e4: "10000", 1e6: "1e+06", 999999: "999999", 2.5e6: "2.5e+06",
		0.0001: "0.0001", 0.00001: "1e-05", -1.2: "-1.2", 1.791703035e12: "1.791703035e+12",
	}
	for v, want := range shapes {
		if got := string(flt(v)); got != want {
			t.Errorf("flt(%v) = %q, want %q", v, got, want)
		}
	}

	// 非有限值不许被写成任何东西。
	for _, v := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		func() {
			defer func() {
				if r := recover(); r == nil {
					t.Errorf("flt(%v) did not refuse to serialise a non-finite value", v)
				}
			}()
			_ = flt(v)
		}()
	}
}

// TestResponsesCarryCanonicalNumbers 是对"整条响应"的检查: 响应里每一个数字都必须能
// 逐位解析回来, 且响应本身能被标准 JSON 解析器读懂(允许 1e+06 这种指数写法)。
func TestResponsesCarryCanonicalNumbers(t *testing.T) {
	w := newTestWorld(t, &toyScorer{}, 3)
	w.Handle([]byte(`{"op":"reset","seed":7}`))
	w.Handle([]byte(`{"op":"step","action":` + floatList(zeroAction(testSpec())) + `}`))
	resp, _, _ := w.Handle([]byte(`{"op":"step","action":` + floatList(zeroAction(testSpec())) + `}`))

	numbers := numberTokens(t, resp)
	if len(numbers) < 10 {
		t.Fatalf("expected the observation and info to carry numbers, found %d in %s", len(numbers), resp)
	}
	for _, tok := range numbers {
		if _, err := strconv.ParseFloat(tok, 64); err != nil {
			t.Errorf("token %q in a response is not a JSON number: %v", tok, err)
		}
	}
}

// numberTokens 用 json.Decoder 的 UseNumber 取回响应里的原始数字文本。
func numberTokens(t *testing.T, resp []byte) []string {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(string(resp)))
	dec.UseNumber()
	var out []string
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case json.Number:
			out = append(out, x.String())
		case []any:
			for _, e := range x {
				walk(e)
			}
		case map[string]any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	var top any
	if err := dec.Decode(&top); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	walk(top)
	return out
}

// ---------------------------------------------------------------------------
// 会话循环与退出码(契约 §1)
// ---------------------------------------------------------------------------

// TestServeExitCodes 钉住三种退出: close → 0, EOF → 0, 未知协议 → 1(且已经回过
// ok:false), 用法错误留给 cmd/forge。
func TestServeExitCodes(t *testing.T) {
	session := strings.Join([]string{
		`{"op":"hello"}`,
		`{"op":"reset","seed":12345}`,
		`{"op":"close"}`,
	}, "\n") + "\n"

	var out strings.Builder
	code := NewServer(newTestWorld(t, &toyScorer{}, 4), ProtocolV1, io.Discard).Serve(
		strings.NewReader(session), &out, nil)
	if code != 0 {
		t.Errorf("a close-terminated session exited %d, want 0", code)
	}
	if got := strings.Count(out.String(), "\n"); got != 3 {
		t.Errorf("the session produced %d responses, want 3", got)
	}

	// stdin EOF 等价于 close。
	out.Reset()
	code = NewServer(newTestWorld(t, &toyScorer{}, 4), ProtocolV1, io.Discard).Serve(
		strings.NewReader(`{"op":"hello"}`), &out, nil)
	if code != 0 {
		t.Errorf("EOF-terminated session exited %d, want 0", code)
	}

	// 未知协议: 第一个请求被拒, 进程非零退出。
	out.Reset()
	code = NewServer(newTestWorld(t, &toyScorer{}, 4), 99, io.Discard).Serve(
		strings.NewReader(session), &out, nil)
	if code != 1 {
		t.Errorf("an unsupported protocol exited %d, want 1", code)
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("an unsupported protocol answered %d requests, want 1: %q", len(lines), out.String())
	}
	if got := errCode(t, []byte(lines[0])); got != CodeUnsupportedProtocol {
		t.Errorf("first response code = %q, want %q", got, CodeUnsupportedProtocol)
	}

	// 内部 panic: 也已回过 ok:false, 然后必须非零退出。
	out.Reset()
	code = NewServer(newTestWorld(t, &toyScorer{panic: true}, 4), ProtocolV1, io.Discard).Serve(
		strings.NewReader(`{"op":"reset","seed":1}`), &out, nil)
	if code != 1 {
		t.Errorf("an internal panic exited %d, want 1", code)
	}
	if got := errCode(t, []byte(strings.TrimRight(out.String(), "\n"))); got != CodeInternal {
		t.Errorf("internal code = %q, want %q", got, CodeInternal)
	}
}

// TestStdoutCarriesProtocolLinesOnly 钉住契约 §1: stdout 上只有协议行, 一行一个
// JSON 对象, 且行数与请求数一一对应。
func TestStdoutCarriesProtocolLinesOnly(t *testing.T) {
	reqs := []string{
		`{"op":"hello"}`,
		"not json at all",
		`{"op":"reset","seed":3}`,
		`{"op":"step","action":` + floatList(zeroAction(testSpec())) + `}`,
		`{"op":"close"}`,
	}
	var out strings.Builder
	code := NewServer(newTestWorld(t, &toyScorer{}, 4), ProtocolV1, io.Discard).Serve(
		strings.NewReader(strings.Join(reqs, "\n")+"\n"), &out, nil)
	if code != 0 {
		t.Fatalf("session exited %d, want 0", code)
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != len(reqs) {
		t.Fatalf("got %d response lines for %d requests", len(lines), len(reqs))
	}
	for _, l := range lines {
		obj := mustJSON(t, []byte(l))
		if _, ok := obj["ok"].(bool); !ok {
			t.Errorf("response line without an ok field: %s", l)
		}
	}
}

// ---------------------------------------------------------------------------
// trace: 录制、逐字节回放、故意改坏
// ---------------------------------------------------------------------------

// TestTraceRoundTripIsByteExact 是 G18 的 Go 一侧在包内的那一半: 录一条会话, 再回放,
// 必须逐字节相同; 并且 trace 的每一行都必须是 {"req":<原始>,"resp":<原始>} 的**字符串
// 拼接**(用 ReadTrace 取回原始字节再比)。
func TestTraceRoundTripIsByteExact(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trace.jsonl")

	reqs := []string{
		`{"op":"hello"}`,
		`{"op":"reset","seed":20260927}`,
		`{"op":"step","action":[0.25,-0.5,1.5,-1.5,0.5,0.5,-0.5,-0.5,0.1,0.2,0.3,0.4]}`,
		`{"op":"close"}`,
	}
	rec, err := NewRecorder(path)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	var out strings.Builder
	code := NewServer(newTestWorld(t, &toyScorer{}, 4), ProtocolV1, io.Discard).Serve(
		strings.NewReader(strings.Join(reqs, "\n")+"\n"), &out, rec)
	if code != 0 {
		t.Fatalf("recording session exited %d, want 0", code)
	}
	if err := rec.Close(); err != nil {
		t.Fatalf("close recorder: %v", err)
	}

	// trace 的行必须恰好是字符串拼接, 且 req/resp 是原始字节。
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read trace: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) != len(reqs) {
		t.Fatalf("trace has %d lines for %d requests", len(lines), len(reqs))
	}
	respLines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	for i, l := range lines {
		want := `{"req":` + reqs[i] + `,"resp":` + respLines[i] + `}`
		if l != want {
			t.Errorf("trace line %d is not the byte concatenation of the request and response:\n got %s\nwant %s",
				i+1, l, want)
		}
	}

	parsed, err := ReadTrace(path)
	if err != nil {
		t.Fatalf("ReadTrace: %v", err)
	}
	if len(parsed) != len(reqs) {
		t.Fatalf("ReadTrace returned %d lines, want %d", len(parsed), len(reqs))
	}
	for i, ln := range parsed {
		if string(ln.Req) != reqs[i] {
			t.Errorf("line %d req round-tripped to %q, want the original bytes %q", ln.No, ln.Req, reqs[i])
		}
		if string(ln.Resp) != respLines[i] {
			t.Errorf("line %d resp round-tripped to %q, want the original bytes %q", ln.No, ln.Resp, respLines[i])
		}
	}

	// 回放一份新的世界: 必须逐字节相同。
	var log strings.Builder
	if got := NewServer(newTestWorld(t, &toyScorer{}, 4), ProtocolV1, &log).Replay(path, &log); got != 0 {
		t.Fatalf("replaying a trace recorded from the same world exited %d, want 0:\n%s", got, log.String())
	}

	// 一个字节能让回放变红, 且报告里必须出现行号。
	broken := filepath.Join(dir, "broken.jsonl")
	// 故意改坏: 在 resp 里插一个空格。整行仍是合法 JSON(所以 ReadTrace 能读), 但世界
	// 产出的那一行不再逐字节相同 —— 这正是回放门要抓的形状。
	corrupt := strings.Replace(string(raw), `"ok":true`, `"ok" :true`, 1)
	if corrupt == string(raw) {
		t.Fatal("the corruption did not change the trace")
	}
	if err := os.WriteFile(broken, []byte(corrupt), 0o644); err != nil {
		t.Fatalf("write broken trace: %v", err)
	}
	log.Reset()
	if got := NewServer(newTestWorld(t, &toyScorer{}, 4), ProtocolV1, &log).Replay(broken, &log); got == 0 {
		t.Fatalf("a corrupted trace replayed green:\n%s", log.String())
	}
	if !strings.Contains(log.String(), "line 1") {
		t.Errorf("the replay report does not name the offending line:\n%s", log.String())
	}
}

// TestSessionsDoNotDependOnLeftoverState 证明同一个请求序列在两个**不同进程**里语义相
// 同: 两个新建的世界(每个都是空注册表的样子: design_id 由 scorer 从 D0001 开始)必须
// 给出同一段响应字节 —— 这是回放门成立的前提。
func TestSessionsDoNotDependOnLeftoverState(t *testing.T) {
	run := func() string {
		var out strings.Builder
		reqs := strings.Join([]string{
			`{"op":"reset","seed":5}`,
			`{"op":"step","action":` + floatList(zeroAction(testSpec())) + `}`,
			`{"op":"step","action":` + floatList(zeroAction(testSpec())) + `}`,
			`{"op":"close"}`,
		}, "\n") + "\n"
		NewServer(newTestWorld(t, &toyScorer{}, 4), ProtocolV1, io.Discard).Serve(
			strings.NewReader(reqs), &out, nil)
		return out.String()
	}
	if a, b := run(), run(); a != b {
		t.Errorf("two identical sessions produced different bytes:\n%s\n%s", a, b)
	}
}
