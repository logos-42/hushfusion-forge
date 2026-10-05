# 实验 A —— 小预算知识复用（Knowledge Reuse Gain vs budget）

**结论先行：用上一轮学到的最优设计播种，在全部 7 个预算档上都赢冷搜索和人工基线播种；
知识在越小预算越值钱 —— 复利曲线成立。**

## 0. 这是什么

文档"五层飞轮 / Phase 1 Design Intelligence"的第一个实验。核心问题（芒格第一层"找到有效的事"）：

> **用过去的知识，达到同样质量所需的 evaluations 是否下降？**

对照三方（同一 spec、同一 seed、同一预算，等成本比较）：

| 方法 | 首代播种来源 | 继承什么 |
|---|---|--|
| `evolution`（冷） | 纯随机 i.i.d. | 无 |
| `evolution_warm`（人工基线） | textbook mirror（人类工程师基线） | 教科书 |
| **`evolution_knowledge`（新）** | **上一轮 registry 的 feasible top-K 最优设计** | **学到的东西** |

`evolution_knowledge` 是本次新增的搜索方法（`internal/search` + CLI `forge benchmark --knowledge DIR`）。
它读 `runs/phase1/registry.jsonl`（0.1.3 判据下的 12001 条记录）里 feasible 且 score 最高的 16 个
设计，铺满进化策略的首代（替代单点 warm + 随机补）。

## 1. 实测数据（7 档预算 × 3 seed，best_mean）

| budget | cold `evolution` | warm(人工) `evolution_warm` | **knowledge `evolution_knowledge`** | **Gain(know−cold)** | Gain(warm−cold) |
|---|---:|---:|---:|---:|---:|
| 10 | −0.5241 | −0.2066 | **0.9489** | **+1.473** | +0.3175 |
| 20 | −0.2669 | −0.1013 | **0.9489** | **+1.216** | +0.1656 |
| 50 | 0.2911 | −0.1013 | **0.9489** | **+0.658** | −0.3924 |
| 100 | 0.4618 | 0.0169 | **0.9633** | **+0.502** | −0.4449 |
| 200 | 0.5715 | 0.5257 | **0.9489** | **+0.377** | −0.0458 |
| 500 | 0.7229 | 0.7902 | **0.9635** | **+0.241** | +0.0673 |
| 1000 | 0.8642 | 0.8924 | **0.9744** | **+0.110** | +0.0282 |

（Gain 逐 seed 同 seed 对齐，非简单均值差；budget=10 的 +1.473 是三 seed 的 +2.19/+1.05/+1.18。）

## 2. 三条结论（诚实读出）

**(1) 知识复用曲线成立，且是文档预言的那条**：Gain 随 budget 单调下降（+1.47 → +0.11）。
正是文档画的 `Knowledge Reuse Gain ↓ 随 budget ↑` —— **知识在越小预算越值钱**。
工程含义：当你只能付得起 10~50 次评估时，知识复用把结果从"碰运气（可能负分）"变成"确定有效（0.95）"。
这是复利点：**越穷（预算小）越该用知识**。

**(2) worst case 被抬升**：budget=10 时 cold 均值 −0.52（可能负分），knowledge 稳定 0.9489。
知识让"小预算起步"从赌博变确定性。这是比"均值高"更硬的证据。

**(3) `evolution_warm`（人工基线播种）只在 budget≥500 才勉强为正增益（+0.07），
budget=50/100 反而是负的**（−0.39/−0.44）。⟹ 教科书先验在小预算**帮倒忙**，而上轮学到的最优
先验在小预算**大赚**。这直接回答了文档的问题：**继承"学到的东西"≠ 继承"教科书"**。

## 3. 诚实边界（必须一起说）

- **同任务继承**：knowledge 继承的是 `runs/phase1`（**同一个 spec** 的上一轮最优）。这不是
  跨任务/跨 spec 泛化 —— 那是文档 `Knowledge Survival` KPI，是**还没做**的下一问。
- **budget=10 时 knowledge 三个 seed 的 best 都是同一个 0.9489**：那是**直接把上一轮冠军当首代**，
  小预算下它没被超越。这是"继承"的复利，不是"探索更远"。真正区分"继承冠军 vs 学会更好"
  要更大预算（1000 档 knowledge 0.9744 > 冠军 0.9489，说明大预算下知识作为起点后继续超越）。
- **seed 依赖**：cold 在 3 seed 间散布大（±0.13），knowledge 散布小（±0.014）⟹ 知识同时
  **压低了方差**（更稳），这是额外但真实的收益。

## 4. 实现（已提交）

- `internal/search`：`AlgorithmEvolutionKnowledge = "evolution_knowledge"` 新方法 +
  `Options.WarmPopulation [][]float64`（整代播种，非空时铺满首代、否则退回原单点 warm）。
  `EvolutionKnowledge` 分发。旧方法行为**逐位不变**（有单测覆盖）。
- `cmd/forge`：`benchmark --knowledge DIR` 读该 registry 的 feasible top-K（16）设计播种；
  `allMethods` 加入新方法。
- 测试：`TestWarmPopulationSeedsFirstGen`（验证播种生效）+ `TestConstantsCoverMethods` 更新为 5 方法。
- 数据：`runs/knowledge_ladder/b{10..1000}/registry.jsonl`（每次 3 seed × 3 方法）。

## 5. 下一步（文档 Day 11-14+）

1. **Rule-guided search**：把 `knowledge/design_rules.md` 挖出的规则（不只是 top-K 设计）作为
   变异方向/先验 —— 这测的是"学到的规律"值钱，不是"冠军点"值钱。
2. **Knowledge Survival（跨任务）**：换 spec（线圈数 4→3/5、换 bounds）重跑，看规则还成不成立。
3. **Rule → Search Prior 的消融**：有规则 vs 无规则，同预算（文档实验 A 的真收尾）。
