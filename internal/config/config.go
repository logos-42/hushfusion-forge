// Package config 是 Forge 引擎所用的每一个物理常量、边界与权重的唯一真源。
//
// 本项目的规矩: 任何会影响 score 的数字都不得在树的其它地方硬编码。一个数字只要重要,
// 就住在这里, 连同它的物理含义与其出处。
//
// FROZEN INTERFACE (v0.1)。JSON tag 是与 Python 参考实现 (python/forge) 共享的交换
// 格式。改一个 tag 是一次 schema 迁移, 而不是一次编辑: 它会破坏 testdata/ 里的跨语言
// golden 比较, 必须两个实现同时改。
package config

import "math"

// MU0 是真空磁导率 [H/m]。
const MU0 = 4.0e-7 * math.Pi

// ForgeVersion 是引擎的判据版本, 语义写死在 docs/version-0.1.2.md §3:
//
//	MAJOR  冻结契约的断裂性变更（改冻结签名/类型/JSON tag 的语义、删字段）
//	MINOR  新增能力（新命令/新世界/新层），旧证据仍可解释
//	PATCH  改判据本身 —— 任何会让**同一个设计得到不同分数**的改动
//
// 它住在 config 而不是 cmd/ 的理由是这个数字会被写进 runs/<tag>/results.json 的
// meta（否则「用今天的判据复现昨天的 run」这件事无法被机器判定）, 而写入方在
// internal/experiment —— cmd/ 在依赖图的最下游, 把版本放那里会让 experiment 反向依赖它。
const ForgeVersion = "0.2"

// Bounds 是单个线圈的搜索盒子 [SI]。
type Bounds struct {
	Radius  [2]float64 `json:"radius"`  // [m]  线圈半径
	Z       [2]float64 `json:"z"`       // [m]  轴向位置
	Current [2]float64 `json:"current"` // [A]  安匝数
}

// Weights 是工程上的运行判断, 不是物理。未加权的原始项总是与 score 一起存储, 以便
// 事后重新推导任何加权。
type Weights struct {
	Field   float64 `json:"field"`   // log10(B_mid / B_ref)
	Mirror  float64 `json:"mirror"`  // log10(R / R_ref)
	Volume  float64 `json:"volume"`  // 好场体积分数
	Ripple  float64 `json:"ripple"`  // 元胞内非单调的场结构
	Cost    float64 `json:"cost"`    // 欧姆成本代理, 由基线归一化
	Penalty float64 `json:"penalty"` // 归一化约束违反量上的乘子
}

// Spec 是待设计的装置 + 评估窗口 + 工程极限。
//
// 保真度声明 (v0.1): 场模型是真空圆环丝电流的精确静磁学。这里没有 plasma: 无压强、
// 无抗磁响应、无平衡、无有限 beta 修正、无涡流、无导体电流共享。那些是 Phase-2 的
// 事项 (见 PLAN.md)。
type Spec struct {
	NCoils int    `json:"n_coils"`
	Bounds Bounds `json:"bounds"`

	BRef      float64 `json:"b_ref"`      // [T] 场项参考值
	MirrorRef float64 `json:"mirror_ref"` // [-] 镜像比参考值

	CoilFieldLimit float64 `json:"coil_field_limit"` // [T]   峰值导体场 (HTS @20 K, 保守取值)
	JEng           float64 `json:"j_eng"`            // [A/m^2] 绕组包电流密度
	TPack          float64 `json:"t_pack"`           // [m]   绕组包厚度

	RPlasma  float64 `json:"r_plasma"`   // [m] 等离子体半径 (中心元胞)
	ZMid     float64 `json:"z_mid"`      // [m] 中平面采样体积的半高
	ZCell    float64 `json:"z_cell"`     // [m] 中心元胞的半长
	ZAxisMax float64 `json:"z_axis_max"` // [m] 为求 throat 而采样的轴向范围
	NAxis    int     `json:"n_axis"`     // 轴向采样点数
	NVolR    int     `json:"n_vol_r"`    // 元胞体积上的径向采样点数
	NVolZ    int     `json:"n_vol_z"`    // 元胞体积上的轴向采样点数

	ConfineFactor float64 `json:"confine_factor"` // |B| <= factor * B_mid 即算作 "好场"
	MinCoilSep    float64 `json:"min_coil_sep"`   // [m] 线圈中心的最小间距

	// MinClearance 是**可造性**约束: 导体面到约束区域(中心元胞)的最小允许净空 [m]。
	//
	// 加它的原因是一个具体的失败: 0.1.0 的最优解把一圈 1.78 MA 的导体放在距中场采样点
	// 4.196 mm 处 —— 当时的目标函数没有一项阻止它, 于是 "机器赢" 赢在了一个真实装置
	// 不可能有的位形上。取值必须与 MinCoilSep 同量级, 并且必须让**人工基线仍然可行**
	// (否则 "机器打败人类" 会退化成 "比谁的基线更不可造")。
	//
	// 与 Weights 同性质: **工程判断, 不是物理**。口径 / 判据 / 死法见 docs/version-0.1.2.md §1。
	MinClearance float64 `json:"min_clearance"` // [m] 导体面到约束区域的最小允许净空

	Weights Weights `json:"weights"`
}

