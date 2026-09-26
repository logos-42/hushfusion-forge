"""Equal-budget benchmark of search methods, plus an honest robustness probe.

Three things are measured, and they answer different questions:

**最优性能** (best-of-budget)   — how good the best design gets at a fixed cost.
**收敛速度** (evals to beat the human baseline) — how much design effort the machine
    needs before it is already better than a competent engineer. This is the
    economically meaningful number: it is the *rate* of the learning loop, not
    its endpoint.
**泛化** (robustness probe) — the best design from each method is re-scored under
    perturbed requirements (field target, cell length, plasma radius) and
    compared against the human baseline re-scored under the same perturbation.
    A design that only wins inside the exact box it was searched in has not
    generalised, and saying so is part of the result.

All methods get the identical evaluation budget and identical seeds.
"""

from __future__ import annotations

import platform
import subprocess
import time
from dataclasses import replace

import numpy as np

from ..config import Spec
from ..knowledge.rules import mine_rules
from ..objective import Evaluator
from ..optimization.baselines import Baseline, textbook_mirror
from ..optimization.search import SearchResult, run_method
from ..registry import Registry
from ..simulation.runner import ExperimentRunner

# requirement perturbations for the generalisation probe
SPEC_VARIANTS: tuple[tuple[str, dict], ...] = (
    ("b_ref=0.8T", {"b_ref": 0.8}),
    ("b_ref=1.2T", {"b_ref": 1.2}),
    ("z_cell=0.60m", {"z_cell": 0.60}),
    ("z_cell=1.00m", {"z_cell": 1.00}),
    ("r_plasma=0.12m", {"r_plasma": 0.12}),
    ("r_plasma=0.18m", {"r_plasma": 0.18}),
)


def aggregate(runs: list[SearchResult], baseline_score: float) -> dict:
    """Per-method statistics across seeds. Never a single-seed claim."""
    out: dict[str, dict] = {}
    by_method: dict[str, list[SearchResult]] = {}
    for r in runs:
        by_method.setdefault(r.algorithm, []).append(r)
    for method, rs in by_method.items():
        best = np.array([r.best_score for r in rs], dtype=float)
        beat = [r.evals_to_beat for r in rs if r.evals_to_beat is not None]
        out[method] = {
            "n_seeds": len(rs),
            "budget": rs[0].budget,
            "best_mean": float(np.mean(best)),
            "best_std": float(np.std(best, ddof=0)),
            "best_min": float(np.min(best)),
            "best_max": float(np.max(best)),
            "n_beating_baseline": len(beat),
            "frac_beating_baseline": len(beat) / len(rs),
            "evals_to_beat_mean": float(np.mean(beat)) if beat else None,
            "evals_to_beat_median": float(np.median(beat)) if beat else None,
            "baseline_score": float(baseline_score),
        }
    return out


def robustness_probe(
    spec: Spec,
    designs: dict[str, list[float]],
    variants: tuple[tuple[str, dict], ...] = SPEC_VARIANTS,
) -> dict:
    """Re-score designs under perturbed requirements, relative to the baseline.

    For every variant the human baseline is re-solved *for that variant* and used
    as the zero line, so the number reported is "still better than a human
    re-designing for the new requirement, and by how much".
    """
    per_design: dict[str, dict[str, float]] = {k: {} for k in designs}
    variant_scores: dict[str, float] = {}
    for name, kw in variants:
        spec_v = replace(spec, **kw)
        base_v = textbook_mirror(spec_v)
        ev = Evaluator(spec_v, cost_ref=base_v.cost)
        s_base = ev.evaluate(np.asarray(base_v.design, float)).score
        variant_scores[name] = float(s_base)
        for dname, x in designs.items():
            per_design[dname][name] = float(ev.evaluate(np.asarray(x, float)).score - s_base)
    summary = {
        dname: {
            "mean_delta_vs_baseline": float(np.mean(list(vals.values()))) if vals else 0.0,
            "worst_delta_vs_baseline": float(np.min(list(vals.values()))) if vals else 0.0,
            "n_variants_winning": int(sum(1 for v in vals.values() if v > 0)),
            "n_variants": len(vals),
        }
        for dname, vals in per_design.items()
    }
    return {
        "variants": [n for n, _ in variants],
        "baseline_score_per_variant": variant_scores,
        "per_design": per_design,
        "summary": summary,
    }


