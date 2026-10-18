# CURRENT_STATE —— 现在是什么状态

> 五个问题逐一回答。**证据数字的唯一权威是 `docs/wiki/current-status.md`（阶段现状）与 `runs/<tag>/`（原始证据）**，
> 本页只做决策层的摘要，不重复证据、不自行发明数字。冲突时以更具体的页面为准，并修正本页。
> 创建：2026-10-17 ｜ 最后确认：2026-10-17

## 1. 现在是什么状态？

- Forge v0.2 判据（0.1.2 起：可造性 `clearance` 进目标函数）。Phase 0 验收完成：机器最优 D9857 score 1.381419 vs 人工基线 −0.290571（Δ +1.672，旧判据）；Phase 1（0.1.2 判据）最优 0.9488837（Δ +1.2395），可行记录 7221/12001。
- 22 道门：21 绿 / 1 红（**G19，红为设计所许**）——v2 世界弱档，P2 不启动；B 项方案已预注册（`docs/version-0.1.2.md` §2）。
- 持续学习（远端守护）：v5 渐近平台 1.0655 → v6 判据探索突破 **1.076974**（mc=0.02 + ek）。分形结构优势已诚实收口（无统计优势）。
- 设计侧：双流环引力（η=1.000000 精确抵消理论点；FRC 几何 η=−20.53 **方向性否定**）、轴向分离双流环（磁位形，非目标）——都停在「设计与验收依据」，未接进判决层（需契约变更）。
- **尚未验证的科学主张**（诚实清单）：μ 的主动产生是上游缺口（本仓只算「给定 μ 能维持多少步」）；真空静磁模型≠聚变装置；「知识播种增益」在测量饱和下测不出（不是证明无用）。

## 2. 下一步是什么？（唯一主任务）

**T-01：G19 世界语义重做（B 项）——把 v2 世界的动力学接到 `internal/design` 的 L3 判决。**
判据、死法、阈值纪律已预注册（`docs/version-0.1.2.md` §2）；验收标准见 `os/acceptance/t01-g19.md`。
任务池全表见 `os/TASKS.md`。

## 3. 怎么证明完成？

- 验收标准 = `os/acceptance/t01-g19.md`（三子门 + 死法 + 不许项）。
- 通用铁律：**Agent 说完成 ≠ 完成**。状态转换必须附证据路径（runs/ 产物、门日志、验证命令输出），且至少一条检查不依赖被测对象自己。

## 4. 什么能自动完成？

| 自动化 | 命令/位置 | 状态 |
|:--|:--|:--|
| 全门验收 | `bash scripts/verify.sh phase0`（22+ 门，含 G24） | 可跑，G19 预期红 |
| kernel bridge | G24（真 Node 进程 + 真 @bolloon/bolloon-kernel，七条） | 真跑（2026-10-12 起） |
| wiki 三连 | `wiki_check.py` / `raw_manifest_check.py` / `wiki_lint.py --strict=v2` | 可跑（push 前必跑） |
| 跨境回放 | G18/G20/G22（trace 逐字节） | 可跑 |
| 远端守护 | `scripts/oml_supervisor_{v5,v6,fractal}.sh`（容器 24h 自愈） | 运行中（远端） |
| 日常检查 | `bash os/tools/daily_check.sh`（编译+名册+wiki 三连+脏树提醒） | **v0.1 新增** |
| 周复盘草稿 | `bash os/tools/weekly_review.sh`（收集本周证据→生成复盘初稿） | **v0.1 新增** |
| headless 注入 | 预留槽位 = kernel `model.acquire` 租约（T-08，**不实现**） | proposed |
| OS↔kernel 双向 | `forge os <submit\|peek\|sync>`（os/SPEC.md §8.3），证据落盘 `os/evidence/`（gitignore） | **已真跑**（2026-10-18 sync 核对通过） |
| AI Provider 接入 | `forge ai <list\|chat\|models>`（docs/wiki/ai-provider-design.md），多 provider 声明式接入 | **已真跑**（2026-10-18 zai 经 goaichat 代理回「通了」） |

## 5. 系统如何变好？

- 每次执行后，把「下次别踩」的经验写回 skill（`hushfusion-forge-development`）或本页「经验」节。
- 每周六复盘（`os/WEEKLY_REVIEW.md`）：链路中哪一环最耗时？最大的不确定性是什么？下周唯一主任务是什么？

## 经验（每次闭环追加，从老到新）

1. （2026-10-17）搭建 OS 决策层本身不产生物理证据——它只有让「跑门、落证据、写回 wiki」更快才算有价值。首批自动化全部复用已有脚本，不新建平台。