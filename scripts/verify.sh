#!/usr/bin/env bash
# Forge acceptance gates.
#
# Every gate is a command whose EXIT CODE is the verdict. A gate that cannot go
# red is not a gate, so each gate below is paired with the failure mode it is
# supposed to catch, and none of them are "the author said it works".
#
# Usage:  bash scripts/verify.sh [tag]
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

# A gate whose precondition is absent is SKIPped loudly, never silently passed.
_opt() {
  local name="$1" precond="$2"; shift 2
  if eval "$precond" >/dev/null 2>&1; then
    _run "$name" "$@"
  else
    printf 'SKIP  %s   (precondition: %s)\n' "$name" "$precond"
    skip=$((skip + 1))
  fi
}

echo "=== Forge gates (tag=$TAG) ==="

# G1-G2: it compiles and the static analyser is quiet.
_run "G1  build" go build ./...
_run "G2  vet" go vet ./...

# G3: formatting is a contract, not a preference.
_run "G3  gofmt clean" bash -c 'test -z "$(gofmt -l . 2>/dev/null | grep -v "^$")"'

# G4: the ownership freeze gate — no overlaps, no uncovered file, and the check
#     itself is mutation-tested (a duplicated roster entry must be reported).
_run "G4  ownership freeze gate" go test ./internal/owners/

# G5: every unit test, including the analytic anchors (on-axis closed form,
#     Helmholtz amplitude/uniformity, discrete-vs-analytic, div/curl) and the
#     golden cross-language comparisons.
_run "G5  unit tests" go test ./...

# G6: the human baseline must lie INSIDE the search box. If it were clipped on
#     decode, "machine beats human" would be scored against a design nobody
#     proposed.
_run "G6  baseline inside search box" go test ./internal/baseline/ -run TestBaselineInsideSearchBox

# G7: the RL environment must not pretend to have a learned policy.
_run "G7  no learned policy (anti-fabrication)" go test ./internal/rlenv/ -run TestNoLearnedPolicy

# G8: the independent Python oracle (scipy, no Go code) must reproduce the frozen
#     golden numbers the Go engine is measured against.
_opt "G8  python oracle vs golden" '[ -f python/aux/oracle.py ]' \
  python3 python/aux/oracle.py --check-golden testdata/

# G9: end-to-end CLI integration: baseline -> evaluate -> compare against
#     testdata/golden_baseline.json (score within 1e-6) -> registry round trip.
_opt "G9  forge verify (end-to-end)" '[ -f cmd/forge/main.go ]' \
  go run ./cmd/forge verify

# G10: cross-language field agreement — Go emits its own field samples, scipy
#      recomputes them, and the two must agree to < 1e-9.
_opt "G10 cross-language field agreement" '[ -f cmd/forge/main.go ] && [ -f python/aux/oracle.py ]' \
  bash -c 'set -e; tmp=$(mktemp -d); go run ./cmd/forge xcheck --out "$tmp/go_xcheck.json" >/dev/null; python3 python/aux/oracle.py --compare-go "$tmp/go_xcheck.json"'

# G11: registry integrity of a produced run (ids sequential, parents exist,
#      required fields present).
_opt "G11 registry integrity ($TAG)" "[ -f runs/$TAG/registry.jsonl ]" \
  go run ./cmd/forge registry --check --registry "runs/$TAG/registry.jsonl"

# G12: schema parity — the Go records must obey the frozen schema the Python
#      auxiliary layer reads.
_opt "G12 go/python schema parity ($TAG)" "[ -f runs/$TAG/registry.jsonl ] && [ -f python/aux/schema_check.py ]" \
  python3 python/aux/schema_check.py "runs/$TAG/registry.jsonl"

echo
echo "=== summary: $pass passed, $fail failed, $skip skipped ==="
if [ "$fail" -ne 0 ]; then
  printf 'failed: %s\n' "${failed_names[*]}"
  exit 1
fi
exit 0
