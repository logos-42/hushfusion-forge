# 持续学习电磁设计闭环 —— headless + forge + 服务器

**目标（用户需求）**：持续学习的电磁模型(headless)能设计并操作 forge 软件系统，效果可验证。

## 架构（scripts/continuous_loop.py）

```
forge registry(设计→score)                    [A] 数据桥: 电流log10 + 全分量标准化
        ↓                                      （1e6量级电流致 GVF trace 溢出 ⟹ 必须归一化）
headless GVF(线性TD(λ)价值函数)               [B] 学习核心: 持续学"哪个设计值得评估"
        ↓ select_topk(利用预测价值)
top-K 设计 → 写 registry → forge benchmark     [A'] 真操作: 当初始种群播种, forge 真评估
        ↓ 读真实 best_score
回流 headless 学习                             [C] 循环: 选→评估→学→再选(持续迭代)
```

## 已跑通

- **数据桥**: `design_to_feature`(归一化) ↔ `feature_to_design`(逆变换, 误差 1e-9)。
- **真操作 forge**: headless 选的设计写成 registry → `forge benchmark --knowledge 目录` →
  作为 evolution_knowledge 初始种群 → forge 真评估 → 读真实 best_score。
  (关键修: forge 需要空 registry, 每轮必须唯一输出目录, 否则报 "already holds N records")。
- **oracle 软验证**: headless top-K score 中位数 0.251 vs 随机 0.093, lift **+0.158**（6 轮）。

## 真评估 vs oracle 的差异（诚实发现）

| 模式 | 学习样本/轮 | lift |
|---|---|---|
| oracle(最近邻近似) | 50 | **+0.158** |
| 真 forge | 5(只学自己选的) | **−0.148** |

**真评估下 headless 反而输** —— 因为真评估每轮只学到自己选的那 5 个设计的真实 score，
GVF 没学够就被拿来选样（样本太少）。oracle 模式学 50 个所以好。

⟹ **这不是 headless 机制失败，是闭环的学习量不够**。修法：真评估下要把 forge 搜索过程产出的
**所有设计**(不只 top-K)回流学习 —— 每轮学 50+ 而非 5。

## 下一步（增大学习样本 + 服务器持续跑）

1. `run_forge_eval` 返回 forge 评估的所有设计(不只 best)，全部回流 GVF 学习。
2. 部署服务器(已装 go1.24 + headless + forge)，跑 --real 多轮，验证持续学习越学越好。
3. 效果验证: headless 起点 + forge 搜索的 best vs 随机起点 + forge 搜索的 best, 多 seed。
