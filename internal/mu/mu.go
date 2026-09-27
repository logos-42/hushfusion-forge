// Package mu 是「交换子世界」的算子代数与 μ 动力学: 与上游 ProjectionPhysics 的定义
// 逐字对应, 不在这里发明任何物理。
//
// 上游唯一真源(全部直读, 不凭记忆):
//
//	ProjectionPhysics/ProjectionPhysics/GravityControl.lean   GCA0–GCA7(抹平算子族 + 布尔边界)
//	ProjectionPhysics/ProjectionPhysics/MuFieldCoupling.lean  TD11–TD21(增益 = 抹平进展 + 顺序差)
//	ProjectionPhysics/ProjectionPhysics/PlasmaDynamics.lean   TD1–TD10(μ 状态方程)
//	ProjectionPhysics/scripts/verify_gravity_control.py       算子的可执行定义(flatten / Q / 交换子)
//	ProjectionPhysics/artifacts/gravitycontrol/report.json    数值见证 N1–N9
//	ProjectionPhysics/artifacts/mudynamics/report.json        数值见证 N1–N15
//
// 两条纪律:
//
//  1. **数值锚点不由本文件手抄**: scripts/emit_mu_anchors.py 从上面那些真实文件读出锚点
//     写成 testdata/mu_anchors.json, 再由 anchor_test.go 逐条比对。
//  2. **窗口余量与窗口关闭步复用 internal/design**(那是同一批上游条目 TD19–TD21 的唯一
//     实现点), 本包只转发, 不重算 —— 重算会造出第二份"差不多的"公式。
//
// 本包只做代数与状态方程, 不含世界语义(那是 internal/world 的协议层), 也不含协议字节。
package mu

import (
	"fmt"
	"math"

	"github.com/logos-42/hushfusion-forge/internal/config"
	"github.com/logos-42/hushfusion-forge/internal/design"
)

// Cells 是一个控制区域: 场向量上的格点下标集合(上游 Lean 的 Finset ι)。
//
// 下标集合的**顺序不参与任何定义**: 区域平均与起伏能量都与顺序无关, 所以下面的函数
// 不做排序(排序反而会掩盖"两个区域是不是同一个集合"的判定, 那由 Relate 做)。
type Cells []int

// AllCells 返回整条场向量的格点集合 Ω(交换子里那条恒被抹平的整体区域)。
func AllCells(n int) Cells {
	out := make(Cells, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, i)
	}
	return out
}

// Region 是一个具名控制区域(键名进 hello 的声明, 也是动作键的一部分)。
type Region struct {
	Key   string
	Cells Cells
}

// Mean 是区域平均(上游 Lean: regionMean): |A|⁻¹ Σ_{i∈A} v_i。
//
// 空区域直接 panic: 平均值无从谈起, 返回 0 会让"抹平一个不存在的区域"看起来像一次成功
// 的控制 —— 本仓的规矩是宁可大声失败。
func Mean(v []float64, a Cells) float64 {
	if len(a) == 0 {
		panic("mu: cannot take a region mean over an empty region")
	}
	s := 0.0
	for _, i := range a {
		s += v[i]
	}
	return s / float64(len(a))
}

// Flatten 是控制算子 P_A(上游 Lean: flatten): A 内替换为区域均值, A 外不动。
//
// 上游定义逐字对应:
//
//	flatten(A, v)_i = mean_A(v)  (i ∈ A)
//	                = v_i        (i ∉ A)
func Flatten(v []float64, a Cells) []float64 {
	m := Mean(v, a)
	out := make([]float64, len(v))
	copy(out, v)
	for _, i := range a {
		out[i] = m
	}
	return out
}

