---
title: HUSHFUSION Forge 项目概览
source: repo
created: 2026-09-26
last_confirmed: 2026-09-26
audience: self
stage: current
schema_version: 2.1
tags: [overview]
---

# HUSHFUSION Forge 项目概览

## 一句话

不是造一台装置，而是造一个**能不断产生、验证、淘汰下一代装置设计的工程系统**。
Forge 是这个系统的第一版：**电磁线圈设计的设计 → 评分 → 记录 → 学习闭环**。

## 它是什么 / 不是什么

| 是 | 不是 |
|---|---|
| 一个可复现的设计搜索与记账系统 | 一台聚变装置 |
| 真空静磁（Biot–Savart）域内的目标函数 | 等离子体平衡 / 有限 β / 涡流 / 输运 |
| 一次"机器能不能比人工基线找到更好的场结构"的可证伪实验 | 聚变能否实现的证据 |
| 一个把每次评估的原始 term 与谱系永久落盘的 registry | 一个好看但不可复核的排行榜 |

## 三层结构（对应 NVIDIA 的四层经营结构）

| 层级 | NVIDIA | 本仓落地 | 现在的状态 |
|---|---|---|---|
| ① 技术架构 | GPU / 互联 / 系统架构 | `internal/physics` + `internal/objective`：场结构与目标函数的**定义权** | 已冻结接口，19 项解析锚点全绿 |
| ② 软件系统 | CUDA / CUDA-X | `internal/search` + `internal/rlenv`：**提出下一代设计**的能力 | 4 种搜索已实装；RL 环境完整但**无策略**（Phase 1） |
| ③ 工业系统 | TSMC / HBM / 机架 | `internal/registry` + `internal/knowledge`：经验的沉淀层 | 已实装，12001 条记录实测 |
| ④ 市场系统 | 云厂商 / AI Lab | 尚无（现在不建） | 无 |

边际成本递减发生在 ②↔③ 之间：每多一次评估的仿真成本是常数，但搜索先验变好、
设计规则变多、失败不重复——三者叠加才是"第 N 台比第 N−1 台更便宜"。
**这是本仓要量化的东西，不是声称的东西。**

## 代码结构

```text
cmd/forge/            CLI：baseline | verify | run | benchmark | rules | report | registry | xcheck | version
internal/
  config/             唯一真源：搜索盒、权重、阈值、参考值。任何影响分数的数字都不许硬编码在别处
  physics/            圆环磁场闭式解(AGM 椭圆积分) + 独立分段级数实现 + 度量 + 网格
  objective/          计分律：terms → weighted → penalties → score（分数可位级回算）
  baseline/           人工基线（教科书镜场，中央室电流由 brentq 解到 B_ref）
  search/             random | lhs | evolution | evolution_warm（等预算、位级可复现）
  runner/             design → evaluate → record（回填 design_id / experiment_id）
  registry/           JSONL 记忆：并发无缺口、截断尾部自修复、完整性门、谱系/分支改进
  knowledge/          Spearman 规则挖掘（每个 run 同号才保留）+ 规则回读
  experiment/         benchmark harness + 需求扰动稳健性探针 + 聚合
  report/             markdown 报告（7 章节，含强制"诚实边界"节）
  rlenv/              RL 环境（Phase 1 接口；LoadPolicy 恒返回 ErrNoLearnedPolicy）
  owners/             所有权冻结门：文件→stage 名册，无重叠、无遗漏，且自身经变异测试
python/
  aux/oracle.py       独立 scipy 数值 oracle（不 import 任何 Go 代码）：G8 门 + 跨语言比对
  aux/schema_check.py registry 冻结 schema 校验（G12 门）
  aux/rules_check.py  Spearman 与同号 run 占比独立复算（G14 门）
  aux/analyze.py      出图 + 从设计向量独立复评基线/最优（G15 门）
testdata/             冻结的跨语言真值（golden_*.json），由 Python 参考实现生成
runs/<tag>/           每次 run 的 registry.jsonl + results.json + report.md + figures/
knowledge/            挖出的设计规则（人可读，带证据）
```

## 判据（唯一入口）

```bash
bash scripts/verify.sh phase0    # 16 道门，任何一道红就是红
```

每道门负责抓什么失败模式，写在 `scripts/verify.sh` 的注释里；当前红绿状态见
[current-status.md](./current-status.md)。

## 相关页面

- [current-status.md](./current-status.md) —— 现在到哪一步、实测数字、下一步
- [sources-and-data.md](./sources-and-data.md) —— 数据从哪来、哪些**没有**数据
- [github-and-raw-strategy.md](./github-and-raw-strategy.md) —— 什么进 git、什么不进
- [runtime-profile.md](./runtime-profile.md) —— 哪些脚本跑 CI、哪些只跑开发机
- [log.md](./log.md) —— 变更日志
