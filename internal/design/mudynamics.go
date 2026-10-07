// mudynamics.go —— 契约 §2 的 μ 动力学 (TD19–TD21) 与判决量 D1/D3 的闭式解。
//
// 逐字移植上游:
//   - ProjectionPhysics/artifacts/mudynamics/{report.json,summary.txt} (TD1–TD21)
//   - ProjectionPhysics/artifacts/fusionroadmap/{report.json,summary.txt} (D1/D2/D3)
//
// 诚实边界 (契约 §6): 状态方程 μ_{n+1} = μ_n + η(1−μ_n) 是**模型选择**, 不是物理定律;
// η 的物理来源仍是第二输入缺口 —— 缺口从「η 是哪来的」移到「抹平功率是哪来的」。
package design

import "math"

// MuAfterSteps 是 μ 状态方程的闭式解: μ_n = 1 − (1−η)^n·(1−μ₀)。
//
// 出处: 上游 mudynamics report.json 的 "closed_form" 与 def/summary N4 (闭式解 vs 递推
// 残差 7.77e-16)、N5 (300 步不可达 / 单调 / 质量 > 0)。对应 TD8: μ 无限逼近 1 但
// 有限步走不到 1。
func MuAfterSteps(mu0, eta float64, n int) float64 {
	return 1 - math.Pow(1-eta, float64(n))*(1-mu0)
}

// WindowCloseStep 是窗口关闭步的**解析**值:
//
//	n_close = ln((1−μ₀)/(m_e/m_i)) / ln(1/(1−η))        [η < 1]
//
// 判据本体: (1−η)^n·(1−μ₀) ≤ m_e/m_i 【TD21】。
//
// 解出来是**实数**: 在上游那组参数 (η=0.05、μ₀=0、D-T 的 m_e/m_i) 下它落在
// (164, 165] 里, 而窗口真正关闭发生在**整数步** —— 判据只在整数 n 上有意义。
// 所以本函数返回解析实数, 整数语义见 FirstClosedStep。
// 具体数值不在本包里写死: 上游字段与解析复算都记在 testdata/projectionphysics_anchors.json,
// 由 anchor_test.go 逐条断言。
//
// 边界 (全部显式, 不返回 NaN 冒充一个数):
//   - η ≥ 1  ⟹ +Inf (一步到 1, 窗口不存在, 契约 §2 明写)
//   - η == 0  ⟹ +Inf (μ 不前进, 窗口永不关闭)
//   - η <  0  ⟹ +Inf (μ 反而远离天花板, 窗口永不关闭)
//   - μ₀ ≥ 1  ⟹ 0    (已经贴着/越过天花板, 0 步)
//   - m_e/m_i ≤ 0 ⟹ +Inf (判据恒不成立, 上游不会出现; 不猜一个值)
//   - m_e/m_i > 0 且解析值为负 ⟹ 0 (判据当下已成立)
//
// 出处: 上游 mudynamics summary.txt N11 行 (关闭步 n*) 与
// report.json results.N15_frc_interface (D-T 窗口关闭步 n* 与阈值 μ ≥ 0.99978)。
func WindowCloseStep(mu0, eta, meOverMi float64) float64 {
	if eta >= 1 || eta <= 0 || meOverMi <= 0 {
		return math.Inf(1)
	}
	if mu0 >= 1 {
		return 0
	}
	n := math.Log((1-mu0)/meOverMi) / math.Log(1/(1-eta))
	if n < 0 {
		return 0
	}
	return n
}

// FirstClosedStep 是窗口首次关闭的**整数**步 = ceil(解析值)。
//
// 上游字段给的是整数步, 解析式给的是实数 —— 两者都对, 说的是不同的事:
// 判据 (1−η)^n(1−μ₀) ≤ m_e/m_i 只对整数 n 求值。下面两组数**只是示例**
// (取自上游 N15 字段与锚点文件里的解析复算), 本包不把它们当常量: 步数一律由
// ceil(WindowCloseStep) 算出来, 上游字段与两条递推事实由 anchor_test.go 断言。
//
// 逐位递推见证 (上游那组参数 η=0.05、μ₀=0, D-T):
//
//	上一步 ⟹ 1−μ 仍 > FLOOR   未关
//	这一步 ⟹ 1−μ ≤ FLOOR       关
//
// 永不关闭 (见 WindowCloseStep 的边界) 时返回 -1 —— 那是「不是一个步数」,
// 不是「第 0 步」, 所以不用 0 冒充。
func FirstClosedStep(mu0, eta, meOverMi float64) int {
	n := WindowCloseStep(mu0, eta, meOverMi)
	if math.IsInf(n, 1) {
		return -1
	}
	return int(math.Ceil(n))
}

// SinkEta is CR9 自抹平增益 η_sink(λ) = 2λ − λ²。
//
// 出处: 上游 artifacts/sinkmuopt/report.json 的「候选机制（诚实标注）」——
// 向心收缩一次把起伏抹掉 2λ−λ² (CR9 sinkContract_fluctuation_scale 的 (1−λ)² 实现)。
// 诚实边界: λ(环流电子收敛度)是**输入参数**(第二输入缺口), 不是推导值;
// η_sink = 2λ−λ² 是 CR9 平方衰减的**候选实现**, 不是唯一映射。
func SinkEta(lam float64) float64 {
	return 2*lam - lam*lam
}

