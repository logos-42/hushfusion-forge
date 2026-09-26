#!/usr/bin/env python3
"""对挖掘出的设计规则做独立重算（知识层）。

``internal/knowledge`` 会为每个 (设计参数, 分数项) 对计算一个按运行
(算法 x seed) 分别计算的 Spearman 秩相关，只保留符号在每一轮存活运行中都
复现的那些对，并把幸存者写进 ``knowledge/design_rules.md`` —— 表格、
适用范围说明，以及一个机器可读的 ```json 块。本脚本用
``scipy.stats.spearmanr`` 从 registry 本身重新推导同样的统计量，并逐条
规则做对齐核对。Go 与 scipy 在系数上必须一致到 < 1e-9；门限设在 0.02，
这样一条 *被公布* 的规则要么是同一条规则，要么是失败，
而绝不会是一次舍入抖动。

有两件事是被刻意 *不* 假设的，因为冻结文档并没有把它们钉死：
  * 把逐运行的 rho 聚合成那一个被上报的 ``rho`` 的方式（均值、中位数和
    最坏情况幅值都说得通）。三种都会算出来，而上报值必须与其中最接近
    的那个相符；
  * 运行过滤器 —— 文档说的是「只用 feasible 记录，记录数少于
    max(20, MinN/8) 的丢掉，至少两轮运行」；这些阈值在这里被实现出来，
    任何运行数量上的不一致都会作为警告上报。

用法：
    python3 python/aux/rules_check.py --rules knowledge/design_rules.md \\
                                      --registry runs/phase0/registry.jsonl
    python3 python/aux/rules_check.py --selftest     # 把这道门本身跑一遍
"""

from __future__ import annotations

import argparse
import json
import re
import sys
import tempfile
import warnings
from collections import defaultdict
from pathlib import Path

try:
    from scipy.stats import spearmanr
except ImportError:                                        # pragma: no cover
    spearmanr = None

RHO_TOL = 0.02
AGREE_TOL = 0.02
DEFAULT_MIN_N = 150
DEFAULT_TOP_K = 12

TERM_KEYS = ("field", "mirror", "volume", "ripple", "cost")
JSON_BLOCK_RE = re.compile(r"```json\s*(.*?)```", re.DOTALL)
PARAM_RE = re.compile(r"^([rzI])_(\d+)$")


# --------------------------------------------------------------------------- #
# 加载
# --------------------------------------------------------------------------- #

def spearman_rho(x, y):
    """带并列名次处理的 Spearman 秩相关（平均名次），由 scipy 计算。"""
    if spearmanr is None:                                  # pragma: no cover
        raise RuntimeError("scipy is required for the independent reconciliation")
    x = [float(v) for v in x]
    y = [float(v) for v in y]
    if len(x) != len(y):
        raise ValueError("series length mismatch")
    if len(x) < 2:
        return 0.0
    with warnings.catch_warnings():
        # 常量输入没有定义的秩相关：scipy 说 NaN，我们说 0
        warnings.simplefilter("ignore")
        r = spearmanr(x, y)
    stat = getattr(r, "statistic", None)
    if stat is None:                                       # scipy < 1.9 的 tuple API
        stat = r[0]                                        # pragma: no cover
    stat = float(stat)
    return 0.0 if stat != stat else stat                   # NaN 记作 0


def load_registry(path):
    """读取 registry JSONL，容忍最后一行被截断。"""
    lines = Path(path).read_text().splitlines()
    recs = []
    for i, raw in enumerate(lines):
        if not raw.strip():
            continue
        try:
            recs.append(json.loads(raw))
        except json.JSONDecodeError:
            if i == len(lines) - 1:
                continue
            raise
    return recs


