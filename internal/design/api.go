// Package design 是 Forge 的**内部设计判决层**: 把 ProjectionPhysics 里的装置判据
// 逐条移植成可计算、可回归、带出处的判据。
//
// 为什么有这一层: Forge v0.1 的评分函数只问「中平面的场均不均匀」。那**不是**装置设计
// 问题 —— 它让一个把线圈贴到距打分点 4.196 mm 的退化解拿了第一名 (D9857, runs/phase0/)。
// 真正决定装置成败的门在上游的内部设计里: 场能不能撑住设计密度 (PF3/FC1)、μ 窗口
// 开不开 (FC11b)、被约束的步数预算有多少 (TD19–TD21)。本包把这些门实现成判据。
//
// 契约: docs/design-layer.md (已冻结)。词条口径一律沿用上游仓库, 不在这里重新发明物理。
//
// 上游唯一真源:
//   - ProjectionPhysics/scripts/verify_moire_field.py  (公式本体: 常数块 + n_max/B_min/
//     n_op/tau_lawson/X_req/B_death/p_rel/v_rel/chi_mu)
//   - ProjectionPhysics/artifacts/moirefield/{report.json,summary.txt}    (七层账本 M1–M8)
//   - ProjectionPhysics/artifacts/mudynamics/{report.json,summary.txt}    (TD19–TD21)
//   - ProjectionPhysics/artifacts/fusionroadmap/{report.json,summary.txt}(判决量 D1/D2/D3)
//
// 锚点纪律: 契约 §3 的数值锚点**不由本文件手抄**。scripts/emit_pp_anchors.py 从上面那些
// 真实文件读出锚点 (连同上游 git commit 与读取时间) 写成
// testdata/projectionphysics_anchors.json, 再由 anchor_test.go 逐条比对。
// 唯一在 Go 源码里出现的上游数字是 sources.go 的场源材料表 —— 它是冻结接口的一部分
// (§4 SourceTable), 并由 anchor_test.go 反查上游 meta.sources 与 M2 行逐位复核。
//
// 诚实边界 (逐条对应契约 §6):
//   - 本层**不算** μ, 只算「给定 μ 能维持多少步、需要多大的场」。μ 的主动产生 =
//     第二输入缺口 (上游原文: 缺口从「η 是哪来的」变成「抹平功率是哪来的」)。
//   - Forge 的场模型仍是真空圆环丝电流的精确静磁学: 无 plasma、无 β 修正、无平衡。
//     本层是把静磁学输出**喂进**上游的门, 不是把 plasma 加进来。
//   - 不算排程/预算: 人·月、μ 阶梯 (1e-4→1e-3→…) 是排程假设与目标曲线, 不进本层。
//   - fc5_locked 一律 unknown, 绝不计入 pass。
//   - 本层是模型选择 + 上游已证条目的代入: 真但平凡, 没有新物理预言。
package design

import (
	"fmt"
	"math"

	"github.com/logos-42/hushfusion-forge/internal/config"
	"github.com/logos-42/hushfusion-forge/internal/physics"
)

// Input 是一次设计判决的外部条件 (不是设计变量)。
//
// 注意 DeviceScaleM: 上游 A_REF = 0.2 m 是 0.6 m 环的参考尺度, 而 Forge 的设计向量里
// 线圈半径最大到 1.0 m —— 两者不是同一个量。本层的 a 是**上游口径**的约束区尺度,
// 由调用方显式给出, 不拿 spec 的边界去凑。
type Input struct {
	DeviceScaleM float64 `json:"device_scale_m"` // a [m]: 等离子体/约束区尺度 (上游 A_REF = 0.2)
	SourceKey    string  `json:"source_key"`     // 场源材料类 (见 SourceTable), 决定 B_cap
	Eta          float64 `json:"eta"`            // μ 状态方程增益 η ∈ [0, 2)
	Mu0          float64 `json:"mu0"`            // 初始 μ
}

