#!/usr/bin/env python3
"""持续运行闭环 v5 —— 配置推荐器(持续学习"哪个搜索配置高效")。

方向2突破: 预测"设计→score"是死路(ρ≈0.05), 但"配置→score"可学(ρ=0.54)。
⟹ 让持续学习模型学"哪个 method×budget×seed 配置高效", 用它操作 forge。

闭环(v5):
  每轮:
    1. 用当前学到的推荐器从配置空间选一个配置
    2. 跑 forge 搜索 → 真实 best_score(确定性)
    3. 回流 (配置特征, best_score) 进累积池
    4. OML 持续学习 配置→score → 越学越会推荐高效配置
    5. warm/cold对照: warm轮=推荐器配置, cold轮=随机配置 → 验证推荐是否优于随机

验证指标: 跨轮 warm 推荐配置的 best_score 应上升/高于 cold (持续学习让forge越找越好)

用法(服务器):
  setsid CUDA_VISIBLE_DEVICES=0 bash scripts/oml_supervisor.sh --v5 ...
"""
from __future__ import annotations

import argparse
import json
import pathlib
import subprocess
import sys
import time

import numpy as np

ROOT = pathlib.Path(__file__).resolve().parents[1]
HEADLESS = pathlib.Path(__file__).resolve().parents[2] / "headless"
sys.path.insert(0, str(HEADLESS))
sys.path.insert(0, str(ROOT / "scripts"))
from oml_continual_core import OMLDesignLearner  # noqa: E402

METHODS = ["random", "lhs", "evolution", "evolution_warm"]
BUDGETS = [50, 100, 200, 400]
SEEDS = [0, 1, 2]


def config_feature(method, budget, seed):
    """配置 → 特征向量 [method_onehot(4), log_budget, seed_mod3]"""
    mi = METHODS.index(method)
    return [1.0 if i == mi else 0.0 for i in range(len(METHODS))] + \
        [np.log10(budget), float(seed % 3)]


