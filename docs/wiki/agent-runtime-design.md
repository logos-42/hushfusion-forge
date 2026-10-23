---
title: 连续对话执行 Agent Runtime 设计（借鉴 Hermes Agent Loop + Skills）
source: session
created: 2026-10-23
last_confirmed: 2026-10-23
audience: self
stage: crystallized
---

# 连续对话执行 Agent Runtime 设计

> 状态：crystallized（2026-10-23）｜ 实现：`internal/agent/` + `internal/ai` transport 扩展
> 入口：`forge agent <chat|serve>` ｜ 前置：`internal/ai`（provider 层，2026-10-18）
> 本页回答：连续对话怎么保持、工具怎么执行、skills 怎么用、诚实边界在哪。

## 0. 一句话

把 Hermes 的 **agent loop**（消息 → 模型 → tool_calls → 执行 → 结果回填 → 模型 →
最终回复）移植成 Go：一个 **Session** 持有连续对话历史，每回合模型可输出 `tool_calls`，
runtime 经 **ToolRegistry** 执行并把结果回填为 tool 消息，直到模型产出最终回复；
**Skills** 是按需加载的上下文——`SKILL.md`（frontmatter + 正文）目录经触发匹配注入对话，
模型需要时也可显式 `skill_view` 拉取。

**执行纪律（本仓内核主导）**：runtime 的写动作一律**经 kernel 通道**（`oskernel.Syscall`
受控写、`internal/kernel` channel 提交任务串行执行、证据落盘），**经 harness 闭环**
（`harness.run/self/report`：跑实验 → 落盘 → 自我调整）——AI 只提议，不直接动文件/命令，
不自称完成。模型推理，harness 强制执行边界（学 Claude Code 1.6% / 98.4% 分工）。

TUI（另一项目）通过 `forge agent serve`（stdio JSONL 会话服务器）连上本 runtime 做连续对话。

## 1. 为什么现在做 / 缺口在哪

现有三层各差一块：

| 层 | 现状 | 缺口 |
|:--|:--|:--|
| `internal/ai` | 单轮 chat/completions，多 key 轮换 | **不支持 tools**（模型无法请求执行） |
| `internal/aios` | 单目标自愈循环（JSON 计划 → 内核执行） | 每次 `Run(goal)` 无跨轮记忆；计划是私有 JSON 协议 |
| `internal/lsp` | definition/hover/references 查询 | 只能被 CLI 透传，未接进任何 loop |

runtime 填这三块：ai 层加原生 `tool_calls`（**实测 goaichat 端点支持**，finish_reason=tool_calls），
aios 的"只提议 + fail-closed"纪律升级为通用工具执行，lsp 变成 `code.read/code.search` 工具。

## 2. 架构

```
TUI（另一项目） ──stdio JSONL──► forge agent serve（cmd/forge）
                                  └─ internal/agent（单一 queryLoop，学 Claude Code：
                                     CLI/TUI/SDK 所有入口共用同一个 loop）
                                       ├─ Session       消息栈 + 渐进压缩管道（预算→裁剪→摘要）
                                       ├─ Loop          回合循环（model ↔ tools，上限 MaxToolTurns）
                                       ├─ ToolRegistry  工具注册/参数校验/调度（fail-closed）
                                       └─ Skills        技能发现/匹配/注入（SkillTool 模式）
                                   复用 internal/ai（transport 扩展 tools + tool_calls）
                                      internal/oskernel（受控 syscall，写仍只"提议"）
                                      internal/kernel（channel 提交任务串行执行）
                                      internal/harness（实验闭环：run→落盘→self 调整→report）
                                      internal/lsp（代码上下文）
```

**三个注入点（学 Claude Code）**：`assemble()` —— 模型看到的（skills 注入 + system）；
`model()` —— 模型能触及的（ToolSpec 列表）；`execute()` —— 操作如何运行（权限门控：
写工具白名单 + kernel 通道，执行前拒绝优先）。

组件边界：`internal/agent` 不自己发 HTTP（走 `ai.Chat` 端口注入）、不自己写 OS
（写工具经 `oskernel.Syscall` + `allowedWrites` 纪律）、不自己跑实验（`harness.*`
经注入的 Executor，由 cmd 层装配真实现）、不直接动文件/命令（任务经 `kernel.submit`
投递 channel 串行执行，证据落盘 `os/kernel_receipts/`）。agent 包保持纯净可测，
装配（接线）全在 cmd/forge —— 与仓库"cmd 是接线负责人"同一原则。

## 3. 连续对话：Session

- `Session` 持有消息栈 `[]Message`（role: system / user / assistant / tool），
  **assistant 的 tool_calls 与对应 tool 结果配对存储**，回放时保持 OpenAI 协议的
  assistant→tool 顺序（协议要求 tool 消息必须跟在产生它的 tool_calls 之后）。
- 预算：`MaxBudgetTokens`（默认 12k），估算 = `len(text)/4`（轻版，预留精确 tokenizer 槽）。
  超预算 → 保留 system + 最新 N 条，压缩最旧 assistant/tool 对为一行摘要（预留压缩器槽，
  v0.1 直接丢弃最旧轮次并记 `compacted: true` —— 诚实，不假装无损）。
- 会话生命周期：v0.1 驻留在 `serve` 进程内（TUI 连接期间连续）；进程退出即失忆，
  **预留** JSONL 落盘（`--persist <dir>`，v0.2）。

## 4. 执行：Loop + ToolRegistry

