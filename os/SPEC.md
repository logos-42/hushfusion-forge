# HUSHFUSION OS —— v0.1 实施规格

> 这份文件把「HUSHFUSION 总目标」编译成可执行的最小闭环。**它是决策层，不是新平台**：
> 任务池 + 证据状态机 + 机会池 + 决策日志 + 周复盘，全部用 Markdown / JSON / 脚本 / Git 运行，
> 挂在现有 hushfusion-forge 仓库里，不新建基础设施。
> 创建：2026-10-17 ｜ 最后确认：2026-10-17 ｜ 版本：v0.1

## 0. 为什么（一句话） + 架构（kernel 主导）

每天早上不应该重新决定「今天要不要努力做 HUSHFUSION」——应该由系统给出唯一主任务、验收标准和证据门槛，让下一步行动清晰、成果可积累、错误早暴露、机会进工作流。

**原则：把自己设计成系统的设计者，不是项目的发动机。**

**架构：HUSHFUSION OS = 决策层（`os/`，人 + 证据在环）+ 执行引擎（Agent Kernel 主导）。**

```
决策层  os/（任务池 / 五态状态机 / 决策日志 / 周复盘）—— 定义「做什么、凭什么算完成」
   │ 提交任务（channel.submit）
   ▼
执行引擎  Agent Kernel（Bolloon Native Macro-Kernel，经 bridge 跨语言接入）——
          channel 串行执行（严格 FIFO）/ lifecycle 记录运行 / transport 通信 / model.acquire 模型租约
   │ 产出证据
   ▼
证据层  runs/<tag>/ + 门禁 verify.sh（22~24 门）+ wiki —— 唯一权威，Agent 说完成 ≠ 完成
```

`os/` 是 kernel 的**工作记忆与决策面**，不是替代 kernel 的第二套执行系统。执行一律走 kernel 机制原语；`os/` 只回答「做什么、凭什么算完成」。

## 1. 目录与职责

```
os/
├── NORTH_STAR.md      # 防止项目不断改变方向（目标与边界，防漂移规则）
├── CURRENT_STATE.md   # 防止凭感觉判断进展（五问：状态/下一步/证明/自动化/变好）
├── TASKS.md           # 防止每天重新思考先做什么（任务池，P=I×E×R/C 排序）
├── EXPERIMENTS.md     # 防止理论和仿真停留在想象（五态证据状态机）
├── OPPORTUNITIES.md   # 防止科研合作与资金机会散失（带匹配理由与下一步）
├── DECISIONS.md       # 防止反复争论已经讨论过的问题（决策日志 + 依据）
├── WEEKLY_REVIEW.md   # 防止系统长期沿错误方向运行（每周六四问复盘）
├── acceptance/        # 可重复执行的验收标准（每个 active 任务一份）
├── tools/             # 自动化脚本（daily_check.sh / weekly_review.sh）
└── SPEC.md            # 本文件
```

## 2. 接入边界（与现有 Forge / Kernel / 研究工作的关系）

| 层 | 归谁 | OS 做什么 / 不做什么 |
|:--|:--|:--|
| 物理/证据 | `runs/<tag>/` + wiki（唯一权威） | OS **不存证据**，只索引与映射状态 |
| 门禁 | `scripts/verify.sh`（22 门，唯一权威） | OS 自动化**只调用**，不改门、不绕门 |
| 合同/裁决 | `CONTRACT.md` §9-10 + `docs/version-*.md` | OS 的 DECISIONS.md 与之一脉相承，冲突时以合同为准 |
| 上游物理 | `~/Downloads/lean/ProjectionPhysics`（活源） | OS 不搬运上游结论；锚点仍由 `emit_*_anchors.py` 重放读出 |
| Kernel | `internal/kernel/` | **执行引擎（主导）**：Bolloon kernel 机制原语的 Go 原生移植（channel.submit / lifecycle / transport / model.acquire），G24 已真跑。os/ 决策不替代 kernel 执行 |

## 3. 统一数据结构

### 任务（每项四字段 + 评分）

```json
{
  "id": "T-01",
  "state": "active | proposed | blocked | done | dropped",
  "produce": "预期产出",
  "accept": "验收标准（指向 os/acceptance/<id>.md）",
  "next": "下一步动作",
  "deps": "依赖或阻塞原因",
  "P": {"I": 5, "E": 5, "R": 4, "C": 3, "score": 33}
}
```

排序启发式：`P = I × E × R / C`（各 1–5 分）。**这是初始排序启发式，不是科学公式**——分数用来辅助决策，不是客观真理。

### 实验（五态，见 `os/EXPERIMENTS.md`）

问题 → 方案（预注册判据+死法）→ 可复现测试（落盘）→ 独立反馈（不依赖被测对象）→ 决策（继续/修正/停止）。

### 状态转换规则

