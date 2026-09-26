"""Golden reproduction: the oracle must reproduce testdata/golden_*.json.

These values were produced by the (now deleted) Python reference implementation and
are the cross-language truth for both the Go engine and this oracle. Nothing here
touches testdata/ -- the files are read-only inputs.
"""

from __future__ import annotations

import json
import math

import numpy as np
import pytest

import oracle
from conftest import TESTDATA


# --------------------------------------------------------------------------- #
# spec
# --------------------------------------------------------------------------- #

def test_spec_is_read_from_the_golden_file_not_copied(spec):
    raw = json.loads((TESTDATA / "golden_spec.json").read_text())["spec"]
    assert spec.n_coils == raw["n_coils"] == 4
    assert spec.n_params == raw["n_params"] == 12
    assert spec.b_ref == raw["b_ref"]
    assert spec.self_field == raw["self_field_T"]
    assert spec.weights == dict(raw["weights"])
    # the derived self-field anchor matches mu0*j*t/2
    assert spec.self_field_anchor() == pytest.approx(spec.self_field, rel=1e-15)
    assert spec.lower().size == spec.upper().size == 12


# --------------------------------------------------------------------------- #
# baseline metrics / score
# --------------------------------------------------------------------------- #

def test_golden_baseline_metrics_reproduce(spec, grids, baseline, golden_baseline):
    got = oracle.evaluate(baseline["design"], spec, baseline["cost_proxy"], grids)
    for key in oracle.METRIC_KEYS:
        ref = golden_baseline["metrics"][key]
        mine = got["metrics"][key]
        if abs(ref) >= 1e-12:
            assert abs(mine - ref) / abs(ref) < 1e-6, (key, mine, ref)
        else:
            assert abs(mine - ref) < 1e-12, (key, mine, ref)


def test_golden_baseline_terms_weighted_penalties_reproduce(spec, grids, baseline, golden_baseline):
    got = oracle.evaluate(baseline["design"], spec, baseline["cost_proxy"], grids)
    for block in ("terms", "weighted", "penalties"):
        assert set(got[block]) == set(golden_baseline[block])
        for key, ref in golden_baseline[block].items():
            assert abs(got[block][key] - ref) <= max(1e-6 * abs(ref), 1e-12), (block, key)
    assert got["feasible"] is True
    assert golden_baseline["feasible"] is True


def test_golden_baseline_score_within_1e_6(spec, grids, baseline, golden_baseline):
    got = oracle.evaluate(baseline["design"], spec, baseline["cost_proxy"], grids)
    assert abs(got["score"] - golden_baseline["score"]) < 1e-6
    # and it is much tighter than the gate -- the gate is a floor, not the target
    assert abs(got["score"] - golden_baseline["score"]) < 1e-12


def test_baseline_cell_current_is_solved_not_guessed(spec, baseline, golden_baseline):
    """brentq must land on the same cell current as the golden file."""
    golden_cell = min(c["current_A"] for c in golden_baseline["coils"])
    assert baseline["cell_current_A"] == pytest.approx(golden_cell, rel=1e-12)
    assert baseline["throat_current_A"] == pytest.approx(3.5 * golden_cell, rel=1e-12)


def test_baseline_design_is_inside_the_search_box(spec, baseline):
    """Gate G6's premise, checked from the python side too: no clipping."""
    x = np.asarray(baseline["design"])
    lo, hi = spec.lower(), spec.upper()
    assert np.all(x >= lo) and np.all(x <= hi)
    assert oracle.vector_to_coils(x, spec) == oracle.vector_to_coils(x, spec)  # canonical


def test_baseline_cost_proxy_equals_sum_i2r(baseline, golden_baseline):
    expect = sum(c["current_A"] ** 2 * c["radius_m"] for c in golden_baseline["coils"])
    assert baseline["cost_proxy"] == pytest.approx(expect, rel=1e-15)
    assert baseline["cost_proxy"] == pytest.approx(golden_baseline["cost_proxy"], rel=1e-15)


