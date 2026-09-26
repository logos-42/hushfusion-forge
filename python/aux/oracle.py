#!/usr/bin/env python3
"""Independent numerical oracle for the HUSHFUSION Forge design pipeline.

This module is a *re-implementation* of the Forge physics + objective
definitions in numpy/scipy. It imports nothing from ``internal/`` (Go) and never
shells out to a Go binary: the entire point of a cross-language oracle is that it
shares no code path with the thing it is checking.

Single source of truth for the device parameters is ``testdata/golden_spec.json``
(``config.Spec.AsMap()``). Nothing here carries a private copy of a default
constant -- every number comes from the spec file that is passed in, so the Go
and Python sides cannot drift apart silently.

Model (v0.1): exact vacuum magnetostatics of circular filament currents.
There is no plasma, no pressure, no finite-beta correction, no eddy current and
no conductor sharing. See CONTRACT.md §5 for the anchors this file must hit.

CLI
    python3 python/aux/oracle.py --check-golden testdata/     # acceptance gate G5
    python3 python/aux/oracle.py --compare-go <go_xcheck.json>
    python3 python/aux/oracle.py --emit-golden <dir>          # for human review
"""

from __future__ import annotations

import argparse
import json
import math
import sys
from pathlib import Path

import numpy as np
from scipy.optimize import brentq
from scipy.special import ellipe, ellipk

MU0 = 4.0e-7 * math.pi

# Frozen objective constants (internal/objective/api.go).
MIRROR_FLOOR = 0.2
MIRROR_MIN = 1.1
TERM_FIELD, TERM_MIRROR, TERM_VOLUME, TERM_RIPPLE, TERM_COST = (
    "field", "mirror", "volume", "ripple", "cost")
PEN_CONDUCTOR, PEN_SEPARATION, PEN_NOT_MIRROR = (
    "conductor_field", "coil_separation", "not_a_mirror")
TERM_KEYS = (TERM_FIELD, TERM_MIRROR, TERM_VOLUME, TERM_RIPPLE, TERM_COST)
PENALTY_KEYS = (PEN_CONDUCTOR, PEN_SEPARATION, PEN_NOT_MIRROR)

# Metrics keys that must match golden_baseline.json (physics.Metrics JSON tags).
METRIC_KEYS = ("B_mid_T", "B_throat_T", "z_throat_m", "mirror_ratio", "volume_good",
               "ripple", "B_coil_max_T", "min_coil_gap_m", "cost_proxy", "mu0")

# Ripple structure prominence is passed by the metrics layer (internal/physics/api.go).
RIPPLE_PROMINENCE = 0.05
# Singular closed form is floored at this centre separation (m) -- same rule as Go.
COIL_PROXIMITY_FLOOR = 5.0e-3

REPO_ROOT = Path(__file__).resolve().parents[2]


# --------------------------------------------------------------------------- #
# spec
# --------------------------------------------------------------------------- #

class Spec:
    """The device under design + evaluation window, read from AsMap-style JSON."""

    def __init__(self, raw: dict):
        s = dict(raw)
        b = s["bounds"]
        self.raw = s
        self.n_coils = int(s["n_coils"])
        self.radius_bounds = tuple(float(v) for v in b["radius"])
        self.z_bounds = tuple(float(v) for v in b["z"])
        self.current_bounds = tuple(float(v) for v in b["current"])
        self.b_ref = float(s["b_ref"])
        self.mirror_ref = float(s["mirror_ref"])
        self.coil_field_limit = float(s["coil_field_limit"])
        self.j_eng = float(s["j_eng"])
        self.t_pack = float(s["t_pack"])
        self.r_plasma = float(s["r_plasma"])
        self.z_mid = float(s["z_mid"])
        self.z_cell = float(s["z_cell"])
        self.z_axis_max = float(s["z_axis_max"])
        self.n_axis = int(s["n_axis"])
        self.n_vol_r = int(s["n_vol_r"])
        self.n_vol_z = int(s["n_vol_z"])
        self.confine_factor = float(s["confine_factor"])
        self.min_coil_sep = float(s["min_coil_sep"])
        self.weights = {k: float(v) for k, v in s["weights"].items()}
        self.self_field = float(s["self_field_T"]) if "self_field_T" in s else self.self_field_anchor()

    # -- constructors ------------------------------------------------------- #
    @classmethod
    def load(cls, path) -> "Spec":
        return cls(json.loads(Path(path).read_text())["spec"])

    @classmethod
    def from_map(cls, m: dict) -> "Spec":
        return cls(m)

    @property
    def n_params(self) -> int:
        return 3 * self.n_coils

    def self_field_anchor(self) -> float:
        """mu0 * j_eng * t_pack / 2 -- the winding-pack conductor-field anchor."""
        return MU0 * self.j_eng * self.t_pack / 2.0

    def lower(self):
        return np.array([self.radius_bounds[0]] * self.n_coils
                        + [self.z_bounds[0]] * self.n_coils
                        + [self.current_bounds[0]] * self.n_coils)

    def upper(self):
        return np.array([self.radius_bounds[1]] * self.n_coils
                        + [self.z_bounds[1]] * self.n_coils
                        + [self.current_bounds[1]] * self.n_coils)


