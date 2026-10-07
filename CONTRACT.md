# CONTRACT — Forge v0.1 并行构建合同

本文件是**所有并行子线的唯一权威合同**。子线开工前必须完整读一遍；与本文件冲突的任何"顺手改进"一律不做，改为在报告里提出。

---

## 0. 目标（一句话）

**Go 为主栈**：把 HUSHFUSION Forge 建成一个能自动提出、评分、记录并改进下一代电磁线圈设计的工程系统。
**Python 只做辅助**：独立数值 oracle（交叉验证）+ 出图/统计核对。Python 不再承载引擎逻辑。

Phase 0 的验收问题只有一个：

> **机器能不能找到一个比"人工设计基线"更好的电磁设计？** 并且这条结论必须能被独立复现。

---

## 1. 权威文件

| 文件 | 作用 | 谁写 |
|---|---|---|
| `internal/config/config.go` | 全部物理常数/边界/权重（唯一真源） | parent（已完成，勿改） |
| `internal/*/api.go` | **冻结接口**：类型 + 函数签名 + 语义定义 | parent（已完成，勿改签名） |
| `internal/owners/owners.go` + `owners_test.go` | 文件所有权名册 + 冻结门（互不重叠 / 无遗漏 / 变异会红） | parent（已完成，勿改） |
| `testdata/golden_*.json` | 跨语言数值锚点（Python 参考实现产出） | parent（已完成，勿改数值） |
| `python/aux/oracle.py` | **独立**数值 oracle（scipy 实现，不 import Go 的任何东西） | stage G |
| `scripts/verify.sh` | 总验收脚本 | parent |
| `docs/version-0.1.3.md` | **0.1.3 的契约**：一次真缺陷修复（v1 trace 标错 protocol ⟹ G18 假绿）+ 版本税第二笔 + 计划 v3 对齐 | parent（2026-10-02） |
| `docs/version-0.1.2.md` | **0.1.2 的判据契约**：可造性口径 · 世界语义重做方案 · 版本语义与发版模型 · 对旧证据的影响 | parent（2026-10-01） |

---

## 2. 文件所有权名册（机器可验，见 `internal/owners/owners.go`）

| Stage | 包/路径 | 所有者 |
|---|---|---|
| root | `go.mod` `LICENSE` `README.md` `README.en.md` `PLAN.md` `CONTRACT.md` `CHANGELOG.md`（2026-10-01 新增） `.gitignore` `internal/config/` `internal/owners/` `testdata/` `scripts/` `knowledge/` `artifacts/`（2026-10-07 新增：持续学习 daemon 的趋势/ckpt 产物，归 parent） | parent |
| wiki | `docs/` `manifests/` `AGENTS.md` `CLAUDE.md` `.cursorrules` `.windsurfrules` `.claude/` `.github/` | parent（2026-09-26 接入 wiki-first 知识系统时新增） |
| A | `internal/physics/` | agent-A |
| B | `internal/objective/` `internal/baseline/` | agent-B |
| C | `internal/registry/` `internal/runner/` | agent-C |
| D | `internal/search/` | agent-D |
| E | `internal/experiment/` `internal/knowledge/` `internal/report/` | agent-E |
| F | `internal/rlenv/` `cmd/` `internal/world/` | agent-F（2026-09-27 世界协议 P0：新增 `internal/world/`，与 rlenv/CLI 接线同属一轮；**合同变更**，见 `docs/world-protocol.md`） |
| G | `python/` | agent-G |
| design | `internal/design/` | design（2026-09-26 内部设计判决层：移植 ProjectionPhysics 的闭式解 + 六道门 + 上游锚点脚本，见 `docs/design-layer.md`） |

**冻结门**（`go test ./internal/owners/`）三条判据：
1. 名册互不重叠；2. 仓库内每个被跟踪文件都恰好属于一个 stage；3. 变异验证——把两个 stage 指到同一个包/同一文件必须报 overlap。
你在 **自己路径之外** 新增文件会让门变红；这是设计行为，不要绕。

---

## 3. 纪律（四件，写死）

1. **只准动分给你的路径**。提交用**显式 pathspec**：`git add internal/physics/`。
   **禁止** `git add -A` / `git add .`（会误纳入其他子线正在写的文件）。
