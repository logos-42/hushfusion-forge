"""Knowledge layer: turn a registry into design rules, with their evidence.

A rule is a *replicated, direction-bearing, quantified* statement about the
design space — not a hunch and not a fitted model. The mining procedure is
deliberately conservative because the whole point of the knowledge base is that
it should be trusted by the next design round:

1. every (parameter, score-term) pair gets a Spearman rank correlation
   **computed per run** (algorithm x seed), i.e. on independent samples;
2. a candidate rule must have the *same sign in every run* — that is the
   replication test, and the reported confidence is the fraction of runs that
   agree;
3. the direction is restated in engineering terms with a decile contrast: median
   of the term for designs in the top decile of the parameter vs the bottom
   decile, so the rule carries a magnitude and not just a sign;
4. the scope is written down (how many designs, which search box, which physics
   model), because a rule mined in a vacuum-field box is a hypothesis about that
   box, not a law of nature.

Phase 1 uses these rules to warm-start searches, so the loop is: designs ->
rules -> better designs.
"""

from __future__ import annotations

import json
import math
from dataclasses import asdict, dataclass
from pathlib import Path

import numpy as np
from scipy.stats import spearmanr

from ..config import Spec

TERM_LABELS_ZH = {
    "field": "中场强 B_mid",
    "mirror": "镜比 R",
    "volume": "可用场体积 V_good",
    "ripple": "纹波 ripple",
    "cost": "造价代理 cost",
    "score": "综合评分 score",
}

PARAM_LABELS_ZH = {
    "r": "半径 r",
    "z": "轴向位置 z",
    "I": "电流(安匝) I",
}


@dataclass
class Rule:
    rule_id: str
    parameter: str
    term: str
    rho: float            # min |rho| across runs (worst-case effect size)
    sign_agreement: float  # fraction of runs whose rho has the same sign
    n_designs: int
    n_runs: int
    decile_low: float     # median term in the parameter's bottom decile
    decile_high: float    # median term in the top decile
    statement: str
    statement_en: str
    scope: str

    def as_dict(self) -> dict:
        return asdict(self)


def parameter_names(spec: Spec) -> list[str]:
    k = spec.n_coils
    return (
        [f"r_{i}" for i in range(k)]
        + [f"z_{i}" for i in range(k)]
        + [f"I_{i}" for i in range(k)]
    )


def _flat(record: dict) -> dict:
    """Flatten a registry record into a scalar dict of params + terms."""
    out = {}
    k = len(record["params"]["radius_m"])
    for i in range(k):
        out[f"r_{i}"] = record["params"]["radius_m"][i]
        out[f"z_{i}"] = record["params"]["z_m"][i]
        out[f"I_{i}"] = record["params"]["current_A"][i]
    for key, val in record["terms"].items():
        out[f"term:{key}"] = val
    out["score"] = record["score"]
    return out


