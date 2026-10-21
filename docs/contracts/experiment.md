# 契约：实验（Experiment Contract v0.1）

> 契约版本 `contracts.ContractVersion = "0.1"`。
> Go 实现：`internal/contracts/device.go`（`Experiment` 类型）；Schema：
> `internal/contracts/schema/experiment.schema.json`。
> 与 `os/` 决策层的关系见 §4。

## 1. 对象

```json
{
  "contract_version": "0.1",
  "experiment_id": "T-01-G19",
  "design_id": "D9857",
  "state": "running",
  "hypothesis": "交换子世界里 A 不再恒等于 0",
  "gates": ["world_structure", "world_protocol_v3"],
  "evidence": [
    {"kind": "trace", "uri": "trace:T-01-G19", "sha256": "…", "forge_version": "0.2"}
  ],
  "notes": ["G19 红为设计所许"]
}
```

## 2. 状态机

```text
proposed → running → done
                 ↘ failed
                 ↘ blocked     (缺输入 / 缺权限 / 被安全内核拒绝)
       → abandoned
```

| 状态 | 含义 | 谁能置 |
|---|---|---|
| `proposed` | 已提出，未开始 | AI 可提议 |
| `running` | 进行中 | 内核 |
| `blocked` | 被阻塞 | 内核（记录原因） |
| `done` | 完成 | **只由人 / 内核** |
| `failed` | 失败 | 内核 |
| `abandoned` | 放弃 | 人 |

**"AI 不得自称完成"是结构性的，不是约定性的**：AI 面没有把 `running` 置成 `done` 的
通路（`internal/aios` 的受控 syscall 集里根本没有这个调用）。本契约沿用同一纪律——
`done` 的写入权在 Governance Kernel，不在提议者手里。

## 3. 字段规则

1. `gates` 列出该实验要过的门（key 集合，与 `DesignVerdict.decisive_keys` 同一命名空间）。
2. `evidence` 只放**引用**（`EvidenceRef`），不放内容。
3. `notes` 记录**失败原因**——一次没有失败原因的实验在事后不可复核。

## 4. 与 `os/` 决策层的关系

`os/` 已经是本仓的事实上的实验/任务记录层（Markdown 块设备 + 状态机）。
本契约**不替换** `os/`：

| | `os/` | `contracts.Experiment` |
|---|---|---|
| 介质 | Markdown 块设备（人可读、可 grep） | JSON（跨进程交换、可 schema 校验） |
| 权威性 | 决策层状态的**唯一真源** | 交换格式 |
| 谁写 | 人经 `ossubmit --settle`；内核受控 syscall | 应用层/编排层 |

两者之间需要一次**显式映射**（尚未实现）：`os/` 的任务/实验块 ⟷ `Experiment` 对象。
在映射落地前，本契约只作为"跨层交换的最小实验句柄"，不作为决策层真源。

## 5. 诚实边界

- 本契约不定义"实验是否完成"的判定规则，只定义完成这一**状态**怎么被表示与写入。
- `blocked` 与 `failed` 的区分：`blocked` = 前提不足（可以补条件后重跑），
  `failed` = 前提齐备但结果不通过（补条件也不改变结论）。
