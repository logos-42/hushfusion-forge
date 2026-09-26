// Package registry: 公司的记忆, 而不是一份日志文件。
//
// 冻结接口 (v0.1) — 负责人: stage C。
//
// 每个曾被评分过的 design 都是一行 JSONL, 携带它的参数、原始物理项、约束残差与
// lineage (parent_design)。由此有两个后果:
//
//   - 搜索从来不是真相来源, registry 才是, 所以一次 run 之后还可以重新评分、
//     重新加权、重新挖掘;
//   - design 的历史是一棵树而不是一个列表, 所以“哪条分支带来的改进最多?”是可以
//     回答的 —— 这正是进度护城河的基元。
//
// 该 JSON schema 与 Python 参考实现共享 (见 python/forge/registry.py)。字段名均已
// 冻结; 任何不一致都会被 schema-parity 门捕获。
package registry

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/logos-42/hushfusion-forge/internal/physics"
)

// Params 是按线圈拆分并命名的 design。
type Params struct {
	RadiusM  []float64 `json:"radius_m"`
	ZM       []float64 `json:"z_m"`
	CurrentA []float64 `json:"current_A"`
}

// Record 是一次 experiment。
type Record struct {
	ExperimentID int                `json:"experiment_id"`
	DesignID     string             `json:"design_id"`
	ParentDesign string             `json:"parent_design,omitempty"`
	Generation   int                `json:"generation"`
	Algorithm    string             `json:"algorithm"`
	Seed         int                `json:"seed"`
	EvalIndex    int                `json:"eval_index"`
	Tag          string             `json:"tag"`
	Timestamp    string             `json:"timestamp"`
	Score        float64            `json:"score"`
	Feasible     bool               `json:"feasible"`
	Params       Params             `json:"params"`
	Terms        map[string]float64 `json:"terms"`
	Weighted     map[string]float64 `json:"weighted"`
	Penalties    map[string]float64 `json:"penalties"`
	Metrics      physics.Metrics    `json:"metrics"`
	Note         string             `json:"note,omitempty"`
}

// RequiredFields 是一条 record 必须携带的键 (对应 Python 的 REQUIRED_FIELDS)。
var RequiredFields = []string{"experiment_id", "design_id", "algorithm", "seed", "score", "params", "terms", "metrics"}

// Registry 是只追加的 JSONL design registry。可安全并发使用。
type Registry struct {
	Path string
	mu   sync.Mutex
	n    int
}

// Open 打开 (必要时创建) path 处的 registry, 并统计已有的行数。末尾被截断的一行
// 会被容忍并跳过, 绝不致命。
func Open(path string) (*Registry, error) {
	n, err := openRegistryFile(path)
	if err != nil {
		return nil, err
	}
	return &Registry{Path: path, n: n}, nil
}

// Len 是磁盘上的 record 数量。
func (r *Registry) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n
}

// NextIDs 返回下一次 Append 将会使用的 id。
func (r *Registry) NextIDs() (experimentID int, designID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	next := r.n + 1
	return next, formatDesignID(next)
}

// Append 写入一条 record: 当 ExperimentID/DesignID 未设置时, 从运行中的计数器
// 分配它们, 并写入一个 UTC 时间戳。它必须被串行化, 这样即使搜索并发运行, id 也
// 保持连续无空洞。
func (r *Registry) Append(rec Record) error {
	// Append 是冻结下来的名字; runner.Score 使用的“赋值并返回”变体走的正是同一条
	// 代码路径 (见 AppendAssign)。
	_, err := r.AppendAssign(rec)
	return err
}

// Records 载入全部 record (容忍末尾被截断的一行)。
func (r *Registry) Records() ([]Record, error) {
	lines, err := r.readLines()
	if err != nil {
		return nil, err
	}
	out := make([]Record, 0, len(lines))
	for _, ln := range lines {
		if rec, ok := decodeRecord(ln.text); ok {
			out = append(out, rec)
		}
	}
	return out, nil
}

// Best 返回得分最高的 record, 可选地限定为 feasible 的 design 和/或某一种
// algorithm (空字符串 = 不限)。
func (r *Registry) Best(feasibleOnly bool, algorithm string) (*Record, bool) {
	recs, err := r.Records()
	if err != nil {
		return nil, false
	}
	var best *Record
	for i := range recs {
		if feasibleOnly && !recs[i].Feasible {
			continue
		}
		if algorithm != "" && recs[i].Algorithm != algorithm {
			continue
		}
		if best == nil || recs[i].Score > best.Score {
			best = &recs[i]
		}
	}
	return best, best != nil
}

