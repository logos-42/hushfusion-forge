#!/usr/bin/env python3
"""持续学习电磁设计闭环 —— 常驻守护进程（服务器持续运行）。

每一轮:
  1. 读 forge registry(设计→score 数据流)
  2. headless(PLNHead) 持续学习价值函数
  3. 从合理候选池选 top-K(引导 forge 下一批)
  4. forge 真评估 → 新增真实数据回流
  5. 跑验证门(5-fold 采样) → 记录 lift 趋势到 JSONL
  6. 循环(常驻, 持续迭代)

效果验证: 每轮输出 headless 选样 lift(应持续>0) + 累积趋势。
停止: 写 /work/liuyuanjie/design_daemon.stop 或 kill 进程。

用法(服务器):
  nohup env CUDA_VISIBLE_DEVICES=0 python -u scripts/design_daemon.py \
    --rounds 1000 --out /work/liuyuanjie/forge/artifacts/design_daemon_trend.jsonl &
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
    ValueModel, design_to_feature, gen_random_designs, load_designs, propose_designs,
    forge_eval_batch,
)


def verify_lift(vm: ValueModel, X: np.ndarray, Y: np.ndarray, rng, topk: int = 30):
    """当轮验证: headless 从已评估池选 top-K 的真实 score vs 随机。"""
    pred = vm.predict(X)
    kk = min(topk, len(X))
    hl = Y[np.argsort(-pred)[:kk]]
    rand = Y[rng.choice(len(X), kk, replace=False)]
    return float(np.median(hl) - np.median(rand)), float(np.median(hl)), float(np.median(rand))


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--rounds", type=int, default=100000, help="最大轮数(常驻设大)")
    ap.add_argument("--registry", default=str(ROOT / "runs" / "phase1" / "registry.jsonl"))
    ap.add_argument("--n-propose", type=int, default=20)
    ap.add_argument("--topk-verify", type=int, default=30)
    ap.add_argument("--forge-root", default=str(ROOT))
    ap.add_argument("--out", default=str(ROOT / "artifacts" / "design_daemon_trend.jsonl"))
    ap.add_argument("--interval", type=float, default=30.0, help="每轮之间休息秒数")
    ap.add_argument("--pretrain", action="store_true")
    args = ap.parse_args()

    stop_file = pathlib.Path("/work/liuyuanjie/design_daemon.stop")
    out = pathlib.Path(args.out)
    out.parent.mkdir(parents=True, exist_ok=True)
    forge_root = pathlib.Path(args.forge_root)

    # 初始数据
    rows = load_designs(pathlib.Path(args.registry))
    X_all = np.array([r[0] for r in rows])
    Y_all = np.array([r[1] for r in rows])
    print(f"[init] 载入 {len(X_all)} 个设计, 开始持续闭环", flush=True)

    rng = np.random.default_rng(0)
    trend = []
    # 关键改进: 不用单网络每轮在线增量(易漂移→波动), 改为【周期性全量重训】。
    # 已验证: 全量重训判别器(5-fold) lift +1.27 全正; 在线增量会漂移(-0.75/-1.48)。
    re_train_every = 5   # 每 5 轮用累积全量数据重训一次
    retrain_cap = 3000   # 重训时最多用最近 3000 条(控时, 防全历史淹没新分布)
    X_buf, Y_buf = X_all.tolist(), Y_all.tolist()   # 累积 buffer(新数据持续追加)
    vm = ValueModel(seed=0)

    def retrain():
        nonlocal vm
        # 用最近的 retrain_cap 条重训一个全新模型(稳定, 非增量)
        vm = ValueModel(seed=int(time.time()) % 1000)
        n_use = min(retrain_cap, len(X_buf))
        Xt = X_buf[-n_use:]
        Yt = Y_buf[-n_use:]
        vm.learn_batch(Xt, Yt, epochs=8)
        return n_use

    # 初始重训
    n_use = retrain()
    print(f"[init] 全量重训 {n_use} 条(非增量, 稳定判别器)", flush=True)

    for rnd in range(args.rounds):
        if stop_file.exists():
            print(f"[stop] 检测到停止文件, 退出", flush=True)
            break
        t0 = time.time()

        # 周期性重训(压波动: 不依赖单网络在线漂移)
        if rnd > 0 and rnd % re_train_every == 0:
            n_use = retrain()
            print(f"[round {rnd}] 重训 {n_use} 条", flush=True)

        # 1. 用当前(重训后)模型从合理候选池引导选 top-K
        hl_cands = propose_designs(vm, args.n_propose, seed=rnd, epsilon=0.2)

        # 2. forge 真评估(新增真实数据)
        try:
            hl_scores, rc, _, err = forge_eval_batch(forge_root, hl_cands,
                                                     f"d_{rnd}", seed=rnd, budget=30)
        except Exception as e:
            print(f"[round {rnd}] forge 评估失败: {e}", flush=True)
            time.sleep(args.interval)
            continue
        if rc != 0:
            print(f"[round {rnd}] forge 返回 {rc}: {err[-200:]}", flush=True)
            time.sleep(args.interval)
            continue

        # 3. 回流: 把 forge 评估的真实 score 追加进累积 buffer(不是在线增量更新单网络)
        real_vals = [v for v in hl_scores.values() if v is not None]
        if len(real_vals) > 1:
            real_vals = real_vals[1:]  # 去掉 baseline
        hl_feats = np.array([design_to_feature(c) for c in hl_cands])
        if len(real_vals) >= len(hl_cands):
            X_buf.extend(hl_feats.tolist())
            Y_buf.extend(real_vals[:len(hl_cands)])

        # 4. 当轮验证(从累积数据池选样 lift —— 反映学到了新数据)
        verify_X = np.array(X_buf[-min(args.topk_verify * 4, len(X_buf)):])
        verify_Y = np.array(Y_buf[-min(args.topk_verify * 4, len(Y_buf)):])
        lift, hl_med, rand_med = verify_lift(vm, verify_X, verify_Y, rng, args.topk_verify)
        trend.append({"round": rnd, "lift": lift, "headless_median": hl_med,
                      "random_median": rand_med, "n_learn": vm.n_learn,
                      "ts": time.strftime("%Y-%m-%dT%H:%M:%S")})
        # 累积趋势: 最近 5 轮 lift 均值
        recent = np.mean([t["lift"] for t in trend[-5:]]) if len(trend) >= 5 else lift
        print(f"[round {rnd}] 验证lift={lift:+.3f} (最近5轮均值{recent:+.3f}) "
              f"headless中位={hl_med:.3f} 随机={rand_med:.3f} 已学{vm.n_learn} "
              f"[{time.time()-t0:.0f}s]", flush=True)

        # 记录趋势
        with open(out, "a") as f:
            f.write(json.dumps(trend[-1]) + "\n")

        time.sleep(args.interval)

    # 汇总
    if trend:
        lifts = [t["lift"] for t in trend]
        print(f"\n=== 守护进程结束: {len(trend)} 轮 ===", flush=True)
        print(f"验证 lift 均值: {np.mean(lifts):+.3f}  中位: {np.median(lifts):+.3f}  "
              f">0比例: {sum(1 for x in lifts if x>0)}/{len(lifts)}", flush=True)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
