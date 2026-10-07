#!/usr/bin/env python3
"""反引力控制 · 持续学习选择器 v2 —— μ₀ 漂移下的在线适应（持续学习的真实舞台）。

v1 教训:
  1. 确定性固定 μ₀ 时, 学习无信息增益(扫描即得 5 步) —— 学习不必要。
  2. 随机采样覆盖不到最优区(300 点点格, 最优区 ~7%, 200 轮仅 2 次) —— 探索不足。
  3. 越界惩罚 1000 直接污染回归, 大 η 全被预测成坏点 —— 目标函数设计错。

v2 改造:
  - **环境漂移**: 每轮 μ₀ ∈ [0.01, 0.8] 随机, 最优 (η, λ) 随 μ₀ 变 ——
    行为最优(闭式)在线移动, 持续学习必须跟踪, 信息增益真实。
  - **log 压缩**: 目标 = log(步数), 越界 = +log(5000)≈8.5 (压缩动态范围)。
  - **对数特征 + μ₀**: 特征 [η, λ, μ₀] (归一化), 让模型见 μ₀ 维度。
  - **UCB 推荐**: 预测低步数 + 探索未评估区(η 区间分桶计数), 不是纯 argmin。

对照: 同 μ₀ 下, warm(学习器) vs cold(随机) —— 应显著更少步数。
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
LOG_PENALTY = math.log(5000.0)  # 越界目标值


def eta_sink(lam):
    return 2.0 * lam - lam * lam


def run_control(mu0, eta_ext, lam, max_steps=5000):
    mu = mu0
    for n in range(1, max_steps + 1):
        eta = eta_ext + eta_sink(lam) * (1.0 - mu)
        mu = mu + eta * (1.0 - mu)
        if mu >= MU_CEIL:
            return n, True
        if mu >= MU_WORK:
            return n, False
    return max_steps, False


def ctrl_feature(eta_ext, lam, mu0):
    return [eta_ext / 0.5, lam / 0.9, mu0 / 0.8]


ETA_GRID = np.linspace(0.02, 0.5, 20)
LAM_GRID = np.linspace(0.0, 0.9, 10)


def random_ctrl(rng):
    return float(rng.choice(ETA_GRID)), float(rng.choice(LAM_GRID))


def main(rounds=300):
    rng = np.random.default_rng(0)
    model = OMLDesignLearner(in_dim=3, d_model=32)
    pool_X, pool_Y = [], []
    warm_delta, cold_delta = [], []   # 相对随机基线的节省(同 μ₀ 配对)
    warm_abs, cold_abs = [], []
    # UCB 探索计数: η 区间 × λ 区间
    N_ETA, N_LAM = 5, 4
    explore_counts = np.zeros((N_ETA, N_LAM)) + 1.0

    def bucket(e, l):
        return min(int(e / 0.5 * N_ETA), N_ETA - 1), min(int(l / 0.9 * N_LAM), N_LAM - 1)

    for rnd in range(rounds):
        mu0 = float(rng.uniform(0.01, 0.8))
        # 行为最优参考(线上扫描该 μ₀ 下最优, 用于评估学习是否逼近)
        best_ref, best_ref_n = None, None
        for e in ETA_GRID:
            for l in LAM_GRID:
                n, over = run_control(mu0, e, l)
                if not over and (best_ref_n is None or n < best_ref_n):
                    best_ref_n, best_ref = n, (e, l)

        is_cold = (rnd % 2 == 1)
        if is_cold or len(pool_X) < 40:
            eta, lam = random_ctrl(rng)
        else:
            cands = [(e, l) for e in ETA_GRID for l in LAM_GRID]
            feats = np.array([ctrl_feature(e, l, mu0) for e, l in cands])
            pred = model.predict(feats)
            # UCB: 预测 + 探索项(该桶评估少 → 加分探索)
            ucb = np.array([pred[i] - 0.25 * np.log(explore_counts[bucket(e, l)[0], bucket(e, l)[1]])
                            for i, (e, l) in enumerate(cands)])
            idx = int(np.argmin(ucb))
            eta, lam = cands[idx]
            explore_counts[bucket(eta, lam)] += 1.0

        n, over = run_control(mu0, eta, lam)
        score = math.log(n) if not over else LOG_PENALTY
        fx = ctrl_feature(eta, lam, mu0)
        pool_X.append(fx)
        pool_Y.append(float(score))

        # 与「该 μ₀ 下最优」的差距(学习质量): 步数比
        if is_cold:
            cold_abs.append(n)
            if best_ref_n:
                cold_delta.append(n / best_ref_n)
        else:
            warm_abs.append(n)
            if best_ref_n:
                warm_delta.append(n / best_ref_n)

        if len(pool_X) >= 8:
            X = np.array(pool_X, dtype=float)
            Y = np.array(pool_Y, dtype=float)
            idxp = rng.permutation(len(X))
            tr = idxp[: max(4, len(X) - 3)]
            te = idxp[max(4, len(X) - 3):]
            try:
                model.oml_step(X[tr], Y[tr], X[te], Y[te], K=3)
            except Exception:
                pass

        if (rnd + 1) % 75 == 0 and warm_abs:
            print(f"round {rnd+1}: warm 均值 {np.mean(warm_abs):.0f} 步 (最优比 {np.mean(warm_delta):.2f}×) | "
                  f"cold 均值 {np.mean(cold_abs):.0f} 步 (最优比 {np.mean(cold_delta):.2f}×)", flush=True)

    print(f"\n=== 结果 (300 轮, μ₀ 漂移) ===")
    print(f"warm(学习器): 均值 {np.mean(warm_abs):.0f} 步, 最优步数比 {np.mean(warm_delta):.2f}×")
    print(f"cold(随机):   均值 {np.mean(cold_abs):.0f} 步, 最优步数比 {np.mean(cold_delta):.2f}×")
    print(f"行为最优参考: 均值 {np.mean([run_control(m, *best)[0] for m in [0.05,0.2,0.5,0.7]]) if False else '见下'}")
    # 学习器最终推荐质量(固定几个 μ₀)
    print("\n推荐质量(固定 μ₀ 验证):")
    for m0 in [0.05, 0.2, 0.5, 0.7]:
        cands = [(e, l) for e in ETA_GRID for l in LAM_GRID]
        feats = np.array([ctrl_feature(e, l, m0) for e, l in cands])
        pred = model.predict(feats)
        idx = int(np.argmin(pred))
        e, l = cands[idx]
        n, over = run_control(m0, e, l)
        # 该 μ₀ 真最优
        bn, beet = None, None
        for ee in ETA_GRID:
            for ll in LAM_GRID:
                nn_, oo = run_control(m0, ee, ll)
                if not oo and (bn is None or nn_ < bn):
                    bn, beet = nn_, (ee, ll)
        print(f"  μ₀={m0:.2f}: 推荐 η={e:.2f} λ={l:.1f} → {n}步 | 真最优 {bn}步 ({beet}) "
              f"| 比 {n/bn:.2f}×")

    ok = np.mean(warm_delta) < np.mean(cold_delta)
    print(f"\n{'✓ 持续学习在线适应有效: warm 比 cold 更接近行为最优' if ok else '✗ 未超随机'}")


if __name__ == "__main__":
    main()