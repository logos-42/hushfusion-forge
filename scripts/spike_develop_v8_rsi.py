#!/usr/bin/env python3
"""发育 spike v8 —— 递归自我改进 (RSI = Recursive Self-Improvement)。

leo 方向: 「RSI 自动递归」= 递归自我改进 —— 发育机制不只按固定阈值触发,
触发参数本身也**根据长层效果递归调整** (改进改进机制)。

v7 教训: RSI 技术指标(涨跌比)在 delta 恒正的单边改善轨迹上恒=100,
  数学上测不出动量衰减 —— RSI 不适用, 不是调参能救的。

v8: 相对衰减探测器 + **自适应阈值**:
  - 每次长层后, 观察新结构 val 是否改善
  - 改善显著 (>5%): 说明触发太松(频繁误触), thresh 收紧 ×0.8
  - 无改善 (<1%): 说明触发太紧(该触发没触发), thresh 放松 ×1.25
  - thresh 夹在 [0.05, 0.6] 防止发散
  - 这就是递归: 触发机制自己学会「多敏感才正确」, 不是人手拍死

对照:
  A. 固定 2 层 / B. 固定 4 层
  C. RelDecay 固定阈值 (v3, thresh=0.2)
  D. RelDecay 自适应阈值 (v8, 递归自我改进) —— 应 ≥ C
任务: deep
"""
import numpy as np
import torch
import torch.nn as nn

torch.manual_seed(0)
np.random.seed(0)


class AdaptiveRelDecayDetector:
    """相对衰减 + 自适应阈值。

    update(val) → 是否触发。
    feedback(improved: bool) → 长层后按效果调 thresh。
    """

    def __init__(self, thresh=0.2, k=3, alpha=0.5, warmup=5):
        self.thresh = thresh
        self.k, self.alpha, self.warmup = k, alpha, warmup
        self.prev = None
        self.ewma = 0.0
        self.early = []
        self.early_avg = None
        self.streak = 0
        self.n = 0
        self.log = []

    def update(self, val):
        self.n += 1
        if self.prev is None:
            self.prev = val
            return False
        imp = (self.prev - val) / abs(self.prev) if self.prev != 0 else 0.0
        if self.n <= self.warmup:
            self.early.append(imp)
            if self.n == self.warmup:
                self.early_avg = max(float(np.mean(self.early)), 1e-9)
            self.ewma = self.alpha * imp + (1 - self.alpha) * self.ewma
            self.prev = val
            return False
        self.ewma = self.alpha * imp + (1 - self.alpha) * self.ewma
        ratio = self.ewma / self.early_avg
        self.streak = self.streak + 1 if ratio < self.thresh else 0
        self.prev = val
        return self.streak >= self.k

    def feedback(self, improved):
        """按长层效果调整阈值 (递归自我改进)。

        v8a 教训: 反馈信号设计反了 —— 「改善」= 长层有价值, 该**放松**阈值
        继续长; 「无改善」= 长层没价值, 该**收紧**阈值少长。原逻辑(改善→
        收紧)让 deep 任务少长层而退化。修正: 改善→放松×1.25, 无改善→收紧×0.8。
        """
        old = self.thresh
        if improved:
            self.thresh = min(0.6, self.thresh * 1.25)   # 长层有价值, 放松多长
            note = "放松(长层有价值)"
        else:
            self.thresh = max(0.05, self.thresh * 0.8)   # 长层无价值, 收紧少长
            note = "收紧(长层无价值)"
        self.log.append((old, self.thresh, note))
        return old, self.thresh, note


def make_data(n=6000):
    x = np.random.uniform(-1.5, 1.5, size=(n, 3)).astype(np.float32)
    y = np.sin(x[:, 0] * x[:, 1] * x[:, 2]) + np.cos(x[:, 0] * x[:, 1])
    return x, y.astype(np.float32).reshape(-1, 1)


