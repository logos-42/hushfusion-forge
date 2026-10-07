#!/usr/bin/env python3
"""发育 spike v7 —— RSI 自动递归探测器 (RSI-of-RSI 二阶动量)。

leo 方向: 「RSI 自动递归」—— 发育触发不该用手拍死的阈值, 而该用标准动量
指标(RSI) + 递归(动量的动量)自动判断「改善动量枯竭」→ 长层。

RSI(n) = 100 - 100/(1+RS), RS = avg_gain/avg_loss (n 周期)
  - 持续改善 (avg_loss≈0) ⟹ RSI→100 (强动量)
  - 改善停滞 (avg_gain≈0) ⟹ RSI→0   (动量枯竭 = 该长层)
  - 退化       (avg_gain<avg_loss) ⟹ RSI<50

自动递归:
  1. 一阶 RSI: 原 val 序列 → RSI(t)
  2. 二阶 RSI: 对 RSI(t) 序列再算 RSI → RSI₂(t) (动量的动量)
  3. 触发: 一阶 RSI 跌破阈值 且 二阶 RSI 也在低位 = 动量枯竭被递归确认 ⟹ 长层
     (只靠一阶会误触发在单个噪声回落上; 二阶确认滤掉它)

对照:
  A. 固定 2 层 (浅层上限)
  B. 固定 4 层 (深层上限)
  C. RelDecay 发育 (v3, 现有判据)
  D. RSI 递归发育 (v7, 新判据) —— 应接近 C 且优于 A
任务: deep (嵌套乘积)
"""
import numpy as np
import torch
import torch.nn as nn

torch.manual_seed(0)
np.random.seed(0)


class RSIRecursiveDetector:
    """RSI 递归探测器。

    一阶 RSI 序列 + 二阶 RSI 递归确认。触发 = 一阶 < thresh1 且
    二阶 < thresh2 (动量枯竭被递归确认) 持续 k 周期。
    """

    def __init__(self, n=8, thresh1=40.0, thresh2=45.0, k=3,
                 warmup_periods=4):
        self.n = n
        self.thresh1 = thresh1
        self.thresh2 = thresh2
        self.k = k
        self.warmup = warmup_periods  # 前几周期不算(等 RSI 序列攒够)
        self.vals = []
        self.rsi_hist = []
        self.streak = 0

    @staticmethod
    def rsi_from_deltas(gains_avg, losses_avg):
        if losses_avg <= 1e-12:
            return 100.0 if gains_avg > 1e-12 else 50.0
        rs = gains_avg / losses_avg
        return 100.0 - 100.0 / (1.0 + rs)

    def _rsi(self, seq):
        """对一维序列算 RSI(n): 返回序列自身(长度 len) 的 RSI 时间序列 + 最后值。"""
        if len(seq) <= self.n:
            return None
        out = []
        for i in range(self.n, len(seq)):
            deltas = [seq[j] - seq[j - 1] for j in range(i - self.n + 1, i + 1)]
            # 注意: val 下降=改善, 所以 gain = 下降量 = -(delta)
            gains = [max(0.0, -d) for d in deltas]
            losses = [max(0.0, d) for d in deltas]
            ga, la = float(np.mean(gains)), float(np.mean(losses))
            out.append(self.rsi_from_deltas(ga, la))
        return out

    def update(self, val):
        """喂一个 val; 返回是否触发长层。"""
        self.vals.append(val)
        if len(self.vals) < self.n + 2:
            return False
        # 一阶 RSI
        rsi1_full = self._rsi(self.vals)
        if rsi1_full is None:
            return False
        rsi1 = rsi1_full[-1]
        self.rsi_hist.append(rsi1)
        # 二阶 RSI (对一阶序列再算)
        rsi2 = None
        if len(self.rsi_hist) >= self.n + 2:
            rsi2_full = self._rsi(self.rsi_hist)
            rsi2 = rsi2_full[-1] if rsi2_full else None
        # 递归确认: 一阶弱 且 (二阶也弱 或 二阶还没攒够)
        trig1 = rsi1 < self.thresh1
        trig2 = (rsi2 is not None and rsi2 < self.thresh2) or rsi2 is None
        if self.warmup > 0:
            self.warmup -= 1
            return False
        if trig1 and trig2:
            self.streak += 1
        else:
            self.streak = 0
        return self.streak >= self.k


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
          develop=False, detector_seed=0, max_grows=4):
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

    if develop:
        detector = RSIRecursiveDetector()
    else:
        detector = None

    for ep in range(epochs):
        opt.zero_grad()
        loss = ((model(xt) - yt) ** 2).mean()
        loss.backward()
        opt.step()
        if (ep + 1) % eval_every == 0:
            with torch.no_grad():
                vloss = ((model(xv) - yv) ** 2).mean().item()
            best_val = min(best_val, vloss)
            if develop and grows < max_grows:
                if detector.update(vloss):
                    dev_log.append({"ep": ep + 1, "n_hidden_after": model.n_hidden + 1,
                                    "val": vloss,
                                    "rsi1": detector.rsi_hist[-1] if detector.rsi_hist else None})
                    model.add_layer()
                    grows += 1
                    print(f"    [发育·RSI] ep={ep+1} val={vloss:.5f} "
                          f"RSI1={detector.rsi_hist[-1]:.1f} 动量枯竭, 长层 → "
                          f"{model.n_hidden} 隐藏层", flush=True)
                    opt = torch.optim.Adam(model.parameters(), lr=lr)
                    detector = RSIRecursiveDetector()
    return best_val, dev_log, model.n_hidden


print("=== 发育 spike v7: RSI 递归探测器 ===")
res = {}
for label, dev, hidden in [
    ("对照 2层[8]", False, (8,)),
    ("对照 4层[8,8,8,8]", False, (8, 8, 8, 8)),
    ("发育·RSI递归", True, (8,)),
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
            bv, lg, nh = train(model, x, y, seed=s, develop=True)
        vals.append(bv)
        if dev:
            logs.append(lg); ns.append(nh)
    med = float(np.median(vals))
    res[label] = med
    extra = f"n_hidden={ns}" if dev else ""
    print(f"{label:<16} {med:.6f}   {extra}")
    if dev:
        for i, lg in enumerate(logs):
            line = ", ".join(f"ep{g['ep']}(RSI {g.get('rsi1', float('nan')):.0f})" for g in lg)
            print(f"    seed{i}: {line or '无触发'}")

print("\n=== 判读 ===")
l2, l4, d = res["对照 2层[8]"], res["对照 4层[8,8,8,8]"], res["发育·RSI递归"]
print(f"  2层 {l2:.6f} / 4层 {l4:.6f} / RSI发育 {d:.6f}")
print(f"  RSI发育 vs 2层: {(d - l2) / l2 * 100:+.1f}%")
print(f"  RSI发育 vs 4层: {(d - l4) / l4 * 100:+.1f}%")
if d < l2 and ns and all(nh >= 2 for nh in ns):
    print("  ✓ RSI 递归探测器触发长层且优于浅层 → 方向成立")
else:
    print(f"  ~ 需看日志: 触发={ns}")