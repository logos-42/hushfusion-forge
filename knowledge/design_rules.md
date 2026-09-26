# Forge knowledge rules — tag phase0

- 生成时间 (UTC): 2026-09-26T02:45:16Z
- 输入: 12001 条 registry record(仅 feasible 参与挖掘)
- 规则数: 1

## 判定口径 (mining criterion)

每条候选规则在**每个 run((algorithm, seed)) 上单独**计算 Spearman 秩相关(并列名次取平均秩);
只有**所有 run 同号**的候选才保留,报告的 ρ 取各 run 中**绝对值最小**的那个(worst-case,不是最好看的那个),
置信度 = 同号 run 占比。run 内 feasible 记录少于 max(20, MinN/8) 的 run 被剔除,存活 run 少于 2 个则不产出任何规则。
方向用 decile 对比复述:参数最低 / 最高十分位时 term 的中位数。

## Scope

search box: 4 coils, radius [0.1,1] m, z [-1.2,1.2] m, current [1e+04,2.5e+06] A; b_ref=1 T, mirror_ref=2; vacuum analytic circular-filament magnetostatics (NO plasma, NO conductor/eddy model)

本文件从 12001 条 registry record 中挖掘;每条规则自己的 scope 字段记录它的挖掘样本
(n_designs / n_runs / 参与的 (algorithm,seed) 集合)。

## Rules

| rule_id | parameter | term | rho | sign_agreement | decile_low | decile_high | n_designs | n_runs |
|---|---|---|---|---|---|---|---|---|
| R001 | I_0 | cost | 0.381 | 1.00 | 0.317795 | 2.95358 | 11071 | 12 |

### 逐条陈述

- **R001** (I_0 / cost): 参数 I_0 越大,term cost 越大:参数最低十分位时 term 中位数 0.317795,最高十分位 2.95358(Spearman ρ=0.381 为各 run 中最差绝对值,同号 run 占比 100%,11071 designs / 12 runs)。
  - EN: cost rises with I_0: median cost = 0.317795 in the lowest decile of I_0 vs 2.95358 in the highest (Spearman rho=0.381, worst-case over runs; sign agreement 100% of runs; n=11071 designs over 12 runs).
  - scope: search box: 4 coils, radius [0.1,1] m, z [-1.2,1.2] m, current [1e+04,2.5e+06] A; b_ref=1 T, mirror_ref=2; vacuum analytic circular-filament magnetostatics (NO plasma, NO conductor/eddy model); feasible records only; 11071 designs over 12 runs ((algorithm,seed) pairs: evolution,evolution_warm,lhs,random); rules are claims about this box, not laws of nature

## 机器可读块 (machine-readable)

```json
[
  {
    "rule_id": "R001",
    "parameter": "I_0",
    "term": "cost",
    "rho": 0.3807183415892278,
    "sign_agreement": 1,
    "n_designs": 11071,
    "n_runs": 12,
    "decile_low": 0.3177951551354352,
    "decile_high": 2.953575393563636,
    "statement": "参数 I_0 越大,term cost 越大:参数最低十分位时 term 中位数 0.317795,最高十分位 2.95358(Spearman ρ=0.381 为各 run 中最差绝对值,同号 run 占比 100%,11071 designs / 12 runs)。",
    "statement_en": "cost rises with I_0: median cost = 0.317795 in the lowest decile of I_0 vs 2.95358 in the highest (Spearman rho=0.381, worst-case over runs; sign agreement 100% of runs; n=11071 designs over 12 runs).",
    "scope": "search box: 4 coils, radius [0.1,1] m, z [-1.2,1.2] m, current [1e+04,2.5e+06] A; b_ref=1 T, mirror_ref=2; vacuum analytic circular-filament magnetostatics (NO plasma, NO conductor/eddy model); feasible records only; 11071 designs over 12 runs ((algorithm,seed) pairs: evolution,evolution_warm,lhs,random); rules are claims about this box, not laws of nature"
  }
]
```
