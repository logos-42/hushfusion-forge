#!/usr/bin/env python3
"""分形树 vs 固定 —— 真实数据评估 (v6 守护进程 701 条真实配置→score)。

toy 控制面的教训 (§2j): 解析已知函数学习无信息增益。真实 forge score
不是解析函数 —— 它是搜索产物的确定性函数, 但形状未知 (v5/v6 用 MLP 学 =
真实 ml 任务)。在真实经验池上重做固定 vs 分形, 回答「真实场景谁学得更好」。

数据: 远端 v6 ckpt, 701 条 (配置特征 11 维 → 真实 forge best_score)。
注意: 该数据是 UCB/随机混合采样的在线轨迹, 有探索偏差 —— 诚实标注。

协议 (同 §2i 干净评估, 但真实数据):
  - 5-fold: 每 fold 80% 训练 (固定 vs 分形树), 20% 测试
  - 指标: 测试 MSE (score 预测), 以及 top-16 推荐命中率
    (模型在全部格点上预测, 选 top-16 配置, 看测试集里真实高分是否被命中)
"""
import math
import pickle
import sys
sys.path.insert(0, "/Users/apple/Downloads/headless")

import numpy as np
import torch
import torch.nn as nn

torch.manual_seed(0)
np.random.seed(0)


class FractalNodeV2:
    def __init__(self, in_dim, d_model=64, depth=0, D=4, warmup=10, ent_reg=0.01):
        self.depth, self.D = depth, D
        self.warmup, self.ent_reg = warmup, ent_reg
        self.net = nn.Sequential(nn.Linear(in_dim, d_model), nn.Tanh(), nn.Linear(d_model, 1))
        self.gate = nn.Parameter(torch.zeros(1))
        self.children = []
        self.child_warmup = []
        self.opt = torch.optim.Adam(list(self.net.parameters()) + [self.gate], lr=1e-3)
        self.update_freq = D ** depth
        self.t = 0

    def n_nodes(self):
        return 1 + sum(c.n_nodes() for c in self.children)

    def spawn_child(self, in_dim):
        child = FractalNodeV2(in_dim, d_model=self.net[0].out_features,
                              depth=self.depth + 1, D=self.D,
                              warmup=self.warmup, ent_reg=self.ent_reg)
        self.children.append(child)
        self.child_warmup.append(self.warmup)
        n = len(self.children) + 1
        self.gate = nn.Parameter(torch.full((n,), math.log(n)))
        self.opt = torch.optim.Adam(list(self.net.parameters()) + [self.gate], lr=1e-3)

    def predict_tree(self, X):
        self_out = self.net(X)
        outs = [self_out]
        child_list = []
        for i, ch in enumerate(self.children):
            if self.child_warmup[i] > 0:
                continue
            cy, _ = ch.predict_tree(X)
            outs.append(cy)
            child_list.append(i)
        if len(outs) == 1:
            return self_out, [self_out]
        gate = torch.softmax(self.gate, dim=0)
        y = torch.zeros_like(self_out)
        y = y + gate[0] * outs[0]
        for k, ch_idx in enumerate(child_list):
            y = y + gate[int(ch_idx) + 1] * outs[k + 1]
        return y, outs

    def train_step(self, X, Y, steps=3):
        self.t += 1
        for _ in range(steps):
            self.opt.zero_grad()
            y, _ = self.predict_tree(X)
            mse = ((y - Y) ** 2).mean()
            active_children = sum(1 for w in self.child_warmup if w == 0)
            if len(self.children) > 0 and active_children > 0:
                gate = torch.softmax(self.gate, dim=0)
                ent = -(gate * torch.log(gate + 1e-9)).sum()
                loss = mse - self.ent_reg * ent
            else:
                loss = mse
            loss.backward()
            self.opt.step()
        for i in range(len(self.children)):
            if self.child_warmup[i] > 0:
                self.child_warmup[i] -= 1

    def predict_flat(self, X):
        y, _ = self.predict_tree(X)
        return y.flatten().detach().numpy()