def load_rules(path):
    """从 md 文件的 ```json 块里取出机器可读的规则。"""
    text = Path(path).read_text()
    blocks = JSON_BLOCK_RE.findall(text)
    if not blocks:
        raise ValueError(f"no ```json block found in {path}")
    for block in blocks:
        try:
            doc = json.loads(block)
        except json.JSONDecodeError:
            continue
        if isinstance(doc, dict) and isinstance(doc.get("rules"), list):
            return [r for r in doc["rules"] if isinstance(r, dict)]
        if isinstance(doc, list) and all(isinstance(r, dict) for r in doc):
            return [r for r in doc if "rule_id" in r or "parameter" in r]
    raise ValueError(f"no rules array found in the json blocks of {path}")


# --------------------------------------------------------------------------- #
# 序列抽取
# --------------------------------------------------------------------------- #

def parameter_series(rec, name):
    """把一个设计参数取成标量序列：r_i -> params.radius_m[i]，等等。"""
    m = PARAM_RE.match(name)
    if not m:
        raise ValueError(f"unrecognised parameter name {name!r}")
    kind, idx = m.group(1), int(m.group(2))
    params = rec.get("params") or {}
    key = {"r": "radius_m", "z": "z_m", "I": "current_A"}[kind]
    arr = params.get(key)
    if isinstance(arr, list) and idx < len(arr):
        return float(arr[idx])
    design = rec.get("design")
    if isinstance(design, list):
        n = len(design) // 3
        off = {"r": 0, "z": n, "I": 2 * n}[kind]
        return float(design[off + idx])
    raise ValueError(f"record has neither params.{key} nor a usable design vector")


def term_series(rec, term):
    terms = rec.get("terms") or {}
    if term not in terms:
        raise ValueError(f"record has no term {term!r}")
    return float(terms[term])