2. **不许 push**。所有线落地、parent 复核、验收之后由 parent 统一推一次。
3. **禁"后台跑 + sleep 轮询"**。要等就前台跑一次、给足 timeout。墙钟不许烧在等待上。
4. **报告必须含"没做到/有保留"的逐条如实清单**：未做 · 半成品 · 只验了一部分 ·
   **需要主线做的事**。只报"已完成"= 把缺口留给 parent 猜。

另外：范围上限——**只交你这一段，不许顺手做下一段**。优先交付"能跑完的最小集"，
每验完一处**当场 commit 自己的 pathspec**，宁可少做几处也要交一个已落库的完成批。

---

## 4. 跨线接口纪律

- 阶段之间**只通过 `api.go` 里已冻结的类型/签名耦合**；需要新钩子时**不要改别人的文件、不要改冻结签名**，
  在报告里写"我需要主线加 X"，由 parent 汇成一条串行收口。
- **依赖注入**：`Evaluator` 依赖 `physics.Solver` 接口（不是具体类型）；`search` 依赖 `runner.Scorer` 接口
  （不是 `*Runner`）——这是为了让 D 段的单测能注入玩具 scorer、独立于物理层通过。
- 不要 import 你不该 import 的包（防止 import cycle）。依赖方向固定：

```
config <- physics <- objective <- runner <- search <- experiment <- report(,knowledge)
                       ^            ^
                  baseline      registry
                  rlenv <- runner, config, physics      cmd/forge -> 全部
```

---

## 5. 数值锚点（跨语言真值，来自 `testdata/`）

Python 参考实现（已逐条验证过解析锚点）产出的 golden 数值。**Go 实现必须复现**：

`testdata/golden_baseline.json`（人工基线 = Helmoltz 中央室 + 两个镜喉）：

| 量 | 值 |
|---|---|
| design（canonical, z 升序） | `r=[0.3,0.5,0.5,0.3] z=[-1.0,-0.25,0.25,1.0] I=[1621279.24,463222.64,463222.64,1621279.24]` |
| `cost_proxy` = `cost_ref` | `1.791703035e12` |
| `B_mid_T` | `1.000000`（由求解器定到 1.0 T） |
| `B_throat_T` | `3.536386` |
| `mirror_ratio` | `3.536386` |
| `volume_good` | `0.780886` |
| `ripple` | `0.0` |
| `B_coil_max_T` | `3.416271` |
| `min_coil_gap_m` | `0.5` |
| `z_throat_m` | `-0.9975` |
| terms | `field=0.0, mirror=0.247530, volume=0.780886, ripple=0.0, cost=1.0` |
| `score` | **`-0.2905708161`** |

复现判据：**score 与 golden 之差 < 1e-6**；各项 metrics 相对差 < 1e-6。做不到就说明物理或目标函数有偏差，不许放宽阈值改判据（改判据 = 造假）。

`testdata/golden_field_samples.json`：3 个设计 × 32 个采样点的 `Br/Bz/|B|`（含轴上点、近圈点、远场点）。Go 的 `AnalyticSolver.Magnitude` 与之相对差必须 < 1e-9。

`testdata/golden_spec.json`：`config.Spec.AsMap()` 的键集合必须与之一致（schema parity gate）。

**0.1.2 新增（可造性，见 `docs/version-0.1.2.md` §1）**：

| 量 | 值 |
|---|---|
| `spec.min_clearance` | `0.05` [m]（工程判断，非物理；硬约束：必须让人工基线仍然可行） |
| `metrics.min_clearance_m`（基线） | `0.22499999999999998`（喉部线圈 (0.30, ±1.00) 到中心元胞的距离 − t_pack/2） |
| `penalties.clearance`（基线） | `0.0` |
| `score`（基线） | **不变**：`-0.2905708161`（基线净空充足 ⟹ 新罚项为 0） |

**一处既有的微小跨语言漂移（0.1.2 复核时发现，不修）**：人工基线的 `field` 项，Go 侧给
`-3.442446546963692e-11`，Python 参考实现给 `9.64327466553287e-17` ⟹ `score` 相差 **3.442e-11**
（在 1e-06 容差内；两者都等价于「B_mid 精确等于 B_ref」）。用 `git worktree add --detach HEAD`
在 0.1.0 原树上复核过：**该差在改动之前就存在**，不是 0.1.2 引入的。记在这里，下一个人不必重新怀疑一遍。

