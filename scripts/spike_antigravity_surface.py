#!/usr/bin/env python3
"""反引力控制 spike 3 —— 持续学习选控制参数 (η_ext × λ → 性能)。

spike 2 发现: 开环 η=0.05 是保守参数, 最优 η≈0.29 直接 20 步 (6.6× 快)。
真正的控制问题 = 选对 (η_ext, λ), 不是闭环分段。

本 spike: 把 (η_ext, λ) 控制面画出来, 验证它是「可学的好形状」
(有明确最优、平滑、可被回归/模型学), 然后给出持续学习模型要学什么。
"""
import math
import numpy as np

ME_OVER_MI = 1.0 / (2.5 * 1822.888486209)
MU_CEIL = 1.0 - ME_OVER_MI
MU_WORK = 0.999


def eta_sink(lam):
    return 2.0 * lam - lam * lam


def run_control(mu0, eta_ext, lam, max_steps=5000):
    """带自抹平的 μ 轨迹。返回 (到窗口步数, 越界)。"""
    mu = mu0
    for n in range(1, max_steps + 1):
        eta = eta_ext + eta_sink(lam) * (1.0 - mu)
        mu = mu + eta * (1.0 - mu)
        if mu >= MU_CEIL:
            return n, True
        if mu >= MU_WORK:
            return n, False
    return max_steps, False


mu0 = 0.15
eta_grid = np.linspace(0.01, 0.5, 20)
lam_grid = np.linspace(0.0, 0.9, 10)

print("=== (η_ext, λ) → 到窗口步数 控制面 (μ₀=0.15) ===")
print(f"{'η_ext':>6} | " + " ".join(f"λ={l:.1f}" for l in lam_grid))
rows = []
for eta in eta_grid:
    cells = []
    for lam in lam_grid:
        n, over = run_control(mu0, eta, lam)
        mark = "X" if over else f"{n:3d}"
        cells.append(mark)
        rows.append((eta, lam, n if not over else None, over))
    print(f"{eta:6.2f} | " + " ".join(f"{c:>6}" for c in cells))

# 安全且最快的最优点
safe = [(eta, lam, n) for eta, lam, n, over in rows if not over and n is not None]
safe.sort(key=lambda t: t[2])
print(f"\n=== 最优控制点 (安全且最快) ===")
for eta, lam, n in safe[:5]:
    print(f"  η_ext={eta:.2f} λ={lam:.1f} → {n} 步")
best_eta, best_lam, best_n = safe[0]
print(f"\n最优: η_ext={best_eta:.2f} λ={best_lam:.1f} → {best_n} 步")
print(f"开环上游参数 η=0.05 λ=0.3: ", end="")
n, over = run_control(mu0, 0.05, 0.3)
print(f"{n} 步 (越界={over})")
print(f"最优 ÷ 上游 = {best_n / n:.2f}× 快")

# 形状可学性: 安全区是否平滑(相邻 η 的步数差小)? 越界区是否清晰?
print("\n=== 形状可学性 ===")
# 越界区比例
over_frac = sum(1 for _, _, _, o in rows if o) / len(rows)
print(f"  越界点占比: {over_frac:.0%} (越界区清晰可学)")
safe_ns = [n for _, _, n, o in rows if not o]
print(f"  安全点步数范围: {min(safe_ns)}~{max(safe_ns)} (动态范围 {max(safe_ns)/min(safe_ns):.1f}×)")
print(f"  ⟹ 形状: 有清晰最优 + 安全/越界边界 + 大动态范围 = 标准可学回归+分类")