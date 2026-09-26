"""Search algorithms over the design space.

Every algorithm here has the same contract: it gets an evaluation *budget*, it
spends it through the :class:`~forge.simulation.runner.ExperimentRunner` (so the
registry is complete), and it returns its best-so-far trajectory, which is the
only honest way to compare algorithms at equal cost.

Methods
-------
``random``          uniform i.i.d. sampling — the null hypothesis
``lhs``             latin-hypercube (stratified space-filling) sampling
``evolution``       (mu+lambda) evolution strategy, mutation variance annealed
                    linearly with the spent budget, elitist selection
``evolution_warm``  identical, except the human baseline seeds the initial
                    population — the cheapest possible form of *knowledge
                    reuse*, included because "does inherited design knowledge
                    pay?" is a measurable question, not a slogan

A true grid search is deliberately **not** provided: at D = 12 dimensions, even
5 points per axis is 2.4e8 evaluations. ``lhs`` is the honest stand-in (a
non-adaptive, structured, space-filling baseline), and the substitution is
stated in the report rather than papered over.
"""

from __future__ import annotations

from dataclasses import asdict, dataclass

import numpy as np

from ..config import Spec, lower_upper
from ..physics.geometry import coils_to_vector, random_design, vector_to_coils
from ..simulation.runner import ExperimentRunner


@dataclass
class SearchResult:
    algorithm: str
    seed: int
    budget: int
    n_evals: int
    best_score: float
    best_design: list
    best_terms: dict
    best_metrics: dict
    best_design_id: str | None
    best_feasible: bool
    evals_to_beat: int | None        # eval index where best-so-far passed the human baseline
    history: list[float]             # best-so-far after each evaluation

    def to_dict(self) -> dict:
        d = asdict(self)
        d.pop("history")  # written separately, it is long
        d["history_len"] = len(self.history)
        return d


class _Tracker:
    """Running best-so-far + how many evaluations it took to pass a target."""

    def __init__(self, baseline_score: float | None):
        self.baseline_score = baseline_score
        self.history: list[float] = []
        self.best = -np.inf
        self.best_res = None
        self.evals_to_beat: int | None = None
        self.n = 0

    def update(self, res) -> None:
        if res.score > self.best:
            self.best = res.score
            self.best_res = res
        if (
            self.baseline_score is not None
            and self.evals_to_beat is None
            and self.best > self.baseline_score
        ):
            self.evals_to_beat = self.n
        self.history.append(float(self.best))
        self.n += 1


def _finish(algorithm: str, seed: int, budget: int, tracker: _Tracker) -> SearchResult:
    r = tracker.best_res
    return SearchResult(
        algorithm=algorithm,
        seed=int(seed),
        budget=int(budget),
        n_evals=tracker.n,
        best_score=float(tracker.best),
        best_design=list(r.design) if r else [],
        best_terms=dict(r.terms) if r else {},
        best_metrics=dict(r.metrics) if r else {},
        best_design_id=r.design_id if r else None,
        best_feasible=bool(r.feasible) if r else False,
        evals_to_beat=tracker.evals_to_beat,
        history=list(tracker.history),
    )


# ----------------------------------------------------------------------------
# algorithms
# ----------------------------------------------------------------------------


def random_search(
    runner: ExperimentRunner,
    spec: Spec,
    seed: int,
    budget: int = 1000,
    baseline_score: float | None = None,
) -> SearchResult:
    rng = np.random.default_rng(seed)
    tracker = _Tracker(baseline_score)
    for k in range(budget):
        x = random_design(rng, spec)
        tracker.update(runner.score(x, algorithm="random", seed=seed, eval_index=k))
    return _finish("random", seed, budget, tracker)


def latin_hypercube_search(
    runner: ExperimentRunner,
    spec: Spec,
    seed: int,
    budget: int = 1000,
    baseline_score: float | None = None,
) -> SearchResult:
    rng = np.random.default_rng(seed)
    lo, hi = lower_upper(spec)
    d = spec.n_params
    n = budget
    u = np.empty((n, d))
    for j in range(d):
        strata = rng.permutation(n)
        u[:, j] = (strata + rng.random(n)) / n
    tracker = _Tracker(baseline_score)
    for k in range(n):
        x = lo + u[k] * (hi - lo)
        x = coils_to_vector(vector_to_coils(x, spec))
        tracker.update(runner.score(x, algorithm="lhs", seed=seed, eval_index=k))
    return _finish("lhs", seed, budget, tracker)


def evolution_search(
    runner: ExperimentRunner,
    spec: Spec,
    seed: int,
    budget: int = 1000,
    baseline_score: float | None = None,
    mu: int = 16,
    lam: int = 48,
    sigma0: float = 0.25,
    sigma_floor: float = 0.03,
    warm_start: np.ndarray | None = None,
    algorithm: str = "evolution",
) -> SearchResult:
    """Elitist (mu+lambda) evolution strategy with annealed Gaussian mutation."""
    rng = np.random.default_rng(seed)
    lo, hi = lower_upper(spec)
    span = hi - lo
    tracker = _Tracker(baseline_score)

    def canonical(x: np.ndarray) -> np.ndarray:
        return coils_to_vector(vector_to_coils(x, spec))

    parents: list = []  # (score, x, design_id)

    def make_child(x: np.ndarray, parent_id: str | None, generation: int):
        res = runner.score(
            canonical(x),
            algorithm=algorithm,
            seed=seed,
            parent=parent_id,
            generation=generation,
            eval_index=tracker.n,
        )
        tracker.update(res)
        return (res.score, np.asarray(res.design, float), res.design_id)

    generation = 0
    if warm_start is not None:
        parents.append(make_child(np.asarray(warm_start, float), None, generation))
    while len(parents) < mu and tracker.n < budget:
        parents.append(make_child(random_design(rng, spec), None, generation))

    while tracker.n < budget:
        generation += 1
        frac = tracker.n / float(budget)
        sigma = max(sigma_floor, sigma0 * (1.0 - frac))
        pool = list(parents)
        n_children = min(lam, budget - tracker.n)
        for _ in range(n_children):
            p = parents[rng.integers(0, len(parents))]
            x = p[1] + sigma * span * rng.standard_normal(spec.n_params)
            x = np.clip(x, lo, hi)
            pool.append(make_child(x, p[2], generation))
        pool.sort(key=lambda t: t[0], reverse=True)
        parents = pool[:mu]

    return _finish(algorithm, seed, budget, tracker)


def evolution_warm_search(
    runner: ExperimentRunner,
    spec: Spec,
    seed: int,
    budget: int = 1000,
    baseline_score: float | None = None,
    warm_start: np.ndarray | None = None,
    **kwargs,
) -> SearchResult:
    if warm_start is None:
        from .baselines import textbook_mirror

        warm_start = np.asarray(textbook_mirror(spec).design, float)
    return evolution_search(
        runner,
        spec,
        seed,
        budget=budget,
        baseline_score=baseline_score,
        warm_start=warm_start,
        algorithm="evolution_warm",
        **kwargs,
    )


METHODS = {
    "random": random_search,
    "lhs": latin_hypercube_search,
    "evolution": evolution_search,
    "evolution_warm": evolution_warm_search,
}


def run_method(
    name: str,
    runner: ExperimentRunner,
    spec: Spec,
    seed: int,
    budget: int = 1000,
    baseline_score: float | None = None,
    **kwargs,
) -> SearchResult:
    if name not in METHODS:
        raise KeyError(f"unknown method {name!r}; available: {sorted(METHODS)}")
    return METHODS[name](
        runner, spec, seed, budget=budget, baseline_score=baseline_score, **kwargs
    )