**轴解析锚点**（不依赖 Python，必须自证）：
- 单圈轴上：`B_z(0,z) = mu0*I*a^2/(2*(a^2+z^2)^1.5)`，`B_r(0,z) = 0`（精确）
- Helmholtz 对（半径 a、间距 a、电流 I）：中心 `B = (4/5)^1.5 * mu0*I/a`；且 `|z| <= 0.1a` 内不均匀度 `< 1.2e-4`
- 独立分段求和 vs 闭式解：`nSeg=512` 时相对差 `< 1e-9`
- 真空恒等式：`div B ~ 0`、`curl B ~ 0`（中心差分，相对 `< 1e-5`）

---

## 6. 验收门（parent 亲自跑，不采信自述）

**权威是 `scripts/verify.sh`，不是本表**：本表是它的镜像，改门必须同时改这里。

**0.1.2 起新增一条前置条件：判据版本**（0.1.3 继承）。 带 tag 的门里 G12/G15/G16 依赖「同一个设计在同一个判据下的分数」，
所以它们额外要求 `runs/<tag>/results.json` 的 `meta.forge_version` == `internal/config.ForgeVersion`。
判据变了（PATCH）而 run 是旧的 ⟹ 这三道门 **SKIP**，并在抬头大声说明：拿新尺子量旧证据是**用错了尺子**，
不是旧 run 坏了。`runs/phase0` 是 **0.1.0 判据下的证据**，永不重跑覆盖；`scripts/verify.sh` 的默认 tag 从
`phase0` 改成 `phase1`（默认 tag 应当指向当前判据的证据）。`$TAG` 是运行标签
（如 `phase0`）。`_opt` 类门在缺少前置产物时**跳过**（`runs/$TAG` 不存在、上游目录不在），
汇总行会分开报「通过 / 失败 / 跳过」——**跳过不等于通过**。

| 门 | 命令 | 判据 |
|---|---|---|
| G1 构建 | `go build ./...` | 编译干净 |
| G2 静态检查 | `go vet ./...` | 无警告 |
| G3 gofmt 干净 | `test -z "$(gofmt -l .)"` | 格式化是契约 |
| G4 所有权冻结门 | `go test ./internal/owners/` | 无文件重叠、无未归口文件；该门自身经过变异测试 |
| G5 单元测试 | `go test ./...` | 全绿（含解析锚点：轴上闭式解 / Helmholtz 幅值与均匀度 / 离散-解析 / div-curl） |
| G6 基线在搜索盒内 | `go test ./internal/baseline/ -run TestBaselineInsideSearchBox` | 人工基线解码后**不被裁剪**（否则人机对比比的不是同一个设计） |
| G7 无伪造策略（反造假门） | `go test ./internal/rlenv/ -run TestNoLearnedPolicy` | 未实现的策略必须显式报错，不许返回编造数值 |
| G8 python oracle 对 golden | `python3 python/aux/oracle.py --check-golden testdata/` | 独立 oracle（不 import Go）复现 Go 引擎 |
| G9 forge verify（端到端） | `go run ./cmd/forge verify` | CLI 端到端 |
| G10 跨语言场一致性 | `forge xcheck` + `oracle.py --compare-go --proximity-floor 0.005` | 带 5 mm 钳位开关（约定不一致会稳定复现 4.05e-02） |
| G11 注册表完整性 ($TAG) | `forge registry --check --registry runs/$TAG/registry.jsonl` | id 连续、parent 存在、必填字段齐全 |
| G12 go/python schema 一致 ($TAG) | `python3 python/aux/schema_check.py runs/$TAG/registry.jsonl` | Go 记录与冻结 schema 一致（`^D(\d{4,})$`） |
| G13 python 辅助层测试 | `python3 -m pytest python/tests -q` | 全绿 |
| G14 规则对账（scipy） | `python3 python/aux/rules_check.py --rules knowledge/design_rules.md --registry runs/$TAG/registry.jsonl` | 规则表与实际记录对账 |
| G15 出图 + 独立复评最优/基线 ($TAG) | `python3 python/aux/analyze.py runs/$TAG` | 独立复评最优与基线 |
| G16 逐位复现 ($TAG) | `python3 scripts/repro_check.py $TAG` | `registry.jsonl` 剔 `tag`/`timestamp` 后 sha256 完全相同 |
| **G23 可造性门**（0.1.2） | `python3 scripts/check_buildability.py` | 三条一起判：(a) 人工基线可行且 `clearance` 罚项**精确**为 0（抓「把真装置也判死」）；(b) 0.1.0 的最优退化解在 0.1.2 下**不可行**且罚项 > 0（抓「约束没咬住它要咬的东西」——这是本版存在的全部理由）；(c) 净空的闭式解 == 圆环面密采样的独立路径（< 1e-9），且侵入时精确 = −t_pack/2（抓「几何算错」）。它是**判据门**，不依赖某个 run，故不受版本前置条件约束 |
| G17 内部设计锚点门 | `python3 scripts/emit_pp_anchors.py --check && go test ./internal/design/` | 上游 ProjectionPhysics（`logos-42/Hibs-Physics`）的闭式解逐条复现；锚点来自工作区重跑产物（上游 `artifacts/` 不入 git），出处见 `testdata/projectionphysics_anchors.json` 的 `provenance` |
| G18 世界协议门（三条一起判） | `forge world serve --replay testdata/world_trace_golden.jsonl` + `python3 python/aux/world_client.py --replay testdata/world_trace_golden.jsonl` + 故意改坏的 hello 必须让客户端非零退出 | (a) Go 把 trace 的每条 req 喂给新世界，响应逐字节相同；(b) Python 客户端同样逐字节相同（**跨语言证据**：证明客户端看到的世界与 Go 写下的是同一个）；(c) 反向断言：改坏的 hello 必须让客户端非零退出且理由是握手。证据 = `testdata/world_trace_golden.jsonl`（由 `--record` 生成；trace 的 `runs/` 目录不入仓，契约 §4） |

