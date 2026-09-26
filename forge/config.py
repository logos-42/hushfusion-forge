"""Single source of truth for every physical constant, bound and weight.

Rule of the house: no number used by the objective may be hard-coded anywhere
else in the package. If a number matters to a score, it lives here and it is
documented with its physical meaning and its provenance.

Fidelity statement (v0.1)
--------------------------
The field model is an exact magnetostatic solution for circular filament
currents in vacuum (Superposition of Biot-Savart for axisymmetric loops).
It contains NO plasma: no pressure, no diamagnetic response, no MHD
equilibrium, no finite-beta correction, no eddy currents, no conductor
current sharing. Those are Phase-2 items and are listed in PLAN.md; every
result produced by v0.1 is a *vacuum-field design* result and is reported
as such.
"""

from __future__ import annotations

from dataclasses import asdict, dataclass, field

import numpy as np

MU0 = 4.0e-7 * np.pi  # vacuum permeability [H/m]


@dataclass(frozen=True)
class ParamBounds:
    """Search-space box for a single coil (SI units).

    The current ceiling is set so that the hand-designed reference
    (``forge.optimization.baselines.textbook_mirror``, whose throat current is
    ~1.62 MA) is strictly *inside* the box. That is a hard requirement, not a
    convenience: a baseline that gets clipped when re-encoded as a design vector
    would be scored as a different machine, and the "machine beats human" claim
    would be measured against a design nobody ever proposed. The invariant is
    enforced by a test (``TestBaselineInsideSearchBox``).
    """

    radius: tuple[float, float] = (0.10, 1.00)     # [m]  loop radius
    z: tuple[float, float] = (-1.20, 1.20)         # [m]  axial position
    current: tuple[float, float] = (1.0e4, 2.5e6)  # [A] ampere-turns


@dataclass(frozen=True)
class Weights:
    """Objective weights — an *engineering operating judgement*, not physics.

    The raw (unweighted) terms are always stored alongside the score, so any
    weighting can be re-derived after the fact without re-running a search.
    """

    field: float = 1.0     # log10(B_mid / B_ref)
    mirror: float = 0.5    # log10(R / R_ref)
    volume: float = 0.75   # good-field volume fraction of the cell
    ripple: float = 0.5    # non-monotonic field structure inside the cell
    cost: float = 1.0      # resistive/coil cost proxy, normalised by baseline
    penalty: float = 10.0  # multiplier on normalised constraint violation


@dataclass(frozen=True)
class Spec:
    """Device under design + the evaluation window + engineering limits."""

    # --- device -------------------------------------------------------
    n_coils: int = 4
    bounds: ParamBounds = field(default_factory=ParamBounds)

    # --- objective references ----------------------------------------
    b_ref: float = 1.0          # [T]   field term reference
    mirror_ref: float = 2.0     # [-]   mirror-ratio reference

    # --- engineering limits ------------------------------------------
    coil_field_limit: float = 12.0   # [T] peak field on the conductor (HTS @20 K, conservative)
    j_eng: float = 1.0e8             # [A/m^2] winding-pack current density (100 A/mm^2, HTS @20 K)
    t_pack: float = 0.05             # [m] winding-pack thickness (radial)

    # --- evaluation window -------------------------------------------
    r_plasma: float = 0.15      # [m] plasma radius (central cell)
    z_mid: float = 0.15         # [m] half-height of the "midplane" sampling volume
    z_cell: float = 0.80        # [m] half-length of the central cell that must hold plasma
    z_axis_max: float = 1.40    # [m] axial extent sampled for the mirror throat
    n_axis: int = 161           # axial sample count
    n_vol_r: int = 13           # radial sample count over the cell volume
    n_vol_z: int = 33           # axial sample count over the cell volume
    confine_factor: float = 1.25  # threshold for "good field": |B| <= 1.25 * B_mid

    # --- geometry constraints ----------------------------------------
    min_coil_sep: float = 0.05  # [m] minimum distance between coil centres

    weights: Weights = field(default_factory=Weights)

    # ------------------------------------------------------------------
    @property
    def n_params(self) -> int:
        """Length of the design vector: [r_0..r_K, z_0..z_K, I_0..I_K]."""
        return 3 * self.n_coils

    def self_field(self) -> float:
        """Field on the conductor from the coil's own winding pack [T].

        Model: a solenoid-like pack of thickness ``t_pack`` carrying uniform
        current density ``j_eng`` has B ~ mu0 * j * t / 2 at the winding face.
        Exact for an infinite slab; used here as the documented anchor value
        that says "a pack at the current-density limit is already sitting in
        this much field". Replaced by a real winding-pack/FEM model in Phase 2.
        """
        return MU0 * self.j_eng * self.t_pack / 2.0

    def as_dict(self) -> dict:
        d = asdict(self)
        d["self_field_T"] = self.self_field()
        d["n_params"] = self.n_params
        return d


def lower_upper(spec: Spec) -> tuple[np.ndarray, np.ndarray]:
    """Design-vector bounds, shape (n_params,)."""
    b = spec.bounds
    lo = np.array([b.radius[0]] * spec.n_coils + [b.z[0]] * spec.n_coils + [b.current[0]] * spec.n_coils)
    hi = np.array([b.radius[1]] * spec.n_coils + [b.z[1]] * spec.n_coils + [b.current[1]] * spec.n_coils)
    return lo, hi
