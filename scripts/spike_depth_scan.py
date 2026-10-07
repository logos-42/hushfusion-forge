#!/usr/bin/env python3
"""深度探索 spike —— 在真实 v6 经验池上扫 learner 深度极限。

数据: 远端 v6 ckpt 的 151 个 (配置→score) 经验, 11 维特征。
问题: 这个配置→score 映射需要多深的网络才到极限?
      深度 {1,2,3,4} 隐藏层 × 宽度 {32} → 哪个 val-loss 最低?
      (给将来的发育机制一个「深度极限」锚点: 到几层不再改善 = 该停)

方法: 5-fold 隔离, 每 fold 用 80% 训练(普通 Adam, 非 OML——先测容量上限),
      20% 验证。报告 mean±std val-MSE。
"""
import pickle
import sys
sys.path.insert(0, "/Users/apple/Downloads/headless")
sys.path.insert(0, "/Users/apple/Downloads/Hushfusion_Forge/scripts")

import numpy as np
import torch
import torch.nn as nn

torch.manual_seed(0)
np.random.seed(0)

# 加载真实经验池
d = pickle.load(open("/Users/apple/.hermes/cache/scratch/v6_ckpt.pkl", "rb"))
pool_X, pool_Y, _ = d
X = np.array(pool_X, dtype=float)
Y = np.array(pool_Y, dtype=float).reshape(-1, 1)
print(f"经验池: {len(X)} 条, 特征 {X.shape[1]} 维, score [{Y.min():.3f}, {Y.max():.3f}]")

# 标准化特征(深度探索用, 不改原特征语义)
Xm, Xs = X.mean(0), X.std(0) + 1e-9
Xn = ((X - Xm) / Xs).astype(np.float32)
Y = Y.astype(np.float32)


def make_mlp(in_dim, hidden_layers, width=32, seed=0):
    torch.manual_seed(seed)
    layers = []
    dims = [in_dim] + [width] * hidden_layers + [1]
    for i in range(len(dims) - 1):
        layers.append(nn.Linear(dims[i], dims[i + 1]))
        if i < len(dims) - 2:
            layers.append(nn.Tanh())
    return nn.Sequential(*layers)


def train_eval(hidden_layers, seed=0, epochs=800, lr=1e-3):
    """5-fold 隔离, 返回 mean±std val-MSE。"""
    n = len(Xn)
    rng = np.random.RandomState(seed)
    idx = rng.permutation(n)
    folds = np.array_split(idx, 5)
    losses = []
    for fi in range(5):
        va = folds[fi]
        tr = np.concatenate([folds[j] for j in range(5) if j != fi])
        model = make_mlp(Xn.shape[1], hidden_layers, seed=seed + fi)
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


print("\n=== 深度极限扫描 (真实 v6 配置→score 数据, 5-fold, width=32) ===")
print(f"{'隐藏层数':<8} {'val-MSE mean±std':<20} {'每fold'}")
results = {}
for hl in [1, 2, 3, 4]:
    m, s, ls = train_eval(hl)
    results[hl] = (m, s)
    print(f"  {hl:<8} {m:.6f}±{s:.6f}   {[f'{l:.5f}' for l in ls]}")

# 深度极限判据: 连续加深不再改善(改善 < 5% 或方差覆盖)
print("\n=== 深度极限判读 ===")
depths = sorted(results)
for i in range(1, len(depths)):
    prev_m = results[depths[i-1]][0]
    cur_m = results[depths[i]][0]
    imp = (prev_m - cur_m) / prev_m * 100 if prev_m > 0 else 0
    print(f"  {depths[i-1]}层 → {depths[i]}层: 改善 {imp:+.1f}%")
best_d = min(results, key=lambda d: results[d][0])
print(f"\n最佳深度: {best_d} 隐藏层 (val-MSE {results[best_d][0]:.6f})")
print("→ 深度极限 = 该层数; 再加深无改善(发育机制将来在此停/长)")