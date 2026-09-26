"""Analytic anchors of the oracle -- claims that hold without any golden file.

CONTRACT.md §5 lists them:
  * single loop on axis: B_z(0,z) = mu0 I a^2 / (2 (a^2+z^2)^1.5), B_r(0,z) = 0 (exact)
  * Helmholtz pair (radius a, spacing a): centre B = (4/5)^1.5 mu0 I / a,
    and non-uniformity < 1.2e-4 for |z| <= 0.1 a
  * closed form vs independent segment summation: < 1e-9 relative at nSeg = 512
  * vacuum identities: div B ~ 0 and curl B ~ 0
"""

from __future__ import annotations

import math

import numpy as np
import pytest

import oracle

MU0 = oracle.MU0


def test_on_axis_single_loop_matches_closed_form_exactly():
    a, i = 0.37, 123456.0
    zs = np.linspace(-2.0, 2.0, 41)
    br, bz = oracle.loop_field(a, i, np.zeros_like(zs), zs)
    expect = MU0 * i * a * a / (2.0 * (a * a + zs * zs) ** 1.5)
    assert np.all(br == 0.0)                                   # exactly zero, no 1e-18 dust
    assert np.allclose(bz, expect, rtol=0.0, atol=0.0)         # bit-identical expression
    assert np.max(np.abs(bz - expect)) == 0.0


def test_on_axis_is_the_r_to_zero_limit():
    """The axis branch must be the limit of the general closed form."""
    a, i = 0.5, 3.0e5
    br0, bz0 = oracle.loop_field(a, i, np.array([0.0]), np.array([0.4]))
    br1, bz1 = oracle.loop_field(a, i, np.array([1e-9]), np.array([0.4]))
    assert br1[0] < 1e-9
    assert abs(bz1[0] - bz0[0]) / bz0[0] < 1e-9


def test_helmholtz_centre_field_and_uniformity():
    a, i = 0.5, 4.63e5
    coils = [(a, -a / 2.0, i), (a, a / 2.0, i)]
    centre = oracle.on_axis_field(coils, np.array([0.0]))[0]
    expect = (4.0 / 5.0) ** 1.5 * MU0 * i / a
    assert centre == pytest.approx(expect, rel=1e-12)

    zs = np.linspace(-0.1 * a, 0.1 * a, 201)
    b = oracle.on_axis_field(coils, zs)
    non_uniformity = float(np.max(np.abs(b - centre)) / centre)
    assert non_uniformity < 1.2e-4                     # the contract's bound
    # and it is the expected magnitude, not an accident of a coarse grid: the
    # deviation grows as z^4 for a Helmholtz pair and hits ~1.14e-4 at z = 0.1a
    assert 1e-5 < non_uniformity < 1.2e-4
    assert np.argmax(np.abs(b - centre)) in (0, len(zs) - 1)
    # flatten the grid and the bound gets tighter, never looser
    zs_tight = np.linspace(-0.05 * a, 0.05 * a, 201)
    assert float(np.max(np.abs(oracle.on_axis_field(coils, zs_tight) - centre)) / centre) < non_uniformity


def test_closed_form_matches_direct_biot_savart_quadrature():
    """A third, integration-based path: the closed form is not self-referential."""
    from scipy.integrate import quad

    a, i = 0.3, 1.6213e6
    for r, z in [(0.2968860565795485, -0.0744371292260437), (0.1, 0.5), (0.62, 0.9)]:
        def components(phi, comp):
            sx, sy = a * math.cos(phi), a * math.sin(phi)
            dlx, dly = -a * math.sin(phi), a * math.cos(phi)
            rx, ry, rz = r - sx, -sy, z
            rn = (rx * rx + ry * ry + rz * rz) ** 1.5
            return {"r": dly * rz / rn, "z": (dlx * ry - dly * rx) / rn}[comp]

        fac = MU0 * i / (4.0 * math.pi)
        q_br = fac * quad(components, 0.0, 2.0 * math.pi, args=("r",), epsabs=1e-13, epsrel=1e-13)[0]
        q_bz = fac * quad(components, 0.0, 2.0 * math.pi, args=("z",), epsabs=1e-13, epsrel=1e-13)[0]
        br, bz = oracle.loop_field(a, i, np.array([r]), np.array([z]))
        scale = math.hypot(q_br, q_bz)
        assert abs(br[0] - q_br) / scale < 1e-9
        assert abs(bz[0] - q_bz) / scale < 1e-9


def test_closed_form_matches_discrete_biot_savart():
    a, i = 0.42, 8.0e5
    pts = [(0.0, 0.0), (0.0, 0.63), (0.21, 0.15), (0.55, -0.4), (0.9, 0.75)]
    for r, z in pts:
        br_e, bz_e = oracle.loop_field(a, i, np.array([r]), np.array([z]))
        br_d, bz_d = oracle.loop_field_discrete(a, i, [r], [z], n_seg=512)
        scale = math.hypot(br_e[0], bz_e[0])
        assert abs(br_d[0] - br_e[0]) / scale < 1e-9
        assert abs(bz_d[0] - bz_e[0]) / scale < 1e-9


def test_superposition_of_coincident_loops_scales_linearly():
    a, i = 0.35, 3.0e5
    one = oracle.coilset_magnitude([(a, 0.0, i)], np.array([0.2]), np.array([0.1]))[0]
    four = oracle.coilset_magnitude([(a, 0.0, i)] * 4, np.array([0.2]), np.array([0.1]))[0]
    assert four == pytest.approx(4.0 * one, rel=1e-12)


def test_vacuum_identities_div_and_curl_free():
    """div B = 0 and curl B = 0 in the source-free region (central differences)."""
    coils = [(0.3, -1.0, 1.62e6), (0.5, -0.25, 4.63e5), (0.5, 0.25, 4.63e5), (0.3, 1.0, 1.62e6)]
    h = 1e-5

    def b(r, z):
        br, bz = oracle.coilset_field(coils, np.array([r]), np.array([z]))
        return float(br[0]), float(bz[0])

    for r, z in [(0.05, 0.02), (0.22, 0.3), (0.6, -0.5)]:
        br_p, bz_p = b(r + h, z)
        br_m, bz_m = b(r - h, z)
        br_u, _ = b(r, z + h)
        br_d, _ = b(r, z - h)
        _, bz_u = b(r, z + h)
        _, bz_d = b(r, z - h)
        # div B = (1/r) d(r Br)/dr + dBz/dz  = 0
        div = ((r + h) * br_p - (r - h) * br_m) / (2.0 * h * r) + (bz_u - bz_d) / (2.0 * h)
        # (curl B)_phi = dBz/dr - dBr/dz = 0
        curl = (bz_p - bz_m) / (2.0 * h) - (br_u - br_d) / (2.0 * h)
        scale = max(abs(br_p), abs(bz_p), 1e-12)
        assert abs(div) / scale < 1e-5
        assert abs(curl) / scale < 1e-5


def test_discrete_solver_is_a_genuinely_separate_path():
    """The two implementations must not be bit-identical, and must converge."""
    a, i = 0.42, 8.0e5
    r, z = 0.21, 0.15
    br_e, bz_e = oracle.loop_field(a, i, np.array([r]), np.array([z]))

    def err(n_seg):
        br, bz = oracle.loop_field_discrete(a, i, [r], [z], n_seg=n_seg)
        return math.hypot(br[0] - br_e[0], bz[0] - bz_e[0]) / math.hypot(br_e[0], bz_e[0])

    assert err(16) != 0.0                                  # not the same code path
    assert err(16) > err(512)                              # converges with n_seg
    assert err(512) < 1e-9                                 # the contract's threshold