# --------------------------------------------------------------------------- #
# field
# --------------------------------------------------------------------------- #

def elliptic_ke(m):
    """Complete elliptic integrals K(m), E(m) for parameter m = k^2.

    scipy.special.ellipk/ellipe take m (= k^2), matching the convention frozen in
    internal/physics/api.go. This is an *independent* implementation from the AGM
    scheme the Go side is told to use -- different algorithm, same function.
    """
    return ellipk(m), ellipe(m)


def loop_field(radius, current, r, z):
    """(Br, Bz) of one circular filament loop centred at z = 0, axis on z.

    Closed form (Simpson et al., NASA/TM-2001-211135) with
        s      = a^2 + r^2 + z^2
        alpha2 = s - 2 a r ,  beta2 = s + 2 a r ,  m = 1 - alpha2/beta2
        C      = mu0 I / pi

        Br = C z / (2 alpha2 beta r) [ s E(m) - alpha2 K(m) ]
        Bz = C   / (2 alpha2 beta)   [ (a^2 - r^2 - z^2) E(m) + alpha2 K(m) ]

    NOTE ON A DOC BUG: internal/physics/api.go prints the Br bracket *without* the
    1/r factor ("B_r = C*z/(2*alpha2*beta) * [...]"). That form disagrees with the
    golden field samples by a factor of exactly r (e.g. at the near-throat probe
    point of golden_field_samples.json it returns -1.2185 T where the golden file,
    and a direct Biot-Savart quadrature, both give -4.05 T). The golden values win:
    the physically correct 1/r is implemented here, and the omission is reported as
    a contract defect rather than reproduced.

    On the axis (r -> 0) the exact limit is used: Br = 0, Bz = mu0 I a^2 /
    (2 (a^2 + z^2)^1.5).
    """
    r = np.asarray(r, dtype=float)
    z = np.asarray(z, dtype=float)
    on_axis = r == 0.0
    rs = np.where(on_axis, 1.0, r)          # placeholder, never used on axis
    s = radius * radius + r * r + z * z
    alpha2 = s - 2.0 * radius * r
    beta2 = s + 2.0 * radius * r
    m = 1.0 - alpha2 / beta2
    with np.errstate(invalid="ignore", divide="ignore", over="ignore"):
        k, e = elliptic_ke(m)
        pref = MU0 * current / (math.pi * 2.0 * alpha2 * np.sqrt(beta2))
        br = pref * (z / rs) * (s * e - alpha2 * k)
        bz = pref * ((radius * radius - r * r - z * z) * e + alpha2 * k)
    bz_axis = MU0 * current * radius * radius / (2.0 * (radius * radius + z * z) ** 1.5)
    return np.where(on_axis, 0.0, br), np.where(on_axis, bz_axis, bz)