// RL 环境 (internal/rlenv) 与 `forge world serve` 共用的 episode 长度与动作步长。
//
// 它们决定每一步走多远、一条 episode 有多长, 因此按房规住在 config 里, 而不是散落在
// CLI 或协议层。internal/rlenv 与 internal/world 各自留零表示"用默认值"时用的就是
// 这两个数字；internal/world 有一条测试盯着 rlenv 的默认值必须与之相同, 所以两边
// 不会各自漂移出一份"差不多的"默认值。
const (
	DefaultMaxSteps   = 20
	DefaultDeltaScale = 0.15
)

// 世界协议 v2 (docs/world-protocol.md §8) 的三条游戏规则数字。它们住在这里(而不是
// 协议层或 CLI)是同一条房规: 任何会被写进**响应字节**的数字只有一个家。
//
// 前两个是 regime 的缺省值(docs/world-structure.md §3 的四档表: 步数预算 8/24、
// 终止目标 1.0), 第三个是 R1 里那一项真代价的权重。三条都**不是**时序结构的来源
// (§2 的 R1/R5 一栏两处都写明"不是"), 所以它们的取值不参与 G19 的判定 —— 这也是
// 它们可以是一个"看起来合理"的值、而不需要标定的原因: 它们只改变走多远与终止原因,
// 不改变顺序, 也不改变 A(§1 的 A 只读真目标函数 score)。
const (
	// DefaultBudget 是 v2 episode 的整数步数预算(§3: tight=8 / loose=24, 缺省取 loose)。
	DefaultBudget = 24

	// DefaultTarget 是 v2 的终止目标分(§3 四档的 target 都是 1.0)。
	DefaultTarget = 1.0

	// LambdaCost 是 R1 奖励里那一项真代价的权重 λ_cost:
	//
	//	reward = Δscore − λ_cost · max(0, Δcost_proxy) / cost(episode 初始设计)
	//
	// 取 1.0 = 不额外缩放: cost 已经用 episode 初始设计的真 cost 归一化过了
	// (§8.4: cost_ref 不用常数)。这个值不参与 G19(A 只读 score), 见上面的说明。
	LambdaCost = 1.0
)

// 窗口余量与关闭步**复用 internal/design**, 不重算 —— 那是同一批上游条目的唯一实现点.

// 交换子世界 (docs/world-commutator-candidates.md) 的世界配置。它是一条**候选**世界的
// 参数, 不是契约: 判据(实测 A 与闭式预测一致)与这些数字的取值无关 —— 换一组权重,
// 闭式预测跟着一起换, 比较仍然成立。
const (
	// MuFieldCells 是场向量的格点数(离散环上的格点)。区域族由它派生(见 mu.DeclaredRegions)。
	MuFieldCells = 16

	// MuInitialMu 是 episode 起点的 μ。0 = 桌面 μ(与 internal/design 的缺省 Mu0 同口径):
	// 起点必须远离 FC11 天花板, 否则"抹平把 μ 推到 1"这条构造就没有余地。
	MuInitialMu = 0.0
)

// μ 世界目标函数的三项相对权重。它们与 Spec.Weights 同一性质: **工程判断, 不是物理**。
//
//	score = W_mu·(μ/MuCeiling) + W_gain·η(v) + W_closed·1[μ ≥ MuCeiling]
//
// 三项都读**状态**(前两项读 μ 与场, 第三项读 μ 与 FC11 天花板), 因此 score 对 μ 严格
// 单调(internal/mu 有一条单测钉住它) —— 单调是"构造性 A>0"的前提:
// 顺序差只改变 μ, 不改变场, 所以 score 必须随 μ 严格增长, 那个顺序差才会读成 A ≠ 0。
const (
	MuScoreWeightMu     = 1.0  // μ 进展项: μ / design.MuCeiling
	MuScoreWeightGain   = 0.5  // 抹平增益项: η = 1 − Q_Ω(v)/Q_Ω(v₀) (上游 TD11 的桥)
	MuScoreWeightClosed = 0.25 // 窗口已关闭的加项: 1[μ ≥ design.MuCeiling] (FC11 硬天花板)
)

