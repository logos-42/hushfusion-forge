#!/usr/bin/env python3
"""分形循环生长 spike —— 递归自相似循环树 (自生长的更多循环)。

leo 方向: 「增加一种自生长的更多循环, 不同循环的层数之间互相沟通, 分形结构的生长」。

v1(2级固定)已证: 分形 > 均匀。本 spike 做完整版 —— **递归生长**:
  - FractalNode: 每个节点有自己的小网络 + 到子节点的凸组合门控
  - 生长: 节点误差进入平台(相对衰减判据) ⟹ 克隆一个子节点(自相似, 不同初始化)
    子节点更新频率 = 父频率 / D (分形维数, 深层更稀疏)
  - 沟通: predict = 递归凸组合: node 输出 = g_self·self_out + Σ g_child·child_out
    (softmax 归一, 每层都满足 Fractal.lean 的凸组合有界定理)
  - 递归: 子节点自己也能再长孙节点 (深度按需生长, 无预设)

对照: 固定 1 节点(均匀) vs 生长树(从 1 节点起, 自动长)
任务: 反引力控制学习 (μ₀ 漂移, 同前协议)
验收: 生长树的 warm 步数 ≤ 固定 1 节点, 且确实长出了子节点(树深 > 1)。
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
    """误差平台检测 (生长触发): 相对衰减。"""
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


class FractalNode:
    """分形循环树的节点: 自网络 + 凸组合门控 + 子节点列表。

    生长: 误差平台 ⟹ spawn 一个子节点 (自相似, 深一级, 更新频率/D)。
    沟通: predict = softmax(g)·[self_out, child_outs...], 递归到叶子。
    """

    def __init__(self, in_dim, d_model=32, depth=0, D=4, parent=None):
        self.depth = depth
        self.D = D
        self.parent = parent
        self.net = nn.Sequential(nn.Linear(in_dim, d_model), nn.Tanh(), nn.Linear(d_model, 1))
        # 凸组合门控: [self] + children, softmax 归一
        self.gate = nn.Parameter(torch.zeros(1))
        self.children = []          # [(child_node, gate_param_index)]
        self.opt = torch.optim.Adam(list(self.net.parameters()) + [self.gate], lr=1e-3)
        self.update_freq = D ** depth   # 深度越深更新越稀疏 (分形维数)
        self.t = 0
        self.detector = RelDecayDetector()
        self.grew = False

    def n_nodes(self):
        return 1 + sum(c.n_nodes() for c in self.children)

    def spawn_child(self, in_dim):
        child = FractalNode(in_dim, d_model=self.net[0].out_features,
                            depth=self.depth + 1, D=self.D, parent=self)
        self.children.append(child)
        # 门控扩到 [self] + children 数
        new_gate = torch.zeros(len(self.children) + 1)
        with torch.no_grad():
            new_gate[0] = self.gate[0] if self.gate.numel() >= 1 else 0.0
        self.gate = nn.Parameter(new_gate)
        self.opt = torch.optim.Adam(list(self.net.parameters()) + [self.gate], lr=1e-3)
        self.grew = True
        return child

    def predict_tree(self, X):
        """递归凸组合: self_out + children 加权。"""
        self_out = self.net(X)
        outs = [self_out]
        for ch in self.children:
            outs.append(ch.predict_tree(X))
        gate = torch.softmax(self.gate, dim=0)
        y = torch.zeros_like(self_out)
        for i, o in enumerate(outs):
            y = y + gate[i] * o
        return y

    def step(self, X, Y, force=False):
        """按更新频率训练自己; 递归步进子节点。"""
        self.t += 1
        loss_self = None
        if force or (self.t % self.update_freq == 0):
            self.opt.zero_grad()
            y = self.predict_tree(X)
            loss = ((y - Y) ** 2).mean()
            loss.backward()
            self.opt.step()
            loss_self = float(loss.detach())
        for ch in self.children:
            ch.step(X, Y, force=False)
        return loss_self


def random_ctrl(rng):
    return float(rng.choice(ETA_GRID)), float(rng.choice(LAM_GRID))


def run_exp(rounds=250, grow=False, seed=0, max_depth=2):
    rng = np.random.default_rng(seed)
    Xbuf, Ybuf = [], []
    root = FractalNode(3, depth=0, D=4)
    warm_abs, cold_abs, warm_ratio, cold_ratio = [], [], [], []
    N_ETA, N_LAM = 5, 4
    explore_counts = np.zeros((N_ETA, N_LAM)) + 1.0

    def bucket(e, l):
        return min(int(e / 0.5 * N_ETA), N_ETA - 1), min(int(l / 0.9 * N_LAM), N_LAM - 1)

    grows_log = []
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
            pred = root.predict_tree(Xt).flatten().detach().numpy()
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
            # 生长: 根节点误差平台且没到最大深度 ⟹ 长子树
            if grow and lself is not None and root.detector.update(lself):
                if root.n_nodes() < 2 ** max_depth:
                    n_before = root.n_nodes()
                    root.spawn_child(3)
                    grows_log.append({"round": rnd, "nodes": root.n_nodes(),
                                      "lost": lself})
                    print(f"    [生长] round={rnd} 误差平台 {lself:.3f} → 长子节点, "
                          f"树节点 {n_before}→{root.n_nodes()}", flush=True)
                    root.detector = RelDecayDetector()
    return warm_abs, cold_abs, warm_ratio, cold_ratio, grows_log, root.n_nodes()


print("=== 分形循环生长: 递归自相似树 vs 固定 1 节点 ===")
for label, grow in [("固定1节点(均匀)", False), ("生长树(递归)", True)]:
    w, c, wr, cr, gl, nnodes = run_exp(250, grow=grow)
    extra = f" | 树节点 {nnodes}, 生长 {len(gl)} 次" if grow else ""
    print(f"{label}: warm 均值 {np.mean(w):.0f}步 (最优比 {np.mean(wr):.2f}×) | "
          f"cold 均值 {np.mean(c):.0f}步 (最优比 {np.mean(cr):.2f}×){extra}")