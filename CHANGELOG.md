# CHANGELOG — HUSHFUSION Forge

只记**判据变化与它对旧证据的影响**。流水账式的过程记录在 `docs/wiki/log.md`。
版本语义写死在 `docs/version-0.1.2.md` §3：

```text
MAJOR  冻结契约的断裂性变更        MINOR  新增能力        PATCH  改判据本身
```

---

## 0.6.0 — 2026-10-25（MINOR：需求清单收口 + OrbitDB 依赖自装）

- dchat 助手目录持久化到 `~/.forge/dchat`（`DCHAT_NODE_DIR` 可覆盖），内嵌 `package.json` 声明 `orbit-db` / `ipfs-core` / `hyperswarm` 依赖；频道启动时 status 探测，缺依赖自动 `npm install --no-audit --no-fund` 一次并重试（只试一次，失败如实报错并给出手动命令）。
- 持久目录不随 `Close` 删除：`node_modules` 装一次跨进程复用；脚本内容变化才重写。
- 至此需求清单全部落地：OrbitDB 商议维护 + git 存档、TERM 后端终端、私钥围栏、python/term 大内容执行、外部内容不可信包装与 prompt/compress/MCP/plugin 防护、主动交流心跳、双内核调度、多会话并发与右侧商议面板。

## 0.5.0 — 2026-10-25（MINOR：TERM 后端终端 + OrbitDB 商议 + git 存档）

- 新增 `term.run` / `term.reset`：后端常驻 bash 终端，cwd 与环境跨调用保持（`cd`/`export` 生效）；超时杀进程并自动重启回 workspace 根；输出 256KB 截断；受 workspace 锁与隐私围栏约束；Web 写工具默认开启。
- 智能体内部商议接入 OrbitDB：`room.start` 后每条 `room_post`/自动汇报异步镜像到 orbitdb 频道（`forge-room`），状态写回 `RoomState.OrbitStatus`（ok / checking / 错误摘要）；缺 Node 依赖时 fail-closed，如实显示"不可用"，不假装已同步。
- 新增 `room.archive`：把商议记录增量写入 `<data-dir>/room-archive` 独立 git 仓库（`journal.jsonl` 追加游标 + `git commit`），不碰用户项目仓库；商议结束（手动停止或轮次上限）自动存档，失败把错误投递回商议频道。
- 商议面板状态行显示 `orbitdb: <状态>`。

## 0.4.0 — 2026-10-25（MINOR：隐私/注入围栏 + Python 执行 + 主动商议）

- 用户消息进入会话前硬拒绝私钥、API key、GitHub/AWS/Slack token、Bearer 等凭据；拒绝的消息不落库、不发模型。旧历史/房间消息启动时也清洗。
- 工具输出统一按不可信外部数据包装：私钥整段阻断，其余凭据/邮箱/手机号/证件号脱敏，转义 `<invoke>` / `<|im_start|>` / `<<SYS>>` 等标记，64KB 截断。
- `read` / `ls` / `shell` 增加敏感路径与命令围栏（`.ssh`、`.env`、`secrets.json`、`id_rsa`、keychain 等），并解析符号链接防绕过。
- 新增 `python.run`：在 workspace 内用 `python3 -I` 执行，最小 env、workspace 锁、10 分钟超时、256KB 输出上限；Web 默认写工具同步开启。
- MCP / 插件调用改为写工具（需白名单），MCP 输出 64KB 截断；插件 load/call/unload 不再默认可执行。
- 上下文压缩摘要明确禁止把工具/外部内容里的指令写进摘要，防止压缩攻击提升权限。
- 内部商议支持主动维护：两个智能体轮流被唤醒、读最近消息、用 `room_post` 发言；每轮写心跳、轮次和状态，最多 12 轮，可手动结束；消息落 `state.json`。

## 0.3.0 — 2026-10-25（MINOR：双内核调度 + 多会话并发 + 内部商议）

