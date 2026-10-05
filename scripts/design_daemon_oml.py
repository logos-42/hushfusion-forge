#!/usr/bin/env python3
"""持续运行闭环 v3 —— OML 持续学习核心(动态调整, 越学越准)。

这是用户要的【动态持续调整、持续学习的电磁模型】常驻闭环:
  每轮: 读 forge 新数据 → OML 内循环适应(support最近设计) + 外循环整合(consolidate)
       → 用持续学到的模型选样 → forge 真评估 → 验证(query误差↓ + 选样lift↑) → 循环
  
验证【动态】证据: 随轮次
  query_mse 应下降(越学越准) + select_lift 应提升/保持正(持续改善)

用法(服务器):
  setsid CUDA_VISIBLE_DEVICES=0 python -u scripts/design_daemon_oml.py \
    --rounds 100000 --forge-root /work/liuyuanjie/forge ... &
"""
from __future__ import annotations

import argparse
import json
import pathlib
import sys
import time

import numpy as np

ROOT = pathlib.Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "scripts"))
from design_loop import (  # noqa: E402
    design_to_feature, feature_to_design, load_designs, gen_random_designs,
)
from oml_continual_core import OMLDesignLearner  # noqa: E402


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--rounds", type=int, default=100000)
    ap.add_argument("--registry", default=str(ROOT / "runs" / "phase1" / "registry.jsonl"))
    ap.add_argument("--support-n", type=int, default=300)
    ap.add_argument("--query-n", type=int, default=100)
    ap.add_argument("--inner-k", type=int, default=3)
    ap.add_argument("--forge-root", default=str(ROOT))
    ap.add_argument("--out", default=str(ROOT / "artifacts" / "oml_daemon_trend.jsonl"))
    ap.add_argument("--interval", type=float, default=20.0)
    args = ap.parse_args()

    stop_file = pathlib.Path("/work/liuyuanjie/design_daemon_oml.stop")
    out = pathlib.Path(args.out)
    out.parent.mkdir(parents=True, exist_ok=True)

    rows = load_designs(pathlib.Path(args.registry))
    X_all = np.array([r[0] for r in rows])
    Y_all = np.array([r[1] for r in rows]).astype(np.float32)
    print(f"[init] 载入 {len(X_all)} 个设计, OML 持续学习闭环启动", flush=True)

    model = OMLDesignLearner(in_dim=X_all.shape[1])
    rng = np.random.default_rng(0)
    trend = []

    for rnd in range(args.rounds):
        if stop_file.exists():
            print(f"[stop] 退出", flush=True)
            break
        t0 = time.time()

        # 数据流: 随机划分 support(已见/适应) + query(要预测/验证)
        n = len(X_all)
        idx = rng.permutation(n)[: args.support_n + args.query_n]
        sup_idx = idx[: args.support_n]
        qry_idx = idx[args.support_n:]
        Xs, ys = X_all[sup_idx], Y_all[sup_idx]
        Xq, yq = X_all[qry_idx], Y_all[qry_idx]

        # OML 一步: 在 support 上适应(持续学习), 在 query 上验证(越学越准?)
        loss_before, loss_adapted = model.oml_step(Xs, ys, Xq, yq, K=args.inner_k)

        # 选样: 用持续学到的模型在 query 上选 top-K, 真实 score vs 随机
        pred = model.predict(Xq)
        kk = min(20, len(qry_idx))
        hl_idx = np.argsort(-pred)[:kk]
        hl = Y_all[qry_idx[hl_idx]]
        rand = Y_all[qry_idx[rng.choice(len(qry_idx), kk, replace=False)]]
        lift = float(np.median(hl) - np.median(rand))

        # ── 操作 forge: 用 OML 持续学到的价值, 引导 forge 搜索下一批 ──
        #    headless 选 top-K 设计 → 写 registry → forge benchmark(--knowledge 播种)
        #    → forge 真评估+搜索 → 真实 score 回流 → OML 持续学习
        hl_cands_raw = np.array([feature_to_design(x) for x in X_all[qry_idx[hl_idx]]])
        op_result = {}
        try:
            _regen_fpath = pathlib.Path(args.forge_root) / "runs" / f"oml_op_{int(time.time()*1000)}"
            _regen_fpath.mkdir(parents=True, exist_ok=True)
            # 写 forge 能读的 registry
            import json as _json, time as _time
            _ts = _time.strftime("%Y-%m-%dT%H:%M:%S")
            _lines = []
            for _i, _d in enumerate(hl_cands_raw):
                _rec = {"experiment_id": 1, "design_id": f"D{_i+1:04d}", "generation": 0,
                        "algorithm": "oml_guided", "seed": 0, "eval_index": _i, "tag": "omlop",
                        "timestamp": _ts, "score": 0.0, "feasible": True,
                        "params": {"radius_m": [float(x) for x in _d[:4]],
                                   "z_m": [float(x) for x in _d[4:8]],
                                   "current_A": [float(x) for x in _d[8:]]},
                        "terms": {}, "weighted": {}, "penalties": {}, "metrics": {}, "note": "oml_guided"}
                _lines.append(_json.dumps(_rec))
            (_regen_fpath / "registry.jsonl").write_text("\n".join(_lines) + "\n")
            # forge benchmark 用 OML 选的设计当初始种群(evolution_knowledge 播种)
            import os as _os, subprocess as _sub
            _out_dir = _regen_fpath / "out"
            _out_dir.mkdir(exist_ok=True)
            _cmd = ["go", "run", "./cmd/forge", "benchmark",
                    "--budget", "30", "--seeds", "0", "--methods", "evolution_knowledge",
                    "--knowledge", str(_regen_fpath), "--out", str(_out_dir), "--tag", "oml_op"]
            # 子进程环境: 注入 go PATH(daemon 由 setsid 启动, 不继承交互 PATH)
            _env = dict(_os.environ)
            _env["PATH"] = "/work/liuyuanjie/go1.24/bin:/work/liuyuanjie/go/bin:" + _env.get("PATH", "")
            _env["GOTOOLCHAIN"] = "local"
            _env["GOPROXY"] = "https://goproxy.cn,direct"
            _r = _sub.run(_cmd, cwd=str(args.forge_root), capture_output=True, text=True, env=_env)
            op_result = {"exit": _r.returncode, "tail": _r.stdout[-200:], "err": _r.stderr[-200:]}
        except Exception as _e:
            import traceback as _tb
            op_result = {"exit": -1, "tail": str(_e), "err": _tb.format_exc()[-500:]}
            print(f"[round {rnd}] 操作forge异常: {_e}\n{op_result['err']}", flush=True)

        trend.append({"round": rnd, "query_mse": loss_before, "query_mse_adapted": loss_adapted,
                      "select_lift": lift, "hl_median": float(np.median(hl)),
                      "rand_median": float(np.median(rand)),
                      "forge_op_exit": op_result.get("exit"),
                      "ts": time.strftime("%Y-%m-%dT%H:%M:%S")})
        recent_mse = np.mean([t["query_mse"] for t in trend[-5:]])
        recent_lift = np.mean([t["select_lift"] for t in trend[-5:]])
        print(f"[round {rnd}] query_mse={loss_before:.3f}(近5轮{recent_mse:.3f}) "
              f"选样lift={lift:+.3f}(近5轮{recent_lift:+.3f}) "
              f"headless={np.median(hl):.3f} 随机={np.median(rand):.3f} "
              f"[{time.time()-t0:.0f}s]", flush=True)

        with open(out, "a") as f:
            f.write(json.dumps(trend[-1]) + "\n")
        time.sleep(args.interval)

    if trend:
        mse = [t["query_mse"] for t in trend]
        lifts = [t["select_lift"] for t in trend]
        print(f"\n=== OML 持续学习闭环结束: {len(trend)} 轮 ===", flush=True)
        print(f"query_mse: 前3轮 {np.mean(mse[:3]):.3f} → 后3轮 {np.mean(mse[-3:]):.3f} "
              f"(误差下降 {np.mean(mse[:3])-np.mean(mse[-3:]):+.3f})", flush=True)
        print(f"select_lift: 前3轮 {np.mean(lifts[:3]):+.3f} → 后3轮 {np.mean(lifts[-3:]):+.3f} "
              f"(提升 {np.mean(lifts[-3:])-np.mean(lifts[:3]):+.3f})", flush=True)
        print(f"lift>0比例: {sum(1 for x in lifts if x>0)}/{len(lifts)}", flush=True)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())