def loop_field_discrete(radius, current, r, z, n_seg=512):
    """Independent Biot-Savart sum over n_seg straight segments per loop.

    Midpoint rule on the exact Biot-Savart integral, evaluated in Cartesian
    coordinates -- no elliptic integrals anywhere, which is what makes it an
    independent check on loop_field.
    """
    phi = (np.arange(n_seg) + 0.5) * (2.0 * math.pi / n_seg)
    sx, sy = radius * np.cos(phi), radius * np.sin(phi)
    dlx, dly = -(2.0 * math.pi * radius / n_seg) * np.sin(phi), (2.0 * math.pi * radius / n_seg) * np.cos(phi)
    r = np.atleast_1d(np.asarray(r, dtype=float))
    z = np.atleast_1d(np.asarray(z, dtype=float))
    br = np.zeros_like(r)
    bz = np.zeros_like(z)
    for i in range(r.size):
        rx, ry, rz = r[i] - sx, -sy, z[i] - np.zeros_like(sx)
        rn = (rx * rx + ry * ry + rz * rz) ** 1.5
        cx, cy, cz = dly * rz, -dlx * rz, dlx * ry - dly * rx
        fac = MU0 * current / (4.0 * math.pi)
        br[i] = fac * np.sum(cx / rn)
        bz[i] = fac * np.sum(cz / rn)
    return br, bz


def coilset_field(coils, r, z):
    """Vector-summed (Br, Bz) of a coil set; coils are (radius, z, current)."""
    r = np.asarray(r, dtype=float)
    z = np.asarray(z, dtype=float)
    br = np.zeros_like(r)
    bz = np.zeros_like(r)
    for a, zc, cur in coils:
        b1, b2 = loop_field(a, cur, r, z - zc)
        br = br + b1
        bz = bz + b2
    return br, bz


def coilset_magnitude(coils, r, z):
    br, bz = coilset_field(coils, r, z)
    return np.sqrt(br * br + bz * bz)


def on_axis_field(coils, z):
    return coilset_magnitude(coils, np.zeros_like(np.asarray(z, dtype=float)), z)


# --------------------------------------------------------------------------- #
# design-vector codec
# --------------------------------------------------------------------------- #

def vector_to_coils(x, spec: Spec):
    """Decode a design vector: clip to the box, canonicalise by z (ascending)."""
    x = np.asarray(x, dtype=float)
    if x.size != spec.n_params:
        raise ValueError(f"design vector has {x.size} entries, spec wants {spec.n_params}")
    k = spec.n_coils
    clipped = np.clip(x, spec.lower(), spec.upper())
    coils = np.column_stack([clipped[:k], clipped[k:2 * k], clipped[2 * k:3 * k]])
    order = np.argsort(coils[:, 1], kind="stable")
    return [(float(a), float(zz), float(i)) for a, zz, i in coils[order]]


def coils_to_vector(coils):
    c = sorted(coils, key=lambda t: t[1])
    k = len(c)
    return [c[i][0] for i in range(k)] + [c[i][1] for i in range(k)] + [c[i][2] for i in range(k)]


# --------------------------------------------------------------------------- #
# grids + metrics
# --------------------------------------------------------------------------- #

class Grids:
    """Stacked sample points: [axis (r=0) | midplane volume | cell volume]."""

    def __init__(self, spec: Spec):
        self.axis_z = np.linspace(-spec.z_axis_max, spec.z_axis_max, spec.n_axis)
        self.axis_in_cell = np.abs(self.axis_z) <= spec.z_cell
        mr = np.linspace(0.0, spec.r_plasma, spec.n_vol_r)
        mz = np.linspace(-spec.z_mid, spec.z_mid, 5)
        cr = np.linspace(0.0, spec.r_plasma, spec.n_vol_r)
        cz = np.linspace(-spec.z_cell, spec.z_cell, spec.n_vol_z)
        MR, MZ = np.meshgrid(mr, mz, indexing="ij")
        CR, CZ = np.meshgrid(cr, cz, indexing="ij")
        self.mid_r, self.mid_z = MR.ravel(), MZ.ravel()
        self.cell_r, self.cell_z = CR.ravel(), CZ.ravel()
        self.n_axis = self.axis_z.size
        self.n_mid = self.mid_r.size
        self.stack_r = np.concatenate([np.zeros(self.n_axis), self.mid_r, self.cell_r])
        self.stack_z = np.concatenate([self.axis_z, self.mid_z, self.cell_z])

    def split_magnitude(self, mag):
        a = self.n_axis
        b = a + self.n_mid
        return mag[:a], mag[a:b], mag[b:]


