# 实验 B —— Rule-guided Search 消融（Phase B）

**结论先行：HUSHFUSION 目前是好的 "replay system"，还没有抽象出可迁移的工程知识。**
Champion（记忆）极强、Rule（规则抽象）弱 —— 正是文档预言里"Champion 强、Rule 弱"的情形。

## 0. 这是什么

实验 A 证明了"过去有效设计降低未来成本"。实验 B 追问**到底是什么在产生复利** ——
是"记住冠军点"，还是"抽象出规则"？四组固定 spec/budget/seed/种群/变异/选择/评估器，只变知识来源：

| 方法 | 首代来源 | 继承 |
|---|---|---|
| `evolution`（冷） | 全盒随机 | 无 |
| `evolution_knowledge`（Champion） | 上一轮 top-K 最优设计 | 冠军点 |
| `evolution_rule`（Rule） | 规则偏置子空间采样 | 规则（design_rules.md） |
| `evolution_champion_rule`（Champ+Rule） | 冠军 top-K 拼规则采样 | 两者 |

规则：`design_rules.md` 的 R001/R002/R003（"I_0/I_1/I_3 ↑ → cost ↑"，cost 是负项 ⟹
降电流更好）。规则方法把这三个电流参数的下半盒采样（方向 bias），其余全盒。

## 1. 数据（4 方法 × 7 budget × 5 seeds，best_mean）

| budget | evolution(冷) | champion | rule | champ+rule |
|---|---:|---:|---:|---:|
| 10 | −0.9057 | **0.9489** | −0.4890 | **0.9489** |
| 20 | −0.2127 | **0.9489** | −0.2079 | **0.9489** |
| 50 | 0.2480 | **0.9489** | 0.2301 | **0.9489** |
| 100 | 0.4159 | **0.9575** | 0.5452 | **0.9575** |
| 200 | 0.5447 | **0.9489** | 0.6063 | **0.9489** |
| 500 | 0.7314 | **0.9713** | 0.8313 | **0.9713** |
| 1000 | 0.8763 | **0.9759** | 0.9099 | **0.9759** |

Evals-to-Target（target=0.5，跨 5 seed 平均，- = 该 budget 内未达）：

| budget | evolution | champion | rule | champ+rule |
|---|---:|---:|---:|---:|
| 10 | − | 1.0 | − | 1.0 |
| 20 | − | 1.0 | − | 1.0 |
| 50 | 23.5 | 1.0 | − | 1.0 |
| 100 | 35.3 | 1.0 | 72.2 | 1.0 |
| 200 | 157.3 | 1.0 | 132.7 | 1.0 |
| 500 | 225.6 | 1.0 | 167.6 | 1.0 |
| 1000 | 210.8 | 1.0 | 206.6 | 1.0 |

## 2. 三条结论（诚实读出）

**(1) Champion 全面碾压**：每个 budget 都 0.9489+，Evals-to-Target 全是 1.0。
但——**"1.0"是播种的冠军本身**（score 0.9489 > 0.5 直接放进首代），不是搜索找到的。
这测的是"记忆有效"，不是"复用高效"。

**(2) Rule 弱但不算无效**：Rule 在 budget≥100 稳定超过冷搜索（100: 0.545 vs 0.416、1000: 0.910 vs 0.876），
但远低于 champion。Evals-to-Target 在 budget≥100 有效（72/133/168/207 次达 0.5，比冷搜索少）。
⟹ **规则方向（降电流）是对的，但只靠 3 条成本规则不足以逼近最优** —— 它没碰 field/mirror/volume 这些正贡献项。

**(3) Champ+Rule ≈ Champion**：拼接种群后 rule 的贡献被 champion 主导（都一样 0.9489+）。
⟹ 规则目前是"锦上添花但不主导"，champion 是主导因素。

## 3. 核心判断：HUSHFUSION 还没形成"可迁移知识"

按文档的分叉判据：

```
Champion 强、Rule 弱  ⟹  目前是好的 replay system, 还没抽象出可迁移的工程知识。
```

这正是**诚实的负面结果**（不是失败）。它指向下一步（Phase C 之前要先做的一件事）：
**规则太稀薄** —— design_rules.md 只有 3 条，全在 cost 项、全指"降电流"。它没有覆盖
field/mirror/volume/ripple，所以 Rule 方法只能优化成本一个维度，自然追不上 champion。

## 4. 诚实边界

- **champion 的 Evals-to-Target=1.0 是"播种即达"，不是搜索**：champion 直接把上一轮冠军放首代。
  要测"继承后继续搜索更快"，target 要设得比 0.9489 高（如 0.97），看谁先到。
- **Rule 只用了 3 条成本规则**：不是规则方法本身没用，是规则覆盖不足。
- **同 spec 继承**：champion 来自 runs/phase1（同一 spec）。跨 spec 是 Phase C。

## 5. 实现（本次新增）

- `internal/search`：`AlgorithmEvolutionRule` / `AlgorithmEvolutionChampionRule` 两方法 +
  `EvolutionRule` / `EvolutionChampionRule` 分发（复用 WarmPopulation 机制）。
- `cmd/forge`：`benchmark --rule FILE`（读 design_rules.md 的 ```json 块 + term 符号权重映射
  → 生成规则偏置子空间种群）+ `--target N`（Evals-to-Target 报告，从 History 算首个 ≥ target 的索引）。
- 测试：方法数断言 5→7。
- 数据：`runs/phaseB/b{10..1000}/`（每次 4 方法 × 5 seeds）。

## 6. 下一步（文档 Day 11-14 / Phase C 前）

1. **加厚规则**：用更大的 registry（phase1 全 12001 条，不只 min-n=150）挖覆盖 field/mirror/volume/ripple 的规则。
2. **Rule 消融在更丰富规则上重跑**：看规则能否从"锦上添花"变"主导"。
3. **跨 spec 的 Knowledge Survival（Phase C）**：换线圈数/bounds，看 champion 和 rule 还成不成立。
