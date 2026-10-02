// 一组线圈的场结构度量 (stage A)。
//
// api.go 中 metricsFor / axisRipple / minCoilGap 的实现, 与
// forge/physics/plasma_model.py 逐键一致。这里的一切都是真空场性质: 无 plasma、
// 无压强、无平衡。
package physics

import (
	"math"

	"github.com/logos-42/hushfusion-forge/internal/config"
)

// RippleProminence 是轴上结构不算作 ripple 的相对 prominence 阈值 (以 B_mid 为单位)。
// 冻结: Python 参考实现以默认参数调用 axisRipple(..., prominence=0.05), api.go 的
// metricsFor 定义也点明同一个 0.05。
const RippleProminence = 0.05

// bMidFloor 在退化设计产生零中平面场时, 把 B_mid 挡在 mirror_ratio 的分母与
// volume_good 的阈值之外。与参考实现同一个 1e-9 下限。
const bMidFloor = 1e-9

// metricsFor 在一次对 g.StackR/g.StackZ 的遍历中计算每一个度量 (另外还有 K 次单线圈
// 辅助调用来取导体场, 与参考实现完全一样)。堆叠数组只采样一次, 然后按
// [NAxis, NMid] 切片。
func metricsFor(coils []Coil, spec config.Spec, g Grids, s Solver) Metrics {
	m := Metrics{
		NCoils: len(coils),
		MU0:    config.MU0,
		// 在下面会被覆盖; 在这里声明是为了即使提前退出也仍带着一个有限的间距。
		MinCoilGapM:   minCoilGap(coils),
		MinClearanceM: minClearance(coils, spec),
	}
	if s == nil || len(g.StackR) == 0 || len(g.StackR) != len(g.StackZ) {
		return m
	}

	// 对堆叠采样点的单次场遍历。
	bAll := s.Magnitude(coils, g.StackR, g.StackZ)

	// 按冻结布局切片, 并做钳制, 使行为异常的 solver 无法让度量路径 panic (有限的记录
	// 胜过崩溃)。
	n := len(bAll)
	nAxis := g.NAxis
	if nAxis > n {
		nAxis = n
	}
	nMid := g.NMid
	if nAxis+nMid > n {
		nMid = n - nAxis
	}
	bAxis := bAll[:nAxis]
	bMidSamples := bAll[nAxis : nAxis+nMid]
	bCell := bAll[nAxis+nMid:]

	// B_mid = 中平面体积上 |B| 的平均值。
	bMid := 0.0
	for _, v := range bMidSamples {
		bMid += v
	}
	if len(bMidSamples) > 0 {
		bMid /= float64(len(bMidSamples))
	}

	// B_throat = 整个采样跨度上轴上 |B| 的最大值 (并列时第一个索引胜出, 同
	// numpy.argmax)。
	bThroat := 0.0
	iThroat := 0
	for i, v := range bAxis {
		if i == 0 || v > bThroat {
			bThroat = v
			iThroat = i
		}
	}

	m.BMidT = bMid
	m.BThroatT = bThroat
	floor := math.Max(bMid, bMidFloor)
	m.MirrorRatio = bThroat / floor
	if iThroat < len(g.AxisZ) {
		m.ZThroatM = g.AxisZ[iThroat]
	}

	// volume_good = 元胞体积中 |B| <= ConfineFactor*B_mid 的比例。
	good := 0
	for _, v := range bCell {
		if v <= spec.ConfineFactor*floor {
			good++
		}
	}
	if len(bCell) > 0 {
		m.VolumeGood = float64(good) / float64(len(bCell))
	}

	// ripple 只取落在元胞内部的轴上采样点。
	axisCell := make([]float64, 0, len(bAxis))
	for i := range bAxis {
		if i < len(g.AxisInCell) && g.AxisInCell[i] {
			axisCell = append(axisCell, bAxis[i])
		}
	}
	m.Ripple = axisRipple(axisCell, bMid, RippleProminence)

	// B_coil_max 与 proximity 标志。
	bOthers, floorHit := conductorField(coils, spec, s)
	m.CoilProximityFloorHit = floorHit
	if len(coils) > 0 {
		peak := math.Inf(-1)
		for _, v := range bOthers {
			if v > peak {
				peak = v
			}
		}
		m.BCoilMaxT = peak + spec.SelfField()
	}

	// cost_proxy = sum_k I_k^2 r_k。
	cost := 0.0
	for _, c := range coils {
		cost += c.Current * c.Current * c.Radius
	}
	m.CostProxy = cost
	return m
}

