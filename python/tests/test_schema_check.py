"""Gate G9 tests: the schema checker must be green on good records and RED on bad ones."""

from __future__ import annotations

import json

import pytest

import schema_check
from conftest import good_record, write_jsonl


def test_valid_registry_passes(tmp_path, capsys):
    path = write_jsonl(tmp_path / "registry.jsonl",
                       [good_record(1), good_record(2, algorithm="random"),
                        good_record(3, algorithm="lhs")])
    code, errors, warnings, n = schema_check.check_registry(path, verbose=False)
    assert (code, errors, warnings, n) == (0, [], [], 3)
    assert schema_check.main([str(path), "--quiet"]) == 0


def test_missing_required_field_is_red(tmp_path):
    rec = good_record(1)
    del rec["terms"]
    path = write_jsonl(tmp_path / "registry.jsonl", [rec])
    code, errors, _, _ = schema_check.check_registry(path, verbose=False)
    assert code == 1
    assert any("missing required key 'terms'" in e for e in errors)


def test_jumped_experiment_id_is_red(tmp_path):
    path = write_jsonl(tmp_path / "registry.jsonl", [good_record(1), good_record(2), good_record(4)])
    code, errors, _, _ = schema_check.check_registry(path, verbose=False)
    assert code == 1
    assert any("experiment_id 4 is not contiguous" in e for e in errors)


def test_missing_metric_key_is_red(tmp_path):
    rec = good_record(1)
    del rec["metrics"]["ripple"]
    path = write_jsonl(tmp_path / "registry.jsonl", [rec])
    code, errors, _, _ = schema_check.check_registry(path, verbose=False)
    assert code == 1
    assert any("metrics missing key 'ripple'" in e for e in errors)


def test_non_numeric_metric_is_red(tmp_path):
    rec = good_record(1)
    rec["metrics"]["B_mid_T"] = "1.0"
    path = write_jsonl(tmp_path / "registry.jsonl", [rec])
    code, errors, _, _ = schema_check.check_registry(path, verbose=False)
    assert code == 1
    assert any("metrics.B_mid_T must be numeric" in e for e in errors)


def test_bad_design_id_is_red(tmp_path):
    path = write_jsonl(tmp_path / "registry.jsonl", [good_record(1, design_id="X0001")])
    code, errors, _, _ = schema_check.check_registry(path, verbose=False)
    assert code == 1
    assert any("does not match D%04d" in e for e in errors)


def test_design_id_not_matching_experiment_id_is_red(tmp_path):
    path = write_jsonl(tmp_path / "registry.jsonl", [good_record(1, design_id="D0009")])
    code, errors, _, _ = schema_check.check_registry(path, verbose=False)
    assert code == 1
    assert any("is not the id of experiment_id" in e for e in errors)


def test_unknown_key_is_red(tmp_path):
    path = write_jsonl(tmp_path / "registry.jsonl", [good_record(1, surprise=1)])
    code, errors, _, _ = schema_check.check_registry(path, verbose=False)
    assert code == 1
    assert any("unknown key" in e for e in errors)


def test_parameter_list_length_mismatch_is_red(tmp_path):
    rec = good_record(1)
    rec["params"]["z_m"] = [0.0, 1.0]
    path = write_jsonl(tmp_path / "registry.jsonl", [rec])
    code, errors, _, _ = schema_check.check_registry(path, verbose=False)
    assert code == 1
    assert any("different lengths" in e for e in errors)


def test_truncated_final_line_is_tolerated(tmp_path):
    path = write_jsonl(tmp_path / "registry.jsonl", [good_record(1), good_record(2)])
    with path.open("a") as fh:
        fh.write('{"experiment_id": 3, "design_id": "D00')
    code, errors, warnings, n = schema_check.check_registry(path, verbose=False)
    assert code == 0 and n == 2
    assert any("truncated final line" in w for w in warnings)


def test_broken_middle_line_is_red(tmp_path):
    path = write_jsonl(tmp_path / "registry.jsonl", [good_record(1)])
    with path.open("a") as fh:
        fh.write("{not json}\n")
        fh.write(json.dumps(good_record(2)) + "\n")
    code, errors, _, _ = schema_check.check_registry(path, verbose=False)
    assert code == 1
    assert any("not valid JSON" in e for e in errors)


def test_missing_file_is_a_usage_error(tmp_path, capsys):
    assert schema_check.main([str(tmp_path / "nope.jsonl")]) == 2
    assert "not found" in capsys.readouterr().err


def test_null_feasible_is_red(tmp_path):
    path = write_jsonl(tmp_path / "registry.jsonl", [good_record(1, feasible=None)])
    code, errors, _, _ = schema_check.check_registry(path, verbose=False)
    assert code == 1
    assert any("'feasible' must be a bool" in e for e in errors)
