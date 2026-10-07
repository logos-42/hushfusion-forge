#!/usr/bin/env python3
"""持续运行闭环 v6 —— 判据探索(让持续学习自主选择学习效率与搜索判据)。

v5 学\"哪个 method×budget×seed 配置高效\"(判据固定 0.05)。
v6 把 **min_clearance 判据**加进配置空间: 4 档判据(0.02/0.05/0.10/0.15)
× 现有方法。让 OML 学\"哪个 (判据, 方法, budget) 配置高效\"。

动机(2026-10-07 min_clearance 敏感度实测):
  mc=0.02 全局 0.9711 > mc=0.05 0.9489 > mc=0.10 0.8489 ≈ mc=0.15 0.8483
  ⟹ 判据选择本身是性能杠杆, 而且 evolution_knowledge 在松判据下(0.02)
  能超 evolution。推荐器如果学会\"在 0.02 判据下用 ek 搜索\"=
  \"自主选择学习效率与搜索方向\", 而不再是被固定判据锁死。

闭环(v6):
  每轮:
    1. 推荐器从 (判据 × 方法 × budget × seed × 超参) 选配置
    2. 跑 forge run --spec-variant min_clearance=X → best_score
    3. 回流 (配置特征含 min_clearance, best_score) 进累积池
    4. OML 持续学习 配置→score → 越学越会推荐高效 (判据, 方法) 组合
    5. warm/cold对照不变

诚实边界:
  - 不同判据下的 best_score **不可直接互比**(分数口径不同)。
    推荐器学的是\"在判据X下, 配置Y的分数相对Z的高低\"——它自洽地学会
    偏好能打出高分的(判据,方法)组合, 这本身就是\"选择搜索效率\"。
  - 判据是搜索盒的一部分, 不是物理。min_clearance 是工程判断。
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
# 连续超参(仅 evolution 系列生效)
MUS = [8, 16, 32]
LAMS = [24, 48, 96]
SIGMAS = [0.12, 0.25, 0.5]
# ★ v6 新增: 判据档位
MIN_CLEARANCES = [0.02, 0.05, 0.10, 0.15]
# 判据档位名(与 --spec-variant 的 Variant.Name 对齐)
MC_NAMES = {0.02: "min_clearance=0.02m", 0.05: "min_clearance=0.05m",
            0.10: "min_clearance=0.10m", 0.15: "min_clearance=0.15m"}


def config_feature(method, budget, seed, mu=None, lam=None, sigma0=None, min_clearance=None):
    """配置 → 特征向量。

    v6: 末尾加 min_clearance 归一化维度(0.02→0.0, 0.15→1.0)。
    判据参与学习 —— 让模型学会\"松判据下某些方法更好\"。
    """
    mi = METHODS.index(method)
    f = [1.0 if i == mi else 0.0 for i in range(len(METHODS))] + \
        [np.log10(budget), float(seed % 3)]
    f += [float((mu or 16)) / 32.0,
          float((lam or 48)) / 96.0,
          float((sigma0 or 0.25)) / 0.5]
    # v6: min_clearance 归一化 [0.02, 0.15] → [0, 1]
    mc = min_clearance if min_clearance is not None else 0.05
    f += [(mc - 0.02) / 0.13]
    return f


def run_forge(method, seed, budget, forge_root, mu=None, lam=None, sigma0=None,
              knowledge_dir=None, registry_path=None, min_clearance=None):
    """跑一次 forge 搜索, 返回 best_score(确定性)。

    v6: 传 --spec-variant min_clearance=X 选判据档。
    0.05 = 默认判据, 也显式传(与其它档同路径, 无歧义)。
    """
    import os
    env = dict(os.environ)
    env["PATH"] = "/work/liuyuanjie/go1.24/bin:/work/liuyuanjie/go/bin:" + env.get("PATH", "")
    env["GOTOOLCHAIN"] = "local"
    env["GOPROXY"] = "https://goproxy.cn,direct"
    cmd = ["go", "run", "./cmd/forge", "run",
           "--method", method, "--seed", str(seed), "--budget", str(budget),
           "--registry", registry_path or "/tmp/oml_v6_scratch.jsonl"]
    if mu is not None:
        cmd += ["--mu", str(mu), "--lam", str(lam), "--sigma0", str(sigma0)]
    if knowledge_dir is not None:
        cmd += ["--knowledge", str(knowledge_dir)]
    if min_clearance is not None:
        mc_name = MC_NAMES[min_clearance]
        cmd += ["--spec-variant", mc_name]
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
    ap.add_argument("--out", default=str(ROOT / "artifacts" / "oml_daemon_trend_v6.jsonl"))
    ap.add_argument("--ckpt", default=str(ROOT / "artifacts" / "oml_daemon_v6_ckpt.pkl"))
    ap.add_argument("--interval", type=float, default=20.0)
    args = ap.parse_args()

    stop_file = pathlib.Path("/work/liuyuanjie/design_daemon_oml.stop")
    out = pathlib.Path(args.out)
    out.parent.mkdir(parents=True, exist_ok=True)
    ckpt = pathlib.Path(args.ckpt)

    # 知识库: v6 用独立目录 runs/v6_knowledge/ (与 v5 分开, 避免双写同一 registry)
    # 初始化时从 v5 知识库复制一份作种子(继承已积累的设计知识), 之后独立生长
    kb_dir = pathlib.Path(args.forge_root) / "runs" / "v6_knowledge"
    kb_dir.mkdir(parents=True, exist_ok=True)
    kb_reg = kb_dir / "registry.jsonl"
    if not kb_reg.exists():
        v5_kb = pathlib.Path(args.forge_root) / "runs" / "v5_knowledge" / "registry.jsonl"
        if v5_kb.exists() and v5_kb.stat().st_size > 0:
            kb_reg.write_text(open(v5_kb).read())
            print(f"[init] 知识库建立: 从 v5 复制 {sum(1 for _ in open(kb_reg))} 条作种子", flush=True)
        else:
            phase1 = pathlib.Path(args.forge_root) / "runs" / "phase1" / "registry.jsonl"
            if phase1.exists():
                kb_reg.write_text(open(phase1).read())
                print(f"[init] 知识库建立: phase1 {sum(1 for _ in open(kb_reg))} 条作种子", flush=True)
            else:
                kb_reg.write_text("")
                print("[init] 无 v5/phase1, 知识库从空开始", flush=True)

    import os
    import pickle
    rng = np.random.default_rng(int(time.time()) % 10_000)
    pool_X, pool_Y = [], []
    model_state = None
    if ckpt.exists():
        try:
            with open(ckpt, "rb") as f:
                data = pickle.load(f)
            if len(data) == 3:
                pool_X, pool_Y, model_state = data
            else:
                pool_X, pool_Y = data
        except Exception as e:
            print(f"[ckpt] 读取失败({e}), 从init开始", flush=True)
            pool_X, pool_Y = [], []
        print(f"[ckpt] 恢复 {len(pool_X)} 个配置经验, 模型={'有' if model_state else '无(需重建)'}", flush=True)
    else:
        # 初始种子: 4 判据 × {最小,最大budget} × seed(省时间但学到判据+预算效应)
        print("[init] 初始探索判据×配置空间(4判据 × budgets={50,400}, evolution带真实超参)...", flush=True)
        for mc in MIN_CLEARANCES:
            for m in METHODS:
                for b in [50, 400]:
                    for s in SEEDS:
                        mu, lam, sigma0 = None, None, None
                        if m in ("evolution", "evolution_warm", "evolution_knowledge"):
                            mu, lam, sigma0 = int(rng.choice(MUS)), int(rng.choice(LAMS)), float(rng.choice(SIGMAS))
                        kb_seed_init = kb_dir if (m == "evolution_knowledge"
                                                  and kb_reg.exists() and kb_reg.stat().st_size > 0) else None
                        sc, err = run_forge(m, s, b, args.forge_root, mu, lam, sigma0,
                                            knowledge_dir=kb_seed_init, registry_path=str(kb_reg),
                                            min_clearance=mc)
                        if sc is not None:
                            pool_X.append(config_feature(m, b, s, mu, lam, sigma0, mc))
                            pool_Y.append(float(sc))
                        else:
                            print(f"[init] {mc} {m}/{b}/s{s} forge失败: {err}", flush=True)
        print(f"[init] 累积池 {len(pool_X)} 个配置经验", flush=True)

    model = OMLDesignLearner(in_dim=len(pool_X[0]))
    if model_state is not None:
        import torch
        try:
            model.head.load_state_dict(model_state["head"])
            model.opt.load_state_dict(model_state["opt"])
            print("[ckpt] 推荐器模型权重已恢复(不冷启动)", flush=True)
        except Exception as e:
            print(f"[ckpt] 模型恢复失败({e}), 用新模型", flush=True)
    trend = []

    for rnd in range(args.rounds):
        if stop_file.exists():
            print("[stop] 退出", flush=True)
            break
        t0 = time.time()

        is_cold = (rnd % 2 == 1)

        # 选配置: v6 配置空间含 min_clearance 判据
        if is_cold:
            mc = float(rng.choice(MIN_CLEARANCES))
            method = rng.choice(METHODS)
            budget = int(rng.choice(BUDGETS))
            seed = int(rng.choice(SEEDS))
            mu, lam, sigma0 = None, None, None
            if method in ("evolution", "evolution_warm", "evolution_knowledge"):
                mu, lam, sigma0 = int(rng.choice(MUS)), int(rng.choice(LAMS)), float(rng.choice(SIGMAS))
        else:
            # 推荐器: 判据×方法×budget×seed×超参
            base_cands = [(mc, m, b, s) for mc in MIN_CLEARANCES for m in METHODS for b in BUDGETS for s in SEEDS]
            cands = base_cands
            if len(pool_X) >= 8:
                cue = [(mc, m, b, s, mu, la, sg)
                       for mc in MIN_CLEARANCES for m in METHODS for b in BUDGETS for s in SEEDS
                       for mu in MUS for la in LAMS for sg in SIGMAS
                       if m in ("evolution", "evolution_warm", "evolution_knowledge")]
                cands = base_cands + cue
            r = rng.random()
            if r < 0.15:  # 低ε纯随机兜底
                best = cands[rng.choice(len(cands))]
                mc, method, budget, seed = best[0], best[1], best[2], best[3]
                mu, lam, sigma0 = (best[4:7] if len(best) > 4 else (None, None, None))
            else:  # UCB: 预测值 + β×探索项
                feats = np.array([config_feature(c[1], c[2], c[3],
                                                 c[4] if len(c) > 4 else None,
                                                 c[5] if len(c) > 5 else None,
                                                 c[6] if len(c) > 6 else None,
                                                 c[0]) for c in cands])
                pred = model.predict(feats)
                pool_arr = np.array(pool_X, dtype=float)
                counts = np.array([
                    np.sum(np.all(np.abs(pool_arr - cfgf) < 1e-6, axis=1))
                    for cfgf in feats], dtype=float)
                beta = 0.5
                explore = np.exp(-counts)
                ucb = pred + beta * explore
                best = cands[int(np.argmax(ucb))]
                mc, method, budget, seed = best[0], best[1], best[2], best[3]
                mu, lam, sigma0 = (best[4:7] if len(best) > 4 else (None, None, None))

        # 跑 forge
        kb_seed = kb_dir if (method == "evolution_knowledge"
                             and kb_reg.exists() and kb_reg.stat().st_size > 0) else None
        score, err = run_forge(method, seed, budget, args.forge_root, mu, lam, sigma0,
                               knowledge_dir=kb_seed, registry_path=str(kb_reg),
                               min_clearance=mc)
        if score is None:
            print(f"[round {rnd}] forge失败: {err}", flush=True)
            time.sleep(args.interval)
            continue

        fx = config_feature(method, budget, seed, mu, lam, sigma0, mc)
        pool_X.append(fx)
        pool_Y.append(float(score))
        n = len(pool_X)

        X = np.array(pool_X, dtype=float); Y = np.array(pool_Y, dtype=float)
        if n >= 8:
            idx = rng.permutation(n)
            tr = idx[: max(4, n - 3)]
            te = idx[max(4, n - 3):]
            model.oml_step(X[tr], Y[tr], X[te], Y[te], K=3)

        trend.append({"round": rnd, "is_cold": is_cold, "method": method,
                      "budget": budget, "seed": seed, "mu": mu, "lam": lam, "sigma0": sigma0,
                      "min_clearance": mc, "best_score": score,
                      "pool_size": n, "ts": time.strftime("%Y-%m-%dT%H:%M:%S")})
        with open(out, "a") as f:
            f.write(json.dumps(trend[-1]) + "\n")

        if rnd % 5 == 0:
            with open(ckpt, "wb") as f:
                pickle.dump((pool_X, pool_Y,
                     {"head": model.head.state_dict(), "opt": model.opt.state_dict()}), f)

        mode = "cold" if is_cold else "warm"
        hp = f"/mu{mu}/lam{lam}/sig{sigma0}" if mu is not None else ""
        print(f"[round {rnd}][{mode}] mc={mc:.2f} {method}/{budget}{hp}/s{seed} → best={score:.3f} "
              f"pool={n} [{time.time()-t0:.0f}s]", flush=True)
        time.sleep(args.interval)

    if trend:
        w = [t["best_score"] for t in trend if not t["is_cold"]]
        c = [t["best_score"] for t in trend if t["is_cold"]]
        print(f"\n=== v6 判据探索闭环结束: {len(trend)} 轮 ===", flush=True)
        print(f"pool: {trend[0]['pool_size']} → {trend[-1]['pool_size']}", flush=True)
        if w and c:
            print(f"warm(推荐) best均值 {np.mean(w):.3f} vs cold(随机) {np.mean(c):.3f} "
                  f"(Δ={np.mean(w)-np.mean(c):+.3f})", flush=True)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