- `proposed → active`：必须写验收标准（`os/acceptance/`）+ 预计产出。
- `active → done`：验收标准逐条勾验，附证据路径。
- `active → blocked`：必须写阻塞原因（不写 = 逃避决策）。
- 任何状态 → `dropped`：必须写原因（发现不值得做，本身是有效结果）。
- **铁律**：Agent 说完成 ≠ 完成；每个转换至少一条检查不依赖被测对象自己（对字节 + 对语义双通道）。

## 4. 第一批自动化与测试命令

| 自动化 | 位置 | 干什么 | 首次验证 |
|:--|:--|:--|:--|
| 日常检查 | `os/tools/daily_check.sh` | 编译 + 名册门 + **oscheck 决策层一致性门（Go）** + wiki 三连 + 脏树提醒（~3 秒内返回） | 2026-10-17 实测；oscheck 2026-10-18 接入 |
| 决策层一致性门 | `os/tools/oscheck/`（Go，`go run ./os/tools/oscheck`） | 任务池铁律 T1–T8（active≤1 / P 对账 / 验收文件必在 / 排序）+ kernel 回执 K1/K2（active 必有非 failed 回执 / done 必有 settled 回执）+ 实验不跳态 E1 + 验收文件不孤儿 A1；变异测试随 G5 自动跑 | 2026-10-18 实测：真实任务池 8 件全绿；15 条变异全部能红 |
| kernel 提交/结算 | `os/tools/ossubmit/`（Go，`go run ./os/tools/ossubmit [--settle ID --evidence runs/…]`） | OS↔kernel 双向环执行面：channel.submit 提交唯一 active 任务、回执落盘 `os/kernel_receipts/`；`--settle` 验收全勾+证据在盘才回写 active→done（fail-closed） | 2026-10-18 真 bridge 实测：T-01 提交成功（回执 verbatim kernel 响应）；未勾验收 settle 被拒（0/6） |
| 周复盘草稿 | `os/tools/weekly_review.sh` | 收集本周 `runs/` + `os/EXPERIMENTS.md` + `git log` → 生成复盘初稿 | 待 2026-10-23 首次运行 |
| 全门验收 | `bash scripts/verify.sh phase0` | 22 道门（G19 预期红） | 已 21 绿/1 红 |
| 提交门 | wiki 三连 + 名册门 | push 前必跑 | 已集成 |

## 5. 7 天内可验收的交付物

| 天 | 交付物 | 验收 |
|:--|:--|:--|
| D1（今天） | os/ 目录 + 7 文件 + 2 脚本 + 名册登记 + wiki 回写 | `git status` 干净、门全绿、wiki 三连通过 |
| D2 | 数据与文献流程首条入口 | `os/EXPERIMENTS.md` 至少一条「数据/文献来源」可追溯 |
| D3 | 最小验证流程 | 一条可重复执行的基准/数据检查（命令 + 输出落盘） |
| D4 | Forge 验收门槛明确 | 哪些测试通过、哪些科学问题仍未解决（写进 CURRENT_STATE） |
| D5 | 合作机会流程 | `os/OPPORTUNITIES.md` ≥3 条有研究方向×匹配理由×下一步 |
| D6 | 自动化 | 至少一项重复任务自动运行并保存日志 |
| D7 | 系统复盘 | 找出最耗时环节 + 最大不确定性 + 下周唯一主任务 |

**约束**：每天优先交付**一个可复用的能力**，不堆七天的待办。

## 6. 分工：Agent 做什么 / leo 做什么

| 阶段 | Agent（我） | leo（人类） |
|:--|:--|:--|
| A 信息整理 | 读论文/数据入口/项目文件；整理证据、缺失信息、候选任务；**所有来源保留链接** | 裁定研究方向 |
| B 执行与测试 | 受控环境跑测试、生成报告、比较结果、发现回归 | 复核结论 |
| C 协作与机会 | 整理专家名单、草拟邮件、追踪合作状态、提醒下一步 | **对外发送前必须确认** |
| D 决策支持 | 汇总每周证据/成本/风险/机会，提出继续/修改/停止建议 | **不自行认定理论成立**——科学判断只属于 leo/独立研究者 |

**硬边界**：Agent 可以建议「哪个任务 P 高」「哪条证据链完整」，**不可以**说「理论成立」。理论成立与否，必须由外部反馈（公开数据、独立 oracle、上游锚点、多 seed）或 leo 本人裁定。

## 7. 防陷阱

1. **搭建系统本身可能变成逃避验证的高级方式** → 每一项系统建设必须回答：是否更快获得可靠物理证据 / 更容易获得外部反馈 / 更有效地获得资源？三答皆否 → 不做。
2. **复杂度诱惑** → v0.1 只允许：一个任务池、一套证据记录、一个自动执行流程、一个每周决策闭环。多 Agent 平台、融资自动化、科研数据库，等最小闭环真运转后再议。
3. **记录 ≠ 结果** → 每个文件承担一个决策功能；没有决策功能的记录不建。
4. **不重复造轮子** → 自动化全部复用 `scripts/verify.sh` / `wiki_check.py` / 已有 daemon，不新建平台。

