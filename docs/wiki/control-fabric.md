---
title: 控制织物（Control Fabric）
source: session
created: 2026-10-21
last_confirmed: 2026-10-21
audience: self
stage: current
status: current
schema_version: 2.1
confidence: high
tags: [architecture, contracts, safety, control, stage-0]
---

# 控制织物（Control Fabric）

2026-10-21 阶段 0 切片：**在不动物理、不动 G19 门、不接硬件的前提下**，把"装置—安全—控制—
证据"这条链的结构先立起来。

权威文档：`docs/architecture-v1.md`（架构契约 v1）、
`docs/contracts/{design,experiment,device,control-command,evidence}.md`（五份契约）。

## 本轮落地了什么

| 组件 | 位置 | 一句话 |
|---|---|---|
| 契约层 | `internal/contracts/` | 6 个 Go 类型 + 6 份 JSON Schema + 严格子集校验器 |
| 安全内核 | `internal/safety/` | 确定性、fail-closed 的 11 条判据（R01–R11） |
| 仿真装置 | `internal/plant/` | 确定性状态机 + 占位传感器/执行器 |
| 控制闭环 | `internal/control/` | 提议 → 判决 → 作用 → 留痕 |

## 三条不可违反的纪律

1. **AI 只提议**。Proposer 接口只返回命令意图，**没有**批准字段。
2. **未经判决不进装置**。只有 `allow` 才 `apply`；`deny` 一律留在证据里。
3. **AI 掉线 ⇒ 装置安全停机**。提议失败时闭环自合成 `safe_stop`（走 R11 逃逸路径），
   断网时装置进入 `safe_stop`，不是保持上一条动作。

## 契约版本

`contracts.ContractVersion = "0.1"`，**独立于** `config.ForgeVersion`。
前者变 = 交换格式变了；后者变 = 同一个设计得了不同分数。两者混为一谈会让"证据可复现"
这句话失去意义。

## 判决语义（三态，不是二态）

`DesignVerdict` 的门结论是 `pass` / `fail` / `unknown`：

- `unknown` **永远不等于** `pass`；
- 任何决定性门 `fail` ⟹ 总判决 `rejected`，不允许被总分掩盖；
- 决定性门全 `pass` ⟹ `accepted`；
- 有 `unknown` 的决定性门 ⟹ `inconclusive`。

这条规则的动机是本仓 0.1.0 的教训：最优解 D9857 把导体贴到距打分点 4.196 mm 处，
因为目标函数里没有"导体净空"这一项——**一个没有净空项的软评分能赢，赢在真实装置不可能
有的位形上**。判决层不允许"部分信息缺失"被悄悄当成"通过"。

## 诚实边界（本轮**没有**做到的事）

1. **没有硬件**。无硬件适配器、无联锁、无校准、无驱动错误映射。阶段 7 之前禁止接真设备。
2. `internal/plant` 是**占位模型**，读数不可用于任何物理结论。
3. `DesignVerdict` 的漏斗**尚未接进** `internal/design` 的六道门；`Judge` 目前是独立可测组件。
4. 没有多 Agent 编排、没有 RL、没有模块化打包。
5. Schema 校验器只支持一小撮关键字，**遇到未知关键字直接报错**（不静默放过）。
6. 轨迹只在内存中生成并可指纹化，尚未落盘到 `runs/` 或独立证据目录。

## 后续入口

- 阶段 1：拆薄 CLI / 应用层
- 阶段 2：统一设计判决漏斗（把 `internal/design` 六门接进 `DesignCandidate`→`DesignVerdict`）
- 阶段 3：仿真装置从占位模型升级为场景库
- 阶段 7：真实硬件适配（**前置条件：硬件安全策略、联锁、校准流程**）
