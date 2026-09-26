"""golden 复现：oracle 必须能让 testdata/golden_*.json 重现。

这些数值由（现已删除的）Python 参考实现产出，对 Go 引擎和本 oracle 而言
都是跨语言的真相。这里不修改 testdata/ —— 那些文件是只读输入。
"""

from __future__ import annotations

import json
import math

import numpy as np
import pytest

import oracle
from conftest import TESTDATA


# --------------------------------------------------------------------------- #
# spec（设备规格）
# --------------------------------------------------------------------------- #

def test_spec_is_read_from_the_golden_file_not_copied(spec):
    raw = json.loads((TESTDATA / "golden_spec.json").read_text())["spec"]
    assert spec.n_coils == raw["n_coils"] == 4
    assert spec.n_params == raw["n_params"] == 12
    assert spec.b_ref == raw["b_ref"]
    assert spec.self_field == raw["self_field_T"]
    assert spec.weights == dict(raw["weights"])
    # 推导出的自场锚点与 mu0*j*t/2 相符
    assert spec.self_field_anchor() == pytest.approx(spec.self_field, rel=1e-15)
    assert spec.lower().size == spec.upper().size == 12


# --------------------------------------------------------------------------- #
# 基线指标 / 分数
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
    # 而且它比门限紧得多 —— 门限是下限，不是目标
    assert abs(got["score"] - golden_baseline["score"]) < 1e-12


def test_baseline_cell_current_is_solved_not_guessed(spec, baseline, golden_baseline):
    """brentq 必须落在与 golden 文件相同的单元电流上。"""
    golden_cell = min(c["current_A"] for c in golden_baseline["coils"])
    assert baseline["cell_current_A"] == pytest.approx(golden_cell, rel=1e-12)
    assert baseline["throat_current_A"] == pytest.approx(3.5 * golden_cell, rel=1e-12)


def test_baseline_design_is_inside_the_search_box(spec, baseline):
    """门 G6 的前提，也从 python 侧检查一遍：没有夹断。"""
    x = np.asarray(baseline["design"])
    lo, hi = spec.lower(), spec.upper()
    assert np.all(x >= lo) and np.all(x <= hi)
    assert oracle.vector_to_coils(x, spec) == oracle.vector_to_coils(x, spec)  # 规范形式


def test_baseline_cost_proxy_equals_sum_i2r(baseline, golden_baseline):
    expect = sum(c["current_A"] ** 2 * c["radius_m"] for c in golden_baseline["coils"])
    assert baseline["cost_proxy"] == pytest.approx(expect, rel=1e-15)
    assert baseline["cost_proxy"] == pytest.approx(golden_baseline["cost_proxy"], rel=1e-15)


# --------------------------------------------------------------------------- #
# 场样本
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
# CLI 门本身
# --------------------------------------------------------------------------- #

def test_check_golden_cli_passes(capsys):
    code = oracle.main(["--check-golden", str(TESTDATA)])
    out = capsys.readouterr().out
    assert code == 0, out
    assert "G5 PASS" in out


def test_check_golden_cli_goes_red_on_a_tampered_golden(tmp_path, capsys):
    """这道门必须能失败：一个被挪动的分数必须触发它。"""
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
    """形状与 golden_field_samples.json 相同的 Go 导出必须能在 1e-9 上通过。"""
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


def test_nearest_wire_distance_is_measured_to_the_wire():
    coils = [(0.3, -1.0, 1.0)]
    d = oracle.nearest_wire_distance(coils, np.array([0.3, 0.0, 0.5]), np.array([-0.996, 0.0, -1.0]))
    assert d[0] == pytest.approx(4e-3, rel=1e-9)          # 在环平面下方 4 mm
    # 在轴上，最近导线点的距离是 sqrt(a^2 + dz^2)，而不是
    assert d[1] == pytest.approx(math.sqrt(0.3 ** 2 + 1.0 ** 2), rel=1e-12)
    assert d[2] == pytest.approx(0.2, rel=1e-9)           # 在环平面内，向外 0.2 m


