#!/usr/bin/env python3
"""Gate G9 (schema parity): validate the Go-written registry JSONL.

The registry schema is frozen in ``internal/registry/api.go`` (Record/Params) and
``internal/physics/api.go`` (Metrics). This checker re-states that schema from the
outside and verifies every record against it, so a Go-side rename or a dropped
metric is caught by something that shares no code with Go.

Checks (all hard errors unless noted):
  * one JSON object per line; a truncated FINAL line is tolerated (the Go reader
    tolerates it too) and reported as a warning;
  * every required key present, no unknown keys (frozen schema), no null values;
  * ``metrics`` carries every frozen metric key with a numeric value;
  * ``params`` carries radius_m / z_m / current_A as equal-length numeric lists;
  * terms / weighted / penalties carry their frozen key sets with numeric values;
  * ``experiment_id`` contiguous from 1; ``design_id`` matches ``D%04d`` and the
    same running counter (Append assigns both from one counter);
  * warning only: parent_design referencing an unknown design_id.

Usage:
    python3 python/aux/schema_check.py runs/phase0/registry.jsonl
"""

from __future__ import annotations

import argparse
import json
import math
import re
import sys
from pathlib import Path

# Frozen key sets, transcribed from the Go JSON tags (not imported from Go).
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
               "penalties": ["conductor_field", "coil_separation", "not_a_mirror"]}
PARAM_FIELDS = ["radius_m", "z_m", "current_A"]

DESIGN_ID_RE = re.compile(r"^D(\d{4})$")


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
            if did in seen_design_ids:
                err(f"duplicate design_id {did}")
        seen_design_ids.add(did)

    parent = rec.get("parent_design")
    if isinstance(parent, str) and parent and seen_design_ids and parent not in seen_design_ids:
        warnings.append(f"line {lineno}: parent_design {parent!r} not seen yet (forward reference?)")
    return rec


def check_registry(path, verbose=True):
    """Validate one registry JSONL file. Returns (exit_code, errors, warnings, n_records)."""
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
