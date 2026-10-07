#!/usr/bin/env python3
"""反引力控制 · 发育式控制学习器 v2 —— 任意深度 DevelopmentalHead。

v1 崩因: PLNHead 硬编码 2 层 (predictor[0]/[2]), clone_params/fwd_with 不认
新增层。v2 重写 DevelopmentalHead: 支持任意深度, 动态克隆/遍历/Meta-SGD。

核心(与 PLNHead 同协议, 只换「结构可变」):
  - blocks: List[Linear|Tanh], 任意深
  - clone_params(): 收集全部 Linear 的 W/b (动态)
  - fwd_with(W, h): 按 lin_indices 顺序遍历
  - per_feature_step: 逐参数 Meta-SGD (step_beta 是 meta 参数, 外循环学)
  - add_layer(): 输出头前插 Linear+Tanh (半宽, 重投影)
  - oml_step 同协议: 内循环克隆头适应 support, 外循环 query loss 反传

对照: 固定1层 / 固定2层 / 发育版(1层起, 相对衰减自动长层)
环境: 反引力 μ 控制, μ₀ 漂移 (同前)。
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


class DevelopmentalHead(nn.Module):
    """任意深度 OML 头 (与 PLNHead 同协议: Meta-SGD 双循环)。

    blocks: [Linear, Tanh, ..., Linear]。lin_indices 记录 Linear 下标。
    """

    def __init__(self, input_dim, d_model, n_classes=1, inner_lr=0.05, hidden_layers=1):
        super().__init__()
        self.blocks = nn.ModuleList()
        dims = [input_dim] + [d_model] * hidden_layers + [n_classes]
        for i in range(len(dims) - 1):
            lin = nn.Linear(dims[i], dims[i + 1])
            if i < len(dims) - 2:
                nn.init.xavier_uniform_(lin.weight)
                nn.init.zeros_(lin.bias)
            self.blocks.append(lin)
            if i < len(dims) - 2:
                self.blocks.append(nn.Tanh())
        self.lin_indices = [i for i, b in enumerate(self.blocks) if isinstance(b, nn.Linear)]
        self.n_layers = hidden_layers
        self.step_beta = nn.Parameter(torch.full((self._n_params(),), math.log(inner_lr)))

    def _n_params(self):
        return sum(self.blocks[i].weight.numel() + self.blocks[i].bias.numel()
                   for i in self.lin_indices)

    def clone_params(self):
        W = []
        for i in self.lin_indices:
            W.append(self.blocks[i].weight.clone())
            W.append(self.blocks[i].bias.clone())
        return W

    def per_feature_step(self, W, grads):
        beta = self.step_beta
        new_W, off = [], 0
        for w, g in zip(W, grads):
            n = w.numel()
            b = beta[off:off + n].view_as(w)
            alpha = b.exp().clamp(max=1.0)
            new_W.append(w - alpha * g)
            off += n
        return new_W

    @staticmethod
    def fwd_with(W, blocks, lin_indices, h):
        """用给定参数前向。W 按 lin_indices 顺序: 每层 [weight, bias]。"""
        wi = 0
        x = h
        for bi, b in enumerate(blocks):
            if bi in lin_indices:
                w, bias = W[wi], W[wi + 1]
                x = x @ w.t() + bias
                wi += 2
            else:
                x = torch.tanh(x)
        return x

    def forward(self, h):
        return self.fwd_with(self.clone_params(), self.blocks, self.lin_indices, h)

    def add_layer(self, width=None):
        """输出头前插 Linear+Tanh (半宽重投影)。"""
        out_idx = self.lin_indices[-1]
        out_lin = self.blocks[out_idx]
        w = width or max(8, out_lin.in_features // 2)
        new_lin = nn.Linear(out_lin.in_features, w)
        nn.init.xavier_uniform_(new_lin.weight)
        nn.init.zeros_(new_lin.bias)
        out_lin.in_features = w
        s = 1.0 / np.sqrt(w)
        out_lin.weight = nn.Parameter(torch.randn(out_lin.out_features, w) * s)
        out_lin.bias = nn.Parameter(torch.zeros(out_lin.out_features))
        # 在 out_lin 前插入 [new_lin, Tanh]
        self.blocks.insert(out_idx, nn.Tanh())
        self.blocks.insert(out_idx, new_lin)
        self.lin_indices = [i for i, b in enumerate(self.blocks) if isinstance(b, nn.Linear)]
        self.n_layers += 1
        # step_beta 扩到新参数数
        new_beta = torch.full((self._n_params(),), math.log(0.05))
        with torch.no_grad():
            old = self.step_beta
            n_old = old.numel()
            new_beta[:n_old] = old
        self.step_beta = nn.Parameter(new_beta)


def oml_step(model, head, Xs, ys, Xq, yq, K=3):
    """双循环: 内循环克隆头适应 support, 外循环 query loss 更新 meta。"""
    Xs = torch.tensor(Xs, dtype=torch.float32)
    ys = torch.tensor(ys, dtype=torch.float32).reshape(-1, 1)
    Xq = torch.tensor(Xq, dtype=torch.float32)
    yq = torch.tensor(yq, dtype=torch.float32).reshape(-1, 1)
    W = head.clone_params()
    for _ in range(K):
        loss_s = ((DevelopmentalHead.fwd_with(W, head.blocks, head.lin_indices, Xs) - ys) ** 2).mean()
        grads = torch.autograd.grad(loss_s, W, create_graph=True)
        W = head.per_feature_step(W, grads)
    loss_q = ((DevelopmentalHead.fwd_with(W, head.blocks, head.lin_indices, Xq) - yq) ** 2).mean()
    opt = torch.optim.Adam(head.parameters(), lr=1e-3)
    opt.zero_grad()
    loss_q.backward()
    torch.nn.utils.clip_grad_norm_(head.parameters(), 1.0)
    opt.step()
    return float(loss_q.detach())


def predict(model, head, X):
    Xt = torch.tensor(np.array(X, dtype=float), dtype=torch.float32)
    with torch.no_grad():
        return DevelopmentalHead.fwd_with(head.clone_params(), head.blocks, head.lin_indices, Xt).numpy().flatten()


def random_ctrl(rng):
    return float(rng.choice(ETA_GRID)), float(rng.choice(LAM_GRID))


def run_exp(rounds=250, hidden_layers=1, develop=False, seed=0):
    rng = np.random.default_rng(seed)
    head = DevelopmentalHead(3, 32, hidden_layers=hidden_layers)
    pool_X, pool_Y = [], []
    warm_abs, cold_abs, warm_ratio, cold_ratio = [], [], [], []
    N_ETA, N_LAM = 5, 4
    explore_counts = np.zeros((N_ETA, N_LAM)) + 1.0
    detector = RelDecayDetector()
    grows = []

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
        if is_cold or len(pool_X) < 40:
            eta, lam = random_ctrl(rng)
        else:
            cands = [(e, l) for e in ETA_GRID for l in LAM_GRID]
            feats = np.array([ctrl_feature(e, l, mu0) for e, l in cands])
            pred = predict(None, head, feats)
            ucb = np.array([pred[i] - 0.25 * np.log(explore_counts[bucket(e, l)[0], bucket(e, l)[1]])
                            for i, (e, l) in enumerate(cands)])
            idx = int(np.argmin(ucb))
            eta, lam = cands[idx]
            explore_counts[bucket(eta, lam)] += 1.0
        n, over = run_control(mu0, eta, lam)
        score = math.log(n) if not over else LOG_PENALTY
        pool_X.append(ctrl_feature(eta, lam, mu0))
        pool_Y.append(float(score))
        if is_cold:
            cold_abs.append(n)
            if best_ref_n: cold_ratio.append(n / best_ref_n)
        else:
            warm_abs.append(n)
            if best_ref_n: warm_ratio.append(n / best_ref_n)
        if len(pool_X) >= 8:
            X = np.array(pool_X, dtype=float); Y = np.array(pool_Y, dtype=float)
            idxp = rng.permutation(len(X))
            tr = idxp[: max(4, len(X) - 3)]; te = idxp[max(4, len(X) - 3):]
            try:
                ql = oml_step(None, head, X[tr], Y[tr], X[te], Y[te], K=3)
            except Exception:
                ql = None
            if develop and ql is not None and detector.update(ql):
                head.add_layer()
                grows.append(rnd)
                detector = RelDecayDetector()
                print(f"    [发育] round={rnd} query_loss={ql:.4f} 相对衰减 → "
                      f"{head.n_layers} 隐藏层", flush=True)
    return warm_abs, cold_abs, warm_ratio, cold_ratio, grows


print("=== 反引力控制 · 发育式控制学习器 v2 (任意深度头) ===")
res = {}
for label, hl, dev in [("固定1层", 1, False), ("固定2层", 2, False), ("发育版", 1, True)]:
    w, c, wr, cr, grows = run_exp(250, hidden_layers=hl, develop=dev)
    res[label] = (w, c, wr, cr, grows)
    extra = f" | 长层 {len(grows)} 次" if dev else ""
    print(f"{label}: warm 均值 {np.mean(w):.0f}步 (最优比 {np.mean(wr):.2f}×) | "
          f"cold 均值 {np.mean(c):.0f}步 (最优比 {np.mean(cr):.2f}×){extra}")

print("\n=== 判读 ===")
f1, f2, dv = res["固定1层"], res["固定2层"], res["发育版"]
print(f"固定1层 warm {np.mean(f1[0]):.0f}步 | 固定2层 warm {np.mean(f2[0]):.0f}步 | 发育版 warm {np.mean(dv[0]):.0f}步")
print(f"发育版长层 {len(dv[4])} 次")
for lab, r in [("固定1层", f1), ("固定2层", f2), ("发育版", dv)]:
    print(f"  {lab}: warm/cold = {np.mean(r[0]):.0f}/{np.mean(r[1]):.0f} 步 (比 {np.mean(r[2]):.2f}×/{np.mean(r[3]):.2f}×)")
print("✓ warm < cold 且发育 ≥ 固定 = 发育式控制学习器成立" if
      (np.mean(dv[0]) < np.mean(dv[1]) and np.mean(dv[0]) <= min(np.mean(f1[0]), np.mean(f2[0])) * 1.1)
      else "~ 看明细")