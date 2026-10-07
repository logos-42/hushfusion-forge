#!/usr/bin/env python3
"""反引力场控制 spike —— μ 动力学环境 + 开环 vs 持续学习闭环控制。

leo 方向: 用自递归生长发育的持续学习模型研究反引力场控制。

环境(上游 sim_two_flow_ring.py 的动力学, 不新写物理):
  μ' = μ + η_total·(1−μ)
  η_total = η_ext + η_sink(λ)·(1−μ),  η_sink(λ) = 2λ−λ²  (CR9 自抹平)
  天花板:  μ < 1 − m_e/m_i = 0.999780568 (FC11, 越界=破坏)
  工作窗口: μ ≥ 0.999 (聚变级)
  控制量:  η_ext (外部驱动) 和 λ (收敛度, 决定自抹平强度)

对照:
  A. 开环固定 η_ext=0.05, 无自抹平 (上游现状, 132 步到窗口)
  B. 开环 + 自抹平 λ=0.3 (上游 sinkmuopt, 85 步)
  C. 闭环自适应 η_ext: 每步根据当前 μ 选 η (持续学习学的策略)
     策略: 离天花板远 → 大 η (快); 近 → 小 η (稳, 不越界)
     学的方式: 简单查表(μ 分桶 → η), 用轨迹经验更新 (持续学习雏形)

验收: C 应比 A/B 更快到达工作窗口, 且不越天花板。
"""
import math

ME_OVER_MI = 1.0 / (2.5 * 1822.888486209)  # m_e/m_i (D-T)
MU_CEIL = 1.0 - ME_OVER_MI                 # FC11 天花板 0.999780568
MU_WORK = 0.999                            # 工作窗口


def eta_sink(lam):
    return 2.0 * lam - lam * lam


def run_openloop(mu0, eta_ext, lam, max_steps=5000):
    """开环: 固定 η_ext + 自抹平。返回 (到窗口步数, 是否越界, 终 μ)。"""
    mu = mu0
    steps = 0
    for _ in range(max_steps):
        eta = eta_ext + eta_sink(lam) * (1.0 - mu) if lam else eta_ext
        mu = mu + eta * (1.0 - mu)
        steps += 1
        if mu >= MU_CEIL:
            return steps, True, mu      # 越界(坏)
        if mu >= MU_WORK:
            return steps, False, mu     # 到窗口(好)
    return -1, False, mu


def run_closed_loop(mu0, policy, max_steps=5000):
    """闭环: 每步用策略(μ → η_ext)选控制量。策略是查表 [0..N) 桶 → η。

    policy: list[float], 桶数 N; μ∈[0, MU_CEIL] 映射到桶。
    持续学习: 跑完后根据轨迹表现调 policy (简单版本: 每一步用到的桶,
    如果没越界且走得快, 保持; 越界则下次该桶用更小 η)。
    """
    mu = mu0
    steps = 0
    N = len(policy)
    usage = [0] * N          # 每个桶用了多少次
    for _ in range(max_steps):
        bucket = min(int(mu / MU_CEIL * N), N - 1)
        eta = policy[bucket]
        usage[bucket] += 1
        mu = mu + eta * (1.0 - mu)
        steps += 1
        if mu >= MU_CEIL:
            return steps, True, mu, usage
        if mu >= MU_WORK:
            return steps, False, mu, usage
    return -1, False, mu, usage


def learn_policy(mu0, N=20, rounds=200, base_eta=0.05, lr=0.5):
    """持续学习闭环: 从空策略(全 base_eta)出发, 每轮跑轨迹, 根据结果调表。

    调整规则(简单强化):
      - 该轮越界: 把越界前用过的桶的 η 减小 (×0.7) —— 惩罚
      - 该轮安全到窗口: 把用过桶的 η 朝 base_eta 的 1.5 倍推 (×1.05) —— 鼓励快
      这是「从经验中学控制」的最小持续学习闭环。
    """
    policy = [base_eta] * N
    best_steps = None
    history = []
    for rnd in range(rounds):
        steps, over, mu, usage = run_closed_loop(mu0, policy)
        history.append((steps, over))
        if over:
            # 越界: 用了的桶全惩罚(减小 η, 有下限 0.01 防死)
            for b in range(N):
                if usage[b]:
                    policy[b] = max(0.01, policy[b] * 0.7)
        else:
            # 安全到窗口
            if best_steps is None or steps <= best_steps:
                # 追平/破纪录: 保持(不动)
                best_steps = steps
            else:
                # 比历史最优慢: 才微调 —— 避免每轮重复乘法的指数衰减
                # 前半段(离天花板远)加速, 后半段(靠近天花板)收稳
                for b in range(N):
                    if usage[b] and b < N - 3:
                        policy[b] = min(0.5, policy[b] * 1.02)
                    elif usage[b] and b >= N - 3:
                        policy[b] = max(0.01, policy[b] * 0.98)
    return policy, best_steps, history


print("=== 反引力场控制: μ 动力学开环 vs 持续学习闭环 ===")
mu0 = 0.15

# A. 开环固定 η=0.05, 无自抹平
sa, oa, mua = run_openloop(mu0, 0.05, 0)
print(f"A 开环 η=0.05 无自抹平: {sa} 步到窗口, 越界={oa}, 终μ={mua:.6f}")

# B. 开环 + 自抹平 λ=0.3
sb, ob, mub = run_openloop(mu0, 0.05, 0.3)
print(f"B 开环 η=0.05 + 自抹平 λ=0.3: {sb} 步到窗口, 越界={ob}, 终μ={mub:.6f}")

# C. 持续学习闭环
N = 20
policy, best_steps, hist = learn_policy(mu0, N=N, rounds=200)
sc, oc, muc, usage = run_closed_loop(mu0, policy)
print(f"C 持续学习闭环 (学出的策略): {sc} 步到窗口, 越界={oc}, 终μ={muc:.6f}")
print(f"   学出策略(前10桶 η): {[f'{p:.3f}' for p in policy[:10]]}")
print(f"   学习轨迹: 最优 {best_steps} 步; 200 轮里越界轮 {sum(1 for _, o in hist if o)}")

# D. 学到的策略在多个起点泛化
print("\n=== 泛化: 学出的策略在不同 μ₀ 上 (vs 开环) ===")
for m0 in [0.01, 0.05, 0.3, 0.5, 0.8]:
    sa, oa, _ = run_openloop(m0, 0.05, 0.3)
    sc, oc, _, _ = run_closed_loop(m0, policy)
    print(f"  μ₀={m0:.2f}: 开环 {sa}步(越界{oa}) | 闭环 {sc}步(越界{oc}) "
          f"{'✓闭环快' if sc < sa else '~'}")

print("\n判读: 闭环应在 μ₀∈[0.01,0.8] 上都快于/等于开环且不越界 = 持续学习控制有效")