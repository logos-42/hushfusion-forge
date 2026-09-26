// Package baseline: 人类 baseline —— 刻意做强, 因为弱的
// baseline 什么都证明不了。
//
// FROZEN INTERFACE (v0.1) — 负责人: stage B。
package baseline

import (
	"github.com/logos-42/hushfusion-forge/internal/config"
	"github.com/logos-42/hushfusion-forge/internal/physics"
)

// Geom 是 textbook mirror 手工写定的比例。
type Geom struct {
	RCell              float64 // [m] 中心 cell 线圈半径
	HalfGapCell        float64 // [m] 中心 cell 间距的一半(Helmholtz: = RCell/2)
	RThroat            float64 // [m] mirror throat 线圈半径
	ZThroat            float64 // [m] throat 线圈的 |z|
	ThroatCurrentRatio float64 // throat 电流 / cell 电流
}

// DefaultGeom 是 reference 几何: r_cell=0.50, half-gap 0.25 (Helmholtz
// 条件: 间距 = 半径), r_throat=0.30, |z_throat|=1.00, ratio 3.5。
func DefaultGeom() Geom {
	return Geom{RCell: 0.50, HalfGapCell: 0.25, RThroat: 0.30, ZThroat: 1.00, ThroatCurrentRatio: 3.5}
}

// Baseline 是一个有名字的人类设计。
type Baseline struct {
	Name   string         `json:"name"`
	Note   string         `json:"note"`
	Coils  []physics.Coil `json:"coils"`
	Cost   float64        `json:"cost_proxy"`
	Design []float64      `json:"design"`
}

// HelmholtzPair 是两个相同线圈, 间距等于它们自身的半径。
func HelmholtzPair(radius, current float64) []physics.Coil {
	// 间距 = 半径, 以 z = 0 为中心(textbook 的均匀场对)。
	return []physics.Coil{
		{Radius: radius, Z: -radius / 2.0, Current: current},
		{Radius: radius, Z: radius / 2.0, Current: current},
	}
}

// TextbookMirror 是被要求让机器打败的手工设计 mirror:
// 一个类 Helmholtz 的中心 cell 加两个 mirror throat。
//
// cell 电流是求解出来的(不是猜的), 使中平面体积平均场
// 精确等于 spec.BRef: 先在几何网格上做包围扫描,
// 再用二分细化到约 1e-9 相对精度。得到的 throat 电流约为
// 1.62 MA, 这就是 config.Bounds.Current[1] 取 2.5e6 的原因。
//
// golden 值位于 testdata/golden_baseline.json (score -0.2905708...):
// Go 实现必须在该文件的 1e-6 之内复现它。
func TextbookMirror(spec config.Spec) (Baseline, error) {
	// reference 实现是拿它的默认闭合形式 solver 求解的;
	// 这里同样如此(见 analyticSolver)。
	return textbookMirror(spec, analyticSolver(), physics.BuildGrids(spec))
}
