// sources.go —— 场源材料类表 (契约 §4 的冻结接口: SourceTable / LookupSource)。
//
// 7 行口径逐字来自上游 ProjectionPhysics/scripts/verify_moire_field.py 的 SOURCES dict:
// B_cap 是各材料/装置的场天花板, label 与文献出处取自上游 report.json 的
// meta.sources 与 M2 行。行的顺序与上游一致 (MATBG⊥ → MATBG∥ → MATTG∥ → ITER →
// SPARC → CFR2 真空 → CFR2 压缩), M8 总表按同一顺序打印。
//
// 为什么这些数字可以住在 Go 源码里 (而契约 §3 的锚点不行): 这张表本身就是**冻结接口的
// 输入数据**, 不是从上游 artifacts 抄来的回归值。防漂移由 anchor_test.go 负责 —— 它把
// 每一行的 B_cap / label / citation 逐条反查上游 report.json 的 meta.sources 与
// M2_field_density.rows (逐位相等, 因为两边都是 JSON 里的字面量)。
//
// 诚实边界 (契约 §6): 二维材料的 B_c2 / B_c 取文献报道的**上界/乐观包络**, 多器件分散;
// ITER/SPARC 是导体级场, CFR2 压缩后场是**脉冲**值 (不是稳态)。本层只把这些当成
// 「场天花板」这一列数字, 不主张任何材料工程上可线圈化。
package design

// 上游 SOURCES dict 的 key (LookupSource 与门表格都用它定位)。
const (
	SourceMATBGN2Perp = "MATBG_N2_perp"
	SourceMATBGN2Par  = "MATBG_N2_par"
	SourceMATTGN3Par  = "MATTG_N3_par"
	SourceITERTF      = "ITER_TF"
	SourceSPARCTF     = "SPARC_TF"
	SourceCFR2Vac     = "CFR2_vac"
	SourceCFR2Comp    = "CFR2_comp"
)

// sourceTable 是 7 行场源材料类。顺序 = 上游 SOURCES dict 的插入顺序。
var sourceTable = []Source{
	{
		Key:      SourceMATBGN2Perp,
		Label:    "MATBG (N=2) 面外 B_c2",
		BCapT:    0.12,
		Citation: "Díez-Mérida 2023 NatCommun 14,2396",
	},
	{
		Key:      SourceMATBGN2Par,
		Label:    "MATBG (N=2) 面内 B_c",
		BCapT:    1.60,
		Citation: "Qin&MacDonald 2021 PRL 127,097001",
	},
	{
		Key:      SourceMATTGN3Par,
		Label:    "MATTG (N=3) 面内 B_c（下界）",
		BCapT:    10.0,
		Citation: "Cao 2021 Nature 595,526",
	},
	{
		Key:      SourceITERTF,
		Label:    "ITER TF 导体级场",
		BCapT:    5.3,
		Citation: "ITER 公开参数",
	},
	{
		Key:      SourceSPARCTF,
		Label:    "SPARC TF",
		BCapT:    12.2,
		Citation: "PF 轮引用",
	},
	{
		Key:      SourceCFR2Vac,
		Label:    "CFR2 真空场（紧凑 FRC 基准）",
		BCapT:    9.0,
		Citation: "Slough 2025 Nucl.Fusion 65,106019",
	},
	{
		Key:      SourceCFR2Comp,
		Label:    "CFR2 压缩后场（脉冲）",
		BCapT:    35.0,
		Citation: "同上",
	},
}

// SourceTable 返回场源材料类表 (7 行, 拷一份, 调用方改不动真源)。
func SourceTable() []Source {
	out := make([]Source, len(sourceTable))
	copy(out, sourceTable)
	return out
}

// LookupSource 按 key 查一行; 未知 key 返回 (Source{}, false) —— 调用方必须显式处理
// 找不到, 而不是拿一个零值 Source (B_cap = 0) 去过门。
func LookupSource(key string) (Source, bool) {
	for _, s := range sourceTable {
		if s.Key == key {
			return s, true
		}
	}
	return Source{}, false
}