// Gate 是一条判据的结果: 可算、可回归、带出处。
//
// json tag 只影响 `forge design --json` 的机器可读输出, 不改变字段集合
// (契约 §4 冻结的 8 个字段一个不多一个不少)。
type Gate struct {
	Key        string  `json:"key"`        // GateFieldMin / GateDeath / GateMuWindow / GateCoilLoad / GateBuildable / GateSteps / GateFC5Locked
	Label      string  `json:"label"`      // 中文短名
	Formula    string  `json:"formula"`    // 实算用的公式 (照 docs/design-layer.md §2 抄)
	Value      float64 `json:"value"`      // 实算值
	Ref        float64 `json:"ref"`        // 判据阈值 (0 表示无阈值)
	Pass       bool    `json:"pass"`       // 判定
	Unknown    bool    `json:"unknown"`    // 未证/不可判 (如 FC5 锁定) → 不得计入 pass
	Provenance string  `json:"provenance"` // 上游条目: FC1/PF3/FC11b/FC4/TD21/...
}

// 冻结的门 key (docs/design-layer.md §5)。前六条是六道门, 第七条是 FC5 锁定的
// 单列 unknown 条目。
const (
	GateFieldMin  = "field_min"
	GateDeath     = "death"
	GateMuWindow  = "mu_window"
	GateCoilLoad  = "coil_load"
	GateBuildable = "buildable"
	GateSteps     = "steps"
	GateFC5Locked = "fc5_locked"
)

// DecisiveGateKeys 是**计入总判决**的门。
//
// steps 不在其中: 契约 §5 明写它「报值, 不判生死」。fc5_locked 也不在其中:
// 它是 unknown, 永远不得计入 pass。
var DecisiveGateKeys = []string{GateFieldMin, GateDeath, GateMuWindow, GateCoilLoad, GateBuildable}

// Source 是场源材料类表的一行 (上游 verify_moire_field.py 的 SOURCES dict)。
type Source struct {
	Key      string  `json:"key"`      // 上游 key
	Label    string  `json:"label"`    // 上游中文 label
	BCapT    float64 `json:"B_cap_T"`  // 场天花板 [T] (上游 B)
	Citation string  `json:"citation"` // 上游 meta.sources 的文献出处
}

// Scope 是一份设计判决层的输出: 输入 + 上游材料行 + 逐条门 + 总结论。
//
// Scope 只装**推导得出来的东西**: 每个字段都能由 Input、上游闭式解、或喂进来的
// physics.Metrics 复算。设计向量本身不在里面 (它由调用方持有)。
type Scope struct {
	In          Input  `json:"input"`
	Source      Source `json:"source"`
	SourceKnown bool   `json:"source_known"`

	// 由上游闭式解推出的量 (CLI 与报告直接引用, 不在别处重算)。
	Tau0S     float64 `json:"tau0_s"`         // TAU0(a) = a²/D₀                          [s]
	NMaxCap   float64 `json:"n_max_cap"`      // n_max(B_cap)                             [m⁻³]
	NOpCap    float64 `json:"n_op_cap"`       // n_op(B_cap) = min(N_DESIGN, n_max)       [m⁻³]
	ChiMuCap  float64 `json:"chi_mu_cap"`     // χ_μ(B_cap, a) = X_req/FLOOR
	BMinT     float64 `json:"b_min_design_T"` // B_min(N_DESIGN)                      [T]
	BDeathT   float64 `json:"b_death_T"`      // B_death(a)                           [T]
	PRelCap   float64 `json:"p_rel_cap"`      // P_rel(B_cap / B_ref)  (相对 9 T 满 β 基准)
	VRelCap   float64 `json:"v_rel_cap"`      // 同功率体积比
	MuCeiling float64 `json:"mu_ceiling"`     // 1 − FLOOR: FC11 硬天花板 (D-T)

	// μ 动力学上下文 (判决量 D1/D3 只报「怎么测」, 不做预测)。
	LockingFactor     float64 `json:"locking_factor"`      // 1/√(1−μ₀) (TD20)
	RciMu0            float64 `json:"rci_mu0"`             // R_ci(μ₀) = 1/(1−μ₀) − 1 (FC9)
	CloseStepAnalytic float64 `json:"close_step_analytic"` // n_close 解析值 (TD21)
	CloseStepInt      int     `json:"close_step_int"`      // 首次关闭的整数步; 永不关闭 = -1

	// 门的输入 (表格脚注要引用它们, 所以留在 Scope 里)。
	BThroatT              float64 `json:"b_throat_T"`
	BCoilMaxT             float64 `json:"b_coil_max_T"`
	MinCoilGapM           float64 `json:"min_coil_gap_m"`
	CoilProximityFloorHit bool    `json:"coil_proximity_floor_hit"`

	Gates           []Gate   `json:"gates"`
	Passed          int      `json:"passed"`
	Failed          int      `json:"failed"`
	Unknown         int      `json:"unknown"`
	AllDecisivePass bool     `json:"all_decisive_pass"`
	Notes           []string `json:"notes"`
}