def _git_commit() -> str | None:
    try:
        return (
            subprocess.check_output(["git", "rev-parse", "--short", "HEAD"], stderr=subprocess.DEVNULL)
            .decode()
            .strip()
        )
    except Exception:
        return None


def run_benchmark(
    registry: Registry,
    spec: Spec | None = None,
    *,
    budget: int = 1000,
    seeds: tuple[int, ...] = (0, 1, 2),
    methods: tuple[str, ...] = ("random", "lhs", "evolution", "evolution_warm"),
    baseline: Baseline | None = None,
    tag: str = "phase0",
    progress: callable | None = None,
) -> dict:
    """Run every method at every seed with an identical budget. Returns a report dict."""
    spec = spec or Spec()
    baseline = baseline or textbook_mirror(spec)
    evaluator = Evaluator(spec, cost_ref=baseline.cost)
    runner = ExperimentRunner(registry, evaluator, tag=tag)

    # the human baseline is recorded too: it is design D0001 and the root of the
    # lineage tree, so "which branch improved on the human?" is answerable
    base_res = runner.score(
        np.asarray(baseline.design, float),
        algorithm="human_baseline",
        seed=-1,
        generation=0,
        eval_index=0,
        extra={"note": baseline.note},
    )
    baseline_score = base_res.score

    runs: list[SearchResult] = []
    for method in methods:
        for seed in seeds:
            t0 = time.perf_counter()
            res = run_method(
                method, runner, spec, seed, budget=budget, baseline_score=baseline_score
            )
            runs.append(res)
            if progress:
                progress(method, seed, res, time.perf_counter() - t0)

    agg = aggregate(runs, baseline_score)

    # generalisation probe: each method's single best design across seeds
    designs: dict[str, list[float]] = {}
    for method in methods:
        rs = [r for r in runs if r.algorithm == method]
        if rs:
            best = max(rs, key=lambda r: r.best_score)
            designs[method] = best.best_design
    designs["human_baseline"] = list(baseline.design)

    best_run = max(runs, key=lambda r: r.best_score) if runs else None

    return {
        "meta": {
            "tag": tag,
            "timestamp": time.strftime("%Y-%m-%dT%H:%M:%S%z"),
            "budget": budget,
            "seeds": list(seeds),
            "methods": list(methods),
            "git_commit": _git_commit(),
            "platform": platform.platform(),
            "python": platform.python_version(),
            "host_cpu": platform.processor(),
        },
        "spec": spec.as_dict(),
        "solver": evaluator.solver.name,
        "cost_ref": evaluator.cost_ref,
        "baseline": {
            "name": baseline.name,
            "note": baseline.note,
            "score": baseline_score,
            "feasible": base_res.feasible,
            "terms": base_res.terms,
            "weighted": base_res.weighted,
            "penalties": base_res.penalties,
            "metrics": base_res.metrics,
            "design": list(base_res.design),
            "cost_proxy": baseline.cost,
            "design_id": base_res.design_id,
        },
        "runs": [r.to_dict() for r in runs],
        "history": {f"{r.algorithm}@{r.seed}": r.history for r in runs},
        "aggregate": agg,
        "robustness": robustness_probe(spec, designs),
        "best": (
            {
                "design_id": best_run.best_design_id,
                "algorithm": best_run.algorithm,
                "seed": best_run.seed,
                "score": best_run.best_score,
                "terms": best_run.best_terms,
                "metrics": best_run.best_metrics,
                "design": best_run.best_design,
                "feasible": best_run.best_feasible,
            }
            if best_run
            else None
        ),
        "registry_summary": registry.summary(),
    }


__all__ = ["run_benchmark", "aggregate", "robustness_probe", "SPEC_VARIANTS", "mine_rules", "replace"]
