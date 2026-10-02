// 装置参数化与求值网格 (stage A)。
//
// 精确镜像 forge/physics/geometry.py, 包括堆叠网格的采样次序: golden 比较按
// [NAxis, NMid] 切分堆叠数组, 所以次序和数量都是契约的一部分。
package physics

import (
	"fmt"
	"math"
	"math/rand"
	"sort"

	"github.com/logos-42/hushfusion-forge/internal/config"
)

// midplaneZSamples 是中平面体积固定的轴向采样点数。冻结的 buildGrids 文档注释把它
// 钉在 5 上 (Python 参考实现即使元胞体积用的是 spec.NVolZ, 中平面仍然硬编码为
// linspace(-ZMid, ZMid, 5))。
const midplaneZSamples = 5

// linspace 对应 numpy.linspace(lo, hi, n): n 个均匀间距的值, 两端点都包含, 最后一个
// 被精确置为 hi (numpy 也做同样的收尾赋值, 这消除了端点上累积的舍入误差)。
func linspace(lo, hi float64, n int) []float64 {
	if n <= 0 {
		return nil
	}
	out := make([]float64, n)
	if n == 1 {
		out[0] = lo
		return out
	}
	step := (hi - lo) / float64(n-1)
	for i := range out {
		out[i] = lo + step*float64(i)
	}
	out[n-1] = hi
	return out
}

// buildGrids 按冻结的次序构造堆叠采样点
// [轴上 (r=0) | 中平面体积 | 元胞体积]:
//
//	axis   : linspace(-ZAxisMax, ZAxisMax, NAxis), r = 0
//	mid    : meshgrid(linspace(0, RPlasma, NVolR), linspace(-ZMid, ZMid, 5), "ij")
//	cell   : meshgrid(linspace(0, RPlasma, NVolR), linspace(-ZCell, ZCell, NVolZ), "ij")
//
// 使用 meshgrid(..., "ij") 时*半径*变化最慢, 所以展平后的次序是
// (r_0, z_0), (r_0, z_1), ... , (r_1, z_0), ...
func buildGrids(spec config.Spec) Grids {
	axisZ := linspace(-spec.ZAxisMax, spec.ZAxisMax, spec.NAxis)
	radii := linspace(0, spec.RPlasma, spec.NVolR)
	zMid := linspace(-spec.ZMid, spec.ZMid, midplaneZSamples)
	zCell := linspace(-spec.ZCell, spec.ZCell, spec.NVolZ)

	nMid := len(radii) * len(zMid)
	nCell := len(radii) * len(zCell)

	stackR := make([]float64, 0, len(axisZ)+nMid+nCell)
	stackZ := make([]float64, 0, cap(stackR))
	midR := make([]float64, 0, nMid)
	cellR := make([]float64, 0, nCell)
	cellZ := make([]float64, 0, nCell)

	for _, z := range axisZ {
		stackR = append(stackR, 0)
		stackZ = append(stackZ, z)
	}
	for _, rr := range radii {
		for _, zz := range zMid {
			stackR = append(stackR, rr)
			stackZ = append(stackZ, zz)
			midR = append(midR, rr)
		}
	}
	for _, rr := range radii {
		for _, zz := range zCell {
			stackR = append(stackR, rr)
			stackZ = append(stackZ, zz)
			cellR = append(cellR, rr)
			cellZ = append(cellZ, zz)
		}
	}

	axisInCell := make([]bool, len(axisZ))
	for i, z := range axisZ {
		axisInCell[i] = math.Abs(z) <= spec.ZCell
	}

	return Grids{
		StackR:     stackR,
		StackZ:     stackZ,
		NAxis:      len(axisZ),
		NMid:       nMid,
		AxisZ:      axisZ,
		AxisInCell: axisInCell,
		MidR:       midR,
		CellR:      cellR,
		CellZ:      cellZ,
	}
}

// vectorToCoils 解码 [r_0..r_K, z_0..z_K, I_0..I_K], 把每一项裁剪到 spec 边界, 并按
// z 排序线圈 (稳定排序, 所以 z 相等的线圈保持它们在向量里的次序)。这个稳定的 z 排序
// 就是规范形式, 它在不改变机器的前提下消除了 K! 排列简并。
func vectorToCoils(x []float64, spec config.Spec) ([]Coil, error) {
	want := spec.NParams()
	if len(x) != want {
		return nil, fmt.Errorf("physics: design vector has %d entries, spec wants %d", len(x), want)
	}
	k := spec.NCoils
	if k <= 0 {
		return nil, nil
	}
	lo, hi := spec.Lower(), spec.Upper()
	coils := make([]Coil, k)
	for i := 0; i < k; i++ {
		coils[i] = Coil{
			Radius:  clampVec(x[i], lo[i], hi[i]),
			Z:       clampVec(x[k+i], lo[k+i], hi[k+i]),
			Current: clampVec(x[2*k+i], lo[2*k+i], hi[2*k+i]),
		}
	}
	sort.SliceStable(coils, func(i, j int) bool { return coils[i].Z < coils[j].Z })
	return coils, nil
}

// clampVec 对应 numpy.clip(v, lo, hi): NaN 会传播, 其它情况下把值钉进 [lo, hi]。
func clampVec(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// coilsToVector 把任意顺序的线圈编码成规范向量 (按 z 升序, 稳定)。
func coilsToVector(coils []Coil) []float64 {
	ordered := make([]Coil, len(coils))
	copy(ordered, coils)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Z < ordered[j].Z })
	k := len(ordered)
	out := make([]float64, 0, 3*k)
	for _, c := range ordered {
		out = append(out, c.Radius)
	}
	for _, c := range ordered {
		out = append(out, c.Z)
	}
	for _, c := range ordered {
		out = append(out, c.Current)
	}
	return out
}

// randomDesign 对搜索盒子做均匀采样, 并以规范次序返回它 (已裁剪 + 已按 z 排序), 这样
// 搜索永远不会去评估一个已经见过的设计的排列。
func randomDesign(rng *rand.Rand, spec config.Spec) []float64 {
	lo, hi := spec.Lower(), spec.Upper()
	x := make([]float64, len(lo))
	for i := range x {
		x[i] = lo[i] + rng.Float64()*(hi[i]-lo[i])
	}
	coils, err := vectorToCoils(x, spec)
	if err != nil {
		return x
	}
	return coilsToVector(coils)
}

// minClearance 见 api.go 的 MinClearance (0.1.2): 导体面到中心元胞的最小净空。
//
// 闭式解的依据: 约束区域 {0 <= r <= RPlasma, |z| <= ZCell} 是柱坐标下的**乘积区域**,
// 且绕轴对称, 所以丝环上每一点到它的距离都相同, 可以直接分离成径向超出与轴向超出。
func minClearance(coils []Coil, spec config.Spec) float64 {
	best := math.Inf(1)
	for _, c := range coils {
		dr := math.Max(0, c.Radius-spec.RPlasma)
		dz := math.Max(0, math.Abs(c.Z)-spec.ZCell)
		if cl := math.Hypot(dr, dz) - spec.TPack/2; cl < best {
			best = cl
		}
	}
	return best
}
