"""规则挖掘的对齐核对：scipy.spearmanr 必须与 Go 上报的结果一致。

手算的锚点直接来自任务 / 契约：
    x=[1,2,3,4,5] vs y=[2,4,6,8,10]  ->  1.0
    x=[1,2,3,4,5] vs y=[5,3,4,1,2]   -> -0.8
    x=[1,1,2,2]   vs y=[1,2,3,4]     ->  0.8944271909999159
"""

from __future__ import annotations

import json

import pytest

import rules_check
from rules_check import spearman_rho


def test_spearman_hand_computed_anchors():
    assert spearman_rho([1, 2, 3, 4, 5], [2, 4, 6, 8, 10]) == pytest.approx(1.0)
    assert spearman_rho([1, 2, 3, 4, 5], [5, 3, 4, 1, 2]) == pytest.approx(-0.8)
    assert spearman_rho([1, 1, 2, 2], [1, 2, 3, 4]) == pytest.approx(0.8944271909999159)


def test_spearman_is_rank_based_and_tie_aware():
    # 单调缩放不可能改变秩相关
    assert spearman_rho([1, 2, 3], [10, 20, 30]) == pytest.approx(1.0)
    assert spearman_rho([1, 2, 3], [0.0, 0.5, 1e6]) == pytest.approx(1.0)
    # 全部并列的输入没有可上报的秩相关
    assert spearman_rho([1, 1, 1], [1, 2, 3]) == 0.0


def test_parameter_and_term_extraction():
    rec = {"params": {"radius_m": [0.3, 0.5, 0.5, 0.3], "z_m": [-1.0, -0.25, 0.25, 1.0],
                      "current_A": [1.6e6, 4.6e5, 4.6e5, 1.6e6]},
           "terms": {"field": 0.1, "mirror": 0.2, "volume": 0.3, "ripple": 0.4, "cost": 0.5}}
    assert rules_check.parameter_series(rec, "r_0") == 0.3
    assert rules_check.parameter_series(rec, "z_3") == 1.0
    assert rules_check.parameter_series(rec, "I_0") == 1.6e6
    assert rules_check.term_series(rec, "cost") == 0.5
    with pytest.raises(ValueError):
        rules_check.parameter_series(rec, "bogus_0")
    with pytest.raises(ValueError):
        rules_check.term_series(rec, "not_a_term")


def test_parameter_extraction_falls_back_to_the_design_vector():
    rec = {"design": [0.3, 0.5, 0.5, 0.3, -1.0, -0.25, 0.25, 1.0, 1.6e6, 4.6e5, 4.6e5, 1.6e6],
           "terms": {"volume": 0.1}}
    assert rules_check.parameter_series(rec, "r_2") == 0.5
    assert rules_check.parameter_series(rec, "z_1") == -0.25
    assert rules_check.parameter_series(rec, "I_3") == 1.6e6


def test_load_rules_reads_the_embedded_json_block(tmp_path):
    doc = {"rules": [{"rule_id": "R001", "parameter": "r_0", "term": "volume", "rho": 0.42}],
           "scope": "test"}
    md = ("# Design rules\n\ntext\n\n```json\n" + json.dumps(doc) + "\n```\n\ntrailing prose\n")
    p = tmp_path / "design_rules.md"
    p.write_text(md)
    rules = rules_check.load_rules(p)
    assert rules == doc["rules"]


def test_load_rules_rejects_a_file_without_a_json_block(tmp_path):
    p = tmp_path / "design_rules.md"
    p.write_text("# nothing machine readable here\n")
    with pytest.raises(ValueError):
        rules_check.load_rules(p)


def test_run_filters_drop_small_and_infeasible_runs():
    records = [{"algorithm": "a", "seed": 1, "feasible": True}] * 25
    records += [{"algorithm": "b", "seed": 1, "feasible": True}] * 5      # 太小
    records += [{"algorithm": "a", "seed": 2, "feasible": False}] * 30    # 不可行 (infeasible)
    runs = rules_check.feasible_runs(records, min_n=150)
    assert set(runs) == {("a", 1)}
    assert len(runs[("a", 1)]) == 25