// BestPerAlgorithm 返回每个 algorithm 名称下的最佳 record。
func (r *Registry) BestPerAlgorithm() map[string]Record {
	recs, err := r.Records()
	if err != nil {
		return map[string]Record{}
	}
	out := make(map[string]Record, 4)
	for i := range recs {
		cur, ok := out[recs[i].Algorithm]
		if !ok || recs[i].Score > cur.Score {
			out[recs[i].Algorithm] = recs[i]
		}
	}
	return out
}

// Lineage 给出 design_id -> 直接子节点 design_id 的映射。
func (r *Registry) Lineage() map[string][]string {
	recs, err := r.Records()
	if err != nil {
		return map[string][]string{}
	}
	return lineageOf(recs)
}

// lineageOf 从已载入的切片构建 design_id -> 直接子节点的映射。子节点的顺序跟随
// record 顺序, 因此结果是确定的。
func lineageOf(recs []Record) map[string][]string {
	tree := make(map[string][]string)
	for i := range recs {
		if recs[i].ParentDesign == "" {
			continue
		}
		tree[recs[i].ParentDesign] = append(tree[recs[i].ParentDesign], recs[i].DesignID)
	}
	return tree
}

// BranchRow 是分支改进表格中的一行。
type BranchRow struct {
	DesignID       string  `json:"design_id"`
	NChildren      int     `json:"n_children"`
	ParentScore    float64 `json:"parent_score"`
	BestChildScore float64 `json:"best_child_score"`
	Gain           float64 `json:"gain"`
}

// BranchImprovement 按父 design 汇报它最好的子节点相对它改进了多少, 按 gain
// 降序排列。
func (r *Registry) BranchImprovement() []BranchRow {
	recs, err := r.Records()
	if err != nil {
		return []BranchRow{}
	}
	children := lineageOf(recs)
	byID := make(map[string]Record, len(recs))
	for i := range recs {
		byID[recs[i].DesignID] = recs[i]
	}
	rows := make([]BranchRow, 0, len(children))
	seenParent := make(map[string]bool, len(children))
	// 遍历 record (而不是 map), 这样行的顺序是确定的。
	for i := range recs {
		parent := recs[i].DesignID
		kids, isParent := children[parent]
		if !isParent || seenParent[parent] {
			continue
		}
		seenParent[parent] = true
		bestChild, nKnown := 0.0, 0
		for _, k := range kids {
			kid, ok := byID[k]
			if !ok {
				continue
			}
			if nKnown == 0 || kid.Score > bestChild {
				bestChild = kid.Score
			}
			nKnown++
		}
		if nKnown == 0 {
			// 该 design 没有任何子节点进入过 registry: 这是一条悬空的 lineage 边,
			// 而不是一行改进。
			continue
		}
		rows = append(rows, BranchRow{
			DesignID:       parent,
			NChildren:      len(kids),
			ParentScore:    recs[i].Score,
			BestChildScore: bestChild,
			Gain:           bestChild - recs[i].Score,
		})
	}
	// 按 gain 降序; 并列时由 design_id 决定先后, 以保证表格可复现 (上面的 Go map
	// 遍历本身是无序的, 而不稳定的并列顺序会让同一个 registry 在不同次运行中渲染
	// 出不同结果)。
	sort.Slice(rows, func(a, b int) bool {
		if rows[a].Gain != rows[b].Gain {
			return rows[a].Gain > rows[b].Gain
		}
		return rows[a].DesignID < rows[b].DesignID
	})
	return rows
}

// Summary 是对 registry 内容的紧凑描述。
type Summary struct {
	NRecords      int            `json:"n_records"`
	NFeasible     int            `json:"n_feasible"`
	PerAlgo       map[string]int `json:"per_algorithm"`
	BestScore     float64        `json:"best_score"`
	BestDesignID  string         `json:"best_design_id"`
	BestAlgorithm string         `json:"best_algorithm"`
}

// Summary 计算上面那个 summary。
func (r *Registry) Summary() Summary {
	sum := Summary{PerAlgo: map[string]int{}}
	recs, err := r.Records()
	if err != nil {
		return sum
	}
	sum.NRecords = len(recs)
	var best *Record
	for i := range recs {
		if recs[i].Feasible {
			sum.NFeasible++
		}
		sum.PerAlgo[recs[i].Algorithm]++
		if best == nil || recs[i].Score > best.Score {
			best = &recs[i]
		}
	}
	if best != nil {
		sum.BestScore = best.Score
		sum.BestDesignID = best.DesignID
		sum.BestAlgorithm = best.Algorithm
	}
	return sum
}

