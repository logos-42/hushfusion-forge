"""The design environment — the Phase-1 landing pad for OaK / RL.

What is here: a complete, working environment. What is *not* here: a learned
policy. That asymmetry is deliberate. Phase 0 asks "can the machine find a
better design than a human?", and a search algorithm answers it. Phase 1 asks
"can the system learn to propose designs, and remember what it learned?", and
only then does a policy belong here.

Interface (gym-like, no gym dependency)::

    obs = env.reset(x0)                       # design vector + normalised metrics
    obs, reward, terminated, truncated, info = env.step(action)

* action   : delta on the design vector, normalised to [-1, 1]^D, scaled by
             ``delta_scale`` of each parameter's range
* reward   : score(after) - score(before) — a potential-based difference, so the
             episode return telescopes exactly to score(final) - score(initial)
             and no shaping constant has to be invented
* episode  : ``max_steps`` design modifications

``random_policy_rollout`` is provided as the honest reference: it is a policy,
it runs, and it is deliberately dumb, so a Phase-1 learner can be compared
against it at equal budget.
"""

from __future__ import annotations

import numpy as np

from ..config import Spec, lower_upper
from ..objective import EvalResult
from ..physics.geometry import coils_to_vector, random_design, vector_to_coils
from ..simulation.runner import ExperimentRunner

OBS_METRIC_KEYS = (
    "B_mid_T",
    "B_throat_T",
    "mirror_ratio",
    "volume_good",
    "ripple",
    "B_coil_max_T",
    "cost_proxy",
)


class FusionDesignEnv:
    def __init__(
        self,
        runner: ExperimentRunner,
        spec: Spec | None = None,
        max_steps: int = 20,
        delta_scale: float = 0.15,
        seed: int = 0,
    ):
        self.runner = runner
        self.spec = spec or runner.evaluator.spec
        self.max_steps = int(max_steps)
        self.delta_scale = float(delta_scale)
        self.lo, self.hi = lower_upper(self.spec)
        self.span = self.hi - self.lo
        self._rng = np.random.default_rng(seed)
        self._x = np.zeros(self.spec.n_params)
        self._score = -np.inf
        self._res: EvalResult | None = None
        self._t = 0
        self._algorithm = "rl_env"

    # -- spaces --------------------------------------------------------
    @property
    def action_dim(self) -> int:
        return self.spec.n_params

    @property
    def observation_dim(self) -> int:
        return self.spec.n_params + len(OBS_METRIC_KEYS)

    def _obs(self) -> np.ndarray:
        x_norm = (self._x - self.lo) / self.span
        if self._res is None:
            m_norm = np.zeros(len(OBS_METRIC_KEYS))
        else:
            raw = np.array([self._res.metrics[k] for k in OBS_METRIC_KEYS], dtype=float)
            ref = np.array([1.0, 1.0, 1.0, 1.0, 0.1, self.spec.coil_field_limit, 1.0])
            m_norm = raw / ref
        return np.concatenate([x_norm, m_norm])

    # -- api -----------------------------------------------------------
    def reset(self, x0: np.ndarray | None = None, *, record: bool = False) -> np.ndarray:
        self._t = 0
        x = random_design(self._rng, self.spec) if x0 is None else np.asarray(x0, float)
        res = self.runner.score(
            x, algorithm=self._algorithm, seed=int(self._rng.integers(1 << 30)), eval_index=self._t,
            extra={"recorded_by_reset": record},
        )
        self._x = np.asarray(res.design, float)
        self._score = res.score
        self._res = res
        return self._obs()

    def step(self, action: np.ndarray):
        if self._res is None:
            raise RuntimeError("call reset() before step()")
        action = np.clip(np.asarray(action, dtype=float).ravel(), -1.0, 1.0)
        if action.size != self.action_dim:
            raise ValueError(f"action has {action.size} entries, expected {self.action_dim}")
        prev_score = self._score
        x = np.clip(self._x + self.delta_scale * self.span * action, self.lo, self.hi)
        self._t += 1
        res = self.runner.score(
            coils_to_vector(vector_to_coils(x, self.spec)),
            algorithm=self._algorithm,
            seed=int(self._rng.integers(1 << 30)),
            parent=self._res.design_id,
            generation=self._t,
            eval_index=self._t,
        )
        reward = float(res.score - prev_score)
        self._x = np.asarray(res.design, float)
        self._score = res.score
        self._res = res
        terminated = False
        truncated = self._t >= self.max_steps
        info = {
            "score": res.score,
            "delta_score": reward,
            "feasible": res.feasible,
            "design_id": res.design_id,
            "step": self._t,
        }
        return self._obs(), reward, terminated, truncated, info


def random_policy_rollout(env: FusionDesignEnv, n_steps: int, seed: int = 0) -> dict:
    """Reference policy: uniform random design modifications. Returns a summary."""
    rng = np.random.default_rng(seed)
    obs = env.reset()
    rewards: list[float] = []
    best = env._score
    for _ in range(n_steps):
        action = rng.uniform(-1.0, 1.0, size=env.action_dim)
        obs, reward, terminated, truncated, info = env.step(action)
        rewards.append(reward)
        best = max(best, info["score"])
        if terminated or truncated:
            obs = env.reset()
    return {
        "policy": "random_actions",
        "n_steps": n_steps,
        "total_reward": float(np.sum(rewards)),
        "best_score": float(best),
        "final_score": info["score"],
    }
