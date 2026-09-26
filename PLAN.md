# PLAN — HUSHFUSION Forge 路线图

> 一句话：**不要经营一个设备，经营一个能不断制造下一代设备的系统。**
> 本文件把这句话翻译成本仓可执行的东西：每个阶段交付什么、判据是什么、
> 哪些指标现在就能自动算出来、哪些必须等真实设备。

---

## 0. Phase 0 的唯一验收问题

> **机器能不能找到一个比"人工设计基线"更好的电磁设计？而且这条结论能被独立复现。**

对应本仓的可执行判据（`bash scripts/verify.sh phase0`）：

| 门 | 判据 |
|---|---|
| 物理正确性 | 轴上闭式解精确；Helmholtz 中心场 = (4/5)^1.5·μ₀I/a；闭式解与独立分段 Biot–Savart 一致 < 1e-9；真空 div/curl ≈ 0 |
| 跨语言一致 | 独立 Python/scipy oracle 复现冻结的 golden 数值（场 < 1e-9，score < 1e-6） |
| 人机对比公平 | 人工基线**必须落在搜索盒内**（解码不被裁剪）——否则比的是谁都没提过的设计 |
| 记录完整性 | registry 每条记录带全部原始 term + 谱系；id 连续；parent 存在 |
| 多 seed | benchmark 每方法 ≥3 seed，报 mean+std+min+max，禁止单 seed 结论 |
| 反造假 | 未实现的策略（Phase 1 的 RL）必须显式报错，不许返回编造数值 |

---

## 1. 与 NVIDIA 结构的一一对应（"经营"到底经营什么）

NVIDIA 厉害的不是 GPU，而是把 GPU 经营成一个自我强化的工业系统。四层结构映射到本项目：

| 层级 | NVIDIA | HUSHFUSION 现在 | 未来 |
|---|---|---|---|
| ① 技术架构 | GPU/互联/系统架构 | `internal/physics` + `objective`：场结构与目标函数的定义权 | 装置架构（磁体/真空/诊断的接口标准） |
| ② 软件系统 | CUDA/CUDA-X | `internal/search` + `rlenv`：提出下一代设计的能力 | 设计自动化平台（相当于未来的"CUDA"） |
| ③ 工业系统 | TSMC/HBM/机架 | `registry` + `knowledge`：制造/实验经验的沉淀层 | 供应链与实验平台（Phase 3 起） |
| ④ 市场系统 | 云厂商/AI Lab | **尚无**（现在不建） | 科研合作 / 服务化 |

**边际成本递减发生在②④之间，而不是在硬件里**：
每多花一次评估，成本是固定的（一次仿真 ≈ 毫秒～秒），但
① 搜索先验更好（热启动 → 收敛更快）、② 设计规则更多（复用 → 搜索空间更小）、
③ 失败记录不重复（registry 里的不可行设计不会再被提出）。
三者叠加 = **第 N 台设计得比第 N−1 台更便宜、更快**。这就是本仓要量化的东西。

## 2. 进步护城河（Progress Moat）在本仓的形态

```
设计 D → 实验(sim) → 结果 → 规则 → 更好的搜索先验 → 设计 D+1 → ...
```

护城河不是"我有一个好设计"（会被抄），而是：

- `runs/<tag>/registry.jsonl` 里**别人必须重新走一遍**的评估历史；
- `knowledge/design_rules.md` 里**带证据**（每 run 同号的 ρ、分位对比）的设计规则；
- `evolution_warm` 与 `evolution` 的差值——**这就是"继承设计知识值多少钱"的第一次定量测量**。

竞争者今天才开始，就得重新走完这条曲线；而曲线越陡，差距越大。

---

## 3. 90 天路线（Phase 0 … Phase 3）

### Phase 0 — 建立"设计—验证"循环（本仓当前阶段）

**目标：能自动产生设计。** 产物：**Forge v0.1**

| 项 | 落地 | 状态 |
|---|---|---|
| 参数化线圈（r, z, I）×4 | `internal/physics` | ✅ 已实现（19 项解析锚点：轴上闭式 2.98e-16、Helmholtz 均匀度 1.14e-4、离散-解析 1.19e-14、div/curl 1.4e-10） |
| 电磁场计算（Biot–Savart） | `internal/physics/magnet.go` | ✅ 两条独立代码路径互验（AGM 椭圆积分 + 分段级数） |
| 目标函数（场强/镜比/体积/纹波/造价/约束） | `internal/objective` | ✅ 分数可位级回算（`Σweighted − w·Σpenalties`） |
| 随机/分层/进化/热启动进化 | `internal/search` | ✅ 等预算整数性、位级复现性、Workers 不变性全绿 |
| 每次评估落盘（含 term 分解 + 谱系） | `internal/registry`+`runner` | ✅ 100 goroutine 并发无缺口；截断尾部自修复 |
| performance vs iteration 曲线 | `internal/report` + `python/aux/analyze.py` | ✅ 报告 7 章节 + 出图 + 独立复评 |
| 一页"我们学到了什么" | `internal/knowledge` → `knowledge/design_rules.md` | ✅ 规则由「每个 run 同号」硬过滤挖出，scipy 独立对账 |

