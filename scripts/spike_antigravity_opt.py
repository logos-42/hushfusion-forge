#!/usr/bin/env python3
"""反引力控制 spike 2 —— 最优控制扫描: 每 μ 桶的最优 η 形状。

问题: 持续学习闭环学到 132 步(=开环), 因探索不足。先问: 控制空间里
到底有没有比开环更快的策略? 用穷举找每 μ 桶的最优 η (离线最优), 
给持续学习一个目标形状, 也验证「闭环控制 > 开环」是否成立。

方法: μ∈[0, 0.99978] 分 20 桶。对每个桶 b, 从 μ_b 出发用固定 η 跑一步,
  统计「该 η 全局最快到窗口且不越界」的得分。用动态规划思想:
  对每个起点 μ, 试 η ∈ {0.01..0.5}, 每步选 η(μ), 贪心向前看 ——
  简化: 对 μ 均匀网格, 试每个 η, 算到窗口的步数, 找最小(带越界惩罚)。
"""
import math
import numpy as np

ME_OVER_MI = 1.0 / (2.5 * 1822.888486209)
MU_CEIL = 1.0 - ME_OVER_MI      # 0.999780568
MU_WORK = 0.999


def steps_to_window(mu0, eta, with_sink=True, lam=0.3, max_steps=5000):
    """从 mu0 用固定 eta (+自抹平) 跑到窗口的步数; 越界返回 None。"""
    mu = mu0
    for n in range(1, max_steps + 1):
        eta_t = eta + (eta_sink(lam) * (1.0 - mu) if with_sink else 0.0)
        mu = mu + eta_t * (1.0 - mu)
        if mu >= MU_CEIL:
            return None
        if mu >= MU_WORK:
            return n
    return None


def eta_sink(lam):
    return 2.0 * lam - lam * lam


# 对一组起点, 找最优固定 η (允许每桶不同)
grid = np.linspace(0.01, 0.95, 30)
etas = np.linspace(0.005, 0.3, 40)

print("=== 最优 η 扫描: 每起点最优固定 η (无自抹平) ===")
opt = []
for mu0 in grid:
    best_n, best_eta = None, None
    for eta in etas:
        n = steps_to_window(mu0, eta, with_sink=False)
        if n is not None and (best_n is None or n < best_n):
            best_n, best_eta = n, eta
    opt.append((mu0, best_eta, best_n))
    if mu0 in [0.01, 0.05, 0.1, 0.15, 0.3, 0.5, 0.8, 0.9]:
        print(f"  μ₀={mu0:.2f}: 最优 η*={best_eta:.3f} → {best_n} 步" if best_n else
              f"  μ₀={mu0:.2f}: 无安全 η")

print("\n=== 最优 η(μ) 形状 (是否单调? 控制规律是什么?) ===")
for i in range(0, len(opt), 6):
    seg = opt[i:i+6]
    print("  " + " | ".join(f"μ={m:.2f}:η={e:.3f}" if e else f"μ={m:.2f}:无" for m, e, _ in seg))

# 与开环固定 η=0.05 对比
print("\n=== 最优分段控制 vs 开环固定 η=0.05 (无自抹平) ===")
openloop_050 = steps_to_window(0.15, 0.05, with_sink=False)
# 最优分段: 每步用该 μ 附近的最优 η (查表插值)
def closed_optimal(mu0, opt_table):
    mus = np.array([o[0] for o in opt_table])
    etas = np.array([o[1] if o[1] else 0.05 for o in opt_table])
    mu = mu0
    for n in range(1, 5000):
        eta = float(np.interp(mu, mus, etas))
        mu = mu + eta * (1.0 - mu)
        if mu >= MU_CEIL:
            return None
        if mu >= MU_WORK:
            return n
    return None

print(f"  开环固定 η=0.05: {openloop_050} 步")
for m0 in [0.01, 0.05, 0.15, 0.3, 0.5, 0.8]:
    cn = closed_optimal(m0, opt)
    print(f"  μ₀={m0:.2f}: 最优分段 {cn} 步 {'✓快于开环' if cn and cn < openloop_050 else '~'}")

print("\n结论: 若最优分段显著快于开环 → 闭环控制有真实空间, 持续学习的目标形状就是这个 η*(μ)")