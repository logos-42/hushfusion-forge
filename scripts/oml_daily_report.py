#!/usr/bin/env python3
"""每日归档 —— 24h 持续运作的长期成果报告。

每天(由 supervisor/cron 触发)读 v4 趋势 + ckpt, 生成一份快照:
  pool 增长 / 回流累计 / select_lift 均值&>0比例 / 回流水误差趋势(模型是否变准)

用法: python3 scripts/oml_daily_report.py --out-dir /work/liuyuanjie/forge/artifacts/daily
"""
from __future__ import annotations

import argparse
import json
import pathlib
import time

import numpy as np


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--trend", default="/work/liuyuanjie/forge/artifacts/oml_daemon_trend_v4.jsonl")
    ap.add_argument("--out-dir", default="/work/liuyuanjie/forge/artifacts/daily")
    ap.add_argument("--ckpt-size", default="/work/liuyuanjie/forge/artifacts/oml_daemon_v4_ckpt.pkl")
    args = ap.parse_args()

    trend_p = pathlib.Path(args.trend)
    out_dir = pathlib.Path(args.out_dir)
    out_dir.mkdir(parents=True, exist_ok=True)
    date = time.strftime("%Y%m%d")

    if not trend_p.exists():
        print(f"趋势文件不存在: {trend_p}")
        return 1

    rows = [json.loads(l) for l in open(trend_p) if l.strip()]
    n = len(rows)
    if n == 0:
        print("趋势为空")
        return 1

    pool_sizes = [r["pool_size"] for r in rows]
    reflux = [r.get("new_reflux", 0) for r in rows]
    lifts = [r["select_lift"] for r in rows]
    errs = [r["reflux_err"] for r in rows if r.get("reflux_err") is not None]
    ops_ok = [r.get("forge_op") for r in rows]

    report = {
        "date": date,
        "rounds": n,
        "pool_start": pool_sizes[0],
        "pool_end": pool_sizes[-1],
        "pool_growth": pool_sizes[-1] - pool_sizes[0],
        "total_reflux": int(sum(reflux)),
        "select_lift_mean": float(np.mean(lifts)),
        "select_lift_pos_ratio": float(sum(1 for x in lifts if x > 0) / n),
        "forge_op_ok": int(sum(1 for x in ops_ok if x == 0)),
        "forge_op_total": len(ops_ok),
    }
    if len(errs) >= 4:
        report["reflux_err_first3"] = float(np.mean(errs[:3]))
        report["reflux_err_last3"] = float(np.mean(errs[-3:]))
        report["reflux_err_improve"] = float(np.mean(errs[:3]) - np.mean(errs[-3:]))

    out = out_dir / f"daily_{date}.json"
    out.write_text(json.dumps(report, indent=2))

    print(f"=== 每日归档 {date} ===")
    for k, v in report.items():
        print(f"  {k}: {v}")
    print(f"→ {out}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