// FluctuationEnergy 是逻辑基元 Q_A(v)(上游 Lean: fluctuationEnergy):
//
//	Q_A(v) = Σ_{i∈A} (v_i − mean_A(v))²
//
// 非负二次型(GCA2a); Q_A(v) = 0 ⟺ A 内常值(GCA2b); 抹平之后恒为 0(GCA2c)。
func FluctuationEnergy(v []float64, a Cells) float64 {
	m := Mean(v, a)
	s := 0.0
	for _, i := range a {
		d := v[i] - m
		s += d * d
	}
	return s
}

// Commutator 是交换子 [P_A, P_B] v:
//
//	[P_A, P_B] v = flatten(A, flatten(B, v)) − flatten(B, flatten(A, v))
//
// 返回的是**向量差**而不是一个范数: 逐分量可比, 且"层状 ⟹ 恒等零"(GCA6a 不交 /
// GCA6b 嵌套)在这里是逐位可断言的。
//
// 上游 N9 / GCA6c 的键名把 `flatten(A, flatten(B, v))` 写作"先 A 后 B"; 按定义, 这
// 一项是 **B 先作用**。锚点文件里记录的是组合式本体 + 上游键名原文, 一律不用键名推语义
// (见 docs/world-commutator-candidates.md 的出处一节)。
func Commutator(v []float64, a, b Cells) []float64 {
	ab := Flatten(Flatten(v, b), a)
	ba := Flatten(Flatten(v, a), b)
	out := make([]float64, len(v))
	for i := range out {
		out[i] = ab[i] - ba[i]
	}
	return out
}

// Norm 是欧氏范数(上游报告里的 ‖[P_A,P_B]v‖ 就是它)。
func Norm(v []float64) float64 {
	s := 0.0
	for _, x := range v {
		s += x * x
	}
	return math.Sqrt(s)
}

// MaxAbs 是逐分量绝对值最大值(断言"交换子恒等零"时比范数更好读)。
func MaxAbs(v []float64) float64 {
	m := 0.0
	for _, x := range v {
		if a := math.Abs(x); a > m {
			m = a
		}
	}
	return m
}

// Relation 是两个区域之间的位置关系(上游 GCA6 的"层状"判定的两个分支)。
type Relation string

const (
	// RelationDisjoint: 不交(A ∩ B = ∅)—— GCA6a, 两个抹平算子可交换。
	RelationDisjoint Relation = "disjoint"
	// RelationNested: 嵌套(A ⊆ B 或 B ⊆ A)—— GCA6b, 复合 = 大区域的控制(吸收律)。
	RelationNested Relation = "nested"
	// RelationPartialOverlap: 部分重叠 —— GCA6c, 交换子 ≠ 0(非布尔)。
	RelationPartialOverlap Relation = "partial_overlap"
)

// Relate 判定两个区域的关系。三种情形穷尽且互斥(集合论的显然事实, 不引入第四种)。
func Relate(a, b Cells) Relation {
	if disjoint(a, b) {
		return RelationDisjoint
	}
	if subset(a, b) || subset(b, a) {
		return RelationNested
	}
	return RelationPartialOverlap
}

// Lamellar 报告两个区域是否**层状**(不交或嵌套)。
//
// 上游 GCA6a/GCA6b 的结论: 层状 ⟹ 两个抹平算子可交换 ⟹ 交换子 ≡ 0。这是本世界
// "构造性零"的那一半 —— 它使"尺子读出的非零"可以被归因, 而不是被一个阈值吸收。
func Lamellar(a, b Cells) bool {
	r := Relate(a, b)
	return r == RelationDisjoint || r == RelationNested
}

func disjoint(a, b Cells) bool {
	set := make(map[int]struct{}, len(b))
	for _, i := range b {
		set[i] = struct{}{}
	}
	for _, i := range a {
		if _, ok := set[i]; ok {
			return false
		}
	}
	return true
}

func subset(a, b Cells) bool {
	set := make(map[int]struct{}, len(b))
	for _, i := range b {
		set[i] = struct{}{}
	}
	for _, i := range a {
		if _, ok := set[i]; !ok {
			return false
		}
	}
	return true
}

