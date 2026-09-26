#!/usr/bin/env python3
"""Plots + tables for one benchmark run (the human-vs-machine figure set).

Reads ``runs/<tag>/results.json`` (experiment.Report, schema frozen in
internal/experiment/api.go) and, optionally, ``runs/<tag>/registry.jsonl``, and
produces into ``runs/<tag>/figures/``:

    best_so_far.png     best-so-far vs evaluations, mean over seeds with a
                        min-max band per method, human baseline as a flat line
    axis_profile.png    on-axis |B|(z) of the human baseline vs the machine best,
                        recomputed with the independent scipy oracle
    benchmark_table.md  equal-budget benchmark table (markdown fragment)

The aggregate block written by Go is independently re-computed from ``runs`` and
any disagreement is printed, because "the number in the report" and "the numbers
the report was summarised from" are two different claims.

Every axis label is ASCII/English on purpose: macOS font fallback for CJK inside
matplotlib is a known time sink and adds nothing to a physics figure.

If matplotlib is unavailable the script still writes the markdown table and says
so out loud -- a missing plotting stack must not take the analysis down.

Usage:
    python3 python/aux/analyze.py runs/phase0
"""

from __future__ import annotations

import argparse
import json
import math
import statistics
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
if str(HERE) not in sys.path:
    sys.path.insert(0, str(HERE))

import oracle  # noqa: E402  (sibling module, independent of Go)

_MPL_ERR = None
try:                                                       # pragma: no cover
    import matplotlib
    matplotlib.use("Agg")
    import matplotlib.pyplot as plt
    HAVE_MPL = True
except Exception as exc:                                   # pragma: no cover
    HAVE_MPL = False
    _MPL_ERR = exc


# --------------------------------------------------------------------------- #
# loading
# --------------------------------------------------------------------------- #

def load_report(target):
    """Accept runs/<tag>, runs/<tag>/results.json or a path to any report json."""
    p = Path(target)
    if p.is_dir():
        p = p / "results.json"
    if not p.is_file():
        raise FileNotFoundError(f"report not found: {p}")
    return json.loads(p.read_text())


def load_registry(target):
    p = Path(target)
    if p.is_dir():
        p = p / "registry.jsonl"
    if not p.is_file():
        return []
    recs = []
    lines = p.read_text().splitlines()
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


# --------------------------------------------------------------------------- #
# curves
# --------------------------------------------------------------------------- #

def history_from_registry(records, algorithm, seed, budget):
    """Best-so-far trajectory rebuilt from the registry when the report omits it."""
    rows = [r for r in records if r.get("algorithm") == algorithm and r.get("seed") == seed]
    if not rows:
        return []
    rows.sort(key=lambda r: (r.get("eval_index", 0), r.get("experiment_id", 0)))
    out, best = [], -math.inf
    for r in rows[:budget] if budget else rows:
        best = max(best, float(r.get("score", -math.inf)))
        out.append(best)
    return out


def best_so_far_curves(report, records=None):
    """{method: {"x", "mean", "lo", "hi", "n_seeds", "note"}} for the score-vs-evals plot."""
    budget = int(report.get("meta", {}).get("budget") or 0)
    per_method = {}
    for run in report.get("runs") or []:
        algo = run.get("algorithm", "?")
        hist = list(run.get("history") or [])
        if not hist and records:
            hist = history_from_registry(records, algo, run.get("seed"), budget)
            if hist:
                per_method.setdefault("_notes", []).append(
                    f"{algo}/seed{run.get('seed')}: history rebuilt from registry")
        if not hist:
            per_method.setdefault("_missing", []).append(f"{algo}/seed{run.get('seed')}")
            continue
        per_method.setdefault(algo, []).append(hist)

    out = {}
    for algo, hists in per_method.items():
        if algo.startswith("_"):
            continue
        n = min(len(h) for h in hists)
        if len({len(h) for h in hists}) != 1:
            out.setdefault("_notes", []).append(
                f"{algo}: trajectories have different lengths "
                f"{sorted({len(h) for h in hists})}; truncated to {n}")
        band = [[h[i] for h in hists] for i in range(n)]
        out[algo] = {
            "x": list(range(1, n + 1)),
            "mean": [sum(b) / len(b) for b in band],
            "lo": [min(b) for b in band],
            "hi": [max(b) for b in band],
            "n_seeds": len(hists),
            "final_mean": sum(b[-1] for b in band) / n if n else float("nan"),
        }
    if "_notes" in per_method and per_method["_notes"]:
        out.setdefault("_notes", []).extend(per_method["_notes"])
    if "_missing" in per_method:
        out["_missing"] = per_method["_missing"]
    return out


# --------------------------------------------------------------------------- #
# aggregate cross-check
# --------------------------------------------------------------------------- #

