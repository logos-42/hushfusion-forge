#!/usr/bin/env python3
"""OML 持续学习核心 —— 动态持续调整的设计价值模型(可导入)。

用户要【动态的持续调整、持续学习的模型】。核心是 headless OML 元学习:
  - 内循环: 在 support(最近已评估设计)上快速适应克隆头(Meta-SGD)
  - 外循环: 在 query(要预测的设计)上更新 meta 参数 + consolidate(不遗忘)
  - 每轮读新数据 → 持续调整 → 越学越准(query mse↓) + 选样越好(lift↑)

用法: from oml_continual_core import OMLDesignLearner
"""
from __future__ import annotations

import numpy as np


class OMLDesignLearner:
    """OML 双循环回归, 持续学习"设计→score"。无编码器(特征直接进)。"""

    def __init__(self, in_dim: int, d_model: int = 64, inner_lr: float = 0.05,
                 outer_lr: float = 1e-3, seed: int = 0):
        import torch
        from hibs_lnn.pln_head import PLNHead
        torch.manual_seed(seed)
        self.in_dim = in_dim
        self.head = PLNHead(input_dim=in_dim, d_model=d_model, n_classes=1, inner_lr=inner_lr)
        self.opt = torch.optim.Adam(self.head.parameters(), lr=outer_lr)

    def _mse(self, W, Xq, yq):
        from hibs_lnn.pln_head import PLNHead
        return ((PLNHead.fwd_with(W, Xq) - yq) ** 2).mean()

    def oml_step(self, Xs, ys, Xq, yq, K: int = 3):
        """内循环适应 support + 外循环在 query 更新 meta(consolidate)。返回 (query_mse 前, 后)。"""
        import torch
        from hibs_lnn.pln_head import PLNHead
        Xs = torch.tensor(Xs, dtype=torch.float32)
        ys = torch.tensor(ys, dtype=torch.float32).reshape(-1, 1)
        Xq = torch.tensor(Xq, dtype=torch.float32)
        yq = torch.tensor(yq, dtype=torch.float32).reshape(-1, 1)
        # 内循环: 克隆头在 support 上适应 K 步(保留梯度图供外循环)
        W = self.head.clone_params()
        for _ in range(K):
            loss_s = ((PLNHead.fwd_with(W, Xs) - ys) ** 2).mean()
            grads = torch.autograd.grad(loss_s, W, create_graph=True)
            W = self.head.per_feature_step(W, grads)
        mse_adapted = float(self._mse(W, Xq, yq).detach())
        # 外循环: query loss 反传更新 meta 参数(consolidate)
        self.opt.zero_grad()
        loss_q = self._mse(W, Xq, yq)
        loss_q.backward()
        torch.nn.utils.clip_grad_norm_(self.head.parameters(), 1.0)
        self.opt.step()
        return float(loss_q.detach()), mse_adapted

    def predict(self, X):
        import torch
        from hibs_lnn.pln_head import PLNHead
        Xt = torch.tensor(np.array(X, dtype=float), dtype=torch.float32)
        with torch.no_grad():
            W = self.head.clone_params()
            return PLNHead.fwd_with(W, Xt).numpy().flatten()

    def select_topk(self, X, k):
        """在候选集上选预测价值 top-k(利用当前持续学到的价值)。"""
        pred = self.predict(X)
        return np.argsort(-pred)[:k]