def mine_rules(
    records: list[dict],
    spec: Spec,
    min_n: int = 150,
    min_abs_rho: float = 0.20,
    top_k: int = 12,
) -> list[Rule]:
    """Mine replicated, direction-bearing rules from registry records."""
    feasible = [r for r in records if r.get("feasible", False)]
    if len(feasible) < min_n:
        return []

    runs: dict[tuple, list[dict]] = {}
    for r in feasible:
        runs.setdefault((r["algorithm"], r["seed"]), []).append(r)
    runs = {k: v for k, v in runs.items() if len(v) >= max(20, min_n // 8)}
    if len(runs) < 2:
        return []

    params = parameter_names(spec)
    terms = list(feasible[0]["terms"].keys()) + ["score"]
    candidates: list[Rule] = []

    for p in params:
        for t in terms:
            rhos: list[float] = []
            for recs in runs.values():
                xs = np.array([_flat(r)[p] for r in recs], dtype=float)
                if t == "score":
                    ys = np.array([r["score"] for r in recs], dtype=float)
                else:
                    ys = np.array([r["terms"][t] for r in recs], dtype=float)
                if xs.size < 20 or np.ptp(xs) == 0.0 or np.ptp(ys) == 0.0:
                    continue
                rho = spearmanr(xs, ys).statistic
                if np.isfinite(rho):
                    rhos.append(float(rho))
            if len(rhos) < 2:
                continue
            arr = np.array(rhos)
            pos = float(np.mean(arr > 0))
            sign_agreement = max(pos, 1.0 - pos)
            rho_min = float(np.sign(np.median(arr)) * np.min(np.abs(arr)))
            if sign_agreement < 1.0 or abs(rho_min) < min_abs_rho:
                continue

            # decile contrast on all feasible designs, for magnitude
            xs = np.array([_flat(r)[p] for r in feasible], dtype=float)
            ys = np.array(
                [r["score"] for r in feasible] if t == "score" else [r["terms"][t] for r in feasible],
                dtype=float,
            )
            q1, q9 = np.quantile(xs, [0.1, 0.9])
            lo = float(np.median(ys[xs <= q1]))
            hi = float(np.median(ys[xs >= q9]))
            candidates.append(
                Rule(
                    rule_id="",
                    parameter=p,
                    term=t,
                    rho=rho_min,
                    sign_agreement=sign_agreement,
                    n_designs=len(feasible),
                    n_runs=len(runs),
                    decile_low=lo,
                    decile_high=hi,
                    statement="",
                    statement_en="",
                    scope="",
                )
            )

    candidates.sort(key=lambda r: abs(r.rho), reverse=True)
    chosen = candidates[:top_k]

    scope = (
        f"真空场模型(v0.1) / 搜索盒 r∈{spec.bounds.radius} m, z∈{spec.bounds.z} m, "
        f"I∈{spec.bounds.current} A / n_coils={spec.n_coils} / "
        f"{len(feasible)} 个可行设计, {len(runs)} 次独立 run / 每 run Spearman ρ 同号"
    )
    for i, rule in enumerate(chosen, start=1):
        rule.rule_id = f"R{i:03d}"
        rule.scope = scope
        direction = "增大" if rule.rho > 0 else "减小"
        term_zh = TERM_LABELS_ZH.get(rule.term, rule.term)
        prefix = rule.parameter.split("_")[0]
        p_zh = f"{PARAM_LABELS_ZH.get(prefix, prefix)}{rule.parameter.split('_')[1]}"
        rule.statement = (
            f"{p_zh} {direction} → {term_zh} {'上升' if rule.rho > 0 else '下降'}；"
            f"最弱 ρ={rule.rho:+.2f}（全部 {rule.n_runs} 次 run 同号）"
        )
        rule.statement_en = (
            f"{rule.parameter} {'up' if rule.rho > 0 else 'down'} -> {rule.term} "
            f"{'up' if rule.rho > 0 else 'down'} (worst-case rho={rule.rho:+.2f}, "
            f"same sign in all {rule.n_runs} runs)"
        )
    return chosen


def write_rules_md(rules: list[Rule], path: str | Path, spec: Spec, n_records: int, tag: str) -> Path:
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    lines = [
        "# Forge Design Rules",
        "",
        f"- tag: `{tag}`",
        f"- registry records mined: {n_records}",
        f"- rules kept: {len(rules)}",
        "- mining: per-run Spearman rank correlation, kept only if the sign is identical in every run;"
        " confidence = sign agreement across runs; magnitude = decile contrast (bottom 10% vs top 10% of the parameter).",
        "",
        "> 这些规则是**假设**，不是定律：它们描述了 v0.1 真空场模型 + 当前搜索盒内的设计空间结构。",
        "> 每一条都带证据（ρ、同号 run 数、分位对比），可以在 Phase 1 被用作搜索先验，也可以被新数据推翻。",
        "",
        "| # | 规则 | ρ | 同号 run | 低分位 median | 高分位 median |",
        "|---|---|---:|---:|---:|---:|",
    ]
    for r in rules:
        lines.append(
            f"| {r.rule_id} | {r.statement} | {r.rho:+.2f} | {r.n_runs}/{r.n_runs} | "
            f"{r.decile_low:+.3f} | {r.decile_high:+.3f} |"
        )
    lines += ["", "## Scope", "", rules[0].scope if rules else "(no rules kept)", ""]
    if rules:
        lines += ["## Machine-readable", "", "```json", json.dumps([r.as_dict() for r in rules], ensure_ascii=False, indent=2), "```", ""]
    path.write_text("\n".join(lines), encoding="utf-8")
    return path


def load_rules(path: str | Path) -> list[dict]:
    """Read rules back out of the markdown's embedded JSON block."""
    text = Path(path).read_text(encoding="utf-8")
    marker = "```json"
    if marker not in text:
        return []
    blob = text.split(marker, 1)[1].split("```", 1)[0]
    return json.loads(blob)


def rule_expectation(rules: list[dict], x: np.ndarray, spec: Spec) -> float:
    """Scalar prior from the rules: sign-weighted push direction for a design.

    Phase 1 uses this to bias proposals; it is intentionally a crude linear
    prior so that its contribution is measurable (and removable) in an ablation.
    """
    if not rules:
        return 0.0
    names = parameter_names(spec)
    vals = {}
    k = spec.n_coils
    for i in range(k):
        vals[f"r_{i}"] = x[i]
        vals[f"z_{i}"] = x[k + i]
        vals[f"I_{i}"] = x[2 * k + i]
    total = 0.0
    for r in rules:
        lo, hi = spec.bounds.radius if r["parameter"].startswith("r_") else (
            spec.bounds.z if r["parameter"].startswith("z_") else spec.bounds.current
        )
        u = (vals[r["parameter"]] - lo) / (hi - lo)
        total += r["rho"] * (u - 0.5)
    return float(total / max(len(rules), 1))


__all__ = [
    "Rule",
    "mine_rules",
    "write_rules_md",
    "load_rules",
    "rule_expectation",
    "parameter_names",
    "TERM_LABELS_ZH",
    "PARAM_LABELS_ZH",
    "math",
]
