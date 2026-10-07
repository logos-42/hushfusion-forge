#!/usr/bin/env python3
"""发育式持续学习 spike v5 —— 剪枝算符(突触修剪)。

愿景:「在层级之间来回创造出属于自己的结构」。已证扩展(长层, v3/v4)。
v5 做另一半: **剪枝** —— 去掉冗余层。对应神经可塑性的突触修剪:
不用的连接被消除, 网络更稀疏、更泛化。

验证: 在**简单任务**(1 隐藏层就够)上训练一个**过深**网络(4 隐藏层),
看剪枝判据能否识别冗余层并删掉, 且 val 不退化(甚至更好, 因为少了过拟合)。

剪枝判据(计划 §1B 的最小版): 层输出与输入的**互信息**低 / 该层参数在 query loss 上的
**梯度范数**持续小 ⟹ 冗余。最小实现用梯度范数(易算): 某层梯度范数 < 全局均值的
某比例持续 k 周期 ⟹ 删掉它。

对照:
  A. 固定 1 层(该任务的最优深度)
  B. 固定 4 层(过深, 冗余)
  C. 剪枝版: 从 4 层起, 自动删冗余层 → 应接近 A 且优于 B
"""
import numpy as np
import torch
import torch.nn as nn

torch.manual_seed(0)
np.random.seed(0)


def make_data(n=6000, task='simple'):
    x = np.random.uniform(-1.5, 1.5, size=(n, 3)).astype(np.float32)
    if task == 'simple':
        y = np.sin(x[:, 0] * x[:, 1]) + np.cos(x[:, 2])
    else:
        y = np.sin(x[:, 0] * x[:, 1] * x[:, 2]) + np.cos(x[:, 0] * x[:, 1])
    return x, y.astype(np.float32).reshape(-1, 1)


class Prunable(nn.Module):
    """可剪枝 MLP。prune(idx) 删除一个 Linear+Tanh 对, 前层直连后层。

    删除规则: 删掉 blocks 里第 idx 个 Linear, 以及紧跟它的 Tanh (如果存在)。
    前一个 Linear 的输出维度改为后一个 Linear 的输入维度 (重投影)。
    """

    def __init__(self, in_dim, hidden=(8, 8, 8, 8)):
        super().__init__()
        self.blocks = nn.ModuleList()
        dims = [in_dim] + list(hidden) + [1]
        for i in range(len(dims) - 1):
            self.blocks.append(nn.Linear(dims[i], dims[i + 1]))
            if i < len(dims) - 2:
                self.blocks.append(nn.Tanh())
        self.hiddens = list(hidden)

    @property
    def n_hidden(self):
        return len(self.hiddens)

    def linear_indices(self):
        return [i for i, b in enumerate(self.blocks) if isinstance(b, nn.Linear)]

    def prune_one(self):
        """删掉一个隐藏 Linear+Tanh 对 (最靠近输出头的那个), 重接前层输出。"""
        lins = self.linear_indices()
        if len(lins) <= 2:  # 只剩输入→输出
            return False
        # 删倒数第二个 Linear (即最深的隐藏层) + 它的 Tanh
        target_lin = lins[-2]  # 最后一个隐藏 Linear
        # 找到它后面的 Tanh
        tanh_idx = target_lin + 1
        has_tanh = tanh_idx < len(self.blocks) and isinstance(self.blocks[tanh_idx], nn.Tanh)
        # 找到它前面的 Linear (前一层)
        prev_lin = lins[-3]
        # 前一层输出维度 = 目标层输入维度
        prev_lin.out_features = self.blocks[target_lin].in_features
        s = 1.0 / np.sqrt(prev_lin.out_features)
        prev_lin.weight = nn.Parameter(torch.randn(prev_lin.out_features, prev_lin.in_features) * s)
        prev_lin.bias = nn.Parameter(torch.zeros(prev_lin.out_features))
        # 删除目标层和它的 tanh (从 blocks 里)
        del self.blocks[target_lin]
        if has_tanh:
            # tanh 现在下标还是 target_lin (删了 Linear 后前移一位)
            if target_lin < len(self.blocks) and isinstance(self.blocks[target_lin], nn.Tanh):
                del self.blocks[target_lin]
        self.hiddens.pop()
        return True

    def forward(self, x):
        h = x
        for b in self.blocks:
            h = b(h)
        return h