// --------------------------------------------------------------------------- μ 动力学

// MuStep 是上游 TD1 的状态方程: μ ↦ μ + η(1 − μ)。
//
// TD1a: 0 ≤ μ ≤ 1 且 0 ≤ η ≤ 1 ⟹ 更新后仍在 [0,1](不超调);
// TD5: η = 1 ⟹ 一步到 1;TD3: 0 < η < 1 且 μ < 1 ⟹ 更新后严格小于 1(有限步不可达)。
func MuStep(muVal, eta float64) float64 { return muVal + eta*(1-muVal) }

// Gain 是上游 TD11 的桥: η = 1 − Q_A(v)/Q_A(v₀) —— 单步控制增益 = 起伏被消掉的相对比例。
//
// Q₀ = 0 时**大声失败**: TD11 的每条陈述都带假设 Q_A(v₀) ≠ 0(参照场自己有起伏),
// 没有它 η 落在 0/0 上, 那不是一个可以拿去评分的数。
func Gain(q, q0 float64) float64 {
	if !(q0 > 0) {
		panic(fmt.Sprintf("mu: reference fluctuation energy Q_A(v0) = %v is not positive — "+
			"the bridge eta = 1 - Q/Q0 (TD11) has no meaning there", q0))
	}
	return 1 - q/q0
}

// GainOf 是同一件事的场形式: 拿两个场算区域 A 的增益。
func GainOf(v, v0 []float64, a Cells) float64 {
	return Gain(FluctuationEnergy(v, a), FluctuationEnergy(v0, a))
}

// OrderGap 是上游 TD15/TD16 的顺序差闭式:
//
//	μ(先抹平再更新) − μ(先更新再抹平) = (1 − μ)(1 − η_before)
//
// **顺序效应只来自增益的差**(TD15): 增益不读场(η_after = η_before)⟹ 差为零 ⟹ 可交换。
// 抹平一次把增益拉到 1(TD12)⟹ 这个差 = (1−μ)(1−η_before) —— 本世界"构造性 A ≠ 0"
// 的解析内容就是这一条。
func OrderGap(muVal, etaBefore float64) float64 { return (1 - muVal) * (1 - etaBefore) }

// MuAfterSteps 转发 internal/design 的闭式解 μ_n = 1 − (1−η)^n(1−μ₀)(TD7)。
func MuAfterSteps(mu0, eta float64, n int) float64 { return design.MuAfterSteps(mu0, eta, n) }

// --------------------------------------------------------------------------- 复用判决层

// WindowMargin 是窗口余量 m_i(1 − μ)(TD19 的单调递减量)。
func WindowMargin(muVal float64) float64 { return 1 - muVal }

// LockingFactor 转发 internal/design 的锁定因子 1/√(1−μ)(TD20), 不在这里重算。
func LockingFactor(muVal float64) float64 { return design.LockingFactor(muVal) }

// WindowCloseStep 转发 internal/design 的窗口关闭步解析值(TD21), m_e/m_i 取上游 D-T 的
// design.Floor。它的边界语义(η ≥ 1 ⟹ +Inf 等)以 design 的注释为准, 本包不改写。
func WindowCloseStep(muVal, eta float64) float64 {
	return design.WindowCloseStep(muVal, eta, design.Floor)
}

// FirstClosedStep 转发 internal/design 的整数关闭步 = ceil(解析值); 永不关闭 = -1。
func FirstClosedStep(muVal, eta float64) int {
	return design.FirstClosedStep(muVal, eta, design.Floor)
}

// Ceiling 是 FC11 的硬天花板 1 − Floor(上游 D-T 口径)。
//
// 它取 internal/design 的 Floor —— 那条常数的唯一实现点 —— 这里只是那一个减法, 本包
// 不重算任何闭式解。有一条单测(TestCeilingMatchesDesignReview)把这个值与
// design.Scope.MuCeiling 钉在一起: 两者漂移即红, 所以"复用"不是一句口头声明。
func Ceiling() float64 { return 1 - design.Floor }