- Web 执行层改为双 lane 内核：`primary` / `secondary` 各自有容量与心跳，按占用把新会话分到较空的 lane；满载时返回明确繁忙，不互相覆盖。
- 每 lane 每秒写 `~/.forge/web/kernel/<role>.json` 心跳；`GET /api/kernel` 返回 role、active/capacity、run 列表、Go heap/goroutine 占用。
- 前端运行态从全局 `busy` 改为按 session 的 `busySessions/aborts`：会话 A 生成时可切到会话 B 继续发送，两个 session 的停止按钮互不影响。
- 同一 workspace 的 `shell` 通过内核锁串行执行，避免两个会话同时写同一目录；Web shell 超时放宽到 10 分钟。
- 新增内部商议频道：右侧面板默认关闭，顶部“内部商议”打开；任务结束后自动汇报，智能体可用 `room_read` / `room_post` 讨论；面板只展示内部消息。
- `GET /api/room`、`POST /api/room`、`GET /api/kernel`；room 持久化在 `state.json`，最多保留 300 条。

## 0.2.10 — 2026-10-25（PATCH：更新检查改走 npm，绕开 GitHub API 限流）

- 检查更新优先请求 npm registry 的 `@hibs/forge/latest`，不再依赖 GitHub 未认证 API 的 60 次/小时额度。
- npm 不可用时回退 GitHub API，再回退 `releases/latest` 的 302 跳转；资产 URL 按 Release 命名规则构造。
- 修复被 GitHub API 限流后 `latest` 为空、更新按钮不出现的问题。

## 0.2.9 — 2026-10-25（PATCH：全量工具名 provider 安全化）

- `ToolRegistry.Specs()` 统一把工具名映射为 `[A-Za-z0-9_-]` 安全名：`mem.get → mem_get`、`tasks.set_state → tasks_set_state`、`skill.view → skill_view`。
- 模型返回的 `tool_calls.function.name` 以及文本 `<invoke name="mem_set">` 会在执行前映射回内部原名，写工具白名单仍按原名判断。
- 修复 custom 等端点对带点 function name 的 `400 invalid function name`，不再只修 `agent_paths` 一个工具。
- 新增回归断言：模型可见工具名不得含点号；`mem_set` 原生调用实测落盘成功。

## 0.2.8 — 2026-10-24（PATCH：修复工具名非法导致模型 400）

- 将 `agent.paths` 改名为 provider 安全的 `agent_paths`：部分 OpenAI-compatible 端点拒绝带两个点的 function name，会直接返回 `400 invalid function name`。
- 系统提示同步改为 `agent_paths`；新增回归测试仍覆盖路径自查。
- 实测普通对话与 `shell` 工具调用恢复正常。

## 0.2.7 — 2026-10-24（PATCH：Web 后台长任务，不随浏览器断开）

- Web 一轮对话改为服务端后台执行：不再绑定 SSE 请求的 `r.Context()`，页面刷新、关闭标签或网络断开不会取消任务。
- 新增 `GET /api/chat/status?session_id=`（运行状态 + 已生成部分文本）和 `POST /api/chat/stop`。
- SSE 每 15 秒心跳，避免长工具调用期间连接被中间层判死；页面重载后自动接续显示后台部分文本，完成后拉取最终历史。
- 停止按钮现在同时中止前端流和服务端后台任务；同一会话并发发送返回 409。
- Web 单次模型调用超时从 120 秒放宽到 10 分钟；工具往返上限仍为 200，避免无限挂死。

## 0.2.6 — 2026-10-24（MINOR：每智能体 workspace + 全局专属目录）

- 智能体新增 `workspace`（工具根/`shell` cwd）与 `global_dir`（全局专属目录）；编辑智能体可直接填写，留空自动回落。
- 默认全局目录为 `~/.forge/web/agents/<agent-id>`；`mem.set` 落盘到 `<global_dir>/memory.json`，跨会话保留。
- `read` / `ls` 支持访问 workspace 或全局目录内的绝对路径；越界仍拒绝。
- 系统提示自动注入 Workspace、全局目录、记忆文件路径；新增只读工具 `agent_paths` 供智能体自查。
- 启动时把已有智能体补齐默认目录；重启/换会话后记忆自动加载并注入 system。

## 0.2.5 — 2026-10-24（PATCH：更新入口常驻检查，不改判据）

