#!/usr/bin/env python3
"""反引力控制 · 发育式控制学习器 v3 —— 冷却期 + 停止判据 (消除过度长层)。

v2 问题: 发育判据(query_loss 相对衰减)过度触发 27 次, 长到 28 层 ——
query_loss 非单调(OML 波动大), 把正常波动误判成「该长层」。

v3 修法 (v6 验证过的组件):
  1. grow_cooldown: 每次长层后冷却 N 个周期, 冷却期内不看发育判据
     (新层刚插入, loss 会暂时波动/变差, 不是「该再长」信号)
  2. no_gain_after_grow: 长层后跟踪新结构效果, 若冷却期后连续 K 周期
     val 没低于长层前最佳 ⟹ 这次长层无收益, 冻结结构停止发育
  3. max_grows 上限兜底(防失控, 但应几乎不触发)

预期: 发育版长 0~2 次就自动停止(控制面 1 层够), 而非 27 次。
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
    def __init__(self, thresh=0.3, k=4, alpha=0.5, warmup=5):
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
    def __init__(self, input_dim, d_model, n_classes=1, inner_lr=0.05, hidden_layers=1):
        super().__init__()
        self.blocks = nn.ModuleList()
        dims = [input_dim] + [d_model] * hidden_layers + [n_classes]
        for i in range(len(dims) - 1):
            lin = nn.Linear(dims[i], dims[i + 1])
            if i < len(dims) - 2:
                nn.init.xavier_uniform_(lin.weight); nn.init.zeros_(lin.bias)
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
            W.append(self.blocks[i].weight.clone()); W.append(self.blocks[i].bias.clone())
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
        out_idx = self.lin_indices[-1]
        out_lin = self.blocks[out_idx]
        w = width or max(8, out_lin.in_features // 2)
        new_lin = nn.Linear(out_lin.in_features, w)
        nn.init.xavier_uniform_(new_lin.weight); nn.init.zeros_(new_lin.bias)
        out_lin.in_features = w
        s = 1.0 / np.sqrt(w)
        out_lin.weight = nn.Parameter(torch.randn(out_lin.out_features, w) * s)
        out_lin.bias = nn.Parameter(torch.zeros(out_lin.out_features))
        self.blocks.insert(out_idx, nn.Tanh())
        self.blocks.insert(out_idx, new_lin)
        self.lin_indices = [i for i, b in enumerate(self.blocks) if isinstance(b, nn.Linear)]
        self.n_layers += 1
        new_beta = torch.full((self._n_params(),), math.log(0.05))
        with torch.no_grad():
            n_old = self.step_beta.numel()
            new_beta[:n_old] = self.step_beta
        self.step_beta = nn.Parameter(new_beta)


def oml_step(head, Xs, ys, Xq, yq, K=3):
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


def predict(head, X):
    Xt = torch.tensor(np.array(X, dtype=float), dtype=torch.float32)
    with torch.no_grad():
        return DevelopmentalHead.fwd_with(head.clone_params(), head.blocks, head.lin_indices, Xt).numpy().flatten()


def random_ctrl(rng):
    return float(rng.choice(ETA_GRID)), float(rng.choice(LAM_GRID))


def run_exp(rounds=250, hidden_layers=1, develop=False, seed=0,
            cooldown=8, patience=6, max_grows=3):
    """v3: develop=True 时带冷却期+停止判据。"""
    rng = np.random.default_rng(seed)
    head = DevelopmentalHead(3, 32, hidden_layers=hidden_layers)
    pool_X, pool_Y = [], []
    warm_abs, cold_abs, warm_ratio, cold_ratio = [], [], [], []
    N_ETA, N_LAM = 5, 4
    explore_counts = np.zeros((N_ETA, N_LAM)) + 1.0
    detector = RelDecayDetector()
    grows = 0
    grow_log = []
    post_grow_best = None
    post_grow_count = 0
    cooldown_left = 0
    grow_val = None
    frozen = False

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
            pred = predict(head, feats)
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
                ql = oml_step(head, X[tr], Y[tr], X[te], Y[te], K=3)
            except Exception:
                ql = None

            if develop and ql is not None and not frozen:
                # 长层后跟踪: 新结构是否带来收益
                if grow_val is not None:
                    post_grow_best = min(post_grow_best, ql)
                    if cooldown_left > 0:
                        cooldown_left -= 1
                    else:
                        post_grow_count += 1
                        if post_grow_count >= patience and post_grow_best >= grow_val:
                            frozen = True
                            grow_log.append({"stop": rnd, "layers": head.n_layers,
                                             "grow_val": grow_val, "best_after": post_grow_best})
                            print(f"    [停止] round={rnd} 冷却后 {patience} 周期 val 未低于长层前 "
                                  f"{grow_val:.3f} (现 {post_grow_best:.3f}) ⟹ 冻结结构 "
                                  f"({head.n_layers} 隐藏层)", flush=True)
                # 发育判据 → 长层 (未冻结且没超上限)
                if not frozen and grows < max_grows and detector.update(ql):
                    grow_val = ql
                    post_grow_best = ql
                    post_grow_count = 0
                    cooldown_left = cooldown
                    head.add_layer()
                    grows += 1
                    grow_log.append({"grow": rnd, "layers": head.n_layers, "ql": ql})
                    print(f"    [发育] round={rnd} ql={ql:.3f} 相对衰减 → {head.n_layers} 隐藏层 "
                          f"(冷却 {cooldown})", flush=True)
                    detector = RelDecayDetector()
    return warm_abs, cold_abs, warm_ratio, cold_ratio, grow_log


print("=== 发育式控制学习器 v3: 冷却期 + 停止判据 ===")
res = {}
for label, hl, dev in [("固定1层", 1, False), ("发育v2(无停止)", 1, False), ("发育v3(+冷却停止)", 1, True)]:
    if label == "发育v2(无停止)":
        # 用 v2 行为: develop 但无限长层 (max_grows=999, 无冷却停止逻辑)
        # 简化: 这里跑一个 develop=True 但 max_grows=999 的对照 —— 但 run_exp 的
        # v3 逻辑总会冻结。诚实处理: v2 的 27 次结果已在上轮记录, 这里只报 v3。
        continue
    w, c, wr, cr, gl = run_exp(250, hidden_layers=hl, develop=dev)
    res[label] = (w, c, wr, cr, gl)
    extra = f" | 长层 {len([g for g in gl if 'grow' in g])} 次, 停止 {len([g for g in gl if 'stop' in g])} 次" if dev else ""
    print(f"{label}: warm 均值 {np.mean(w):.0f}步 (最优比 {np.mean(wr):.2f}×) | "
          f"cold 均值 {np.mean(c):.0f}步 (最优比 {np.mean(cr):.2f}×){extra}")

f1 = res["固定1层"]
dv = res["发育v3(+冷却停止)"]
print("\n=== 判读 ===")
print(f"固定1层 warm {np.mean(f1[0]):.0f}步 | 发育v3 warm {np.mean(dv[0]):.0f}步")
grows_n = len([g for g in dv[4] if "grow" in g])
stops_n = len([g for g in dv[4] if "stop" in g])
print(f"发育v3: 长层 {grows_n} 次 (v2 是 27 次), 停止 {stops_n} 次")
if grows_n <= 3 and stops_n >= 1:
    print("✓ 冷却期+停止判据生效: 结构自动收敛, 不过度长层")
elif grows_n == 0:
    print("~ 无长层(判据太紧/1层确实够)")
else:
    print("~ 仍过度长层, 需调参")
if np.mean(dv[0]) <= np.mean(f1[0]) * 1.1:
    print("✓ 发育v3 ≥ 固定1层 (不劣)")