# --------------------------------------------------------------------------- #
# field samples
# --------------------------------------------------------------------------- #

def test_golden_field_samples_within_1e_9(golden_samples):
    worst = 0.0
    for s in golden_samples:
        n = len(s["design"]) // 3
        coils = [(s["design"][i], s["design"][n + i], s["design"][2 * n + i]) for i in range(n)]
        r = np.asarray(s["points_r"], dtype=float)
        z = np.asarray(s["points_z"], dtype=float)
        br, bz = oracle.coilset_field(coils, r, z)
        mag = np.sqrt(br * br + bz * bz)
        for mine, key in ((br, "br"), (bz, "bz"), (mag, "b_mag")):
            rel, _, n_bad = oracle.compare_series(mine, s[key], 1e-9, 1e-12)
            worst = max(worst, rel)
            assert n_bad == 0, (s["design_name"], key, rel)
    assert worst < 1e-9


def test_axis_samples_have_zero_radial_field(golden_samples):
    s = golden_samples[0]
    axis = [i for i, r in enumerate(s["points_r"]) if r == 0.0]
    assert axis, "golden samples must include on-axis points"
    for i in axis:
        assert s["br"][i] == 0.0


# --------------------------------------------------------------------------- #
# the CLI gate itself
# --------------------------------------------------------------------------- #

def test_check_golden_cli_passes(capsys):
    code = oracle.main(["--check-golden", str(TESTDATA)])
    out = capsys.readouterr().out
    assert code == 0, out
    assert "G5 PASS" in out


def test_check_golden_cli_goes_red_on_a_tampered_golden(tmp_path, capsys):
    """The gate must be able to fail: a shifted score has to trip it."""
    for name in ("golden_spec.json", "golden_field_samples.json"):
        (tmp_path / name).write_text((TESTDATA / name).read_text())
    doc = json.loads((TESTDATA / "golden_baseline.json").read_text())
    doc["score"] = doc["score"] + 0.05
    (tmp_path / "golden_baseline.json").write_text(json.dumps(doc))
    assert oracle.main(["--check-golden", str(tmp_path)]) == 1
    capsys.readouterr()


def test_check_golden_cli_reports_missing_files(tmp_path, capsys):
    assert oracle.main(["--check-golden", str(tmp_path)]) == 2
    assert "missing" in capsys.readouterr().err


def test_compare_go_accepts_the_golden_sample_layout(tmp_path, capsys):
    """A Go export shaped like golden_field_samples.json must pass at 1e-9."""
    code = oracle.main(["--compare-go", str(TESTDATA / "golden_field_samples.json")])
    out = capsys.readouterr().out
    assert code == 0, out
    assert "PASS" in out


def test_compare_go_goes_red_on_a_perturbed_field(tmp_path, capsys):
    doc = json.loads((TESTDATA / "golden_field_samples.json").read_text())
    doc["samples"][0]["b_mag"][0] = doc["samples"][0]["b_mag"][0] * 1.0 + 1e-6
    p = tmp_path / "go_xcheck.json"
    p.write_text(json.dumps(doc))
    assert oracle.main(["--compare-go", str(p)]) == 1
    assert "FAIL" in capsys.readouterr().out


def test_emit_golden_refuses_to_overwrite_testdata(tmp_path, capsys):
    assert oracle.main(["--emit-golden", str(TESTDATA)]) == 2
    assert "refusing" in capsys.readouterr().err


def test_emit_golden_round_trips(tmp_path, capsys):
    assert oracle.main(["--emit-golden", str(tmp_path)]) == 0
    capsys.readouterr()
    for name in ("golden_spec.json", "golden_baseline.json", "golden_field_samples.json"):
        assert (tmp_path / name).is_file()
    emitted = json.loads((tmp_path / "golden_baseline.json").read_text())
    frozen = json.loads((TESTDATA / "golden_baseline.json").read_text())
    assert abs(emitted["score"] - frozen["score"]) < 1e-6
    # re-checking the emitted set must be green as well
    assert oracle.main(["--check-golden", str(tmp_path)]) == 0
    capsys.readouterr()


