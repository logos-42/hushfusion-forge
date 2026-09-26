---
title: GitHub 与 Raw 仓分工策略
source: repo
created: 2026-09-26
last_confirmed: 2026-09-26
audience: self
stage: current
schema_version: 2.1
tags: [strategy, git]
---

# GitHub 与 Raw 仓分工策略

## 结论

- **GitHub `logos-42/hushfusion-forge`：public + `master` 分支**，中英 README 互链。
  放：代码 + wiki + manifest + 冻结真值 + 每次 run 的证据。
- **本地 raw 仓**（`/Users/apple/Downloads/hushfusion_forge_raw/`）：放 PDF / 图片 / 外部资料原件。
- **不放**：编译产物、`runs/scratch/`、任何凭据。

## 进 git 的具体判定

| 路径 | 进？ | 理由 |
|---|---|---|
| `internal/` `cmd/` `python/` `scripts/` `testdata/` | 是 | 代码、门禁、冻结真值 |
| `docs/wiki/` `manifests/` `AGENTS.md` `CLAUDE.md` `.cursorrules` `.windsurfrules` | 是 | 知识系统本体（schema 是产品） |
| `runs/phase0/` 全量（含 14 MB `registry.jsonl`） | 是 | **这是 Phase 0 唯一的原始证据**：12001 条记录、每条带全部 term 与谱系。删掉它，"机器赢了"就只剩一句话 |
| `runs/scratch/` | 否（.gitignore） | 开发期临时台架，无长期价值 |
| `raw/` `raw_local/` `raw_vault/` `.obsidian/` | 否（.gitignore） | 原件与编辑器状态 |
| 二进制 / `forge` 可执行文件 / `bin/` | 否（.gitignore） | 一律 `go run ./cmd/forge` |

## 为什么把 14 MB 的 JSONL 也提交（而不是只交聚合表）

聚合表（`results.json` 的 aggregate 块）是**结论**；registry 是**证据**。本仓的立场是：
结论必须能被人从证据独立重算出来。`python3 python/aux/schema_check.py runs/phase0/registry.jsonl`
与 `python3 python/aux/analyze.py runs/phase0` 就是给外部复核者准备的两条命令，
它们只需要这个文件 + 代码。用 14 MB 换"任何人都能复核"，值。

若将来预算涨到十万级评估，再改为压缩归档 + 摘要（届时 `analyze.py` 仍能从聚合块复核）。

## 提交纪律

- 只 `git add` 本次文件，分阶段提交，**禁止 `git add -A`**（本仓曾被并行施工的同名文件误纳入）。
- 有门禁的改动，先跑 `bash scripts/verify.sh phase0` 再提交。
- 知识系统的改动必须同时过 `wiki_check.py` + `wiki_lint.py --strict=v2` + `raw_manifest_check.py`，
  并在 [log.md](./log.md) 追加条目。
- 不伪造：任何提交信息里的数字必须能在 `runs/` 里找到出处。
