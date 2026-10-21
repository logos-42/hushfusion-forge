// Package contracts 是 HUSHFUSION Forge 的**核心契约层**：把「设计 / 实验 / 设备 /
// 控制命令 / 证据」这五类跨层交换的对象定义成一个可序列化、可机器校验的冻结面。
//
// 为什么要单独一层: 未来的 AI 平面、Bolloon 编排平面、Safety Kernel、仿真装置与
// 真实硬件适配层之间, 交换的**只有这些结构**。把它们写死在一个包里, 边界就不再
// 靠约定维持, 而是靠编译器与 schema 维持。
//
// 诚实边界:
//   - 本包**不含任何物理**。它定义"一条设计候选长什么样""一条命令长什么样",
//     不判断"这个设计好不好"——那是 internal/design 与 internal/objective 的职责。
//   - ContractVersion 是**契约版本**, 与 internal/config.ForgeVersion（**判据版本**,
//     决定同一个设计得几分）是两个不同的东西: 前者变 = 交换格式变了; 后者变 = 分数变了。
//   - 本包不做任何 I/O, 不依赖网络、时钟或 LLM。
package contracts

// ContractVersion 是本层的契约版本（交换格式的版本）。
//
// 语义: 破坏性变更（删字段 / 改字段语义 / 改必填集）= MAJOR；新增可选字段 = MINOR；
// 纯文档 = PATCH。与 config.ForgeVersion 的版本语义**互相独立**, 不要混用。
const ContractVersion = "0.1"

// GateStatus 是单道门的判决三态。
//
// 三态而不是两态是刻意的: "算不出来 / 没证" 与 "算出来是不过" 是两件不同的事,
// 把前者混进后者就会让一个未知项变成一次静默的放行。
type GateStatus string

const (
	// GatePass 通过。
	GatePass GateStatus = "pass"
	// GateFail 不通过。
	GateFail GateStatus = "fail"
	// GateUnknown 无法判决（缺输入 / 未证 / 不可判）。**绝不等于 pass**。
	GateUnknown GateStatus = "unknown"
)

// GateStatusAll 是三态的完整集合, 供 schema 枚举校验使用。
var GateStatusAll = []GateStatus{GatePass, GateFail, GateUnknown}

// EvidenceKind 是证据的种类（决定它能被谁引用、能证明什么）。
type EvidenceKind string

const (
	// EvidenceTrace 控制闭环的一次执行轨迹。
	EvidenceTrace EvidenceKind = "trace"
	// EvidenceRecord 注册表里的一条设计记录。
	EvidenceRecord EvidenceKind = "record"
	// EvidenceGate 一道门自己的判定结果。
	EvidenceGate EvidenceKind = "gate"
	// EvidenceReport 人类可读报告。
	EvidenceReport EvidenceKind = "report"
	// EvidenceExternal 来自仓外的资料（上游仓库 / 文献）。
	EvidenceExternal EvidenceKind = "external"
)

// EvidenceRef 指向一份证据。
//
// 它是**引用**而不是内容: 引用必须带 SHA256（或至少一个稳定标识）,
// 因为"证据会变"这件事本身必须可见——一个没有指纹的引用在审计时等于没有引用。
type EvidenceRef struct {
	// Kind 证据种类。
	Kind EvidenceKind `json:"kind"`
	// URI 证据位置（仓内相对路径 / URL / 内容地址）。
	URI string `json:"uri"`
	// SHA256 内容指纹（十六进制）。为空表示该证据尚无稳定指纹（此时不得用于判决）。
	SHA256 string `json:"sha256,omitempty"`
	// ForgeVersion 产出这份证据时的**判据版本**（config.ForgeVersion）。
	ForgeVersion string `json:"forge_version,omitempty"`
	// ProducedAt 产出时间（RFC3339）。
	ProducedAt string `json:"produced_at,omitempty"`
}

