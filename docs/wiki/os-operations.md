---
title: HUSHFUSION OS —— 决策层
source: session
created: 2026-10-17
last_confirmed: 2026-10-17
audience: self
stage: current
status: current
schema_version: 2.1
confidence: high
tags: [os, decision-layer, task-pool]
---

# HUSHFUSION OS —— 决策层

leo 2026-10-17 定的工作原则：**把自己设计成系统的设计者，不是项目的发动机。**
本仓 22 道门 + `runs/` 证据落盘已经证明「门禁 + 可复现证据」有效；
缺的是把科学、工程、合作、资源连成一圈的那层——任务池、证据状态机、机会池、决策日志、周复盘。

OS 是**决策层，不是新平台**：Markdown + JSON + 脚本 + Git，挂在既有仓库里，不新建基础设施。
证据的唯一权威仍在 `runs/<tag>/` 与 `docs/wiki/current-status.md`；本层只做索引、映射与决策记录。

**执行由 Agent Kernel 主导**：OS 提交任务（channel.submit）→ Bolloon kernel 串行执行
（channel / lifecycle / transport / model.acquire，经 bridge 跨语言接入，G24 七条真跑）→ 产出证据进 runs/。
`os/` 是 kernel 的工作记忆与决策面，不替代 kernel 执行。
**headless 预留**：headless（SSM+OML 持续学习，`~/Downloads/headless`）未来注入 kernel 作为核心——
预留槽位 = `model.acquire` 租约（headless 实现 `openConnection`）；本版只留接口不实现（T-08）。

## 结构

```
os/
├── NORTH_STAR.md      # 目标与边界（防漂移）
├── CURRENT_STATE.md   # 五问：状态/下一步/证明/自动化/变好
├── TASKS.md           # 任务池，P = I×E×R/C 排序；active ≤ 1 件
├── EXPERIMENTS.md     # 五态证据状态机（问题→方案→复现→独立反馈→决策）
├── OPPORTUNITIES.md   # 机会池（带匹配理由与下一步；对外发送前 leo 确认）
├── DECISIONS.md       # 决策日志（D-01…D-07，每行带依据证据）
├── WEEKLY_REVIEW.md   # 每周六四问复盘
├── acceptance/        # 可重复执行的验收标准（每个 active 任务一份）
├── tools/             # daily_check.sh / weekly_review.sh（复用既有门）
└── SPEC.md            # v0.1 实施规格（接入边界/数据结构/状态转换/自动化/7天交付/人机分工）
```

## 状态转换铁律（与门禁同一哲学）

- 任务 `proposed → active` 必须先写验收标准；`active → done` 逐条勾验并附证据路径；
  `blocked` 必须写阻塞原因。
- **Agent 说完成 ≠ 完成**：每个状态转换至少一条检查不依赖被测对象自己（对字节 + 对语义双通道）——
  与 G18 抓「trace 标错版本自己跟自己比」、G19「能被命令行调绿的门不是门」同一套纪律。
- Agent 可以建议「哪个任务 P 高」「哪条证据链完整」；**不可以**说「理论成立」——
  科学判断只属于 leo 或独立的外部反馈（公开数据、独立 oracle、上游锚点、多 seed）。

## 当前主任务

T-01：G19 世界语义重做（B 项）——把 v2 世界动力学接到 `internal/design` L3 判决。
验收标准：`os/acceptance/t01-g19.md`（三条子门 + 死法 + 不许项；预注册于 `docs/version-0.1.2.md` §2）。

## 相关

- `os/SPEC.md`（v0.1 实施规格）
- `docs/wiki/current-status.md`（阶段现状权威）
- 所有权名册：`internal/owners/owners.go` wiki stage（`os/` 归 parent，2026-10-17 合同变更）