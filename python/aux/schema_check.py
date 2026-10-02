#!/usr/bin/env python3
"""门 G9（schema 一致性）：校验 Go 写出的 registry JSONL。

registry schema 冻结在 ``internal/registry/api.go`` (Record/Params) 和
``internal/physics/api.go`` (Metrics)。本检查器从外部把这份 schema 重新
陈述一遍，并逐条记录校验，因此 Go 侧的改名或漏掉一个指标，会被一个
与 Go 不共享任何代码的东西抓到。

检查项（除注明外都是硬错误）：
  * 每行一个 JSON 对象；被截断的 *最后* 一行被容忍（Go 的读取器也容忍它），
    并作为警告上报；
  * 每个必需的键都在、没有未知键（冻结 schema）、没有 null 值；
  * ``metrics`` 携带每一个冻结的指标键，且值为数值；
  * ``params`` 携带 radius_m / z_m / current_A，为等长的数值列表；
  * terms / weighted / penalties 携带各自的冻结键集合，值为数值；
  * ``experiment_id`` 从 1 开始连续；``design_id`` 匹配 ``D%04d``（至少
    四位数字，因此 D10000 是合法的），并且
    与同一个递增计数器一致（Append 从同一个计数器分配两者）；
  * 仅警告：parent_design 指向了一个未知的 design_id。

用法：
    python3 python/aux/schema_check.py runs/phase0/registry.jsonl
"""

from __future__ import annotations

import argparse
import json
import math
import re
import sys
from pathlib import Path

# 冻结的键集合，从 Go 的 JSON tag 抄录而来（不是从 Go 导入的）。
REQUIRED_FIELDS = ["experiment_id", "design_id", "algorithm", "seed", "score",
                   "params", "terms", "metrics"]
OPTIONAL_FIELDS = ["parent_design", "note"]
ALL_FIELDS = set(REQUIRED_FIELDS) | {
    "generation", "eval_index", "tag", "timestamp", "feasible",
    "weighted", "penalties", *OPTIONAL_FIELDS,
}
NUMERIC_FIELDS = ["experiment_id", "generation", "seed", "eval_index", "score"]
STRING_FIELDS = ["design_id", "algorithm", "tag", "timestamp", "parent_design", "note"]
BOOL_FIELDS = ["feasible"]

METRIC_FIELDS = ["B_mid_T", "B_throat_T", "z_throat_m", "mirror_ratio", "volume_good",
                 "ripple", "B_coil_max_T", "min_coil_gap_m", "cost_proxy",
                 "coil_proximity_floor_hit", "n_coils", "mu0"]
TERM_FIELDS = {"terms": ["field", "mirror", "volume", "ripple", "cost"],
               "weighted": ["field", "mirror", "volume", "ripple", "cost"],
               "penalties": ["conductor_field", "coil_separation", "not_a_mirror",
                             "clearance"]}
PARAM_FIELDS = ["radius_m", "z_m", "current_A"]

DESIGN_ID_RE = re.compile(r"^D(\d{4,})$")
# NB: `%04d` 是 *最小* 宽度，因此一份 10 000+ 条记录的 registry 里出现
# D10000 是合法的。这里用恰好 {4} 位的模式，会让这道门在规模上悄悄变红
#（12 001 条记录时报了 2002 个错误），而每一个小 fixture 都能通过。
# 正则只是形状检查；真正的规则是 _check_record 内部那个精确的规范形式
# 比较 (did == "D%04d" % experiment_id)。


def _is_number(v):
    return isinstance(v, (int, float)) and not isinstance(v, bool) and math.isfinite(float(v))


