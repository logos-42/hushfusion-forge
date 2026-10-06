#!/usr/bin/env python3
"""v5/v6 配置推荐闭环 —— 定时归档快照(24h不间断的长期成果可见)。

读最新趋势 jsonl, 生成快照: 轮数 / warm-vs-cold / ek被选 / 最高score / 知识库规模。
由 supervisor_v5 每次循环间隙调用; 每次追加到 artifacts/daily/<date>.jsonl。

用法: python3 scripts/oml_daily_report_v5.py --trend PATH --kb PATH --out-dir PATH
"""
from __future__ import annotations

import argparse
import json
import pathlib
import time

import numpy as np


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--trend", default="/work/liuyuanjie/forge/artifacts/oml_daemon_trend_v5_latest.jsonl")
    ap.add_argument("--kb", default="/work/liuyuanjie/forge/runs/v5_knowledge/registry.jsonl")
    ap.add_argument("--out-dir", default="/work/liuyuanjie/forge/artifacts/daily")
    args = ap.parse_args()

    trend_p = pathlib.Path(args.trend)
    out_dir = pathlib.Path(args.out_dir)
    out_dir.mkdir(parents=True, exist_ok=True)

    if not trend_p.exists() or trend_p.stat().st_size == 0:
        print("趋势文件不存在或空, 跳过")
        return 0

    rows = [json.loads(l) for l in open(trend_p) if l.strip()]
    n = len(rows)
    if n == 0:
        return 0

    w = [r["best_score"] for r in rows if not r["is_cold"]]
    c = [r["best_score"] for r in rows if r["is_cold"]]
    ek = [r for r in rows if r.get("method") == "evolution_knowledge"]
    kb_size = 0
    kb_p = pathlib.Path(args.kb)
    if kb_p.exists():
        kb_size = sum(1 for _ in open(kb_p))

    snap = {
        "ts": time.strftime("%Y-%m-%dT%H:%M:%S"),
        "date": time.strftime("%Y%m%d"),
        "rounds": n,
        "warm_mean": float(np.mean(w)) if w else None,
        "cold_mean": float(np.mean(c)) if c else None,
        "delta": float(np.mean(w) - np.mean(c)) if w and c else None,
        "warm_pos": f"{sum(1 for x in w if x > 0)}/{len(w)}" if w else "0/0",
        "ek_selected": len(ek),
        "ek_best_ma": float(np.mean([x["best_score"] for x in ek])) if ek else None,
        "all_best": float(max(r["best_score"] for r in rows)),
        "knowledge_size": kb_size,
    }

    daily_f = out_dir / f"daily_{snap['date']}.jsonl"
    with open(daily_f, "a") as f:
        f.write(json.dumps(snap) + "\n")

    print(f"[daily] {snap['ts']} rounds={n} warm={snap['warm_mean']:.3f} "
          f"cold={snap['cold_mean']:.3f} Δ={snap['delta']:+.3f} "
          f"ek={snap['ek_selected']} best={snap['all_best']:.3f} kb={kb_size}", flush=True)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())