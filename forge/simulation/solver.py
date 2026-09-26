"""Field solvers, behind one interface.

``AnalyticalVacuumSolver``
    Exact closed-form circular-loop magnetostatics in vacuum. The default, and
    the only solver used for results in this repository's Phase 0 report.

``DiscreteFilamentSolver``
    The same physics computed by direct numerical Biot-Savart summation. No
    shared code path with the closed form, so it is used by ``verify_forge.py``
    as an independent check of the analytical solver, and can be selected for
    convergence studies.

Phase 2 will add higher-fidelity solvers (finite-thickness winding packs,
conductor magnetisation, eddy currents, and a plasma equilibrium) behind this
same two-method interface. Nothing downstream of the solver knows which one is
in use, which is the point: swapping in real physics must not require touching
the objective, the search or the registry.
"""

from __future__ import annotations

from typing import Protocol, Sequence

import numpy as np

from ..physics.geometry import Coil
from ..physics.magnetic_field import (
    coilset_field_magnitude,
    loop_field,
    loop_field_discrete,
)


class Solver(Protocol):
    name: str

    def field_magnitude(self, coils: Sequence[Coil], r, z) -> np.ndarray: ...

    def field_components(self, coils: Sequence[Coil], r, z): ...


class AnalyticalVacuumSolver:
    """Exact circular-filament magnetostatics via complete elliptic integrals."""

    name = "analytic-vacuum-loops"

    def field_components(self, coils: Sequence[Coil], r, z):
        from ..physics.magnetic_field import coilset_field

        return coilset_field(coils, r, z)

    def field_magnitude(self, coils: Sequence[Coil], r, z) -> np.ndarray:
        return coilset_field_magnitude(coils, r, z)


class DiscreteFilamentSolver:
    """Independent implementation: direct Biot-Savart sum over segments."""

    name = "discrete-filaments"

    def __init__(self, n_seg: int = 720):
        self.n_seg = int(n_seg)

    def field_components(self, coils: Sequence[Coil], r, z):
        r, z = np.broadcast_arrays(
            np.asarray(r, dtype=float), np.asarray(z, dtype=float)
        )
        br = np.zeros_like(r)
        bz = np.zeros_like(z)
        for c in coils:
            br_c, bz_c = loop_field_discrete(
                c.radius, c.current, r, z - c.z, n_seg=self.n_seg
            )
            br += br_c
            bz += bz_c
        return br, bz

    def field_magnitude(self, coils: Sequence[Coil], r, z) -> np.ndarray:
        br, bz = self.field_components(coils, r, z)
        return np.sqrt(br * br + bz * bz)


def analytic_loop_on_axis(radius: float, current: float, z) -> np.ndarray:
    """Textbook anchor: B_z on the axis of a single loop."""
    from ..config import MU0

    z = np.asarray(z, dtype=float)
    a2 = radius * radius
    return MU0 * current * a2 / (2.0 * (a2 + z * z) ** 1.5)


def analytic_helmholtz(radius: float, current: float) -> float:
    """Textbook anchor: B at the centre of a Helmholtz pair, spacing = radius."""
    from ..config import MU0

    return (4.0 / 5.0) ** 1.5 * MU0 * current / radius


__all__ = [
    "Solver",
    "AnalyticalVacuumSolver",
    "DiscreteFilamentSolver",
    "analytic_loop_on_axis",
    "analytic_helmholtz",
    "loop_field",
]
