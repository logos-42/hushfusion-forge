// Package physics: 装置参数化、静磁学、场度量。
//
// FROZEN INTERFACE (v0.1) — owner: stage A (见 internal/owners/owners.go)。
// 由 stage owner 在 magnet.go / metrics.go / geometry.go 中实现。
//
// 这里的一切都是*真空场*性质。没有 plasma。
package physics

import (
	"math/rand"

	"github.com/logos-42/hushfusion-forge/internal/config"
)

// Coil 是单个圆形丝环, 其轴位于 z 轴上。
type Coil struct {
	Radius  float64 `json:"radius_m"`
	Z       float64 `json:"z_m"`
	Current float64 `json:"current_A"`
}

// Solver 计算一组线圈在柱坐标采样点上的 |B|。
//
// 必须存在两种实现, 并且它们必须一致:
//   - AnalyticSolver: 通过完全椭圆积分的精确闭式解 (默认)
//   - DiscreteSolver: 对线段直接做 Biot-Savart 求和
//
// 它们不共享任何代码路径, 这正是第二个能作为第一个的独立检验的原因
// (见 cmd/forge verify)。
type Solver interface {
	Name() string
	Magnitude(coils []Coil, r, z []float64) []float64
}

// AnalyticSolver 是精确的圆环丝求解器 (椭圆积分)。
type AnalyticSolver struct{}

// DiscreteSolver 对每个环的 NSeg 条直线段做 Biot-Savart 求和。
type DiscreteSolver struct{ NSeg int }

// EllipticKE 返回第一类与第二类完全椭圆积分 K(m) 和 E(m), 参数
// m = k^2 属于 [0, 1)。
//
// 要求的算法 (AGM, 算术-几何平均) —— 用它, 不要近似:
//
//	a0 = 1, b0 = sqrt(1-m), c0 = sqrt(m)
//	a_{n+1} = (a_n + b_n)/2
//	b_{n+1} = sqrt(a_n * b_n)
//	c_{n+1} = (a_n - b_n)/2
//	K(m) = pi / (2 * a_N)
//	E(m) = K(m) * (1 - sum_{n=0..N} 2^(n-1) * c_n^2)
//
// 其中求和式的 n = 0 项为 c_0^2 / 2。迭代至 c_n ~ 0 (通常 5-10 次迭代到机器精度)。
func EllipticKE(m float64) (k, e float64) { return ellipticKE(m) }

// LoopField 返回给定半径与电流的单个圆形丝环在点 (r, z) 处的 (B_r, B_z), 该环位于
// 平面 z = 0 内。
//
// 闭式解 (Simpson et al., NASA/TM-2001-211135), 其中
//
//	alpha2 = a^2 + r^2 + z^2 - 2*a*r
//	beta2  = a^2 + r^2 + z^2 + 2*a*r
//	m      = 1 - alpha2/beta2
//	C      = mu0*I/pi
//
//	B_r = C*z/(2*alpha2*beta*r) * [ (a^2 + r^2 + z^2)*E(m) - alpha2*K(m) ]
//	B_z =   C  /(2*alpha2*beta) * [ (a^2 - r^2 - z^2)*E(m) + alpha2*K(m) ]
//
// B_r 的分母含 1/r 因子。本行注释此前遗漏了它 (实现里一直在), 这一缺陷已由三方独立
// 确认: golden 场样本 (testdata/) + 直接数值积分 + Python oracle。缺了 1/r, 每个离轴
// B_r 都会差一个 r 因子。
//
// 在轴上 (r -> 0) 必须使用精确极限以避免 0/0:
//
//	B_r = 0,  B_z = mu0*I*a^2 / (2*(a^2 + z^2)^1.5)
//
// 轴上分支由 testdata/ 中的 golden 场样本覆盖。
//
// 采样点到最近导线点的距离小于 5e-3 m 时, alpha2 会被钳到那个下限 (见
// ProximityFloor): 该钳制对**任意**采样点生效, 不只是线圈中心, 所以与 Python 参考实现
// 做跨语言场比较时必须带 `--proximity-floor 0.005`。
func LoopField(radius, current, r, z float64) (br, bz float64) {
	return loopField(radius, current, r, z)
}

