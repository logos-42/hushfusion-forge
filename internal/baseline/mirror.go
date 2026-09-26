// 手工设计 mirror 的构造, 以及固定其电流量级的求解。
// 从 api.go 拆出来, 好让那些纯算术的部分
// (线圈布局、cost proxy、bounds 审计、包围求解器)可以
// 单独做单元测试, 不需要一个能用的 field solver。
package baseline

import (
	"fmt"
	"math"

	"github.com/logos-42/hushfusion-forge/internal/config"
	"github.com/logos-42/hushfusion-forge/internal/physics"
)

const (
	// 手工设计的 Name 与 Note。该 note 与 Python reference 写进
	// testdata/golden_baseline.json 的那一个逐字节相同, 这样两个
	// 实现用同样的话描述同一台机器。
	textbookName = "textbook_mirror"
	textbookNote = "Helmholtz-like central cell (r=0.50 m, spacing=r) + mirror throats " +
		"(r=0.30 m, |z|=1.00 m, I=3.5x cell). Cell current solved so the " +
		"midplane field hits spec.b_ref exactly; not a straw man."

	// Cell 电流包围扫描: 用的是 reference 实现
	// (forge/optimization/baselines.py)所用的同一套 24 点几何网格, 再
	// 二分细化到相对宽度 cellCurrentRelTol。扫描窗口
	// 刻意比 spec.Bounds.Current 更宽, 这样 "根在
	// 搜索盒之外" 会被报成包围失败或由 checkInsideBounds 报出,
	// 而绝不会被静默裁剪。
	cellCurrentScanLo = 1.0e3
	cellCurrentScanHi = 5.0e6
	cellCurrentScanN  = 24
	cellCurrentRelTol = 1e-9

	cellBisectMaxIter = 200
)

// analyticSolver 是 reference 实现使用的闭合形式 solver
// (internal/physics: 用椭圆积分给出精确的圆环丝场,
// "analytic-vacuum-loops")。这是决定 "哪个 solver 是
// 人类 baseline 的 reference" 的唯一位置 —— TextbookMirror 与 golden 测试
// 都走这里, 所以它们不会跑偏。不小心掉到
// discrete solver 上会让每一个 golden 数字都移动。
func analyticSolver() physics.Solver { return physics.AnalyticSolver{} }

// mirrorCoils 按 reference 实现的顺序摆出手工设计
// (先两个中心 cell 线圈, 再两个 throat)。testdata/golden_baseline.json
// 的 "coils" 就是这个顺序, 而 "design" 是同一台机器在 canonical
// (z 排序)顺序下的表示, 所以构造顺序保持原样。
func mirrorCoils(g Geom, cellCurrent float64) []physics.Coil {
	throat := g.ThroatCurrentRatio * cellCurrent
	return []physics.Coil{
		{Radius: g.RCell, Z: -g.HalfGapCell, Current: cellCurrent},
		{Radius: g.RCell, Z: g.HalfGapCell, Current: cellCurrent},
		{Radius: g.RThroat, Z: -g.ZThroat, Current: throat},
		{Radius: g.RThroat, Z: g.ZThroat, Current: throat},
	}
}

// costProxy 是欧姆成本代理 sum_k I_k^2 r_k [A^2 m] —— 定义与
// physics.Metrics.CostProxy 相同, 在这里单独算是因为 baseline 必须在
// 任何 metrics 存在之前就能报出自己的成本。
func costProxy(coils []physics.Coil) float64 {
	sum := 0.0
	for _, c := range coils {
		sum += c.Current * c.Current * c.Radius
	}
	return sum
}

// checkInsideBounds 在任一 coil 落在搜索盒之外时返回 error。
//
// 这是 "baseline 不被裁剪" 规则在源头上的实现: physics.VectorToCoils
// 会按 spec.Bounds 裁剪, 所以搜索盒之外的手工设计在重新编码时会变成
// 另一台机器, "机器打败了人类" 就会是拿一个没人
// 提出过的设计来衡量的。用闭区间比较 —— 恰好落在边界上的值
// 不会被裁剪移动, 所以是允许的。
func checkInsideBounds(coils []physics.Coil, spec config.Spec) error {
	for i, c := range coils {
		switch {
		case c.Radius < spec.Bounds.Radius[0] || c.Radius > spec.Bounds.Radius[1]:
			return fmt.Errorf("coil %d radius %.10g m is outside the search box [%g, %g] m: the baseline would be clipped when re-encoded",
				i, c.Radius, spec.Bounds.Radius[0], spec.Bounds.Radius[1])
		case c.Z < spec.Bounds.Z[0] || c.Z > spec.Bounds.Z[1]:
			return fmt.Errorf("coil %d z %.10g m is outside the search box [%g, %g] m: the baseline would be clipped when re-encoded",
				i, c.Z, spec.Bounds.Z[0], spec.Bounds.Z[1])
		case c.Current < spec.Bounds.Current[0] || c.Current > spec.Bounds.Current[1]:
			return fmt.Errorf("coil %d current %.10g A is outside the search box [%g, %g] A: the baseline would be clipped when re-encoded",
				i, c.Current, spec.Bounds.Current[0], spec.Bounds.Current[1])
		}
	}
	return nil
}

