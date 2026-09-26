# Forge phase0 运行报告

**结论一句话**: 机器最优 score = 1.381418559(evolution_warm / seed 0),人工基线 score = -0.2905708161,Δ = +1.671989375 —— 机器**超过**人工基线。

- 逐项**赢**的项: cost, field, volume
- 逐项**输**的项: mirror
- 打平: ripple
- 泛化(§3): 该 design 在 6 个扰动变体上平均 Δ = +1.643816369,最差 Δ = +1.47618518,赢 6/6 个变体。

## 1. 设置

| 项 | 值 |
|---|---|
| tag | phase0 |
| timestamp (UTC) | 2026-09-26T02:45:03Z |
| budget (每次 run 的评估上限) | 1000 |
| seeds | 0, 1, 2 |
| methods | random, lhs, evolution, evolution_warm |
| workers | 1 |
| solver | analytic-vacuum-loops |
| cost_ref (人工基线欧姆代价) | 1.791703035e+12 |
| git commit | c0d3bb0c54bc |
| platform | darwin/amd64 |
| go version | go1.27.1 |
| 登记的设计数 (registry) | 12001(其中 feasible 11072) |
| registry best | 1.381418559 (D9857) |

spec(`config.Spec`,唯一真源):

| key | value |
|---|---|
| b_ref | 1 |
| bounds | current=[10000, 2500000], radius=[0.1, 1], z=[-1.2, 1.2] |
| coil_field_limit | 12 |
| confine_factor | 1.25 |
| j_eng | 100000000 |
| min_coil_sep | 0.05 |
| mirror_ref | 2 |
| n_axis | 161 |
| n_coils | 4 |
| n_params | 12 |
| n_vol_r | 13 |
| n_vol_z | 33 |
| r_plasma | 0.15 |
| self_field_T | 3.141592654 |
| t_pack | 0.05 |
| weights | cost=1, field=1, mirror=0.5, penalty=10, ripple=0.5, volume=0.75 |
| z_axis_max | 1.4 |
| z_cell | 0.8 |
| z_mid | 0.15 |

registry 记录分布: evolution=3000, evolution_warm=3000, human_baseline=1, lhs=3000, random=3000

## 2. 人工基线 vs 机器最优

人工基线: **textbook_mirror** — Helmholtz-like central cell (r=0.50 m, spacing=r) + mirror throats (r=0.30 m, |z|=1.00 m, I=3.5x cell). Cell current solved so the midplane field hits spec.b_ref exactly; not a straw man.(design D0001,4 个线圈)

### 2.1 目标项 (score 的组成)

| term | 人工基线 | 机器最优 | Δ (机器 − 人工) | 方向 | 谁更好 |
|---|---|---|---|---|---|
| cost | 1 | 0.2967029935 | -0.7032970065 | ↓ 好 | 机器 |
| field | -3.442446547e-11 | 0.9736793466 | 0.9736793466 | ↑ 好 | 机器 |
| mirror | 0.2475296965 | -0.0246820211 | -0.2722117176 | ↑ 好 | 人 |
| ripple | 0 | 0 | 0 | ↓ 好 | 打平 |
| volume | 0.7808857809 | 0.9557109557 | 0.1748251748 | ↑ 好 | 机器 |
| **score** | **-0.2905708161** | **1.381418559** | **+1.671989375** | **↑ 好** | **机器** |

### 2.2 物理 metrics(不是目标项,是证据)

| metric | 人工基线 | 机器最优 | Δ | 方向 |
|---|---|---|---|---|
| B_mid_T (midplane 体平均场) | 0.9999999999 | 9.411944262 | +8.411944262 | 无方向(参考) |
| B_throat_T (轴上峰值场) | 3.536386241 | 17.78391284 | +14.2475266 | 无方向(参考) |
| z_throat_m (峰值轴向位置) | -0.9975 | -0.0175 | +0.98 | 无方向(参考) |
| mirror_ratio | 3.536386241 | 1.889504691 | -1.646881549 | ↑ 好 |
| volume_good | 0.7808857809 | 0.9557109557 | +0.1748251748 | ↑ 好 |
| ripple | 0 | 0 | 0 | ↓ 好 |
| B_coil_max_T | 3.416271369 | 8.236302051 | +4.820030682 | ↓ 好 |
| min_coil_gap_m | 0.5 | 0.06217955806 | -0.4378204419 | ↑ 好 |
| cost_proxy | 1.791703035e+12 | 5.31603654e+11 | -1.260099381e+12 | ↓ 好 |

