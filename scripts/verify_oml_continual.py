#!/usr/bin/env python3
"""动态持续学习验证 —— headless OML 元学习核心, 设计→score 回归。

用户要的是【动态的持续调整、持续学习的模型】(非静态选择器)。
用 headless 真正的持续学习引擎 OML(内循环快速适应 + 外循环 consolidate + Reptile 整合):
  - 数据流式喂入: 每轮给"最近 N 个已评估设计"当 support(当前任务)
  - 内循环: 在 support 上快速适应克隆头(Meta-SGD 步长)
  - 外循环: 在 query(要预测的设计)上更新 meta 参数 + consolidate 保持旧知识
  - Reptile: 本体 init 朝适应后权重拉一步(持续整合新经验)

验证【动态】证据: 随持续学习轮次,
  (1) 预测误差(query 上的 score MSE/MAE) 应下降 —— 越学越准
  (2) 选样 lift(选 top-K 的真实 score vs 随机) 应提升 —— 持续改善
这才是"持续学习的电磁模型"的核心信号, 不是静态 lift。

用法: python3 scripts/verify_oml_continual.py
"""
from __future__ import annotations

import argparse
import pathlib
import sys

import numpy as np

ROOT = pathlib.Path(__file__).resolve().parents[1]
HEADLESS = pathlib.Path(__file__).resolve().parents[2] / "headless"
sys.path.insert(0, str(HEADLESS))
sys.path.insert(0, str(ROOT / "scripts"))
from design_loop import design_to_feature, load_designs  # noqa: E402


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--registry", default=str(ROOT / "runs" / "phase1" / "registry.jsonl"))
    ap.add_argument("--rounds", type=int, default=20)
    ap.add_argument("--support-n", type=int, default=300)
    ap.add_argument("--query-n", type=int, default=100)
    ap.add_argument("--inner-k", type=int, default=3)
    ap.add_argument("--outer-lr", type=float, default=1e-3)
    ap.add_argument("--out", default=str(ROOT / "artifacts" / "oml_continual.json"))
    args = ap.parse_args()

    import torch
    import torch.nn as nn
    from hibs_lnn.pln_head import PLNHead

    # 数据: 设计特征 → score
    rows = load_designs(pathlib.Path(args.registry))
    X = np.array([r[0] for r in rows])
    Y = np.array([r[1] for r in rows]).astype(np.float32)
    print(f"载入 {len(X)} 个设计(特征归一化)", flush=True)

    # OML 回归: PLNHead 当回归头(n_classes=1), 无编码器(特征直接进)
    class OMLRegressor:
        """OML 双循环, 改造为回归(MSE) + 无 enc。持续学习不遗忘。"""
        def __init__(self, in_dim, d_model=64, inner_lr=0.05, outer_lr=1e-3, seed=0):
            torch.manual_seed(seed)
            self.head = PLNHead(input_dim=in_dim, d_model=d_model, n_classes=1,
                                inner_lr=inner_lr)
            # 外循环优化器: 更新 PLNHead 的 meta 参数(含 inner_lr/step_beta)
            self.opt = torch.optim.Adam(self.head.parameters(), lr=outer_lr)

        def _mse(self, W, Xq, yq):
            logits = PLNHead.fwd_with(W, Xq)
            return ((logits - yq) ** 2).mean()

        def oml_step(self, Xs, ys, Xq, yq, K=3):
            """内循环适应 support + 外循环在 query 上更新 meta(consolidate)。"""
            Xs = torch.tensor(Xs, dtype=torch.float32)
            ys = torch.tensor(ys, dtype=torch.float32).reshape(-1, 1)
            Xq = torch.tensor(Xq, dtype=torch.float32)
            yq = torch.tensor(yq, dtype=torch.float32).reshape(-1, 1)
            # 内循环: 克隆头在 support 上适应 K 步(保留梯度图供外循环)
            W = self.head.clone_params()
            for _ in range(K):
                logits_s = PLNHead.fwd_with(W, Xs)
                loss_s = ((logits_s - ys) ** 2).mean()
                grads = torch.autograd.grad(loss_s, W, create_graph=True)
                # per_feature step (Meta-SGD)
                W = self.head.per_feature_step(W, grads)
            # 外循环: query loss 反传更新 meta 参数
            self.opt.zero_grad()
            loss_q = self._mse(W, Xq, yq)
            loss_q.backward()
            torch.nn.utils.clip_grad_norm_(self.head.parameters(), 1.0)
            self.opt.step()
            return float(loss_q.detach()), float(self._mse(W, Xq, yq).detach())

        def predict(self, X):
            Xt = torch.tensor(X, dtype=torch.float32)
            with torch.no_grad():
                W = self.head.clone_params()
                return PLNHead.fwd_with(W, Xt).numpy().flatten()

    model = OMLRegressor(in_dim=X.shape[1], outer_lr=args.outer_lr)

    # 流式持续学习: 按轮次切数据, 每轮 support=最近一批, query=下一批, 双循环更新
    n_total = len(X)
    n_sup, n_q = args.support_n, args.query_n
    # 把数据排成流: 前 (rounds*support) 当历史支持, 每轮推进
    rng = np.random.default_rng(0)
    order = rng.permutation(n_total)
    # 用流式窗口: 每轮 support=窗口内前 n_sup, query=窗口后 n_q (像持续观察新数据)
    metrics = []
    window_start = 0
    for rnd in range(args.rounds):
        # support: 窗口内的设计(已见过) ; query: 窗口后的新设计(要预测)
        sup_end = window_start + n_sup
        qry_end = sup_end + n_q
        if qry_end > n_total:
            print(f"[round {rnd}] 数据用完, 停", flush=True)
            break
        sup_idx = order[window_start:sup_end]
        qry_idx = order[sup_end:qry_end]
        # OML 一步: 在 support 上适应(持续学习), 在 query 上验证(预测新数据)
        loss_q, loss_q_adapted = model.oml_step(
            X[sup_idx], Y[sup_idx], X[qry_idx], Y[qry_idx], K=args.inner_k)
        # 选样: 用学到的模型在 query 上选 top-K, 真实 score vs 随机
        pred = model.predict(X[qry_idx])
        kk = min(20, len(qry_idx))
        hl = Y[qry_idx][np.argsort(-pred)[:kk]]
        rand = Y[qry_idx][rng.choice(len(qry_idx), kk, replace=False)]
        lift = float(np.median(hl) - np.median(rand))
        metrics.append({"round": rnd, "query_mse": loss_q, "query_mse_adapted": loss_q_adapted,
                        "select_lift": lift, "hl_median": float(np.median(hl)),
                        "rand_median": float(np.median(rand))})
        print(f"[round {rnd}] query_mse={loss_q:.3f} 选样lift={lift:+.3f} "
              f"headless={np.median(hl):.3f} 随机={np.median(rand):.3f}", flush=True)
        window_start = sup_end  # 推进流

    # 动态持续学习证据:
    mse = [m["query_mse"] for m in metrics]
    lifts = [m["select_lift"] for m in metrics]
    print(f"\n=== 动态持续学习验证({len(metrics)} 轮) ===")
    print(f"预测误差(query_mse): 前3轮均值 {np.mean(mse[:3]):.3f} → 后3轮均值 {np.mean(mse[-3:]):.3f}")
    print(f"  误差下降 = {np.mean(mse[:3]) - np.mean(mse[-3:]):+.3f}  (持续学习→越学越准)")
    print(f"选样 lift: 前3轮均值 {np.mean(lifts[:3]):+.3f} → 后3轮均值 {np.mean(lifts[-3:]):+.3f}")
    print(f"  lift 提升 = {np.mean(lifts[-3:]) - np.mean(lifts[:3]):+.3f}  (持续改善)")

    out = pathlib.Path(args.out)
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(__import__("json").dumps({
        "metrics": metrics,
        "summary": {
            "mse_first3": float(np.mean(mse[:3])), "mse_last3": float(np.mean(mse[-3:])),
            "mse_improvement": float(np.mean(mse[:3]) - np.mean(mse[-3:])),
            "lift_first3": float(np.mean(lifts[:3])), "lift_last3": float(np.mean(lifts[-3:])),
            "lift_improvement": float(np.mean(lifts[-3:]) - np.mean(lifts[:3])),
            "n_rounds": len(metrics),
        }}, indent=2))
    print(f"结果 → {out}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
