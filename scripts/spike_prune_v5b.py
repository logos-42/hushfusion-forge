#!/usr/bin/env python3
"""剪枝 spike v5b —— 用「恒等初始化层」构造真实冗余。

v5a 发现: 自然训练的过深网络所有层梯度都健康 (0.008~0.020), 没有死层
⟹ 梯度范数判据在自然网络不触发。这是诚实结果: 过度参数化让每层都有活干。

v5b 构造**真实冗余**: 中间插一层用恒等初始化 (权重=I, bias=0, 无 tanh 激活)。
这一层学成「透传」后就是该剪的 —— 它的梯度会持续接近 0 (因为它的贡献
可以被前后层吸收)。

对照:
  A. 固定 2 层 (最优)
  B. 固定 3 层含恒等冗余层 (有死重)
  C. 剪枝版: 从 3 层(含恒等层)起, 自动删掉恒等层 → 应接近 A, 优于 B
"""
import numpy as np
import torch
import torch.nn as nn

torch.manual_seed(0)
np.random.seed(0)


def make_data(n=6000):
    x = np.random.uniform(-1.5, 1.5, size=(n, 3)).astype(np.float32)
    y = np.sin(x[:, 0] * x[:, 1]) + np.cos(x[:, 2])
    return x, y.astype(np.float32).reshape(-1, 1)


class Prunable(nn.Module):
    """可剪枝 MLP。hidden 里可含 identity=True 的恒等层(冗余构造)。"""

    def __init__(self, in_dim, hidden):
        super().__init__()
        self.blocks = nn.ModuleList()
        self.linear_meta = []  # (is_identity, in, out)
        dims = [in_dim] + [h if not isinstance(h, tuple) else h[0] for h in hidden] + [1]
        full_hidden = []
        for h in hidden:
            if isinstance(h, tuple):
                full_hidden.extend([h[0], ("ident", h[0])])
            else:
                full_hidden.append(h)
        prev = in_dim
        n_blocks = 0
        for h in full_hidden:
            if h == ("ident",) or (isinstance(h, tuple) and h[0] == "ident"):
                w = h[1]
                lin = nn.Linear(w, w)
                # 恒等初始化: 权重=I, 无 bias
                nn.init.eye_(lin.weight)
                lin.bias = nn.Parameter(torch.zeros(w))
                # 制作「透传」: 这里用线性层权重固定近似 I, 学起来梯度小
                self.blocks.append(lin)
                self.linear_meta.append(("ident", w, w))
                prev = w
                continue
            w = h
            lin = nn.Linear(prev, w)
            nn.init.xavier_uniform_(lin.weight)
            self.blocks.append(lin)
            self.linear_meta.append(("normal", prev, w))
            self.blocks.append(nn.Tanh())
            prev = w
            n_blocks += 1
        # 输出层
        out = nn.Linear(prev, 1)
        nn.init.xavier_uniform_(out.weight)
        self.blocks.append(out)
        self.linear_meta.append(("normal", prev, 1))
        self._n_hidden = n_blocks

    @property
    def n_hidden(self):
        return self._n_hidden

    def linear_indices(self):
        idx = 0
        res = []
        for i, b in enumerate(self.blocks):
            if isinstance(b, nn.Linear):
                res.append(i)
        return res

    def prune_ident(self):
        """删掉恒等层(透传), 前后直连。返回是否删了。"""
        for i, b in enumerate(self.blocks):
            if isinstance(b, nn.Linear) and i < len(self.linear_meta) and self.linear_meta[i][0] == "ident":
                del self.blocks[i]
                del self.linear_meta[i]
                self._n_hidden -= 0  # 恒等层不算隐藏层
                return True
        return False

    def forward(self, x):
        h = x
        for b in self.blocks:
            h = b(h)
        return h


