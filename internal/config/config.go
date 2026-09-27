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
		"weights": map[string]any{
			"field": s.Weights.Field, "mirror": s.Weights.Mirror, "volume": s.Weights.Volume,
			"ripple": s.Weights.Ripple, "cost": s.Weights.Cost, "penalty": s.Weights.Penalty,
		},
		"self_field_T": s.SelfField(),
		"n_params":     s.NParams(),
	}
}