### 2.3 约束残差 (penalties)

| penalty | 人工基线 | 机器最优 |
|---|---|---|
| coil_separation | 0 | 见 registry.jsonl(机器最优记录携带同一字段) |
| conductor_field | 0 | 见 registry.jsonl(机器最优记录携带同一字段) |
| not_a_mirror | 0 | 见 registry.jsonl(机器最优记录携带同一字段) |

人工基线 feasible = true,机器最优 feasible = true。

人工基线 design 向量 (r…, z…, I…): `0.3, 0.5, 0.5, 0.3, -1, -0.25, 0.25, 1, 1621279.238, 463222.6396, 463222.6396, 1621279.238`

机器最优 design 向量: `0.3117116878, 0.1, 0.1, 0.6162864822, -0.2069077444, -0.05798370049, 0.004195857568, 1.2, 10000, 1467415.042, 1778145.561, 10000`

## 3. 方法对比 (等预算)

### 3.1 最优性能 (best-of-budget) 与收敛速度 (evals-to-beat)

| method | n_seeds | budget | best_mean | best_std | best_min | best_max | 赢过基线的 seed | frac | evals_to_beat mean | median |
|---|---|---|---|---|---|---|---|---|---|---|
| evolution_warm | 3 | 1000 | 1.307291513 | 0.06436056286 | 1.265627141 | 1.381418559 | 3 | 1.00 | 11.33333333 | 4 |
| evolution | 3 | 1000 | 1.303252216 | 0.01592427478 | 1.291972898 | 1.321468252 | 3 | 1.00 | 8.333333333 | 3 |
| random | 3 | 1000 | 0.8385016592 | 0.1918653527 | 0.6176340484 | 0.9639500383 | 3 | 1.00 | 8 | 3 |
| lhs | 3 | 1000 | 0.6287649435 | 0.02158200467 | 0.6045988451 | 0.6461189216 | 3 | 1.00 | 14 | 8 |

判定口径(可被 Python 辅助层复算):

- 基线分数 `baseline_score = -0.2905708161`(同一次评估口径;高于它才算赢,严格大于)。
- `best_std` 是**样本标准差 (ddof = 1)**;跨 seed 统计,同一 seed 重复出现只计一次。
- `evals_to_beat` 是首次超过基线的评估序号,**只对赢过基线的 seed 取 mean / median**;`-1` 表示一个 seed 都没赢过。
- **单 seed 结论无效**:本表所有数字都是跨 seed 统计,报告不从中挑最好看的 seed。

### 3.2 泛化 (robustness probe)

变体 (6 个): b_ref=0.8T, b_ref=1.2T, z_cell=0.60m, z_cell=1.00m, r_plasma=0.12m, r_plasma=0.18m

每个变体都把**人工基线重新求解**为那个变体的零点(`baseline.TextbookMirror` 在扰动后的 spec 下重解 cell current,
再用该变体自己的 cost_ref 打分),所以 Δ > 0 的含义是"仍然优于**为新需求重新设计的人**",不是"优于原基线"。

| design | mean Δ vs 重新求解的基线 | worst Δ | 赢的变体数 | 变体总数 |
|---|---|---|---|---|
| evolution | +1.600480444 | +1.430220887 | 6 | 6 |
| evolution_warm | +1.643816369 | +1.47618518 | 6 | 6 |
| human_baseline | -0.03983813156 | -0.4655899861 | 2 | 6 |
| lhs | +0.9120982582 | +0.7601163111 | 6 | 6 |
| random | +1.219125262 | +1.054588891 | 6 | 6 |

变体零点(重新求解的人工基线 score):

| variant | baseline score |
|---|---|
| b_ref=0.8T | -0.290570816 |
| b_ref=1.2T | -0.2905708162 |
| r_plasma=0.12m | -0.2976074351 |
| r_plasma=0.18m | -0.2870047417 |
| z_cell=0.60m | -0.1262351518 |
| z_cell=1.00m | -0.3989624245 |

逐 design / 逐变体的 Δ 全表在 `results.json` 的 `robustness.per_design`。
**原始设计向量没有变**:泛化探针只重打分,不重新搜索;一个设计在被扰动后的盒子里可能已经越界。

### 3.3 关于 grid search 的替代说明

本批**没有真正的 grid search**:设计向量 D = 12,即使每轴只取 5 个点也是 5^12 ≈ 2.4e8 次评估,超出任何等预算比较。
用 `lhs`(latin-hypercube,分层空间填充)作为诚实的替代品 —— 这是替代,不是等价,不做掩饰。

