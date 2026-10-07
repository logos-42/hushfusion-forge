#!/usr/bin/env python3
"""宽度探索 spike —— 在真实 v6 经验池上扫宽度极限。

深度已钉死 1~2 层(深度扫描结论)。现在问: 宽度多少才到容量饱和?
隐藏层 = 2 (深度极限), 宽度 {16, 32, 64, 128, 256}。
5-fold 隔离 + 多 seed。给发育机制的宽度锚点。

数据: 远端 v6 ckpt 的 151 个 (配置→score) 经验, 11 维特征。
"""
import pickle
import sys
sys.path.insert(0, "/Users/apple/Downloads/headless")

import numpy as np
import torch
import torch.nn as nn

torch.manual_seed(0)
np.random.seed(0)

d = pickle.load(open("/Users/apple/.hermes/cache/scratch/v6_ckpt.pkl", "rb"))
pool_X, pool_Y, _ = d
X = np.array(pool_X, dtype=float)
Y = np.array(pool_Y, dtype=float).reshape(-1, 1)
print(f"经验池: {len(X)} 条, 特征 {X.shape[1]} 维, score [{Y.min():.3f}, {Y.max():.3f}]")

Xm, Xs = X.mean(0), X.std(0) + 1e-9
Xn = ((X - Xm) / Xs).astype(np.float32)
Y = Y.astype(np.float32)


def make_mlp(in_dim, width, hidden_layers=2, seed=0):
    torch.manual_seed(seed)
    layers = []
    dims = [in_dim] + [width] * hidden_layers + [1]
    for i in range(len(dims) - 1):
        layers.append(nn.Linear(dims[i], dims[i + 1]))
        if i < len(dims) - 2:
            layers.append(nn.Tanh())
    return nn.Sequential(*layers)


def train_eval(width, seed=0, epochs=800, lr=1e-3):
    n = len(Xn)
    rng = np.random.RandomState(seed)
    idx = rng.permutation(n)
    folds = np.array_split(idx, 5)
    losses = []
    for fi in range(5):
        va = folds[fi]
        tr = np.concatenate([folds[j] for j in range(5) if j != fi])
        model = make_mlp(Xn.shape[1], width, seed=seed + fi)
        opt = torch.optim.Adam(model.parameters(), lr=lr)
        xt, yt = torch.tensor(Xn[tr]), torch.tensor(Y[tr])
        xv, yv = torch.tensor(Xn[va]), torch.tensor(Y[va])
        best = float("inf")
        for ep in range(epochs):
            opt.zero_grad()
            loss = ((model(xt) - yt) ** 2).mean()
            loss.backward()
            opt.step()
            if (ep + 1) % 100 == 0:
                with torch.no_grad():
                    best = min(best, ((model(xv) - yv) ** 2).mean().item())
        losses.append(best)
    return float(np.mean(losses)), float(np.std(losses)), losses


print("\n=== 宽度极限扫描 (2 隐藏层, 5-fold, 3 seeds 取中位) ===")
print(f"{'宽度':<8} {'val-MSE mean±std':<20} {'3 seed 中位'}")
results = {}
for w in [16, 32, 64, 128, 256]:
    all_m = []
    for s in range(3):
        m, std, ls = train_eval(w, seed=s)
        all_m.append(m)
    med = float(np.median(all_m))
    results[w] = med
    print(f"  {w:<8} {np.mean(all_m):.6f}±{np.std(all_m):.6f}   {med:.6f}")

print("\n=== 宽度极限判读 ===")
ws = sorted(results)
for i in range(1, len(ws)):
    prev, cur = results[ws[i-1]], results[ws[i]]
    imp = (prev - cur) / prev * 100 if prev > 0 else 0
    print(f"  {ws[i-1]} → {ws[i]}: 改善 {imp:+.1f}%")
best_w = min(results, key=lambda w: results[w])
print(f"\n最佳宽度: {best_w} (val-MSE {results[best_w]:.6f})")
print(f"→ 宽度极限 ≈ {best_w}; 更宽无改善 = 容量饱和点(发育机制宽度锚点)")