// TargetScore 返回"解"处的世界分数: μ 到顶(窗口关闭)且场被抹平(η = 1)。
//
//	TargetScore = w_mu·(1/Ceiling) + w_gain·1 + w_closed
//
// 存在的理由不是"设一个够不着的靶", 而是让**靶可达**: 交换子世界的终止条件是
// score ≥ target, 若把 target 拍成某个整数(例如 1.0), 世界就会在一次**部分**解处终止,
// 于是"两个次序各走两步"这种测量根本走不完(TD16 的顺序差需要两步)。这里用世界自己那个
// 分数函数在同一批常数上求值, 并且**保持与 world.muScore 相同的求和次序**
// (w_mu·(μ/C) + w_gain·η 先加, w_closed 最后加), 于是解处 score == TargetScore 是数位级
// 相等 —— 有一条单测(TestTargetScoreIsReachableBitExactly)钉住这件事。
func TargetScore(wMu, wGain, wClosed float64) float64 {
	head := wMu*(1/Ceiling()) + wGain*1
	return head + wClosed
}

// Closed 报告 μ 是否已达 FC11 硬天花板(窗口关闭)。
func Closed(muVal float64) bool { return muVal >= Ceiling() }

// --------------------------------------------------------------------------- 目标函数

// Score 是本世界的**真目标函数**: 世界把它原样报在 info.score 里(不是整形过的 reward)。
//
//	score(v, v0, μ) = W_mu · μ/MuCeiling
//	                + W_gain · η(v)                     η = 1 − Q_Ω(v)/Q_Ω(v₀)   (TD11 的桥)
//	                + W_closed · 1[μ ≥ MuCeiling]        (FC11 硬天花板)
//
// 三项都读**状态**: 第一、三项读 μ, 第二项读场(经 Ω 上的起伏能量)。权重住
// internal/config(房规: 影响分数的数字只有一个家)。
//
// 对 μ 的单调性是构造性的前提, 单独有一条单测(TestScoreStrictlyMonotoneInMu):
// 顺序差只改变 μ 而不改变场, 所以"顺序差"只有在这个函数随 μ 严格增长时才会读成 A ≠ 0。
func Score(v, v0 []float64, muVal float64) float64 {
	s := config.MuScoreWeightMu*(muVal/Ceiling()) +
		config.MuScoreWeightGain*GainOf(v, v0, AllCells(len(v)))
	if Closed(muVal) {
		s += config.MuScoreWeightClosed
	}
	return s
}

// --------------------------------------------------------------------------- 声明式世界配置

// ActionKind 是声明式动作空间里的动作类别。
type ActionKind string

const (
	// KindFlatten: 对某个区域施加抹平算子 P_A。
	KindFlatten ActionKind = "flatten"
	// KindUpdateMu: 按**当前场**的增益推进 μ 一步(TD1 + TD11)。
	KindUpdateMu ActionKind = "update_mu"
)

// ActionKeyFlatten 是抹平动作键的前缀: "flatten:<区域键>"。
const ActionKeyFlatten = "flatten:"

// ActionKeyUpdateMu 是 μ 更新动作的键。
const ActionKeyUpdateMu = "update_mu"

// ActionSpace 是一个**声明的有限**动作集合: 键 + 线编码 + 语义。
//
// 为什么是有限集合而不是一个盒子: 世界真有的动作只有"抹平某个区域"和"更新一次 μ"。
// 从一个连续盒子里抽出来的增量向量在这个世界里**根本没有对应的动作**, 拿它们去量 A
// 量到的是别的东西(上一轮的教训: 动作集合里没有换源那个动作, 于是 R4 永远不可见)。
type ActionSpace struct {
	Keys    []string     // 动作键, 顺序 = 线编码的位序
	Kinds   []ActionKind // 与 Keys 一一对应
	Regions []string     // 抹平动作对应的区域键; update_mu 处为空串
	Vectors [][]float64  // 每个动作的线编码(one-hot): 第 k 个动作 = e_k
}