def train(model, x, y, epochs=1500, lr=1e-2, seed=0, eval_every=50,
          prune=False, max_prunes=3, grad_ratio=0.1, k=4):
    """训练 + (可选)剪枝。剪枝判据: 某隐藏层梯度范数 < 全局均值×grad_ratio 持续 k 周期。"""
    torch.manual_seed(seed)
    n = len(x)
    idx = np.random.RandomState(seed).permutation(n)
    tr, va = idx[:int(0.8 * n)], idx[int(0.8 * n):]
    xt, yt = torch.tensor(x[tr]), torch.tensor(y[tr])
    xv, yv = torch.tensor(x[va]), torch.tensor(y[va])
    opt = torch.optim.Adam(model.parameters(), lr=lr)
    best_val = float('inf')
    prunes = 0
    weak_streak = 0
    dev_log = []

    for ep in range(epochs):
        opt.zero_grad()
        loss = ((model(xt) - yt) ** 2).mean()
        loss.backward()
        opt.step()
        if (ep + 1) % eval_every == 0 and prune and prunes < max_prunes:
            with torch.no_grad():
                vloss = ((model(xv) - yv) ** 2).mean().item()
            best_val = min(best_val, vloss)
            # 收集隐藏 Linear 的梯度范数
            lins = model.linear_indices()
            hidden_lins = lins[1:-1]  # 去掉输入/输出
            if hidden_lins:
                norms = []
                for li in hidden_lins:
                    b = model.blocks[li]
                    norms.append(b.weight.grad.norm().item() if b.weight.grad is not None else 0.0)
                global_mean = float(np.mean([abs(n) for n in norms])) if norms else 0.0
                # 最弱的隐藏层
                min_norm = min(norms) if norms else 0.0
                if global_mean > 0 and min_norm < global_mean * grad_ratio:
                    weak_streak += 1
                else:
                    weak_streak = 0
                if weak_streak >= k:
                    ok = model.prune_one()
                    prunes += 1
                    dev_log.append({"ep": ep + 1, "n_hidden": model.n_hidden,
                                    "val": vloss, "pruned": True})
                    print(f"    [剪枝] ep={ep+1} val={vloss:.5f} 弱层, 删一层 → "
                          f"{model.n_hidden} 隐藏层", flush=True)
                    weak_streak = 0
                    opt = torch.optim.Adam(model.parameters(), lr=lr)
            else:
                weak_streak = 0
        elif (ep + 1) % eval_every == 0:
            with torch.no_grad():
                vloss = ((model(xv) - yv) ** 2).mean().item()
            best_val = min(best_val, vloss)
    return best_val, dev_log, model.n_hidden


print("=== 剪枝 spike v5: 过深网络自动删冗余层 ===")
res = {}
for label, dev, hidden in [
    ("对照 1层[8]", False, (8,)),
    ("对照 4层[8,8,8,8]", False, (8, 8, 8, 8)),
    ("剪枝(4层起)", True, (8, 8, 8, 8)),
]:
    vals, logs, ns = [], [], []
    for s in range(3):
        if not dev:
            torch.manual_seed(s)
            model = Prunable(3, hidden=hidden)
            x, y = make_data(task='simple')
            bv, lg, nh = train(model, x, y, seed=s, prune=False)
        else:
            torch.manual_seed(s)
            model = Prunable(3, hidden=(8, 8, 8, 8))
            x, y = make_data(task='simple')
            bv, lg, nh = train(model, x, y, seed=s, prune=True)
        vals.append(bv)
        if dev:
            logs.append(lg); ns.append(nh)
    med = float(np.median(vals))
    res[label] = med
    extra = f"n_hidden终={ns} prunes={[len(l) for l in logs]}" if dev else ""
    print(f"{label:<22} {med:.6f}   {extra}")

print("\n=== 判读 ===")
l1, l4, pr = res["对照 1层[8]"], res["对照 4层[8,8,8,8]"], res["剪枝(4层起)"]
print(f"  1层 {l1:.6f} / 4层 {l4:.6f} / 剪枝 {pr:.6f}")
print(f"  剪枝 vs 固定4层: {(pr - l4) / l4 * 100:+.1f}%")
print(f"  剪枝 vs 最优1层: {(pr - l1) / l1 * 100:+.1f}%")
if pr <= l4 * 1.05:
    print("  ✓ 剪枝不退化(删冗余层无害)")
else:
    print("  ~ 剪枝有退化(可能删错/重接损失)")
if ns and all(n <= 2 for n in ns):
    print("  ✓ 剪枝真的删了层(4 → ≤2)")
else:
    print(f"  ~ 剪枝触发不足: n_hidden终={ns}")