def recompute_aggregate(report):
    """Independently re-derive the per-method aggregation from report['runs']."""
    budget = int(report.get("meta", {}).get("budget") or 0)
    base = float((report.get("baseline") or {}).get("score", float("nan")))
    per = {}
    for run in report.get("runs") or []:
        per.setdefault(run.get("algorithm", "?"), []).append(run)
    out = {}
    for algo, runs in per.items():
        scores = [float(r.get("best_score", float("nan"))) for r in runs]
        n = len(scores)
        mean = sum(scores) / n if n else float("nan")
        std = statistics.stdev(scores) if n > 1 else 0.0
        beating = [s for s in scores if s > base]
        evals = [int(r.get("evals_to_beat", -1)) for r in runs if int(r.get("evals_to_beat", -1)) >= 0]
        out[algo] = {
            "n_seeds": n, "budget": budget,
            "best_mean": mean, "best_std": std,
            "best_min": min(scores) if n else float("nan"),
            "best_max": max(scores) if n else float("nan"),
            "n_beating_baseline": len(beating),
            "frac_beating_baseline": len(beating) / n if n else float("nan"),
            "evals_to_beat_mean": (sum(evals) / len(evals)) if evals else -1.0,
            "evals_to_beat_median": (statistics.median(evals)) if evals else -1.0,
            "baseline_score": base,
        }
    return out


def compare_aggregate(report, verbose=True):
    """Compare the report's aggregate block with the re-derived one. Returns bool."""
    mine = recompute_aggregate(report)
    theirs = report.get("aggregate") or {}
    ok = True
    if not theirs:
        if verbose:
            print("  [WARN] report has no aggregate block; recomputed one is used for the table")
        return True, mine
    for algo, my in mine.items():
        th = theirs.get(algo)
        if th is None:
            print(f"  [WARN] aggregate missing method {algo!r} present in runs")
            ok = False
            continue
        for key, val in my.items():
            if key not in th:
                continue
            t = th[key]
            if isinstance(val, float) and (math.isnan(val) or math.isnan(float(t))):
                continue
            denom = max(abs(float(val)), abs(float(t)), 1e-12)
            if abs(float(val) - float(t)) > 1e-9 * denom:
                print(f"  [FAIL] aggregate[{algo}].{key}: report={t!r} recomputed={val!r}")
                ok = False
    if verbose and ok:
        print(f"  aggregate block re-derived from runs: {len(mine)} method(s) agree to 1e-9 relative")
    return ok, mine


# --------------------------------------------------------------------------- #
# axis profile
# --------------------------------------------------------------------------- #

def axis_profile(spec_map, design, n_points=None):
    """Independent on-axis |B|(z) of one design, from the scipy oracle."""
    spec = oracle.Spec.from_map(spec_map)
    coils = oracle.vector_to_coils(design, spec)
    n = n_points or (4 * spec.n_axis - 3)
    z = [(-spec.z_axis_max) + (2.0 * spec.z_axis_max) * i / (n - 1) for i in range(n)]
    mag = oracle.on_axis_field(coils, z)
    return z, list(mag), coils


# --------------------------------------------------------------------------- #
# markdown table
# --------------------------------------------------------------------------- #

def benchmark_table(report, mine=None):
    agg = mine if mine is not None else (report.get("aggregate") or {})
    base = float((report.get("baseline") or {}).get("score", float("nan")))
    meta = report.get("meta") or {}
    lines = [
        f"Equal-budget benchmark (tag `{meta.get('tag', '?')}`, "
        f"budget {meta.get('budget', '?')} evals/method/seed, "
        f"seeds {meta.get('seeds', '?')}).",
        "",
        "| method | seeds | best mean | best std | best min | best max | beating human | evals to beat (mean) |",
        "|---|---:|---:|---:|---:|---:|---:|---:|",
        f"| human baseline (`textbook_mirror`) | - | {base:.6f} | - | - | - | - | - |",
    ]
    for algo in sorted(agg):
        a = agg[algo]
        lines.append(
            f"| {algo} | {a['n_seeds']} | {a['best_mean']:.6f} | {a['best_std']:.6f} | "
            f"{a['best_min']:.6f} | {a['best_max']:.6f} | "
            f"{a['n_beating_baseline']}/{a['n_seeds']} | {a['evals_to_beat_mean']:.1f} |")
    lines += ["", f"Human baseline score: `{base:.6f}`. Higher is better.", ""]
    if mine is not None and (report.get("aggregate") or {}):
        lines.append("_Aggregate block cross-checked against a recomputation from `runs[]` "
                     "(tolerance 1e-9 relative)._\n")
    return "\n".join(lines)


# --------------------------------------------------------------------------- #
# figures
# --------------------------------------------------------------------------- #

def plot_best_so_far(curves, baseline_score, out_path, title="best-so-far vs evaluations"):
    fig, ax = plt.subplots(figsize=(8.0, 5.0), dpi=150)
    for algo in sorted(k for k in curves if not k.startswith("_")):
        c = curves[algo]
        ax.plot(c["x"], c["mean"], linewidth=1.8, label=f"{algo} (mean of {c['n_seeds']} seeds)")
        ax.fill_between(c["x"], c["lo"], c["hi"], alpha=0.18, linewidth=0)
    if baseline_score == baseline_score:                    # not NaN
        ax.axhline(baseline_score, linestyle="--", color="0.35", linewidth=1.4,
                   label="human baseline (textbook_mirror)")
    ax.set_xlabel("evaluations (equal budget per method and seed)")
    ax.set_ylabel("best score so far")
    ax.set_title(title)
    ax.grid(alpha=0.25, linewidth=0.5)
    ax.legend(loc="lower right", fontsize=8, framealpha=0.9)
    fig.tight_layout()
    fig.savefig(out_path)
    plt.close(fig)