// Index 返回一个线编码向量在声明里的下标。
//
// 判据是**逐位相等**: 声明之外的动作一律拒绝(世界不能"顺手也接受一下"一个它没有的
// 动作 —— 那会让客户端以为自己在驱动另一个世界)。Float 比较在这里是精确的: 编码只有
// 0 与 1, 没有舍入余地。
func (a ActionSpace) Index(vector []float64) (int, bool) {
	for k, v := range a.Vectors {
		if len(v) != len(vector) {
			continue
		}
		same := true
		for i := range v {
			if v[i] != vector[i] {
				same = false
				break
			}
		}
		if same {
			return k, true
		}
	}
	return 0, false
}

// Dim 是线编码的维度 = 声明的动作个数。
func (a ActionSpace) Dim() int { return len(a.Keys) }

// DeclaredRegions 返回本世界的区域族(键名进 hello, 也是动作键的一部分)。
//
// 规则只由格点数 n 决定(n 必须是 4 的倍数, 那是一条断言而不是一个默认值):
//
//	omega  {0..n-1}        全域 —— 抹平它 ⟹ 场变常值 ⟹ Q = 0 ⟹ η = 1(TD11b/TD12)
//	left   {0..n/2-1}      与 right 不交(层状), 与 middle 部分重叠
//	right  {n/2..n-1}      与 left 不交(层状)
//	inner  {0..n/4-1}      嵌套在 left 里(层状: 吸收律 GCA6b)
//	middle {n/4..3n/4-1}   与 left / right 部分重叠(GCA6c)
//
// 这个族是**世界配置**, 在第一次计量之前就写死: 它同时含层状对(交换子恒等零)与部分
// 重叠对(交换子 ≠ 0), 于是"尺子读不读得出区别"在同一份声明里就能判 —— 而不必去跟一个
// 拍出来的阈值比大小。
func DeclaredRegions(n int) []Region {
	if n <= 0 || n%4 != 0 {
		panic(fmt.Sprintf("mu: the field must have 4k cells (got %d) — the declared region "+
			"family is derived from that, and a silent rounding would move the regions", n))
	}
	span := func(lo, hi int) Cells {
		out := make(Cells, 0, hi-lo)
		for i := lo; i < hi; i++ {
			out = append(out, i)
		}
		return out
	}
	return []Region{
		{Key: "omega", Cells: span(0, n)},
		{Key: "left", Cells: span(0, n/2)},
		{Key: "right", Cells: span(n/2, n)},
		{Key: "inner", Cells: span(0, n/4)},
		{Key: "middle", Cells: span(n/4, 3*n/4)},
	}
}

// DeclaredActionSpace 把区域族展开成一个声明式动作空间:
// 每个区域一个抹平动作(按区域声明顺序), 末尾一个 μ 更新动作。
func DeclaredActionSpace(regions []Region) ActionSpace {
	space := ActionSpace{}
	for _, r := range regions {
		space.Keys = append(space.Keys, ActionKeyFlatten+r.Key)
		space.Kinds = append(space.Kinds, KindFlatten)
		space.Regions = append(space.Regions, r.Key)
	}
	space.Keys = append(space.Keys, ActionKeyUpdateMu)
	space.Kinds = append(space.Kinds, KindUpdateMu)
	space.Regions = append(space.Regions, "")
	for k := range space.Keys {
		vec := make([]float64, len(space.Keys))
		vec[k] = 1
		space.Vectors = append(space.Vectors, vec)
	}
	return space
}

// RegionByKey 按键取区域(找不到时第二个返回值为 false, 不返回零值冒名顶替)。
func RegionByKey(regions []Region, key string) (Region, bool) {
	for _, r := range regions {
		if r.Key == key {
			return r, true
		}
	}
	return Region{}, false
}
