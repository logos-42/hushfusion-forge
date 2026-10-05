#!/usr/bin/env python3
"""选择能力扩展验证 —— 对比 exploit / UCB / diverse 三种选样的真实价值。

用户要【让持续学习的选择能力变得更多】(更强更多样) + 接入更多操作入口。
纯新增: 不改正在运行的守护进程(那是已验证的 exploit 基线), 独立离线验证新增策略。

在 registry 数据上, 用 OML 学到的模型分三种策略各选 top-K,
比较它们的真实 score(registry 里的)中位 vs 随机基线(lift)。
证明: UCB / diverse 在【保持价值】的同时【覆盖更广】(多样性↑), 选择能力更强更多样。

用法: python3 scripts/verify_select_strategies.py
"""
from __future__ import annotations

import argparse
import json
import pathlib
import sys

import numpy as np

ROOT = pathlib.Path(__file__).resolve().parents[1]
HEADLESS = pathlib.Path(__file__).resolve().parents[2] / "headless"
sys.path.insert(0, str(HEADLESS))
sys.path.insert(0, str(ROOT / "scripts"))
from design_loop import load_designs  # noqa: E402
from oml_continual_core import OMLDesignLearner  # noqa: E402


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--registry", default=str(ROOT / "runs" / "phase1" / "registry.jsonl"))
    ap.add_argument("--support-n", type=int, default=300)
    ap.add_argument("--pool-n", type=int, default=500)   # 候选池
    ap.add_argument("--k", type=int, default=20)          # 每种策略选 top-K
    ap.add_argument("--folds", type=int, default=3)       # 交叉折叠(多seed更可信)
    ap.add_argument("--out", default=str(ROOT / "artifacts" / "select_strategies.json"))
    args = ap.parse_args()

    rows = load_designs(pathlib.Path(args.registry))
    X = np.array([r[0] for r in rows])
    Y = np.array([r[1] for r in rows]).astype(np.float32)
    print(f"载入 {len(X)} 个设计", flush=True)

    results = []
    for fold in range(args.folds):
        rng = np.random.default_rng(fold)
        idx = rng.permutation(len(X))
        sup = idx[: args.support_n]
        pool = idx[args.support_n: args.support_n + args.pool_n]
        Xs, ys = X[sup], Y[sup]
        Xp, Yp = X[pool], Y[pool]

        model = OMLDesignLearner(in_dim=X.shape[1], seed=fold)
        # OML 学一轮(在 support 上适应, 建立价值模型)
        qry = pool[args.k:]  # query 用于外循环更新
        model.oml_step(Xs, ys, X[qry], Y[qry], K=3)

        k = min(args.k, len(pool))
        strategies = {
            "exploit": model.select_topk(Xp, k),
            "ucb": model.select_topk_ucb(Xp, k, lam=0.5),
            "diverse": model.select_topk_diverse(Xp, k, lam=0.3),
        }
        # 随机基线
        rand_idx = rng.choice(len(pool), k, replace=False)
        rand_lift = float(np.median(Yp[rand_idx]) - np.median(Yp))
        row = {"fold": fold, "rand_median": float(np.median(Yp)),
               "rand_lift": rand_lift}
        for name, sel in strategies.items():
            med = float(np.median(Yp[sel]))
            lift = med - float(np.median(Yp))
            # 多样性: 所选设计两两平均距离(越大=覆盖越广)
            selX = Xp[sel]
            if len(sel) > 1:
                d = np.linalg.norm(selX[:, None, :] - selX[None, :, :], axis=2)
                diversity = float(d[np.triu_indices(len(sel), 1)].mean())
            else:
                diversity = 0.0
            row[f"{name}_median"] = med
            row[f"{name}_lift"] = lift
            row[f"{name}_diversity"] = diversity
        results.append(row)

    # 汇总
    print(f"\n=== 选择策略对比 ({args.folds} 折, pool={args.pool_n}, k={args.k}) ===", flush=True)
    print(f"{'策略':<10}{'lift均值':>10}{'lift>0':>8}{'多样性':>10}  (lift=所选真实score中位 vs 池中位)", flush=True)
    agg = {}
    for name in ["exploit", "ucb", "diverse"]:
        lifts = [r[f"{name}_lift"] for r in results]
        divs = [r[f"{name}_diversity"] for r in results]
        agg[name] = {"lift_mean": float(np.mean(lifts)), "lift_pos": int(sum(1 for x in lifts if x > 0)),
                     "diversity": float(np.mean(divs))}
        print(f"{name:<10}{np.mean(lifts):>10.3f}{sum(1 for x in lifts if x>0):>4}/{len(lifts):<4}{np.mean(divs):>10.3f}", flush=True)

    out = pathlib.Path(args.out)
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(json.dumps({"results": results, "summary": agg}, indent=2))
    print(f"结果 → {out}", flush=True)

    # 门: 三种策略 lift 都>0(不损失价值), 且 ucb/diverse 多样性 ≥ exploit(覆盖更广)
    ok = all(agg[n]["lift_mean"] > 0 for n in ["exploit", "ucb", "diverse"])
    ok = ok and agg["ucb"]["diversity"] >= agg["exploit"]["diversity"]
    ok = ok and agg["diverse"]["diversity"] >= agg["exploit"]["diversity"]
    print(f"\n门判定: {'PASS —— 三种策略都有效且更广' if ok else 'FAIL'}", flush=True)
    return 0 if ok else 1


if __name__ == "__main__":
    raise SystemExit(main())
