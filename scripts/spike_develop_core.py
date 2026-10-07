#!/usr/bin/env python3
"""发育式持续学习 —— 核心模块 spike (V6.1/7 的最小实现)。

验证: 一个「需要 3 层」的任务 + 从 2 层起步 + 残差监控规则
       ⟹ 模型能否自动长到 3 层, 并达到「固定 3 层」的 val-loss 水平?

规则(发育控制器, 最小版):
  - 每 eval_every 步量 validation residual
  - 连续 stagnation_n 个周期 residual 不降 (relative 改善 < tol)
  - ⟹ 触发「复制-变异」: 在输出头前插入一层 (增长算符)
  - 记录发育日志: 何时长层 / 长层前后 val-loss

对照组:
  A. 固定 2 层 (浅层上限)
  B. 固定 3 层 (深层上限)
  C. 发育版 (从 2 层起, 自动长层) —— 应达到 B 水平

任务: y = sin(x1*x2*x3) + cos(x1*x2)  (嵌套乘积, 实测 3 层显著优于 2 层)

这是「模型本身重塑」的最小可证伪实验: 结构能按任务需求自我调整。
"""
import numpy as np
import torch
import torch.nn as nn

torch.manual_seed(0)
np.random.seed(0)


def make_data(n=6000, task='deep'):
    x = np.random.uniform(-1.5, 1.5, size=(n, 3)).astype(np.float32)
    if task == 'deep':
        y = np.sin(x[:, 0] * x[:, 1] * x[:, 2]) + np.cos(x[:, 0] * x[:, 1])
    else:
        y = np.sin(x[:, 0] * x[:, 1]) + np.cos(x[:, 2])
    return x, y.astype(np.float32).reshape(-1, 1)


class DevelopmentalMLP(nn.Module):
    """可发育 MLP: 初始 hidden=给定; grow() 在输出头前插入一层 (复制-变异)。

    增长保持输入/输出维度; 新层宽度 = old 的 out_features(继承, 变异半宽)。
    """

    def __init__(self, in_dim, hidden=(8,)):
        super().__init__()
        self.blocks = nn.ModuleList()
        dims = [in_dim] + list(hidden) + [1]
        for i in range(len(dims) - 1):
            self.blocks.append(nn.Linear(dims[i], dims[i + 1]))
            if i < len(dims) - 2:
                self.blocks.append(nn.Tanh())
        self.hiddens = list(hidden)

    def n_hidden(self):
        return len(self.hiddens)

    def add_layer(self, width=None):
        """输出头前插一层 Linear+Tanh (增长算符)。新层宽 = 待替换层宽的一半(变异)。"""
        out_lin = self.blocks[-1]  # 输出 Linear
        w = width or max(4, out_lin.in_features // 2)
        new_lin = nn.Linear(out_lin.in_features, w)
        nn.init.xavier_uniform_(new_lin.weight)
        nn.init.zeros_(new_lin.bias)
        # 输出层重接
        out_lin.in_features = w
        s = 1.0 / np.sqrt(w)
        out_lin.weight = nn.Parameter(torch.randn(out_lin.out_features, w) * s)
        out_lin.bias = nn.Parameter(torch.zeros(out_lin.out_features))
        # 插入位置: 输出层之前 [..., new_lin, new_tanh, out_lin]
        self.blocks.insert(len(self.blocks) - 1, new_lin)
        self.blocks.insert(len(self.blocks) - 1, nn.Tanh())
        self.hiddens.append(w)

    def forward(self, x):
        h = x
        for b in self.blocks:
            h = b(h)
        return h


def train(model, x, y, epochs=600, lr=1e-2, seed=0, eval_every=50,
          develop=False, stagnation_n=2, tol=1e-3):
    """训练 + (可选)发育。返回 (best_val, 发育日志)。"""
    torch.manual_seed(seed)
    n = len(x)
    idx = np.random.RandomState(seed).permutation(n)
    tr, va = idx[:int(0.8 * n)], idx[int(0.8 * n):]
    xt, yt = torch.tensor(x[tr]), torch.tensor(y[tr])
    xv, yv = torch.tensor(x[va]), torch.tensor(y[va])

    opt = torch.optim.Adam(model.parameters(), lr=lr)
    best_val = float('inf')
    val_hist = []
    stagnant = 0
    dev_log = []
    prev_val = None

    for ep in range(epochs):
        opt.zero_grad()
        loss = ((model(xt) - yt) ** 2).mean()
        loss.backward()
        opt.step()
        if (ep + 1) % eval_every == 0:
            with torch.no_grad():
                vloss = ((model(xv) - yv) ** 2).mean().item()
            val_hist.append(vloss)
            best_val = min(best_val, vloss)

            if develop and prev_val is not None:
                rel_imp = (prev_val - vloss) / abs(prev_val) if prev_val != 0 else 0
                if rel_imp < tol:  # 没改善
                    stagnant += 1
                else:
                    stagnant = 0
                if stagnant >= stagnation_n:
                    model.add_layer()
                    dev_log.append({"ep": ep + 1, "action": "grow",
                                    "n_hidden": model.n_hidden(),
                                    "val_before": prev_val})
                    print(f"    [发育] ep={ep+1} val={prev_val:.5f} 未改善, 长层 → "
                          f"{model.n_hidden()} 隐藏层", flush=True)
                    stagnant = 0
                    opt = torch.optim.Adam(model.parameters(), lr=lr)  # 新参数重建
            prev_val = vloss
    return best_val, dev_log, model.n_hidden()


print("=== 发育核心 spike: deep 任务 (3 seeds) ===")
print(f"{'配置':<16} {'val-MSE(median)':<16} {'长层?':<8}")
res = {}
for label, dev, hidden in [
    ("对照 2层[8]", False, (8,)),
    ("对照 3层[8,4]", False, (8, 4)),
    ("发育(2层起)", True, (8,)),
]:
    vals, logs, ns = [], [], []
    for s in range(3):
        if not dev:
            torch.manual_seed(s)
            model = DevelopmentalMLP(3, hidden=hidden)
            x, y = make_data(task='deep')
            bv, lg, nh = train(model, x, y, seed=s, develop=False)
        else:
            torch.manual_seed(s)
            model = DevelopmentalMLP(3, hidden=(8,))
            x, y = make_data(task='deep')
            bv, lg, nh = train(model, x, y, seed=s, develop=True)
        vals.append(bv)
        if dev:
            logs.append(lg)
            ns.append(nh)
    med = float(np.median(vals))
    res[label] = med
    extra = f"n_hidden={ns}" if dev else ""
    print(f"{label:<16} {med:.6f}   {extra}")

print("\n=== 发育判读 ===")
l2, l3, dev_r = res["对照 2层[8]"], res["对照 3层[8,4]"], res["发育(2层起)"]
print(f"  2层: {l2:.6f}  3层: {l3:.6f}  发育: {dev_r:.6f}")
print(f"  发育 vs 固定3层: 差 {(dev_r - l3) / l3 * 100:+.1f}%")
print(f"  发育 vs 固定2层: 改善 {(l2 - dev_r) / l2 * 100:+.1f}%")
if dev_r <= l3 * 1.15:  # 15% 内
    print("  ✓ 发育版达到固定3层水平 → 「自动长层」机制成立")
else:
    print("  ✗ 发育版未达3层水平 —— 增长规则需调整")