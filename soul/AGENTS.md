# AGENTS — Forge 协作规约

本文件是 Forge 在本仓库内的协作规约，与仓库根的 `AGENTS.md` 配套（根文件是仓库全局规则，本文件是 Forge 自身的补充约定）。

## 1. 每次新会话默认动作

只要任务不是纯闲聊，默认先：

1. `python3 scripts/version_check.py`（有新版才提示，没有就静默）
2. 读 `docs/wiki/index.md`、`docs/wiki/current-status.md`、`docs/wiki/log.md`
3. 查 `manifests/raw_sources.csv` 后再使用任何非代码文件（PDF/Excel/截图/附件等）
4. 不在上面 → 先登记（少量手填，大量跑 `python3 scripts/ingest_raw.py`）

## 2. 默认范式

- `compile-first`、`writeback` 必做；中等规模先 `wiki`，不先上重 RAG
- `Idea / Intent` 优先于 `Code`；只改代码不回写 wiki 算没做完
- 知识分层：raw（原始资料）→ wiki（编译共识）→ code（执行层）

## 3. 一致性规则

- `current-status.md` 与其他 wiki 冲突 → 以更具体页面为准，再修正 current-status
- `log.md` 缺历史 → 不猜，只追加自己的
- 两个 wiki 页面矛盾 → 标记给用户，解决后再继续

## 4. 所有权纪律（来自 CONTRACT.md）

- 只准动分给我的路径；提交用显式 pathspec，禁止 `git add -A` / `git add .`
- 不许 push；由 parent 统一推
- 在路径之外新增文件会让 owners 名册门变红——这是设计行为，不要绕
- 合同变更（新增路径/改名册）必须同步 CONTRACT.md

## 5. 记忆纪律

- 跨会话事实存 `mem.set`（写入全局记忆文件），不把秘密存进记忆
- 记忆与人格文件分开：`soul/` 放人格与规约，记忆走 agent 记忆系统

## 6. 协作

- 与其它智能体讨论走内部商议频道（room_post / room_read）
- 内部商议不能替代给用户的最终回复
