#!/usr/bin/env python3
"""反引力控制学习器 v3 —— 目标分离建模 (越界分类 + 步数回归)。

§2i 诊断: 混合回归 (越界罚分 8.5 + 正常步数 log0.7~6.5 同一目标) 是双峰,
MLP 学不动 ⟹ MAE 3.07, 命中比 80×。修法: 拆成两个头:
  - 分类头: P(越界) —— 二分类 (安全/越界)
  - 回归头: 仅安全样本训练 log步数 (单峰, 好学)
  推荐: 先筛安全 (分类概率), 再在安全区选回归预测最小的点。

对照:
  A. 混合回归 (v2, MAE~3.07, 命中~80×) —— 复现基线
  B. 分离建模 (分类+回归) —— 应显著提升命中比

评估: 干净协议 (同一经验序列, 400 训练 + 200 测试)
  - 分类准确率 (越界/安全)
  - 安全区回归 MAE
  - 最优点命中比 (推荐 (η,λ) 的步数 / 真最优步数)
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


class PlainNode:
    """混合回归 (v2 基线)。"""
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


class SeparateModel(nn.Module):
    """分离建模: 分类头 + 回归头 (共享隐藏层, 两个输出头)。"""
    def __init__(self, in_dim, d_model=32):
        super().__init__()
        self.shared = nn.Sequential(nn.Linear(in_dim, d_model), nn.Tanh())
        self.cls_head = nn.Linear(d_model, 1)   # P(越界) logit
        self.reg_head = nn.Linear(d_model, 1)   # log(步数), 仅安全样本训练

    def forward(self, X):
        h = self.shared(X)
        return self.cls_head(h), self.reg_head(h)

    def predict_flat(self, X):
        """返回每个点的「期望步数」: 越界概率×罚 + 安全概率×回归预测。"""
        with torch.no_grad():
            clogit, rlog = self.forward(X)
            p_over = torch.sigmoid(clogit).flatten()
            # 期望 = P(over)·LOG_PENALTY + (1-P(over))·reg
            exp = p_over * LOG_PENALTY + (1 - p_over) * rlog.flatten()
            return exp.numpy()

    def predict_safe(self, X, thresh=0.5):
        """推荐: 只报安全区 (P(over) < thresh) 的回归值; 全越界则返回 999。"""
        with torch.no_grad():
            clogit, rlog = self.forward(X)
            p_over = torch.sigmoid(clogit).flatten()
            safe = (p_over < thresh).float()
            out = torch.where(safe > 0, rlog.flatten(), torch.full_like(rlog.flatten(), 999.0))
            return out.numpy(), p_over.numpy()


def train_separate(model, X, Y, steps=5):
    """分离训练: 分类头用全部样本 (越界=1), 回归头只安全样本。"""
    opt = torch.optim.Adam(model.parameters(), lr=1e-3)
    for _ in range(steps):
        opt.zero_grad()
        clogit, rlog = model(X)
        # 分类: 越界 = Y >= LOG_PENALTY*0.9
        over = (Y[:, 0] >= LOG_PENALTY * 0.9).float().reshape(-1, 1)
        loss_cls = nn.functional.binary_cross_entropy_with_logits(clogit, over)
        # 回归: 只安全样本
        safe = (over < 0.5).squeeze()
        if safe.sum() > 0:
            loss_reg = ((rlog[safe] - Y[safe]) ** 2).mean()
        else:
            loss_reg = torch.zeros(())
        loss = loss_cls + loss_reg
        loss.backward()
        opt.step()
        return float(loss_cls.detach()), float(loss_reg.detach())


def eval_hit(model, mu0_test):
    """最优点命中比: 模型推荐 (η,λ) 的步数 / 真最优步数。"""
    cands = [(e, l) for e in ETA_GRID for l in LAM_GRID]
    feats = np.array([ctrl_feature(e, l, mu0_test) for e, l in cands])
    Xt = torch.tensor(feats, dtype=torch.float32)
    n_true = None
    for e in ETA_GRID:
        for l in LAM_GRID:
            n_, o_ = run_control(mu0_test, e, l)
            if not o_ and (n_true is None or n_ < n_true):
                n_true = n_
    if isinstance(model, SeparateModel):
        exp, p_over = model.predict_safe(Xt)
        # 只在安全区 (P<0.5) 选
        safe_idx = [i for i, p in enumerate(p_over) if p < 0.5]
        if not safe_idx:
            return 999.0, None, None
        idx = safe_idx[int(np.argmin(exp[safe_idx]))]
    else:
        pred = model.predict_flat(Xt)
        idx = int(np.argmin(pred))
    be, bl = cands[idx]
    n_rec, _ = run_control(mu0_test, be, bl)
    if n_rec is None or n_true is None:
        return 999.0, None, None
    return n_rec / n_true, (be, bl), n_true


def main():
    rng = np.random.default_rng(7)
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

    # 测试集: 全新 μ0
    Xte_c, Yte_c = [], []
    Xte_r, Yte_r = [], []
    for _ in range(200):
        mu0 = float(rng.uniform(0.01, 0.8))
        e = float(rng.choice(ETA_GRID))
        l = float(rng.choice(LAM_GRID))
        n, over = run_control(mu0, e, l)
        y = math.log(n) if not over else LOG_PENALTY
        Xte_c.append(ctrl_feature(e, l, mu0))
        Yte_c.append(1.0 if over else 0.0)
        if not over:
            Xte_r.append(ctrl_feature(e, l, mu0))
            Yte_r.append(y)
    Xte_c = np.array(Xte_c); Yte_c = np.array(Yte_c)
    Xte_r = np.array(Xte_r); Yte_r = np.array(Yte_r)

    print("=== 分离建模 vs 混合回归 (反引力控制) ===")
    # A. 混合回归
    m_a = PlainNode(3)
    for i in range(0, 400, 50):
        Xb, Yb = Xt[i:i+50], Yt[i:i+50]
        if len(Xb) < 10: continue
        for _ in range(5):
            m_a.train_step(Xb, Yb)
    ma_test = float(np.mean(np.abs(m_a.predict_flat(torch.tensor(Xte_c, dtype=torch.float32)) - Yte_c * LOG_PENALTY - (1-Yte_c) * 3.0)))
    hit_a = [eval_hit(m_a, m0)[0] for m0 in [0.1, 0.3, 0.5, 0.7]]
    print(f"A 混合回归: 命中比 {[f'{h:.1f}×' for h in hit_a]} (均值 {np.mean(hit_a):.1f}×)")

    # B. 分离建模
    m_b = SeparateModel(3)
    for i in range(0, 400, 50):
        Xb, Yb = Xt[i:i+50], Yt[i:i+50]
        if len(Xb) < 10: continue
        train_separate(m_b, Xb, Yb, steps=5)
    # 分类准确率
    with torch.no_grad():
        clogit, _ = m_b(torch.tensor(Xte_c, dtype=torch.float32))
        pred_over = (torch.sigmoid(clogit).flatten() > 0.5).numpy()
    acc = float(np.mean(pred_over == Yte_c))
    # 回归 MAE (安全样本)
    if len(Xte_r) > 0:
        with torch.no_grad():
            _, rlog = m_b(torch.tensor(Xte_r, dtype=torch.float32))
        reg_mae = float(np.mean(np.abs(rlog.flatten().numpy() - np.array(Yte_r))))
    else:
        reg_mae = float('nan')
    hit_b = [eval_hit(m_b, m0)[0] for m0 in [0.1, 0.3, 0.5, 0.7]]
    print(f"B 分离建模: 分类准确率 {acc:.1%} | 安全区回归MAE {reg_mae:.3f} | "
          f"命中比 {[f'{h:.1f}×' for h in hit_b]} (均值 {np.mean(hit_b):.1f}×)")

    print("\n=== 判读 ===")
    print(f"命中比: 混合 {np.mean(hit_a):.1f}× vs 分离 {np.mean(hit_b):.1f}×")
    if np.mean(hit_b) < np.mean(hit_a) * 0.5:
        print("✓ 分离建模显著改善 → 目标双峰确实是瓶颈 (§2i 验证)")
    else:
        print("~ 改善有限")


if __name__ == "__main__":
    main()