// solveCellCurrent 求解中心 cell 电流, 使中平面体积平均
// 场精确等于 spec.BRef(相对精度 cellCurrentRelTol)。
//
// 每个线圈电流都随 cell 电流线性缩放, 因此中平面场
// 对它是线性的: 根唯一, 在几何网格上做包围扫描
// 只要它落在 [cellCurrentScanLo, cellCurrentScanHi] 内就能找到。
func solveCellCurrent(spec config.Spec, g Geom, grids physics.Grids, s physics.Solver) (float64, error) {
	f := func(iCell float64) float64 {
		return physics.MetricsFor(mirrorCoils(g, iCell), spec, grids, s).BMidT - spec.BRef
	}
	return bisectBracket(cellCurrentScanLo, cellCurrentScanHi, cellCurrentScanN, cellCurrentRelTol, f)
}

// textbookMirror 是 solver 与 grids 由外部提供的 TextbookMirror,
// 好让测试用一个注入的 field 驱动整个构造过程。
func textbookMirror(spec config.Spec, s physics.Solver, grids physics.Grids) (Baseline, error) {
	g := DefaultGeom()
	iCell, err := solveCellCurrent(spec, g, grids, s)
	if err != nil {
		return Baseline{}, fmt.Errorf("textbook mirror cell current: %w", err)
	}
	coils := mirrorCoils(g, iCell)
	if err := checkInsideBounds(coils, spec); err != nil {
		return Baseline{}, err
	}
	return Baseline{
		Name:   textbookName,
		Note:   textbookNote,
		Coils:  coils,
		Cost:   costProxy(coils),
		Design: physics.CoilsToVector(coils),
	}, nil
}

// bisectBracket 在 [lo, hi] 上的 n 点几何网格中定位 f 的变号位置,
// 再用二分细化, 直到包围区间的相对宽度
// 小于 relTol。
//
// 失败情形会被返回, 绝不粉饰: 不可用的参数列表、
// 没有跨过根的网格、NaN 残差(否则它会被
// 细化成一个人造的根)。无限残差是允许的 —— 它的符号
// 仍然有意义, 所以那里的二分仍然是诚实的。
func bisectBracket(lo, hi float64, n int, relTol float64, f func(float64) float64) (float64, error) {
	switch {
	case !(lo > 0) || !(hi > lo):
		return 0, fmt.Errorf("invalid bracket scan window [%g, %g]: need 0 < lo < hi", lo, hi)
	case n < 2:
		return 0, fmt.Errorf("bracket scan needs at least 2 grid points, got %d", n)
	case !(relTol > 0):
		return 0, fmt.Errorf("invalid relative tolerance %g", relTol)
	}
	xLo := lo
	yLo, err := evalNoNaN(f, xLo)
	if err != nil {
		return 0, err
	}
	if yLo == 0 {
		return xLo, nil
	}
	for i := 1; i < n; i++ {
		xHi := lo * math.Pow(hi/lo, float64(i)/float64(n-1))
		yHi, err := evalNoNaN(f, xHi)
		if err != nil {
			return 0, err
		}
		if yHi == 0 {
			return xHi, nil
		}
		if (yLo < 0) != (yHi < 0) {
			return bisect(xLo, xHi, yLo, relTol, f)
		}
		xLo, yLo = xHi, yHi
	}
	return 0, fmt.Errorf("no sign change over %d geometric grid points in [%g, %g]: cannot bracket a root", n, lo, hi)
}

// bisect 细化一个已跨根的包围区间 [lo, hi], 已知两端
// 残差符号不同。它返回一个距根在 relTol*|root| 以内的点。
func bisect(lo, hi, fLo, relTol float64, f func(float64) float64) (float64, error) {
	for i := 0; i < cellBisectMaxIter; i++ {
		mid := 0.5 * (lo + hi)
		if mid <= lo || mid >= hi {
			// 包围区间已缩到相邻浮点数: 这是算术能达到的
			// 最近距离。
			return 0.5 * (lo + hi), nil
		}
		fMid, err := evalNoNaN(f, mid)
		if err != nil {
			return 0, err
		}
		if fMid == 0 {
			return mid, nil
		}
		if (fLo < 0) != (fMid < 0) {
			hi = mid
		} else {
			lo, fLo = mid, fMid
		}
		if hi-lo <= relTol*math.Abs(mid) {
			return 0.5 * (lo + hi), nil
		}
	}
	return 0, fmt.Errorf("bisection did not reach a relative width of %g in %d iterations", relTol, cellBisectMaxIter)
}

// evalNoNaN 求 f 的值并拒绝 NaN 残差: 把一个 NaN 细化成
// "解" 会产出一个伪造的电流, 而不是求解出来的电流。
func evalNoNaN(f func(float64) float64, x float64) (float64, error) {
	y := f(x)
	if math.IsNaN(y) {
		return 0, fmt.Errorf("the bracketed function returned NaN at %g", x)
	}
	return y, nil
}
