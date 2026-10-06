#!/usr/bin/env python3
"""方向2验证: 搜索配置→效果 的持续学习 —— 配置推荐器可否学。

核心: 放弃预测"设计→score"(ρ≈0.05死路), 改学"哪个搜索配置更高效"。
  配置空间: method×seed×budget(mu/lam等默认), 每种配置跑 forge, 得 best_score(确定性)。
  目标: 模型学 配置→best_score, 学会推荐高配置。

验证: 先跑一批配置(多样), 测 配置特征→best_score 是否可学(有真实信号)。
若可学 → 持续学习配置推荐器成立(越学越会配置forge)。

用法: python3 scripts/verify_config_learning.py
"""
from __future__ import annotations

import pathlib
import subprocess
import sys

import numpy as np

ROOT = pathlib.Path(__file__).resolve().parents[1]
HEADLESS = pathlib.Path(__file__).resolve().parents[2] / "headless"
sys.path.insert(0, str(HEADLESS))
sys.path.insert(0, str(ROOT / "scripts"))


def run_forge(method, seed, budget):
    """跑一次 forge 搜索, 返回 best_score。"""
    r = subprocess.run(["go", "run", "./cmd/forge", "run",
                        "--method", method, "--seed", str(seed), "--budget", str(budget)],
                       cwd=str(ROOT), capture_output=True, text=True)
    out = r.stdout
    import re
    m = re.search(r"best_score=([-\d.eE]+)", out)
    if not m:
        return None
    return float(m.group(1))


def main() -> int:
    methods = ["random", "lhs", "evolution", "evolution_warm"]
    budgets = [50, 100, 200]
    seeds = [0, 1, 2]

    # 跑一批配置, 收集 (配置特征, best_score)
    Xs, Ys = [], []
    method_idx = {m: i for i, m in enumerate(methods)}
    score_grid = {}
    print("跑配置网格 (method × budget × seed)...", flush=True)
    for method in methods:
        for budget in budgets:
            for seed in seeds:
                s = run_forge(method, seed, budget)
                if s is None:
                    print(f"  {method}/{budget}/{seed}: 无score", flush=True)
                    continue
                # 配置特征: [method_onehot(4), log_budget, seed%2]
                feat = [1.0 if i == method_idx[method] else 0.0 for i in range(len(methods))]
                feat += [np.log10(budget), float(seed % 2)]
                Xs.append(feat)
                Ys.append(s)
                score_grid[(method, budget)] = score_grid.get((method, budget), []) + [s]
                print(f"  {method:15s} b={budget:3d} s={seed} → {s:.3f}", flush=True)

    if len(Xs) < 20:
        print("数据太少")
        return 1
    X = np.array(Xs); Y = np.array(Ys)
    print(f"\n收集 {len(X)} 个配置 (method×budget×seed), score范围 {Y.min():.3f}~{Y.max():.3f}", flush=True)

    # 跨方法差异: 同一方法不同 seed 稳定吗?(确定性)
    print("\n=== 配置效应(方法×budget, 跨seed均值) ===", flush=True)
    for (method, budget), scores in sorted(score_grid.items()):
        print(f"  {method:15s} b={budget:3d}: 均值 {np.mean(scores):.3f} std {np.std(scores):.3f}", flush=True)

    # MLP 学 配置→score (5-fold)
    from scipy.stats import pearsonr, spearmanr
    from oml_continual_core import OMLDesignLearner
    from numpy.random import default_rng

    rng = default_rng(0)
    idx = rng.permutation(len(X))
    folds = [idx[i::5] for i in range(5)]
    pear, spea = [], []
    for fold in range(5):
        te_idx = np.sort(folds[fold])
        tr_idx = np.array(sorted(set(range(len(X))) - set(te_idx.tolist())))
        model = OMLDesignLearner(in_dim=X.shape[1], seed=fold)
        model.oml_step(X[tr_idx], Y[tr_idx], X[te_idx][:10], Y[te_idx][:10], K=3)
        pred = model.predict(X[te_idx])
        p, _ = pearsonr(pred, Y[te_idx])
        s, _ = spearmanr(pred, Y[te_idx])
        pear.append(p); spea.append(s)
        print(f"[fold {fold}] Pearson ρ={p:+.3f} Spearman ρ={s:+.3f} "
              f"(n_test={len(te_idx)})", flush=True)

    rho = float(np.mean(pear))
    print(f"\n=== 配置→score 学习 (5-fold) ===", flush=True)
    print(f"Pearson ρ 均值 {rho:+.3f} (对比 设计→score 的 +0.05)", flush=True)
    verdict = ("配置可学(信号强) → 持续学习配置推荐器可行" if rho > 0.5 else
               "配置也弱 → 需换思路")
    print(f"判定: {verdict}", flush=True)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())