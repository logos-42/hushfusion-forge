#!/usr/bin/env python3
"""分形树 vs 固定 — 干净评估 (排除探索运气, 同一经验序列比预测精度)。

§2h 诊断: 在线 UCB 步数被早期探索运气支配 (argmin 锁死误判坏点),
结构差异淹没在 ±50 步方差里。修法: **同一经验序列** 训练两者,
预热后**离线评估预测精度** (模型没见过但真实的 (η,λ,μ0)→步数 映射) ——
不给任何在线探索机会, 直接比「给定相同经验, 谁学得更准」。

协议:
  1. 离线生成经验池: 从 μ0 漂移环境采样 600 条 (η,λ,μ0)→log步数(+越界)
  2. 前 400 条训练 (固定1节点 vs 分形树), 每 50 条前向评估一次
  3. 后 200 条 = 测试集 (两种结构都没见过)
  4. 评估: 预测 log步数 vs 真值的 MAE + 推荐最优控制点的命中率
     (预测最优 (η,λ) 的步数 vs 该 μ0 的真最优步数, 比值越小越好)
"""
import math
import sys
sys.path.insert(0, "/Users/apple/Downloads/headless")

import numpy as np
import torch
import torch.nn as nn

torch.manual_seed(0)
np.random.seed(0)

ME_OVER_MI = 1.0 / (2.5 * 1822.888486209)
MU_CEIL = 1.0 - ME_OVER_MI
MU_WORK = 0.999
LOG_PENALTY = math.log(5000.0)


def eta_sink(lam):
    return 2.0 * lam - lam * lam


def run_control(mu0, eta_ext, lam, max_steps=5000):
    mu = mu0
    for n in range(1, max_steps + 1):
        eta = eta_ext + eta_sink(lam) * (1.0 - mu)
        mu = mu + eta * (1.0 - mu)
        if mu >= MU_CEIL:
            return n, True
        if mu >= MU_WORK:
            return n, False
    return max_steps, False


def ctrl_feature(eta_ext, lam, mu0):
    return [eta_ext / 0.5, lam / 0.9, mu0 / 0.8]


ETA_GRID = np.linspace(0.02, 0.5, 20)
LAM_GRID = np.linspace(0.0, 0.9, 10)


class FractalNodeV2:
    def __init__(self, in_dim, d_model=32, depth=0, D=4, warmup=10, ent_reg=0.01):
        self.depth, self.D = depth, D
        self.warmup, self.ent_reg = warmup, ent_reg
        self.net = nn.Sequential(nn.Linear(in_dim, d_model), nn.Tanh(), nn.Linear(d_model, 1))
        self.gate = nn.Parameter(torch.zeros(1))
        self.children = []
        self.child_warmup = []
        self.opt = torch.optim.Adam(list(self.net.parameters()) + [self.gate], lr=1e-3)
        self.update_freq = D ** depth
        self.t = 0
        self.clean_losses = []

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

    def train_step(self, X, Y):
        """纯监督训练一步 (离线评估用, 无生长判据)。"""
        self.t += 1
        opt = self.opt
        opt.zero_grad()
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
        opt.step()
        # 递减子节点热身
        for i in range(len(self.children)):
            if self.child_warmup[i] > 0:
                self.child_warmup[i] -= 1
        return float(mse.detach())

    def predict_flat(self, X):
        y, _ = self.predict_tree(X)
        return y.flatten().detach().numpy()


class PlainNode:
    def __init__(self, in_dim, d_model=32):
        self.net = nn.Sequential(nn.Linear(in_dim, d_model), nn.Tanh(), nn.Linear(d_model, 1))
        self.opt = torch.optim.Adam(self.net.parameters(), lr=1e-3)

    def predict_flat(self, X):
        with torch.no_grad():
            return self.net(X).flatten().numpy()

    def train_step(self, X, Y):
        self.opt.zero_grad()
        loss = ((self.net(X) - Y) ** 2).mean()
        loss.backward()
        self.opt.step()
        return float(loss.detach())


