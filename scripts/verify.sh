#!/usr/bin/env bash
# Forge 验收门禁。
#
# 每一道门的判据就是它所跑命令的退出码。**不可变红的门不是门**，所以每道门旁边都
# 写清它负责抓什么失败模式；没有任何一道门是"作者说它能跑"。
#
# 用法:  bash scripts/verify.sh [tag]
set -uo pipefail

export PATH="$HOME/.local/bin:$PATH"
export GOTOOLCHAIN=local
REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO" || exit 2
TAG="${1:-phase0}"

pass=0
fail=0
skip=0
failed_names=()

_run() {
  local name="$1"; shift
  local out
  out="$(mktemp -t forgegate)"
  if "$@" >"$out" 2>&1; then
    printf 'PASS  %s\n' "$name"
    pass=$((pass + 1))
  else
    printf 'FAIL  %s\n' "$name"
    sed 's/^/        /' "$out" | tail -14
    failed_names+=("$name")
    fail=$((fail + 1))
  fi
  rm -f "$out"
}

# 前置条件不存在的门会被大声 SKIP，绝不静默通过。
_opt() {
  local name="$1" precond="$2"; shift 2
  if eval "$precond" >/dev/null 2>&1; then
    _run "$name" "$@"
  else
    printf 'SKIP  %s   (precondition: %s)\n' "$name" "$precond"
    skip=$((skip + 1))
  fi
}

echo "=== Forge 门禁 (tag=$TAG) ==="

# G1-G2: 能编译，且静态分析器没有意见。
_run "G1  构建" go build ./...
_run "G2  静态检查" go vet ./...

# G3: 格式化是契约，不是个人偏好。
_run "G3  gofmt 干净" bash -c 'test -z "$(gofmt -l . 2>/dev/null | grep -v "^$")"'

# G4: 所有权冻结门 —— 无重叠、无未归口文件，且这道门自身经过变异测试
#     （重复的名册条目必须被报出来）。
_run "G4  所有权冻结门" go test ./internal/owners/

# G5: 全部单元测试，含解析锚点（轴上闭式解、Helmholtz 幅值/均匀度、
#     离散-解析互验、div/curl）以及跨语言 golden 比对。
_run "G5  单元测试" go test ./...

# G6: 人工基线必须落在搜索盒**内部**。若解码时被裁剪，"机器打败人类"比的
#     就是一个谁都没提过的设计。
_run "G6  基线在搜索盒内" go test ./internal/baseline/ -run TestBaselineInsideSearchBox

# G7: RL 环境不得假装自己有学到的策略。
_run "G7  无伪造策略（反造假门）" go test ./internal/rlenv/ -run TestNoLearnedPolicy

# G8: 独立的 Python oracle（scipy，不 import 任何 Go 代码）必须复现 Go 引擎
#     用来标定自己的那批冻结 golden 数字。
_opt "G8  python oracle 对 golden" '[ -f python/aux/oracle.py ]' \
  python3 python/aux/oracle.py --check-golden testdata/

# G9: CLI 端到端集成：基线 -> 评估 -> 与 testdata/golden_baseline.json 比对
#     （分数 1e-6 内）-> 注册表往返。
_opt "G9  forge verify（端到端）" '[ -f cmd/forge/main.go ]' \
  go run ./cmd/forge verify

# G10: 跨语言场一致性 —— Go 导出自己的场采样，scipy 独立重算，两者差 < 1e-9。
#      **必须带 --proximity-floor 0.005**: Go 求解器对任何距离导线 5 mm 以内的
#      采样点都钳住 alpha2，而精确闭式解不钳。不加这个开关，任何把线圈摆到采样
#      点毫米级的设计都会产生量级 1e-2 的"分歧"——那是约定不一致，不是 bug。
_opt "G10 跨语言场一致性" '[ -f cmd/forge/main.go ] && [ -f python/aux/oracle.py ]' \
  bash -c 'set -e; tmp=$(mktemp -d); go run ./cmd/forge xcheck --out "$tmp/go_xcheck.json" >/dev/null; python3 python/aux/oracle.py --compare-go "$tmp/go_xcheck.json" --proximity-floor 0.005'

# G11: 产出的 run 的注册表完整性（id 连续、父设计存在、必需字段齐全）。
_opt "G11 注册表完整性 ($TAG)" "[ -f runs/$TAG/registry.jsonl ]" \
  go run ./cmd/forge registry --check --registry "runs/$TAG/registry.jsonl"

# G12: schema 一致性 —— Go 写出的记录必须符合 Python 辅助层读取的那套冻结 schema。
_opt "G12 go/python schema 一致 ($TAG)" "[ -f runs/$TAG/registry.jsonl ] && [ -f python/aux/schema_check.py ]" \
  python3 python/aux/schema_check.py "runs/$TAG/registry.jsonl"

# G13: Python 辅助层自己的测试。（辅助层的唯一职责是独立复核 Go，所以它自己也得有门。）
_opt "G13 python 辅助层测试" '[ -d python/tests ]' \
  python3 -m pytest python/tests -q

# G14: 规则对账 —— scipy 独立复算挖出来的 Spearman rho 与同号 run 占比。
_opt "G14 规则对账（scipy）" "[ -f knowledge/design_rules.md ] && [ -f runs/$TAG/registry.jsonl ]" \
  python3 python/aux/rules_check.py --rules knowledge/design_rules.md --registry "runs/$TAG/registry.jsonl"

# G15: 出图 + 最强的一次廉价复核：报告声称基线和机器最优各得了多少分，这里用
#      numpy/scipy 从设计向量重新算一遍（并对齐 Go 的 5 mm 钳位半径）。两者不一致
#      就意味着"机器打败人类"这句话所依赖的定义已经不唯一了。
#      输出同时打印落在钳位半径内的采样点数——那是"最优点在靠奇异性得分"的证据。
_opt "G15 出图 + 独立复评最优/基线 ($TAG)" "[ -f runs/$TAG/results.json ]" \
  python3 python/aux/analyze.py "runs/$TAG"

# G16: 复现门 —— 用 runs/<tag>/results.json 里记录的参数（不硬编码）重跑一次，逐位比对。
#      只排除 tag 与 timestamp；任何数字、id、谱系、term 变动都算红。没有这道门，
#      "机器赢了人工基线"就只是一次性观测，不是可复核的证据。
_opt "G16 逐位复现 ($TAG)" "[ -f runs/$TAG/results.json ] && [ -f scripts/repro_check.py ]" \
  python3 scripts/repro_check.py "$TAG"

echo
echo "=== 汇总: $pass 通过, $fail 失败, $skip 跳过 ==="
if [ "$fail" -ne 0 ]; then
  printf 'failed: %s\n' "${failed_names[*]}"
  exit 1
fi
exit 0