## 4. 设计谱系

谱系是一条**树**,不是列表:每条记录带 `design_id` / `parent_design` / `generation`,因此"哪个分支贡献了最多改进"是可答的。

根节点: **D0001 = human_baseline**(人工基线,design D0001)。

| algorithm | seed | best design_id | best score |
|---|---|---|---|
| random | 0 | D0241 | 0.9339208909 |
| random | 1 | D1306 | 0.6176340484 |
| random | 2 | D2446 | 0.9639500383 |
| lhs | 0 | D3276 | 0.6355770638 |
| lhs | 1 | D4394 | 0.6461189216 |
| lhs | 2 | D5379 | 0.6045988451 |
| evolution | 0 | D6920 | 1.296315498 |
| evolution | 1 | D7946 | 1.321468252 |
| evolution | 2 | D8910 | 1.291972898 |
| evolution_warm | 0 | D9857 | 1.381418559 |
| evolution_warm | 1 | D10882 | 1.265627141 |
| evolution_warm | 2 | D11984 | 1.274828839 |

**本报告 v0.1 没有逐分支的增益表**:`experiment.Report` 的字段里没有 lineage 表(冻结 schema),完整父链与 `registry.BranchImprovement()` 的 branch gain 在 `registry.jsonl` 那一侧,不在这个文件里。
上表只列出每条 run 的 best design_id,可以据此在 registry 里沿 `parent_design` 回溯。这是缺口,已在 §6 记录。

## 5. 知识库 rules

| rule_id | parameter | term | rho (worst-case) | sign_agreement | decile_low | decile_high | n_designs | n_runs |
|---|---|---|---|---|---|---|---|---|
| R001 | I_0 | cost | 0.381 | 1.00 | 0.3177951551 | 2.953575394 | 11071 | 12 |

陈述:

- **R001**: 参数 I_0 越大,term cost 越大:参数最低十分位时 term 中位数 0.317795,最高十分位 2.95358(Spearman ρ=0.381 为各 run 中最差绝对值,同号 run 占比 100%,11071 designs / 12 runs)。