- 启动后每 60 秒检查一次更新；新版本发布后，已打开的页面无需刷新即可出现底部更新按钮。
- 设置弹窗新增“检查更新”和状态行：显示“当前已是最新 0.2.x”或“发现新版本 0.2.y”。
- 无更新时底部按钮仍按原约定隐藏；设置里可手动确认版本状态。

## 0.2.4 — 2026-10-24（PATCH：一条龙自动更新，不改判据）

- 点击更新后直接下载、替换当前二进制、拉起新进程并退出旧进程；前端轮询 `/api/version`，版本切换后自动刷新。
- 新进程通过 `HUSHFUSION_RESTART_WAIT` 等待旧进程释放端口；监听端口被占时自动重试，避免替换成功但新进程 bind 失败。
- Windows 改为“运行中的 exe 先改名到 `.bak`，再写入新 exe”，不再返回“不能自替换”错误。
- 移除更新流程里的 `confirm` 和 `alert`；更新中/重启中/失败只改按钮文案，控制台记录错误。

## 0.2.3 — 2026-10-24（PATCH：文本工具调用兜底，不改判据）

- 部分 OpenAI-compatible 端点把工具调用写成 `<invoke name="mem.get">…</invoke>` 文本，运行时之前会把它当普通回复显示。
- `Turn` / `TurnStream` 现在识别已注册工具的文本调用块，转成标准 `tool_calls` 执行并回填；未知工具/代码示例不执行。
- 同一响应内重复调用去重；SSE 增量阶段隐藏原始 `<invoke>` 块，避免用户看到一屏调用文本。
- 新增 `TestParseTextToolCalls`、`TestTurnExecutesTextToolInvoke`、`TestTextToolStreamFilter`。
- npm 包同时以 `@hibs/forge@0.2.3` 发布（`forge` bin，公开访问）；原 `hushfusion-forge` 继续保留。

## 0.2.2 — 2026-10-24（PATCH：Web 外壳交互修复，不改判据）

- 生成中发送按钮变为终止按钮，可中断 SSE 并保留已生成内容。
- 流式回复只增量更新当前 AI 气泡，扫光动画速度稳定；滚动仅发生在消息区。
- 写工具 `shell / mem.set / mem.reset / tasks.add / tasks.set_state` 默认开启；只读工具保持默认可用。
- 修复 Go 模块 `v0.2.1` 因 `python/aux` 保留名被 proxy 拒绝的问题，目录改为 `python/auxiliary`。
- 发布 npm 包 `hushfusion-forge@0.2.2`（启动器 + Node SDK）与 Go `v0.2.2` Release。

## 0.2 — 2026-10-05（MINOR：版本号跃迁 + 证据冻结）

**这是判据版本从 `0.1.x` 跃到 `0.2` 的一次 MINOR**（新增能力=版本号正式成为 0.2 线）。它不改变任何判据数字
（目标函数 / clearance / 门阈值全部与 0.1.3 相同），但**版本税必须交**：hello 里带 `engine.version`，
bump 让三份冻结 trace 与两份世界结构证据全部失效，重录后除版本串外逐位一致。

### 1. 改了什么

| 位置 | 内容 |
|:--|:--|
| `internal/config/config.go` | `ForgeVersion = "0.1.3"` → `"0.2"` |
| 三份 trace | `world_trace_golden{.jsonl,_v2.jsonl,_mu.jsonl}` 用 `forge world serve --record` 重录 |
| 两份世界结构 | `world_structure_v2.json` / `world_structure_mu.json` 用 `--write` 重测 |

### 2. 版本税的**干净**实测（这次没踩 0.1.3 那个坑）

重录时用**旧 trace 的 req 序列**喂给 0.2 新世界；重录后把新 trace 的 `engine.version` 归一成 0.2
与旧 trace 逐位比较 —— **0/8、0/10、0/17 全部一致**。世界结构同理（排除 `engine_version` 后逐位一致）。
⟹ 引擎除版本串外**没有行为变化**，这是把"bump"与"物理漂移"分开的硬证据。