// SinkMuAfterSteps 是带自抹平的 μ 递推数值迭代: μ' = μ + [η_ext + η_sink·(1−μ)]·(1−μ)。
//
// 自抹平随 (1−μ) 衰减 (TD18: 满增益⟺零代价⟺区域已平坦 —— 起伏→0 时抹平失去对象)。
// 这是**数值迭代**, 不是闭式 —— η 随 μ 变, 闭式解不存在 (与 MuAfterSteps 不同)。
//
// 诚实边界: 上游数值验证 (sinkmuopt 报告 C1/C3):
//
//	C1 到达工作窗口 μ=0.999: 132 步(纯外部) → 85 步(+自抹平 λ=0.3)
//	C3 外部 RMF 减半仍可达: 267 步(纯外部) → 150 步(+自抹平)
//	负面发现: η_sink 恒定会破坏硬界(μ 冲过 FC11 到 1); 必须随 (1−μ) 衰减。
func SinkMuAfterSteps(mu0, etaExt, lam float64, n int) float64 {
	mu := mu0
	for i := 0; i < n; i++ {
		eta := etaExt + SinkEta(lam)*(1-mu)
		mu = mu + eta*(1-mu)
	}
	return mu
}

// SinkMuWorkSteps 是带自抹平的 μ 到达工作窗口 μ_work 的步数 (数值迭代, 上限 maxSteps)。
//
// 工作窗口 μ_work = 0.999 是聚变级目标 (FC11 天花板 0.99978 之下的工作区)。
// 返回: 首次 μ ≥ μ_work 的步数; 若 maxSteps 内未达返回 -1 (与 FirstClosedStep 同语义)。
//
// 出处: 上游 sinkmuopt 报告的 C1 (132→85 步)。
func SinkMuWorkSteps(mu0, etaExt, lam, muWork float64, maxSteps int) int {
	mu := mu0
	for i := 0; i <= maxSteps; i++ {
		if mu >= muWork {
			return i
		}
		eta := etaExt + SinkEta(lam)*(1-mu)
		mu = mu + eta*(1-mu)
	}
	return -1
}

// LockingFactor 是锁定因子 1/√(1−μ_n)。
//
// 分母恒正 (TD8: μ < 1) 且随 n 单调递增 (TD20: 逼近发散点) —— 上游 N15 三条
// 布尔见证。μ ≥ 1 时返回 +Inf (发散点本身), 不返回 NaN。
//
// 出处: 上游 report.json results.N15_frc_interface 与 fusionroadmap R2
// 「τ_E 增益 = 1/√(1−μ)」。
func LockingFactor(mu float64) float64 {
	if mu >= 1 {
		return math.Inf(1)
	}
	return 1 / math.Sqrt(1-mu)
}

// MuFromGain 是 FC4 的**反解**: 由 τ_E 增益 g 反解 μ = 1 − 1/g²。
//
// g = 10 ⟹ μ = 0.99。与 LockingFactor 互为逆 (g = 1/√(1−μ))。
//
// 出处: 合同 §2「由增益反解 μ: μ = 1 − 1/g²」【FC4 反解】。
func MuFromGain(g float64) float64 { return 1 - 1/(g*g) }

// Rci 是判决量 D1: 回旋共振频移 R_ci(μ) = 1/(1−μ) − 1。
//
// 定义即口径 —— 本层只报「怎么测」, 不做预测。零假设 (标准 MHD) 是 R_ci = 0。
// μ ≥ 1 时返回 +Inf。
//
// 出处: FC9; 上游 fusionroadmap R1_D1_cyclotron_signature。
func Rci(mu float64) float64 {
	if mu >= 1 {
		return math.Inf(1)
	}
	return 1/(1-mu) - 1
}

// MuMinFromDelta 是 D1 的可探测 μ 下限: μ_min(δ) = δ/(1+δ) (δ = 诊断相对精度)。
//
// 出处: FC9; 上游 fusionroadmap R1「诊断精度 → 可探测 μ 下限」与 summary.txt
// 「诊断 δ ⟹ 可探测 μ 下限」(上游 summary.txt R1 行)。与 Rci 互为逆: Rci(MuMinFromDelta(δ)) = δ。
func MuMinFromDelta(delta float64) float64 { return delta / (1 + delta) }

// PowerMultiple 是判决量 D3 的功率倍数: μ ∝ P^k 时, 跨过 decades 个数量级所需的
// 输入功率倍数 = (10^decades)^(1/k)。
//
// 上游口径 (Δ ≈ 4 个数量级, 从桌面 μ≈1e-4 到聚变级 μ≈0.999):
//
//	k = 1    ⟹ 1e4    (可行)
//	k = 0.5  ⟹ 1e8    (五年计划内不可行)
//	k = 0.25 ⟹ 1e16   (判死刑)
//	k = 0.1  ⟹ 1e40
//
// k ≤ 0 时返回 +Inf (幂律无意义, 不返回 NaN)。
//
// 出处: 上游 fusionroadmap R3_D3_power_scaling「从桌面 μ≈1e-4 到聚变级 μ≈0.999
// （Δ≈4 个数量级）所需输入功率倍数」。
func PowerMultiple(k, decades float64) float64 {
	if k <= 0 {
		return math.Inf(1)
	}
	return math.Pow(math.Pow(10, decades), 1/k)
}
