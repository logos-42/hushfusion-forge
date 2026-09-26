"""analyze.py：从报告生成图与表，外加 aggregate 交叉校验。"""

from __future__ import annotations

import json
import math

import numpy as np
import pytest

import analyze
import oracle
from conftest import TESTDATA


def synth_runs(budget=40):
    runs = []
    for algo, base in (("random", -0.55), ("evolution", -0.35), ("evolution_warm", -0.30)):
        for seed in (1, 2):
            hist = [base + 0.01 * i for i in range(budget)]
            hist[-1] = base + 0.5
            runs.append({
                "algorithm": algo, "seed": seed, "budget": budget, "n_evals": budget,
                "best_score": hist[-1], "best_design": [0.3, 0.5, 0.5, 0.3, -1.0, -0.25, 0.25, 1.0,
                                                        1.6e6, 4.6e5, 4.6e5, 1.6e6],
                "best_terms": {}, "best_metrics": {}, "best_design_id": "D0002",
                "best_feasible": True, "evals_to_beat": 12 * (seed + 1), "history": hist,
            })
    return runs


def make_report(spec, baseline, budget=40, tamper_aggregate=False, tamper_best=False):
    spec_map = json.loads((TESTDATA / "golden_spec.json").read_text())["spec"]
    grids = oracle.Grids(spec)
    cost_ref = baseline["cost_proxy"]
    base_eval = oracle.evaluate(baseline["design"], spec, cost_ref, grids)
    # 一次真实 stage-E/F benchmark 运行 (runs/scratch/bench) 的获胜设计，抄成
    # 纯数字，这样这个 fixture 不依赖任何文件 —— 它是能赢过基线的那个
    best_design = [0.34074440599674666, 1.0, 0.1, 0.3157946290596812,
                   -0.20688985165708562, 0.012635023002828594,
                   0.04049787940226826, 0.07602128113826669,
                   624565.672280665, 10000.0, 2163774.4578687255, 529264.0963625956]
    best_eval = oracle.evaluate(best_design, spec, cost_ref, grids)
    report = {
        "meta": {"tag": "demo", "timestamp": "1970-01-01T00:00:00Z", "budget": budget,
                 "seeds": [1, 2], "methods": ["random", "evolution", "evolution_warm"],
                 "git_commit": "deadbeef", "platform": "test", "go_version": "go1.22",
                 "workers": 1},
        "spec": spec_map,
        "solver": "analytic-vacuum-loops",
        "cost_ref": cost_ref,
        "baseline": {"name": "textbook_mirror", "note": "", "score": base_eval["score"],
                     "feasible": base_eval["feasible"], "terms": base_eval["terms"],
                     "weighted": base_eval["weighted"], "penalties": base_eval["penalties"],
                     "metrics": base_eval["metrics"],
                     "design": baseline["design"], "cost_proxy": baseline["cost_proxy"],
                     "design_id": "D0001"},
        "runs": synth_runs(budget),
        "history": {},
        "aggregate": {},
        "robustness": {"variants": [], "baseline_score_per_variant": {}, "per_design": {},
                       "summary": {}},
        "best": {"design_id": "D0007", "algorithm": "evolution_warm", "seed": 2,
                 "score": best_eval["score"] + (0.05 if tamper_best else 0.0),
                 "terms": best_eval["terms"], "metrics": best_eval["metrics"],
                 "design": best_design, "feasible": best_eval["feasible"]},
        "registry_summary": {"n_records": 240, "n_feasible": 240, "per_algorithm": {},
                             "best_score": best_eval["score"], "best_design_id": "D0007",
                             "best_algorithm": "evolution_warm"},
    }
    mined = analyze.recompute_aggregate(report)
    for key, val in mined.items():
        val["evals_to_beat_median"] = val["evals_to_beat_mean"]
    report["aggregate"] = mined
    if tamper_aggregate:
        first = sorted(mined)[0]
        report["aggregate"][first] = dict(mined[first])
        report["aggregate"][first]["best_mean"] += 0.05
    return report


def write_run_dir(tmp_path, report, records=()):
    run_dir = tmp_path / "runs" / "demo"
    run_dir.mkdir(parents=True)
    (run_dir / "results.json").write_text(json.dumps(report))
    if records:
        (run_dir / "registry.jsonl").write_text("".join(json.dumps(r) + "\n" for r in records))
    return run_dir


def test_analyze_writes_the_table_and_figures(tmp_path, spec, baseline, capsys):
    report = make_report(spec, baseline)
    run_dir = write_run_dir(tmp_path, report)
    code = analyze.analyze(run_dir)
    out = capsys.readouterr().out
    assert code == 0
    assert (run_dir / "figures" / "benchmark_table.md").is_file()
    table = (run_dir / "figures" / "benchmark_table.md").read_text()
    for algo in ("random", "evolution", "evolution_warm"):
        assert algo in table
    assert "human baseline" in table
    assert f"{report['baseline']['score']:.6f}" in table
    assert "aggregate block re-derived" in out
    if analyze.HAVE_MPL:
        assert (run_dir / "figures" / "best_so_far.png").is_file()
        assert (run_dir / "figures" / "axis_profile.png").is_file()
        assert (run_dir / "figures" / "best_so_far.png").stat().st_size > 1000
    else:                                                  # pragma: no cover
        assert "matplotlib unavailable" in out