// LoopFieldDiscrete 是独立实现: 对 nSeg 条近似该环的直线段直接做 Biot-Savart 求和。
// 在离导线较远的点上, nSeg >= 512 时它必须与 LoopField 相对一致到 < 1e-9。
func LoopFieldDiscrete(radius, current, r, z float64, nSeg int) (br, bz float64) {
	return loopFieldDiscrete(radius, current, r, z, nSeg)
}

// CoilsetField 叠加每个线圈的场 (每个平移到它自己的 z)。
func CoilsetField(coils []Coil, r, z []float64) (br, bz []float64) {
	return coilsetField(coils, r, z)
}

// OnAxisField 返回轴上的 |B| (r = 0)。精确, 且比一般求值更省: 只需要 LoopField 的
// 轴上分支。
func OnAxisField(coils []Coil, z []float64) []float64 {
	return onAxisField(coils, z)
}

// Grids 是堆叠起来的求值采样点:
// [轴上 (r=0) | 中平面体积 | 元胞体积]。
type Grids struct {
	StackR     []float64 // 每个采样点的柱坐标半径
	StackZ     []float64 // 每个采样点的轴向坐标
	NAxis      int       // 轴上采样点占据 Stack[0:NAxis]
	NMid       int       // 中平面体积占据 Stack[NAxis : NAxis+NMid]
	AxisZ      []float64 // 轴上采样位置, 供报告用
	AxisInCell []bool    // |AxisZ[i]| <= spec.ZCell
	MidR       []float64
	CellR      []float64
	CellZ      []float64
}

// BuildGrids 精确镜像 Python 参考实现 (forge/physics/geometry.py), 包括拼接采样点的
// 次序:
//
//	axis   : linspace(-ZAxisMax, +ZAxisMax, NAxis), r = 0
//	mid    : meshgrid(linspace(0, RPlasma, NVolR), linspace(-ZMid, ZMid, 5), "ij")
//	cell   : meshgrid(linspace(0, RPlasma, NVolR), linspace(-ZCell, ZCell, NVolZ), "ij")
//
// 注意中平面体积使用固定的 5 个轴向采样点。采样次序很重要: golden 比较按
// [NAxis, NMid] 切分这个堆叠数组。
func BuildGrids(spec config.Spec) Grids {
	return buildGrids(spec)
}

// Metrics 是一组线圈中与目标函数相关的场度量。
//
// JSON tag 是 FROZEN 的: 它们是跨语言交换格式, 必须与
// forge/physics/plasma_model.py 逐键一致。
type Metrics struct {
	BMidT                 float64 `json:"B_mid_T"`
	BThroatT              float64 `json:"B_throat_T"`
	ZThroatM              float64 `json:"z_throat_m"`
	MirrorRatio           float64 `json:"mirror_ratio"`
	VolumeGood            float64 `json:"volume_good"`
	Ripple                float64 `json:"ripple"`
	BCoilMaxT             float64 `json:"B_coil_max_T"`
	MinCoilGapM           float64 `json:"min_coil_gap_m"`
	MinClearanceM         float64 `json:"min_clearance_m"`
	CostProxy             float64 `json:"cost_proxy"`
	CoilProximityFloorHit bool    `json:"coil_proximity_floor_hit"`
	NCoils                int     `json:"n_coils"`
	MU0                   float64 `json:"mu0"`
}