// GateByKey 按 key 取一条门 (CLI 的表格与测试都按 key 定位, 不按下标)。
func (s Scope) GateByKey(key string) (Gate, bool) {
	for _, g := range s.Gates {
		if g.Key == key {
			return g, true
		}
	}
	return Gate{}, false
}

// Review 对一份 coil 设计 (由 spec + metrics 给定) 跑全部门。
//
// 契约 §5 的六条判据 + 一条单列的 fc5_locked (Unknown)。任何"读不到输入"的情况
// (例如未知的场源 key) 都报 Unknown, 绝不悄悄给默认值 —— 默认值会让一份读不懂的
// 设计看起来像过了门。
func Review(spec config.Spec, m physics.Metrics, in Input) Scope {
	s := Scope{
		In: in,

		Tau0S:     Tau0(in.DeviceScaleM),
		BMinT:     BMin(NDesign, Beta),
		BDeathT:   BDeath(in.DeviceScaleM),
		MuCeiling: 1 - Floor,

		LockingFactor:     LockingFactor(in.Mu0),
		RciMu0:            Rci(in.Mu0),
		CloseStepAnalytic: WindowCloseStep(in.Mu0, in.Eta, Floor),
		CloseStepInt:      FirstClosedStep(in.Mu0, in.Eta, Floor),

		BThroatT:              m.BThroatT,
		BCoilMaxT:             m.BCoilMaxT,
		MinCoilGapM:           m.MinCoilGapM,
		CoilProximityFloorHit: m.CoilProximityFloorHit,
	}

	src, known := LookupSource(in.SourceKey)
	s.Source, s.SourceKnown = src, known
	if known {
		s.NMaxCap = NMax(src.BCapT)
		s.NOpCap = NOp(src.BCapT)
		s.ChiMuCap = ChiMu(src.BCapT, in.DeviceScaleM)
		s.PRelCap = PRel(src.BCapT, BRefPower)
		s.VRelCap = VRel(src.BCapT, BRefPower)
	} else {
		s.Notes = append(s.Notes, fmt.Sprintf("未知场源 key %q: death / mu_window / coil_load "+
			"一律报 unknown, 不给默认值 (已知 key 见 SourceTable)", in.SourceKey))
	}

	// ---- 门 1: field_min [PF3 / FC1] ----
	s.Gates = append(s.Gates, Gate{
		Key:        GateFieldMin,
		Label:      "约束场门",
		Formula:    "B_throat ≥ B_min(N_DESIGN) = √(4·MU0·KT_J·n_design/BETA)",
		Value:      m.BThroatT,
		Ref:        s.BMinT,
		Pass:       m.BThroatT >= s.BMinT,
		Provenance: "PF3 / FC1（上游 M2 行；数值见 testdata/projectionphysics_anchors.json）",
	})

	// ---- 门 2: death [MF-M5★ / FC11b] ----
	death := Gate{
		Key:        GateDeath,
		Label:      "死活判据",
		Formula:    "B_cap ≥ B_death(a) = √(4·MU0·KT_J·n_cross/BETA), n_cross = (NT_LAWSON/TAU0(a))·√FLOOR",
		Ref:        s.BDeathT,
		Provenance: "MF-M5★ / FC11b（上游 M5 扫描；数值见锚点文件）",
	}
	if known {
		death.Value = src.BCapT
		death.Pass = src.BCapT >= s.BDeathT
	} else {
		death.Unknown = true
	}
	s.Gates = append(s.Gates, death)

	// ---- 门 3: mu_window [FC4 / FC11b] ----
	win := Gate{
		Key:        GateMuWindow,
		Label:      "μ 窗口门",
		Formula:    "χ_μ(B_cap, a) = X_req/FLOOR > 1, X_req = (TAU0(a)/τ_L(n_op(B_cap)))²",
		Ref:        1.0,
		Provenance: "FC4 / FC11b（上游 M4 行；数值见锚点文件）",
	}
	if known {
		win.Value = s.ChiMuCap
		win.Pass = s.ChiMuCap > 1.0
	} else {
		win.Unknown = true
	}
	s.Gates = append(s.Gates, win)

	// ---- 门 4: coil_load [spec.CoilFieldLimit + B_cap] ----
	// 导体场天花板取 spec 的 HTS 极限与该场源材料极限的**较小者**: 材料撑不到的场,
	// 导体再强也没有意义。
	load := Gate{
		Key:        GateCoilLoad,
		Label:      "导体场门",
		Formula:    "B_coil_max ≤ min(spec.CoilFieldLimit, B_cap)",
		Provenance: "spec.CoilFieldLimit (HTS @20 K) + M6 载流门量级账",
	}
	if known {
		load.Ref = math.Min(spec.CoilFieldLimit, src.BCapT)
		load.Value = m.BCoilMaxT
		load.Pass = m.BCoilMaxT <= load.Ref
	} else {
		load.Unknown = true
	}
	s.Gates = append(s.Gates, load)

	// ---- 门 5: buildable [几何] ----
	// 两个条件都不满足才算过关的否命题: min_coil_gap ≥ 2·TPack **且** 没有踩到近导线
	// 钳位地板。D9857 那种把线圈摆到打分点 4.2 mm 的退化解死在这里。
	s.Gates = append(s.Gates, Gate{
		Key:        GateBuildable,
		Label:      "可造性门",
		Formula:    "min_coil_gap ≥ 2·spec.TPack 且 !coil_proximity_floor_hit",
		Value:      m.MinCoilGapM,
		Ref:        2 * spec.TPack,
		Pass:       m.MinCoilGapM >= 2*spec.TPack && !m.CoilProximityFloorHit,
		Provenance: "spec.TPack / spec.MinCoilSep + D9857 退化解 (runs/phase0/)",
	})

	// ---- 门 6: steps [TD21] —— 报值, 不判生死 ----
	//
	// 契约 §5 的判据是 n_close > 0 (η < 1)。+∞ 表示「窗口不存在」(η ≥ 1: 一步到 1),
	// 它不是「> 0」的一个正常取值, 所以单独排除 —— 否则 η ≥ 1 会假过门。
	stepsPass := s.CloseStepAnalytic > 0 && !math.IsInf(s.CloseStepAnalytic, 1)
	s.Gates = append(s.Gates, Gate{
		Key:        GateSteps,
		Label:      "关闭步",
		Formula:    "n_close = ln((1−μ₀)/FLOOR)/ln(1/(1−η)) > 0 (η < 1)",
		Value:      s.CloseStepAnalytic,
		Ref:        0,
		Pass:       stepsPass,
		Provenance: "TD21（上游 N15 字段与 summary.txt N11；整数步与解析值见锚点文件）",
	})

	// ---- FC5 锁定: 单列 unknown, 永不报 pass ----
	s.Gates = append(s.Gates, Gate{
		Key:        GateFC5Locked,
		Label:      "FC5 锁定",
		Formula:    "Λ = τ_E 增益 / S* 惩罚 ≡ 1 (FC5 预言); 几何捕获 Λ > 1 未证",
		Ref:        1.0,
		Pass:       false,
		Unknown:    true,
		Provenance: "FC5 (上游 R2: Λ≡1 是预言, Λ>1 是本设计与 FC 轮的分歧点, 未证)",
	})

	// ---- 计数与总结论 ----
	for _, g := range s.Gates {
		switch {
		case g.Unknown:
			s.Unknown++
		case g.Pass:
			s.Passed++
		default:
			s.Failed++
		}
	}
	s.AllDecisivePass = s.SourceKnown
	for _, key := range DecisiveGateKeys {
		g, ok := s.GateByKey(key)
		if !ok || g.Unknown || !g.Pass {
			s.AllDecisivePass = false
		}
	}
	if s.CoilProximityFloorHit {
		s.Notes = append(s.Notes,
			"coil_proximity_floor_hit = true: 有采样点落在距导线 5e-3 m 的钳位地板内, "+
				"该几何被目标函数惩罚, 可造性门按否判决 (D9857 那类退化解)")
	} else if s.MinCoilGapM < 2*spec.TPack {
		s.Notes = append(s.Notes, fmt.Sprintf(
			"min_coil_gap = %g m < 2·spec.TPack = %g m: 两个线圈包会互相穿过去, 可造性门按否判决",
			s.MinCoilGapM, 2*spec.TPack))
	}
	if !stepsPass {
		s.Notes = append(s.Notes,
			"关闭步 n_close 不为正 (η ≥ 1 或 μ₀ ≥ 1): 一步到 1, 窗口不存在 —— 报值不判生死, 不计入总判决")
	}
	if s.Unknown > 0 {
		s.Notes = append(s.Notes, fmt.Sprintf("有 %d 条门是 unknown (未证/读不到输入): "+
			"unknown 绝不计入 pass, 也不进总判决", s.Unknown))
	}
	s.Notes = append(s.Notes,
		"本层不算 μ: 给定 μ₀ 只算窗口余量与所需场; μ 的主动产生 = 第二输入缺口 (契约 §6)")
	return s
}

