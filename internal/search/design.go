// search 层的设计空间辅助函数。
//
// api.go 的冻结文档注释用 physics 层(stage A)的术语描述了
// 两个设计空间操作:
//
//	physics.RandomDesign(rng, spec)   均匀 i.i.d. 采样, canonical 顺序
//	physics.VectorToCoils + physics.CoilsToVector   裁剪 + z 排序("canonical")
//
// Stage A 是一条独立的并行线, 所以直接调用它会让 search 包
// 在 A 落地之前无法测试 —— 而 runner.Scorer
// 做成接口的全部意义, 就在于 stage D 的单元测试能自己
// 通过。因此这两个操作是接缝变量, 带一个语义与文档一致的
// 本地实现; 由 physics 支撑的实现也一并提供
// (PhysicsCanonicalise), 这样在串行收口轮里的接线只是
// 一行赋值, 而不是重写:
//
//	search.SampleDesign = physics.RandomDesign
//	search.Canonicalise = search.PhysicsCanonicalise
//	search.WarmStartDesign = func(spec config.Spec) []float64 { b, _ := baseline.TextbookMirror(spec); return b.Design }
//
// 本地实现只用 config.Spec.Lower/Upper: 边界留在
// config 这个唯一真源里, 所以这里不重复任何物理数字。
package search

import (
	"math/rand"
	"sort"

	"github.com/logos-42/hushfusion-forge/internal/config"
	"github.com/logos-42/hushfusion-forge/internal/physics"
)

// SampleDesign 从搜索盒中抽一个均匀 i.i.d. 设计, 并以
// canonical(z 排序)顺序返回。它掌管 RNG 流: 调用方必须
// 按固定的逻辑顺序调用它, 一次运行才可复现。
//
// 默认值: physics.RandomDesign, 与 Random 的冻结文档注释
// 的规定完全一致 —— stage A 是搜索盒的唯一真源。
// 这个接缝保持为变量, 以便单元测试替换成确定性采样器,
// 下面的 localSampleDesign 则保留为 reference 实现, 供
// search_test.go 里的 parity gate 比对(在写作时它对同一 seed
// 与 physics.RandomDesign 逐位相同)。
var SampleDesign = physics.RandomDesign

// Canonicalise 把设计裁剪到盒子并放进 canonical 顺序(线圈按 z
// 升序排序), 消掉 K! 的线圈排列简并。默认值:
// physics 实现(VectorToCoils + CoilsToVector); 下面的
// localCanonicalise 是 parity gate 比对的 reference 实现。
var Canonicalise = PhysicsCanonicalise

// WarmStartDesign 返回在 Options.WarmStart 为空时用来播种
// EvolutionWarm 初始种群的设计: textbook mirror 设计
// (testdata/golden_baseline.json, "textbook_mirror"), 也就是被要求
// 让机器打败的那个人类设计。
var WarmStartDesign = localWarmStart

func localSampleDesign(rng *rand.Rand, spec config.Spec) []float64 {
	lo, hi := spec.Lower(), spec.Upper()
	x := make([]float64, len(lo))
	for j := range x {
		x[j] = lo[j] + rng.Float64()*(hi[j]-lo[j])
	}
	return localCanonicalise(x, spec)
}

// localCanonicalise 镜像 physics.VectorToCoils + physics.CoilsToVector:
// 把每个参数裁剪进盒子, 然后用稳定排序把 (r, z, I) 三元组按 z 排序,
// 这样相等的 z 值会保持它们的输入顺序。
func localCanonicalise(x []float64, spec config.Spec) []float64 {
	out := make([]float64, len(x))
	copy(out, x)

	nc := spec.NCoils
	lo, hi := spec.Lower(), spec.Upper()
	if len(out) != spec.NParams() || nc <= 0 || len(lo) != len(out) {
		return out
	}
	for j := range out {
		out[j] = clipTo(out[j], lo[j], hi[j])
	}
	if nc == 1 {
		return out
	}
	type triple struct{ r, z, i float64 }
	cs := make([]triple, nc)
	for k := 0; k < nc; k++ {
		cs[k] = triple{r: out[k], z: out[nc+k], i: out[2*nc+k]}
	}
	sort.SliceStable(cs, func(a, b int) bool { return cs[a].z < cs[b].z })
	for k := 0; k < nc; k++ {
		out[k], out[nc+k], out[2*nc+k] = cs[k].r, cs[k].z, cs[k].i
	}
	return out
}

// PhysicsCanonicalise 是由 stage A 支撑的 Canonicalise 实现。
// 放在这里(stage A 落地后会被编译并做 parity 测试), 好让上面的替换
// 只是一行接线改动。
func PhysicsCanonicalise(x []float64, spec config.Spec) []float64 {
	coils, err := physics.VectorToCoils(x, spec)
	if err != nil {
		out := make([]float64, len(x))
		copy(out, x)
		return out
	}
	return physics.CoilsToVector(coils)
}

// textbookMirrorDesign 是 canonical(z 升序)形式的人类 baseline,
// 从 testdata/golden_baseline.json("design")逐位拷贝而来。
var textbookMirrorDesign = []float64{
	0.3, 0.5, 0.5, 0.3,
	-1.0, -0.25, 0.25, 1.0,
	1621279.2385890577, 463222.63959687366, 463222.63959687366, 1621279.2385890577,
}

// localWarmStart 对参考 4 线圈装置返回 textbook mirror, 对其他线圈数
// 返回盒子中心(mirror 的比例只对 4 个线圈
// 有定义)。真正的真源是 baseline.TextbookMirror,
// 它还会*求解* cell 电流; 这个字面量是那个已求解设计的
// golden 副本, stage B 落地后会被上面的调用替换。
func localWarmStart(spec config.Spec) []float64 {
	if spec.NCoils == 4 {
		x := make([]float64, len(textbookMirrorDesign))
		copy(x, textbookMirrorDesign)
		return localCanonicalise(x, spec)
	}
	lo, hi := spec.Lower(), spec.Upper()
	x := make([]float64, len(lo))
	for j := range x {
		x[j] = 0.5 * (lo[j] + hi[j])
	}
	return localCanonicalise(x, spec)
}

func clipTo(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