// MetricsFor 在一次对 g.StackR/StackZ 的场遍历中计算每一个度量。
//
// 定义 (必须与 Python 参考实现完全一致):
//
//	B_mid    = 中平面体积采样点上 |B| 的平均值
//	B_throat = 整个采样跨度上轴上 |B| 的最大值
//	R        = B_throat / B_mid
//	V_good   = 元胞体积采样点中 |B| <= ConfineFactor*B_mid 的比例
//	ripple   = AxisRipple(元胞内的轴上采样点, B_mid, 0.05)
//	B_coil   = 对每个线圈 k, 取该线圈位置上全部*其它*线圈场强的**模之和**
//	           Σ_{j≠k} |B_j(r_k,z_k)|, 再加 spec.SelfField(),
//	           然后对 k 取最大值: max_k( Σ_{j≠k} |B_j(r_k,z_k)| + SelfField() )。
//	           注意这是模之和, 而不是先按矢量求和再取模的 |Σ_j B_j|。
//	cost     = sum_k I_k^2 * r_k
//	min_gap  = 两个线圈中心之间最小的 3-D 距离
//
// 如果有另一个线圈位于 5e-3 m 之内, 闭式解是奇异的: 把它钳住 (floor) 并置
// CoilProximityFloorHit = true (目标函数会惩罚这种几何; 但有限数值仍必须进入记录,
// 而不是 NaN)。
//
// 注意这个 5e-3 m 钳制对**任意**采样点生效, 不只是线圈中心 (见 ProximityFloor):
// 与 Python 参考实现做跨语言场比较时必须带 `--proximity-floor 0.005`, 否则 1e-2 量级
// 的分歧会周期性出现, 那是约定不一致而不是 bug (Phase 0 已实测)。
func MetricsFor(coils []Coil, spec config.Spec, g Grids, s Solver) Metrics {
	return metricsFor(coils, spec, g, s)
}

// AxisRipple 是轴上非单调结构的归一化幅度。
//
// 对相间的连续内部极值 (max, min) 求 |B_peak - B_adjacent_valley| 之和, 只保留比
// prominence*BMid 更深的那些结构, 然后除以 BMid。单调或单峰剖面精确返回 0。
// 内部极值通过三点比较 B[i-1] < B[i] > B[i+1] 找到。
func AxisRipple(bAxisCell []float64, bMid, prominence float64) float64 {
	return axisRipple(bAxisCell, bMid, prominence)
}

// MinCoilGap 是两个线圈中心之间的最小距离; 少于两个线圈时返回 +Inf。
func MinCoilGap(coils []Coil) float64 { return minCoilGap(coils) }

// MinClearance 是导体面到约束区域的最小净空 [m] (0.1.2 新增)。
//
// 定义 (docs/version-0.1.2.md §1):
//
//	约束区域 R_cell = { 0 <= r <= spec.RPlasma, |z| <= spec.ZCell }   （中心元胞, 与
//	                    volume_good 的采样区域同一口径; 不是 ZMid —— 那只是 B_mid 的取样窗）
//	导体面         = 丝环 (r_k, z_k) 起、minor 半径 spec.TPack/2 的圆环面
//	c_k            = dist((r_k, z_k), R_cell) − spec.TPack/2        （可以有符号: <0 = 已侵入）
//	min_clearance  = min_k c_k
//
// dist 的闭式解利用 R_cell 在柱坐标下是**乘积区域**这一事实:
//
//	dist = sqrt( max(0, a − RPlasma)^2 + max(0, |z| − ZCell)^2 )      （精确）
//
// 为什么要它: 0.1.0 的最优解把导体放在距中场采样点 4.196 mm 处, 而当时的目标函数
// 没有一项阻止它。少于一个线圈时返回 +Inf (与 MinCoilGap 同一约定)。
func MinClearance(coils []Coil, spec config.Spec) float64 { return minClearance(coils, spec) }

// VectorToCoils 解码一个设计向量, 裁剪到 spec 边界并按 z 排序 (规范形式: 消除 K! 排列
// 简并)。若 len(x) != spec.NParams() 则返回错误。
func VectorToCoils(x []float64, spec config.Spec) ([]Coil, error) {
	return vectorToCoils(x, spec)
}

// CoilsToVector 把线圈 (任意顺序) 编码成规范设计向量。
func CoilsToVector(coils []Coil) []float64 { return coilsToVector(coils) }

// RandomDesign 在规范次序下对搜索盒子做均匀采样。
func RandomDesign(rng *rand.Rand, spec config.Spec) []float64 {
	return randomDesign(rng, spec)
}