# --------------------------------------------------------------------------- #
# objective semantics
# --------------------------------------------------------------------------- #

def test_score_is_the_weighted_sum_and_penalties_subtract(spec, grids, baseline, golden_baseline):
    got = oracle.evaluate(baseline["design"], spec, baseline["cost_proxy"], grids)
    w = spec.weights
    manual = (w["field"] * got["terms"]["field"] + w["mirror"] * got["terms"]["mirror"]
              + w["volume"] * got["terms"]["volume"] - w["ripple"] * got["terms"]["ripple"]
              - w["cost"] * got["terms"]["cost"]
              - w["penalty"] * sum(got["penalties"].values()))
    assert got["score"] == pytest.approx(manual, rel=1e-15)


def test_ripple_is_zero_for_a_single_peaked_profile():
    b = [1.0, 1.1, 1.2, 1.3, 1.2, 1.1, 1.0]
    assert oracle.axis_ripple(b, 1.0, 0.05) == 0.0


def test_ripple_counts_non_monotonic_structure_above_prominence():
    # one interior (max, min) pair of depth 0.6 -> 0.6 / b_mid
    b = [0.0, 0.5, 1.0, 0.5, 0.4, 0.5]
    assert oracle.axis_ripple(b, 1.0, 0.05) == pytest.approx(0.6, rel=1e-12)
    # and the depth is measured peak-to-valley, not peak-to-last-sample
    b_deep = [0.0, 0.5, 1.0, 0.5, 0.2, 0.6]
    assert oracle.axis_ripple(b_deep, 1.0, 0.05) == pytest.approx(0.8, rel=1e-12)
    # two alternating structures sum: (max,min)=0.4 and (min,max)=0.2
    b2 = [0.0, 0.5, 1.0, 0.6, 0.7, 0.8, 0.7]
    assert oracle.axis_ripple(b2, 1.0, 0.05) == pytest.approx(0.6, rel=1e-12)
    # structures shallower than prominence * b_mid are filtered out
    b_shallow = [0.0, 0.5, 1.0, 0.97, 1.0, 0.5]      # two structures of depth 0.03
    assert oracle.axis_ripple(b_shallow, 1.0, 0.05) == 0.0
    assert oracle.axis_ripple(b_shallow, 1.0, 0.02) == pytest.approx(0.06, rel=1e-12)
    # a flat top has no strict interior extremum -> no structure reported
    b_flat = [0.0, 1.0, 1.0, 0.0]
    assert oracle.axis_ripple(b_flat, 1.0, 0.05) == 0.0


def test_metrics_are_finite_on_a_degenerate_design(spec, grids):
    """Coincident coils are singular physics; the oracle must still return numbers."""
    x = [0.5] * 4 + [0.0] * 4 + [1.0e5, 1.0e5, 1.0e5, 1.0e5]
    got = oracle.evaluate(x, spec, 1.0e12, grids)
    for key in oracle.METRIC_KEYS:
        assert math.isfinite(got["metrics"][key]), key
    assert got["metrics"]["coil_proximity_floor_hit"] is True
    assert got["metrics"]["min_coil_gap_m"] < 5e-3


def test_design_vector_is_clipped_and_canonicalised(spec):
    x = [0.0] * 4 + [0.0, 0.0, 0.0, 0.0] + [1.0e9] * 4       # all out of bounds
    coils = oracle.vector_to_coils(x, spec)
    for a, z, i in coils:
        assert spec.radius_bounds[0] <= a <= spec.radius_bounds[1]
        assert spec.z_bounds[0] <= z <= spec.z_bounds[1]
        assert spec.current_bounds[0] <= i <= spec.current_bounds[1]
    assert [c[1] for c in coils] == sorted(c[1] for c in coils)
    with pytest.raises(ValueError):
        oracle.vector_to_coils([0.1] * 5, spec)
