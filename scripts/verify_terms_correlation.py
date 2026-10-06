#!/usr/bin/env python3
"""方向2验证: 用物理 terms+penalties 当特征, 预测 score 的相关性 ρ。

核心洞察: score 是 terms 的确定函数(score=Σw·term - w_pen·Σpenalty)。
把 terms+penalties 当特征, 预测就从"学未知复杂函数"变成"近似已知线性函数",
应该大幅可学(对比原始参数 ρ≈0.05)。

严格5-fold: 用 forge 输出里的 terms+penalties 特征, MLP 学, holdout 预测, 算 ρ。
若 ρ 显著高(>0.5) ⟹ 方向对, 后续守护进程改用 terms 特征引导 forge。

用法: python3 scripts/verify_terms_correlation.py
"""
from __future__ import annotations

import glob
import json
import pathlib
import sys

import numpy as np

ROOT = pathlib.Path(__file__).resolve().parents[1]
HEADLESS = pathlib.Path(__file__).resolve().parents[2] / "headless"
sys.path.insert(0, str(HEADLESS))
sys.path.insert(0, str(ROOT / "scripts"))
from oml_continual_core import OMLDesignLearner  # noqa: E402


def main() -> int:
    # 收集所有 forge 输出里的 (terms+penalties, score)
    Xs, Ys = [], []
    for reg in sorted(glob.glob(str(ROOT / "runs" / "oml_v4_*" / "out" / "registry.jsonl"))):
        for line in open(reg):
            r = json.loads(line)
            if r.get("score") is None or not r.get("feasible", True):
                continue
            t = r.get("terms", {})
            p = r.get("penalties", {})
            feats = [t.get("field", 0), t.get("mirror", 0), t.get("volume", 0),
                     t.get("ripple", 0), t.get("cost", 0),
                     p.get("conductor_field", 0), p.get("coil_separation", 0),
                     p.get("not_a_mirror", 0), p.get("clearance", 0)]
            Xs.append(feats); Ys.append(float(r["score"]))
    if len(Xs) < 50:
        print(f"数据太少: {len(Xs)} 条 (需≥50)")
        return 1
    X = np.array(Xs); Y = np.array(Ys)
    print(f"收集 {len(X)} 条 (terms+penalties 特征, 9维)", flush=True)

    from scipy.stats import pearsonr, spearmanr
    from numpy.random import default_rng

    rng = default_rng(0)
    idx = rng.permutation(len(X))
    folds = [idx[i::5] for i in range(5)]
    pear, spea = [], []
    for fold in range(5):
        te_idx = np.sort(folds[fold])
        tr_idx = np.array(sorted(set(range(len(X))) - set(te_idx.tolist())))
        model = OMLDesignLearner(in_dim=X.shape[1], seed=fold)
        model.oml_step(X[tr_idx], Y[tr_idx], X[te_idx][:60], Y[te_idx][:60], K=3)
        pred = model.predict(X[te_idx])
        p, _ = pearsonr(pred, Y[te_idx])
        s, _ = spearmanr(pred, Y[te_idx])
        pear.append(p); spea.append(s)
        print(f"[fold {fold}] Pearson ρ={p:+.3f}  Spearman ρ={s:+.3f}", flush=True)

    rho = float(np.mean(pear))
    print(f"\n=== terms特征 预测 score (5-fold隔离) ===", flush=True)
    print(f"Pearson ρ 均值 {rho:+.3f} (范围 {min(pear):+.3f}~{max(pear):+.3f})", flush=True)
    print(f"Spearman ρ 均值 {np.mean(spea):+.3f}", flush=True)
    print(f"对比: 原始参数特征 ρ≈+0.05", flush=True)
    verdict = "方向对(terms特征大幅可学) → 守护进程改用terms引导" if rho > 0.5 else (
        "仍弱(terms特征也未学动) → 需换思路")
    print(f"判定: {verdict}", flush=True)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
