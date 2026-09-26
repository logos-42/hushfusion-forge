"""Field-structure metrics of a coil set — the quantities the objective scores.

Everything here is a *vacuum field* property. No plasma: no pressure, no
diamagnetic response, no equilibrium. The metrics are the standard knobs of a
magnetic-mirror central cell, and each one is computable from a single field
pass over the stacked sample points:

``B_mid``      volume-averaged |B| in the midplane plasma volume — the field the
               plasma actually sits in (not the on-axis value).
``B_throat``   maximum |B| on the axis anywhere in +-z_axis_max — the field that
               sets the loss cone.
``mirror_ratio``  B_throat / B_mid. R <= 1 means no mirror confinement at all.
``volume_good``  fraction of the central-cell volume with |B| <= confine_factor*B_mid
               — the volume that can hold plasma near the design field.
``ripple``     normalised amplitude of *non-monotonic* structure on the axis
               inside the cell. A single-peaked or monotonic profile gives
               exactly 0 by construction: the term exists only to penalise field
               dips caused by discrete coils sitting inside the cell (the
               mirror-cell analogue of toroidal ripple).
``B_coil_max`` peak field on a conductor = field from all *other* coils at that
               coil's location + the winding-pack self-field anchor
               (``Spec.self_field``).
``cost_proxy`` sum_k I_k^2 r_k — ohmic-dissipation proxy (P = I^2 R with R ~ r
               at fixed conductor cross-section). Normalised by the human
               baseline inside the objective, so 1.0 == "as expensive as the
               hand-designed reference".
"""

from __future__ import annotations

import numpy as np

from ..config import MU0, Spec
from .geometry import Coil, Grids

PROXIMITY_FLOOR = 5e-3  # [m] keeps the 1/alpha^2 self-location term finite


def axis_ripple(b_axis_cell: np.ndarray, b_mid: float, prominence: float = 0.05) -> float:
    """Normalised amplitude of non-monotonic structure on the axis.

    Sums |B_peak - B_adjacent_valley| over consecutive interior extrema that
    alternate (max, min), keeping only structures deeper than
    ``prominence * b_mid``. Monotonic / single-peaked profiles return 0.0.
    """
    n = b_axis_cell.size
    if n < 5 or b_mid <= 0.0:
        return 0.0
    mid = b_axis_cell[1:-1]
    left = b_axis_cell[:-2]
    right = b_axis_cell[2:]
    is_max = (mid > left) & (mid > right)
    is_min = (mid < left) & (mid < right)
    hits = np.nonzero(is_max | is_min)[0]
    if hits.size < 2:
        return 0.0
    signs = np.where(is_max[hits], 1, -1)
    idx = hits + 1
    total = 0.0
    for k in range(hits.size - 1):
        if signs[k] * signs[k + 1] < 0:
            amp = abs(b_axis_cell[idx[k]] - b_axis_cell[idx[k + 1]])
            if amp > prominence * b_mid:
                total += amp
    return float(total / b_mid)


def min_coil_gap(coils: tuple[Coil, ...]) -> float:
    """Smallest 3-D distance between two coil centres [m] (inf for a single coil)."""
    gaps = [
        float(np.hypot(a.radius - b.radius, a.z - b.z))
        for i, a in enumerate(coils)
        for b in coils[i + 1 :]
    ]
    return min(gaps) if gaps else float("inf")


def coil_conductor_field(coils: tuple[Coil, ...], spec: Spec, solver) -> tuple[float, bool]:
    """Peak field on the conductors [T] and whether the proximity floor was hit.

    Vectorised over the *source* coil: one field call per coil evaluates that
    coil's contribution at every *other* coil's location, which turns K*(K-1)
    single-point calls into K calls. Same physics, ~3x faster.
    """
    k = len(coils)
    floor_hit = False
    b_others = np.zeros(k)
    for j, src in enumerate(coils):
        targets = [i for i in range(k) if i != j]
        if not targets:
            continue
        rr = np.array([coils[i].radius for i in targets])
        zz = np.array([coils[i].z for i in targets])
        gaps = np.hypot(rr - src.radius, zz - src.z)
        if float(np.min(gaps)) < PROXIMITY_FLOOR:
            floor_hit = True
        b_others[targets] += solver.field_magnitude(src_one(coils, j), rr, zz - src.z)
    peak = float(np.max(b_others + spec.self_field())) if k else 0.0
    return peak, floor_hit


def src_one(coils: tuple[Coil, ...], j: int) -> tuple[Coil, ...]:
    c = coils[j]
    return (Coil(c.radius, 0.0, c.current),)


def metrics(coils: tuple[Coil, ...], spec: Spec, grids: Grids, solver) -> dict:
    """All objective-relevant field metrics for one coil set (single field pass)."""
    b_all = solver.field_magnitude(coils, grids.stack_r, grids.stack_z)
    b_axis = b_all[: grids.n_axis]
    b_mid = float(np.mean(b_all[grids.n_axis : grids.n_axis + grids.n_mid]))
    b_cell = b_all[grids.n_axis + grids.n_mid :]

    i_throat = int(np.argmax(b_axis))
    b_throat = float(b_axis[i_throat])

    volume_good = float(np.mean(b_cell <= spec.confine_factor * max(b_mid, 1e-9)))
    ripple = axis_ripple(b_axis[grids.axis_in_cell], b_mid)
    b_coil, floor_hit = coil_conductor_field(coils, spec, solver)
    cost = float(sum(c.current**2 * c.radius for c in coils))
    gap = min_coil_gap(coils)

    return {
        "B_mid_T": b_mid,
        "B_throat_T": b_throat,
        "z_throat_m": float(grids.axis_z[i_throat]),
        "mirror_ratio": b_throat / max(b_mid, 1e-9),
        "volume_good": volume_good,
        "ripple": ripple,
        "B_coil_max_T": b_coil,
        "min_coil_gap_m": gap,
        "cost_proxy": cost,
        "coil_proximity_floor_hit": floor_hit,
        "n_coils": len(coils),
        "mu0": MU0,
    }
