// 真空中圆形丝环的静磁学 (stage A)。
//
// api.go 中冻结声明的实现。两条彼此独立的代码路径被有意放在这里:
//
//	loopField            精确闭式解, 完全椭圆积分 (AGM)
//	loopFieldDiscrete    对直线段直接做 Biot-Savart 求和
//
// 它们不共享任何一行算术, 这正是第二个能构成对第一个的真正交叉检验的原因
// (physics_test.go 断言在离导线较远的点上, nSeg = 512 时二者相对一致到 < 1e-9)。
package physics

import (
	"math"

	"github.com/logos-42/hushfusion-forge/internal/config"
)

const (
	// axisTol 是低于它的半径的采样点将用精确的轴上极限来求值。与受保护的 Python
	// 参考实现同一个值 (forge/physics/magnetic_field.py: _AXIS_TOL); 一般闭式解在
	// r -> 0 时 B_r 有 0/0 抵消, 所以极限分支不只是一项优化, 而是那里唯一有数值意义
	// 的表达式。
	axisTol = 1e-9

	// ProximityFloor 是闭式解中 1/alpha^2 项开始爆掉时的采样点到导线距离 [m]。任何
	// 到最近导线点的距离小于它的采样点都按恰好这么远来求值, 于是场保持有限 (退化几何
	// 上的一个度量必须以数字进入记录, 而不是 NaN)。
	//
	// 适用范围: 它对**任意**采样点都生效, 不只是线圈中心 —— 只要某个采样点到某条导线
	// 的距离 < 5e-3 m, alpha2 就被钳住。
	//
	// 跨语言比较: 与 Python 参考实现做场比较时必须带 `--proximity-floor 0.005`, 否则
	// 1e-2 量级的分歧会周期性出现 —— 那是约定不一致, 不是 bug (Phase 0 已实测)。
	//
	// 与受保护的 Python 参考实现 (forge/physics/plasma_model.py) 中的 PROXIMITY_FLOOR
	// 是同一个常量, 也是 MetricsFor 用来置 CoilProximityFloorHit 的阈值。
	ProximityFloor = 5e-3

	// tiny 镜像参考实现里的 np.maximum(x, 1e-300) 保护: 它只需让一个除法保持有限,
	// 它从来不是物理尺度。
	tiny = 1e-300

	// ellipMMax 把 m = k^2 限制在略低于 K(m) 发散处的 1 以下。
	ellipMMax = 1.0 - 1e-12

	// keMaxIter / keEps 界定椭圆积分的 AGM 迭代。AGM 每步都把精度翻倍。keEps 是
	// 相对于 a_n 的: 停止判据必须是相对的, 因为 a_n - b_n 无法分辨低于 eps*a_n 的
	// 差异, 所以绝对判据永远不会触发, 迭代会继续添加纯舍入噪声的 2^(n-1) c_n^2 项
	// (随着 2^(n-1) 增长, 那些项是被放大而不是被抑制)。
	keMaxIter = 64
	keEps     = 1e-16

	// defaultNSeg 在 DiscreteSolver 的 NSeg <= 0 (结构体的零值) 时使用。它与参考
	// 实现的 loop_field_discrete 默认值一致。
	defaultNSeg = 1440
)