def _check_record(rec, lineno, errors, warnings, seen_design_ids, expect_id):
    def err(msg):
        errors.append(f"line {lineno}: {msg}")

    if not isinstance(rec, dict):
        err("record is not a JSON object")
        return None

    for key in REQUIRED_FIELDS:
        if key not in rec:
            err(f"missing required key {key!r}")

    unknown = sorted(set(rec) - ALL_FIELDS)
    if unknown:
        err(f"unknown key(s) {unknown} (frozen schema: {sorted(ALL_FIELDS)})")

    for key in NUMERIC_FIELDS:
        if key in rec and not _is_number(rec[key]):
            err(f"{key!r} must be a number, got {rec[key]!r}")
    for key in STRING_FIELDS:
        if key in rec and not isinstance(rec[key], str):
            err(f"{key!r} must be a string, got {rec[key]!r}")
    for key in BOOL_FIELDS:
        if key in rec and not isinstance(rec[key], bool):
            err(f"{key!r} must be a bool, got {rec[key]!r}")

    metrics = rec.get("metrics")
    if isinstance(metrics, dict):
        for key in METRIC_FIELDS:
            if key not in metrics:
                err(f"metrics missing key {key!r}")
            elif key == "coil_proximity_floor_hit":
                if not isinstance(metrics[key], (bool, int, float)):
                    err(f"metrics.coil_proximity_floor_hit must be bool/number, got {metrics[key]!r}")
            elif not _is_number(metrics[key]):
                err(f"metrics.{key} must be numeric, got {metrics[key]!r}")
    elif metrics is not None:
        err(f"'metrics' must be an object, got {type(metrics).__name__}")

    for block, keys in TERM_FIELDS.items():
        b = rec.get(block)
        if isinstance(b, dict):
            for key in keys:
                if key not in b:
                    err(f"{block} missing key {key!r}")
                elif not _is_number(b[key]):
                    err(f"{block}.{key} must be numeric, got {b[key]!r}")
        elif b is not None:
            err(f"{block!r} must be an object, got {type(b).__name__}")

    params = rec.get("params")
    if isinstance(params, dict):
        for key in PARAM_FIELDS:
            v = params.get(key)
            if not isinstance(v, list) or not all(_is_number(x) for x in v):
                err(f"params.{key} must be a list of numbers")
        lens = {k: len(params[k]) for k in PARAM_FIELDS if isinstance(params.get(k), list)}
        if len(lens) == len(PARAM_FIELDS) and len(set(lens.values())) != 1:
            err(f"params arrays have different lengths: {lens}")
        else:
            n_metric = (metrics or {}).get("n_coils") if isinstance(metrics, dict) else None
            if lens and _is_number(n_metric) and int(n_metric) != list(lens.values())[0]:
                err(f"params length {list(lens.values())[0]} != metrics.n_coils {n_metric}")
    elif params is not None:
        err(f"'params' must be an object, got {type(params).__name__}")

    eid_raw = rec.get("experiment_id")
    eid = None
    if isinstance(eid_raw, (int, float)) and not isinstance(eid_raw, bool):
        eid = int(eid_raw)
    if eid is not None and expect_id is not None and eid != expect_id:
        err(f"experiment_id {eid} is not contiguous from 1 (expected {expect_id})")
    did = rec.get("design_id")
    if isinstance(did, str):
        m = DESIGN_ID_RE.match(did)
        if not m:
            err(f"design_id {did!r} does not match D%04d")
        else:
            if eid is not None and int(m.group(1)) != eid:
                err(f"design_id {did} is not the id of experiment_id {eid}")
            if eid is not None:
                canonical = "D%04d" % eid
                if did != canonical:
                    err(f"design_id {did!r} is not the canonical form of "
                        f"experiment_id {eid} (expected {canonical!r})")
            if did in seen_design_ids:
                err(f"duplicate design_id {did}")
        seen_design_ids.add(did)

    parent = rec.get("parent_design")
    if isinstance(parent, str) and parent and seen_design_ids and parent not in seen_design_ids:
        warnings.append(f"line {lineno}: parent_design {parent!r} not seen yet (forward reference?)")
    return rec


def check_registry(path, verbose=True):
    """校验一个 registry JSONL 文件。返回 (exit_code, errors, warnings, n_records)。"""
    path = Path(path)
    errors, warnings = [], []
    if not path.is_file():
        print(f"error: registry not found: {path}", file=sys.stderr)
        return 2, errors, warnings, 0
    lines = path.read_text().splitlines()
    seen, expect_id, n = set(), 1, 0
    for i, raw in enumerate(lines, start=1):
        if not raw.strip():
            warnings.append(f"line {i}: blank line")
            continue
        try:
            rec = json.loads(raw)
        except json.JSONDecodeError as exc:
            if i == len(lines):
                warnings.append(f"line {i}: truncated final line tolerated ({exc.msg})")
                continue
            errors.append(f"line {i}: not valid JSON ({exc.msg})")
            continue
        _check_record(rec, i, errors, warnings, seen, expect_id)
        expect_id += 1
        n += 1
    if verbose:
        print(f"G9 schema parity: {path} -- {n} record(s)")
        for w in warnings:
            print(f"  [WARN] {w}")
        for e in errors:
            print(f"  [FAIL] {e}")
        print(f"G9 {'PASS' if not errors else 'FAIL'}: "
              f"{n} records, {len(errors)} error(s), {len(warnings)} warning(s)")
    return (0 if not errors else 1), errors, warnings, n


def main(argv=None) -> int:
    ap = argparse.ArgumentParser(description="G9 registry schema parity check (stage G)")
    ap.add_argument("registry", nargs="?", default=None,
                    help="registry JSONL (default: runs/phase0/registry.jsonl)")
    ap.add_argument("--quiet", action="store_true")
    args = ap.parse_args(argv)
    if not args.registry:
        root = Path(__file__).resolve().parents[2]
        args.registry = root / "runs" / "phase0" / "registry.jsonl"
    code, _, _, _ = check_registry(args.registry, verbose=not args.quiet)
    return code


if __name__ == "__main__":
    sys.exit(main())