def train(model, x, y, epochs=1000, lr=1e-2, seed=0, eval_every=50,
          prune=False, max_prunes=1, grad_ratio=0.05, k=4):
    torch.manual_seed(seed)
    n = len(x)
    idx = np.random.RandomState(seed).permutation(n)
    tr, va = idx[:int(0.8 * n)], idx[int(0.8 * n):]
    xt, yt = torch.tensor(x[tr]), torch.tensor(y[tr])
    xv, yv = torch.tensor(x[va]), torch.tensor(y[va])
    opt = torch.optim.Adam(model.parameters(), lr=lr)
    best_val = float('inf')
    prunes = 0
    weak_streak = 0
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
            if prune and prunes < max_prunes:
                # 检查恒等层梯度
                ident_grads = []
                global_grads = []
                for i, b in enumerate(model.blocks):
                    if isinstance(b, nn.Linear) and b.weight.grad is not None:
                        gn = b.weight.grad.norm().item()
                        global_grads.append(gn)
                        if i < len(model.linear_meta) and model.linear_meta[i][0] == "ident":
                            ident_grads.append(gn)
                gm = float(np.mean(global_grads)) if global_grads else 0.0
                if ident_grads and gm > 0:
                    ident_weak = all(g < gm * grad_ratio for g in ident_grads)
                else:
                    ident_weak = False
                if ident_weak:
                    weak_streak += 1
                else:
                    weak_streak = 0
                if weak_streak >= k:
                    ok = model.prune_ident()
                    prunes += 1
                    dev_log.append({"ep": ep + 1, "val": vloss, "pruned": ok,
                                    "ident_grads": [f"{g:.6f}" for g in ident_grads],
                                    "global_mean": f"{gm:.6f}"})
                    print(f"    [剪枝] ep={ep+1} val={vloss:.5f} 恒等层弱, 删除 → "
                          f"成功={ok}", flush=True)
                    weak_streak = 0
                    opt = torch.optim.Adam(model.parameters(), lr=lr)
    return best_val, dev_log, prunes


print("=== 剪枝 spike v5b: 恒等冗余层自动删除 ===")
res = {}
for label, dev, hidden in [
    ("对照 2层[8]", False, [8]),
    ("对照 3层[8,(ident,8)]", False, [8, (8,)]),
    ("剪枝(3层,含恒等)", True, [8, (8,)]),
]:
    vals, logs, prs = [], [], []
    for s in range(3):
        if not dev:
            torch.manual_seed(s)
            model = Prunable(3, hidden=hidden)
            x, y = make_data()
            bv, lg, prs_ = train(model, x, y, seed=s, prune=False)
        else:
            torch.manual_seed(s)
            model = Prunable(3, hidden=[8, (8,)])
            x, y = make_data()
            bv, lg, prs_ = train(model, x, y, seed=s, prune=True)
        vals.append(bv)
        if dev:
            logs.append(lg); prs.append(prs_)
    med = float(np.median(vals))
    res[label] = med
    extra = f"prunes={prs}" if dev else ""
    print(f"{label:<24} {med:.6f}   {extra}")
    if dev:
        for lg in logs:
            for item in lg:
                print(f"    [log] ep={item['ep']} val={item['val']:.5f} pruned={item['pruned']} "
                      f"ident_grads={item['ident_grads']} gm={item['global_mean']}")

print("\n=== 判读 ===")
l2, l3, pr = res["对照 2层[8]"], res["对照 3层[8,(ident,8)]"], res["剪枝(3层,含恒等)"]
print(f"  2层 {l2:.6f} / 3层含冗余 {l3:.6f} / 剪枝 {pr:.6f}")
print(f"  剪枝 vs 含冗余3层: {(pr - l3) / l3 * 100:+.1f}%")
if (pr <= l3 * 1.05) and sum(prs) > 0:
    print("  ✓ 剪枝删掉冗余且不退化")
elif sum(prs) == 0:
    print("  ~ 剪枝未触发 —— 恒等层的梯度没弱到阈值")
else:
    print(f"  ~ 剪枝退化 (prunes={prs})")