def axis_ripple(b_axis_cell, b_mid, prominence=RIPPLE_PROMINENCE):
    """Normalised amplitude of NON-monotonic structure on the axis.

    Interior extrema are found by 3-point comparison; consecutive extrema that
    alternate (max, min) contribute |peak - valley| when the structure is deeper
    than prominence * b_mid. Divided by b_mid, so a monotonic or single-peaked
    profile returns exactly 0.
    """
    b = np.asarray(b_axis_cell, dtype=float)
    if b.size < 3 or not np.isfinite(b_mid) or b_mid <= 0.0:
        return 0.0
    ext = []
    for i in range(1, b.size - 1):
        if b[i - 1] < b[i] and b[i] > b[i + 1]:
            ext.append((i, +1))
        elif b[i - 1] > b[i] and b[i] < b[i + 1]:
            ext.append((i, -1))
    total = 0.0
    for (i0, s0), (i1, s1) in zip(ext, ext[1:]):
        if s0 == s1:
            continue
        depth = abs(b[i0] - b[i1])
        if depth > prominence * b_mid:
            total += depth
    return total / b_mid


def min_coil_gap(coils):
    if len(coils) < 2:
        return math.inf
    best = math.inf
    for i in range(len(coils)):
        for j in range(i + 1, len(coils)):
            best = min(best, math.hypot(coils[i][0] - coils[j][0], coils[i][1] - coils[j][1]))
    return best


def _displace_to_floor(coils, k, floor=COIL_PROXIMITY_FLOOR):
    """If coil k sits closer than `floor` to another centre, return a proxy point."""
    a, zc, _ = coils[k]
    for j, (aj, zj, _) in enumerate(coils):
        if j == k:
            continue
        d = math.hypot(a - aj, zc - zj)
        if d < floor:
            if d == 0.0:
                return a + floor, zc
            s = floor / d
            return a + (a - aj) * (s - 1.0), zc + (zc - zj) * (s - 1.0)
    return a, zc


def metrics_for(coils, spec: Spec, grids: Grids):
    """Every objective-relevant metric of one coil set (physics.Metrics parity)."""
    mag = coilset_magnitude(coils, grids.stack_r, grids.stack_z)
    axis, mid, cell = grids.split_magnitude(mag)
    b_mid = float(mid.mean())
    i_throat = int(np.argmax(axis))
    b_throat = float(axis[i_throat])
    volume_good = float(np.mean(cell <= spec.confine_factor * b_mid)) if cell.size else 0.0
    ripple = axis_ripple(axis[grids.axis_in_cell], b_mid, RIPPLE_PROMINENCE)
    self_field = spec.self_field

    proximity_floor_hit = False
    b_coil_max = 0.0
    for k in range(len(coils)):
        pt_r, pt_z = _displace_to_floor(coils, k)
        if (pt_r, pt_z) != (coils[k][0], coils[k][1]):
            proximity_floor_hit = True
        total = 0.0
        for j in range(len(coils)):
            if j == k:
                continue
            aj, zj, ij = coils[j]
            br, bz = loop_field(aj, ij, np.array([pt_r]), np.array([pt_z - zj]))
            total += float(math.hypot(br[0], bz[0]))   # sum of magnitudes, not |sum|
        b_coil_max = max(b_coil_max, total + self_field)

    cost = float(sum(i * i * a for a, _, i in coils))
    return {
        "B_mid_T": b_mid,
        "B_throat_T": b_throat,
        "z_throat_m": float(grids.axis_z[i_throat]) if axis.size else 0.0,
        "mirror_ratio": b_throat / b_mid if b_mid else 0.0,
        "volume_good": volume_good,
        "ripple": float(ripple),
        "B_coil_max_T": float(b_coil_max),
        "min_coil_gap_m": float(min_coil_gap(coils)),
        "cost_proxy": cost,
        "coil_proximity_floor_hit": bool(proximity_floor_hit),
        "n_coils": int(len(coils)),
        "mu0": MU0,
    }


# --------------------------------------------------------------------------- #
# objective
# --------------------------------------------------------------------------- #