// ellipticKE 通过算术-几何平均返回 K(m) 与 E(m), 与 api.go 中的规定完全一致:
//
//	a0 = 1, b0 = sqrt(1-m), c0 = sqrt(m)
//	K(m) = pi/(2 a_N)
//	E(m) = K(m) * (1 - sum_{n=0..N} 2^(n-1) c_n^2),  n = 0 项 = c0^2/2
func ellipticKE(m float64) (k, e float64) {
	if m < 0 {
		m = 0
	}
	if m >= 1 {
		// 落在冻结定义域 [0, 1) 之外: K 发散, E -> 1。按数学极限上报, 而不是伪造
		// 成一个有限值。调用方 (loopField) 严格保持在 [0, 1 - 1e-12) 之内。
		return math.Inf(1), 1
	}
	a := 1.0
	b := math.Sqrt(1 - m)
	c := math.Sqrt(m)
	// 求和式的 n = 0 项: 2^(-1) c0^2。
	sum := 0.5 * c * c
	pw := 1.0 // 下一个 n 所对应的 2^(n-1)
	prev := math.Inf(1)

	for n := 1; n <= keMaxIter; n++ {
		an := 0.5 * (a + b)
		cn := 0.5 * (a - b)
		bn := math.Sqrt(a * b)
		a, b, c = an, bn, cn
		sum += pw * c * c
		abs := math.Abs(c)
		// 已收敛到机器精度: |c_n| 处在 (或低于) a_n - b_n 的舍入底噪, 也就是它不再
		// 下降。此后再加的任何东西都是被 2^(n-1) 放大的噪声。
		if c == 0 || abs <= keEps*a || abs >= prev || pw > 1e300 {
			break
		}
		prev = abs
		pw *= 2
	}
	k = math.Pi / (2 * a)
	e = k * (1 - sum)
	return k, e
}

// loopField 是单个圆形丝的精确离轴/轴上场, 按 api.go 中冻结的闭式解。该环位于平面
// z = 0 内。
//
// 给评审者的说明: api.go 的文档注释曾经把 B_r 写成不含 1/r 因子的形式。冻结公式是
// 标准 Simpson et al. 表达式
//
//	B_r = C*z/(2*alpha2*beta*r) * [ (a^2 + r^2 + z^2)*E(m) - alpha2*K(m) ]
//
// testdata/ 里的 golden 数据正是用它生成的 (golden_field_samples.json 的解析部分逐位
// 复现它)。1/r 在这里被保留, 否则每一个离轴 B_r 都会差一个 r 因子 (golden 比较会相差
// 约 1e-2 相对误差, 而不是 1e-9)。
//
// 那个 1/r 因子在 api.go 的冻结注释里曾缺失 (实现里一直有); 这一缺陷已由三方独立确认:
// golden 场样本 + 直接数值积分 + Python oracle。
//
// 另外: 这里的 alpha2 钳制对**任意**采样点都生效, 不只是线圈中心 (见 ProximityFloor)。
func loopField(radius, current, r, z float64) (br, bz float64) {
	a2 := radius * radius
	r2z2 := r*r + z*z
	alpha2 := a2 + r2z2 - 2*radius*r // 到最近导线点的距离平方
	if alpha2 < proximityFloor2 {
		alpha2 = proximityFloor2
	}
	beta2 := a2 + r2z2 + 2*radius*r
	beta := math.Sqrt(math.Max(beta2, tiny))
	m := 1 - alpha2/math.Max(beta2, tiny)
	if m < 0 {
		m = 0
	} else if m > ellipMMax {
		m = ellipMMax
	}

	if math.Abs(r) < axisTol {
		// 精确极限: B_r = 0, B_z = mu0*I*a^2 / (2*(a^2+z^2)^1.5)。
		den := math.Max(a2+z*z, tiny)
		return 0, config.MU0 * current * a2 / (2 * math.Pow(den, 1.5))
	}

	ek, ee := ellipticKE(m)
	c := config.MU0 * current / math.Pi
	denom := 2 * alpha2 * beta
	br = c * z * ((a2+r2z2)*ee - alpha2*ek) / (denom * r)
	bz = c * ((a2-r2z2)*ee + alpha2*ek) / denom
	return br, bz
}

// proximityFloor2 是 ProximityFloor 的平方 (钳制作用在 alpha^2 上)。
const proximityFloor2 = ProximityFloor * ProximityFloor

