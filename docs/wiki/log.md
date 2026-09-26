# Wiki 日志

| 日期 | 主题 | 子题 | 变更 | 证据 / 门 |
|---|---|---|---|---|
| 2026-09-26 | 启动 | 初始化知识系统 | 建立 wiki / manifest / 21 个校验脚本 / repo 级规则（AGENTS.md、CLAUDE.md、.cursorrules、.windsurfrules） | `wiki_check.py`、`raw_manifest_check.py`、`wiki_lint.py --strict=v2` |
| 2026-09-26 | Phase 0 验收 | 人机对比闭环跑通 | Go 主栈 12 个包 + CLI 全部实装；phase0 跑出 12001 条记录（4 方法 × 3 seed × 1000 评估），机器最优 1.381419 vs 人工基线 −0.290571（Δ +1.672），4 方法 3/3 赢 | `runs/phase0/{registry.jsonl,results.json,report.md}`；`bash scripts/verify.sh phase0` 15/15 PASS |
| 2026-09-26 | 真实缺陷 1 | 跨语言"分歧"实为钳位约定 | 独立 oracle 复评最优设计时 B_mid 差 4.05e-02；查明是 Go 对距导线 5 mm 内的采样点钳住 alpha2 而精确闭式解不钳。带同一半径复算后差 **1.7e-15**；不带则稳定复现 4.05e-02（反事实对照） | G10 写死 `--proximity-floor 0.005`；`analyze.py --proximity-floor`（默认 0.005）；PLAN.md §3"Phase 0 的一个真实发现" |
| 2026-09-26 | 真实缺陷 2 | schema 门在规模上失效 | `schema_check.py` 的 design_id 正则要求**恰好 4 位**，在 12001 条记录上产生 2002 个假错误（`%04d` 是最小宽度，D10000 合法）。修为 `{4,}` 并加"必须是 experiment_id 的规范形式"精确判定 | 新增两个回归测试（10001 条全绿 + `D00001` 必须红）；对真实 registry 现为 `0 error(s)` |
| 2026-09-26 | 技能缺陷 | 维基 bootstrap 中止 | `bootstrap_knowledge_system.py` 未定义 v2 三个哨兵（`__LAST_CONFIRMED__` / `__AUDIENCE__` / `__STAGE__`），4 个模板渲染即抛错、整体中止不落盘。补哨兵 + `import os`，默认 `stage=current` / `audience=self`（与 `wiki_lint --strict=v2` 的枚举一致） | 见 skill「维基 llm」的故障排查节；`wiki_lint.py --strict=v2` 通过 |
| 2026-09-26 | 注释中文化 | 代码注释与文档统一中文 | 全部 Go 包 + Python 辅助层的注释/docstring 改中文（保留英文技术术语与标识符）；三处冻结注释缺陷同时修正（B_r 的 1/r、B_coil_max 的模之和语义、钳位半径适用范围） | `gofmt`/`go vet`/`go test ./...` 全绿；70 pytest 全绿；15 门复跑全绿 |
| 2026-09-26 | 新增 G16 复现门 | 把一次性核对变成常设判据 | 中文化之后独立复核：按 `runs/phase0/results.json` 的 meta 原参数重跑，12001 条记录剔除 `tag`/`timestamp` 后 sha256 完全相同（`ebeb3cd9…`），`results.json` 去 meta 后整体相同——**证明注释轮没有动任何行为**。该核对固化为 `scripts/repro_check.py` + 门 G16 | `bash scripts/verify.sh phase0` → 16/16 PASS |
