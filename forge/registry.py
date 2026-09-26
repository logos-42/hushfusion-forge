"""Design registry — the company's memory, not a log file.

Every design ever scored is one JSONL line with its parameters, its raw physical
terms, its constraint residuals and its lineage (``parent_design``). Two things
follow from taking this seriously:

* the search is *never* the source of truth — the registry is, so a run can be
  re-scored, re-weighted and re-mined afterwards;
* design history is a tree, not a list, because every evolutionary child is
  recorded with the design that produced it. That is what makes questions like
  "which branch produced the most improvement?" answerable, which is the
  primitive of a progress moat.
"""

from __future__ import annotations

import json
from collections import Counter, defaultdict
from pathlib import Path

REQUIRED_FIELDS = (
    "experiment_id",
    "design_id",
    "algorithm",
    "seed",
    "score",
    "params",
    "terms",
    "metrics",
)


class Registry:
    def __init__(self, path: str | Path):
        self.path = Path(path)
        self.path.parent.mkdir(parents=True, exist_ok=True)
        self._n = self._count_existing()

    # -- plumbing ------------------------------------------------------
    def _count_existing(self) -> int:
        if not self.path.exists():
            return 0
        n = 0
        with self.path.open("r", encoding="utf-8") as fh:
            for line in fh:
                if line.strip():
                    n += 1
        return n

    def __len__(self) -> int:
        return self._n

    def next_experiment_id(self) -> int:
        return self._n + 1

    def next_design_id(self) -> str:
        return f"D{self._n + 1:04d}"

    def append(self, record: dict) -> None:
        missing = [f for f in REQUIRED_FIELDS if f not in record]
        if missing:
            raise ValueError(f"registry record missing fields: {missing}")
        with self.path.open("a", encoding="utf-8") as fh:
            fh.write(json.dumps(record, sort_keys=True) + "\n")
        self._n += 1

    def records(self) -> list[dict]:
        if not self.path.exists():
            return []
        out = []
        with self.path.open("r", encoding="utf-8") as fh:
            for line in fh:
                line = line.strip()
                if not line:
                    continue
                try:
                    out.append(json.loads(line))
                except json.JSONDecodeError:
                    # a truncated final line (interrupted run) is skipped, the
                    # rest of the history is still valid
                    continue
        return out

    # -- queries -------------------------------------------------------
    def best(self, feasible_only: bool = False, algorithm: str | None = None) -> dict | None:
        best = None
        for r in self.records():
            if feasible_only and not r.get("feasible", False):
                continue
            if algorithm and r["algorithm"] != algorithm:
                continue
            if best is None or r["score"] > best["score"]:
                best = r
        return best

    def best_per_algorithm(self) -> dict[str, dict]:
        out: dict[str, dict] = {}
        for r in self.records():
            cur = out.get(r["algorithm"])
            if cur is None or r["score"] > cur["score"]:
                out[r["algorithm"]] = r
        return out

    def lineage(self) -> dict[str, list[str]]:
        """design_id -> list of direct children design_ids."""
        tree: dict[str, list[str]] = defaultdict(list)
        for r in self.records():
            if r.get("parent_design"):
                tree[r["parent_design"]].append(r["design_id"])
        return dict(tree)

    def branch_improvement(self) -> list[dict]:
        """Per parent design: how much its best child improved over it."""
        by_id = {r["design_id"]: r for r in self.records()}
        rows = []
        for parent, kids in self.lineage().items():
            p = by_id.get(parent)
            if p is None:
                continue
            kid_best = max(by_id[k]["score"] for k in kids if k in by_id)
            rows.append(
                {
                    "design_id": parent,
                    "n_children": len(kids),
                    "parent_score": p["score"],
                    "best_child_score": kid_best,
                    "gain": kid_best - p["score"],
                }
            )
        rows.sort(key=lambda r: r["gain"], reverse=True)
        return rows

    def summary(self) -> dict:
        recs = self.records()
        if not recs:
            return {"n_records": 0}
        best = max(recs, key=lambda r: r["score"])
        return {
            "n_records": len(recs),
            "n_feasible": sum(1 for r in recs if r.get("feasible")),
            "per_algorithm": dict(Counter(r["algorithm"] for r in recs)),
            "best_score": best["score"],
            "best_design_id": best["design_id"],
            "best_algorithm": best["algorithm"],
            "first_timestamp": recs[0].get("timestamp"),
            "last_timestamp": recs[-1].get("timestamp"),
        }
