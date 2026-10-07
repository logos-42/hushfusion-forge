#!/usr/bin/env python3
"""发育核心 spike v3 —— 相对衰减探测器驱动的自动长层。

v2 坏判据(绝对平台)在浅层永不触发。
v3 用相对衰减(当前改善率 / 早期改善率 < 0.2 持续 3 周期)判定「结构榨干了」。
"""
import numpy as np
import torch
import torch.nn as nn

torch.manual_seed(0)
np.random.seed(0)


class RelDecayDetector:
    def __init__(self, thresh=0.2, k=3, alpha=0.5, warmup=5):
        self.thresh, self.k, self.alpha, self.warmup = thresh, k, alpha, warmup
        self.prev = None; self.ewma = 0.0; self.early = []
        self.early_avg = None; self.streak = 0; self.n = 0

    def update(self, val):
        self.n += 1
        if self.prev is None:
            self.prev = val; return False
        imp = (self.prev - val) / abs(self.prev) if self.prev != 0 else 0.0
        if self.n <= self.warmup:
            self.early.append(imp)
            if self.n == self.warmup:
                self.early_avg = max(float(np.mean(self.early)), 1e-9)
            self.ewma = self.alpha * imp + (1 - self.alpha) * self.ewma
            self.prev = val; return False
        self.ewma = self.alpha * imp + (1 - self.alpha) * self.ewma
        ratio = self.ewma / self.early_avg
        self.streak = self.streak + 1 if ratio < self.thresh else 0
        self.prev = val
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


def train(model, x, y, epochs=1500, lr=1e-2, seed=0, eval_every=50,
          develop=False, max_grows=2):
    torch.manual_seed(seed)
    n = len(x)
    idx = np.random.RandomState(seed).permutation(n)
    tr, va = idx[:int(0.8 * n)], idx[int(0.8 * n):]
    xt, yt = torch.tensor(x[tr]), torch.tensor(y[tr])
    xv, yv = torch.tensor(x[va]), torch.tensor(y[va])
    opt = torch.optim.Adam(model.parameters(), lr=lr)
    best_val = float('inf')
    detector = RelDecayDetector()
    grows = 0
    dev_log = []

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
                    model.add_layer()
                    grows += 1
                    dev_log.append({"ep": ep + 1, "n_hidden": model.n_hidden(), "val": vloss})
                    print(f"    [发育] ep={ep+1} val={vloss:.5f} 相对衰减, 长层 → "
                          f"{model.n_hidden()} 隐藏层", flush=True)
                    opt = torch.optim.Adam(model.parameters(), lr=lr)
                    detector = RelDecayDetector()  # 重置(新结构新基线)
    return best_val, dev_log, model.n_hidden()


print("=== 发育核心 spike v3 (相对衰减驱动) ===")
res = {}
for label, dev, hidden in [
    ("对照 2层[8]", False, (8,)),
    ("对照 3层[8,4]", False, (8, 4)),
    ("发育(2层起)", True, (8,)),
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
    extra = f"n_hidden={ns} grows={[len(l) for l in logs]}" if dev else ""
    print(f"{label:<16} {med:.6f}   {extra}")

print("\n=== 判读 ===")
l2, l3, dev_r = res["对照 2层[8]"], res["对照 3层[8,4]"], res["发育(2层起)"]
print(f"  2层: {l2:.6f}  3层: {l3:.6f}  发育: {dev_r:.6f}")
print(f"  发育 vs 固定3层: 差 {(dev_r - l3) / l3 * 100:+.1f}%")
print(f"  发育 vs 固定2层: 改善 {(l2 - dev_r) / l2 * 100:+.1f}%")
if dev_r <= l3 * 1.15:
    print("  ✓ 发育版达到固定3层水平 → 「相对衰减→长层」机制成立")
else:
    print("  ✗ 发育版未达3层水平")