#!/usr/bin/env python3
"""发育 spike v6 —— 收敛判据: 自动停止长层 (不预设 max_grows)。

前序: v3/v4 用 max_grows=2 硬限制长层次数。这一版让模型**自己决定**长到几层停:
  - 长层后, 重置相对衰减探测器(新结构新基线)
  - 若新结构也进入相对衰减(收敛), 再长一层
  - 若长层后 val 不再改善(新层没带来收益) ⟹ **停止发育**, 冻结结构
    (计划 §3: 连续多周期无改善 = 当前结构已适合任务, 不再扩)

对照:
  A. 固定 2 层 (浅层上限)
  B. 固定 4 层 (深层上限)
  C. 发育+收敛判据: 从 2 层起, 自己决定长到几层, 停止后冻结
任务: deep (嵌套乘积, 需要 ≥2 隐藏层才有好表现)

验收:
  - 发育版应长到 2~3 层就停 (不是无限长)
  - 发育版 val 应接近或优于固定 2/4 层
  - 停止判据必须真的触发 (记录花了多少轮确认停止)
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


def train(model, x, y, epochs=3000, lr=1e-2, seed=0, eval_every=50,
          develop=False, stop_patience=8, grow_cooldown=20):
    """训练 + 发育 + 收敛自动停止。

    停止判据: 某次长层后, **冷却期 grow_cooldown 个周期内不看停止**(让新层充分
    学习), 之后若 val 持续 stop_patience 个周期没低于长层前最佳 ⟹ 这次长层
    没带来收益, 停止发育。
    v6a 教训: 冷却期太短(3)会让「新层还没学完的暂时持平」被误判为「长层无收益」,
    seed1/2 在 2 层就停, 远差于对照。冷却期让新层有机会证明自己。
    """
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
    post_grow_best = None   # 长层后的最佳 val
    post_grow_count = 0     # 长层后经过的周期
    cooldown_left = 0       # 冷却剩余周期(长层后重置)
    stop_reason = "epochs_exhausted"

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
                # 长层后跟踪: 新结构是否带来收益
                if post_grow_best is not None:
                    post_grow_best = min(post_grow_best, vloss)
                    if cooldown_left > 0:
                        cooldown_left -= 1
                    else:
                        post_grow_count += 1
                        if post_grow_count >= stop_patience and post_grow_best >= dev_log[-1]["val_before"]:
                            stop_reason = f"no_gain_after_grow_{grows}"
                            print(f"    [停止] ep={ep+1} 冷却后 {stop_patience} 周期 val 未低于"
                                  f"长层前 {dev_log[-1]['val_before']:.5f} (现 {post_grow_best:.5f}), 冻结结构 "
                                  f"({model.n_hidden} 隐藏层)", flush=True)
                            break

                # 相对衰减 → 长层
                if detector.update(vloss):
                    dev_log.append({"grow": grows + 1, "ep": ep + 1,
                                    "n_hidden_after": model.n_hidden + 1,
                                    "val_before": vloss})
                    model.add_layer()
                    grows += 1
                    print(f"    [发育] ep={ep+1} val={vloss:.5f} 相对衰减, 长层 → "
                          f"{model.n_hidden} 隐藏层", flush=True)
                    opt = torch.optim.Adam(model.parameters(), lr=lr)
                    detector = RelDecayDetector()
                    post_grow_best = vloss
                    post_grow_count = 0
                    cooldown_left = grow_cooldown
    return best_val, dev_log, model.n_hidden, stop_reason


print("=== 发育 spike v6: 收敛判据自动停止长层 ===")
res = {}
for label, dev, hidden in [
    ("对照 2层[8]", False, (8,)),
    ("对照 4层[8,8,8,8]", False, (8, 8, 8, 8)),
    ("发育+收敛(2层起)", True, (8,)),
]:
    vals, logs, ns, srs = [], [], [], []
    for s in range(3):
        if not dev:
            torch.manual_seed(s)
            model = DevelopmentalMLP(3, hidden=hidden)
            x, y = make_data()
            bv, lg, nh, sr = train(model, x, y, seed=s, develop=False)
        else:
            torch.manual_seed(s)
            model = DevelopmentalMLP(3, hidden=(8,))
            x, y = make_data()
            bv, lg, nh, sr = train(model, x, y, seed=s, develop=True)
        vals.append(bv)
        if dev:
            logs.append(lg); ns.append(nh); srs.append(sr)
    med = float(np.median(vals))
    res[label] = med
    extra = f"n_hidden终={ns} stop={srs}" if dev else ""
    print(f"{label:<20} {med:.6f}   {extra}")
    if dev:
        for i, lg in enumerate(logs):
            grows_info = ", ".join(f"ep{g['ep']}→{g['n_hidden_after']}层(val {g['val_before']:.4f})" for g in lg)
            print(f"    seed{i}: [{grows_info}]")

print("\n=== 判读 ===")
l2, l4, devr = res["对照 2层[8]"], res["对照 4层[8,8,8,8]"], res["发育+收敛(2层起)"]
print(f"  2层 {l2:.6f} / 4层 {l4:.6f} / 发育+收敛 {devr:.6f}")
print(f"  发育 vs 2层: {(devr - l2) / l2 * 100:+.1f}%")
print(f"  发育 vs 4层: {(devr - l4) / l4 * 100:+.1f}%")
if ns and all(2 <= n <= 3 for n in ns):
    print(f"  ✓ 自动收敛到 {ns} 隐藏层 (2~3, 不是无限长) —— 收敛判据工作")
else:
    print(f"  ~ 收敛层数异常: {ns}")
if any("no_gain" in s for s in srs):
    print("  ✓ 停止判据真的触发(no_gain_after_grow) —— 模型自己决定「够了」")
else:
    print(f"  ~ 停止判据未按预期触发: {srs}")