#!/usr/bin/env python3
"""Emit cross-language golden files from the Python reference implementation.

The Python package under ``python/forge`` is the *reference* implementation of
the v0.1 physics (it is the one whose analytic anchors were verified first).
These golden files are what the Go engine is measured against, so a porting bug
cannot hide: two independent implementations, one numerical artifact.

Outputs (all under ``testdata/``):
  * ``golden_spec.json``        — the default spec + the exact JSON key names
  * ``golden_baseline.json``    — hand-designed textbook mirror: design, metrics,
                                  raw terms, weighted terms, penalties, score
  * ``golden_field_samples.json`` — B_r/B_z/|B| at a deterministic point set for
                                  several designs (on-axis, off-axis, near-coil,
                                  far-field), full float precision

Usage:  python3 scripts/emit_golden.py
"""

from __future__ import annotations

import json
import platform
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
for cand in (ROOT, ROOT / "python"):
    if (cand / "forge").is_dir():
        sys.path.insert(0, str(cand))
        break

import numpy as np  # noqa: E402

import forge  # noqa: E402
from forge.config import Spec  # noqa: E402
from forge.objective import Evaluator  # noqa: E402
from forge.optimization.baselines import textbook_mirror  # noqa: E402
from forge.physics.geometry import Coil, coils_to_vector, vector_to_coils  # noqa: E402
from forge.simulation.solver import AnalyticalVacuumSolver  # noqa: E402

OUT = ROOT / "testdata"


def main() -> int:
    OUT.mkdir(parents=True, exist_ok=True)
    spec = Spec()
    solver = AnalyticalVacuumSolver()
    baseline = textbook_mirror(spec)
    ev = Evaluator(spec, cost_ref=baseline.cost)
    res = ev.evaluate(np.array(baseline.design, float))

    (OUT / "golden_spec.json").write_text(
        json.dumps(
            {
                "spec": spec.as_dict(),
                "provenance": {
                    "generated_by": "scripts/emit_golden.py",
                    "forge_version": forge.__version__,
                    "python": platform.python_version(),
                    "numpy": np.__version__,
                },
            },
            indent=2,
            sort_keys=True,
        ),
        encoding="utf-8",
    )

    (OUT / "golden_baseline.json").write_text(
        json.dumps(
            {
                "name": baseline.name,
                "note": baseline.note,
                "design": [float(v) for v in res.design],
                "coils": [c.as_dict() for c in windowed(baseline.coils)],
                "cost_proxy": float(baseline.cost),
                "cost_ref": float(ev.cost_ref),
                "score": float(res.score),
                "feasible": bool(res.feasible),
                "terms": {k: float(v) for k, v in res.terms.items()},
                "weighted": {k: float(v) for k, v in res.weighted.items()},
                "penalties": {k: float(v) for k, v in res.penalties.items()},
                "metrics": {k: (float(v) if isinstance(v, (int, float)) else v) for k, v in res.metrics.items()},
            },
            indent=2,
            sort_keys=True,
        ),
        encoding="utf-8",
    )

    # ---- field samples: several designs x a deterministic point set ----------
    designs = {
        "textbook_mirror": [float(v) for v in baseline.design],
        "single_loop": [0.35, 0.35, 0.35, 0.35, 0.0, 0.0, 0.0, 0.0, 3.0e5, 3.0e5, 3.0e5, 3.0e5],
        "wide_asymmetric": [0.20, 0.55, 0.80, 0.95, -1.10, -0.15, 0.30, 1.05, 8.0e5, 2.5e5, 1.2e5, 9.0e5],
    }
    rng = np.random.default_rng(20260926)
    pts_r = np.concatenate(
        [
            np.array([0.0, 0.0, 0.0, 0.0]),                    # on axis
            np.array([0.02, 0.05, 0.10, 0.15]),                # plasma-radius range
            rng.uniform(0.0, 0.6, 20),
            np.array([0.79, 0.81, 1.19, 1.21]),                # near the search-box edge
        ]
    )
    pts_z = np.concatenate(
        [
            np.array([0.0, 0.25, 0.75, 1.35]),
            np.array([-0.05, 0.0, 0.05, 0.10]),
            rng.uniform(-1.4, 1.4, 20),
            np.array([0.0, 0.3, -0.6, 0.9]),
        ]
    )
    samples = []
    for name, x in designs.items():
        coils = vector_to_coils(np.array(x, float), spec)
        br, bz = solver.field_components(coils, pts_r, pts_z)
        mag = np.sqrt(br**2 + bz**2)
        samples.append(
            {
                "design_name": name,
                "design": x,
                "points_r": [float(v) for v in pts_r],
                "points_z": [float(v) for v in pts_z],
                "br": [float(v) for v in br],
                "bz": [float(v) for v in bz],
                "b_mag": [float(v) for v in mag],
                "solver": solver.name,
            }
        )

    (OUT / "golden_field_samples.json").write_text(
        json.dumps({"samples": samples}, indent=2, sort_keys=True), encoding="utf-8"
    )

    print(f"wrote {OUT}/golden_spec.json")
    print(f"wrote {OUT}/golden_baseline.json  (score={res.score:.10f})")
    print(f"wrote {OUT}/golden_field_samples.json  ({len(samples)} designs x {pts_r.size} points)")
    print(f"baseline: cost={baseline.cost:.6e} I_cell={baseline.coils[0].current:.1f} "
          f"I_throat={baseline.coils[2].current:.1f} B_mid={res.metrics['B_mid_T']:.6f}")
    return 0


def windowed(coils):
    return tuple(coils)


if __name__ == "__main__":
    raise SystemExit(main())