def main():
    rng = np.random.default_rng(7)
    # 离线经验池: 600 条, μ0 漂移
    Xs, Ys = [], []
    for _ in range(600):
        mu0 = float(rng.uniform(0.01, 0.8))
        e = float(rng.choice(ETA_GRID))
        l = float(rng.choice(LAM_GRID))
        n, over = run_control(mu0, e, l)
        y = math.log(n) if not over else LOG_PENALTY
        Xs.append(ctrl_feature(e, l, mu0))
        Ys.append(y)
    Xs = np.array(Xs, dtype=float)
    Ys = np.array(Ys, dtype=float)
    Xt = torch.tensor(Xs, dtype=torch.float32)
    Yt = torch.tensor(Ys, dtype=torch.float32).reshape(-1, 1)

    # 测试集: 全新 μ0 采样 (两种结构都没见过)
    Xte, Yte = [], []
    for _ in range(200):
        mu0 = float(rng.uniform(0.01, 0.8))
        e = float(rng.choice(ETA_GRID))
        l = float(rng.choice(LAM_GRID))
        n, over = run_control(mu0, e, l)
        y = math.log(n) if not over else LOG_PENALTY
        Xte.append(ctrl_feature(e, l, mu0))
        Yte.append(y)
    Xte = np.array(Xte, dtype=float)
    Yte = np.array(Yte, dtype=float)

    print("=== 干净评估: 同一经验序列, 预测 log步数 MAE + 最优控制点命中 ===")
    results = {}
    # 分形树: 400 训练样本中途长 2 个子节点 (模拟生长)
    for label, make, grow_at in [
        ("固定1节点", lambda: PlainNode(3), None),
        ("分形树(400后长2子)", lambda: FractalNodeV2(3, D=4), [200, 300]),
    ]:
        model = make()
        train_n = 400
        for i in range(0, train_n, 50):
            Xb = Xt[i:i+50]
            Yb = Yt[i:i+50]
            if len(Xb) < 10:
                continue
            for _ in range(5):  # 每批 5 步
                model.train_step(Xb, Yb)
            if grow_at and (i + 50) in grow_at and isinstance(model, FractalNodeV2):
                model.spawn_child(3)
                model.spawn_child(3)
                print(f"  [生长] 样本 {i+50}: 树长到 {model.n_nodes()} 节点", flush=True)
        # 测试集评估
        pred = model.predict_flat(torch.tensor(Xte, dtype=torch.float32))
        mae = float(np.mean(np.abs(pred - Yte)))
        # 最优控制点命中: 对每个测试 μ0, 用模型在格点上预测, 选最优, 比真最优
        # (只评估安全区, 越界预测 = 罚)
        hit_ratios = []
        for mu0_test in [0.1, 0.3, 0.5, 0.7]:
            cands = [(e, l) for e in ETA_GRID for l in LAM_GRID]
            feats = np.array([ctrl_feature(e, l, mu0_test) for e, l in cands])
            p = model.predict_flat(torch.tensor(feats, dtype=torch.float32))
            best_idx = int(np.argmin(p))
            be, bl = cands[best_idx]
            n_rec, o_rec = run_control(mu0_test, be, bl)
            # 真最优
            n_true = None
            for e in ETA_GRID:
                for l in LAM_GRID:
                    n_, o_ = run_control(mu0_test, e, l)
                    if not o_ and (n_true is None or n_ < n_true):
                        n_true = n_
            if n_rec and n_true:
                hit_ratios.append(n_rec / n_true)
        results[label] = (mae, hit_ratios)
        print(f"{label}: 测试 MAE={mae:.3f} (log步数) | 最优点命中比 {[f'{r:.1f}×' for r in hit_ratios]} "
              f"(1.0=完美)")

    m1, h1 = results["固定1节点"]
    m2, h2 = results["分形树(400后长2子)"]
    print(f"\n=== 判读 ===")
    print(f"MAE: 固定 {m1:.3f} vs 分形 {m2:.3f} ({'分形更好' if m2 < m1 else '固定更好'})")
    print(f"命中比均值: 固定 {np.mean(h1):.2f}× vs 分形 {np.mean(h2):.2f}×")
    if m2 < m1 * 0.95:
        print("✓ 分形树学习更快/更准 (干净评估下)")
    elif m2 < m1 * 1.05:
        print("~ 相当 (差异在噪声)")
    else:
        print("✗ 分形不如固定 (干净评估下) —— 生长没有帮助")


if __name__ == "__main__":
    main()