// loopFieldDiscrete 是独立的 Biot-Savart 检验: 环被替换成 nSeg 条直线段, 每条由它的
// 中点与切向长度表示, 场是 (mu0 I/4pi) dl x (r-r')/|r-r'|^3 各项之和。它在 nSeg 上
// 几何收敛 (解析周期被积函数上的中点法则), 这正是它与闭式解的一致是对两个实现而言的
// 陈述、而不是对任一求积法则的陈述的原因。
func loopFieldDiscrete(radius, current, r, z float64, nSeg int) (br, bz float64) {
	if nSeg < 1 {
		nSeg = defaultNSeg
	}
	dphi := 2 * math.Pi / float64(nSeg)
	segLen := 2 * math.Pi * radius / float64(nSeg)
	pre := config.MU0 * current / (4 * math.Pi)

	var sumR, sumZ float64
	for i := 0; i < nSeg; i++ {
		phi := (float64(i) + 0.5) * dphi
		sinp, cosp := math.Sincos(phi)

		// 环上的线段中点及其切向量 dl。
		cx := radius * cosp
		cy := radius * sinp
		dlx := -segLen * sinp
		dly := segLen * cosp

		// 从线段到观测点 (r, 0, z) 的向量。
		dx := r - cx
		dy := -cy
		dz := z
		dist2 := dx*dx + dy*dy + dz*dz
		dist := math.Sqrt(dist2)
		if dist < 1e-12 {
			dist = 1e-12
		}
		d3 := dist * dist * dist

		// (dl x delta)_r = dly*dz, (dl x delta)_z = dlx*dy - dly*dx。
		sumR += dly * dz / d3
		sumZ += (dlx*dy - dly*dx) / d3
	}
	return pre * sumR, pre * sumZ
}

// coilsetField 叠加每一个线圈, 每个平移到它自己的 z。
func coilsetField(coils []Coil, r, z []float64) (br, bz []float64) {
	if len(r) != len(z) {
		panic("physics: coilsetField called with mismatched r/z lengths")
	}
	n := len(r)
	br = make([]float64, n)
	bz = make([]float64, n)
	for _, c := range coils {
		for i := 0; i < n; i++ {
			cbr, cbz := loopField(c.Radius, c.Current, r[i], z[i]-c.Z)
			br[i] += cbr
			bz[i] += cbz
		}
	}
	return br, bz
}

// onAxisField 返回轴上的 |B|, 只使用精确的轴上分支。取绝对值的是*和* (负电流线圈会
// 做减法)。
func onAxisField(coils []Coil, z []float64) []float64 {
	out := make([]float64, len(z))
	for _, c := range coils {
		a2 := c.Radius * c.Radius
		for i, zi := range z {
			dz := zi - c.Z
			out[i] += config.MU0 * c.Current * a2 / (2 * math.Pow(a2+dz*dz, 1.5))
		}
	}
	for i := range out {
		out[i] = math.Abs(out[i])
	}
	return out
}

// Name 实现 Solver。这些字符串与受保护的 Python 参考实现
// (AnalyticalVacuumSolver.name / DiscreteFilamentSolver.name) 以及
// testdata/golden_field_samples.json 的 "solver" 字段一致。
func (AnalyticSolver) Name() string { return "analytic-vacuum-loops" }

// Magnitude 实现 Solver: 叠加后线圈组的 sqrt(B_r^2 + B_z^2)。
func (AnalyticSolver) Magnitude(coils []Coil, r, z []float64) []float64 {
	br, bz := coilsetField(coils, r, z)
	out := make([]float64, len(br))
	for i := range out {
		out[i] = math.Hypot(br[i], bz[i])
	}
	return out
}

// Name 实现 Solver。
func (DiscreteSolver) Name() string { return "discrete-filaments" }

// Magnitude 用独立的线段求和实现 Solver: 各段的场先按矢量求和, 然后再取模
// (与参考实现的 DiscreteFilamentSolver.field_magnitude 一致)。
// NSeg <= 0 回落到 defaultNSeg。
func (d DiscreteSolver) Magnitude(coils []Coil, r, z []float64) []float64 {
	if len(r) != len(z) {
		panic("physics: DiscreteSolver.Magnitude called with mismatched r/z lengths")
	}
	n := len(r)
	brs := make([]float64, n)
	bzs := make([]float64, n)
	for _, c := range coils {
		for i := 0; i < n; i++ {
			cbr, cbz := loopFieldDiscrete(c.Radius, c.Current, r[i], z[i]-c.Z, d.NSeg)
			brs[i] += cbr
			bzs[i] += cbz
		}
	}
	out := make([]float64, n)
	for i := range out {
		out[i] = math.Hypot(brs[i], bzs[i])
	}
	return out
}
