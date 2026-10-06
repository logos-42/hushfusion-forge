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

METHODS = ["random", "lhs", "evolution", "evolution_warm", "evolution_knowledge"]
BUDGETS = [50, 100, 200, 400]
SEEDS = [0, 1, 2]
# 连续超参(仅 evolution 系列生效; random/lhs 忽略)
MUS = [8, 16, 32]
LAMS = [24, 48, 96]
SIGMAS = [0.12, 0.25, 0.5]


def config_feature(method, budget, seed, mu=None, lam=None, sigma0=None):
    """配置 → 特征向量 [method_onehot(4), log_budget, seed_mod3, norm_mu, norm_lam, norm_sigma]"""
    mi = METHODS.index(method)
    f = [1.0 if i == mi else 0.0 for i in range(len(METHODS))] + \
        [np.log10(budget), float(seed % 3)]
    # 超参归一化(evolution 系列才有意义; 否则给中性 0.5)
    f += [float((mu or 16)) / 32.0,
          float((lam or 48)) / 96.0,
          float((sigma0 or 0.25)) / 0.5]
    return f


def run_forge(method, seed, budget, forge_root, mu=None, lam=None, sigma0=None, knowledge_dir=None, registry_path=None):
    """跑一次 forge 搜索, 返回 best_score(确定性)。

    knowledge_dir: 若给, 用 evolution_knowledge 从该目录 registry 读 top-K 播种
    registry_path: registry 输出(累积知识库); 默认 /tmp 临时
    """
    import os
    env = dict(os.environ)  # 继承(fix: 缺HOME/GOCACHE致go build失败)
    env["PATH"] = "/work/liuyuanjie/go1.24/bin:/work/liuyuanjie/go/bin:" + env.get("PATH", "")
    env["GOTOOLCHAIN"] = "local"
    env["GOPROXY"] = "https://goproxy.cn,direct"
    cmd = ["go", "run", "./cmd/forge", "run",
           "--method", method, "--seed", str(seed), "--budget", str(budget),
           "--registry", registry_path or "/tmp/oml_v5_scratch.jsonl"]
    if mu is not None:
        cmd += ["--mu", str(mu), "--lam", str(lam), "--sigma0", str(sigma0)]
    if knowledge_dir is not None:
        cmd += ["--knowledge", str(knowledge_dir)]
    r = subprocess.run(cmd, cwd=str(forge_root), capture_output=True, text=True, env=env)
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

    # 知识库初始化: v5_knowledge/registry.jsonl 若不存在, 用 phase1 registry 作种子
    # (让第一轮就有12001条知识可播种, 而非空库退化)
    kb_dir = pathlib.Path(args.forge_root) / "runs" / "v5_knowledge"
    kb_dir.mkdir(parents=True, exist_ok=True)
    kb_reg = kb_dir / "registry.jsonl"
    if not kb_reg.exists():
        phase1 = pathlib.Path(args.forge_root) / "runs" / "phase1" / "registry.jsonl"
        if phase1.exists():
            kb_reg.write_text(open(phase1).read())
            print(f"[init] 知识库建立: phase1 {sum(1 for _ in open(kb_reg))} 条作种子", flush=True)
        else:
            kb_reg.write_text("")
            print("[init] 无 phase1, 知识库从空开始", flush=True)

    import os
    import pickle
    rng = np.random.default_rng(int(time.time()) % 10_000)
    pool_X, pool_Y = [], []
    if ckpt.exists():
        with open(ckpt, "rb") as f:
            pool_X, pool_Y = pickle.load(f)
        print(f"[ckpt] 恢复 {len(pool_X)} 个配置经验", flush=True)
    else:
        # 初始种子: 覆盖 method × {最小,最大budget} × seed(省时间但学到budget效应)
        # ★特征一致性: evolution系列必须带真实超参评估(不能用中性, 否则推荐器被假高分误导)
        print("[init] 初始探索配置空间(budgets={50,400}, evolution带真实超参)...", flush=True)
        for m in METHODS:
            for b in [50, 400]:
                for s in SEEDS:
                    mu, lam, sigma0 = None, None, None
                    if m in ("evolution", "evolution_warm"):
                        mu, lam, sigma0 = int(rng.choice(MUS)), int(rng.choice(LAMS)), float(rng.choice(SIGMAS))
                    sc, err = run_forge(m, s, b, args.forge_root, mu, lam, sigma0)
                    if sc is not None:
                        pool_X.append(config_feature(m, b, s, mu, lam, sigma0))
                        pool_Y.append(float(sc))
        print(f"[init] 累积池 {len(pool_X)} 个配置经验", flush=True)

    model = OMLDesignLearner(in_dim=len(pool_X[0]))
    trend = []

    for rnd in range(args.rounds):
        if stop_file.exists():
            print("[stop] 退出", flush=True)
            break
        t0 = time.time()

        is_cold = (rnd % 2 == 1)

        # 选配置: warm=推荐器, cold=随机(method/budget/seed + 超参)
        if is_cold:
            method = rng.choice(METHODS)
            budget = int(rng.choice(BUDGETS))
            seed = int(rng.choice(SEEDS))
            mu, lam, sigma0 = None, None, None
            if method in ("evolution", "evolution_warm"):
                mu, lam, sigma0 = int(rng.choice(MUS)), int(rng.choice(LAMS)), float(rng.choice(SIGMAS))
        else:
            # 推荐器: 扩展配置空间(方法×budget×seed×超参)打分, ε探索
            cands = [(m, b, s) for m in METHODS for b in BUDGETS for s in SEEDS]
            if len(pool_X) >= 8:  # 有经验后才启用超参维(否则空间太大瞎猜)
                cue = [(m, b, s, mu, la, sg)
                       for m in METHODS for b in BUDGETS for s in SEEDS
                       for mu in MUS for la in LAMS for sg in SIGMAS
                       if m in ("evolution", "evolution_warm")]
                cands = [(m, b, s, None, None, None) for m in METHODS for b in BUDGETS for s in SEEDS] + cue
            r = rng.random()
            if r < 0.15:  # 低ε纯随机兜底
                method, budget, seed = cands[rng.choice(len(cands))][:3]
                mu, lam, sigma0 = (None, None, None)
            else:  # UCB: 预测值 + λ×探索项(配置评估次数少=不确定=值得探索), 替代argmax
                feats = np.array([config_feature(*c) for c in cands])
                pred = model.predict(feats)
                # 探索项: 该配置已评估次数(越低越探索); 从累积池按最近5~9维特征统计
                pool_arr = np.array(pool_X, dtype=float)
                counts = np.array([
                    np.sum(np.all(np.abs(pool_arr - cfgf) < 1e-6, axis=1))
                    for cfgf in feats], dtype=float)
                beta = 0.5  # 探索系数(独立于超参lam)
                explore = np.exp(-counts)  # 0次=1, 次数多→趋0
                ucb = pred + beta * explore
                best = cands[int(np.argmax(ucb))]
                method, budget, seed = best[0], best[1], best[2]
                mu, lam, sigma0 = (best[3:6] if len(best) > 3 else (None, None, None))

        # 跑 forge: 只有 evolution_knowledge 用知识库播种(其他方法无知识=对照)
        # 这样 warm 推荐器学会选 knowledge 方法 → 真闭环"持续学习学会用知识库"
        kb_seed = kb_dir if (method == "evolution_knowledge"
                             and kb_reg.exists() and kb_reg.stat().st_size > 0) else None
        score, err = run_forge(method, seed, budget, args.forge_root, mu, lam, sigma0,
                               knowledge_dir=kb_seed, registry_path=str(kb_reg))
        if score is None:
            print(f"[round {rnd}] forge失败: {err}", flush=True)
            time.sleep(args.interval)
            continue

        # 回流进累积池(含超参特征)
        fx = config_feature(method, budget, seed, mu, lam, sigma0)
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
                      "budget": budget, "seed": seed, "mu": mu, "lam": lam, "sigma0": sigma0,
                      "best_score": score,
                      "pool_size": n, "ts": time.strftime("%Y-%m-%dT%H:%M:%S")})
        with open(out, "a") as f:
            f.write(json.dumps(trend[-1]) + "\n")

        if rnd % 5 == 0:
            with open(ckpt, "wb") as f:
                pickle.dump((pool_X, pool_Y), f)

        mode = "cold" if is_cold else "warm"
        hp = f"/mu{mu}/lam{lam}/sig{sigma0}" if mu is not None else ""
        print(f"[round {rnd}][{mode}] {method}/{budget}{hp}/s{seed} → best={score:.3f} "
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