// NMax 是 FC1: FRC β≈1 平衡的密度上限 n = β·B²/(4·μ₀·kT) [m⁻³]。
//
// 出处: 上游 verify_moire_field.py def n_max() 常数块 (MU0/KB/EV/T_KEV/BETA),
// 报告条目 M2/M8。
func NMax(b float64) float64 { return Beta * b * b / (4 * MU0 * KTJ) }

// BMin 是 PF3 的反解: β ≤ 1 要求的最小约束场 B = √(4·μ₀·kT·n/β) [T]。
//
// 出处: 上游 def B_min() (合同 §3 锚点: B_min(N_DESIGN) = 1.0991 T, 容差 5e-3)。
// beta ≤ 0 时返回 +Inf (无解), 不返回 NaN。
func BMin(n, beta float64) float64 {
	if beta <= 0 {
		return math.Inf(1)
	}
	return math.Sqrt(4 * MU0 * KTJ * n / beta)
}

// NOp 是运行密度 = min(设计密度, 该场下的密度上限) [m⁻³]。
//
// 出处: 上游 def n_op()。
func NOp(b float64) float64 { return math.Min(NDesign, NMax(b)) }

// TauLawson 是劳森要求的最小约束时间 τ_L = n·τ / n = 2e20/n [s]。
//
// 出处: 上游 def tau_lawson() (劳森 n·τ = 2e20 @15 keV)。
func TauLawson(n float64) float64 { return NTLawson / n }

