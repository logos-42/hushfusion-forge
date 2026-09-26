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
| 2026-09-26 | 首推 GitHub | 仓库上线 (public, master) | `gh repo create logos-42/hushfusion-forge --public` + SSH 推送。HTTPS 走本机代理 (`http.proxy=127.0.0.1:7890`) 传 pack 时两次 `send-pack: unexpected disconnect`，改用 SSH (`~/.ssh/github_key`) 一次通过 | 远端 master == 本地 cccf900；139 个文件在树上 |
| 2026-09-26 | 真实缺陷 3 | CI 首推即红（第 3 步 untracked_raw_check） | 检查器扫描全仓，把**流水线生成的** `runs/phase0/figures/*.png` 当成"未登记的外部原件"要求登记进 manifest。混为一谈会让 manifest 失去意义（它回答的是"这份东西从哪来"）。修为：内置生成物目录集 + 新增 `manifests/.rawcheckignore` 声明式排除（并在输出里说明该写进 ignore 还是 manifest） | 双向验证：修后绿；`test_raw_probe.png` / `notignored/x.png` 仍红（排除规则不能掩盖真原件） |
| 2026-09-26 | 真实缺陷 4 | provenance 门红（CI 首推没跑到，本地补跑暴露） | 4 个 wiki 页写了 schema 里不存在的 `source: repo`。`docs/wiki/SCHEMA.md` 只允许 raw 路径 / URL / `session` 三种；`provenance_check` 于是要求它们提供 `source_hash`（源码来自外部时才成立）。这 4 页的信息确实来自本 session 与仓库自身，改为 `source: session` | `provenance_check --ci` → OK (5 session-exempt)；`wiki_lint --strict=v2` 仍绿 |
