---
title: Agent Runtime 常见问题
source: session
created: 2026-10-23
last_confirmed: 2026-10-23
audience: self
stage: current
---

# Agent Runtime 常见问题

本页把 Agent Runtime 的常见机制统一到一张表。实现状态以当前代码和测试为准；设计页见 [agent-runtime-design.md](./agent-runtime-design.md)。

## 1. Loop、ReAct runtime 与生命周期

### Q：一次请求如何运行？

A：`internal/agent/loop.go` 的 `Session.Turn` 使用 ReAct 形态：追加 user 消息 → 调模型 → 若有 `tool_calls` 则校验并执行 → 将 assistant/tool 消息成对回填 → 再调模型 → 直到最终回复或达到 `MaxToolTurns`。达到上限返回错误，不编造“已完成”。

### Q：runtime 的生命周期是什么？

A：一次 `Turn` 是最小执行生命周期；`Session` 保存对话状态；`ServeHandler` 按 session ID 管理多个会话；`serve` 进程退出后内存态会消失。工具执行通过注入端口完成，模型 HTTP 不由 agent 包直接发起。

### Q：所谓 React runtime 是 React 前端吗？

A：本仓是 Go runtime，没有 React 前端。这里按 ReAct（reasoning + acting）理解：模型提出动作，runtime 执行动作并回填结果。若以后接 React TUI，TUI 仍只是客户端，不改变 loop 的唯一权威。

### Q：错误如何恢复？

A：工具失败会以明确错误文本回灌模型自愈；未知工具、坏 JSON、越权写入直接拒绝。provider 层对 429、401/403、5xx、网络错误支持 key 轮换/回退和冷却；400 等请求错误不盲目重试。错误恢复不等于成功，最终仍需真实回执或证据。

### Q：超时和取消如何处理？

A：模型调用由 context 取消和 client timeout 控制；`Session.Turn` 每轮检查 `ctx.Err()`。终端执行现在统一经 `internal/sandbox` + `internal/proc`：`kernel.BolloonExecContext` 取消即杀**整个进程组**（含 shell 的孙进程——修复了 `exec.CommandContext` 只杀直接子进程、管道被孙进程持住导致 `Wait` 阻塞到超时的坑）；超时由 `proc.Options.Timeout` 强杀整组（`sandbox.Policy.Timeout` 同效），`KillGrace` 字段为将来分级保留。exec 围栏策略全部由 env 驱动：`BOLLOON_EXEC_ROOT`/`_TIMEOUT`/`_ENV_ALLOW`/`_MAX_CONCURRENT`，默认最小权限（`PATH,HOME,BOLLOON_BIN`）。全局超时/重试/预算调度器仍未统一成一个。

## 2. Session、记忆、历史与上下文

### Q：session 如何区分和重置？

A：stdio JSONL 请求可带 `session`；缺省使用 `default`。`ServeHandler` 维护进程内会话表，有 `MaxSessions` 上限；`reset` 清空指定会话，`quit` 优雅退出。TUI 另有 `os/sessions/<id>.md` 与机器可读 JSON 的历史落盘，二者不要混同。

### Q：历史记录保存什么？

A：Session 历史保存 system 外的 user、assistant、tool 消息；assistant 的 tool calls 与对应 tool 结果必须保持协议顺序。`History()` 供审计和展示。Agent serve 的持久化恢复仍是预留能力，不能把进程内历史说成跨重启记忆。

### Q：智能体记忆和对话历史有什么区别？

A：历史是本轮协议上下文；`internal/agent/memory.go` 的 `MemStore` 是会话内 KV，可跨回合读写、按键排序注入。它不是秘密存储，也不应放 API key、私钥等敏感值。

### Q：记忆能跨进程/跨重启保留吗？

A：能（v0.2）。`NewMemStoreFile(path)` 把记忆落 JSON 文件（tmp+rename 原子替换，权限 0600）；**文件损坏会在构造期报错**（不会静默从空开始＝悄悄丢光记忆），文件不存在则视为首次运行空库，落盘失败记入 `LastPersistErr` 不吞。`NewMemStore()` 仍是纯内存模式（不写文件）。

TUI 用 `--persist`（或 `--memory-file PATH`）开启落盘到 `<os>/agent-memory.json`，并提供 `/remember <k> <v>`、`/forget <k>`、`/memory` 三条命令；每轮 AI 会把记忆块经 `aios.ExtraSystem` 注入系统提示，落盘失败会在状态栏明确告知并退回内存模式。serve 进程的会话记忆同理可文件化。`os/sessions/<id>.json` 另存 TUI 会话的用量账本，可与记忆文件并存。