> **发版清单规则**（写死）：重录 trace 必须**显式给 `--protocol`**，且重录后做"归一逐位比较"，
> 确认 diff 只在 `engine.version` —— 否则无法区分"版本税"与"真的改了行为"。

### 3. 门禁（`bash scripts/verify.sh phase1`，判据 0.2）

**19 通过 / 1 失败 / 3 跳过**：

- **G19 红** —— 与升号前**同状态**（G19b 弱档、G19c 源没进动力学），是**既有已知结论**（脚本明说
  弱档时 G19 必须红 = 真实结果不是 bug），非升号引入。
- **G12/G15/G16 跳过** —— `runs/phase1/registry.jsonl` 是 0.1.3 判据产物，0.2 下 `SAME_JUDGE` 不成立
  （照 §3.2"旧证据永不重跑覆盖"，如实 SKIP，不是通过）。
- **G14 从红变绿** —— 0.1.3 的 phase1 有 `forge_version` 字段 ⟹ 0.2 下规则对账真实跑通（scipy 独立复核）。

### 4. 版本史

`0.1.0`（Phase 0）→ `0.1.1`（计划重编，仅文档）→ `0.1.2`（可造性进判据，PATCH）→
`0.1.3`（v1 trace 假绿修复，PATCH）→ **`0.2`**（版本线跃迁 + 证据冻结，MINOR）。

### 未做

`runs/phase0`（0.1.0 判据）与 `runs/phase1`（0.1.3 判据）**保持旧证据，不重跑覆盖**（§3.2）；
B 项世界语义重做仍未实施（G19 红为设计所许）；协议 v4 未做；硬件四轴仍挂起。

## 0.1.3 — 2026-10-02（PATCH：一条真缺陷修复 + 计划对齐）

**判据未变**（`clearance` 罚项与 `min_clearance = 0.05` 与 0.1.2 完全相同），所以这是一次 PATCH ——
但它必须发，因为里面有一条**真缺陷**。

### 1. 真缺陷：0.1.2 重录 v1 黄金 trace 时漏了 `--protocol 1`

重录命令缺了 `--protocol`，而缺省值是 **2** ⟹ 那份文件装着 v1 的请求序列、响应却是 protocol 2 的会话。
**G18 当时是绿的——假绿**：回放以 trace 自己的 `hello.protocol` 为准，标错版本的 trace 在回放时
自己跟自己比，必然逐字节一致。抓到它的是 `internal/world` 的单测
（`TestTraceProtocolReadsTheHelloResponse`：`the v1 golden trace declares protocol 2, want 1`）。

> **规则（写死）**：回放类门的判据若是「与 trace 一致」，它**无法**发现「trace 本身标错」。
> 一份证据至少要有一条**不依赖它自己**的检查。两条路都要求「对得上」，但对的不是同一个东西：
> 一条对字节，一条对语义。
> **发版清单新增**：重录 trace 必须**显式给 `--protocol`**。

### 2. 版本税的**第二笔**实测（同 0.1.2 的成因）

版本 0.1.2 → 0.1.3 后三份 trace **再次全部失效** —— 世界 hello 的声明里带 **engine 版本串**，
而它是冻结字节的一部分。⟹ 三份 trace 重录 + 两份世界结构证据重测（`engine_version` 字段）。

| trace | 行数 | 响应字节 |
|---|---:|---:|
| v1 | 8 | 5653（**缺陷版，作废**）→ 见本轮实测 |
| v2 | 10 | 7199 → 见本轮实测 |
| mu | 17 | 10048 → 见本轮实测 |

⟹ 这是**每版都要交的税**。候选处置（**本版不动，留待决策**）：协议声明只应携带**语义哈希**，
不该把构建期字符串放进冻结字节；若保留 engine，则它必须在回放比较中**被排除**。
两者任一都要求协议版本前进（**v4**）= 协议契约变更，不能顺手做。

### 3. 计划对齐（只对齐文档与记账，不改判据）

