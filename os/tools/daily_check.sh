#!/usr/bin/env bash
# os/tools/daily_check.sh —— HUSHFUSION OS 日常检查（每天一次，~2 秒）
#
# 干三件事（全部复用已有门，不新建平台）：
#   1. 门禁能编译 + 名册门绿（最快的早暴露：有没有把仓搞坏）
#   2. wiki 三连（push 前必跑的那套）
#   3. 脏树提醒（leo 常与并行智能体编辑同一批文件，先查 git status 防覆盖）
#
# 退出码：全部通过 → 0；任一红 → 1。wiki 三连红 = 预发布门（不是门禁红，也报）。
set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

export PATH="$HOME/.local/bin:$PATH" 2>/dev/null || true
export GOTOOLCHAIN=local 2>/dev/null || true

fail=0
warn=0

echo "==[1/3] Go 编译 + 名册门 =="
# 注意: 仓根有 forge/ (Python 包) 目录, go build ./cmd/forge/ 会报 "build output forge
# already exists" —— 一律 -o /tmp 构建（见 skill hushfusion-forge-development 坑 #30）。
if out=$(go build -o /tmp/forge_dailycheck ./cmd/forge/ 2>&1); then
    echo "  build: ok"
else
    echo "  build: FAIL"
    printf '%s\n' "$out" | head -5
    fail=1
fi
if go test ./internal/owners/ -count=1 >/dev/null 2>&1; then
    echo "  owners roster: ok"
else
    echo "  owners roster: FAIL"
    fail=1
fi

echo "==[2/3] wiki 三连（预发布门）=="
if python3 scripts/wiki_check.py >/dev/null 2>&1; then
    echo "  wiki_check: ok"
else
    echo "  wiki_check: FAIL"
    warn=1
fi
if python3 scripts/raw_manifest_check.py >/dev/null 2>&1; then
    echo "  raw_manifest_check: ok"
else
    echo "  raw_manifest_check: FAIL"
    warn=1
fi
if python3 scripts/wiki_lint.py --strict=v2 >/dev/null 2>&1; then
    echo "  wiki_lint --strict=v2: ok"
else
    echo "  wiki_lint --strict=v2: FAIL"
    warn=1
fi

echo "==[3/3] 脏树提醒 =="
if [ -n "$(git status --porcelain)" ]; then
    echo "  working tree NOT clean:"
    git status --porcelain | head -10
    warn=1
else
    echo "  working tree clean"
fi

echo
if [ "$fail" -ne 0 ]; then
    echo "DAILY CHECK: FAIL (门禁红，先修)"
    exit 1
elif [ "$warn" -ne 0 ]; then
    echo "DAILY CHECK: WARN (wiki/脏树待处理，可提交前处理)"
    exit 1
fi
echo "DAILY CHECK: OK"
exit 0