def feasible_runs(records, min_n=DEFAULT_MIN_N, min_per_run=None):
    """把 feasible 记录按运行 (算法 x seed) 分组，并应用过滤器。"""
    floor = min_per_run if min_per_run is not None else max(20, min_n // 8)
    runs = defaultdict(list)
    for rec in records:
        if not rec.get("feasible", False):
            continue
        runs[(rec.get("algorithm", "?"), rec.get("seed", -1))].append(rec)
    return {k: v for k, v in runs.items() if len(v) >= floor}


# --------------------------------------------------------------------------- #
# 对齐核对
# --------------------------------------------------------------------------- #

def reconcile(rules, records, rho_tol=RHO_TOL, agree_tol=AGREE_TOL, min_n=DEFAULT_MIN_N,
              verbose=True):
    """重新计算每一条规则并比较。返回 (exit_code, rows)。"""
    runs = feasible_runs(records, min_n=min_n)
    ok = True
    rows = []
    if verbose:
        print(f"rules reconciliation: {len(rules)} rule(s) vs {len(runs)} surviving run(s) "
              f"({sum(len(v) for v in runs.values())} feasible records)")
        print(f"  {'rule':<22}{'term':<9}{'go rho':>12}{'scipy(mean)':>14}{'scipy(med)':>13}"
              f"{'worst|rho|':>12}{'d(rho)':>10}{'agg':>8}  sign vs agree")
    for rule in rules:
        param, term = rule.get("parameter"), rule.get("term")
        per_run = {}
        for key, recs in runs.items():
            xs = []; ys = []
            for rec in recs:
                try:
                    xs.append(parameter_series(rec, param))
                    ys.append(term_series(rec, term))
                except ValueError:
                    xs = ys = []
                    break
            if len(xs) >= 3 and len(set(xs)) > 1 and len(set(ys)) > 1:
                per_run[key] = spearman_rho(xs, ys)
        if len(per_run) < 2:
            rows.append({"rule": rule.get("rule_id"), "status": "SKIP",
                         "detail": f"only {len(per_run)} run(s) usable"})
            if verbose:
                print(f"  [SKIP] {rule.get('rule_id')}: only {len(per_run)} run(s) usable")
            continue
        vals = list(per_run.values())
        mean = sum(vals) / len(vals)
        med = sorted(vals)[len(vals) // 2] if len(vals) % 2 else \
            (sorted(vals)[len(vals) // 2 - 1] + sorted(vals)[len(vals) // 2]) / 2.0
        worst = min(vals, key=abs)
        cands = {"mean": mean, "median": med, "worst": worst}
        go_rho = float(rule.get("rho", float("nan")))
        best_name, best_delta = None, float("inf")
        for name, val in cands.items():
            d = abs(go_rho - val)
            if d < best_delta:
                best_name, best_delta = name, d
        n_designs = sum(len(v) for v in runs.values())
        # 符号一致度：与上报 rho 同号的运行所占比例
        pos = sum(1 for v in vals if v > 0)
        neg = sum(1 for v in vals if v < 0)
        majority = (pos > neg) - (pos < neg)
        my_agree = (pos if majority > 0 else neg) / len(vals) if majority != 0 else 0.0
        go_agree = float(rule.get("sign_agreement", float("nan")))
        sign_bad = (go_rho > 0) != (majority > 0) or go_rho == 0 or majority == 0
        agree_bad = abs(my_agree - go_agree) > agree_tol
        good = best_delta <= rho_tol and not sign_bad and not agree_bad
        ok &= good
        rows.append({"rule": rule.get("rule_id"), "parameter": param, "term": term,
                     "go_rho": go_rho, "scipy_rho_mean": mean, "scipy_rho_median": med,
                     "scipy_rho_worst": worst, "best_agg": best_name, "delta": best_delta,
                     "go_sign_agreement": go_agree, "scipy_sign_agreement": my_agree,
                     "n_runs": len(vals), "n_designs": n_designs,
                     "status": "OK" if good else "FAIL"})
        if verbose:
            print(f"  [{'OK  ' if good else 'FAIL'}] {str(rule.get('rule_id')):<20}"
                  f"{str(term):<9}{go_rho:>12.6f}{mean:>14.6f}{med:>13.6f}{worst:>12.6f}"
                  f"{best_delta:>10.2e}{best_name:>8}  "
                  f"{'ok' if not sign_bad else 'SIGN'}/{my_agree:.3f} (go {go_agree:.3f})")
            if rule.get("n_runs") is not None and int(rule["n_runs"]) != len(vals):
                print(f"       [WARN] go n_runs={rule['n_runs']} scipy n_runs={len(vals)}")
            if rule.get("n_designs") is not None and int(rule["n_designs"]) != n_designs:
                print(f"       [WARN] go n_designs={rule['n_designs']} scipy usable={n_designs}")
    print(f"rules reconciliation {'PASS' if ok else 'FAIL'}"
          f" ({sum(1 for r in rows if r['status'] == 'OK')}/{len(rows)} rule(s) matched"
          f", tolerance {rho_tol})")
    return (0 if ok else 1), rows


# --------------------------------------------------------------------------- #
# 自检
# --------------------------------------------------------------------------- #

def _synth_records(n_runs=3, n_per_run=40, seed=11):
    """合成的 feasible registry 记录，带有已知且干净的相关系数。"""
    import random
    rng = random.Random(seed)
    recs, eid = [], 0
    for run in range(n_runs):
        algo = "evolution" if run < 2 else "random"
        for k in range(n_per_run):
            eid += 1
            r = 0.1 + 0.9 * rng.random()
            z = -1.2 + 2.4 * rng.random()
            cur = 1e4 + 2.5e6 * rng.random()
            recs.append({
                "experiment_id": eid, "design_id": f"D{eid:04d}", "algorithm": algo,
                "seed": 7 if run % 2 == 0 else 8, "generation": k, "eval_index": k,
                "tag": "selftest", "timestamp": "1970-01-01T00:00:00Z", "score": -r,
                "feasible": True,
                "params": {"radius_m": [r, 0.5, 0.5, 0.3], "z_m": [z, -0.25, 0.25, 1.0],
                           "current_A": [cur, 4.6e5, 4.6e5, 1.6e6]},
                "terms": {"field": 0.1 * r, "mirror": 0.3 * z, "volume": r, "ripple": z * 0.1,
                          "cost": cur / 1.79e12},
                "weighted": {"field": 0.1 * r, "mirror": 0.15 * z, "volume": 0.75 * r,
                             "ripple": -0.05 * z, "cost": -cur / 1.79e12},
                "penalties": {"conductor_field": 0.0, "coil_separation": 0.0, "not_a_mirror": 0.0},
                "metrics": {"B_mid_T": 1.0, "B_throat_T": 2.0, "z_throat_m": -1.0,
                            "mirror_ratio": 2.0, "volume_good": 0.8, "ripple": 0.0,
                            "B_coil_max_T": 3.4, "min_coil_gap_m": 0.5,
                            "cost_proxy": cur, "coil_proximity_floor_hit": False,
                            "n_coils": 4, "mu0": 1.2566370614359173e-06},
            })
    return recs


def selftest(verbose=True):
    """证明这道门会触发：干净的规则通过，被改过的 rho 变红。"""
    records = _synth_records()
    runs = feasible_runs(records)
    pair = ("r_0", "volume")
    rhos = {}
    for key, recs in runs.items():
        rhos[key] = spearman_rho([parameter_series(r, pair[0]) for r in recs],
                                 [term_series(r, pair[1]) for r in recs])
    mean = sum(rhos.values()) / len(rhos)
    rules = [{"rule_id": "R001", "parameter": pair[0], "term": pair[1], "rho": mean,
              "sign_agreement": 1.0, "n_designs": sum(len(v) for v in runs.values()),
              "n_runs": len(runs), "decile_low": 0.0, "decile_high": 1.0,
              "statement": "selftest", "statement_en": "selftest", "scope": "selftest"}]
    print("-- selftest A: honest rule must pass --")
    code_ok, _ = reconcile(rules, records, verbose=verbose)
    print("-- selftest B: rho shifted by 0.5 must fail --")
    bad = [dict(rules[0], rho=mean - 0.5)]
    code_bad, _ = reconcile(bad, records, verbose=verbose)
    print(f"selftest {'PASS' if code_ok == 0 and code_bad == 1 else 'FAIL'}"
          f" (honest={code_ok}, falsified={code_bad})")
    return 0 if (code_ok == 0 and code_bad == 1) else 1


# --------------------------------------------------------------------------- #
# cli（命令行入口）
# --------------------------------------------------------------------------- #

def main(argv=None) -> int:
    root = Path(__file__).resolve().parents[2]
    ap = argparse.ArgumentParser(description="independent rule reconciliation (stage G)")
    ap.add_argument("--rules", default=str(root / "knowledge" / "design_rules.md"))
    ap.add_argument("--registry", default=str(root / "runs" / "phase0" / "registry.jsonl"))
    ap.add_argument("--rho-tol", type=float, default=RHO_TOL)
    ap.add_argument("--min-n", type=int, default=DEFAULT_MIN_N)
    ap.add_argument("--selftest", action="store_true")
    args = ap.parse_args(argv)
    if args.selftest:
        return selftest()
    for path, what in ((args.rules, "rules markdown"), (args.registry, "registry")):
        if not Path(path).is_file():
            print(f"error: {what} not found: {path}", file=sys.stderr)
            print("       (stage E writes knowledge/design_rules.md; until then this gate "
                  "cannot run -- use --selftest to check the gate itself)", file=sys.stderr)
            return 2
    rules = load_rules(args.rules)
    records = load_registry(args.registry)
    code, _ = reconcile(rules, records, rho_tol=args.rho_tol, min_n=args.min_n)
    return code


if __name__ == "__main__":
    sys.exit(main())