// DefaultSpec 返回 v0.1 的参考装置与评估窗口。
//
// 电流上限的设置使得手工设计的参考装置 (internal/baseline.TextbookMirror, throat 电流
// ~1.62 MA) 严格落在盒子*内部*。一个在重新编码成设计向量时被裁剪的基线会被当成另一
// 台机器来打分, 于是 "机器胜过人类" 就变成了对着一个没有人提出过的设计来测量。由
// TestBaselineInsideSearchBox 强制。
func DefaultSpec() Spec {
	return Spec{
		NCoils: 4,
		Bounds: Bounds{
			Radius:  [2]float64{0.10, 1.00},
			Z:       [2]float64{-1.20, 1.20},
			Current: [2]float64{1.0e4, 2.5e6},
		},
		BRef:           1.0,
		MirrorRef:      2.0,
		CoilFieldLimit: 12.0,
		JEng:           1.0e8,
		TPack:          0.05,
		RPlasma:        0.15,
		ZMid:           0.15,
		ZCell:          0.80,
		ZAxisMax:       1.40,
		NAxis:          161,
		NVolR:          13,
		NVolZ:          33,
		ConfineFactor:  1.25,
		MinCoilSep:     0.05,
		MinClearance:   0.05,
		Weights: Weights{
			Field:   1.0,
			Mirror:  0.5,
			Volume:  0.75,
			Ripple:  0.5,
			Cost:    1.0,
			Penalty: 10.0,
		},
	}
}

// NParams 是设计向量长度: [r_0..r_K, z_0..z_K, I_0..I_K]。
func (s Spec) NParams() int { return 3 * s.NCoils }

// SelfField 是线圈自身绕组包作用在导体上的场 [T]。
//
// 模型: 一个厚度为 TPack、载有均匀电流密度 JEng 的类螺线管绕组包, 在绕组面上有
// B ~ mu0*j*t/2 (对无限大平板是精确的)。它是 "一个处在电流密度极限的绕组包本身就
// 已经坐在这么大的场里" 的文档化锚点, Phase 2 会被绕组包/FEM 模型取代。
func (s Spec) SelfField() float64 { return MU0 * s.JEng * s.TPack / 2.0 }

// Lower 是设计向量的下界, 形状 (NParams)。
func (s Spec) Lower() []float64 {
	out := make([]float64, 0, s.NParams())
	for i := 0; i < s.NCoils; i++ {
		out = append(out, s.Bounds.Radius[0])
	}
	for i := 0; i < s.NCoils; i++ {
		out = append(out, s.Bounds.Z[0])
	}
	for i := 0; i < s.NCoils; i++ {
		out = append(out, s.Bounds.Current[0])
	}
	return out
}

// Upper 是设计向量的上界, 形状 (NParams)。
func (s Spec) Upper() []float64 {
	out := make([]float64, 0, s.NParams())
	for i := 0; i < s.NCoils; i++ {
		out = append(out, s.Bounds.Radius[1])
	}
	for i := 0; i < s.NCoils; i++ {
		out = append(out, s.Bounds.Z[1])
	}
	for i := 0; i < s.NCoils; i++ {
		out = append(out, s.Bounds.Current[1])
	}
	return out
}

// AsMap 用与 Python 参考实现 (python/forge/config.py: Spec.as_dict) 相同的键渲染
// spec, 包括两个派生字段。schema 对等门比较的就是它。
func (s Spec) AsMap() map[string]any {
	return map[string]any{
		"n_coils":          s.NCoils,
		"bounds":           map[string]any{"radius": s.Bounds.Radius[:], "z": s.Bounds.Z[:], "current": s.Bounds.Current[:]},
		"b_ref":            s.BRef,
		"mirror_ref":       s.MirrorRef,
		"coil_field_limit": s.CoilFieldLimit,
		"j_eng":            s.JEng,
		"t_pack":           s.TPack,
		"r_plasma":         s.RPlasma,
		"z_mid":            s.ZMid,
		"z_cell":           s.ZCell,
		"z_axis_max":       s.ZAxisMax,
		"n_axis":           s.NAxis,
		"n_vol_r":          s.NVolR,
		"n_vol_z":          s.NVolZ,
		"confine_factor":   s.ConfineFactor,
		"min_coil_sep":     s.MinCoilSep,
		"min_clearance":    s.MinClearance,
		"weights": map[string]any{
			"field": s.Weights.Field, "mirror": s.Weights.Mirror, "volume": s.Weights.Volume,
			"ripple": s.Weights.Ripple, "cost": s.Weights.Cost, "penalty": s.Weights.Penalty,
		},
		"self_field_T": s.SelfField(),
		"n_params":     s.NParams(),
	}
}