// Check 校验注册表: id 从 1 连续、必需字段齐全、每个 parent_design 都能解析。
// 返回一个人类可读的问题列表 (空 = 健康)。供验收门使用。
func (r *Registry) Check() ([]string, error) {
	lines, err := r.readLines()
	if err != nil {
		return nil, err
	}
	type entry struct {
		lineNo int // 物理行号, 用于消息
		pos    int // 从 1 开始的 record 序号, experiment_id 即以此为基准校验
		raw    map[string]json.RawMessage
		rec    Record
	}
	problems := []string{}
	entries := make([]entry, 0, len(lines))
	for i, ln := range lines {
		raw, rawErr := decodeObject(ln.text)
		rec, recErr := decodeRecord(ln.text)
		if rawErr != nil || !recErr {
			// 只有最后一行被截断才是可以容忍的 (那是进程被杀时留下的痕迹)。中间出现
			// 损坏的一行是真实的损伤: 它承载的 record 已经永久丢失。注意它仍占据序列中
			// 的位置, 因此不会改变其后 record 的 id。
			if i == len(lines)-1 {
				continue
			}
			problems = append(problems, fmt.Sprintf(
				"line %d: not valid JSON (a truncated final line is tolerated, an unreadable interior line is not)", ln.no))
			continue
		}
		entries = append(entries, entry{lineNo: ln.no, pos: i + 1, raw: raw, rec: rec})
	}

	// 1. experiment_id 从 1 连续: 第 k 条 record 必须携带 id k, 因此被删除的
	//    record、重新编号、重复、或损坏的一行都会表现为同一条可检查的陈述。
	firstLineOfID := make(map[int]int, len(entries))
	for _, e := range entries {
		id := e.rec.ExperimentID
		switch {
		case id <= 0:
			problems = append(problems, fmt.Sprintf(
				"line %d: experiment_id %d is out of range (record %d must carry id %d)",
				e.lineNo, id, e.pos, e.pos))
		case id != e.pos:
			if first, dup := firstLineOfID[id]; dup {
				problems = append(problems, fmt.Sprintf(
					"line %d: experiment_id %d is not sequential (duplicate of the record on line %d; record %d must carry id %d)",
					e.lineNo, id, first, e.pos, e.pos))
			} else {
				problems = append(problems, fmt.Sprintf(
					"line %d: experiment_id %d is not sequential (record %d must carry id %d)",
					e.lineNo, id, e.pos, e.pos))
			}
		}
		if _, seen := firstLineOfID[id]; !seen {
			firstLineOfID[id] = e.lineNo
		}
	}

	// 2. 必需字段存在于 RAW 行中 (类型化解码看不到缺失的键: 它只会给出零值),
	//    外加 design_id 的规范性检查。
	firstLineOfDesign := make(map[string]int, len(entries))
	for _, e := range entries {
		var missing []string
		for _, f := range RequiredFields {
			if _, ok := e.raw[f]; !ok {
				missing = append(missing, f)
			}
		}
		if len(missing) > 0 {
			problems = append(problems, fmt.Sprintf(
				"line %d: missing required field(s): %s", e.lineNo, strings.Join(missing, ", ")))
		}
		switch {
		case e.rec.DesignID == "":
			problems = append(problems, fmt.Sprintf("line %d: design_id is empty", e.lineNo))
		default:
			if first, dup := firstLineOfDesign[e.rec.DesignID]; dup {
				problems = append(problems, fmt.Sprintf(
					"line %d: duplicate design_id %q (first seen on line %d)", e.lineNo, e.rec.DesignID, first))
			} else {
				firstLineOfDesign[e.rec.DesignID] = e.lineNo
			}
		}
	}

	// 3. 每条 lineage 边都指向一个确实存在的 design。
	for _, e := range entries {
		if e.rec.ParentDesign == "" {
			continue
		}
		if _, ok := firstLineOfDesign[e.rec.ParentDesign]; !ok {
			problems = append(problems, fmt.Sprintf(
				"line %d: parent_design %q does not exist in the registry", e.lineNo, e.rec.ParentDesign))
		}
	}
	return problems, nil
}
