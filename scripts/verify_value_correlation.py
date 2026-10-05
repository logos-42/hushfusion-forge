#!/usr/bin/env python3
"""根因验证: OML 模型预测价值 vs forge 真实 score 的相关性 ρ。

判断"持续学习模型有没有学会'什么设计好'":
  - ρ 强(>0.3) → 模型学到真信号, 引导 forge 才有基础
  - ρ 弱(<0.1) → 模型没学会, 后续引导都是空中楼阁(根因)

严格: 训练/预测隔离 —— 用 OML 在 support 上学, 在 holdout query 上预测,
      对比预测值与真实 score, 算 Pearson ρ 和 Spearman ρ。

用法: python3 scripts/verify_value_correlation.py
"""
from __future__ import annotations

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
    rows = load_designs(pathlib.Path(ROOT / "runs" / "phase1" / "registry.jsonl"))
    X = np.array([r[0] for r in rows])
    Y = np.array([r[1] for r in rows]).astype(np.float32)
    print(f"载入 {len(X)} 个设计", flush=True)

    # 5-fold 严格隔离: 每折 OML 学 support, 在独立 holdout 上预测, 算 ρ
    from scipy.stats import pearsonr, spearmanr
    from numpy.random import default_rng

    rng = default_rng(0)
    n = len(X)
    idx = rng.permutation(n)
    folds = [idx[i::5] for i in range(5)]  # 交错切 = 5-fold
    pear, spea = [], []
    for fold in range(5):
        te_idx = np.sort(folds[fold])
        tr_idx = np.array(sorted(set(range(n)) - set(te_idx.tolist())))
        # OML 在 train 上适应(3步), 在 test 上预测(从未见过)
        model = OMLDesignLearner(in_dim=X.shape[1], seed=fold)
        model.oml_step(X[tr_idx], Y[tr_idx], X[te_idx][:60], Y[te_idx][:60], K=3)
        pred = model.predict(X[te_idx])
        p, _ = pearsonr(pred, Y[te_idx])
        s, _ = spearmanr(pred, Y[te_idx])
        pear.append(p); spea.append(s)
        print(f"[fold {fold}] Pearson ρ={p:+.3f}  Spearman ρ={s:+.3f}", flush=True)

    print(f"\n=== 价值预测相关性(5-fold 隔离) ===", flush=True)
    print(f"Pearson ρ 均值 {np.mean(pear):+.3f} (范围 {min(pear):+.3f}~{max(pear):+.3f})", flush=True)
    print(f"Spearman ρ 均值 {np.mean(spea):+.3f} (范围 {min(spea):+.3f}~{max(spea):+.3f})", flush=True)
    rho = np.mean(pear)
    verdict = "模型学会真信号(ρ强), 引导有基础" if rho > 0.3 else (
        "信号弱(ρ弱), 模型没学会'什么好设计' → 引导forge是徒劳(根因)")
    print(f"判定: {verdict}", flush=True)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