def test_compare_go_near_wire_floor_flag(tmp_path, capsys):
    """同口径模式的接线检查（这里模拟 Go 侧）。"""
    spec = oracle.Spec.load(TESTDATA / "golden_spec.json")
    design = oracle.textbook_mirror(spec)["design"]
    n = spec.n_coils
    coils = [(design[i], design[n + i], design[2 * n + i]) for i in range(n)]
    r = np.array([0.3, 0.05, 0.6])
    z = np.array([-0.996, 0.0, 0.4])                      # 第一个点距离导线 4 mm
    br, bz = oracle.coilset_field(coils, r, z, proximity_floor=5e-3)
    case = {"design": design, "design_name": "near_wire_probe",
            "points_r": list(r), "points_z": list(z),
            "br": list(br), "bz": list(bz), "b_mag": list(np.sqrt(br * br + bz * bz))}
    p = tmp_path / "near_wire.json"
    p.write_text(json.dumps({"samples": [case]}))

    assert oracle.main(["--compare-go", str(p)]) == 1     # 精确形式 vs 被夹取的形式
    out = capsys.readouterr().out
    assert "closer than 0.005 m to a wire" in out and "NOT clamped" in out

    assert oracle.main(["--compare-go", str(p), "--proximity-floor", "0.005"]) == 0
    out = capsys.readouterr().out
    assert "PASS" in out and "clamped to the floor" in out


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
    # 重新校验生成的集合也必须是绿的
    assert oracle.main(["--check-golden", str(tmp_path)]) == 0
    capsys.readouterr()


# --------------------------------------------------------------------------- #
# 目标函数语义
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
    # 一对内部 (max, min)，深度 0.6 -> 0.6 / b_mid
    b = [0.0, 0.5, 1.0, 0.5, 0.4, 0.5]
    assert oracle.axis_ripple(b, 1.0, 0.05) == pytest.approx(0.6, rel=1e-12)
    # 而且深度是量到峰到谷，而不是峰到最后一个样本
    b_deep = [0.0, 0.5, 1.0, 0.5, 0.2, 0.6]
    assert oracle.axis_ripple(b_deep, 1.0, 0.05) == pytest.approx(0.8, rel=1e-12)
    # 两个交替结构相加：(max,min)=0.4 和 (min,max)=0.2
    b2 = [0.0, 0.5, 1.0, 0.6, 0.7, 0.8, 0.7]
    assert oracle.axis_ripple(b2, 1.0, 0.05) == pytest.approx(0.6, rel=1e-12)
    # 比 prominence * b_mid 更浅的结构会被过滤掉
    b_shallow = [0.0, 0.5, 1.0, 0.97, 1.0, 0.5]      # 两个深度为 0.03 的结构
    assert oracle.axis_ripple(b_shallow, 1.0, 0.05) == 0.0
    assert oracle.axis_ripple(b_shallow, 1.0, 0.02) == pytest.approx(0.06, rel=1e-12)
    # 平顶没有严格的内部极值 -> 不上报任何结构
    b_flat = [0.0, 1.0, 1.0, 0.0]
    assert oracle.axis_ripple(b_flat, 1.0, 0.05) == 0.0


def test_metrics_are_finite_on_a_degenerate_design(spec, grids):
    """重合的线圈是奇异物理；oracle 仍然必须返回数字。"""
    x = [0.5] * 4 + [0.0] * 4 + [1.0e5, 1.0e5, 1.0e5, 1.0e5]
    got = oracle.evaluate(x, spec, 1.0e12, grids)
    for key in oracle.METRIC_KEYS:
        assert math.isfinite(got["metrics"][key]), key
    assert got["metrics"]["coil_proximity_floor_hit"] is True
    assert got["metrics"]["min_coil_gap_m"] < 5e-3


def test_design_vector_is_clipped_and_canonicalised(spec):
    x = [0.0] * 4 + [0.0, 0.0, 0.0, 0.0] + [1.0e9] * 4       # 全部越界
    coils = oracle.vector_to_coils(x, spec)
    for a, z, i in coils:
        assert spec.radius_bounds[0] <= a <= spec.radius_bounds[1]
        assert spec.z_bounds[0] <= z <= spec.z_bounds[1]
        assert spec.current_bounds[0] <= i <= spec.current_bounds[1]
    assert [c[1] for c in coils] == sorted(c[1] for c in coils)
    with pytest.raises(ValueError):
        oracle.vector_to_coils([0.1] * 5, spec)