class PlainNode:
    def __init__(self, in_dim, d_model=64):
        self.net = nn.Sequential(nn.Linear(in_dim, d_model), nn.Tanh(), nn.Linear(d_model, 1))
        self.opt = torch.optim.Adam(self.net.parameters(), lr=1e-3)

    def predict_flat(self, X):
        with torch.no_grad():
            return self.net(X).flatten().numpy()

    def train_step(self, X, Y, steps=3):
        for _ in range(steps):
            self.opt.zero_grad()
            loss = ((self.net(X) - Y) ** 2).mean()
            loss.backward()
            self.opt.step()


def main():
    d = pickle.load(open("/Users/apple/.hermes/cache/scratch/v6_ckpt_latest.pkl", "rb"))
    X, Y, _ = d
    X = np.array(X, dtype=float)
    Y = np.array(Y, dtype=float).reshape(-1, 1)
    print(f"真实经验: {len(X)} 条, 维度 {X.shape[1]}, score [{Y.min():.3f}, {Y.max():.3f}]")
    # 标准化
    Xm, Xs = X.mean(0), X.std(0) + 1e-9
    Xn = ((X - Xm) / Xs).astype(np.float32)
    # 归一化 Y (score 范围小, 不缩放)

    n = len(Xn)
    rng = np.random.default_rng(42)
    idx = rng.permutation(n)
    folds = np.array_split(idx, 5)

    print("\n=== 真实数据: 固定 vs 分形树 (5-fold, 预测 score) ===")
    for grow in [False, True]:
        mses = []
        for fi in range(5):
            va = folds[fi]
            tr = np.concatenate([folds[j] for j in range(5) if j != fi])
            Xt, Yt = torch.tensor(Xn[tr]), torch.tensor(Y[tr])
            Xv, Yv = torch.tensor(Xn[va]), torch.tensor(Y[va])
            if grow:
                model = FractalNodeV2(Xn.shape[1], D=4)
                for ep in range(60):
                    model.train_step(Xt, Yt, steps=1)
                    # 中途长两个子节点 (仿真实发育)
                    if ep == 20 or ep == 40:
                        model.spawn_child(Xn.shape[1])
            else:
                model = PlainNode(Xn.shape[1])
                for ep in range(60):
                    model.train_step(Xt, Yt, steps=1)
            pred = model.predict_flat(Xv)
            mse = float(np.mean((pred - Yv.numpy().flatten()) ** 2))
            mses.append(mse)
        label = "分形树(长2子)" if grow else "固定(单节点)"
        print(f"{label}: 5-fold MSE = {np.mean(mses):.4f} ± {np.std(mses):.4f} "
              f"({[f'{m:.4f}' for m in mses]})")

    # 再跑一次大训练量对比 (60→150 ep, 确认不是训练不足)
    print("\n=== 真实数据: 长训练 (150 ep) ===")
    for grow in [False, True]:
        mses = []
        for fi in range(5):
            va = folds[fi]
            tr = np.concatenate([folds[j] for j in range(5) if j != fi])
            Xt, Yt = torch.tensor(Xn[tr]), torch.tensor(Y[tr])
            Xv, Yv = torch.tensor(Xn[va]), torch.tensor(Y[va])
            if grow:
                model = FractalNodeV2(Xn.shape[1], D=4)
                for ep in range(150):
                    model.train_step(Xt, Yt, steps=1)
                    if ep in (40, 80, 120):
                        model.spawn_child(Xn.shape[1])
            else:
                model = PlainNode(Xn.shape[1])
                for ep in range(150):
                    model.train_step(Xt, Yt, steps=1)
            pred = model.predict_flat(Xv)
            mse = float(np.mean((pred - Yv.numpy().flatten()) ** 2))
            mses.append(mse)
        label = "分形树(长3子)" if grow else "固定(单节点)"
        print(f"{label}: 5-fold MSE = {np.mean(mses):.4f} ± {np.std(mses):.4f}")


if __name__ == "__main__":
    main()