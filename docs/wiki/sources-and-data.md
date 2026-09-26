---
title: 资料与数据
source: repo
created: 2026-09-26
last_confirmed: 2026-09-26
audience: self
stage: current
schema_version: 2.1
tags: [data, raw]
---

# 资料与数据

## 数据有三类，来源与可信级别完全不同

| 类别 | 位置 | 谁生成的 | 可信级别 |
|---|---|---|---|
| **冻结的跨语言真值** | `testdata/golden_*.json` | Python 参考实现（已进 git 历史后从工作树移除） | 最高：Go 与 scipy 两条独立路径都必须复现它（G5/G8/G10） |
| **本次 run 的全部评估记录** | `runs/phase0/registry.jsonl`（14 MB，12001 条） | Go 引擎自己写的 | 高：G11 完整性门 + G12 schema 门 + G15 独立复评 |
| **派生的知识与图表** | `knowledge/design_rules.md`、`runs/phase0/{report.md,figures/}` | Go 报告层 + Python 分析层 | 中：规则由 G14 与 scipy 对账，图表是渲染产物（可重生） |

`testdata/` 是**唯一真源**：任何 Go 侧数字与它对不上就是 Go 侧错。
`python/aux/oracle.py --emit-golden` 可以重算它，但默认拒绝写入 `testdata/`（要 `--force`），
避免"用新实现悄悄改真值"。

## raw 根目录（不在此仓）

```text
/Users/apple/Downloads/hushfusion_forge_raw/     ← init_raw_root.py 建，与仓库平级
  external_sources/  customer_answers/  screenshots/  extracted_text/  archive/
```

原件（论文 PDF、截图、外部资料）放这里，**不进 git**；仓库里只保留
`manifests/raw_sources.csv`（登记表）与编译后的结论。登记与巡检：

```bash
python3 scripts/ingest_raw.py
python3 scripts/stale_report.py
python3 scripts/delta_compile.py --write-drafts
```

## 明确**没有**的数据（不要假装有）

- **真实实验测量**：无。Phase 0 全部是真空静磁仿真。仿真与现实的误差（`simulation − reality`）
  在 Phase 3 之前**无法定义**，因为没有"现实"那一项。
- **等离子体数据**：无。目标函数里没有等离子体，只有场结构。
- **成本实测**：无。`cost_proxy = Σ I²a` 是几何代理量，不是报价。
- **硬件学习曲线**（Cost / Build Time / MTBF / Yield / Time-to-Next-Prototype）：无。
  `PLAN.md` §5 保留空表而不填数字——空表比编造的数字诚实。

## 登记纪律

新 raw 一进来就跑 `intake_filter.py`（脱敏）→ `ingest_raw.py`（登记 18 列 v2 清单）→ `raw_manifest_check.py`（门）。
清单里 `content_hash` / `size_bytes` / `ingested_at` 由脚本填，不手写。
