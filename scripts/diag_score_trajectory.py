#!/usr/bin/env python3
"""score 轨迹依赖诊断 —— 同一设计在不同搜索轨迹里的 score 方差。

前提判断: 若同一设计(相同参数)在不同搜索批次中 score 方差大,
⟹ score 是"搜索轨迹属性"而非"单设计静态属性", 预测单设计score无稳定信号,
⟹ 必须转向引导搜索轨迹(而非块预测)。

方法: 从 registry 里取若干设计, 检查同一 params 是否多次出现(out/registry.jsonl
多次评估), 计算同一 params 的 score 分布(std/range)。

用法: python3 scripts/diag_score_trajectory.py
"""
from __future__ import annotations

import glob
import json
import pathlib

import numpy as np

ROOT = pathlib.Path(__file__).resolve().parents[1]


def main() -> int:
    # 收集所有 forge 输出里的 (params签名, score)
    sig_score = {}
    for reg in sorted(glob.glob(str(ROOT / "runs" / "oml_v4_*" / "out" / "registry.jsonl")) +
                       sorted(glob.glob(str(ROOT / "runs" / "oml_op_*" / "out" / "registry.jsonl")))):
        for line in open(reg):
            r = json.loads(line)
            if r.get("score") is None:
                continue
            p = r.get("params", {})
            sig = json.dumps([p.get("radius_m", []), p.get("z_m", []), p.get("current_A", [])],
                             sort_keys=True)
            sig_score.setdefault(sig, []).append(float(r["score"]))

    # 只看出现 ≥2 次的(同设计被评估多次)
    dup = {s: v for s, v in sig_score.items() if len(v) >= 2}
    if not dup:
        print("没有同一设计被评估≥2次 —— 需要设计一个重复评估实验")
        return 1

    print(f"同设计(≥2次) {len(dup)} 组, 各组评估次数分布:")
    ns = [len(v) for v in dup.values()]
    print(f"  次数: min={min(ns)} max={max(ns)} 均值={np.mean(ns):.1f}")

    # 每组 score 的 std / range(轨迹依赖强度)
    stds = [np.std(v) for v in dup.values()]
    ranges = [max(v) - min(v) for v in dup.values()]
    means = [np.mean(v) for v in dup.values()]
    print(f"\n=== score 轨迹依赖诊断 ===")
    print(f"同设计 score std: 均值 {np.mean(stds):.4f} 中位 {np.median(stds):.4f} "
          f"max {max(stds):.4f}")
    print(f"同设计 score range: 均值 {np.mean(ranges):.4f} 中位 {np.median(ranges):.4f} "
          f"max {max(ranges):.4f}")
    print(f"同设计 score 均值跨度: {min(means):.3f} ~ {max(means):.3f}")

    # 结论判据: 若同设计 range 相对其 score 量级不可忽略 → 轨迹依赖强
    scale = np.std(list(sig_score.values())) if False else np.std([v for vs in sig_score.values() for v in vs])
    rel = np.mean(ranges) / (scale + 1e-9)
    print(f"\nscore 全局 std: {scale:.4f}")
    print(f"同设计内 range / 全局 std = {rel:.2f}  ({'轨迹依赖强' if rel>0.3 else '轨迹依赖弱'})")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
