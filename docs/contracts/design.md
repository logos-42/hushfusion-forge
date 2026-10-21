# 契约：设计（Design Contract v0.1）

> 契约版本 `contracts.ContractVersion = "0.1"`。
> Go 实现：`internal/contracts/api.go`；Schema：`internal/contracts/schema/design-*.schema.json`。
> 本文是**交换格式**的冻结面，不是判决逻辑的说明书。判决逻辑在 `internal/design`。

## 1. 两个对象，两件事

| 对象 | 回答的问题 | 谁产出 |
|---|---|---|
| `DesignCandidate` | **谁提出了什么参数？** | AI / 人 / 基线 / 搜索 |
| `DesignVerdict` | **这份设计在每道门下是什么结论？** | 判决层（判） |

把两者分开，是为了让"谁提出的"与"谁判的"在数据上就是两件事——一份判决里同时带着
候选与结论，就无法在事后区分"AI 提的"与"判出来的"。

## 2. DesignCandidate

```json
{
  "contract_version": "0.1",
  "design_id": "D9857",
  "parent_design_id": "D9856",
  "origin": "ai",
  "parameters": {"r_1": 0.10, "z_1": 0.04, "i_1": 1780000.0},
  "units": {"r_1": "m", "z_1": "m", "i_1": "A"},
  "created_at": "2026-10-21T00:00:00Z"
}
```

规则：

1. `origin ∈ {ai, human, baseline, search}`。
2. `parameters` 至少一项；**单位缺项表示"未知"，不是"默认"**。缺单位的参数不得进入
   可造性判决。
3. `parameters` 只装**数值**。离散选择（材料类、拓扑）走 `internal/config` 的源表，
   不塞进浮点字典——否则会出现"字符串塞进浮点字典"这种迟早会发生的静默损坏。
4. `parent_design_id` 构成谱系；为空表示根。

## 3. GateResult

```json
{
  "key": "mu_window",
  "status": "pass",
  "value": 1.479,
  "threshold": 1.0,
  "unit": "-",
  "detail": "χ_μ=1.479 > 1",
  "evidence": [{"kind": "gate", "uri": "gate:mu_window", "sha256": "…"}]
}
```

每道门必须能独立回答三件事：**判据是什么**（key/threshold/unit）、**结论是什么**
（status）、**证据在哪**（evidence）。缺任何一件，这道门就不是门。

`status` 是三态：

| 值 | 含义 | 计入 pass？ |
|---|---|---|
| `pass` | 通过 | 是 |
| `fail` | 不通过 | 否 |
| `unknown` | 无法判决（缺输入/未证/不可判） | **否，且不得当作 pass** |

## 4. DesignVerdict 与四条硬规则

```json
{
  "contract_version": "0.1",
  "design_id": "D9857",
  "overall": "rejected",
  "gates": [ … ],
  "decisive_keys": ["field_min", "death", "mu_window", "coil_load", "buildable"],
  "evidence": [ … ]
}
```

`overall ∈ {accepted, rejected, inconclusive}`。规则（由 `contracts.Judge` 实现，
并被单测钉住）：

1. `unknown` **永远不等于** `pass`；
2. 任何一道**决定性门** `fail` ⟹ 总判决 `rejected`，不允许被总分掩盖；
3. 决定性门全部 `pass` ⟹ `accepted`；
4. 没有决定性门，或存在 `unknown` 的决定性门 ⟹ `inconclusive`。

`decisive_keys` 是"计入总判决"的门集合；不在其中的门只报值、不判生死
（`internal/design` 的 `steps` 门就是这种）。

**为什么禁止用一个总分掩盖硬门失败**：本仓已经吃过这个亏——0.1.0 的最优解把线圈贴到
距打分点 4.196 mm 处，因为目标函数里没有"导体净空"这一项，于是"机器赢"赢在了一个
真实装置不可能有的位形上（见 `docs/wiki/current-status.md` 的"诚实边界"）。

## 5. 与现有判决层的对应关系（尚未接线）

| 现有门（`internal/design`） | key | 是否决定性 |
|---|---|---|
| 场强下限 | `field_min` | 是 |
| 死亡场 | `death` | 是 |
| μ 窗口 | `mu_window` | 是 |
| 线圈负载 | `coil_load` | 是 |
| 可造性 | `buildable` | 是 |
| 步数预算 | `steps` | **否**（报值） |
| FC5 锁定 | `fc5_locked` | **否**（恒 `unknown`） |

本轮只提供了 `DesignCandidate` / `DesignVerdict` / `Judge` 与 schema，**尚未**把
`internal/design` 的六道门接进漏斗——那是阶段 2 的活。

## 6. 诚实边界

- 本契约**不含任何物理**。它定义"一条候选长什么样"，不判断"这个设计好不好"。
- `EvidenceRef` 是**引用**，不是内容。引用带 `sha256` 才有审计价值；无指纹的引用
  在审计时等于没有引用。
