#!/usr/bin/env python3
"""持续运行闭环 v4 —— OML 持续学习核心, 真实回流 + 严格验证。

用户三条批评(已接受):
  1. 验证不足(只看lift vs随机) ⟹ 改: 多指标 + 真正的 out-of-sample 泛化
  2. 核心未达标(模型没变好) ⟹ 根因: forge 真实score从未回流, 一直学静态数据
     ⟹ 修: 每轮读 forge out/registry.jsonl, (参数,score) 累积进池, 模型在累积池上持续学
  3. 缺24h运作 ⟹ 修: checkpoint持久化 + 生成长期报告

闭环(v4):
  每轮:
    1. 从累积池抽样 support+query, OML 双循环更新
    2. 用模型选 top-K (exploit/ucb/diverse 可配)
    3. 写候选 registry → forge benchmark 真评估
    4. 读回 out/registry.jsonl 的真实 score → 累积池 + 回流数据
    5. 验证: 在【未见过的回流新增数据】上算预测误差(真泛化), 跨轮追踪是否下降

  "模型越来越好"= 累积池增大时, 对新增(未见)数据的预测误差下降 / 选样质量提升
  这是真正的持续学习改进证据, 不是静态lift。

用法(服务器):
  setsid CUDA_VISIBLE_DEVICES=0 python -u scripts/design_daemon_oml.py \
    --rounds 100000 --forge-root /work/liuyuanjie/forge \
    --out /work/liuyuanjie/forge/artifacts/oml_daemon_trend_v4.jsonl --interval 30 &
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
    design_to_feature, feature_to_design, load_designs,
)
from oml_continual_core import OMLDesignLearner  # noqa: E402


def _params_to_feature(p):
    """forge registry 一条的 params → 12维特征(纯Python list)。"""
    return [float(v) for v in design_to_feature(
        list(p["radius_m"]) + list(p.get("z_m", [])) + list(p["current_A"]))]

def _to_plain(x):
    """任意设计特征 → 纯Python float list (JSON-safe)。"""
    return [float(v) for v in x]


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--rounds", type=int, default=100000)
    ap.add_argument("--registry", default=str(ROOT / "runs" / "phase1" / "registry.jsonl"))
    ap.add_argument("--support-n", type=int, default=300)
    ap.add_argument("--query-n", type=int, default=60)
    ap.add_argument("--inner-k", type=int, default=3)
    ap.add_argument("--topk", type=int, default=20)
    ap.add_argument("--strategy", default="exploit", choices=["exploit", "ucb", "diverse"])
    ap.add_argument("--forge-root", default=str(ROOT))
    ap.add_argument("--out", default=str(ROOT / "artifacts" / "oml_daemon_trend_v4.jsonl"))
    ap.add_argument("--interval", type=float, default=30.0)
    ap.add_argument("--ckpt", default=str(ROOT / "artifacts" / "oml_daemon_v4_ckpt.pkl"))
    args = ap.parse_args()

    stop_file = pathlib.Path("/work/liuyuanjie/design_daemon_oml.stop")
    out = pathlib.Path(args.out)
    out.parent.mkdir(parents=True, exist_ok=True)
    ckpt = pathlib.Path(args.ckpt)

    # ── 累积池(初始化自 registry, 之后不断回流 forge 真实score) ──
    import pickle
    pool_X, pool_Y, seen_signatures = [], [], set()
    if ckpt.exists():
        with open(ckpt, "rb") as f:
            pool_X, pool_Y, seen_signatures = pickle.load(f)
        print(f"[ckpt] 恢复累积池 {len(pool_X)} 条", flush=True)
    else:
        rows = load_designs(pathlib.Path(args.registry))
        pool_X = [_to_plain(r[0]) for r in rows]
        pool_Y = [float(r[1]) for r in rows]
        seen_signatures = {json.dumps(x, sort_keys=True) for x in pool_X}
        print(f"[init] 累积池初始化 {len(pool_X)} 条", flush=True)

    model = OMLDesignLearner(in_dim=len(pool_X[0]))
    rng = np.random.default_rng(int(time.time()) % 10_000)
    trend = []

    for rnd in range(args.rounds):
        if stop_file.exists():
            print(f"[stop] 退出", flush=True)
            break
        t0 = time.time()

        # 1. 从累积池抽样 support + query(在累积池上持续学)
        pool = np.array(pool_X, dtype=float)
        poolY = np.array(pool_Y, dtype=float)
        n = len(pool)
        if n < args.support_n + args.query_n + 5:
            print(f"[round {rnd}] 池太小({n}), 等累积", flush=True)
            time.sleep(args.interval)
            continue
        idx = rng.permutation(n)[: args.support_n + args.query_n]
        sup_idx, qry_idx = idx[: args.support_n], idx[args.support_n:]
        Xs, ys = pool[sup_idx], poolY[sup_idx]
        Xq, yq = pool[qry_idx], poolY[qry_idx]
        loss_before, loss_adapted = model.oml_step(Xs, ys, Xq, yq, K=args.inner_k)

        # 2. 选 top-K(可配策略) / cold 对照轮(随机引导, 归因)
        #    交替: 偶数为 warm(OML引导), 奇数为 cold(随机引导) → 严格对比 forge best_score
        is_cold = (rnd % 2 == 1)
        if is_cold:
            sel = rng.choice(len(pool), args.topk, replace=False)
            sel = np.sort(sel)
        elif args.strategy == "ucb":
            sel = model.select_topk_ucb(pool, args.topk, lam=0.5)
        elif args.strategy == "diverse":
            sel = model.select_topk_diverse(pool, args.topk, lam=0.3)
        else:
            sel = model.select_topk(pool, args.topk)
        kk = min(args.topk, len(sel))
        hl = poolY[sel[:kk]]
        rand_idx = rng.choice(len(pool), kk, replace=False)
        rand = poolY[rand_idx]
        lift_pool = float(np.median(hl) - np.median(rand))

        # 3. 写候选 registry(用选出的设计参数) → forge 真评估
        hl_cands = [list(feature_to_design(pool[i])) for i in sel[:kk]]
        op = {}
        new_reflux = 0
        outd = None
        try:
            import os as _os, subprocess as _sub
            op_dir = pathlib.Path(args.forge_root) / "runs" / f"oml_v4_{int(time.time()*1000)}"
            op_dir.mkdir(parents=True)
            _ts = time.strftime("%Y-%m-%dT%H:%M:%S")
            lines = [json.dumps({
                "experiment_id": i + 1, "design_id": f"D{i+1:04d}", "generation": 0,
                "algorithm": f"oml_{args.strategy}", "seed": 0, "eval_index": i, "tag": "oml_v4",
                "timestamp": _ts, "score": 0.0, "feasible": True,
                "params": {"radius_m": [float(x) for x in d[:4]],
                           "z_m": [float(x) for x in d[4:8]],
                           "current_A": [float(x) for x in d[8:]]},
                "terms": {}, "weighted": {}, "penalties": {}, "metrics": {}, "note": "oml_v4"})
                for i, d in enumerate(hl_cands)]
            (op_dir / "registry.jsonl").write_text("\n".join(lines) + "\n")
            outd = op_dir / "out"
            outd.mkdir()
            _env = dict(_os.environ)
            _env["PATH"] = "/work/liuyuanjie/go1.24/bin:/work/liuyuanjie/go/bin:" + _env.get("PATH", "")
            _env["GOTOOLCHAIN"] = "local"
            _env["GOPROXY"] = "https://goproxy.cn,direct"
            _cmd = ["go", "run", "./cmd/forge", "benchmark", "--budget", "30",
                    "--seeds", "0", "--methods", "evolution_knowledge",
                    "--knowledge", str(op_dir), "--out", str(outd), "--tag", "oml_v4"]
            _r = _sub.run(_cmd, cwd=str(args.forge_root), capture_output=True, text=True, env=_env)
            op = {"exit": _r.returncode}
            # 4. 读回 forge 真实 score → 累积池回流(只加未见过的)
            if _r.returncode == 0:
                res_reg = outd / "registry.jsonl"
                if res_reg.exists():
                    for line in open(res_reg):
                        rec = json.loads(line)
                        if not rec.get("feasible", True):
                            continue
                        try:
                            fx = list(_params_to_feature(rec["params"]))
                        except Exception:
                            continue
                        sig = json.dumps(fx, sort_keys=True)
                        if sig in seen_signatures:
                            continue
                        seen_signatures.add(sig)
                        pool_X.append(fx)
                        pool_Y.append(float(rec.get("score", 0.0)))
                        new_reflux += 1
        except Exception as _e:
            op = {"exit": -1, "err": str(_e)[:200]}

        # 5. 效果验证(核心): 读 forge results.json 提取真实产出指标
        #    best_score = 这轮 forge 找到的最优设计(持续学习让 forge 找得更好?)
        #    evals_to_beat = 达到超越 baseline 需要的评估次数(越小越好)
        #    跨轮追踪: best_score 应上升 / evals_to_beat 应下降 = 模型在让 forge 变好
        best_score, evals_to_beat = None, None
        if op.get("exit") == 0 and outd is not None and outd.exists():
            resj = outd / "results.json"
            if resj.exists():
                try:
                    rj = json.loads(open(resj).read())
                    if rj.get("best"):
                        best_score = float(rj["best"]["score"])
                    agg = rj.get("aggregate", {}).get("evolution_knowledge", {})
                    if agg.get("evals_to_beat_mean") is not None:
                        evals_to_beat = float(agg["evals_to_beat_mean"])
                except Exception:
                    pass

        # 5b. 辅助: 在【本轮新增回流数据】上算预测误差(未见过 → 真泛化)
        reflux_err = None
        if new_reflux > 0:
            newX = np.array(pool_X[-new_reflux:], dtype=float)
            newY = np.array(pool_Y[-new_reflux:], dtype=float)
            pred = model.predict(newX)
            reflux_err = float(np.mean((pred - newY) ** 2))

        trend.append({"round": rnd, "query_mse": loss_before, "query_mse_adapted": loss_adapted,
                      "select_lift": lift_pool, "pool_size": n, "new_reflux": new_reflux,
                      "reflux_err": reflux_err, "forge_op": op.get("exit"),
                      "best_score": best_score, "evals_to_beat": evals_to_beat,
                      "strategy": args.strategy, "is_cold": is_cold,
                      "ts": time.strftime("%Y-%m-%dT%H:%M:%S")})
        with open(out, "a") as f:
            f.write(json.dumps(trend[-1]) + "\n")

        # checkpoint 持久化(24h运作: 崩溃可恢复)
        if rnd % 5 == 0:
            with open(ckpt, "wb") as f:
                pickle.dump((pool_X, pool_Y, seen_signatures), f)

        recent_lift = np.mean([t["select_lift"] for t in trend[-5:]])
        mode = "cold" if is_cold else "warm"
        print(f"[round {rnd}][{mode}] pool={n} reflux={new_reflux} "
              f"(回水误{reflux_err:.3f}) lift={lift_pool:+.3f}"
              f"(近5{recent_lift:+.3f}) best={best_score:.3f} evals_to_beat={evals_to_beat}"
              f" [op={op.get('exit')} {time.time()-t0:.0f}s]",
              flush=True)
        time.sleep(args.interval)

    # ── 结束: 长期趋势报告 ──
    if trend:
        lifts = [t["select_lift"] for t in trend]
        errs = [t["reflux_err"] for t in trend if t["reflux_err"] is not None]
        print(f"\n=== v4 闭环结束: {len(trend)} 轮 ===", flush=True)
        print(f"pool 增长: {trend[0]['pool_size']} → {trend[-1]['pool_size']}", flush=True)
        print(f"回流数据累计: {sum(t['new_reflux'] for t in trend)} 条", flush=True)
        print(f"select_lift: 前3 {np.mean(lifts[:3]):+.3f} → 后3 {np.mean(lifts[-3:]):+.3f}"
              f" (全正 {sum(1 for x in lifts if x>0)}/{len(lifts)})", flush=True)
        if len(errs) >= 4:
            print(f"回流预测误差: 前3 {np.mean(errs[:3]):.3f} → 后3 {np.mean(errs[-3:]):.3f}"
                  f" (下降=模型随累积数据变准)", flush=True)
        # 效果指标: forge 真实产出趋势(持续学习让 forge 变好?)
        bs = [t["best_score"] for t in trend if t.get("best_score") is not None]
        eb = [t["evals_to_beat"] for t in trend if t.get("evals_to_beat") is not None]
        if len(bs) >= 4:
            print(f"forge best_score: 前3 {np.mean(bs[:3]):.3f} → 后3 {np.mean(bs[-3:]):.3f}"
                  f" (上升=持续学习让forge找得更好)", flush=True)
        if len(eb) >= 4:
            print(f"evals_to_beat: 前3 {np.mean(eb[:3]):.1f} → 后3 {np.mean(eb[-3:]):.1f}"
                  f" (下降=更快超越baseline)", flush=True)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())