def evaluate(x, spec: Spec, cost_ref: float, grids: Grids):
    """score / terms / weighted / penalties / feasible / metrics for one design."""
    coils = vector_to_coils(x, spec)
    m = metrics_for(coils, spec, grids)
    w = spec.weights
    terms = {
        TERM_FIELD: math.log10(max(m["B_mid_T"], 1e-9) / spec.b_ref),
        TERM_MIRROR: math.log10(max(m["mirror_ratio"], MIRROR_FLOOR) / spec.mirror_ref),
        TERM_VOLUME: m["volume_good"],
        TERM_RIPPLE: m["ripple"],
        TERM_COST: m["cost_proxy"] / cost_ref if cost_ref else 0.0,
    }
    weighted = {
        TERM_FIELD: w["field"] * terms[TERM_FIELD],
        TERM_MIRROR: w["mirror"] * terms[TERM_MIRROR],
        TERM_VOLUME: w["volume"] * terms[TERM_VOLUME],
        TERM_RIPPLE: -w["ripple"] * terms[TERM_RIPPLE],
        TERM_COST: -w["cost"] * terms[TERM_COST],
    }
    penalties = {
        PEN_CONDUCTOR: max(0.0, m["B_coil_max_T"] / spec.coil_field_limit - 1.0),
        PEN_SEPARATION: max(0.0, (spec.min_coil_sep - m["min_coil_gap_m"]) / spec.min_coil_sep),
        PEN_NOT_MIRROR: max(0.0, (MIRROR_MIN - m["mirror_ratio"]) / MIRROR_MIN),
    }
    score = (sum(weighted.values()) - w["penalty"] * sum(penalties.values()))
    return {
        "score": score,
        "terms": terms,
        "weighted": weighted,
        "penalties": penalties,
        "feasible": all(p <= 0.0 for p in penalties.values()),
        "metrics": m,
        "design": coils_to_vector(coils),
        "coils": coils,
    }


# --------------------------------------------------------------------------- #
# human baseline
# --------------------------------------------------------------------------- #

DEFAULT_GEOM = {"r_cell": 0.50, "half_gap_cell": 0.25, "r_throat": 0.30,
                "z_throat": 1.00, "throat_current_ratio": 3.5}


def textbook_mirror(spec: Spec, geom=None, grids=None):
    """Helmholtz-like central cell + two mirror throats, cell current SOLVED.

    The cell current is not guessed: brentq drives the midplane volume-averaged
    |B| to spec.b_ref exactly (bracket [current_min, current_max/ratio] keeps the
    throat current inside the search box, so the design is not clipped when it is
    re-encoded -- gate G6). The throat current is `ratio` x the cell current.
    """
    g = dict(DEFAULT_GEOM)
    if geom:
        g.update(geom)
    grids = grids or Grids(spec)
    a_cell, half_gap = g["r_cell"], g["half_gap_cell"]
    a_throat, z_throat, ratio = g["r_throat"], g["z_throat"], g["throat_current_ratio"]

    def b_mid_of(cell_current):
        coils = [(a_throat, -z_throat, ratio * cell_current),
                 (a_cell, -half_gap, cell_current),
                 (a_cell, half_gap, cell_current),
                 (a_throat, z_throat, ratio * cell_current)]
        return float(coilset_magnitude(coils, grids.mid_r, grids.mid_z).mean())

    lo = spec.current_bounds[0]
    hi = spec.current_bounds[1] / ratio
    f_lo, f_hi = b_mid_of(lo) - spec.b_ref, b_mid_of(hi) - spec.b_ref
    if f_lo * f_hi > 0:                       # pragma: no cover - config guard
        raise RuntimeError(f"cannot bracket the cell current: f({lo})={f_lo}, f({hi})={f_hi}")
    cell = brentq(lambda i: b_mid_of(i) - spec.b_ref, lo, hi,
                  xtol=1e-12, rtol=8.881784197001252e-16, maxiter=300)
    coils = [(a_throat, -z_throat, ratio * cell), (a_cell, -half_gap, cell),
             (a_cell, half_gap, cell), (a_throat, z_throat, ratio * cell)]
    coils = [(a, z, i) for a, z, i in sorted(coils, key=lambda t: t[1])]
    return {
        "name": "textbook_mirror",
        "coils": [{"radius_m": a, "z_m": z, "current_A": i} for a, z, i in coils],
        "design": coils_to_vector(coils),
        "cost_proxy": float(sum(i * i * a for a, _, i in coils)),
        "cell_current_A": cell,
        "throat_current_A": ratio * cell,
    }


# --------------------------------------------------------------------------- #
# comparison helpers
# --------------------------------------------------------------------------- #