def run_forge(method, seed, budget, forge_root):
    """跑一次 forge 搜索, 返回 best_score(确定性)。"""
    import os
    env = dict(os.environ)  # 继承(fix: 缺HOME/GOCACHE致go build失败)
    env["PATH"] = "/work/liuyuanjie/go1.24/bin:/work/liuyuanjie/go/bin:" + env.get("PATH", "")
    env["GOTOOLCHAIN"] = "local"
    env["GOPROXY"] = "https://goproxy.cn,direct"
    r = subprocess.run(["go", "run", "./cmd/forge", "run",
                        "--method", method, "--seed", str(seed), "--budget", str(budget),
                        "--registry", "/tmp/oml_v5_scratch.jsonl"],
                       cwd=str(forge_root), capture_output=True, text=True, env=env)
    import re
    m = re.search(r"best_score=([-\d.eE]+)", r.stdout)
    if not m:
        return None, r.stderr[-200:]
    return float(m.group(1)), None


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--rounds", type=int, default=100000)
    ap.add_argument("--forge-root", default=str(ROOT))
    ap.add_argument("--out", default=str(ROOT / "artifacts" / "oml_daemon_trend_v5.jsonl"))
    ap.add_argument("--ckpt", default=str(ROOT / "artifacts" / "oml_daemon_v5_ckpt.pkl"))
    ap.add_argument("--interval", type=float, default=20.0)
    args = ap.parse_args()

    stop_file = pathlib.Path("/work/liuyuanjie/design_daemon_oml.stop")
    out = pathlib.Path(args.out)
    out.parent.mkdir(parents=True, exist_ok=True)
    ckpt = pathlib.Path(args.ckpt)

    import os
    import pickle
    pool_X, pool_Y = [], []
    if ckpt.exists():
        with open(ckpt, "rb") as f:
            pool_X, pool_Y = pickle.load(f)
        print(f"[ckpt] 恢复 {len(pool_X)} 个配置经验", flush=True)
    else:
        # 初始种子: 覆盖 method × {最小,最大budget} × seed(省时间但学到budget效应)
        print("[init] 初始探索配置空间(budgets={50,400})...", flush=True)
        for m in METHODS:
            for b in [50, 400]:  # 最小+最大: 学 budget 单调方向
                for s in SEEDS:
                    sc, err = run_forge(m, s, b, args.forge_root)
                    if sc is not None:
                        pool_X.append(config_feature(m, b, s))
                        pool_Y.append(float(sc))
        print(f"[init] 累积池 {len(pool_X)} 个配置经验", flush=True)

    model = OMLDesignLearner(in_dim=len(pool_X[0]))
    rng = np.random.default_rng(int(time.time()) % 10_000)
    trend = []

    for rnd in range(args.rounds):
        if stop_file.exists():
            print("[stop] 退出", flush=True)
            break
        t0 = time.time()

        is_cold = (rnd % 2 == 1)

        # 选配置: warm=推荐器, cold=随机
        if is_cold:
            method = rng.choice(METHODS)
            budget = int(rng.choice(BUDGETS))
            seed = int(rng.choice(SEEDS))
        else:
            # 推荐器: 全配置空间打分, 选预测最高(带ε探索, 探索新budget区)
            cands = [(m, b, s) for m in METHODS for b in BUDGETS for s in SEEDS]
            r = rng.random()
            if r < 0.4:  # ε探索: 倾向试中等budget(100/200, 初始池未覆盖)
                mid = [c for c in cands if c[1] in (100, 200)]
                method, budget, seed = (mid[rng.choice(len(mid))] if mid else
                                        cands[rng.choice(len(cands))])
            else:  # 利用: 打分选预测最高
                feats = np.array([config_feature(m, b, s) for m, b, s in cands])
                pred = model.predict(feats)
                method, budget, seed = cands[int(np.argmax(pred))]

        # 跑 forge
        score, err = run_forge(method, seed, budget, args.forge_root)
        if score is None:
            print(f"[round {rnd}] forge失败: {err}", flush=True)
            time.sleep(args.interval)
            continue

        # 回流进累积池
        fx = config_feature(method, budget, seed)
        pool_X.append(fx)
        pool_Y.append(float(score))
        n = len(pool_X)

        # OML 持续学习: 训练/验证分离
        X = np.array(pool_X, dtype=float); Y = np.array(pool_Y, dtype=float)
        if n >= 8:
            idx = rng.permutation(n)
            tr = idx[: max(4, n - 3)]
            te = idx[max(4, n - 3):]
            model.oml_step(X[tr], Y[tr], X[te], Y[te], K=3)

        # 验证(只限 warm): 推荐配置得到的 score 是否在改善
        trend.append({"round": rnd, "is_cold": is_cold, "method": method,
                      "budget": budget, "seed": seed, "best_score": score,
                      "pool_size": n, "ts": time.strftime("%Y-%m-%dT%H:%M:%S")})
        with open(out, "a") as f:
            f.write(json.dumps(trend[-1]) + "\n")

        if rnd % 5 == 0:
            with open(ckpt, "wb") as f:
                pickle.dump((pool_X, pool_Y), f)

        mode = "cold" if is_cold else "warm"
        print(f"[round {rnd}][{mode}] {method}/{budget}/{seed} → best={score:.3f} "
              f"pool={n} [{time.time()-t0:.0f}s]", flush=True)
        time.sleep(args.interval)

    if trend:
        w = [t["best_score"] for t in trend if not t["is_cold"]]
        c = [t["best_score"] for t in trend if t["is_cold"]]
        print(f"\n=== v5 配置推荐闭环结束: {len(trend)} 轮 ===", flush=True)
        print(f"pool: {trend[0]['pool_size']} → {trend[-1]['pool_size']}", flush=True)
        if w and c:
            print(f"warm(推荐) best均值 {np.mean(w):.3f} vs cold(随机) {np.mean(c):.3f} "
                  f"(Δ={np.mean(w)-np.mean(c):+.3f})", flush=True)
            print(f"warm全正 {sum(1 for x in w if x>0)}/{len(w)}, cold {sum(1 for x in c if x>0)}/{len(c)}", flush=True)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())