### Q：怎么看 token 用量、成本和缓存命中率？

A：`internal/ai` 现已透出 provider 上报的 usage（兼容 OpenAI `prompt_tokens_details.cached_tokens` 与 DeepSeek `prompt_cache_hit_tokens` 两种形状）。三种入口：
- **TUI**：header 显示 `tok:prompt/completion · cache:% · $成本`；缓存未上报显示 `–`（不是 0%，那是「无从得知」），价目未知显示 `—`（不是 $0）。
- **CLI**：`forge os ai -z "..." --usage-file /tmp/u.json` 写 JSON（tokens/cache_hit_rate/api_calls/model_reported；价目未知时 `cost_usd: null`，**绝不写 0**）。
- **代码**：`aios.Report.Usage`（`ai.UsageLedger`）与 `Usage.Cost(model)`。

成本需要价目，且**默认未知即不显示**（不编价格造假）。注入方式：`HUSHFUSION_AI_PRICE_INPUT_PER_M` / `_OUTPUT_PER_M` / `_CACHED_PER_M`（每 100 万 token 美元）或 `ai.SetPrice`。有缓存价则按更低缓存价计费，未给则按输入价高估（不低报）。命中率只在 provider 真上报 `cached_tokens` 时有意义——`Usage.CacheReported` 专门区分「0 命中」与「没上报」，避免拿 0% 误导优化方向。

### Q：上下文压缩怎么做？

A：loop 先截断超长 tool 结果，再从最旧轮次删除并保留当前请求；会设置 `Compacted` 并追加“较早轮次已被压缩丢弃”的诚实提示。预算判定用「估算 × 校准系数」：用上次真实 `prompt_tokens` 与当次 `len/4` 估算之比修正——专门修中文（UTF-8 一字常被低估 5~18 倍）导致压缩永远不触发、上下文撑爆 provider 上限的根因；荒谬比例（<0.1 或 >20）拒采信防脏数据拉爆预算。它不是无损摘要，精确 tokenizer 也尚未接入。

## 3. 工具、权限、围栏与进程

### Q：工具调用如何注册？

A：`ToolRegistry` 注册 `ToolSpec + Executor`。模型只看到 `Specs()` 返回的声明；runtime 依据工具名路由执行。未知工具、非法参数 JSON、写工具不在白名单时 fail-closed。工具结果必须以 `tool_call_id` 回填。

### Q：工具名称有什么规范？

A：工具名必须非空并稳定；推荐使用命名空间形式，如 `os.view`、`kernel.submit`、`harness.run`、`skill.view`、`code.read`。同名注册时后注册覆盖，因此装配层必须避免无意覆盖，并在审计输出中保留实际名称。

### Q：权限验证分几层？

A：第一层是 runtime 的 `AllowWrites` 白名单；第二层是 OS kernel 的 syscall 和写前不变量；第三层可用 `internal/identity` 的 DID capability token 验证主体、能力、受众和有效期。任一层无法验证都拒绝，不能用“模型说它有权限”替代授权。

### Q：围栏和沙箱分别是什么？

A：围栏是约束集合，沙箱是执行实现。`internal/sandbox` 提供 jail cwd、路径逃逸检查、环境变量白名单、独立进程组、超时 TERM→KILL。它不是完整容器：当前没有 RLIMIT 资源限制、网络隔离，也不能消除符号链接检查竞态。

### Q：terminal 命令如何执行？

A：禁止裸 `os/exec` 直通 agent。命令应先通过 sandbox Policy，明确 binary、Root、EnvAllow、EnvSet、Timeout；stdin 不继承父进程，避免交互卡死。命令不存在、root 非目录、路径越界都应失败，而不是猜测替代命令。

### Q：process 管理如何避免孤儿和僵尸？

A：`internal/proc.Manager` 为每个进程登记 ID、PID、状态和退出码；启动独立 pgid，context 取消传播到子进程，waiter goroutine 负责回收，Stop 杀整个进程组，并可设置并发上限。登记表是进程内状态，不是跨重启的监控数据库。

## 4. 任务、计划、待办与目标

### Q：智能体任务和计划存在哪里？

A：`internal/agent/tasks.go` 的 `TaskStore` 提供任务、所属目标、证据和状态转移；HUSHFUSION OS 的 `os/TASKS.md`、`os/SPEC.md`、验收文件仍是项目级决策层。Agent 可以提议，kernel/OS 负责受控写入。

