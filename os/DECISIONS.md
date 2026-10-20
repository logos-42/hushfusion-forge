# DECISIONS —— 关键决策与依据

> 防止反复争论已经讨论过的问题。**规则**：新决策先记这里再执行；撤销决策也要记（带撤销原因）。
> 与 `docs/version-*.md` / `CONTRACT.md` §9-§10 裁决表一脉相承——本页是决策日志，那两处是合同正文。
> 创建：2026-10-17 ｜ 最后确认：2026-10-17

## 决策日志（从新到旧）

| ID | 日期 | 决策 | 依据（证据） | 备选（为什么不选） | 后果 | 状态 |
|:--|:--|:--|:--|:--|:--|:--|
| D-08 | 2026-10-17 | **headless 未来注入 kernel 作为核心（模型推理/持续学习本体），本版只留接口不实现**：预留槽位 = kernel `model.acquire` 租约（headless 实现 `openConnection` 端口，经 bridge 暴露给 Go）；语义边界与世界协议一致（只走机制、不掺业务判定） | kernel SDK 已有 ModelRuntime 只读租约机制（K6）；G24 (f)「缺 openConnection ⇒ 内核抛错」是现成的诚实边界；leo 指示「headless 模型以后注入 kernel 作为核心」 | 现在就接线 → 数据/模型耦合未定、最小闭环尚未运转，违背减法 | T-08（proposed）；不动代码、不占门 | proposed |
| D-09 | 2026-10-18 | **双向打通 OS↔kernel（forge os submit/peek/sync）**：决策层经 kernel 机制原语执行并回读核对——submit 投递任务、peek 读回 actor 状态、sync 做「提交→回读→核对 task/marker」的双向闭环，证据落盘 os/evidence/（gitignore）；诚实边界：每调用是独立 bridge 会话（actor 状态进程内）、sleep-ms 是机制演示非真实物理负载 | leo「双向打通 OS↔kernel」；G24 已证明 bridge 机制（FIFO/隔离/fail-closed）可跑，只差 OS 侧 CLI 接线 | 直接给 channel 挂真实物理计算 → 负载未定、先证机制 | `forge os`（cmd/forge + .gitignore）；真跑 sync 核对通过（2026-10-18） | done |
| D-10 | 2026-10-18 | **AI Provider 接入层（internal/ai + forge ai）**：借鉴 Hermes Agent 的 ProviderProfile 设计（声明式 profile + 注册表别名 + OpenAI-compatible 默认 transport）用 Go 标准库实现——「支持更多 AI API」靠协议归一而非多写客户端，一家一份声明即可接入（内置 zai/GLM、deepseek、openai、openrouter、anthropic、custom、local 7 家）；API key 只走环境变量，值不进代码/日志 | leo「阅读 hermes 项目的 AI provider 设计…用 go 语言写」；实测 `~/Downloads/hermes` 的 providers/base.py 证实机制；真调用 zai 经 goaichat 代理返回「通了」 | 抄 Hermes 全量字段（30+ 字段/凭据池轮换/插件目录）→ 违背减法，v0.1 只留用得到的 12 字段 | `internal/ai/`（名册合同变更）+ `forge ai`；v0.1 只实现 chat_completions 非流式，anthropic_messages/SSE/轮换明确不做 | done |
| D-07 | 2026-10-17 | **建立 HUSHFUSION OS 决策层，kernel 主导执行**：`os/` = kernel 的工作记忆与决策面（任务池 + 实验状态机 + 机会池 + 决策日志 + 周复盘），执行一律走 kernel 机制原语；首个 `os/` 全部复用既有门，不新建平台 | 22+ 门已证明「门禁+证据落盘」有效，缺的是任务/机会/决策闭环层；kernel bridge（G24）已是跨语言执行引擎 | 一次性建设「OS 平台/多 Agent 架构」→ 把复杂工作变复杂基础设施，违背 leo 减法原则 | 所有权名册登记 `os/`（合同变更）；T-01 成为唯一主任务 | active |
| D-06 | 2026-10-07 | **分形/发育结构优势收口为否定**：机制可工作、长程持续学习成立，但没有统计优势 | 3 seeds 推翻单 seed 2.2×；toy+真实 5-fold 都不优于固定（0.086 vs 0.072） | 继续赌结构优势 → 违反「多 seed / 诚实评估」纪律 | 结构留作「大网络+收益递减」regime 重启；守护进程 v6 继续跑判据探索 | done |
| D-05 | 2026-10-02 | **版本模型**：MAJOR（契约断裂）/ MINOR（新能力）/ PATCH（改判据）；版本号=消融轴 | 0.1.0 退化解只能靠判据变更解决，跨版本对比是唯一合法消融轴 | 加 `--clearance=false` 开关 → 两侧不归档、不可逐位复现 | G12/G15/G16 加判据版本匹配前置；旧 run 不重跑 | done |
| D-04 | 2026-10-02 | **主路线=引力场 μ(t)**：FRC 降为工程参照/稳定性对照/独立备用；G4 改为引力场主约束路线门 | leo 指示「要的是双环方案产生的引力场，不是 FRC」；η(R_H) 单峰、同半径充要 | 继续 FRC 主线 → 方向与 leo 目标不符 | 双流环引力设计页 + 轴向页加方向声明 | done |
| D-03 | 2026-09-27 | **G19 弱档红为设计所许**：P2 不启动；B 项（世界语义接到 L3）预注册，换层须重新预注册阈值 | 实测：A 全来自 R2 夹取（~3% 全量程）、R4 换源不进目标函数、四档逐位相同 | 调阈值/预算/动作集合变绿 → 能被命令行调绿的门不是门 | G19 红至今；B 项=当前主任务 T-01 | active |
| D-02 | 2026-09-27 | **世界协议 v1 冻结**：JSONL/stdio、最短往返浮点、7 错误码；语义唯一权威是 `internal/rlenv` | 跨语言逐位复现是「结论独立」的硬门槛（G18 三条） | 数值接近即通过 → 逐位复现的仍是响应字节，不是接近 | 协议只搬运，不在这里加 shaping/惩罚 | done |
| D-01 | 2026-09-26 | **可造性进目标函数**（0.1.2）：第 4 罚项 `clearance`（MinClearance=0.05，工程判断非物理） | 机器最优 D9857 把导体放距采样点 4.196mm（钳位半径内）——退化解 | 只改搜索约束 → 治标；罚项同族 max(0,(允许−实际)/允许) 逐位可审计 | Δ +1.672→+1.2395；可行记录 11072→7221 | done |
| D-11 | 2026-10-20 | OS 升级为 Linux 式宏内核（internal/oskernel）：单一内核核心拥有状态与持久化，所有访问经 Kernel.Syscall 单一入口，各决策域是子系统驱动，铁律是写前不变量守卫；phase-1 持久化仍为 os/*.md 块设备（git 背书 + oscheck 权威门），AI 粘合列为 phase-3 | leo 2026-10-20 指示「os 更新成 Linux 宏内核架构，不是文档系统」；oscore/oscheck/ossubmit 已证门禁+证据有效，缺统一内核 syscall 边界与写前守卫 | 一次性重写为运行时平台 → 违背减法 + 22 门回归风险；保留纯文档系统 → 无统一 syscall 边界与不变量守卫 | 新增 internal/oskernel（syscall+驱动+守卫）；forge os 写入全部经内核；CLI/TUI 成为 syscall 客户端；名册+合同变更；TUI 内可录入 | active |
| D-12 | 2026-10-21 | phase-3 AI 粘合落地（internal/aios + forge os ai）：AI 经内核读 syscall 取状态 → 出 JSON 动作计划 → 白名单校验（只 propose/add/new/advance）→ 经内核写 syscall 执行（写前守卫）；AI 结构上无法自称完成（无 active→done syscall），只提议；非 JSON/越权/缺 key 全 fail-closed | leo 指示「phase-3 AI 粘合」；internal/ai 已具 7 家 OpenAI-compatible provider；oskernel 已是唯一受控界面，AI 接入只需在其上加白名单+端口注入 | 让 AI 直接改 os/*.md → 绕过宏内核守卫与 syscall 边界，违背刚建的架构；给 AI 开放 active→done → 违背"done 不许自称"铁律 | 新增 internal/aios（名册+合同变更）；forge os ai 子命令；ChatFunc 端口注入可零网络测试；科学结论/完成判定仍归 leo 或独立反馈 | done |

## 决策模板（新决策照填）

```text
D-0N｜日期：<决策一句话>
依据：<证据路径，禁止「我觉得」>
备选：<被否方案 + 原因>
后果：<对任务池/门/证据的影响>
状态：<active | done | 被 D-0M 撤销（说明）>
```