def compare_series(mine, ref, rel_tol, abs_tol, rel_floor=1e-12):
    """Compare two equal-length series.

    Relative error is used where |ref| >= rel_floor; below that a relative error
    is meaningless (denormal / exact-zero references), so the absolute tolerance
    applies instead. Returns (max_rel, argmax_rel, n_bad, max_abs_over_floor).
    """
    mine = np.asarray(mine, dtype=float)
    ref = np.asarray(ref, dtype=float)
    if mine.shape != ref.shape:
        raise ValueError(f"shape mismatch: {mine.shape} vs {ref.shape}")
    big = np.abs(ref) >= rel_floor
    max_rel, i_rel = 0.0, -1
    n_bad = 0
    for i in range(ref.size):
        d = abs(float(mine[i]) - float(ref[i]))
        if big[i]:
            rel = d / abs(float(ref[i]))
            if rel > max_rel:
                max_rel, i_rel = rel, i
            if not rel <= rel_tol:
                n_bad += 1
        elif d > abs_tol:
            n_bad += 1
    return max_rel, i_rel, n_bad


def _cmp_line(label, mine, ref, rel_tol, abs_tol):
    diff = abs(float(mine) - float(ref))
    if abs(float(ref)) >= 1e-12:
        shown = f"rel={diff / abs(float(ref)):.3e}"
        ok = diff <= rel_tol * abs(float(ref))
    else:
        shown = f"abs={diff:.3e}"
        ok = diff <= abs_tol
    flag = "OK  " if ok else "FAIL"
    print(f"  [{flag}] {label:<24} reference={ref!r:<24} oracle={mine!r:<24} {shown}")
    return ok


# --------------------------------------------------------------------------- #
# gates
# --------------------------------------------------------------------------- #

def check_golden(data_dir, verbose=True):
    """Gate G5: recompute the golden numbers and compare. Returns exit code."""
    data_dir = Path(data_dir)
    spec_path = data_dir / "golden_spec.json"
    base_path = data_dir / "golden_baseline.json"
    samp_path = data_dir / "golden_field_samples.json"
    for p in (spec_path, base_path, samp_path):
        if not p.is_file():
            print(f"error: missing {p}", file=sys.stderr)
            return 2
    spec = Spec.load(spec_path)
    golden = json.loads(base_path.read_text())
    grids = Grids(spec)

    base = textbook_mirror(spec, grids=grids)
    got = evaluate(base["design"], spec, base["cost_proxy"], grids)
    ok = True

    if verbose:
        print(f"G5 cross-language oracle vs golden: spec={spec_path} "
              f"({spec.n_coils} coils, {spec.n_params} params)")
        print(f"  cell current solved by brentq: {base['cell_current_A']!r} "
              f"(golden {golden['coils'][1]['current_A']!r})")
    ok &= _cmp_line("cost_proxy", base["cost_proxy"], golden["cost_proxy"], 1e-6, 1e-12)
    for key in METRIC_KEYS:
        ok &= _cmp_line(key, got["metrics"][key], golden["metrics"][key], 1e-6, 1e-12)
    for key in TERM_KEYS:
        ok &= _cmp_line(f"terms.{key}", got["terms"][key], golden["terms"][key], 1e-6, 1e-12)
    for key in TERM_KEYS:
        ok &= _cmp_line(f"weighted.{key}", got["weighted"][key], golden["weighted"][key], 1e-6, 1e-12)
    for key in PENALTY_KEYS:
        ok &= _cmp_line(f"penalties.{key}", got["penalties"][key], golden["penalties"][key], 1e-6, 1e-12)
    ok &= _cmp_line("design(max|dI|)", base["design"][2 * spec.n_coils], golden["design"][2 * spec.n_coils], 1e-6, 1e-12)
    ok &= _cmp_line("score", got["score"], golden["score"], 1e-6, 1e-6)

    # ---- field samples: 3 designs x 32 points, relative < 1e-9 ------------- #
    samples = json.loads(samp_path.read_text())["samples"]
    if verbose:
        print("  field samples (analytic closed form vs golden):")
    worst = 0.0
    for s in samples:
        n = len(s["design"]) // 3
        coils = [(float(s["design"][i]), float(s["design"][n + i]), float(s["design"][2 * n + i]))
                 for i in range(n)]
        r = np.asarray(s["points_r"], dtype=float)
        z = np.asarray(s["points_z"], dtype=float)
        br, bz = coilset_field(coils, r, z)
        mag = np.sqrt(br * br + bz * bz)
        n_bad = 0
        rels = []
        for mine, key in ((br, "br"), (bz, "bz"), (mag, "b_mag")):
            rel, _, bad = compare_series(mine, s[key], 1e-9, 1e-12)
            rels.append(rel)
            n_bad += bad
        worst = max(worst, max(rels))
        ok &= n_bad == 0
        if verbose:
            print(f"    [{ 'OK  ' if n_bad == 0 else 'FAIL'}] {s['design_name']:<16} "
                  f"n={r.size} max_rel={max(rels):.3e} (br/bz/b_mag)")
    print(f"G5 {'PASS' if ok else 'FAIL'}  (worst sample relative error {worst:.3e})")
    return 0 if ok else 1