并行的计划线改成 **v3**（`docs/wiki/fusion-program-roadmap-v3.md`）：主路线 = 时变电磁场 →
产生/调制引力场 `μ(t)` → **引力场承担主要约束与惯性/输运调控**；FRC 降为「工程参照 + `FC5`
稳定性对照 + 独立备用」；`G4` 改为「引力场主约束路线门」；新增引力场—等离子体耦合层。
**本版对它的贡献只有一条硬的**：0.1.2 的 **G23 可造性门** + `clearance` 罚项是那张计划里
「制造/可复用性」要求**目前唯一已落成机器判据的一条**，其余仍是计划文本或 `[缺口]`。

### 未做

B 项世界语义重做仍未实施（预注册于 `docs/version-0.1.2.md` §2），**G19 仍红且红为设计所许**；
协议 v4 未做；硬件四轴仍挂起。

## 0.1.2 — 2026-10-01（PATCH：判据变更 —— 可造性进目标函数）

### 版本史的一处说明：0.1.1 是**并行的计划重编轮**

同一时间另一条线把聚变计划重编（先 v2 `docs/wiki/fusion-program-roadmap-v2.md`：可复用制造系统 +
五步工程法 + 物理判决漏斗；随后重读上游物理架构改为 v3 `docs/wiki/fusion-program-roadmap-v3.md`：
μ 局部/全局分层、S3 先于大型装置），**只改文档、不改判据**。那一轮记为 **0.1.1**；
本版（**0.1.2**）是**判据变更**，接在它后面。两轮合起来是这一版的实际内容：
计划换了组织方式，判据多了一条可造性。

参考它时必须一起读它的四条边界（尤其是「Forge 当前是线圈/真空静磁设计搜索器，
不是等离子体平衡、有限 β、输运或发电量证明器」与「μ 的主动产生仍是第二输入缺口」）。

### 判据变了：同一个设计现在得不同的分

- 目标函数新增第 4 条罚项 **`clearance`**：`max(0, (min_clearance − min_clearance_m)/min_clearance)`。
- `spec` 新增 **`min_clearance = 0.05` m**（工程判断，非物理；唯一硬约束 = 必须让人工基线仍然可行）。
- `physics.Metrics` 新增 **`min_clearance_m`**（**冻结类型变更**）：导体面到约束区域的最小净空 [m]，可负。
  约束区域 = 中心元胞 `{0 ≤ r ≤ RPlasma, |z| ≤ ZCell}`（与 `volume_good` 同一口径，不是 `ZMid`——
  那只是 `B_mid` 的取样窗）；导体面 = 丝环 ± `t_pack/2`。口径与死法见 `docs/version-0.1.2.md` §1。

### 为什么改（这不是加功能，是修一个具体的失败）

0.1.0 的冠军 **D9857** 把一圈 `a=0.10 m`、`1.78 MA` 的导体放在距中场采样点 **4.196 mm** 处——
它就坐在约束区域里面。旧目标函数没有任何一项阻止这件事，于是「机器赢人类」赢在了一个真实装置
不可能有的位形上。**这是 0.1.0 自己写在诚实边界里的那条，0.1.2 把它变成判据。**

### 实测（消融轴 = 版本边界；同一元组 budget 1000 × seeds 0,1,2 × 4 方法）

| | 0.1.0（`runs/phase0`） | 0.1.2（`runs/phase1`） |
|---|---:|---:|
| 人工基线 | −0.2905708161 | −0.2905708161（**不变**：基线净空充足，罚项为 0） |
| 机器最优 | 1.3814186（D9857） | **0.9488837**（D7996, evolution, seed 1） |
| **Δ vs 人类** | **+1.672** | **+1.2395** |
| 可行记录 | 11072 / 12001 | **7221 / 12001** |
| 追平基线所需评估（4 方法均值） | 8.3 / 11.3 / 14.0 / 8.0 | 12 / 17 / 40 / 9 |

**「可造性这条约束值多少钱」= 0.4325（Δ 掉了 26%）；它同时砍掉约 32% 的搜索空间
（11072 → 7221 条可行）。** 四方法仍然 3/3 seed 赢基线。

**0.1.0 那个冠军在新判据下的下场**（`scripts/check_buildability.py` 现场重算）：
`score 1.3814186 → −13.6099621`，`feasible True → False`，`clearance 罚项 = 1.5`，`最小净空 = −0.025 m`
（= 恰好 `−t_pack/2`，即导体整根埋在约束区域里）。这个 swing（14.99）就是这条约束的分量。

