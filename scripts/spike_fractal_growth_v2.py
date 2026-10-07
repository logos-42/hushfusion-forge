#!/usr/bin/env python3
"""分形循环生长 v2 —— 修复死子节点 (门控探索 + 干净 loss)。

v1(2f)缺陷:
  1. 子节点门控初始 0 ⟹ 贡献 0 ⟹ 梯度 0 ⟹ 永远死
  2. 生长判据被越界罚分 (loss 21~25) 误触发
修法:
  1. 门控初始 = 均匀 (1/(n_children+1)) —— 子节点出生就有贡献, 有梯度可学
     + 子节点出生后 warmup 期独立训练 (先学会独立预测, 再进凸组合)
  2. 生长判据只喂安全配置的归一化 loss (排除越界罚分) —— 干净信号
  3. 门控加熵正则 (鼓励探索, 防凸组合塌缩到单节点)
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
    """分形树节点 v2: 门控均匀起步 + 子节点热身 + 熵正则。

    生长: 节点收到**干净 loss**(只含安全配置)进入平台 ⟹ spawn 子节点。
    子节点: 出生后 warmup 轮独立训练(不计入门控), 之后门控从均匀起步。
    沟通: 预测 = 递归凸组合, softmax 门控 + 熵正则(λ_ent 鼓励多用子节点)。
    """

    def __init__(self, in_dim, d_model=32, depth=0, D=4, warmup=15, ent_reg=0.01):
        self.depth, self.D = depth, D
        self.warmup, self.ent_reg = warmup, ent_reg
        self.net = nn.Sequential(nn.Linear(in_dim, d_model), nn.Tanh(), nn.Linear(d_model, 1))
        self.gate = nn.Parameter(torch.zeros(1))  # 只有自己时门控 [1]
        self.children = []
        self.child_warmup = []   # 每个子节点剩余热身轮数
        self.opt = torch.optim.Adam(list(self.net.parameters()) + [self.gate], lr=1e-3)
        self.update_freq = D ** depth
        self.t = 0
        self.detector = RelDecayDetector()
        # 干净 loss 缓冲: 只装安全配置的归一化 loss (用于生长判据)
        self.clean_losses = []

    def n_nodes(self):
        return 1 + sum(c.n_nodes() for c in self.children)

    def spawn_child(self, in_dim):
        """门控均匀重初始化 + 子节点热身计数。"""
        child = FractalNodeV2(in_dim, d_model=self.net[0].out_features,
                              depth=self.depth + 1, D=self.D,
                              warmup=self.warmup, ent_reg=self.ent_reg)
        self.children.append(child)
        self.child_warmup.append(self.warmup)
        # 门控均匀起步: 所有节点(含自己)均分
        n = len(self.children) + 1
        new_gate = torch.full((n,), math.log(n))  # log 空间, softmax 后 1/n
        self.gate = nn.Parameter(new_gate)
        self.opt = torch.optim.Adam(list(self.net.parameters()) + [self.gate], lr=1e-3)
        return child

    def predict_tree(self, X):
        self_out = self.net(X)
        outs = [self_out]
        child_list = []
        for i, ch in enumerate(self.children):
            # 热身中的子节点: 不参与凸组合(还没被信任), 单独训练
            if self.child_warmup[i] > 0:
                continue
            cy, _ = ch.predict_tree(X)
            outs.append(cy)
            child_list.append(i)
        if len(outs) == 1:
            return self_out, [self_out]
        gate = torch.softmax(self.gate, dim=0)
        # 只取参与子节点的门控权重 (子节点在 children 里的序号 +1 = gate 下标)
        y = torch.zeros_like(self_out)
        y = y + gate[0] * outs[0]  # 自己
        for k, ch_idx in enumerate(child_list):
            y = y + gate[int(ch_idx) + 1] * outs[k + 1]
        return y, outs

    def step(self, X, Y, clean_only=True):
        """训练自己 + 子节点. clean_only: 只喂安全配置样本(过滤越界罚分)。"""
        self.t += 1
        # 过滤: 只保留安全样本 (Y < LOG_PENALTY*0.9) 用于生长判据
        mask = (Y[:, 0] < LOG_PENALTY * 0.9)
        loss_self = None
        if mask.sum() > 0:
            Xc, Yc = X[mask], Y[mask]
            if self.t % self.update_freq == 0 or self.t == 1:
                self.opt.zero_grad()
                y, _ = self.predict_tree(Xc)
                mse = ((y - Yc) ** 2).mean()
                # 熵正则: 鼓励凸组合用多个节点 (防塌缩)
                if len(self.children) > 0 and sum(1 for w in self.child_warmup if w == 0) > 0:
                    gate = torch.softmax(self.gate, dim=0)
                    ent = -(gate * torch.log(gate + 1e-9)).sum()
                    loss = mse - self.ent_reg * ent
                else:
                    loss = mse
                loss.backward()
                self.opt.step()
                loss_self = float(mse.detach())
                # 干净 loss 入缓冲 (生长判据)
                self.clean_losses.append(float(mse.detach()))
                self.clean_losses = self.clean_losses[-10:]
        # 热身子节点独立训练 + 递减热身
        for i, ch in enumerate(self.children):
            if self.child_warmup[i] > 0:
                if mask.sum() > 0:
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
        """生长判据: 只基于干净 loss (安全配置)。"""
        if len(self.clean_losses) < 6 or len(self.children) >= 4:
            return False
        return self.detector.update(self.clean_losses[-1])


def random_ctrl(rng):
    return float(rng.choice(ETA_GRID)), float(rng.choice(LAM_GRID))


def run_exp(rounds=250, grow=False, seed=0, max_nodes=5):
    rng = np.random.default_rng(seed)
    Xbuf, Ybuf = [], []
    root = FractalNodeV2(3, depth=0, D=4)
    warm_abs, cold_abs, warm_ratio, cold_ratio = [], [], [], []
    N_ETA, N_LAM = 5, 4
    explore_counts = np.zeros((N_ETA, N_LAM)) + 1.0
    grows_log = []

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
            pred, _ = root.predict_tree(Xt)
            pred = pred.flatten().detach().numpy()
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
            lself = root.step(Xb, Yb)
            if grow and lself is not None and root.grow_check():
                if root.n_nodes() < max_nodes:
                    nb = root.n_nodes()
                    root.spawn_child(3)
                    grows_log.append({"round": rnd, "nodes": root.n_nodes()})
                    print(f"    [生长] round={rnd} 干净loss平台 {lself:.3f} → 长子, "
                          f"节点 {nb}→{root.n_nodes()}, 门控均匀起步", flush=True)
                    root.detector = RelDecayDetector()
    return warm_abs, cold_abs, warm_ratio, cold_ratio, grows_log, root.n_nodes()


print("=== 分形循环生长 v2: 门控探索 + 干净 loss ===")
for label, grow in [("固定1节点", False), ("生长树v2", True)]:
    w, c, wr, cr, gl, nn_ = run_exp(250, grow=grow)
    extra = f" | 树节点 {nn_}, 生长 {len(gl)} 次" if grow else ""
    print(f"{label}: warm 均值 {np.mean(w):.0f}步 (最优比 {np.mean(wr):.2f}×) | "
          f"cold 均值 {np.mean(c):.0f}步 (最优比 {np.mean(cr):.2f}×){extra}")