def _iter_go_cases(doc):
    """Tolerant reader for a Go field cross-check export.

    Accepts a top-level list, or a dict with one of the keys
    samples/points/records/cases/comparisons/designs holding the list. Each case
    needs a 12-entry `design` plus per-point r/z and the Go-side field values.
    r/z may be nested under `points`/`points_r`/`r` (aliases are accepted);
    the field may be `b_mag`/`magnitude`, `br`, `bz`.
    """
    if isinstance(doc, list):
        return doc
    if not isinstance(doc, dict):
        raise ValueError("compare-go input must be a JSON object or array")
    for key in ("samples", "points", "records", "cases", "comparisons", "designs", "results"):
        if isinstance(doc.get(key), list):
            return doc[key]
    raise ValueError(f"no case list found; top-level keys = {sorted(doc)}")


def _pick(d, *names, default=None):
    for n in names:
        if n in d:
            return d[n]
    return default


def compare_go(path, verbose=True):
    """Compare a Go-exported field table against the scipy oracle, point by point."""
    path = Path(path)
    if not path.is_file():
        print(f"error: missing {path}", file=sys.stderr)
        return 2
    doc = json.loads(path.read_text())
    cases = _iter_go_cases(doc)
    if not cases:                                          # pragma: no cover
        print("error: no cases in the cross-check file", file=sys.stderr)
        return 2
    spec_path = _pick(doc, "spec_path", default=None) if isinstance(doc, dict) else None
    spec = Spec.load(Path(spec_path)) if spec_path else None

    ok = True
    worst_rel = 0.0
    worst_abs = 0.0
    print(f"cross-language field check: {path} ({len(cases)} case(s))")
    for ci, case in enumerate(cases):
        design = _pick(case, "design", "x", "vector")
        if design is None:
            print(f"error: case {ci} has no design vector", file=sys.stderr)
            return 2
        n = len(design) // 3
        coils = [(float(design[i]), float(design[n + i]), float(design[2 * n + i])) for i in range(n)]
        pts = _pick(case, "points", default=None)
        r = _pick(case, "points_r", "r", "radii")
        z = _pick(case, "points_z", "z", "zs")
        if pts is not None and (r is None or z is None):
            r = [p["r"] for p in pts] if not isinstance(pts[0], dict) else [p.get("r", p.get("radius")) for p in pts]
            z = [p["z"] for p in pts] if not isinstance(pts[0], dict) else [p.get("z") for p in pts]
        if r is None or z is None:
            print(f"error: case {ci} has no sample points (r/z)", file=sys.stderr)
            return 2
        r = np.asarray(r, dtype=float)
        z = np.asarray(z, dtype=float)
        mine_br, mine_bz = coilset_field(coils, r, z)
        mine_mag = np.sqrt(mine_br ** 2 + mine_bz ** 2)
        name = _pick(case, "design_name", "name", default=f"case{ci}") if isinstance(case, dict) else f"case{ci}"
        checked = []
        for mine, keys in ((mine_br, ("br", "B_r")), (mine_bz, ("bz", "B_z")),
                           (mine_mag, ("b_mag", "magnitude", "|B|", "mag"))):
            ref = _pick(case, *keys)
            if ref is None:
                continue
            ref = np.asarray(ref, dtype=float)
            rel, i_rel, bad = compare_series(mine, ref, 1e-9, 1e-9)
            absmax = float(np.max(np.abs(mine - ref)))
            worst_rel = max(worst_rel, rel)
            worst_abs = max(worst_abs, absmax)
            ok &= bad == 0
            checked.append(f"{keys[0]}: rel={rel:.3e} abs={absmax:.3e}"
                           + ("" if bad == 0 else f" BAD={bad} at point {i_rel}"))
        if not checked:
            print(f"  error: case {ci} carries no Go field values (looked for br/bz/b_mag)", file=sys.stderr)
            return 2
        print(f"  [{'OK  ' if ok else 'FAIL'}] {name:<20} n={r.size} " + " | ".join(checked))
    print(f"cross-language field check {'PASS' if ok else 'FAIL'} "
          f"(max relative {worst_rel:.3e}, max absolute {worst_abs:.3e} T)")
    return 0 if ok else 1


