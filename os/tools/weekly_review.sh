#!/usr/bin/env bash
# os/tools/weekly_review.sh —— 生成本周复盘初稿（每周六跑一次）
#
# 收集三类证据，写进 os/WEEKLY_REVIEW.md 的「本周复盘」节（生成版，leo 确认真实状态后回写）：
#   1. 本周新增/修改的 runs/ 证据目录（git log 本周 + runs 目录 mtime）
#   2. 本周实验记录（os/EXPERIMENTS.md 有变化的行）
#   3. 本周提交（git log --since="7 days ago"）
# 只生成初稿，不覆盖已确认的复盘；输出到 STDOUT 并写 os/WEEKLY_REVIEW.draft.md。
set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

SINCE="7 days ago"
DRAFT="os/WEEKLY_REVIEW.draft.md"
TODAY="$(date +%Y-%m-%d)"

{
    echo "# WEEKLY_REVIEW 本周复盘初稿（自动生成 $TODAY，待 leo 确认）"
    echo
    echo "## 本周提交（git log --since=\"$SINCE\"）"
    git log --since="$SINCE" --oneline --no-decorate 2>/dev/null | head -40 || echo "（无提交）"
    echo
    echo "## 本周证据目录（runs/ 最近 7 天改动）"
    find runs -maxdepth 2 -type d -mtime -7 2>/dev/null | sort || echo "（无）"
    echo
    echo "## 本周实验/任务状态变化（os/EXPERIMENTS.md + os/TASKS.md 的 mtime）"
    stat -f "%Sm %N" -t "%Y-%m-%d %H:%M" os/EXPERIMENTS.md os/TASKS.md 2>/dev/null || echo "（无）"
    echo
    echo "## 四问（待 leo 填）"
    echo "1. 四大系统状态："
    echo "2. 最耗时/最阻塞环节："
    echo "3. 最大不确定性："
    echo "4. 下周唯一主任务："
} > "$DRAFT"

echo "草稿已生成：$DRAFT"
echo "确认后把它合并进 os/WEEKLY_REVIEW.md（不要覆盖已确认的复盘）。"
exit 0