def plot_axis_profiles(profiles, out_path, title="on-axis |B|(z)"):
    fig, ax = plt.subplots(figsize=(8.0, 5.0), dpi=150)
    for label, (z, mag, coils) in profiles.items():
        ax.plot(z, mag, linewidth=1.8, label=label)
        zt = max(((abs(m), zz) for zz, m in zip(z, mag)))[1]
        ax.axvline(zt, linestyle=":", linewidth=1.0, alpha=0.6)
    ax.set_xlabel("z [m]")
    ax.set_ylabel("|B| on axis [T]")
    ax.set_title(title + " (independent scipy oracle)")
    ax.grid(alpha=0.25, linewidth=0.5)
    ax.legend(loc="upper right", fontsize=8, framealpha=0.9)
    fig.tight_layout()
    fig.savefig(out_path)
    plt.close(fig)


# --------------------------------------------------------------------------- #
# driver
# --------------------------------------------------------------------------- #

def analyze(target, registry_path=None, make_figures=True, verbose=True):
    report = load_report(target)
    tag = (report.get("meta") or {}).get("tag") or Path(target).name or "run"
    out_dir = Path(target)
    if out_dir.is_file():
        out_dir = out_dir.parent
    out_dir = out_dir / "figures"
    out_dir.mkdir(parents=True, exist_ok=True)

    reg = load_registry(registry_path) if registry_path else load_registry(out_dir.parent)
    if verbose:
        print(f"analyze: tag={tag} budget={(report.get('meta') or {}).get('budget')} "
              f"seeds={(report.get('meta') or {}).get('seeds')} "
              f"methods={(report.get('meta') or {}).get('methods')}")
        print(f"  registry records: {len(reg)}")

    ok, mine = compare_aggregate(report, verbose=verbose)
    table = benchmark_table(report, mine=mine)
    (out_dir / "benchmark_table.md").write_text(table)
    if verbose:
        print(f"  wrote {out_dir / 'benchmark_table.md'}")

    if make_figures and HAVE_MPL:
        curves = best_so_far_curves(report, reg)
        for note in curves.get("_notes", []):
            print(f"  [note] {note}")
        for miss in curves.get("_missing", []):
            print(f"  [WARN] no best-so-far history for {miss} (and registry had none)")
        base_score = float((report.get("baseline") or {}).get("score", float("nan")))
        try:
            if len([k for k in curves if not k.startswith("_")]) >= 1:
                plot_best_so_far(curves, base_score, out_dir / "best_so_far.png",
                                 title=f"{tag}: best-so-far vs evaluations")
                print(f"  wrote {out_dir / 'best_so_far.png'}")
            else:
                print("  [WARN] no trajectories: best-so-far figure skipped")
            profiles = {}
            spec_map = report.get("spec")
            base_rec = report.get("baseline") or {}
            if spec_map and base_rec.get("design"):
                z, mag, _ = axis_profile(spec_map, base_rec["design"])
                profiles["human baseline"] = (z, mag, None)
            best = report.get("best")
            if spec_map and best and best.get("design"):
                z, mag, _ = axis_profile(spec_map, best["design"])
                profiles[f"machine best ({best.get('algorithm', '?')}/seed{best.get('seed')})"] = (z, mag, None)
            if profiles:
                plot_axis_profiles(profiles, out_dir / "axis_profile.png", title=f"{tag}: {title_suffix()}")
                print(f"  wrote {out_dir / 'axis_profile.png'}")
            else:
                print("  [WARN] no designs in the report: axis-profile figure skipped")
        except Exception as exc:                            # pragma: no cover
            print(f"  [WARN] plotting failed ({exc!r}); markdown table still written")
    elif make_figures:
        print(f"  [WARN] matplotlib unavailable ({_MPL_ERR!r}); figures skipped, "
              f"markdown table written instead")
    if verbose:
        print(table)
    return (0 if ok else 1)


def title_suffix():
    return "on-axis |B|(z): human baseline vs machine best"


def main(argv=None) -> int:
    ap = argparse.ArgumentParser(description="Forge benchmark figures + tables (stage G)")
    ap.add_argument("target", nargs="?", default=None,
                    help="runs/<tag> directory or a results.json path")
    ap.add_argument("--registry", default=None, help="registry.jsonl (default: <run>/registry.jsonl)")
    ap.add_argument("--no-figures", action="store_true")
    args = ap.parse_args(argv)
    if not args.target:
        root = Path(__file__).resolve().parents[2]
        args.target = root / "runs" / "phase0"
    try:
        return analyze(args.target, args.registry, make_figures=not args.no_figures)
    except FileNotFoundError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main())