class DevelopmentalMLP(nn.Module):
    def __init__(self, in_dim, hidden=(8,)):
        super().__init__()
        self.blocks = nn.ModuleList()
        dims = [in_dim] + list(hidden) + [1]
        for i in range(len(dims) - 1):
            self.blocks.append(nn.Linear(dims[i], dims[i + 1]))
            if i < len(dims) - 2:
                self.blocks.append(nn.Tanh())
        self.hiddens = list(hidden)

    @property
    def n_hidden(self):
        return len(self.hiddens)

    def add_layer(self, width=None):
        out_lin = self.blocks[-1]
        w = width or max(4, out_lin.in_features // 2)
        new_lin = nn.Linear(out_lin.in_features, w)
        nn.init.xavier_uniform_(new_lin.weight)
        nn.init.zeros_(new_lin.bias)
        out_lin.in_features = w
        s = 1.0 / np.sqrt(w)
        out_lin.weight = nn.Parameter(torch.randn(out_lin.out_features, w) * s)
        out_lin.bias = nn.Parameter(torch.zeros(out_lin.out_features))
        self.blocks.insert(len(self.blocks) - 1, new_lin)
        self.blocks.insert(len(self.blocks) - 1, nn.Tanh())
        self.hiddens.append(w)

    def forward(self, x):
        h = x
        for b in self.blocks:
            h = b(h)
        return h


def train(model, x, y, epochs=2500, lr=1e-2, seed=0, eval_every=50,
          develop=False, adaptive=False, max_grows=3,
          feedback_after=6):
    """feedback_after: 长层后过 N 周期评估效果并反馈给探测器。"""
    torch.manual_seed(seed)
    n = len(x)
    idx = np.random.RandomState(seed).permutation(n)
    tr, va = idx[:int(0.8 * n)], idx[int(0.8 * n):]
    xt, yt = torch.tensor(x[tr]), torch.tensor(y[tr])
    xv, yv = torch.tensor(x[va]), torch.tensor(y[va])
    opt = torch.optim.Adam(model.parameters(), lr=lr)
    best_val = float('inf')
    grows = 0
    dev_log = []
    grow_val = None       # 长层时的 val
    grow_after = 0        # 长层后经过的周期
    grow_best = None      # 长层后的最佳 val
    detector = AdaptiveRelDecayDetector() if develop else None

    for ep in range(epochs):
        opt.zero_grad()
        loss = ((model(xt) - yt) ** 2).mean()
        loss.backward()
        opt.step()
        if (ep + 1) % eval_every == 0:
            with torch.no_grad():
                vloss = ((model(xv) - yv) ** 2).mean().item()
            best_val = min(best_val, vloss)

            if develop:
                # 长层后效果跟踪 → 反馈
                if grow_val is not None and adaptive:
                    grow_best = min(grow_best, vloss)
                    grow_after += 1
                    if grow_after == feedback_after:
                        improved = grow_best < grow_val * 0.95   # 改善 >5%
                        old, new, note = detector.feedback(improved)
                        dev_log.append({"feedback_ep": ep + 1, "old_thresh": old,
                                        "new_thresh": new, "note": note,
                                        "grow_val": grow_val, "grow_best": grow_best})
                        print(f"    [递归] ep={ep+1} 长层效果={'改善' if improved else '无改善'} "
                              f"({grow_val:.5f}→{grow_best:.5f}) thresh {old:.3f}→{new:.3f} {note}",
                              flush=True)
                        grow_val, grow_best, grow_after = None, None, 0

                if grows < max_grows and detector.update(vloss):
                    model.add_layer()
                    grows += 1
                    grow_val = vloss
                    grow_best = vloss
                    grow_after = 0
                    dev_log.append({"grow_ep": ep + 1, "n_hidden": model.n_hidden,
                                    "thresh": detector.thresh})
                    print(f"    [发育] ep={ep+1} val={vloss:.5f} 相对衰减(thresh={detector.thresh:.3f}), "
                          f"长层 → {model.n_hidden} 隐藏层", flush=True)
                    opt = torch.optim.Adam(model.parameters(), lr=lr)
                    detector = AdaptiveRelDecayDetector(thresh=detector.thresh)  # 保留学到的阈值
    return best_val, dev_log, model.n_hidden


print("=== 发育 spike v8: 递归自我改进 (自适应阈值) ===")
res = {}
for label, dev, hidden, adaptive in [
    ("对照 2层[8]", False, (8,), False),
    ("对照 4层[8,8,8,8]", False, (8, 8, 8, 8), False),
    ("RelDecay固定阈值", True, (8,), False),
    ("递归自适应阈值", True, (8,), True),
]:
    vals, logs, ns = [], [], []
    for s in range(3):
        if not dev:
            torch.manual_seed(s)
            model = DevelopmentalMLP(3, hidden=hidden)
            x, y = make_data()
            bv, lg, nh = train(model, x, y, seed=s, develop=False)
        else:
            torch.manual_seed(s)
            model = DevelopmentalMLP(3, hidden=(8,))
            x, y = make_data()
            bv, lg, nh = train(model, x, y, seed=s, develop=True, adaptive=adaptive)
        vals.append(bv)
        if dev:
            logs.append(lg); ns.append(nh)
    med = float(np.median(vals))
    res[label] = med
    extra = f"n_hidden={ns}" if dev else ""
    print(f"{label:<18} {med:.6f}   {extra}")
    if dev:
        for i, lg in enumerate(logs):
            grows_n = sum(1 for g in lg if "grow_ep" in g)
            fb = [g for g in lg if "feedback_ep" in g]
            fbline = "; ".join(f"{g['note']}→thresh {g['new_thresh']:.3f}" for g in fb) or "无反馈"
            print(f"    seed{i}: 长层 {grows_n} 次 | {fbline}")

print("\n=== 判读 ===")
l2, l4 = res["对照 2层[8]"], res["对照 4层[8,8,8,8]"]
fix, ada = res["RelDecay固定阈值"], res["递归自适应阈值"]
print(f"  2层 {l2:.6f} / 4层 {l4:.6f}")
print(f"  固定阈值 {fix:.6f} / 自适应 {ada:.6f}")
print(f"  自适应 vs 固定: {(ada - fix) / fix * 100:+.1f}%")
if ada <= fix * 1.05:
    print("  ✓ 递归自我改进 ≥ 固定阈值 (自动调参不劣于人拍)")
else:
    print("  ~ 自适应未超固定 —— 看反馈日志")