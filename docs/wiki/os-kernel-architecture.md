---
title: HUSHFUSION OS 宏内核架构
source: session
created: 2026-10-20
last_confirmed: 2026-10-20
audience: self
stage: current
status: current
schema_version: 2.1
confidence: high
tags: [os, kernel, architecture, macro-kernel]
---

# HUSHFUSION OS 宏内核架构

leo 2026-10-20 定的方向：**把 OS 从「散落的 Markdown 文档系统」升级为 Linux 式宏内核架构**——不是不要文档，而是让文档成为一个**内核**的持久化块设备，所有访问经**系统调用**，各决策域是内核里的子系统驱动，铁律是系统调用边界的**写前不变量守卫**。

## Linux 内核 → HUSHFUSION OS kernel 映射

| Linux 内核 | HUSHFUSION OS kernel（`internal/oskernel`） |
|---|---|
| `syscall(2)` 单一入口 | `Kernel.Syscall(name, args)` 单一入口 + `syscallTable` 分发表 |
| 子系统（VFS / net / sched / block） | drivers：`tasks` / `experiments` / `opportunities` / `decisions` / `receipts` |
| 块设备 / VFS | blockdev：`os/*.md`（git 背书，`oscheck` 仍是权威门） |
| 能力 / 不变量检查 | invariant：写前守卫（`active≤1` / `P=floor(I×E×R/C)` / ID 不重复 / `E1` 不跳态） |
| `fork`/多进程 | phase-1 单进程单内核；多内核/并发在 headless 注入后（T-08） |

## 系统调用面（phase-1）

读（走 `internal/osview` 同一把尺子）：
`tasks.list` `tasks.active` `experiments.list` `opportunities.list` `decisions.list` `receipts.list` `dashboard` `invariants.check` `version`

写（写前守卫，保 `oscheck` 绿）：
`tasks.propose`（有序插入 + ID 不重复 + 评分合法） `opportunities.add` `decisions.add`（自动分配 D-NN） `experiments.new` `experiments.advance`（守 E1 不跳态）

`forge os` 的 CLI 与 `forge os tui` 都只是**系统调用的两个客户端**——不再有"哪个脚本能改哪个文件"的散装问题，所有写都过同一个内核入口与同一套守卫。

## 诚实边界（phase-1）

- **持久化仍是 Markdown**：内核把它当块设备读写；git 背书不变，权威日门仍是 `forge os check`（`oscheck`，还覆盖 K1/K2 kernel 回执与 A1 验收孤儿，且自身经变异测试）。内核的 `invariants.check` 是进程内快速审计，不替代 oscheck。
- **状态机铁律不变**：`active→done` 仍只由 `ossubmit --settle` 执行；内核写系统调用只做 propose/add/new/advance，不碰 active/done。
- **不是新平台**：没有新建数据库/服务；内核是仓库内的一个 Go 包，遵守既有 22 门与减法原则。

## 迁移路径

- **phase-1（本次）**：`internal/oskernel` 内核核心 + syscall 分发 + 子系统驱动 + 写前守卫；`forge os` / `tui` 经内核。
- **phase-2**：内核状态可换结构化存储（块设备驱动可插拔），保持 syscall 面不变。
- **phase-3（AI 粘合）**：AI 经 syscall 操作 OS（读仪表盘、提任务、记决策），内核是 AI 与决策层之间唯一的受控界面——这正是"AI 接入后 OS 使用 kernel 控制执行"的落地。

## 相关

- [os-operations.md](./os-operations.md)（决策层操作与 CLI/TUI）
- `internal/oskernel/`（内核实现）、`internal/osview/`（只读视图层）
- 决策 D-11（本架构转向）、T-08（headless 注入 kernel）
