#!/usr/bin/env python3
"""发育触发判据 v3 —— 相对衰减(结构容量天花板探测器)。

v2 教训: 「绝对平台」(EWMA 改善 < eps)在浅层上永不触发 —— 浅层 3000 轮
仍在慢性下降 (min EWMA 0.0093 > 1e-3)。等它自然到平台 = 永远等不到。

v3 判据: 相对衰减 —— 当前窗口的改善率 vs 本结构自己的早期改善率。
  ratio = 最近 k 周期 EWMA 改善率 / 训练前段(前 k 周期)平均改善率
  ratio < 0.05  ⟹ 这个结构已榨干它自己能给的(相对它自己), 该长层。
它不管绝对速度(浅层慢但相对自己也在放缓), 只问「相比早期, 现在还剩多少改善力」。

对照场景:
  - 真平台: ratio → 0, 触发 ✓
  - 慢性逼近(浅层): 早期快, 后期慢 → ratio 降到 ~0, 触发 ✓ (v2 不能, v3 能)
  - 快速学习初期: ratio 高, 不触发 ✓
"""
import numpy as np


class RelDecayDetector:
    """相对衰减探测器: 当前改善率 / 早期平均改善率 < ratio_thresh 持续 k 次 ⟹ 触发。"""

    def __init__(self, ratio_thresh=0.10, k=3, alpha=0.5, warmup=5):
        self.thresh = ratio_thresh
        self.k = k
        self.alpha = alpha
        self.warmup = warmup
        self.prev = None
        self.ewma_imp = 0.0
        self.early_imps = []
        self.early_avg = None
        self.streak = 0
        self.n = 0

    def update(self, val):
        self.n += 1
        if self.prev is None:
            self.prev = val
            return False
        imp = (self.prev - val) / abs(self.prev) if self.prev != 0 else 0.0
        if self.n <= self.warmup:
            self.early_imps.append(imp)
            if self.n == self.warmup:
                self.early_avg = float(np.mean(self.early_imps))
            self.ewma_imp = self.alpha * imp + (1 - self.alpha) * self.ewma_imp
            self.prev = val
            return False
        self.ewma_imp = self.alpha * imp + (1 - self.alpha) * self.ewma_imp
        ratio = self.ewma_imp / self.early_avg if self.early_avg and self.early_avg > 0 else 0.0
        self.streak = self.streak + 1 if ratio < self.thresh else 0
        self.prev = val
        return self.streak >= self.k


def run(name, vals, show=False):
    print(f"\n=== {name} ===")
    det = RelDecayDetector(ratio_thresh=0.10, k=3, warmup=5)
    trig = []
    for i, v in enumerate(vals):
        if det.update(v):
            trig.append(i)
    print(f"  触发: {len(trig)} 次, 在 {trig[:8]}...")
    return trig


# A. 快速下降 → 不触发
fast = [1.0 * (0.9 ** i) for i in range(20)]
# B. 慢性逼近 (1/i): 早期快后期慢 → v3 应触发 (v2 不能)
slow = [1.0 / (i + 1) for i in range(1, 40)]
# C. 真平台: 先降后平坦 → 触发
plat = [1.0 * (0.9 ** i) for i in range(5)] + [0.59] * 20
# D. 浅层真轨迹 (实测): e 3000轮采样 → 应触发 (它确实在衰减)
shallow_real = [0.1343, 0.0470, 0.0363, 0.0278, 0.0220, 0.0197, 0.0184, 0.0177,
                0.0168, 0.0162, 0.0157, 0.0152, 0.0148, 0.0145, 0.0142, 0.0140,
                0.0137, 0.0135, 0.0133, 0.0131, 0.0129, 0.0128, 0.0127, 0.0125,
                0.0123, 0.0122, 0.0121, 0.0119, 0.0117, 0.0115]

ta = run("A 快速下降(不触发)", fast)
tb = run("B 慢性逼近(v2不能,v3应触发)", slow)
tc = run("C 真平台(触发)", plat)
td = run("D 浅层真实轨迹(应触发: 相对早期明显衰减)", shallow_real)

print("\n=== 判读 ===")
print(f"A {len(ta)} 次 (期望 0)  {'✓' if len(ta)==0 else '✗'}")
print(f"B {len(tb)} 次 (期望 ≥1)  {'✓' if len(tb)>0 else '✗'}")
print(f"C {len(tc)} 次 (期望 ≥1)  {'✓' if len(tc)>0 else '✗'}")
print(f"D {len(td)} 次 (期望 ≥1 —— 关键: 真实浅层也能被识别)  {'✓' if len(td)>0 else '✗'}")