def test_reconcile_passes_on_synthetic_data_and_goes_red_when_falsified(capsys):
    records = rules_check._synth_records()
    runs = rules_check.feasible_runs(records)
    rhos = [spearman_rho([rules_check.parameter_series(r, "r_0") for r in recs],
                         [rules_check.term_series(r, "volume") for r in recs])
            for recs in runs.values()]
    mean = sum(rhos) / len(rhos)
    assert mean == pytest.approx(1.0)                     # 按构造 volume = r_0
    good = [{"rule_id": "R001", "parameter": "r_0", "term": "volume", "rho": mean,
             "sign_agreement": 1.0, "n_designs": sum(len(v) for v in runs.values()),
             "n_runs": len(runs)}]
    code, rows = rules_check.reconcile(good, records, verbose=False)
    assert code == 0 and rows[0]["status"] == "OK"
    assert rows[0]["delta"] < 1e-12
    capsys.readouterr()

    bad = [dict(good[0], rho=-mean)]                      # 符号被翻转
    code, rows = rules_check.reconcile(bad, records, verbose=False)
    assert code == 1 and rows[0]["status"] == "FAIL"

    drifted = [dict(good[0], rho=mean - 0.05)]            # 超出 0.02 的容差
    code, _ = rules_check.reconcile(drifted, records, verbose=False)
    assert code == 1


def test_reconcile_reports_aggregation_ambiguity_honestly(capsys):
    """逐运行 rho 不同时，仍必须能与最接近的那种聚合方式对齐。"""
    import math

    records = rules_check._synth_records()
    for i, rec in enumerate(records):
        r0 = rec["params"]["radius_m"][0]
        # 一个秩相关很强但不完全的分数项，且逐运行不同
        rec["terms"]["cost"] = r0 + 0.03 * math.sin(i)
    runs = rules_check.feasible_runs(records)
    rhos = [spearman_rho([rules_check.parameter_series(r, "r_0") for r in recs],
                         [rules_check.term_series(r, "cost") for r in recs])
            for recs in runs.values()]
    assert len(rhos) >= 2 and all(v > 0.5 for v in rhos)
    assert len(set(rhos)) > 1                             # 各轮运行确实不同
    rule = [{"rule_id": "R001", "parameter": "r_0", "term": "cost",
             "rho": sum(rhos) / len(rhos),
             "sign_agreement": 1.0, "n_designs": 1, "n_runs": len(rhos)}]
    code, rows = rules_check.reconcile(rule, records, verbose=False)
    assert rows[0]["best_agg"] == "mean"
    assert rows[0]["delta"] < 1e-12                        # 上报的 rho 是均值
    assert code == 0
    capsys.readouterr()


def test_cli_selftest_is_green():
    assert rules_check.main(["--selftest"]) == 0


def test_cli_reports_missing_inputs(tmp_path, capsys):
    code = rules_check.main(["--rules", str(tmp_path / "nope.md"),
                             "--registry", str(tmp_path / "nope.jsonl")])
    assert code == 2
    assert "not found" in capsys.readouterr().err


def test_cli_end_to_end_on_a_written_corpus(tmp_path, capsys):
    records = rules_check._synth_records()
    reg = tmp_path / "registry.jsonl"
    reg.write_text("".join(json.dumps(r) + "\n" for r in records))
    runs = rules_check.feasible_runs(records)
    rhos = [spearman_rho([rules_check.parameter_series(r, "r_0") for r in recs],
                         [rules_check.term_series(r, "volume") for r in recs])
            for recs in runs.values()]
    rules = [{"rule_id": "R001", "parameter": "r_0", "term": "volume",
              "rho": sum(rhos) / len(rhos), "sign_agreement": 1.0,
              "n_designs": sum(len(v) for v in runs.values()), "n_runs": len(rhos),
              "statement": "s", "statement_en": "s", "scope": "s"}]
    md = tmp_path / "design_rules.md"
    md.write_text("# Rules\n\n```json\n" + json.dumps({"rules": rules}, indent=2) + "\n```\n")
    assert rules_check.main(["--rules", str(md), "--registry", str(reg)]) == 0
    capsys.readouterr()
    # 篡改已公布的系数：这道门必须变红
    rules[0]["rho"] = -1.0
    md.write_text("# Rules\n\n```json\n" + json.dumps({"rules": rules}) + "\n```\n")
    assert rules_check.main(["--rules", str(md), "--registry", str(reg)]) == 1
    capsys.readouterr()