def emit_golden(out_dir, force=False):
    """Re-emit the golden files from the oracle, for human review."""
    out_dir = Path(out_dir)
    if out_dir.resolve() == (REPO_ROOT / "testdata").resolve() and not force:
        print(f"refusing to overwrite frozen golden values in {out_dir}; pass --force to do it anyway",
              file=sys.stderr)
        return 2
    out_dir.mkdir(parents=True, exist_ok=True)
    spec_path = REPO_ROOT / "testdata" / "golden_spec.json"
    spec = Spec.load(spec_path)
    grids = Grids(spec)
    prov = {"generated_by": "python/aux/oracle.py --emit-golden",
            "spec_source": str(spec_path), "numpy": np.__version__,
            "python": sys.version.split()[0], "note": "independent scipy recomputation"}

    (out_dir / "golden_spec.json").write_text(json.dumps(
        {"provenance": prov, "spec": spec.raw}, indent=2, sort_keys=True) + "\n")

    base = textbook_mirror(spec, grids=grids)
    got = evaluate(base["design"], spec, base["cost_proxy"], grids)
    baseline = {
        "name": base["name"], "note": prov["note"] + " (helmoltz cell + mirror throats)",
        "coils": base["coils"], "design": base["design"],
        "cost_proxy": base["cost_proxy"], "cost_ref": base["cost_proxy"],
        "score": got["score"], "terms": got["terms"], "weighted": got["weighted"],
        "penalties": got["penalties"], "feasible": got["feasible"], "metrics": got["metrics"],
    }
    (out_dir / "golden_baseline.json").write_text(json.dumps(baseline, indent=2, sort_keys=True) + "\n")

    frozen = REPO_ROOT / "testdata" / "golden_field_samples.json"
    if not frozen.is_file():                               # pragma: no cover
        print(f"error: {frozen} needed for the sample layout", file=sys.stderr)
        return 2
    samples = []
    for s in json.loads(frozen.read_text())["samples"]:
        n = len(s["design"]) // 3
        coils = [(float(s["design"][i]), float(s["design"][n + i]), float(s["design"][2 * n + i]))
                 for i in range(n)]
        r = np.asarray(s["points_r"], dtype=float)
        z = np.asarray(s["points_z"], dtype=float)
        br, bz = coilset_field(coils, r, z)
        samples.append({"design_name": s["design_name"], "design": s["design"],
                        "solver": "oracle-scipy-closed-form",
                        "points_r": list(r), "points_z": list(z),
                        "br": list(br), "bz": list(bz),
                        "b_mag": list(np.sqrt(br * br + bz * bz))})
    (out_dir / "golden_field_samples.json").write_text(json.dumps(
        {"provenance": prov, "samples": samples}, indent=2) + "\n")
    print(f"emitted golden_spec.json / golden_baseline.json / golden_field_samples.json -> {out_dir}")
    return 0


# --------------------------------------------------------------------------- #
# cli
# --------------------------------------------------------------------------- #

def main(argv=None) -> int:
    ap = argparse.ArgumentParser(description="Forge independent numerical oracle (stage G)")
    g = ap.add_mutually_exclusive_group(required=True)
    g.add_argument("--check-golden", metavar="DIR", help="recompute and compare against testdata/golden_*.json")
    g.add_argument("--compare-go", metavar="FILE", help="compare a Go field export point by point")
    g.add_argument("--emit-golden", metavar="DIR", help="regenerate golden files into DIR")
    ap.add_argument("--force", action="store_true", help="allow --emit-golden into testdata/")
    args = ap.parse_args(argv)
    if args.check_golden:
        return check_golden(args.check_golden)
    if args.compare_go:
        return compare_go(args.compare_go)
    return emit_golden(args.emit_golden, force=args.force)


if __name__ == "__main__":
    sys.exit(main())