// GateResult 是一道设计判决门的结果。
//
// 每道门必须能独立回答三件事: 判据是什么（Key/Threshold/Unit）、结论是什么
// （Status）、证据在哪（Evidence）。缺任何一件, 这道门就不是门。
type GateResult struct {
	// Key 门的稳定标识（内部设计层用 field_min / death / ...）。
	Key string `json:"key"`
	// Status 判决三态。
	Status GateStatus `json:"status"`
	// Value 实算值。
	Value float64 `json:"value"`
	// Threshold 判据阈值（0 表示该门无阈值, 只报值）。
	Threshold float64 `json:"threshold"`
	// Unit 量纲。
	Unit string `json:"unit,omitempty"`
	// Detail 人类可读的一句话说明（为什么是这个结论）。
	Detail string `json:"detail,omitempty"`
	// Evidence 支撑这道门的证据引用。
	Evidence []EvidenceRef `json:"evidence,omitempty"`
}

// DesignCandidate 是一条设计候选。
//
// 它只是"候选": 参数 + 单位 + 出处, 不含分数、不含判决。
// 把它和 DesignVerdict 分开, 是为了让"谁提出的"与"谁判的"在数据上就是两件事。
type DesignCandidate struct {
	// ContractVersion 契约版本。
	ContractVersion string `json:"contract_version"`
	// DesignID 全局唯一设计标识。
	DesignID string `json:"design_id"`
	// ParentDesignID 父设计（谱系）。空表示根。
	ParentDesignID string `json:"parent_design_id,omitempty"`
	// Origin 候选来源: ai / human / baseline / search。
	Origin string `json:"origin"`
	// Parameters 设计变量（数值）。
	Parameters map[string]float64 `json:"parameters"`
	// Units 每个设计变量的单位（缺项的单位是"未知", 不是"默认"）。
	Units map[string]string `json:"units,omitempty"`
	// CreatedAt 创建时间（RFC3339）。
	CreatedAt string `json:"created_at,omitempty"`
}

// Verdict 的三种总判决。
const (
	// VerdictAccepted 全部决定性门通过。
	VerdictAccepted = "accepted"
	// VerdictRejected 至少一道决定性门不通过。
	VerdictRejected = "rejected"
	// VerdictInconclusive 无法判决（缺决定性门, 或有决定性门是 unknown）。
	VerdictInconclusive = "inconclusive"
)

// DesignVerdict 是一次设计判决的完整结果。
//
// 硬规则（由 Judge 设计, 由单测钉住）:
//
//  1. unknown 永远不等于 pass;
//  2. 任何一道决定性门 fail ⟹ 总判决 rejected, 不允许被总分掩盖;
//  3. 决定性门全部 pass ⟹ accepted;
//  4. 没有决定性门, 或存在 unknown 的决定性门 ⟹ inconclusive。
type DesignVerdict struct {
	// ContractVersion 契约版本。
	ContractVersion string `json:"contract_version"`
	// DesignID 被判决的设计标识。
	DesignID string `json:"design_id"`
	// Overall 总判决: accepted / rejected / inconclusive。
	Overall string `json:"overall"`
	// Gates 逐门结果（含非决定性门）。
	Gates []GateResult `json:"gates"`
	// DecisiveKeys 计入总判决的门 key。
	DecisiveKeys []string `json:"decisive_keys"`
	// Evidence 支撑整次判决的证据。
	Evidence []EvidenceRef `json:"evidence,omitempty"`
}

// Judge 按上面四条硬规则给出一个 DesignVerdict。
//
// decisive 是**决定性门**的 key 集合; 不在其中的门只报值, 不判生死
// （例如 internal/design 的 steps 门就是这种）。
func Judge(designID string, gates []GateResult, decisive []string) DesignVerdict {
	v := DesignVerdict{
		ContractVersion: ContractVersion,
		DesignID:        designID,
		Gates:           gates,
		DecisiveKeys:    append([]string(nil), decisive...),
	}
	isDecisive := map[string]bool{}
	for _, k := range decisive {
		isDecisive[k] = true
	}
	sawDecisive := false
	sawUnknown := false
	for _, g := range gates {
		if !isDecisive[g.Key] {
			continue
		}
		sawDecisive = true
		switch g.Status {
		case GateFail:
			v.Overall = VerdictRejected
			return v
		case GateUnknown:
			sawUnknown = true
		}
	}
	switch {
	case !sawDecisive:
		v.Overall = VerdictInconclusive
	case sawUnknown:
		v.Overall = VerdictInconclusive
	default:
		v.Overall = VerdictAccepted
	}
	return v
}