**新最优点的位置**：净空 **0.0518751 m**（要求 0.05）——**贴着新约束的边界**。这是正常的最优化行为
（任何起作用的约束都会被顶到边界），且与 0.1.0 的边界性质不同：0.1.0 顶的是求解器 5 mm 的 `alpha2`
**数值钳位**（物理上不存在），0.1.2 顶的是一条**声明的工程要求**。**由此得一条必须一起说的限定**：
`Δ = +1.2395` 这个数是被 `min_clearance = 0.05` 这个**判断**撑着的，换一个值它就是另一个数——
它不是物理常数。`MinClearance` 住 `internal/config/config.go`，随时可被质疑并重设（重设 = 新的 PATCH）。

### 对旧证据的影响（写死）

- `runs/phase0/` 是 **0.1.0 判据下的证据**，**永不重跑覆盖**；引用它必须带 `v0.1.0` 字样。
- 带 tag 的门里 **G12 / G15 / G16 额外要求判据版本匹配**：拿 0.1.2 的尺子量 0.1.0 的 run，
  它们 **SKIP** 并在抬头大声说明（`SKIP ≠ 通过`）。默认 tag 从 `phase0` 改为 `phase1`。
- run 的 meta 新增 `forge_version`（真源：`internal/config.ForgeVersion`，`cmd.forge.Version` 只是别名）。
  0.1.0 的 run 没有这个字段 ⟹ 显示 `unknown(pre-forge_version)`，**不替它猜一个版本**
  （猜出来的版本会让门绿在一个不存在的事实上）。

### 新增门

- **G23 可造性门**（`scripts/check_buildability.py`，三条一起判）：(a) 基线可行且 `clearance` 罚项精确为 0；
  (b) 0.1.0 的退化解在 0.1.2 下不可行且罚项 > 0；(c) 净空闭式解 == 圆环面密采样独立路径（< 1e-9）
  且侵入时精确 = −t_pack/2。**没有 (b)，「退化解被踢出去了」就只是一次性观测。**

### 复核时发现、**不修**的一处既有漂移

人工基线 `field` 项：Go 给 `-3.442446546963692e-11`，Python 参考实现给 `9.64327466553287e-17`
⟹ `score` 相差 **3.442e-11**（在 1e-06 容差内，两者都等价于「B_mid 精确等于 B_ref」）。
用 `git worktree add --detach HEAD` 在 0.1.0 原树上复核过：**该差在本次改动之前就存在**。
记下来是为了下一个人不必重新怀疑一遍。

### 未做完（如实）**

- **B 项「世界语义重做」（让 R3/R4 真进动力学、G19 该绿）本版未实施**：方案已写进
  `docs/version-0.1.2.md` §2（把 v2 世界的转移接到 `internal/design` 的 L3 判决，复用已被 G17 锚定的
  实现，不新增物理），判据与死法也已预注册；但代码未动。G19 的状态因此与 0.1.0 相同：
  **红，且红为设计所许**。下一版做。
- **硬件四轴（装置/诊断/建造/排期）仍未接入**：等上游把环几何与设备清单填好；
  `artifacts/world_feed/world_feed.json` 里 7 个缺口槽位取值仍为 `null`——**缺口处不填预设值**。

---

## 0.1.0 — 2026-09-26（Phase 0 收口）

Phase 0 的验收问题「机器能不能找到一个比人工设计基线更好的电磁设计，且能被独立复现」：
能。12001 条记录、4 方法 × 3 seed × 1000 次评估、18 道门全绿。人工基线 −0.2905708161，
机器最优 1.3814186（D9857），Δ = **+1.672**。
**但最优解踩在一个退化解上**（导体距中场采样点 4.196 mm，落在求解器 5 mm 钳位半径内），
且热启动增益在 1000 次预算下不显著（+0.004，方差更大）。这两条写在 `runs/phase0/report.md` §6
与 `docs/wiki/current-status.md` 的诚实边界里。详细见 `PLAN.md` §3。
