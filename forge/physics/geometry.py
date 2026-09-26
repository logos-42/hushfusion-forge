"""Parameterisation of the device: coaxial circular filament coils + eval grids.

Design vector layout (length 3K for K coils)::

    x = [r_0 .. r_{K-1},  z_0 .. z_{K-1},  I_0 .. I_{K-1}]

Canonical form: coils are always stored sorted by ``z`` (ascending). This kills
the K! permutation degeneracy of the search space without constraining the
geometry, so an optimiser never wastes evaluations on re-orderings of the same
machine.

The evaluation grids are stacked into a *single* sample array so that one field
pass per coil covers the axis, the midplane volume and the cell volume. This is
a pure speed decision (~3x): the search does tens of thousands of evaluations
and the physics is identical.
"""

from __future__ import annotations

from dataclasses import dataclass

import numpy as np

from ..config import Spec


@dataclass(frozen=True)
class Coil:
    """A single circular filament loop, axis on the z-axis."""

    radius: float   # [m]
    z: float        # [m]
    current: float  # [A] ampere-turns (+phi direction)

    def as_dict(self) -> dict:
        return {"radius_m": self.radius, "z_m": self.z, "current_A": self.current}


def vector_to_coils(x: np.ndarray, spec: Spec) -> tuple[Coil, ...]:
    """Decode a design vector into coils (bounds-clipped, z-sorted)."""
    x = np.asarray(x, dtype=float).ravel()
    if x.size != spec.n_params:
        raise ValueError(f"design vector has {x.size} entries, spec wants {spec.n_params}")
    k = spec.n_coils
    r = np.clip(x[:k], *spec.bounds.radius)
    z = np.clip(x[k : 2 * k], *spec.bounds.z)
    i = np.clip(x[2 * k :], *spec.bounds.current)
    order = np.argsort(z, kind="stable")
    return tuple(Coil(float(r[j]), float(z[j]), float(i[j])) for j in order)


def coils_to_vector(coils) -> np.ndarray:
    """Encode coils (any order) into the canonical design vector."""
    ordered = sorted(coils, key=lambda c: c.z)
    return np.array(
        [c.radius for c in ordered] + [c.z for c in ordered] + [c.current for c in ordered],
        dtype=float,
    )


def random_design(rng: np.random.Generator, spec: Spec) -> np.ndarray:
    """Uniform random sample of the search box, in canonical order."""
    from ..config import lower_upper

    lo, hi = lower_upper(spec)
    x = lo + rng.random(spec.n_params) * (hi - lo)
    return coils_to_vector(vector_to_coils(x, spec))


# ----------------------------------------------------------------------------
# evaluation grids (built once per spec, reused for every candidate)
# ----------------------------------------------------------------------------


@dataclass(frozen=True)
class Grids:
    """Stacked sample points: [axis | midplane volume | cell volume]."""

    stack_r: np.ndarray      # (N,) cylindrical radius of every sample
    stack_z: np.ndarray      # (N,) axial coordinate
    n_axis: int              # axis samples occupy stack[:n_axis]  (r = 0)
    n_mid: int               # midplane volume occupies the next n_mid
    axis_z: np.ndarray       # on-axis sample positions, for reporting
    axis_in_cell: np.ndarray # (n_axis,) bool, |z| <= z_cell
    mid_r: np.ndarray        # (n_mid,) radii of the midplane volume
    cell_r: np.ndarray       # (n_cell,) radii of the cell volume
    cell_z: np.ndarray

    @property
    def n_cell(self) -> int:
        return self.stack_r.size - self.n_axis - self.n_mid


def build_grids(spec: Spec) -> Grids:
    axis_z = np.linspace(-spec.z_axis_max, spec.z_axis_max, spec.n_axis)

    r = np.linspace(0.0, spec.r_plasma, spec.n_vol_r)
    z_mid = np.linspace(-spec.z_mid, spec.z_mid, 5)
    rm, zm = np.meshgrid(r, z_mid, indexing="ij")

    z_cell = np.linspace(-spec.z_cell, spec.z_cell, spec.n_vol_z)
    rc, zc = np.meshgrid(r, z_cell, indexing="ij")

    stack_r = np.concatenate([np.zeros(spec.n_axis), rm.ravel(), rc.ravel()])
    stack_z = np.concatenate([axis_z, zm.ravel(), zc.ravel()])

    return Grids(
        stack_r=stack_r,
        stack_z=stack_z,
        n_axis=axis_z.size,
        n_mid=rm.size,
        axis_z=axis_z,
        axis_in_cell=np.abs(axis_z) <= spec.z_cell,
        mid_r=rm.ravel(),
        cell_r=rc.ravel(),
        cell_z=zc.ravel(),
    )
