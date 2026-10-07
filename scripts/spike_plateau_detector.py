#!/usr/bin/env python3
"""平台期探测器 —— V6.1/7 发育触发判据 v2。

上一轮(§5c): 「残差不降」是坏判据, 浅层从不停滞 (慢性低效逼近)。
修法: 用「损失下降**速度**」判平台 —— 最近 k 个周期, 指数加权平均改善率 < ε。
浅层慢性逼近: vdot 小但恒正 → 不触发(对, 它确实还在学);
真正的平台: vdot ≈ 0 持续 k 周期 → 触发。

这里验证探测器本身: 构造已知场景, 看它能否正确识别
  A. 训练初期(快速下降) → 不触发 ✓
  B. 训练后期(慢速逼近, 仍正) → 不触发 ✓ (与坏判据的关键区别)
  C. 真平台(val 平坦) → 触发 ✓
"""
import numpy as np


class PlateauDetector:
    """窗口内指数加权改善率 < eps 持续 k 次 ⟹ 平台期。"""

    def __init__(self, eps=1e-3, k=3, alpha=0.5):
        self.eps = eps
        self.k = k
        self.alpha = alpha
        self.prev = None
        self.ewma_imp = 0.0
        self.streak = 0
        self.log = []

    def update(self, val):
        """喂一个 val; 返回 (is_plateau, ewma_improvement)。"""
        if self.prev is None:
            self.prev = val
            return False, 0.0
        imp = (self.prev - val) / abs(self.prev) if self.prev != 0 else 0.0
        self.ewma_imp = self.alpha * imp + (1 - self.alpha) * self.ewma_imp
        if self.ewma_imp < self.eps:
            self.streak += 1
        else:
            self.streak = 0
        self.prev = val
        is_plat = self.streak >= self.k
        self.log.append((val, self.ewma_imp, self.streak, is_plat))
        return is_plat, self.ewma_imp

    def summary(self):
        for i, (val, imp, streak, plat) in enumerate(self.log):
            mark = " <<< PLATEAU" if plat else ""
            print(f"  {i:3d} val={val:.5f} ewma_imp={imp:+.5f} streak={streak}{mark}")


def run_scenario(name, vals, show=True):
    print(f"\n=== {name} ===")
    det = PlateauDetector(eps=1e-3, k=3)
    trig = []
    for i, v in enumerate(vals):
        plat, imp = det.update(v)
        if plat:
            trig.append(i)
    if show:
        det.summary()
    print(f"  触发平台: {len(trig)} 次, 在 {trig[:5]}...")
    return trig


# A. 训练初期: 快速下降 (每步 -10%)
fast = [1.0 * (0.9 ** i) for i in range(20)]
# B. 慢性逼近: 慢速但恒正 (1/i 衰减, 永远在降)
slow = [1.0 / (i + 1) for i in range(1, 30)]
# C. 真平台: 先降后完全平坦
plat = [1.0 * (0.9 ** i) for i in range(5)] + [0.59] * 15
# D. 平台后回升(任务漂移): 平坦后 val 变差
drift = [1.0 * (0.9 ** i) for i in range(5)] + [0.59] * 10 + [0.8, 0.85, 0.82, 0.83, 0.84, 0.83, 0.84, 0.85]

ta = run_scenario("A 快速下降(应不触发)", fast, show=False)
tb = run_scenario("B 慢性逼近(应不触发——与坏判据区别)", slow, show=False)
tc = run_scenario("C 真平台(应触发)", plat)
td = run_scenario("D 漂移后平台(应触发, 且是结构重塑信号)", drift, show=False)

print("\n=== 判读 ===")
print(f"A {len(ta)} 次 (期望 0)  {'✓' if len(ta)==0 else '✗'}")
print(f"B {len(tb)} 次 (期望 0 —— 探测器的核心价值)  {'✓' if len(tb)==0 else '✗'}")
print(f"C {len(tc)} 次 (期望 ≥1)  {'✓' if len(tc)>0 else '✗'}")
print(f"D {len(td)} 次 (期望 ≥1, 漂移也是平台信号)  {'✓' if len(td)>0 else '✗'}")