完整表格、scope 与机器可读 ```json 块: `knowledge/design_rules.md`

## 6. 诚实边界

这一节的作用是让读者知道,上面的数字**在什么范围内**才算数。只报赢的部分不是报告。

### 6.1 v0.1 没有建模的东西

- **没有 plasma**:没有压力、没有抗磁性响应、没有平衡、没有有限 beta、没有杂质辐射、没有中子 —— 场模型是真空静磁 (`config.Spec` 的 fidelity 声明)。
- **线圈是理想圆环电流丝**:没有导体截面、绕组包细节、匝数分布、电缆与接头、支撑结构、冷屏、热辐射。
- `B_coil_max` = 其它线圈在该线圈位置处的场 + `spec.SelfField()`,后者是无限大电流板近似 `mu0*j*t/2`,不是 winding-pack / FEM 结果。
- `cost_proxy = Σ I²r` 是**欧姆代价代理**,不是成本:不含 HTS 带材价格、低温制冷功率、电源、结构重量、装配与失超保护。
- 没有热分析、没有 quench 分析、没有应力分析、没有匝间绝缘与工程可制造性约束(可绕制半径、支撑几何、公差)。
- 没有 plasma 与线圈的耦合:设计"对 plasma 更好"只能通过真空场指标(mirror_ratio / volume_good / ripple)间接表达。
- 没有真正的 grid search(见 §3.3):D = 12 时穷举不可行,用 `lhs` 替代,替代关系如实写出。
- 预算 1000 次评估/run 是经验值,不是收敛性证明;搜索盒(`bounds`)之外的更优设计既没被找到,也没被排除。
- **没有学习型策略**:`internal/rlenv` 里未实现的策略必须显式报错(合同 §6 的 G10 门),所以本报告不含任何 RL 结果。

### 6.2 什么会推翻本结论

- 泛化探针里 worst Δ < 0(见 §3.2):只在原盒子里赢的设计没有泛化,原结论随之作废。
- 领先幅度小于跨 seed 的 `best_std`(见 §3.1):差异落在搜索噪声内,不能当成结论。
- 领先**只来自 cost 项**而 field / mirror / volume 没有改善:那是"买到了更便宜的磁体",不是更好的设计 —— §2 的逐项表就是为了一眼看穿这种情形。
- 人工基线在解码后被搜索盒裁剪:比较的就不是同一个设计(由 `TestBaselineInsideSearchBox` 专门守门)。
- 解析解与独立实现的离散 Biot-Savart 求和不一致,或 Python oracle(scipy)与 Go 的相对差 > 1e-9 / score > 1e-6(合同 §6 的 G5):数值栈不可信,所有 field 数字作废。
- 同 seed 两次运行 `best_score` 不一致(合同 §6 的 G7):随机性叙述是假的。
- registry 完整性检查(`registry --check`)或 schema parity(`python/aux/schema_check.py`,合同 §6 的 G8/G9)失败:本报告的 runs / aggregate 无法被独立复算,结论退化为自述。

### 6.3 哪些数字是实测、哪些是假设

**实测**(由 solver / evaluator 算出,可从 `registry.jsonl` 逐条复算):

- score、terms、weighted、penalties、metrics(每个被评估的设计都逐条落库);
- `runs[].history` 与 `evals_to_beat` 是搜索过程的直接记录;
- §3.2 的 Δ 是对**给定设计向量**(不重新搜索)重新评估得到;
- `best_of_budget` / `mean` / `std` / `min` / `max` 只是上面前两类的算术。

本批实测的机器最优 score = 1.381418559(design D9857, evolution_warm seed 0)。

**假设**(不是测出来的,换掉会换赢家):

- 权重 `w_field / w_mirror / w_volume / w_ripple / w_cost / w_penalty` 是工程判断,不是物理;
- `b_ref` / `mirror_ref` / `confine_factor` 与采样窗口(`n_axis` / `n_vol_r` / `n_vol_z` / `z_mid` / `z_cell` / `z_axis_max`)是评估口径;
- `coil_field_limit` / `j_eng` / `t_pack` 是设计裕度假设,直接决定哪些设计被判 infeasible;
- `bounds`(搜索盒)本身是假设;`cost_ref` = 人工基线欧姆代价是使 cost 项读数为 1.0 的归一化约定;
- 泛化探针里的"重新求解的人工基线"是同一比例几何下重解 cell current 的 `baseline.TextbookMirror`,不是重新做一轮人类设计;
- 统计口径:方差用样本定义(ddof = 1);Spearman 按 (algorithm, seed) 每个 run 单独计算、并列名次取平均秩;规则表的 `sign_agreement` 因为"同号才保留"的硬过滤恒为 1.0,它是复制证据而不是质量分数;
- `RuleExpectation` 目前**没有计入 term 在目标函数里的符号**(ripple / cost 为负权重),因此它对这两个 term 的方向先验是有偏的;Phase 1 修复。

### 6.4 覆盖范围

- 本报告只覆盖 tag = `phase0`、seeds = 0, 1, 2、methods = random, lhs, evolution, evolution_warm 的组合;没跑的 seed 与方法不做任何声明。
- 所有方法级数字都是跨 seed 统计;**单 seed 结论在本项目里无效**。
- `rl_env_reference` 为空:本批没有把 run 登记为 RL 环境参考轨迹。

## 7. 下一步 (Phase 1)

已经**在接口里就位**的钩子(不需要改冻结签名就能接上):

- `registry` 的 `parent_design` / `BranchImprovement()`:改进谱系已经是数据,不再需要重跑;
- `runner.Scorer` 接口:新算法只要接受一个 scorer 就能进等预算对比;
- `search.Options.WarmStart`:知识复用的最小形式(拿一个设计起跑)已经可测 —— 本批的 `evolution_warm` 就是它;
- `knowledge.RuleExpectation`:规则 → 提议先验的线性映射已经有了可调用形式;
- `internal/rlenv`:RL 环境的缝合点,且**未实现的策略必须显式报错**(不许返回编造数值)。

具体下一步:

1. 把 `RuleExpectation` 接进提案层(需要 `search` 暴露一个规则先验钩子,`Options.WarmStart` 只能带一个设计向量,带不了规则集合);
2. 做"规则先验 ±"的 ablation:先验必须能被关掉,否则它的贡献不可测;
3. 把 branch gain 表并入报告(需要主线决定:加 `Report` 字段,还是单独出一张表);
4. 修 `RuleExpectation` 的 term 符号问题(见 §6.3),并让复现阈值可参数化(`MineOpts` 增字段属冻结签名变更,由主线决定);
5. 增加第二个物理模型(winding-pack / 有限 beta)作为**模型不确定性**的对照 —— 只在一个模型里赢,说明不了工程上赢。

