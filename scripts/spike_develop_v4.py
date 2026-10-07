#!/usr/bin/env python3
"""发育式持续学习 spike v4 —— 层间交互(跳跃连接)。

愿景:「在层之间进行交互…来回创造出属于自己的结构」。
v3 验证了「自动长层」。v4 加第二块: 每次长层后, 允许在 前层→新输出 之间
建立一条**可学习 gate 的跳跃连接**(gate 初始 0, 由训练决定开不开)。
这模仿皮层可塑性: 同步激活的神经元建立连接(LTP)。

对照:
  A. 固定 2 层(浅层地板)
  B. 固定 3 层(深层地板)
  C. 发育 v3(纯串行长层, 无跳跃) —— 已有基线
  D. 发育 v4(长层 + 跳跃连接) —— 新: 层间交互

任务: y = sin(x1*x2*x3) + cos(x1*x2)  (deep, 需要非线性组合)
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


class Developable(nn.Module):
    """可发育 + 可跳跃的 MLP。

    - blocks: [Linear, Tanh, ..., Linear] (串行主干)
    - skips: 跳跃连接列表, 每项 = (src_block_out_idx, dst_linear_idx, gate)
      gate 是 nn.Parameter(标量), 初始 0 —— 由训练决定是否打开。
    - add_skip(): 从当前最深层(最后一块 Tanh 输出)直连输出 Linear, gate=0
    """

    def __init__(self, in_dim, hidden=(8,), use_skip=False):
        super().__init__()
        self.blocks = nn.ModuleList()
        dims = [in_dim] + list(hidden) + [1]
        for i in range(len(dims) - 1):
            self.blocks.append(nn.Linear(dims[i], dims[i + 1]))
            if i < len(dims) - 2:
                self.blocks.append(nn.Tanh())
        self.hiddens = list(hidden)
        self.use_skip = use_skip
        self.skips = []  # (src_idx, dst_idx, gate_param)

    @property
    def n_hidden(self):
        return len(self.hiddens)

    def _hidden_out_idx(self):
        """当前最后一个隐藏激活(tanh)在 blocks 里的下标。"""
        for i in range(len(self.blocks) - 1, -1, -1):
            if isinstance(self.blocks[i], nn.Tanh):
                return i
        return -1

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
        # 每次长层后: 建立从最深层(新隐藏激活)到输出头的跳跃连接 gate=0
        if self.use_skip:
            src = self._hidden_out_idx()
            dst = len(self.blocks) - 1  # 输出 Linear
            gate = nn.Parameter(torch.zeros(1))
            self.skips.append((src, dst, gate))

    def forward(self, x):
        acts = {}
        h = x
        for i, b in enumerate(self.blocks):
            h = b(h)
            acts[i] = h
        # 跳跃: dst 线性层输出 += gate * src 激活 (先跳过, 后加)
        if self.skips:
            out_idx = len(self.blocks) - 1
            base = acts[out_idx]
            for src, dst, gate in self.skips:
                if dst == out_idx:
                    base = base + gate * acts[src]
            return base
        return h

    def skip_gates(self):
        return [float(g.item()) for _, _, g in self.skips]


def train(model, x, y, epochs=2000, lr=1e-2, seed=0, eval_every=50,
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
                    dev_log.append({"ep": ep + 1, "n_hidden": model.n_hidden,
                                    "val": vloss, "gates": list(model.skip_gates())})
                    print(f"    [发育] ep={ep+1} val={vloss:.5f} 长层 → "
                          f"{model.n_hidden} 隐藏层, skips={len(model.skips)} gates={model.skip_gates()}", flush=True)
                    opt = torch.optim.Adam(model.parameters(), lr=lr)
                    detector = RelDecayDetector()
    return best_val, dev_log, model.n_hidden, model.skip_gates()


print("=== 发育 spike v4: 长层 + 层间跳跃连接 ===")
res = {}
for label, dev, hidden, skip in [
    ("对照 2层[8]", False, (8,), False),
    ("对照 3层[8,4]", False, (8, 4), False),
    ("发育v3(串行)", True, (8,), False),
    ("发育v4(+跳跃)", True, (8,), True),
]:
    vals, logs, ns, gs = [], [], [], []
    for s in range(3):
        if not dev:
            torch.manual_seed(s)
            model = Developable(3, hidden=hidden)
            x, y = make_data()
            bv, lg, nh, sk = train(model, x, y, seed=s, develop=False)
        else:
            torch.manual_seed(s)
            model = Developable(3, hidden=(8,), use_skip=skip)
            x, y = make_data()
            bv, lg, nh, sk = train(model, x, y, seed=s, develop=True)
        vals.append(bv)
        if dev:
            logs.append(lg); ns.append(nh); gs.append(sk)
    med = float(np.median(vals))
    res[label] = med
    extra = f"n_hidden={ns} gates={gs}" if dev else ""
    print(f"{label:<16} {med:.6f}   {extra}")

print("\n=== 判读 ===")
l2, l3 = res["对照 2层[8]"], res["对照 3层[8,4]"]
v3, v4 = res["发育v3(串行)"], res["发育v4(+跳跃)"]
print(f"  2层 {l2:.6f} / 3层 {l3:.6f} / 发育v3 {v3:.6f} / 发育v4 {v4:.6f}")
print(f"  v4 vs v3: {(v4 - v3) / v3 * 100:+.1f}%")
print(f"  v4 vs 固定3层: {(v4 - l3) / l3 * 100:+.1f}%")
if v4 < v3:
    print("  ✓ 跳跃连接带来额外收益 → 「层间交互」机制成立")
else:
    print("  ~ 跳跃连接无额外收益(可能 gate 没打开/任务不需要) —— 记录为诚实结果")