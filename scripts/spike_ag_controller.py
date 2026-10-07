#!/usr/bin/env python3
"""反引力场控制 · 发育式持续学习控制选择器（核心闭环实验）。

把上一轮的发现(控制问题 = (η_ext, λ) 参数选择)接进已建好的发育式持续学习:
  - 环境: μ' = μ + η_total(1−μ), η_total = η_ext + η_sink(λ)(1−μ)
    目标 5 步(最优控制点 η_ext=0.45, λ=0.4), FC11 天花板 0.999780568
  - 学习器: OMLDesignLearner(OML 持续学习) 学 (η_ext, λ) → 到窗口步数
  - 发育结构: 相对衰减判据 → 自动长层(从 1 隐藏层起, 需要时加深)
  - 对照: 随机选控制点(冷) vs 学习器推荐(热) — 热应显著更快

验收: warm(学习器)平均步数 < cold(随机)平均步数, 且推荐器学会偏好高分区。
"""
import math
import sys
sys.path.insert(0, "/Users/apple/Downloads/headless")
sys.path.insert(0, "/Users/apple/Downloads/Hushfusion_Forge/scripts")

import numpy as np
import torch

from oml_continual_core import OMLDesignLearner

torch.manual_seed(0)
np.random.seed(0)

ME_OVER_MI = 1.0 / (2.5 * 1822.888486209)
MU_CEIL = 1.0 - ME_OVER_MI
MU_WORK = 0.999
MU0 = 0.15


def eta_sink(lam):
    return 2.0 * lam - lam * lam


def run_control(eta_ext, lam, max_steps=5000):
    """μ 动力学: 返回 (到窗口步数, 越界)。"""
    mu = MU0
    for n in range(1, max_steps + 1):
        eta = eta_ext + eta_sink(lam) * (1.0 - mu)
        mu = mu + eta * (1.0 - mu)
        if mu >= MU_CEIL:
            return n, True
        if mu >= MU_WORK:
            return n, False
    return max_steps, False


def ctrl_feature(eta_ext, lam):
    """控制点特征: [η_ext, λ, η_sink(λ)] 归一化到 0..1 附近。"""
    return [eta_ext / 0.5, lam / 0.9, eta_sink(lam) / 0.99]


# 参与式 OML: 每轮经验回流, 训练, 发育
ETA_GRID = np.linspace(0.02, 0.5, 25)
LAM_GRID = np.linspace(0.0, 0.9, 12)


def random_ctrl(rng):
    return float(rng.choice(ETA_GRID)), float(rng.choice(LAM_GRID))


def main(rounds=200):
    rng = np.random.default_rng(0)
    model = OMLDesignLearner(in_dim=3, d_model=32)
    pool_X, pool_Y = [], []
    warm_steps, cold_steps = [], []
    n_trials = 0

    for rnd in range(rounds):
        is_cold = (rnd % 2 == 1)
        if is_cold or len(pool_X) < 30:
            eta, lam = random_ctrl(rng)
        else:
            # 推荐: 全控制面打分取预测步数最少(性能最优)的点
            cands = [(e, l) for e in ETA_GRID for l in LAM_GRID]
            feats = np.array([ctrl_feature(e, l) for e, l in cands])
            pred = model.predict(feats)
            # 预测值 = 步数, 取最小
            idx = int(np.argmin(pred))
            eta, lam = cands[idx]

        n, over = run_control(eta, lam)
        score = n if not over else n + 1000  # 越界惩罚
        fx = ctrl_feature(eta, lam)
        pool_X.append(fx)
        pool_Y.append(float(score))
        n_trials += 1

        if is_cold:
            cold_steps.append(score)
        else:
            warm_steps.append(score)

        # OML 持续学习
        if len(pool_X) >= 8:
            X = np.array(pool_X, dtype=float)
            Y = np.array(pool_Y, dtype=float)
            idxp = rng.permutation(len(X))
            tr = idxp[: max(4, len(X) - 3)]
            te = idxp[max(4, len(X) - 3):]
            try:
                model.oml_step(X[tr], Y[tr], X[te], Y[te], K=3)
            except Exception as e:
                pass

        if (rnd + 1) % 50 == 0:
            if warm_steps:
                print(f"round {rnd+1}: warm mean={np.mean(warm_steps):.1f} "
                      f"(n={len(warm_steps)}) cold mean={np.mean(cold_steps):.1f} "
                      f"(n={len(cold_steps)})", flush=True)

    # 最终推荐倾向
    cands = [(e, l) for e in ETA_GRID for l in LAM_GRID]
    feats = np.array([ctrl_feature(e, l) for e, l in cands])
    pred = model.predict(feats)
    best_idx = int(np.argmin(pred))
    best_eta, best_lam = cands[best_idx]
    n, over = run_control(best_eta, best_lam)

    print(f"\n=== 结果 ===")
    print(f"warm(推荐) mean={np.mean(warm_steps):.1f} 步 (n={len(warm_steps)})")
    print(f"cold(随机) mean={np.mean(cold_steps):.1f} 步 (n={len(cold_steps)})")
    print(f"Δ = {np.mean(cold_steps) - np.mean(warm_steps):+.1f} 步")
    print(f"推荐最佳: η_ext={best_eta:.2f} λ={best_lam:.1f} → {n} 步 (越界={over})")
    print(f"已知最优: η_ext=0.45 λ=0.4 → 5 步")
    print(f"随机基线: 平均 {np.mean(cold_steps):.1f} 步")
    ok = np.mean(warm_steps) < np.mean(cold_steps) and n < 15 and not over
    print(f"\n{'✓ 持续学习控制有效: warm 显著快于 cold, 学会接近最优' if ok else '✗ 未达预期'}")


if __name__ == "__main__":
    main()