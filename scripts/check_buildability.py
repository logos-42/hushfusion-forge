#!/usr/bin/env python3
"""G23 可造性门（0.1.2；口径与判据见 docs/version-0.1.2.md §1 与 §5）。

三条**一起**判，缺一条不算过：

  (a) 人工基线在 0.1.2 下仍然可行，且 `clearance` 罚项**精确**为 0
      → 抓的失败模式：新约束把真装置也判死。那时「机器打败人类」比的就成了
        「谁的基线更不可造」，而不是设计能力。

  (b) 0.1.0 的最优**退化解**（runs/phase0/results.json 的 best）在 0.1.2 下不可行，
      且 `clearance` 罚项 > 0
      → 抓的失败模式：约束没咬住它要咬的东西。这条是 0.1.2 存在的**全部理由**；
        没有它，「退化解被踢出去了」就只是一次性观测，不是可回归的事实。

  (c) 净空的闭式解 == 在圆环面上密采样的独立路径（只在不重叠时，相对 < 1e-9）
      → 抓的失败模式：几何算错。净空是本版唯一一处新数学，必须有第二条独立路径。
        另外钉住符号约定：导体侵入约束区域时净空必须精确等于 −t_pack/2。

不采信任何自述：三条都用**当前代码**现场重算。

退出码: 0 通过；1 失败；2 前置产物缺失（由 scripts/verify.sh 的 _opt 前置条件拦截）。
"""

from __future__ import annotations

import json
import math
import random
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "python" / "aux"))

import oracle  # noqa: E402

TOL = 1e-9


def fail(msg: str) -> None:
    print(f"  [FAIL] {msg}")


def main() -> int:
    spec_path = ROOT / "testdata" / "golden_spec.json"
    golden_base = ROOT / "testdata" / "golden_baseline.json"
    phase0 = ROOT / "runs" / "phase0" / "results.json"
    for p in (spec_path, golden_base, phase0):
        if not p.is_file():
            print(f"G23: 缺前置产物 {p}", file=sys.stderr)
            return 2

    spec = oracle.Spec.load(spec_path)
    grids = oracle.Grids(spec)
    base = oracle.textbook_mirror(spec, grids=grids)
    base_eval = oracle.evaluate(base["design"], spec, base["cost_proxy"], grids)
    ok = True

    # ---- (a) 基线必须仍然可行，且 clearance 罚项精确为 0 -------------------- #
    clr = base_eval["penalties"]["clearance"]
    print(f"  G23a 基线: feasible={base_eval['feasible']} clearance 罚项={clr!r} "
          f"最小净空={base_eval['metrics']['min_clearance_m']!r} m "
          f"(要求 >= {spec.min_clearance_allowed} m)")
    if not base_eval["feasible"]:
        fail("人工基线在新判据下不可行 —— 那会让「机器打败人类」变成「谁更不可造」")
        ok = False
    if clr != 0.0:
        fail(f"人工基线的 clearance 罚项 = {clr!r}，必须精确为 0.0")
        ok = False

    # ---- (b) 0.1.0 的退化解必须被咬住 ------------------------------------- #
    prev = json.loads(phase0.read_text(encoding="utf-8"))
    best = prev.get("best") or {}
    x = best.get("design")
    if not x:
        fail("runs/phase0/results.json 里没有 best.design")
        return 1
    got = oracle.evaluate(x, spec, base["cost_proxy"], grids)
    pen = got["penalties"]["clearance"]
    print(f"  G23b 0.1.0 最优 {best.get('design_id')}: score {best.get('score')} → "
          f"{got['score']:.12g}; feasible {best.get('feasible')} → {got['feasible']}; "
          f"clearance 罚项 = {pen:.6g} (最小净空 {got['metrics']['min_clearance_m']:.6g} m)")
    if got["feasible"]:
        fail("0.1.0 的退化解在 0.1.2 下仍然可行 —— 可造性约束没咬住它")
        ok = False
    if not pen > 0.0:
        fail(f"0.1.0 的退化解 clearance 罚项 = {pen!r}，必须 > 0")
        ok = False

    # ---- (c) 几何两条独立路径 --------------------------------------------- #
    rng = random.Random(0)
    worst, n_cmp, n_inside = 0.0, 0, 0
    for _ in range(200):
        xx = ([rng.uniform(*spec.radius_bounds) for _ in range(spec.n_coils)]
              + [rng.uniform(*spec.z_bounds) for _ in range(spec.n_coils)]
              + [rng.uniform(*spec.current_bounds) for _ in range(spec.n_coils)])
        coils = oracle.vector_to_coils(xx, spec)
        closed = oracle.min_clearance(coils, spec)
        if closed >= 0.0:
            brute = oracle.min_clearance_surface_bruteforce(coils, spec)
            worst = max(worst, abs(closed - brute))
            n_cmp += 1
        else:
            n_inside += 1
    # 侵入情形：导体整个坐在约束区域里 ⟹ 净空必须精确等于 −t_pack/2
    inside = [(0.10, 0.0, 1.0e6)] + list(base["coils"])[1:]
    pen_val = oracle.min_clearance(inside, spec)
    print(f"  G23c 闭式 vs 面采样: {n_cmp} 个不重叠样本 max|差| = {worst:.3e} (容差 {TOL:g}); "
          f"{n_inside} 个侵入样本; 侵入情形净空 = {pen_val!r} (应 = {-spec.t_pack / 2!r})")
    if n_cmp < 50:
        fail(f"不重叠样本只有 {n_cmp} 个，这条子检查没被真正行使")
        ok = False
    if worst > TOL:
        fail(f"闭式解与独立面采样差 {worst:.3e} > {TOL:g}")
        ok = False
    if pen_val != -spec.t_pack / 2.0:
        fail(f"侵入情形的净空 = {pen_val!r}，应精确为 {-spec.t_pack / 2.0!r}")
        ok = False

    print("G23 可造性门: " + ("PASS" if ok else "FAIL"))
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
