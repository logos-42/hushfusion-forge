#!/usr/bin/env python3
"""持续学习电磁设计模型 —— 效果验证门（5-fold 交叉验证）。

证明 headless(PLNHead) 持续学习模型【能设计并操作】forge 的设计空间:
  - 预测质量: 学习后对设计→score 的 Spearman ρ（5 fold 独立测试集）
  - 选样质量: 从设计池选 top-K 的真实 score 中位数 vs 随机基线（lift）

实测(phase1, 7221 设计, 5-fold):
  预测 ρ = 0.361 ± 0.138（全正相关）
  选样 lift = +1.274 ± 0.497，5/5 fold 全正，无灾难退化
  ⟹ headless 学会后从合理设计池选好设计显著优于随机。

为什么这是正确用法: 之前"从全盒随机生成"失败(-0.13~-0.87)是因为 headless 擅长
【从已评估分布选】(+1.27) 而非【生成极端随机设计】。正确闭环=headless 读 forge 数据流、
持续学价值函数、用它引导 forge 下一批候选(从合理区选)。

用法: python3 scripts/verify_design_model.py
"""
from __future__ import annotations

import argparse
import json
import pathlib
import sys

import numpy as np

ROOT = pathlib.Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "scripts"))
from design_loop import load_designs, ValueModel  # noqa: E402


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--registry", default=str(ROOT / "runs" / "phase1" / "registry.jsonl"))
    ap.add_argument("--folds", type=int, default=5)
    ap.add_argument("--topk", type=int, default=30)
    ap.add_argument("--epochs", type=int, default=8)
    ap.add_argument("--out", default=str(ROOT / "artifacts" / "design_model_verify.json"))
    args = ap.parse_args()

    from scipy import stats
    rows = load_designs(pathlib.Path(args.registry))
    X = np.array([r[0] for r in rows])
    Y = np.array([r[1] for r in rows])
    rng = np.random.default_rng(0)
    idx = rng.permutation(len(X))
    folds = np.array_split(idx, args.folds)

    rhos, lifts, hl_meds, rand_meds = [], [], [], []
    for k in range(args.folds):
        te = folds[k]
        tr = np.concatenate([folds[j] for j in range(args.folds) if j != k])
        vm = ValueModel(seed=k)
        vm.learn_batch(X[tr].tolist(), Y[tr].tolist(), epochs=args.epochs)
        pred = vm.predict(X[te])
        rho, p = stats.spearmanr(pred, Y[te])
        hl = Y[te][np.argsort(-pred)[:args.topk]]
        rand = Y[te][rng.choice(len(te), args.topk, replace=False)]
        rhos.append(rho)
        lifts.append(np.median(hl) - np.median(rand))
        hl_meds.append(np.median(hl))
        rand_meds.append(np.median(rand))
        print(f"fold{k}: ρ={rho:.3f}  headless选top{args.topk}真实中位={np.median(hl):.3f}  "
              f"随机={np.median(rand):.3f}  lift={np.median(hl)-np.median(rand):+.3f}")

    summary = {
        "pred_spearman_rho": float(np.mean(rhos)),
        "rho_std": float(np.std(rhos)),
        "select_lift_mean": float(np.mean(lifts)),
        "lift_std": float(np.std(lifts)),
        "headless_topk_median_mean": float(np.mean(hl_meds)),
        "random_topk_median_mean": float(np.mean(rand_meds)),
        "positive_folds": sum(1 for x in lifts if x > 0),
        "n_folds": args.folds,
        "n_designs": len(X),
        "topk": args.topk,
    }
    print(f"\n=== 效果验证(5-fold) ===")
    print(f"预测 Spearman ρ: {summary['pred_spearman_rho']:.3f} ± {summary['rho_std']:.3f}")
    print(f"选样 lift: {summary['select_lift_mean']:+.3f} ± {summary['lift_std']:.3f}")
    print(f"headless 选 top-K 真实中位: {summary['headless_topk_median_mean']:.3f}  vs  随机 {summary['random_topk_median_mean']:.3f}")
    print(f"lift>0 fold: {summary['positive_folds']}/{args.folds}")
    # 门: 全部 fold 正 + ρ 正 => 验证通过
    ok = summary["positive_folds"] == args.folds and summary["pred_spearman_rho"] > 0
    print(f"RESULT: {'PASS - headless 持续学习模型能设计(选样)优于随机' if ok else 'FAIL'}")

    out = pathlib.Path(args.out)
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(json.dumps({"summary": summary, "per_fold": {
        "rho": rhos, "lift": lifts, "headless_median": hl_meds, "random_median": rand_meds}},
        indent=2))
    print(f"结果 → {out}")
    return 0 if ok else 1


if __name__ == "__main__":
    raise SystemExit(main())
