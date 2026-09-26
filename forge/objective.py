"""The objective: one number for the machine, plus every raw term it came from.

Design principle (important):

    the composite score is NEVER stored alone.

Every evaluation records the raw physical terms (field, mirror ratio, good
volume, ripple, cost) and every constraint residual, so any weighting can be
re-derived after the fact, and a later reviewer can ask "did the machine win on
physics, or did it just buy a cheaper magnet?" without re-running anything.

Score (all terms dimensionless)::

    score = + w_field  * log10(B_mid / B_ref)
            + w_mirror * log10(max(R, 0.2) / R_ref)
            + w_volume * V_good
            - w_ripple * ripple
            - w_cost   * (cost / cost_ref)
            - w_penalty * (violations)

Constraints (soft, reported as residuals and as a boolean ``feasible``):
  * peak conductor field <= coil_field_limit
  * smallest coil-to-coil gap >= min_coil_sep
  * mirror ratio >= 1.1  (below that the device is not a mirror)
"""

from __future__ import annotations

from dataclasses import dataclass, field as dc_field

import numpy as np

from .config import Spec, lower_upper
from .physics.geometry import Grids, build_grids, coils_to_vector, vector_to_coils
from .physics.plasma_model import metrics as field_metrics
from .simulation.solver import AnalyticalVacuumSolver, Solver

MIRROR_FLOOR = 0.2   # [T-free] floor in the mirror log term, keeps it finite
MIRROR_MIN = 1.1     # below this, "not a mirror" penalty kicks in


@dataclass
class EvalResult:
    """Everything one evaluation produced."""

    score: float
    terms: dict           # raw (unweighted) physical terms
    weighted: dict        # weighted contributions to the score
    penalties: dict       # normalised constraint violations
    feasible: bool
    metrics: dict
    design: list          # canonical design vector
    design_id: str | None = None      # set by ExperimentRunner when recorded
    experiment_id: int | None = None

    def to_record(self) -> dict:
        return {
            "score": self.score,
            "terms": self.terms,
            "weighted": self.weighted,
            "penalties": self.penalties,
            "feasible": self.feasible,
            "metrics": self.metrics,
            "design": self.design,
        }


class Evaluator:
    """Scores designs. Holds the spec/grids/solver so callers stay thin."""

    def __init__(
        self,
        spec: Spec | None = None,
        solver: Solver | None = None,
        cost_ref: float | None = None,
        grids: Grids | None = None,
    ):
        self.spec = spec or Spec()
        self.solver = solver or AnalyticalVacuumSolver()
        self.grids = grids or build_grids(self.spec)
        if cost_ref is None:
            # default reference: the hand-designed textbook mirror (see
            # forge.optimization.baselines). Imported lazily to keep this module
            # importable from anywhere without cycles.
            from .optimization.baselines import textbook_mirror

            cost_ref = textbook_mirror(self.spec).cost
        self.cost_ref = float(cost_ref)
        self.n_evals = 0
        self.bounds = lower_upper(self.spec)

    # ------------------------------------------------------------------
    def terms_for(self, x: np.ndarray) -> tuple[dict, dict, dict]:
        coils = vector_to_coils(x, self.spec)
        m = field_metrics(coils, self.spec, self.grids, self.solver)
        w = self.spec.weights

        terms = {
            "field": np.log10(max(m["B_mid_T"], 1e-9) / self.spec.b_ref),
            "mirror": np.log10(max(m["mirror_ratio"], MIRROR_FLOOR) / self.spec.mirror_ref),
            "volume": m["volume_good"],
            "ripple": m["ripple"],
            "cost": m["cost_proxy"] / self.cost_ref,
        }
        weighted = {
            "field": w.field * terms["field"],
            "mirror": w.mirror * terms["mirror"],
            "volume": w.volume * terms["volume"],
            "ripple": -w.ripple * terms["ripple"],
            "cost": -w.cost * terms["cost"],
        }
        penalties = {
            "conductor_field": max(0.0, m["B_coil_max_T"] / self.spec.coil_field_limit - 1.0),
            "coil_separation": max(
                0.0,
                (self.spec.min_coil_sep - m["min_coil_gap_m"]) / self.spec.min_coil_sep,
            ),
            "not_a_mirror": max(0.0, (MIRROR_MIN - m["mirror_ratio"]) / MIRROR_MIN),
        }
        return terms, weighted, {**penalties, "metrics": m}

    def evaluate(self, x: np.ndarray) -> EvalResult:
        x = np.asarray(x, dtype=float).ravel()
        terms, weighted, pen_with_metrics = self.terms_for(x)
        m = pen_with_metrics.pop("metrics")
        total_penalty = self.spec.weights.penalty * sum(pen_with_metrics.values())
        score = float(sum(weighted.values()) - total_penalty)
        self.n_evals += 1
        return EvalResult(
            score=score,
            terms={k: float(v) for k, v in terms.items()},
            weighted={k: float(v) for k, v in weighted.items()},
            penalties={k: float(v) for k, v in pen_with_metrics.items()},
            feasible=bool(all(v <= 0.0 for v in pen_with_metrics.values())),
            metrics=m,
            design=[float(v) for v in coils_to_vector(vector_to_coils(x, self.spec))],
        )

    def score(self, x: np.ndarray) -> float:
        return self.evaluate(x).score

    # ------------------------------------------------------------------
    def as_dict(self) -> dict:
        return {
            "spec": self.spec.as_dict(),
            "solver": self.solver.name,
            "cost_ref": self.cost_ref,
        }
