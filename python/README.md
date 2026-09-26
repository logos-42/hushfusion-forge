# python/ — 辅助校验层 (阶段 G)

Python **不是** 引擎的一部分；它是引擎的独立检查。这里不 import Go，也不调用
`forge` 二进制。一切由 `testdata/golden_spec.json` 驱动，它是设备参数的唯一真实
来源，因此两种语言不可能悄悄漂移开。

| 文件 | 作用 |
|---|---|
| `aux/oracle.py` | 物理 + 目标函数的独立 numpy/scipy 重新实现（门 G5） |
| `aux/schema_check.py` | registry JSONL 的 schema 一致性（门 G9） |
| `aux/rules_check.py` | 从 registry 重新推导挖出的 Spearman 规则并做对齐核对 |
| `aux/analyze.py` | best-so-far / 轴向剖面图 + 等预算 markdown 表 |
| `tests/` | 上述一切的 pytest 套件（包含反面用例） |

## 运行

```bash
# gate G5: recompute the golden numbers and compare
python3 python/aux/oracle.py --check-golden testdata/

# compare a Go field export with the scipy oracle, point by point.
# `forge xcheck` writes runs/scratch/field_samples.json in exactly this shape.
python3 python/aux/oracle.py --compare-go runs/scratch/field_samples.json
# ... and like-for-like against Go's 5 mm near-wire clamp (see below)
python3 python/aux/oracle.py --compare-go runs/scratch/field_samples.json --proximity-floor 0.005

# regenerate golden files into a scratch dir for human review (refuses testdata/)
python3 python/aux/oracle.py --emit-golden /tmp/golden_review

# gate G9
python3 python/aux/schema_check.py runs/phase0/registry.jsonl

# rule reconciliation (needs what stage E writes); --selftest exercises the gate itself
python3 python/aux/rules_check.py --rules knowledge/design_rules.md \
                                 --registry runs/phase0/registry.jsonl
python3 python/aux/rules_check.py --selftest

# figures + table for one run
python3 python/aux/analyze.py runs/phase0

# tests
python3 -m pytest python/tests -q
```

`--compare-go` 既接受一个裸的 JSON 数组（cases），也接受一个 dict，其键为
`samples`/`points`/`records`/`cases` 之一并持有该数组；每个 case 需要一个 12 项的
`design`、样本点（`points_r`/`r` 和 `points_z`/`z`）以及 Go 侧的场值
（`br`、`bz` 和/或 `b_mag`）。`golden_field_samples.json` 是合法输入，因此同一条
路径可以不依赖 Go 就被测试。

## 实现 oracle 时发现的契约缺陷

1. **`internal/physics/api.go` 打印的 `B_r` 闭式解没有 `1/r` 因子。** 文档注释写的是
   `B_r = C*z/(2*alpha2*beta) * [...]`，但正确的表达式是
   `C*z/(2*alpha2*beta*r) * [...]`。冻结的 golden 样本和直接做 Biot-Savart 求积
   都要求这个 `1/r`：在 `golden_field_samples.json` 的近喉部探针点上，文档里的
   形式给出 `-1.2185 T`，而 golden 文件给出 `-4.0457 T`（恰好是正确的值）。
   `oracle.py` 实现的是物理形式（golden 胜出）；一个照字面实现注释的 Go 实现者
   会在每一个离轴样本上挂掉 G5。
2. **`B_coil_max` 定义不完整。** 注释说的是「在该线圈位置上、来自所有 *其它* 线圈的
   场 + `spec.SelfField()`」，读起来像 `|sum_j B_j| + self`（对基线给出 3.3346 T），
   但 golden 值 3.4163 T 是 `sum_j |B_j| + self`（验证到 ~4e-13）。oracle 采用
   golden 的语义。
3. **逐运行 rho 的聚合方式定义不完整 —— 实测答案是：最坏情况。**
   知识层文档没有说明上报的 `rho` 是逐运行系数的均值、中位数还是最坏情况。
   `rules_check.py` 三种都算，并要求已公布的值与其中最接近的一个在 0.02 以内吻合。
   对一套真实的挖掘规则（`runs/scratch/rules.md` 及其 registry）运行，五个规则上
   上报值都与 **最坏情况 |rho|** 吻合到 <=1.1e-16，而均值相差 0.026-0.16 —— 因此
   Go 上报的是最坏情况系数（与「最坏情况 |rho| 至少为 MinAbsRho」一致）。工具仍然
   接受三者中的任意一个，并打印匹配上的是哪一个，这样这道门始终是一道门，而不是
   某一个实现换了种说法的复述。
4. **5 mm 近导线夹取的应用范围不同。** api.go 在 `B_coil_max` 里把这个下限描述成
   线圈与线圈的奇异情形（「如果另一个线圈的距离小于 5e-3 m」）。
   `internal/physics/magnet.go` 则把它应用到 *每一个* 到最近导线的平方距离
   (`alpha2`) 低于该下限的样本点。在 golden 探针集上这是看不见的（没有任何点
   进入导线 5 mm 以内），但 `forge xcheck` 的扰动设计把一个探针放在离导线
   4.896 mm 处，于是两种定义相差 4.3%（Go 67.85 T vs 精确 70.78 T）。oracle 的
   默认值是精确闭式解；`--proximity-floor 0.005` 复现 Go 的夹取，用于同口径
   交叉校验，而 `compare_go` 总会报告有多少点落在该下限内，因此这个模式无法
   掩盖差异。

## 实测的跨语言一致性

`forge xcheck`（3 个设计 x 32 个点，96 个样本：golden 教科书镜像、一个
r+5%/z-5%/I+8% 的扰动设计，以及一个盒子中心设计）：

```
exact closed form           max relative 5.4e-02  (1 point inside the 5 mm wire floor)
--proximity-floor 0.005     max relative 2.2e-12   <-- like-for-like, PASS
```

也就是说，Go 的解析求解器与这个 scipy oracle 在所有「奇异分支未被刻意夹取」的
地方一致到 ~1e-12，唯一的分歧就是上面记录在案的近导线定义。

对一份真实的 benchmark 报告（`runs/scratch/bench/results.json`），`analyze.py`
会从 `runs[]` 重新推导 aggregate 块（一致到相对 1e-9），并从零重新给记录在案的
基线和最优设计打分：

```
[OK] oracle re-score of the baseline: report=-0.29057081610977475 oracle=-0.290570816109776   abs_diff=1.2e-15
[OK] oracle re-score of the best:     report=+1.1679212601196869  oracle=+1.1679212601196913  abs_diff=4.4e-15
```

即在那个（预算 60、seeds [0,1]）运行上，一个独立实现同时确认了人工基线分数和
+1.4585 的机器优势。

## golden 文件无法钉死的定义（在此处选定，并诚实记录）

* `ripple`：内部极值由严格的三点比较找出；连续交替的 (max, min) 对只有在深度超过
  `prominence * B_mid`（prominence = 0.05）时才贡献 `|peak - valley|`。基线对这个
  选择是敏感的 —— 它在单元内的轴向剖面只比其旁瓣低约 0.030 T，这也是 golden 的
  `ripple` 是 0.0 而不是某个更大值的原因。`> 0.05` 这个界限实现为严格不等式，
  因此一个 0.030 T 的结构会被丢掉。
* `coil_proximity_floor_hit`：golden 里只出现过 `0.0`（基线从未靠近过）。当两个
  线圈中心距离小于 5e-3 m 时 oracle 会把它置位，并在这个位移到该间距的点上求
  那个奇异场。
* `min_coil_gap_m` 是三维中心距 `hypot(dr, dz)`（已验证：基线为 0.5 m）。
