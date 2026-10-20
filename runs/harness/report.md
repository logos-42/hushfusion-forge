# HUSHFUSION harness 本地自优化闭环 — 摘要

- 策略: `heuristic`
- episode 数: 40
- 评估步数（落盘数据点）: 277
- store 累计: 710
- 命中的问题（达到目标）: 30 (75.0%)
- 平均分: 1.7015
- 最高分: 1.7502 (天花板=1.7502)

## 各相位摘要（基线对照）

| 相位 | 策略 | episode | 平均分 | 命中率 |
|---|------|---------|--------|--------|
| P0 | `random` | 40 | 1.6301 | 78.0% |
| P1 | `heuristic` | 40 | 1.7015 | 75.0% |

## 动作 × 相位 平均Δ 矩阵（自调整信号）

| 动作 | P0(`random`) | P1(`heuristic`) |
|---|---|---|
| `flatten:omega` | +0.0937 | +0.3853 |
| `flatten:left` | +0.0586 | +0.0535 |
| `flatten:right` | +0.0746 | +0.0906 |
| `flatten:inner` | +0.0073 | +0.0420 |
| `flatten:middle` | +0.0404 | +0.1530 |
| `update_mu` | +0.6747 | +0.3195 |

## 动作平均 score 增量（规则层自调整信号）

| # | 动作 | 平均Δ |
|---|------|-------|
| 0 | `flatten:omega` | +0.1760 |
| 1 | `flatten:left` | +0.0565 |
| 2 | `flatten:right` | +0.0787 |
| 3 | `flatten:inner` | +0.0119 |
| 4 | `flatten:middle` | +0.0543 |
| 5 | `update_mu` | +0.4277 |

## Top 模型设计（按最终分）+ verdict

1. P0 ep0 seed1000003 — score=1.7502 命中=true 步=14 — **verdict: 命中**
   动作: update_mu → flatten:inner → update_mu → update_mu → flatten:left → flatten:omega → flatten:left → flatten:right → flatten:middle → flatten:omega → flatten:right → flatten:left → flatten:omega → update_mu
2. P1 ep0 seed1000003 — score=1.7502 命中=true 步=2 — **verdict: 命中**
   动作: flatten:omega → update_mu
3. P1 ep1 seed1000004 — score=1.7502 命中=true 步=7 — **verdict: 命中**
   动作: flatten:right → update_mu → flatten:middle → update_mu → flatten:right → flatten:omega → update_mu
4. P0 ep2 seed1000005 — score=1.7502 命中=true 步=3 — **verdict: 命中**
   动作: flatten:left → flatten:omega → update_mu
5. P1 ep2 seed1000005 — score=1.7502 命中=true 步=2 — **verdict: 命中**
   动作: flatten:omega → update_mu
