// record.go — 一次求值如何变成一条 registry record。
//
// Score() 被刻意拆成三块: 求值 (上游 physics/objective)、构造 record (纯函数, 本
// 文件)、以及记录 + 回填 (registry 的锁)。正是这个拆分让记录路径能在求值器还不可用
// 时就得到真实验证, 也正是它使交还给搜索层的 lineage 边是真正被写入的那条 record
// 的 id。
package runner

import (
	"fmt"

	"github.com/logos-42/hushfusion-forge/internal/objective"
	"github.com/logos-42/hushfusion-forge/internal/registry"
)

// evaluate 运行上游求值, 并在 runner 缺少依赖就被构建时给出清晰的失败 (三层调用
// 之外的 nil 解引用比这个难读得多)。
func (r *Runner) evaluate(x []float64) objective.EvalResult {
	if r.Reg == nil {
		panic("runner.Score: nil Registry — build the runner with runner.New(reg, ev, tag)")
	}
	if r.Ev == nil {
		panic("runner.Score: nil Evaluator — build it with objective.NewEvaluator(...) and pass it to runner.New")
	}
	return r.Ev.Evaluate(x)
}

// record 为一次求值构造 registry record。ExperimentID、DesignID 与 Timestamp 是
// 刻意留空的: registry 在持有自己的锁时分配它们, 这是多个 goroutine 并发求值时
// id 保持无空洞且唯一的唯一办法。
func (r *Runner) record(res objective.EvalResult, meta Meta) registry.Record {
	return registry.Record{
		ParentDesign: meta.Parent,
		Generation:   meta.Generation,
		Algorithm:    meta.Algorithm,
		Seed:         meta.Seed,
		EvalIndex:    meta.EvalIndex,
		Tag:          r.Tag,
		Score:        res.Score,
		Feasible:     res.Feasible,
		Params:       designParams(res.Design, res.Metrics.NCoils),
		Terms:        orEmpty(res.Terms),
		Weighted:     orEmpty(res.Weighted),
		Penalties:    orEmpty(res.Penalties),
		Metrics:      res.Metrics,
		Note:         meta.Note,
	}
}

// recordResult 追加 record, 并把这条 record 获得的 id 回填进搜索层收到的
// EvalResult。
func (r *Runner) recordResult(res objective.EvalResult, meta Meta) objective.EvalResult {
	if r.Reg == nil {
		panic("runner.Score: nil Registry — build the runner with runner.New(reg, ev, tag)")
	}
	assigned, err := r.Reg.AppendAssign(r.record(res, meta))
	if err != nil {
		// 被评分却没有被存储的 record 等于没有发生: registry 是报告在下游所做一切
		// 论断的真相来源。Python 参考实现也在这里抛错
		// (Registry.append -> ValueError/OSError); 冻结的 Go 签名没有错误可返回,
		// 所以用 panic 报告, 而不是悄悄丢掉这次 experiment。
		panic(fmt.Sprintf("runner.Score: recording the experiment in %s failed: %v", r.Reg.Path, err))
	}
	res.DesignID = assigned.DesignID
	res.ExperimentID = assigned.ExperimentID
	return res
}

// designParams 把规范 design 向量 [r_0..r_K, z_0..z_K, I_0..I_K] 拆成 record 里
// 按线圈命名的数组, 与 Python 参考实现完全一致 (design[:n]、design[n:2n]、
// design[2n:], 其中 n = n_coils)。
//
// 它绝不能在一个畸形向量上 panic: 如果求值返回的长度不是 3*n_coils, record 仍然
// 必须携带现有的全部证据 (params 数组偏短的 record 是可诊断的; 崩掉的 run 不是)。
func designParams(design []float64, nCoils int) registry.Params {
	k := nCoils
	if k <= 0 || 3*k > len(design) {
		k = len(design) / 3
	}
	if k < 0 {
		k = 0
	}
	radius := make([]float64, 0, k)
	radius = append(radius, design[:k]...)
	zEnd := min(2*k, len(design))
	z := make([]float64, 0, k)
	z = append(z, design[k:zEnd]...)
	current := make([]float64, 0, k)
	if 2*k < len(design) {
		current = append(current, design[2*k:]...)
	}
	return registry.Params{RadiusM: radius, ZM: z, CurrentA: current}
}

// orEmpty 保持 record 的 map 字段 JSON 类型稳定: nil map 会被写成 null, 而
// Python 参考实现写的是 {}。
func orEmpty(m map[string]float64) map[string]float64 {
	if m == nil {
		return map[string]float64{}
	}
	return m
}