**Phase 0 实测结果**（2026-09-26，`runs/phase0/`，12001 条记录，4 方法 × 3 seed × 1000 次评估，纯计算 5.5 s）：

| method | seeds | best mean | best std | 赢基线 | evals-to-beat(mean) |
|---|---:|---:|---:|---:|---:|
| 人工基线 `textbook_mirror` | — | −0.290571 | — | — | — |
| `evolution` | 3 | **1.303252** | 0.015924 | 3/3 | 8.3 |
| `evolution_warm` | 3 | **1.307292** | 0.064361 | 3/3 | 11.3 |
| `lhs` | 3 | 0.628765 | 0.021582 | 3/3 | 14.0 |
| `random` | 3 | 0.838502 | 0.191865 | 3/3 | 8.0 |

机器最优 **D9857，score = 1.381419**（evolution_warm, seed 0），Δ vs 人类 = **+1.672**。
三个必须一起说的事实（`runs/phase0/report.md` §6 有全文）：

1. **人工基线在这个盒子里是弱基线**：随机搜索平均 8 次评估就追平它。它赢在"是个真装置"，不赢在指标。
2. **热启动的增益在 1000 次预算下接近噪声**（1.3073 vs 1.3033，后者方差还更小）——
   `evolution_warm` 只是把收敛前的初始位置提前，长预算下被追平。**"继承知识值多少钱"这一问
   在 Phase 0 的答案是：在这个预算下 ≈ 0**，必须换更硬的判据（更小预算、跨任务迁移）才能测出来。
3. **最优点在靠奇异性得分**（见下节），这是 Phase 0 最重要的负面结果。

### Phase 0 的一个真实发现：最优设计踩在钳位半径上

D9857 把两个线圈摆在 `a=0.10 m`、`z=−0.058 / +0.0042 m`，其中一圈距中场采样点仅
**4.196 mm**——落在求解器 5 mm 的 `alpha2` 钳位半径**之内**。后果（两次独立复算实证）：

| 复算方式 | B_mid | 与 Go 记录值的差 |
|---|---:|---:|
| Go 记录值（钳位） | 9.4119443 T | — |
| Python oracle + 同样的 5 mm 钳位 | 9.4119443 T | **1.7e-15** |
| Python oracle 不钳位（精确闭式） | 9.7930532 T | 4.05e-02 |

结论有两层，都要记：

- **工程上**：目标函数目前允许"把导体贴到打分点旁边"来换中场强。真实装置不可能在约束区域内部
  放一圈 0.1 m 半径、1.78 MA 的线圈。**所以这个"最优设计"很可能不是可造的装置**，
  而是一个被目标函数合法利用的退化解。
- **数值上**：跨语言比场时必须对齐钳位半径（`--proximity-floor 0.005`），否则 1e-2 量级的
  "分歧"会周期性出现——那是约定不一致，不是 bug。这条已写死进 `scripts/verify.sh` G10。

**因此 Phase 1 的第一件事不是新算法，而是补一条"可造性"约束**（导体到约束区域的净空）：

### Phase 1 — 让系统学会设计（Day 8–30）

**目标：让搜索带着上一轮的知识跑。** 产物：**Fusion Design Agent v0.1**

- [ ] **补可造性约束（Phase 1 第一件事）**：导体到约束区域的最小净空（clearance）进 penalty 组，
      然后跑**消融**：加约束前后，机器对人工基线的那 +1.672 还剩多少？这是本仓第一个真正的
      "物理约束值多少钱"实验，也是把退化解踢出最优位的唯一方法。
- [ ] Bayesian optimization（GP/代理模型）与 evolution 等预算对比
- [ ] 把 `internal/rlenv` 接到已有的 OaK/持续学习实现上（**接口已就位，环境已完整，只缺策略**）
- [ ] 设计规则作为搜索先验：`knowledge.RuleExpectation` 做**消融**（有先验 vs 无先验，同预算）
- [ ] benchmark 表加入 `泛化` 列（已有判据：六个需求扰动变体的稳健性探针）
- [ ] 设计注册表的谱系分析产出"哪个分支改进最多"
- [ ] `rlenv` 观测槽 6（`cost_proxy`）改为按基线 cost 归一——现在是原始量级的 1.8e12，对策略是量纲极差的特征（需同步改冻结的 `ObsMetricRefs`）
- [ ] 报告 §4 接入 `registry.BranchImprovement()` 的分支增益表（报告 schema 冻结，加字段需同步 schema_check 与报告测试）
- [ ] 小预算阶梯（50/100/200）重测热启动增益——1000 次预算下它落在噪声里（+0.004，方差更大）

