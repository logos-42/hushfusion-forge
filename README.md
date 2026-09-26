# HUSHFUSION Forge

**中文** | [English](README.en.md)

> 不是造一台装置，而是造一个**能不断产生、验证、淘汰下一代装置设计的工程系统**。
> Forge 是这个系统的第一版：电磁线圈设计的设计—评分—记录—学习闭环。

---

## 为什么是"设计循环"而不是"装置"

聚变装置本身不是第一阶段该交付的东西。一个只有一个人、没有实验室的团队，
先把"设计能力"本身做成资产，才是可积累的那部分：

```
参数化设计 → 磁场物理 → 目标函数 → 搜索 → 设计注册表 → 设计规则 ↺
```

一次实验真正产出的不是"一个更好的线圈"，而是**一条可被下一代复用的设计规则**。
这就是与"逐个造原型"的区别：知识被写进系统，而不是留在人脑里。

Phase 0 的验收问题只有一个：

> **机器能不能找到一个比"人工设计基线"更好的电磁设计？而且这条结论能被独立复现。**

---

## 架构（Go 为主栈，Python 只做辅助）

```
cmd/forge/            CLI：baseline / verify / run / benchmark / rules / report / registry / xcheck
internal/
  config/             唯一真源：全部物理常数、搜索边界、目标权重
  physics/            圆环电流精确静磁学（完整椭圆积分 AGM）+ 独立分段 Biot–Savart 交叉实现
  objective/          score = 场强 + 镜比 + 可用场体积 − 纹波 − 造价 − 违反约束罚
  baseline/           人工基线：Helmholtz 中央室 + 镜喉（中央室电流被**求解**到目标场强）
  registry/           设计注册表（JSONL，带 parent_design 谱系，是记忆不是日志）
  runner/             评分 → 分配 id → 落盘（注册表"天然完整"而不是靠自觉）
  search/             random / lhs / evolution / evolution_warm（等预算对比）
  experiment/         等预算 benchmark + 泛化探针 + 结果 JSON
  knowledge/          从注册表挖设计规则（每个 run 单独算 Spearman，全部同号才保留）
  report/             中文运行报告（含**诚实边界**章节）
  rlenv/              Phase 1 的 RL 环境接口 + 随机策略参考基线
  owners/             文件所有权名册 + 冻结门（互不重叠 / 无遗漏 / 变异会红）
python/               仅辅助：独立 scipy oracle（跨语言交叉验证）+ 出图 + 统计对账
```

两条硬规则：

1. **Go 是主栈**（引擎、CLI、注册表、搜索）；Python 只在"独立验证 / 出图 / 统计核对"
   这三件它确实更强的事上出现。Python **不**承载引擎逻辑。
2. **口径只有一处**：所有常数、边界、权重在 `internal/config/config.go`；
   Python 侧从 `testdata/golden_spec.json` 读 spec，不抄一份默认值。

---

## 物理口径与诚实边界（v0.1）

`internal/physics` 是**精确的真空静磁学**：圆环电流的闭式解（完整椭圆积分，AGM 算法），
外加一条**独立**的分段 Biot–Savart 求和实现，两者互为交叉验证。

它**不**包含：等离子体（压强、抗磁性、平衡、有限 β）、涡流、导体电流分配、
有限厚绕组包的真实场分布。因此 v0.1 的一切结论都是**真空场设计**结论——
"这个线圈配置的场结构好不好"，而**不是**"这台装置能不能聚变"。

人工基线也不是稻草人：它的几何是教科书镜机比例，电流是**求解**出来的
（中场空间平均场恰好等于目标值 1.0 T），因此在同一个目标函数下人机是被公平比较的。

## 快速开始

```bash
# 全部验收门（含跨语言 oracle 交叉验证、注册表完整性、反造假门）
bash scripts/verify.sh phase0

# 单次运行
go run ./cmd/forge baseline
go run ./cmd/forge benchmark --budget 1000 --seeds 0,1,2 --methods random,lhs,evolution,evolution_warm
go run ./cmd/forge rules --registry runs/phase0/registry.jsonl --out knowledge/design_rules.md
go run ./cmd/forge report --results runs/phase0/results.json --out runs/phase0/report.md
```

## 目录约定

- `runs/<tag>/` — 本次运行的证据：`registry.jsonl`（每次评估一行）、`results.json`、`report.md`、`figures/`
- `knowledge/design_rules.md` — 知识库：带证据（ρ、同号 run 数、分位对比）的设计规则
- `testdata/golden_*.json` — 冻结的跨语言数值锚点
- `CONTRACT.md` — 并行构建合同（文件所有权名册、数值锚点、验收门）

## License

MIT — Copyright (c) 2026 LIU YUANJIE（刘元杰）
