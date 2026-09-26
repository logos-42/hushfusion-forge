"""Magnetostatic field of circular filament loops in vacuum.

Two independent implementations, deliberately kept side by side so that one can
verify the other (see ``scripts/verify_forge.py``):

``loop_field``
    Exact closed form for a circular filament, using complete elliptic
    integrals of the first and second kind (Simpson et al., NASA/TM-2001-211135,
    "Simple Analytic Expressions for the Magnetic Field of a Circular Current
    Loop"). Exact up to floating point; fast, fully vectorised.

``loop_field_discrete``
    Direct numerical Biot-Savart sum over ``n_seg`` straight segments. Slower,
    no shared code path with the closed form, and therefore an honest
    independent cross-check of it.

Convention: cylindrical coordinates ``(r, z)``, axisymmetric, current in the
+phi (azimuthal) direction, loops centred on the z-axis at ``z = coil.z``.
"""

from __future__ import annotations

import numpy as np
from scipy.special import ellipe, ellipk

from ..config import MU0

_AXIS_TOL = 1e-9


def loop_field(radius: float, current: float, r, z):
    """Exact (B_r, B_z) of one circular loop. Shapes of ``r`` and ``z`` must match."""
    r, z = np.broadcast_arrays(
        np.asarray(r, dtype=float), np.asarray(z, dtype=float)
    )
    a = float(radius)
    a2 = a * a
    r2z2 = r * r + z * z
    alpha2 = a2 + r2z2 - 2.0 * a * r          # squared distance to the nearest wire point
    beta2 = a2 + r2z2 + 2.0 * a * r
    beta = np.sqrt(np.maximum(beta2, 1e-300))
    m = np.clip(1.0 - alpha2 / np.maximum(beta2, 1e-300), 0.0, 1.0 - 1e-12)
    k_ell = ellipk(m)
    e_ell = ellipe(m)
    c = MU0 * current / np.pi
    denom = 2.0 * np.maximum(alpha2, 1e-300) * beta

    axis = np.abs(r) < _AXIS_TOL
    r_safe = np.where(axis, 1.0, r)
    br = c * z * ((a2 + r2z2) * e_ell - alpha2 * k_ell) / (denom * r_safe)
    bz = c * ((a2 - r2z2) * e_ell + alpha2 * k_ell) / denom

    if np.any(axis):
        bz_axis = MU0 * current * a2 / (2.0 * np.maximum(a2 + z * z, 1e-300) ** 1.5)
        bz = np.where(axis, bz_axis, bz)
        br = np.where(axis, 0.0, br)
    return br, bz


def coilset_field(coils, r, z):
    """Superposed (B_r, B_z) of a coil set. Shapes of ``r`` and ``z`` must match."""
    r, z = np.broadcast_arrays(
        np.asarray(r, dtype=float), np.asarray(z, dtype=float)
    )
    br = np.zeros_like(r)
    bz = np.zeros_like(z)
    for c in coils:
        br_c, bz_c = loop_field(c.radius, c.current, r, z - c.z)
        br += br_c
        bz += bz_c
    return br, bz


def coilset_field_magnitude(coils, r, z) -> np.ndarray:
    br, bz = coilset_field(coils, r, z)
    return np.sqrt(br * br + bz * bz)


def on_axis_field(coils, z) -> np.ndarray:
    """|B| on the axis (r=0). Exact, and cheaper than the general evaluation."""
    z = np.asarray(z, dtype=float)
    bz = np.zeros_like(z)
    for c in coils:
        a2 = c.radius * c.radius
        bz += MU0 * c.current * a2 / (2.0 * (a2 + (z - c.z) ** 2) ** 1.5)
    return np.abs(bz)


def loop_field_discrete(radius: float, current: float, r, z, n_seg: int = 1440):
    """Independent check: direct Biot-Savart sum over ``n_seg`` segments."""
    phi = (np.arange(n_seg) + 0.5) * 2.0 * np.pi / n_seg
    centres = np.stack(
        [radius * np.cos(phi), radius * np.sin(phi), np.zeros_like(phi)], axis=-1
    )
    dl = (2.0 * np.pi * radius / n_seg) * np.stack(
        [-np.sin(phi), np.cos(phi), np.zeros_like(phi)], axis=-1
    )
    shape = np.broadcast(np.asarray(r, float), np.asarray(z, float)).shape
    rs = np.asarray(r, dtype=float).ravel()
    zs = np.asarray(z, dtype=float).ravel()
    points = np.stack([rs, np.zeros_like(rs), zs], axis=-1)          # (M,3)
    delta = points[:, None, :] - centres[None, :, :]                 # (M,N,3)
    dist = np.linalg.norm(delta, axis=-1)
    cross = np.cross(dl[None, :, :], delta)                          # dl x delta
    b = (MU0 * current / (4.0 * np.pi)) * np.sum(
        cross / np.maximum(dist, 1e-12)[..., None] ** 3, axis=1
    )
    return b[:, 0].reshape(shape), b[:, 2].reshape(shape)
