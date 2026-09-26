"""Human baselines — deliberately strong, because a weak baseline proves nothing.

``helmholtz_pair``
    The textbook uniform-field solution: two identical loops separated by their
    own radius. Analytic, verifiable, and used as a physics anchor in
    ``verify_forge.py``.

``textbook_mirror``
    The honest hand-designed competitor the machine has to beat: a Helmholtz-like
    central cell plus two mirror throats. Its geometric proportions
    (r_cell = 0.50 m / half-gap 0.25 m / r_throat = 0.30 m / |z_throat| = 1.00 m /
    I_throat = 3.5 x I_cell) are the kind of numbers a competent engineer writes
    down from mirror-machine design rules, and its current scale is **solved**
    (not guessed) so that the midplane field lands exactly on the target
    ``spec.b_ref``. Beating a straw man is worthless; this one is tuned to the
    same objective the machine is scored on.
"""

from __future__ import annotations

from dataclasses import dataclass

import numpy as np
from scipy.optimize import brentq

from ..config import Spec
from ..physics.geometry import Coil, build_grids, coils_to_vector
from ..physics.plasma_model import metrics as field_metrics
from ..simulation.solver import AnalyticalVacuumSolver

_SOLVER = AnalyticalVacuumSolver()

TEXTBOOK_GEOMETRY = {
    "r_cell": 0.50,       # [m]
    "half_gap_cell": 0.25,  # [m] -> Helmholtz condition: spacing = cell radius
    "r_throat": 0.30,     # [m]
    "z_throat": 1.00,     # [m]
    "throat_current_ratio": 3.5,
}


@dataclass(frozen=True)
class Baseline:
    name: str
    coils: tuple[Coil, ...]
    cost: float
    design: list
    note: str

    def as_dict(self) -> dict:
        return {
            "name": self.name,
            "cost_proxy": self.cost,
            "design": self.design,
            "coils": [c.as_dict() for c in self.coils],
            "note": self.note,
        }


def helmholtz_pair(radius: float, current: float) -> tuple[Coil, ...]:
    """Two identical loops separated by their radius, centred on z = 0."""
    return (Coil(radius, -radius / 2.0, current), Coil(radius, radius / 2.0, current))


def _midplane_field(coils: tuple[Coil, ...], spec: Spec) -> float:
    grids = build_grids(spec)
    return float(
        np.mean(field_metrics(coils, spec, grids, _SOLVER)["B_mid_T"])
    )


def _solve_cell_current(spec: Spec, geom: dict, lo: float = 1.0e3, hi: float = 5.0e6) -> float:
    """Solve the cell current so the midplane field equals ``spec.b_ref``."""
    k = geom["throat_current_ratio"]

    def make(i_cell: float) -> tuple[Coil, ...]:
        return (
            Coil(geom["r_cell"], -geom["half_gap_cell"], i_cell),
            Coil(geom["r_cell"], geom["half_gap_cell"], i_cell),
            Coil(geom["r_throat"], -geom["z_throat"], k * i_cell),
            Coil(geom["r_throat"], geom["z_throat"], k * i_cell),
        )

    f = lambda i: _midplane_field(make(i), spec) - spec.b_ref  # noqa: E731
    grid = np.geomspace(lo, hi, 24)
    vals = [f(i) for i in grid]
    for a, b, fa, fb in zip(grid[:-1], grid[1:], vals[:-1], vals[1:]):
        if fa == 0.0:
            return float(a)
        if fa * fb < 0.0:
            return float(brentq(f, a, b, xtol=1.0, rtol=1e-10))
    raise RuntimeError("could not bracket the textbook-mirror cell current")


def textbook_mirror(spec: Spec | None = None, geom: dict | None = None) -> Baseline:
    """The hand-designed mirror the machine is asked to beat."""
    spec = spec or Spec()
    geom = {**TEXTBOOK_GEOMETRY, **(geom or {})}
    i_cell = _solve_cell_current(spec, geom)
    k = geom["throat_current_ratio"]
    coils = (
        Coil(geom["r_cell"], -geom["half_gap_cell"], i_cell),
        Coil(geom["r_cell"], geom["half_gap_cell"], i_cell),
        Coil(geom["r_throat"], -geom["z_throat"], k * i_cell),
        Coil(geom["r_throat"], geom["z_throat"], k * i_cell),
    )
    cost = float(sum(c.current**2 * c.radius for c in coils))
    return Baseline(
        name="textbook_mirror",
        coils=coils,
        cost=cost,
        design=[float(v) for v in coils_to_vector(coils)],
        note=(
            "Helmholtz-like central cell (r=0.50 m, spacing=r) + mirror throats "
            "(r=0.30 m, |z|=1.00 m, I=3.5x cell). Cell current solved so the "
            "midplane field hits spec.b_ref exactly; not a straw man."
        ),
    )
