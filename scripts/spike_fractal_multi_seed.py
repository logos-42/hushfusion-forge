#!/usr/bin/env python3
"""分形循环长期持续学习 —— 多 seed 稳健性验证 (3 seeds × 1500 轮)。

leo 纪律: 单 seed 结论不可信, 必须多 seed 验证。
§2g 的 2.2× 优势来自 seed=0, 需 3 seeds 确认不是运气。
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


def run_long(rounds=1200, grow=False, seed=0):
    rng = np.random.default_rng(seed)
    Xbuf, Ybuf = [], []
    root = FractalNodeV2(3, depth=0, D=4) if grow else PlainNode(3)
    warm_abs, cold_abs = [], []
    N_ETA, N_LAM = 5, 4
    explore_counts = np.zeros((N_ETA, N_LAM)) + 1.0
    grows_n = 0

    def bucket(e, l):
        return min(int(e / 0.5 * N_ETA), N_ETA - 1), min(int(l / 0.9 * N_LAM), N_LAM - 1)

    for rnd in range(rounds):
        mu0 = float(rng.uniform(0.01, 0.8))
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
        else:
            warm_abs.append(n)
        if len(Xbuf) >= 40:
            Xb = torch.tensor(np.array(Xbuf[-40:]), dtype=torch.float32)
            Yb = torch.tensor(np.array(Ybuf[-40:]), dtype=torch.float32).reshape(-1, 1)
            if grow:
                lself = root.step(Xb, Yb)
                if lself is not None and root.grow_check():
                    if root.n_nodes() < 5:
                        root.spawn_child(3)
                        grows_n += 1
                        root.detector = RelDecayDetector()
            else:
                root.step(Xb, Yb)
    if grow:
        return warm_abs, cold_abs, grows_n, root.n_nodes()
    return warm_abs, cold_abs, 0, 1


print("=== 分形树 vs 固定 — 多 seed 稳健性 (3 seeds × 1200 轮) ===")
plain_all, fractal_all = [], []
plain_cold, fractal_cold = [], []
grows_info = []
for seed in [0, 1, 2]:
    wp, cp, _, _ = run_long(1200, grow=False, seed=seed)
    wf, cf, gn, nn_ = run_long(1200, grow=True, seed=seed)
    plain_all.append(np.mean(wp)); fractal_all.append(np.mean(wf))
    plain_cold.append(np.mean(cp)); fractal_cold.append(np.mean(cf))
    grows_info.append((gn, nn_))
    print(f"seed {seed}: 固定 warm {np.mean(wp):.0f}步 | 分形 warm {np.mean(wf):.0f}步 "
          f"| 分形优势 {(np.mean(wp)/np.mean(wf)-1)*100:+.0f}% | 生长 {gn}次/树{nn_}节点")

print(f"\n=== 汇总 (3 seeds) ===")
print(f"固定 warm: {np.mean(plain_all):.0f} ± {np.std(plain_all):.0f} 步")
print(f"分形 warm: {np.mean(fractal_all):.0f} ± {np.std(fractal_all):.0f} 步")
print(f"cold 基线: 固定 {np.mean(plain_cold):.0f} / 分形 {np.mean(fractal_cold):.0f} 步")
print(f"分形/固定 平均比: {np.mean(plain_all)/np.mean(fractal_all):.2f}×")
wins = sum(1 for a, b in zip(plain_all, fractal_all) if b < a)
print(f"分形赢 {wins}/3 seeds")
print(f"生长: {grows_info}")
if wins >= 3:
    print("✓ 分形树多 seed 稳健优于固定 → 2.2× 不是 seed 运气")
elif wins >= 2:
    print("~ 分形树多数 seed 占优 (需看方差)")
else:
    print("✗ 分形优势不稳健")