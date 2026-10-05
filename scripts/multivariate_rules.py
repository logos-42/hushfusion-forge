#!/usr/bin/env python3
"""多变量规则抽象 —— 从 registry 挖掘"参数组合 → score"的可迁移规则。

Phase B/C 证明单参数规则（"参数→单项 term"）跨任务失效（Phase C: rule-cold 在 S1/S2/S3 全负或~0）。
本脚本升级为**多变量判别**：逻辑回归在 top-quartile vs bottom-quartile 的 score 上训练，
找出"哪些参数组合"共同区分高低分设计，并用 cross-validation 量可学性、用跨-spec 迁移测通用性。

用法:
    python3 scripts/multivariate_rules.py                       # 在 phase1 上挖 + CV
    python3 scripts/multivariate_rules.py --test-transfer       # 测跨 spec 迁移(phaseC)

发现（phase1, CV=0.978）:
    I_0..I_3 全偏小（coef -2.0~-2.5, 主导）+ r_0 偏小 + z_0/z_1 偏大、z_2/z_3 偏小
    ⟹ 高 score 设计的"形状"是: 4 线圈电流都小 + 半径小 + 轴向聚在特定区间 —— 组合效应
跨 spec 迁移: S1 0.703 / S2 0.684 / S3 0.685 (0.5=不迁移) —— 显著优于单参数规则(Phase C ~0)
"""

from __future__ import annotations

import json
import sys
from pathlib import Path

import numpy as np

ROOT = Path(__file__).resolve().parents[1]


def load(reg_path: Path):
    """读 registry, 返回 (score, 12维设计向量) 列表, 只看 feasible。"""
    reg = [json.loads(l) for l in open(reg_path)]
    feas = [r for r in reg if r.get("feasible")]
    rows = []
    for r in feas:
        p = r["params"]
        vec = list(p["radius_m"]) + list(p["z_m"]) + list(p["current_A"])
        rows.append((r["score"], vec))
    return rows


def fit_discriminator(rows, lr=0.5, iters=3000):
    """在 top/bottom quartile 上训练逻辑回归判别器, 返回 (w, b, mu, sd)。"""
    scores = np.array([x[0] for x in rows])
    X = np.array([x[1] for x in rows])
    q_hi, q_lo = np.percentile(scores, 75), np.percentile(scores, 25)
    y = np.where(scores >= q_hi, 1, np.where(scores <= q_lo, 0, -1))
    mask = y != -1
    Xm, ym = X[mask], y[mask].astype(float)
    mu, sd = Xm.mean(0), Xm.std(0)
    Xn = (Xm - mu) / (sd + 1e-12)
    n, d = Xn.shape
    w, b = np.zeros(d), 0.0
    for _ in range(iters):
        z = Xn @ w + b
        p = 1 / (1 + np.exp(-z))
        w -= lr * (Xn.T @ (p - ym) / n)
        b -= lr * (p - ym).mean()
    return w, b, mu, sd


def cv_accuracy(rows, k=5, seed=0):
    """5-fold CV 准确率 —— 测这个任务里参数组合能被学得多好。"""
    scores = np.array([x[0] for x in rows])
    X = np.array([x[1] for x in rows])
    q_hi, q_lo = np.percentile(scores, 75), np.percentile(scores, 25)
    y = np.where(scores >= q_hi, 1, np.where(scores <= q_lo, 0, -1))
    mask = y != -1
    Xm, ym = X[mask], y[mask].astype(float)
    mu, sd = Xm.mean(0), Xm.std(0)
    Xn = (Xm - mu) / (sd + 1e-12)
    rng = np.random.default_rng(seed)
    idx = rng.permutation(len(ym))
    accs = []
    for k_ in range(k):
        te = idx[k_::k]
        tr = np.setdiff1d(idx, te)
        w, b = fit_discriminator_from(Xn[tr], ym[tr])
        accs.append(((Xn[te] @ w + b > 0) == ym[te]).mean())
    return float(np.mean(accs)), float(np.std(accs))


def fit_discriminator_from(Xn, ym, lr=0.5, iters=3000):
    n, d = Xn.shape
    w, b = np.zeros(d), 0.0
    for _ in range(iters):
        z = Xn @ w + b
        p = 1 / (1 + np.exp(-z))
        w -= lr * (Xn.T @ (p - ym) / n)
        b -= lr * (p - ym).mean()
    return w, b


def test_transfer(train_rows, test_rows, label):
    """用 train_rows 学到的判别器, 直接套到 test_rows 上测迁移。"""
    w, b, mu, sd = fit_discriminator(train_rows)
    scores = np.array([x[0] for x in test_rows])
    X = np.array([x[1] for x in test_rows])
    q_hi, q_lo = np.percentile(scores, 75), np.percentile(scores, 25)
    y = np.where(scores >= q_hi, 1, np.where(scores <= q_lo, 0, -1))
    mask = y != -1
    Xm, ym = X[mask], y[mask].astype(float)
    Xn = (Xm - mu) / (sd + 1e-12)  # 用训练集的标准化参数
    pred = (Xn @ w + b) > 0
    acc = (pred == ym).mean()
    print(f"  {label}: 判别器迁移区分高低分 = {acc:.3f}  [0.5=不迁移, 1.0=完全迁移]")
    return acc


def main() -> int:
    names = [f"r_{i}" for i in range(4)] + [f"z_{i}" for i in range(4)] + [f"I_{i}" for i in range(4)]
    s0 = load(ROOT / "runs" / "phase1" / "registry.jsonl")

    print(f"S0(phase1) 挖多变量规则, feasible={len(s0)}")
    acc, std = cv_accuracy(s0)
    print(f"  多变量判别器 5-fold CV: {acc:.3f} (±{std:.3f})  [0.5=纯猜, 1.0=完美]")
    w, b, _, _ = fit_discriminator(s0)
    print("\n  参数重要性（标准化系数, 越大越关键）:")
    order = np.argsort(-np.abs(w))
    for i in order:
        sign = "越大→高分" if w[i] > 0 else "越小→高分"
        print(f"    {names[i]:<5} coef={w[i]:+.3f}  {sign}")

    if "--test-transfer" in sys.argv:
        print("\n  跨 spec 迁移（判别器在 S0 学, 套到 phaseC 各变体）:")
        for tag, label in [("S1_geometry_b1000", "S1 几何间距"),
                           ("S2_field_b1000", "S2 目标场"),
                           ("S3_plasma_b1000", "S3 等离子体")]:
            test_rows = load(ROOT / "runs" / "phaseC" / tag / "registry.jsonl")
            test_transfer(s0, test_rows, label)
        print("\n  对比: 单参数规则(Phase C)跨 spec rule-cold ≈ 0; 多变量规则 > 0.68")
        print("  ⟹ 组合效应比单参数更接近设计本质, 跨任务存活更好。")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