---

## 7. 报告格式（子线交回时用）

```
stage: <A..G>
状态: 完成 / 部分完成 / 阻塞
已实现: <文件:函数 列表>
已自验: <命令 + 真实输出摘要>
未做 / 半成品: <逐条>
只验了一部分: <哪一部分>
需要主线做的事: <钩子/接口/决策>
与合同不符之处: <无 或 逐条说明>
```

---

## 8. 已知的跨阶段依赖（不必自行解决，报告即可）

- E 段的端到端 benchmark 需要 A/B/C/D 均已落地；E 段先把**纯函数**（Aggregate/ApplyVariant/markdown 渲染/JSON round-trip/Spearman）验绿，
  端到端由 parent 在收口后跑。
- F 段的 CLI 依赖全部包；允许先 `go build ./cmd/forge` 通过即算完成编译级验证，端到端由 parent 跑。
- G 段的 oracle 必须**独立实现**，不许 import `internal/`，也不许调用 Go 二进制来"验证" Go。

---

## 9. 主线裁决（2026-09-26 收口轮，全部有实测支撑）

| 议题 | 裁决 | 证据 |
|---|---|---|
| `registry.AppendAssign`（C 段新增的导出符号） | **批准保留**。冻结签名 `Append(Record) error` 按值传参、拿不回它分配的 id，而 `NextIDs()+Append()` 是真竞态；搜索层要按 DesignID 建谱系，必须拿到**已落盘那条记录**的 id。已核实：无冻结签名/类型/JSON tag 被改，`Append` 委托给它，写入路径只有一条 | `go test -race ./internal/registry/`（100 goroutine 无缺口）；`runs/phase0/registry.jsonl` 经 `registry --check` 与 `schema_check.py` 双门 PASS |
| `internal/rlenv` 的 `ObsMetricValue`（F 段新增导出） | **批准保留**（观测槽取值的单一映射点；未知键 panic） | G7 反造假门 + `forge verify` 的 rlenv 槽位检查 |
| 近导线 5 mm 的 `alpha2` 钳位语义 | **冻结为官方定义**：对**任意**采样点距导线 < 5e-3 m 即钳位。跨语言比场必须带 `--proximity-floor 0.005` | 带钳位：Go 与 scipy 差 1.7e-15；不带：稳定复现 4.05e-02（同一最优设计，反事实对照）。`verify.sh` G10 已写死该开关 |
| `search.WarmStartDesign` 用字面量而非 import `internal/baseline` | **保留现状**：依赖箭头里没有 search→baseline；该字面量被 `testdata/golden_baseline.json` 硬校验（最大相对差 7.9e-11） | D 段隔离副本旁路核对 |
| `internal/objective` 让 NaN 传播（不夹成有限值） | **保留**：物理层吐 NaN 时，分数就该是 NaN 而不是一个假的有限值 | B 段单测；`feasible` 判定与 Python `all(v<=0)` 对齐 |
| `internal/rlenv/api.go` 的私有默认值改为**引用** `config.DefaultMaxSteps / DefaultDeltaScale` | **批准**：房规说「影响分数的数字只有一个家」，两份字面量 `20 / 0.15` 正是它禁止的东西。冻结面是**签名 / 类型 / JSON tag 与 golden 数值**，私有常量的初始值不在其中（签名与类型逐字节未动） | `go test ./internal/rlenv/ ./internal/world/` 全绿；`world_test.TestDefaultsComeFromConfig` 改为盯**接线**（零值是否走到 config）而不是「两个数字碰巧相等」 |
| `owners.IsTracked` 跳过 `.kilo/` `.kilocode/` | **批准**：这道门叫 `TestRosterCoversEveryTrackedFile`，而它按文件系统遍历时会看见**别的 agent 工具留在仓里的 worktree 检出**（未跟踪、不受本仓名册管辖）——判它会长期假红，**假红比没有门更伤**。同次加了两侧断言测试 | `go test ./internal/owners/`（新增 `TestIsTrackedSkipsForeignCheckoutsButNotSources`：跳过项必须被跳过，**且真文件必须仍然可见**）；G4/G5 从红转绿，名册自报 `120 files across 10 stages` |
| 契约 §6 关于黄金 trace 覆盖范围的措辞 | **写准，不假装覆盖**：trace 只走 5 步而 `max_steps=20`，`truncated` 恒为 `false`。`truncated=true` 与「done 之后再 step」由 `internal/world` 单测盯住，**不**由这份跨语言 trace 盯住 | `docs/world-protocol.md` §6 已写明覆盖/不覆盖两侧；trace 尺度锚点（8 行 / 响应 3635 字节）写进契约便于漂移可见 |
| 世界协议新增导出符号（`internal/world` 一整套：`ProtocolVersion`、冻结错误码、`Server/Replay/Recorder/TraceLine/SpecSHA256`…） | **批准保留**：它们是协议层的冻结面本身（跨语言对账需要 `SpecSHA256` 这类单一实现点）。已核实 `internal/rlenv/api.go` 的签名/类型/JSON tag 与 `testdata/golden_*.json` 数值未被改动 | G18 三条门；`git show --stat` 可验范围 |
| `internal/mu/`（新包：flatten / Q_A / 交换子 / 区域族分类 / μ 状态方程 / 增益）归 F 段 | **批准**：它是 `--world mu` 的语义底座，与 `internal/world/` 同一段工作；名册非重叠原则不受影响（`internal/mu/` 此前无人认领）。同一次把 `internal/mu/` 写进 `owners.go` 的 F 段路径 | `go test ./internal/owners/`（新路径入册后 G4 自报覆盖文件数）；`emit_mu_anchors.py --check` 锚点来自上游 `artifacts/{mudynamics,gravitycontrol}/report.json` |
| Python 回放的判据从**字符级**改成**字节级** | **批准（真缺陷修复）**：Go 侧一直按字节比（`bytes.Equal`），Python 侧却把世界 stdout 解码成 `str` 再比 —— 原 UTF-8 与 `\uXXXX` 转义两种写法**字符相同、字节不同**，那是契约 §2 唯一要抓的东西。修前 Python 数出 10048「bytes」（实为字符），与 Go 的 10048 不一致，两个数本身就报警了 | 修后三份 trace 两侧字节数一致（3635/7199/10048）；冒充判据：把 mu trace 里的 `−` 换成 `\u2212`（字符相同、字节不同）→ **Go 与 Python 都 FAIL**（修前 Python 会假绿） |
| `baseline.TextbookMirror` 解出的电流越界即 error（不返回被裁剪的基线） | **保留**：否则"人机对比"比的不是同一个设计 | G6 |
| `golden_*.json` 里 bool/int 被写成 JSON 浮点（`"n_coils": 4.0`） | **暂不迁移**：三个阶段各用 `map[string]float64` 兜住，迁移的收益不抵最后一刻动真值的风险。列入 Phase 2 的 schema 迁移 | 现存 goldens 由 `oracle.py --check-golden` 全绿复核 |
| `rlenv` 观测槽 6（`cost_proxy`/1.0 ≈ 1.8e12）量纲极差 | **记入 Phase 1 前置**：改为按基线 cost 归一，需同步改冻结的 `ObsMetricRefs` | 见 PLAN.md Phase 1 清单 |
| 报告 §4 设计谱系偏薄（`registry.BranchImprovement()` 未进报告） | **记入 Phase 1**：报告 schema 冻结，加字段需同步 schema_check 与报告测试 | `runs/phase0/report.md` §4 |