判据：多 seed、等预算、消融齐全；RL 必须与 random policy 与 evolution 同时对比，不允许"RL=一切"。

### Phase 2 — 让系统学会预测（Month 2）

**目标：让仿真能预测现实。** 产物：**Fusion Digital Twin v0.1**

- [ ] 更高保真求解器（有限厚绕组包、导体磁化、涡流）——**放在同一个 `physics.Solver` 接口后面**，
      不动物理层以外的任何代码（这就是接口冻结的价值）
- [ ] 代理模型 + 不确定性（预测误差本身变成一等公民）
- [ ] 定义并记录 `simulation − reality` 误差；误差驱动模型更新

### Phase 3 — 让软件连接现实（Month 3）

**目标：闭环落到物理。** 产物：**Hardware Loop v0.1**

- [ ] 小线圈 + 电源 + 传感器 + DAQ（数千～数万元级）
- [ ] 自动采集 → 与仿真比对 → 回写 registry（**测量**成为记录的一种类型）
- [ ] 这一天才算 HUSHFUSION 真正诞生：仿真→设计→硬件→测量→数据→模型的完整回路

---

## 4. 现在就能自动算出来的经营指标

| 指标 | 本仓来源 | 为什么它是经营指标 |
|---|---|---|
| Design Progress / iteration | `SearchResult.History` | 学习速率，不是结果 |
| Evals-to-beat-human | `SearchResult.EvalsToBeat` | **追平熟练工程师所需的设计成本** |
| Simulation cost / eval | benchmark 实测 | 边际成本的分子 |
| Knowledge reuse gain | `evolution_warm` − `evolution` | 继承知识值多少钱 |
| Feasibility rate | registry `feasible` 占比 | 搜索是否在物理可行域内工作 |
| Branch improvement | `registry.BranchImprovement()` | 哪个设计分支最会改进（团队结构雏形） |
| Generalisation retention | `RobustnessProbe` | 需求变了还算不算数 |

**尚不能算（等真实设备）**：Cost / Build Time / MTBF / Yield / Time-to-Next-Prototype——
它们需要有东西可以被制造。此时把表列出来、留空，比编造数字诚实。

---

## 5. 硬件学习曲线（占位，Phase 3 起填）

| Generation | Cost | Build Time | Performance | Reliability | Knowledge |
|---|---:|---:|---:|---:|---:|
| G0 | — | — | — | — | — |
| G1 | — | — | — | — | — |

追求的方向固定：**Cost ↓ Build Time ↓ Performance ↑ Reliability ↑ Knowledge ↑**

---

## 6. 今天的最小行动（不要设计三年后的系统）

```bash
go run ./cmd/forge baseline                    # 人工基线长什么样、多少钱、什么场结构
go run ./cmd/forge benchmark --budget 1000 --seeds 0,1,2
go run ./cmd/forge rules --registry runs/phase0/registry.jsonl --out knowledge/design_rules.md
bash scripts/verify.sh phase0                  # 全部门：物理锚点 + 跨语言 oracle + 复现性
```

跑通之后，第二个问题自然出现：**它能不能记住上一轮？**（Phase 1）
再之后：能不能跨任务迁移？能不能自己提出下一代结构？能不能用真实实验验证？
最后才是：**能不能制造聚变装置？**

---

## 7. 诚实边界

- v0.1 是**真空场**模型：没有等离子体、没有平衡、没有有限 β、没有涡流。
  它回答"这个线圈配置的场结构好不好"，**不回答**"这台装置能不能聚变"。
- 目标函数的权重是**工程判断，不是物理**。所以每个 term 都原样落盘：
  换权重不必重跑搜索，任何结论都可以按别的权重重新读一遍。
- "机器赢过人工基线"这句话，必须同时给出**赢在哪一项**（场强？镜比？造价？）——
  只报总分等于隐去信息。
- grid search 在 D=12 维下不可行（每轴 5 点 = 2.4e8 次），本仓用 LHS 作为诚实替代，
  并在报告里写明这个替换，而不是含糊过去。
- **最优解的合法性未经物理审查**：D9857 的线圈贴着中场采样点（4.2 mm），它的分数部分是
  目标函数允许的退化解（详见 §3 Phase 0 发现）。任何引用 `score = 1.381419` 的场合都必须
  连带这句限定，否则就是把一个不可造的位形说成"机器设计的装置"。
- `evolution_warm` 与 `evolution` 的差在 1000 次预算下不显著（+0.004，方差反而更大）。
  **"知识复用有正收益"这句话在 Phase 0 尚未被证实**，不许在 Phase 1 之前当成结论引用。
