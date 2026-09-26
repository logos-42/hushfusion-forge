"""Experiment runner: score a design, give it an id, append it to the registry.

This is the layer that turns "an optimiser called a function" into "an
experiment with a lineage". Everything a search algorithm does passes through
here, which is what makes the registry complete by construction rather than by
discipline.
"""

from __future__ import annotations

import time

import numpy as np

from ..objective import EvalResult, Evaluator
from ..registry import Registry


class ExperimentRunner:
    def __init__(self, registry: Registry, evaluator: Evaluator, tag: str = "phase0"):
        self.registry = registry
        self.evaluator = evaluator
        self.tag = tag
        self.n_scored = 0

    def score(
        self,
        x: np.ndarray,
        *,
        algorithm: str,
        seed: int,
        parent: str | None = None,
        generation: int | None = None,
        eval_index: int | None = None,
        extra: dict | None = None,
    ) -> EvalResult:
        res = self.evaluator.evaluate(x)
        self.n_scored += 1
        record = {
            "experiment_id": self.registry.next_experiment_id(),
            "design_id": self.registry.next_design_id(),
            "parent_design": parent,
            "generation": generation,
            "algorithm": algorithm,
            "seed": int(seed),
            "eval_index": int(eval_index if eval_index is not None else self.n_scored - 1),
            "tag": self.tag,
            "timestamp": time.strftime("%Y-%m-%dT%H:%M:%S%z"),
            "score": res.score,
            "feasible": res.feasible,
            "params": {
                "radius_m": res.design[: res.metrics["n_coils"]],
                "z_m": res.design[res.metrics["n_coils"] : 2 * res.metrics["n_coils"]],
                "current_A": res.design[2 * res.metrics["n_coils"] :],
            },
            "terms": res.terms,
            "weighted": res.weighted,
            "penalties": res.penalties,
            "metrics": res.metrics,
        }
        if extra:
            record.update(extra)
        self.registry.append(record)
        res.design_id = record["design_id"]
        res.experiment_id = record["experiment_id"]
        return res
