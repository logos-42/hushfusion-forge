#!/usr/bin/env python3
"""分形循环长期持续学习测试 —— 1000 轮, 分段趋势, 树 vs 固定。

leo 核心问题: 分形循环生长结构在**长期过程**里能持续学习吗? 效果如何?

设计:
  - 1500 轮 (而非 250), μ₀ 持续漂移 (非平稳环境 = 持续学习的真实舞台)
  - 分三阶段看趋势: warm 均值是否随时间下降 (持续改善?) 
  - 对照: 固定 1 节点 vs 分形生长树 (树在误差平台时自动长)
  - 看生长树的节点数随时间: 是否持续长 / 收敛
  - warm/cold 交替照旧: 推荐器 vs 随机, 长期下 warm 应反超 cold
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


class RelDecayDetector:
    def __init__(self, thresh=0.25, k=3, alpha=0.5, warmup=4):
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
        self.detector = RelDecayDetector()
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
        return child

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

    def step(self, X, Y):
        self.t += 1
        mask = (Y[:, 0] < LOG_PENALTY * 0.9)
        loss_self = None
        if mask.sum() > 0:
            Xc, Yc = X[mask], Y[mask]
            if self.t % self.update_freq == 0 or self.t == 1:
                self.opt.zero_grad()
                y, _ = self.predict_tree(Xc)
                mse = ((y - Yc) ** 2).mean()
                active_children = sum(1 for w in self.child_warmup if w == 0)
                if len(self.children) > 0 and active_children > 0:
                    gate = torch.softmax(self.gate, dim=0)
                    ent = -(gate * torch.log(gate + 1e-9)).sum()
                    loss = mse - self.ent_reg * ent
                else:
                    loss = mse
                loss.backward()
                self.opt.step()
                loss_self = float(mse.detach())
                self.clean_losses.append(float(mse.detach()))
                self.clean_losses = self.clean_losses[-10:]
        for i, ch in enumerate(self.children):
            if self.child_warmup[i] > 0:
                if mask.sum() > 0:
                    Xc, Yc = X[mask], Y[mask]
                    ch_opt = torch.optim.Adam(ch.net.parameters(), lr=1e-3)
                    ch_opt.zero_grad()
                    yc = ch.net(Xc)
                    lc = ((yc - Yc) ** 2).mean()
                    lc.backward()
                    ch_opt.step()
                self.child_warmup[i] -= 1
            else:
                ch.step(X, Y)
        return loss_self

    def grow_check(self):
        if len(self.clean_losses) < 6 or len(self.children) >= 4:
            return False
        return self.detector.update(self.clean_losses[-1])


class PlainNode:
    """固定单节点 (均匀对照)。"""
    def __init__(self, in_dim, d_model=32):
        self.net = nn.Sequential(nn.Linear(in_dim, d_model), nn.Tanh(), nn.Linear(d_model, 1))
        self.opt = torch.optim.Adam(self.net.parameters(), lr=1e-3)

    def predict(self, X):
        with torch.no_grad():
            return self.net(X).flatten().numpy()

    def step(self, X, Y):
        self.opt.zero_grad()
        loss = ((self.net(X) - Y) ** 2).mean()
        loss.backward()
        self.opt.step()
        return float(loss.detach())


def random_ctrl(rng):
    return float(rng.choice(ETA_GRID)), float(rng.choice(LAM_GRID))


def run_long(rounds=1500, grow=False, seed=0):
    rng = np.random.default_rng(seed)
    Xbuf, Ybuf = [], []
    root = FractalNodeV2(3, depth=0, D=4) if grow else PlainNode(3)
    warm_abs, cold_abs = [], []
    warm_ratio, cold_ratio = [], []
    N_ETA, N_LAM = 5, 4
    explore_counts = np.zeros((N_ETA, N_LAM)) + 1.0
    grows_log = []
    nodes_hist = [1]

    def bucket(e, l):
        return min(int(e / 0.5 * N_ETA), N_ETA - 1), min(int(l / 0.9 * N_LAM), N_LAM - 1)

    for rnd in range(rounds):
        mu0 = float(rng.uniform(0.01, 0.8))
        best_ref_n = None
        for e in ETA_GRID:
            for l in LAM_GRID:
                n, over = run_control(mu0, e, l)
                if not over and (best_ref_n is None or n < best_ref_n):
                    best_ref_n = n
        is_cold = (rnd % 2 == 1)
        if is_cold or len(Xbuf) < 40:
            eta, lam = random_ctrl(rng)
        else:
            cands = [(e, l) for e in ETA_GRID for l in LAM_GRID]
            feats = np.array([ctrl_feature(e, l, mu0) for e, l in cands])
            Xt = torch.tensor(feats, dtype=torch.float32)
            if grow:
                pred, _ = root.predict_tree(Xt)
                pred = pred.flatten().detach().numpy()
            else:
                pred = root.predict(Xt)
            ucb = np.array([pred[i] - 0.25 * np.log(explore_counts[bucket(e, l)[0], bucket(e, l)[1]])
                            for i, (e, l) in enumerate(cands)])
            idx = int(np.argmin(ucb))
            eta, lam = cands[idx]
            explore_counts[bucket(eta, lam)] += 1.0
        n, over = run_control(mu0, eta, lam)
        score = math.log(n) if not over else LOG_PENALTY
        Xbuf.append(ctrl_feature(eta, lam, mu0))
        Ybuf.append(float(score))
        if is_cold:
            cold_abs.append(n)
            if best_ref_n: cold_ratio.append(n / best_ref_n)
        else:
            warm_abs.append(n)
            if best_ref_n: warm_ratio.append(n / best_ref_n)
        if len(Xbuf) >= 40:
            Xb = torch.tensor(np.array(Xbuf[-40:]), dtype=torch.float32)
            Yb = torch.tensor(np.array(Ybuf[-40:]), dtype=torch.float32).reshape(-1, 1)
            if grow:
                lself = root.step(Xb, Yb)
                if lself is not None and root.grow_check():
                    if root.n_nodes() < 5:
                        nb = root.n_nodes()
                        root.spawn_child(3)
                        grows_log.append({"round": rnd, "nodes": root.n_nodes()})
                        root.detector = RelDecayDetector()
                if rnd % 50 == 0:
                    nodes_hist.append(root.n_nodes())
            else:
                root.step(Xb, Yb)
    return warm_abs, cold_abs, warm_ratio, cold_ratio, grows_log, nodes_hist


print("=== 分形循环长期持续学习测试 (1500 轮, μ₀ 持续漂移) ===")
for label, grow in [("固定1节点", False), ("分形生长树", True)]:
    w, c, wr, cr, gl, nh = run_long(1500, grow=grow)
    print(f"\n{label}:")
    # 三阶段趋势
    seg = 500
    for i in range(0, 1500, seg):
        ws = w[i:i+seg]
        cs = c[i:i+seg]
        if ws:
            print(f"  round {i}-{i+seg}: warm {np.mean(ws):.0f} ({len(ws)}轮) | "
                  f"cold {np.mean(cs):.0f} ({len(cs)}轮) | 最优比 warm {np.mean(wr[i:i+seg]):.2f}× / cold {np.mean(cr[i:i+seg]):.2f}×")
    print(f"  全程: warm {np.mean(w):.0f} 步 ({np.mean(wr):.2f}×) | cold {np.mean(c):.0f} 步 ({np.mean(cr):.2f}×)")
    if grow:
        print(f"  生长: {len(gl)} 次, 节点历史 {nh}")