### Q：待办状态如何转移？

A：状态为 `proposed → active → blocked → done|dropped` 的受控集合。进入 `done` 必须提供证据路径；终态不可再次转移。完成只能被证明，不能被模型声称。

### Q：长期目标如何执行、查询和复盘？

A：长期目标必须有文本、完成判据和进度记录；活跃目标应绑定可查询的任务。查询应优先读取 `os` 状态或任务 store 的 active/summary，而不是依赖聊天记忆。review 由 `internal/review` 的规则 reviewer 或人工 reviewer 形成显式 verdict；没有批准结论就不能收尾。

### Q：计划能否直接变成完成？

A：不能。计划是提议和执行序列，结果必须落证据；任务状态、OS 写前守卫、review verdict 和必要的人工作为完成门。任何“模型回复看起来完成”都不等于系统状态已完成。

### Q：什么是派发/delegation？

A：这里将原清单中的“自粘贴派发”按任务或子智能体 delegation 理解：父 agent 生成有边界的子任务，子进程/子 agent 返回可审计结果。当前 runtime 有 kernel channel 和 process 管理原语，但通用子智能体派发协议、预算继承和结果合并尚未冻结，不能假装已经具备。

## 5. Bootstrap、文档、身份与模型

### Q：bootstrap 注入哪些文档？

A：`internal/persona` 按优先级发现 `SOUL.md`、`AGENTS.md`、`CLAUDE.md`、`.cursorrules`、`MEMORY.md`，再按预算合成 `# Agent Bootstrap`。大文件会截断并留痕，空目录不是错误；低优先级文档不会挤掉高优先级人格与规约。

### Q：agent 文档和 persona 文档怎么分？

A：agent 文档描述 runtime、工具、生命周期和边界，本仓主文档是 `docs/wiki/agent-runtime-design.md`；persona 文档描述“这个 agent 是谁、遵守什么行为约束”，主要由 `SOUL.md` 等文件承载。二者都只是输入，不能绕过 kernel 权限或完成门。

### Q：DID 身份如何使用？

A：`internal/identity` 用标准库 Ed25519 派生 `did:key:z...`，签发和验证固定算法的 capability token。token 包含 issuer、subject、caps、audience、issued/expiry；解析、签名、主体、能力和过期任一失败即拒绝。私钥只应由外部 KMS/HSM 或内存注入，本包不提供落盘私钥接口。

### Q：模型如何选择？

A：provider 由 `internal/ai` 的声明式 Profile 和注册表选择；模型由 Agent Config 的 Provider/Model/BaseURL 指定，空 model 使用 provider 默认值。查询入口：`forge ai models`（某 provider 模型列表）、`forge ai list`（已注册 provider）。密钥只能通过环境变量或受控注入进入，不能写进 CLI 参数、日志或 memory。

### Q：bootstrap 是否等于加载所有上下文？

A：不是。bootstrap、skills、memory、goal/task summary 都是有预算的输入来源；必须记录截断或未注入情况。加载失败应跳过可选文档并保留诊断，不能静默伪造“已加载”。

## 6. 当前边界与验收

已覆盖并有单测的核心机制包括 loop/tool roundtrip、未知工具和写权限拒绝、上下文裁剪+真实用量校准、session reset/quit、persona 发现与系统提示注入、DID 签验、sandbox 进程组超时、proc 回收/超时杀组/并发闸/forget、review 规则与任务完成门、token usage 透出（cost/cache）、TUI `--persist` 记忆落盘与 `/remember /forget /memory`、sandbox+proc 已接 `exec.bolloon`。

仍以代码落地和独立门禁为准、未冻结的部分：
- **`forge agent` 顶层 CLI 命令**：Agent Runtime 目前只能经 TUI（`forge os tui`）或 stdio serve 使用，未接进 `forge` 顶层 `agent` 子命令。
- **serve 进程的跨重启会话状态**：记忆落盘与 `os/sessions` 已覆盖“记忆”与“TUI 会话”，但不是 serve 进程完整状态的跨重启恢复。
- 流式输出；通用子智能体 delegation（派发协议/预算继承/结果合并）；精确 tokenizer（现用 `len/4` 估算＋真实用量校准）；网络隔离；RLIMIT 资源限制。

本页设计状态与已提交代码一致（截至 2026-10-23：sandbox+proc 接 exec、persona 接系统提示、review 门接任务完成判定、记忆持久化 v0.2、token usage 透出、`--persist` 接 TUI）。新增能力以代码和单测为准。