---

## 10. 主线裁决（2026-10-01，0.1.2 收口轮——可造性进判据 + 版本模型）

| 议题 | 裁决 | 证据 |
|---|---|---|
| `internal/physics.Metrics` 新增 `min_clearance_m`（**冻结类型变更**） | **批准**：它是「可造性」唯一的原始量，而房规要求每个影响分数的量都原样落盘（否则审阅者只看到一个罚项数字，无法独立复算）。同步改了 Python 参考实现、`schema_check.py` 的冻结键集、`main.go` 的 `metricKeys`、registry 单测，以及 `testdata/golden_baseline.json`（**文本级最小插入**，保持该文件既有的「bool/int 写成浮点」格式——整文件重出会改类型并让 Go 侧读不进来，实测踩过一次） | 13 个 metrics 键；`forge verify` 10/10；golden 里 `coil_proximity_floor_hit` 仍是 `0.0` |
| `internal/objective` 新增 `clearance` 罚项（`penaltyOrder` **追加在末尾**） | **批准**：追加而非插入，使既有三条的累加顺序逐位不变（`score == Σweighted − w·Σpenalties` 的可审计性是硬要求）；形状与既有三条同族 | G23b：0.1.0 最优 D9857 的分数 1.3814186 → **−13.6099621**（罚项 1.5，净空 −0.025 m） |
| `config.ForgeVersion` 作为版本**唯一真源**（`cmd/forge.Version` 只是别名） | **批准**：run 的 meta 必须记住产出它的判据版本，而写入方在 `internal/experiment`——版本若住在 `cmd/`，`experiment` 就要反向依赖 CLI。`runs/<tag>` 之间的可比性由这一个常量定义 | `forge version` = 0.1.2；`phase1/results.json` 的 `meta.forge_version = "0.1.2"` |
| `objective_test` 手工构造的 metrics fixture 补 `MinClearanceM` | **批准（必须）**：golden 加载器若不读新键，`min_clearance_m` 就是 0 ⟹ 罚项凭空变成 1 ⟹ 分数差 −10——那是**测试的错**，不是实现的错 | `TestGoldenScoreReproducedFromFrozenMetrics` 复现冻结分数到 1e-12 |
| `python/tests/test_analyze.py` 的 fixture 获胜设计换新 | **批准**：旧 fixture 是 0.1.0 的获胜设计，其中一圈 `r=0.10, z=0.04` 的导体落在约束区域**内部**（净空 −0.025）⟹ 在 0.1.2 下被罚 −15，不再是「能赢过基线的那个」。换成本版真实获胜设计 D7996 并写明原因 | `pytest python/tests -q` 103 passed |
| 0.1.2 的消融轴 = **版本边界**（不额外加 `--clearance=false` 开关） | **批准**：同一元组（budget 1000 × seeds 0,1,2 × 4 方法）只有判据不同，两侧都被归档、都可逐位复现——版本号本身就是那个开关。加一个开关等于让冻结的 CLI 面变宽，收益不抵 | `runs/phase0`（0.1.0）：Δ = **+1.672**；`runs/phase1`（0.1.2）：Δ = **+1.2395**；两版都逐位复现 |