// conductorField 为每一个线圈返回所有*其它*线圈在该线圈位置产生的场的模之和, 外加
// 是否存在任一线圈对靠得比 ProximityFloor 更近。
//
// 有两个细节被参考实现冻结, 并且有数值影响:
//   - 求和的是模 (不是矢量): sum_j |B_j(x_i)|;
//   - 每个*源*线圈一次场调用, 用它在所有其它线圈的位置上求值; 这是 K 次调用而不是
//     K*(K-1) 次单点调用, 物理完全相同。
func conductorField(coils []Coil, spec config.Spec, s Solver) ([]float64, bool) {
	bOthers := make([]float64, len(coils))
	floorHit := false
	if len(coils) == 0 {
		return bOthers, floorHit
	}
	tr := make([]float64, 0, len(coils)-1)
	tz := make([]float64, 0, len(coils)-1)
	for j, src := range coils {
		targets := make([]int, 0, len(coils)-1)
		tr, tz = tr[:0], tz[:0]
		for i, tgt := range coils {
			if i == j {
				continue
			}
			targets = append(targets, i)
			tr = append(tr, tgt.Radius)
			tz = append(tz, tgt.Z-src.Z)
			if math.Hypot(tgt.Radius-src.Radius, tgt.Z-src.Z) < ProximityFloor {
				floorHit = true
			}
		}
		if len(targets) == 0 {
			continue
		}
		srcOnly := []Coil{{Radius: src.Radius, Z: 0, Current: src.Current}}
		mags := s.Magnitude(srcOnly, tr, tz)
		for k, i := range targets {
			if k < len(mags) {
				bOthers[i] += mags[k]
			}
		}
	}
	return bOthers, floorHit
}

// axisRipple 是轴上非单调结构的归一化幅度, 按冻结定义: 内部极值用三点比较找到,
// 只有比 prominence*bMid 更深的结构才算, 只有相间的连续极值 (max, min) 才被求和,
// 最后结果除以 bMid。单调或单峰剖面精确给出 0。
func axisRipple(bAxisCell []float64, bMid, prominence float64) float64 {
	n := len(bAxisCell)
	if n < 5 || bMid <= 0 {
		return 0
	}
	type extremum struct {
		value float64
		isMax bool
	}
	exts := make([]extremum, 0, n/2)
	for i := 1; i < n-1; i++ {
		left, mid, right := bAxisCell[i-1], bAxisCell[i], bAxisCell[i+1]
		isMax := mid > left && mid > right
		isMin := mid < left && mid < right
		if isMax || isMin {
			exts = append(exts, extremum{value: mid, isMax: isMax})
		}
	}
	if len(exts) < 2 {
		return 0
	}
	total := 0.0
	for k := 0; k+1 < len(exts); k++ {
		if exts[k].isMax == exts[k+1].isMax {
			continue // 不是相间的 (max, min) / (min, max) 极值对
		}
		amp := math.Abs(exts[k].value - exts[k+1].value)
		if amp > prominence*bMid {
			total += amp
		}
	}
	return total / bMid
}

// minCoilGap 是两个线圈中心之间最小的 3-D 距离, 少于两个线圈时为 +Inf。(r, z) 这一对
// 就是轴对称线圈中心之间的自然 3-D 距离, 因为两者处在同一个方位角上。
func minCoilGap(coils []Coil) float64 {
	best := math.Inf(1)
	for i := range coils {
		for j := i + 1; j < len(coils); j++ {
			d := math.Hypot(coils[i].Radius-coils[j].Radius, coils[i].Z-coils[j].Z)
			if d < best {
				best = d
			}
		}
	}
	return best
}