## 8. 兼容接口（bolloon 已接 G24；headless 预留）

### 8.1 与 Bolloon（已接入，G24 七条真跑）

- 执行引擎 = **Bolloon Native Macro-Kernel** 的 **Go 原生移植**（`internal/kernel`，进程内即用；2026-10-23 起不再经 Node `bridge/`）。
- 已暴露机制原语：`channel.submit`（串行 FIFO）/ `channel.peek` / `lifecycle.submit` / `transport.submit` / `model.acquire`（只读租约）。
- 设计纪律（来自 kernel SDK）：**只暴露机制，不判「该不该继续」；端口未注入 ⇒ fail-closed**——与 OS「Agent 说完成 ≠ 完成」同一哲学。
- 门：G24（串行 / 隔离 / fail-closed / 命名端口 + 双核）。前置：无（原生内核，进程内即跑）。

### 8.2 与 headless（预留接口，预备，不实现）

- headless = SSM + OML 持续学习模型系统（`~/Downloads/headless`，纯 Python，logos-42）。它以后**注入 kernel 作为核心**（模型推理 / 持续学习本体），本版只**留接口**，不做实现。
- **预留槽位 = kernel 的 `model.acquire` 租约机制**：headless 的 OaK/LM 模型实现 `openConnection` 端口（推理 + 持续学习状态快照），经 bridge 的 `model.acquire` 暴露给 Go 侧——G24 (f)「缺 openConnection ⇒ 内核抛错」正是为这个槽位预埋的诚实边界。
- **预留语义边界（与 headless 的既有进程边界一致）**：本仓世界协议（`docs/world-protocol.md`）只搬运 + 校验，不添加 shaping / 惩罚 / 归一化；headless 注入 kernel 同样只走机制，不掺业务判定。
- **状态**：`proposed`（预留在 TASKS.md T-08）。不写代码、不建服务、不占门。等最小闭环真运转 + leo 拍板注入时机再激活。
- 相关：`docs/wiki/os-operations.md`、T-08、`~/Downloads/headless` README。

### 8.3 OS ↔ kernel 双向打通（已实现，forge os，2026-10-18）

`forge os <submit|peek|sync>` 把决策层接到 kernel 上，双向闭环：

| 命令 | 方向 | 做什么 | 证据落盘 |
|:--|:--|:--|:--|
| `forge os submit --channel <id> --task <taskID> [--marker <m>] [--sleep-ms <n>]` | OS→kernel | 把 OS 任务投递给 kernel channel，ChannelActor 串行执行（严格 FIFO / 跨 channel 隔离 / 端口未注入 fail-closed） | `os/evidence/submit-<ts>.json` |
| `forge os peek --channel <id>` | kernel→OS | 读回该 channel actor 的状态快照 | `os/evidence/peek-<ts>.json` |
| `forge os sync ...` | 双向 | submit 后立即 peek，**核对 kernel 记住了本次提交的 task/marker**——这就是打通的证据 | `os/evidence/sync-<ts>.json` |

**语义（与 kernel SDK 纪律一致）**：OS 只给「做什么」（task/marker），kernel 只执行机制（串行、隔离、fail-closed），**不判「该不该继续」**——判据仍是门禁与 leo。

**与 `os/tools/ossubmit` 的分工（同一底层 internal/kernel）**：
- `ossubmit`（os/tools/）是**编排闭环**：读 TASKS.md 找唯一 active 任务 → 提交 → 回执落盘 `os/kernel_receipts/<id>.json` → `--settle` 验收勾验 + 翻态回写；oscheck 门（T1–T8/E1/A1/K1–K2）随 G5 强制它。
- `forge os`（cmd/forge）是**机制核对原语**：不绑定 TASKS.md，任意 channel 可 submit/peek/sync；sync 做「提交→读回 actor 状态→核对 task/marker」的显式双向证据（ossubmit 没有读回 actor 状态这一步）。
- 共用 `internal/kernel` 同一客户端，无重复代码；日常流程走 ossubmit，机制核对/调试走 forge os。

**诚实边界（实测确认）**：
1. 每个 `forge os` 调用是**独立内核会话**——ChannelActor 状态只在进程内存在，进程退出后 actor 即消失。所以 `sync` 是同一进程内完成双向核对；跨进程「读回上次提交」不存在，要核对历史去读 `os/evidence/` 落盘文件（peek 在 actor 不存在时会给出这条提示，不假装成功）。
2. `--sleep-ms` 目前是**机制演示**：kernel 侧执行的是「串行等待 + 记 marker」，不是真实物理计算。真实任务执行（把 verify/run 挂进 channel）是下一步——先证明机制双向通，再挂负载。
3. `os/evidence/` 已 gitignore（scratch 性质）。**决策落盘走结构化文档**（DECISIONS / EXPERIMENTS / TASKS 状态机），evidence 只作核对用。