def test_analyze_goes_red_when_the_aggregate_disagrees(tmp_path, spec, baseline, capsys):
    report = make_report(spec, baseline, tamper_aggregate=True)
    run_dir = write_run_dir(tmp_path, report)
    assert analyze.analyze(run_dir) == 1
    out = capsys.readouterr().out
    assert "[FAIL] aggregate[" in out


def test_rescore_agrees_with_the_report(spec, baseline, capsys):
    report = make_report(spec, baseline)
    assert analyze.rescore_report(report) is True
    out = capsys.readouterr().out
    assert "oracle re-score of the baseline" in out and "oracle re-score of the best" in out
    assert "[OK  ]" in out
    # 重新打分后的最优确实就是机器的获胜设计
    assert report["best"]["score"] > report["baseline"]["score"]


def test_rescore_goes_red_on_a_falsified_best_score(tmp_path, spec, baseline, capsys):
    report = make_report(spec, baseline, tamper_best=True)
    assert analyze.rescore_report(report, verbose=False) is False
    run_dir = write_run_dir(tmp_path, report)
    assert analyze.analyze(run_dir) == 1
    assert "[FAIL] oracle re-score of the best" in capsys.readouterr().out


def test_rescore_skips_a_report_without_a_spec(capsys):
    assert analyze.rescore_report({"baseline": {"score": 0.0}}) is True
    assert "skipped" in capsys.readouterr().out


def test_best_so_far_curves_have_a_band_per_method(spec, baseline):
    report = make_report(spec, baseline)
    curves = analyze.best_so_far_curves(report)
    assert set(curves) >= {"random", "evolution", "evolution_warm"}
    for algo in ("random", "evolution", "evolution_warm"):
        c = curves[algo]
        assert c["n_seeds"] == 2
        assert len(c["x"]) == len(c["mean"]) == len(c["lo"]) == len(c["hi"]) == 40
        assert all(lo <= m <= hi for lo, m, hi in zip(c["lo"], c["mean"], c["hi"]))


def test_history_is_rebuilt_from_the_registry_when_missing(spec, baseline):
    report = make_report(spec, baseline)
    for run in report["runs"]:
        run["history"] = []
    records = []
    for i, run in enumerate(report["runs"], start=1):
        for k in range(30):
            records.append({"experiment_id": i * 100 + k, "design_id": f"D{i * 100 + k:04d}",
                            "algorithm": run["algorithm"], "seed": run["seed"], "eval_index": k,
                            "score": -1.0 + 0.01 * k, "feasible": True})
    curves = analyze.best_so_far_curves(report, records)
    assert curves.get("_missing") is None
    assert curves.get("_notes")
    assert len(curves["random"]["mean"]) == 30


def test_axis_profile_is_the_oracle_on_axis_field(spec, baseline):
    spec_map = json.loads((TESTDATA / "golden_spec.json").read_text())["spec"]
    z, mag, coils = analyze.axis_profile(spec_map, baseline["design"])
    assert len(z) == len(mag) == 4 * spec.n_axis - 3
    direct = oracle.on_axis_field(coils, np.array(z))
    assert np.allclose(mag, direct, rtol=0, atol=0)
    # 剖面的最大值就是上报的基线喉部场
    golden = json.loads((TESTDATA / "golden_baseline.json").read_text())
    assert max(mag) == pytest.approx(golden["metrics"]["B_throat_T"], rel=1e-6)
    assert z[int(np.argmax(mag))] == pytest.approx(golden["metrics"]["z_throat_m"], abs=1e-3)


def test_recompute_aggregate_matches_the_synthetic_truth(spec, baseline):
    report = make_report(spec, baseline)
    mine = analyze.recompute_aggregate(report)
    runs = [r for r in report["runs"] if r["algorithm"] == "evolution"]
    scores = [r["best_score"] for r in runs]
    assert mine["evolution"]["best_mean"] == pytest.approx(sum(scores) / len(scores))
    assert mine["evolution"]["best_min"] == min(scores)
    assert mine["evolution"]["best_max"] == max(scores)
    assert mine["evolution"]["n_seeds"] == 2
    base = report["baseline"]["score"]
    assert mine["evolution"]["n_beating_baseline"] == sum(1 for s in scores if s > base)
    ok, _ = analyze.compare_aggregate(report, verbose=False)
    assert ok


def test_main_reports_a_missing_report(tmp_path, capsys):
    assert analyze.main([str(tmp_path / "absent")]) == 2
    assert "report not found" in capsys.readouterr().err


def test_table_format_is_markdown(spec, baseline):
    table = analyze.benchmark_table(make_report(spec, baseline))
    lines = table.splitlines()
    header = next(i for i, ln in enumerate(lines) if ln.startswith("| method |"))
    assert lines[header + 1].startswith("|---")
    rows = [ln for ln in lines if ln.startswith("| ")
            and not ln.startswith("|---") and not ln.startswith("| method |")]
    assert len(rows) == 1 + 3                              # 人工基线 + 3 个方法
    assert all(ln.count("|") == 9 for ln in rows)