- **原生 tool_calls 协议**（非 JSON 计划退化）：`ChatRequest` 扩展 `Tools []ToolSpec`；
  响应解析 `choices[0].message.tool_calls`（id/type/function.name/function.arguments）。
  实测：goaichat（deepseek-v4.1-flash）对 tools 请求返回 `finish_reason: tool_calls`。
- `ToolSpec` 声明式注册：`Name / Description / Parameters(JSON Schema 子集) / Executor`。
- **fail-closed 三闸**：未知工具 → 拒；参数 JSON 解析失败或 schema 校验失败 → 拒（原因回灌）；
  写工具不在 `allowedWrites` 白名单 → 拒（AI 无自称完成/翻状态能力，沿用 aios 纪律）。
- 回合循环：`loop` 内最多 `MaxToolTurns`（默认 6）次 model 调用；每轮 tool 结果回填后
  继续，直到模型无 tool_calls（最终回复）或预算耗尽（fail-closed 报错，不假装完成）。
- tool 执行失败不回滚、不重试猜测：错误文本原样回灌给模型自愈（学 aios 的 history 回灌）。

## 5. Skills：按需加载的上下文

- **目录合并**：`HUSHFUSION_SKILLS_DIR`（缺省 `repo_root/skills/`）+ `~/.hermes/skills`
  两处，同名后者覆盖（学 hermes skills 分层）。
- `SKILL.md` 格式：frontmatter 必填 `name` / `description`，正文自由；扫描时解析
  frontmatter 建索引（mtime 失效缓存）。
- **匹配（v0.1 关键词）**：把最近一条 user 消息与每个 skill 的 description 做词频交集，
  命中 ≥1 个显著词即候选；候选按命中数排序，取 top-K（默认 3）注入。
  **预留 embedding 槽**：`SkillMatcher` 是接口，v0.1 实现 `KeywordMatcher`，后续可换向量匹配。
- **注入预算**：每个 skill 正文截断到 `SkillInjectionBudget`（默认 800 字符）进 system prompt；
  完整正文经 `skill.view` 工具按需拉取。模型显式请求 = 工具调用，受 ToolRegistry 统一纪律。

## 6. 内置工具 v0.1

| 工具 | 动作 | 白名单/纪律 |
|:--|:--|:--|
| `os.view` | 读 OS dashboard 快照（只读） | 只读，无需白名单 |
| `os.propose` | 提议新任务/机会/决策（写） | `allowedWrites` 五条，经 `oskernel.Syscall` |
| `kernel.submit` | 经 kernel channel 提交任务串行执行（写） | `internal/kernel`，回执落盘 `os/kernel_receipts/<id>.json` |
| `harness.run` | 跑数学模型实验（episodes/budget/seed），落盘 store | 经注入 Executor 调 `internal/harness`；只落盘不擅自改代码 |
| `harness.report` | 读最近一次 harness 摘要（只读） | 只读 |
| `skill.view` | 按名字加载技能正文 | 名字必须命中已索引技能 |
| `code.read` | 读仓库内文件（相对路径） | 限定仓库根内、`.git/` `runs/scratch/` 拒绝 |
| `code.search` | 仓库内正则搜索 | 同上白名单 |

**内核主导的完整闭环（T-08 预备槽的落地形态）**：AI 提议 → `kernel.submit` 提交 →
kernel channel 串行执行 → 回执落盘 → 下轮把回执读回给模型；`harness.run` 只产生
数据与报告（`runs/<tag>/store.jsonl` + report），不产生"完成"判定——科学结论/判定
仍归 leo 或独立反馈（沿用 aios 纪律，AI 结构上无法自称完成）。

预留（v0.2 候选）：`forge.run`（白名单子命令透传）、`mem.recall`（会话记忆检索）。

## 7. CLI 形态

- `forge agent serve`：**stdio JSONL 会话服务器**（复用世界协议的形态）：stdin 每行一个请求
  `{"msg": "...", "session": "id"?}`，stdout 每行一个响应
  `{"session", "role", "content", "tool_calls"?}`。会话按 `session` 字段区分（缺省 = 单会话），
  驻留进程内连续；非 TTY 直通，TUI 经管道连。请求 `{"quit": true}` 优雅退出。
- `forge agent chat --msg "..." [--provider p] [--model m] [--base-url u] [--skills-dir d]`：
  单发（无状态，进程退出即失忆 —— 诚实说明，连续对话走 serve）。
- 密钥纪律沿用 internal/ai：只从环境变量解析，值不进 CLI/日志。

## 8. 诚实边界

1. v0.1 无流式（`stream=false`，transport 遇 true 即报错）、无会话持久化（serve 进程内）。
2. skills 匹配是**关键词近似**不是 embedding；注入是截断不是智能摘要。
3. token 预算是 `len/4` 估算，不是 provider tokenizer。
4. AI 的写动作仍只"提议"（`allowedWrites` + 内核守卫）；科学结论/完成判定归人。
5. 工具执行不保证事务性；失败文本回灌自愈，不自动重试。
6. 并发会话：v0.1 serve 单进程串行处理（一请求一轮），预留并发槽。

## 9. 门与测试

- `internal/agent` 单测（零网络，假 ChatFunc）：loop 收敛 / tool 结果回填 /
  未知工具 fail-closed / 参数校验拒绝 / 预算压缩 / skill 关键词匹配与注入截断。
- `internal/ai` 扩展测试：httptest 假端点断言请求带 `tools`、响应 tool_calls 解析、
  `tool` role 消息原样透传。
- serve 冒烟：echo 两行 JSONL 请求 → 两行响应（含 session 回显）。
- 名册：`internal/agent/` 归 root stage（合同变更，本节为理由）。