// PRel 是满 β 分支下的相对功率密度 P ∝ B⁴, 相对基准场 B_ref [无量纲]。
//
// 出处: 上游 def p_rel() (合同 §3 锚点: p_rel(9.0) = 1.0 自洽)。
func PRel(b, bRef float64) float64 {
	r := NMax(b) / NMax(bRef)
	return r * r
}

// VRel 是同一功率所需的相对体积 (满 β 分支, ∝ B⁻⁴)。
//
// 出处: 上游 def v_rel()。
func VRel(b, bRef float64) float64 { return 1.0 / PRel(b, bRef) }

// 契约 §4 冻结的闭式解签名 —— 用函数值赋值做**编译期**签名校验: 任何签名漂移
// (改参数个数/顺序/类型, 或不小心把它变成方法) 都会让本文件编译失败, 而不是等到
// 某个调用点才发现。这些函数的实现按契约 §4 分派在 confinement.go / mudynamics.go /
// sources.go 里。
var (
	_ func(b float64) float64                                  = NMax
	_ func(n, beta float64) float64                            = BMin
	_ func(b float64) float64                                  = NOp
	_ func(n float64) float64                                  = TauLawson
	_ func(a float64) float64                                  = Tau0
	_ func(b, a float64) float64                               = XReq
	_ func(a float64) float64                                  = BDeath
	_ func(b, a float64) float64                               = ChiMu
	_ func(b, bRef float64) float64                            = PRel
	_ func(b, bRef float64) float64                            = VRel
	_ func(mu0, eta float64, n int) float64                    = MuAfterSteps
	_ func(mu0, eta, meOverMi float64) float64                 = WindowCloseStep
	_ func(mu0, eta, meOverMi float64) int                     = FirstClosedStep
	_ func(lam float64) float64                                = SinkEta
	_ func(mu0, etaExt, lam float64, n int) float64            = SinkMuAfterSteps
	_ func(mu0, etaExt, lam, muWork float64, maxSteps int) int = SinkMuWorkSteps
	_ func(mu float64) float64                                 = LockingFactor
	_ func(g float64) float64                                  = MuFromGain
	_ func(mu float64) float64                                 = Rci
	_ func(delta float64) float64                              = MuMinFromDelta
	_ func(k, decades float64) float64                         = PowerMultiple
	_ func() []Source                                          = SourceTable
	_ func(key string) (Source, bool)                          = LookupSource
	_ func(config.Spec, physics.Metrics, Input) Scope          = Review
)
