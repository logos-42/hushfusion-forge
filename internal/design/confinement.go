// confinement.go —— 契约 §2 的场-密度 / μ 窗口 / 功率-体积闭式解与常数。
//
// 公式与精度**逐字移植**上游 ProjectionPhysics/scripts/verify_moire_field.py, 不得改写、
// 不得「顺手优化」。每个常数与每个函数头都写明出处条目 (FC1 / PF3 / FC4 / FC11b / TD19–21)。
//
// 关于常数求值顺序 (跨语言浮点): 上游是 Python, 常数与闭式解按 float64 逐步求值。
// 这里一律用 **const 无精度常数算术 + 末尾一次舍入**。两者的差别只有 1–2 ULP
// (实测派生量相对差 ~2e-15), 因此 anchor_test.go 用 rel 1e-12 锚定上游字段,
// 而不是逐位相等 —— 见 testdata/projectionphysics_anchors.json 的 check_note。
package design

import (
	"math"

	"github.com/logos-42/hushfusion-forge/internal/config"
)

// 上游 verify_moire_field.py 「常数」块 (同值同精度)。
const (
	// MU0 是真空磁导率 [H/m]。上游写法 4·π×1e-7; 这里引用 internal/config 的同一处
	// 真源 (config 的写法是 4.0e-7*π —— 同一个数), 而不是抄第二份物理常数。
	MU0 = config.MU0

	KB = 1.380649e-23    // 玻尔兹曼常数 [J/K]
	EV = 1.602176634e-19 // 电子伏 [J]

	ME = 9.1093837015e-31 // 电子质量 [kg]              (FC11 地板分子)

	// AtomicMassUnit 是原子质量单位 [kg]。上游名 U。
	AtomicMassUnit = 1.66053906660e-27

	// MI 是 D-T 平均离子质量 [kg] = 2.5·U (沿用上游 verify_frc_compact 的口径)。
	MI = 2.5 * AtomicMassUnit

	TKeV = 15.0            // [keV] 上游参考温度
	KTJ  = TKeV * 1e3 * EV // [J]   kT

	Beta     = 1.0  // FRC β≈1 (FC1 的 β)
	NDesign  = 1e20 // [m⁻³] 仓库参考设计密度 (μ≥0.9997 那一轮的口径)
	ARef     = 0.2  // [m]   参考装置尺寸 a = 0.2 m (0.6 m 环)
	D0       = 1.11 // [m²/s] ITER 级 μ=0 标定 (二轮修正链 τ_E = τ₀/√(1−μ))
	NTLawson = 2e20 // [m⁻³·s] 劳森 n·τ @15 keV (n·T·τ = 3e21 keV·s/m³)

	// Floor 是 FC11 地板 m_e/m_i —— μ 窗口的硬下界 (1−μ > m_e/m_i)。D-T 口径:
	// 2.1943e-4。上游同式: FLOOR = ME/MI。
	Floor = ME / MI

	// BRefPower 是功率/体积相对值的基准场 [T]: 满 β、B = 9 T 的 CFR2 真空场。
	BRefPower = 9.0
)

// Tau0 是 TAU0(a) = a²/D₀ —— μ=0 标定的约束时间 [s] (二轮修正链 τ_E = τ₀/√(1−μ))。
//
// 出处: 上游 verify_moire_field.py 常数块 TAU0 = A_REF**2/D0 与 def X_req/B_death 内联的
// tau0 = a**2/D0。契约 §4 的冻结 API 清单没有单列它, 但 XReq/BDeath 都由它定义, 且
// 契约 §3 把 TAU0(0.2) = 0.0360 s 列为锚点 —— 所以这里导出它, 以便单独锚定。
func Tau0(a float64) float64 { return a * a / D0 }

// XReq 是所需的 (1−μ) 上限: τ_E = τ₀/√(1−μ) ≥ τ_L ⟹ 1−μ ≤ (τ₀/τ_L)²。
//
// 出处: 上游 def X_req(B, a) = (tau0/tau_lawson(n_op(B)))² 【FC4, 报告 M4】。
// 返回无量纲值; > Floor 才是可行 (见 ChiMu)。
func XReq(b, a float64) float64 {
	r := Tau0(a) / TauLawson(NOp(b))
	return r * r
}

// BDeath 是死活判据的临界场 [T]: 使 X_req(B) = FLOOR 的 B。
//
// B 低于它 ⟹ μ 窗口关闭 ⟹ **无解**, 而不是「更难」。B_death ∝ 1/a (X_req ∝ n²
// 且 n = βB²/(4μ₀kT))。
//
// 出处: 上游 def B_death(a) 【MF-M5★】。它的数值与 1/a 标度 (以及容差) 由 anchor_test.go
// 从锚点文件读进来比对 —— 不在本包里写死, 否则就变成自证。
func BDeath(a float64) float64 {
	nCross := (NTLawson / Tau0(a)) * math.Sqrt(Floor)
	return math.Sqrt(4 * MU0 * KTJ * nCross / Beta)
}

// ChiMu 是 μ 门裕度 χ_μ = X_req/FLOOR。
//
// > 1 ⟹ 可行; < 1 ⟹ μ 窗口关闭 (无解, 不是「更难」)。
//
// 出处: 上游 def chi_mu(B, a) = X_req(B,a)/FLOOR 【FC4 / FC11b, 报告 M4】。它的数值与
// 契约 §3 给的容差由 anchor_test.go 从锚点文件读进来比对。
func ChiMu(b, a float64) float64 { return XReq(b, a) / Floor }
