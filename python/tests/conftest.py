"""Shared fixtures/helpers for the stage-G auxiliary tests.

Run from the repository root:
    python3 -m pytest python/tests -q
"""

from __future__ import annotations

import json
import sys
from pathlib import Path

import pytest

HERE = Path(__file__).resolve().parent
PY = HERE.parent                          # <repo>/python
AUX = PY / "aux"
ROOT = PY.parent                          # repository root
TESTDATA = ROOT / "testdata"

for p in (str(AUX), str(HERE)):
    if p not in sys.path:
        sys.path.insert(0, p)

import oracle  # noqa: E402
import rules_check  # noqa: E402
import schema_check  # noqa: E402


@pytest.fixture(scope="session")
def spec():
    """The spec under test -- read from testdata/golden_spec.json, never copied."""
    return oracle.Spec.load(TESTDATA / "golden_spec.json")


@pytest.fixture(scope="session")
def grids(spec):
    return oracle.Grids(spec)


@pytest.fixture(scope="session")
def golden_baseline():
    return json.loads((TESTDATA / "golden_baseline.json").read_text())


@pytest.fixture(scope="session")
def golden_samples():
    return json.loads((TESTDATA / "golden_field_samples.json").read_text())["samples"]


@pytest.fixture(scope="session")
def baseline(spec, grids):
    return oracle.textbook_mirror(spec, grids=grids)


def good_record(eid=1, **over):
    """A registry record that satisfies the frozen schema."""
    rec = {
        "experiment_id": eid,
        "design_id": f"D{eid:04d}",
        "generation": 0,
        "algorithm": "evolution",
        "seed": 7,
        "eval_index": eid,
        "tag": "t",
        "timestamp": "1970-01-01T00:00:00Z",
        "score": -0.25,
        "feasible": True,
        "params": {"radius_m": [0.3, 0.5, 0.5, 0.3], "z_m": [-1.0, -0.25, 0.25, 1.0],
                   "current_A": [1.6e6, 4.6e5, 4.6e5, 1.6e6]},
        "terms": {"field": 0.0, "mirror": 0.25, "volume": 0.78, "ripple": 0.0, "cost": 1.0},
        "weighted": {"field": 0.0, "mirror": 0.125, "volume": 0.585, "ripple": -0.0, "cost": -1.0},
        "penalties": {"conductor_field": 0.0, "coil_separation": 0.0, "not_a_mirror": 0.0},
        "metrics": {"B_mid_T": 1.0, "B_throat_T": 3.53, "z_throat_m": -0.9975,
                    "mirror_ratio": 3.53, "volume_good": 0.78, "ripple": 0.0,
                    "B_coil_max_T": 3.41, "min_coil_gap_m": 0.5,
                    "cost_proxy": 1.79e12, "coil_proximity_floor_hit": False,
                    "n_coils": 4, "mu0": 1.2566370614359173e-06},
    }
    rec.update(over)
    return rec


def write_jsonl(path, records):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text("".join(json.dumps(r) + "\n" for r in records))
    return path


__all__ = ["oracle", "rules_check", "schema_check", "TESTDATA", "ROOT", "good_record",
           "write_jsonl"]
