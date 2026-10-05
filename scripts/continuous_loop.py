#!/usr/bin/env python3
"""持续学习电磁设计闭环 —— headless 核心 + forge 设计/操作 + 持续迭代。

目标（用户需求）：
    持续学习的电磁模型(headless GVF/DualSignalProposer)能**设计并操作** forge 软件系统，
    且效果可验证。

闭环：
    [A] 数据桥   forge registry(设计向量→score)  →  headless 可学的 (特征, 奖励) 流
    [B] 学习核心 headless GVF(设计价值) + DualSignalProposer(选样)  持续学"哪个设计值得评估"
    [B] 选样器   headless 从候选设计里选 top-K  →  喂回 forge 评估
    [C] 循环     选→评估→学→再选  持续迭代(服务器)
    [验证]       headless 选的 top-K 的 score 中位数 vs 随机基线 → 显著更高 = 有效

用法:
    python3 scripts/continuous_loop.py --rounds 5                # 本地跑 5 轮
    python3 scripts/continuous_loop.py --rounds 20 --server      # 服务器持续跑 20 轮
"""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

import numpy as np

ROOT = Path(__file__).resolve().parents[1]
# headless 路径 —— 服务器上可能是 /work/liuyuanjie/headless
HEADLESS = Path(__file__).resolve().parents[2] / "headless"
sys.path.insert(0, str(HEADLESS))

from hibs_lnn.dual_proposer import DualSignalProposer  # noqa: E402
from hibs_lnn.evidence_bank import GVF  # noqa: E402


# ───────────────────────── [A] 数据桥 ─────────────────────────
def design_to_feature(vec):
    """设计向量 → 归一化特征（电流 log10 + 全部分量标准化到可学尺度）。

    为什么必须归一化: forge 的电流是 1e6 量级, 而 GVF 是线性 w@phi + trace 累积,
    1e6 量级的特征会让 w 在电流分量上大幅变化、trace 累积溢出(实测 overflow + NaN)。
    电流取 log10 后变 4~6 量级, 再把所有分量标准化到 [0,1] 附近 —— headless 才能稳定学习。
    """
    f = np.array(vec, dtype=float)
    # 电流分量(索引 8..11)取 log10
    f[8:] = np.log10(np.clip(f[8:], 1e3, None))
    # 全局标准化到 ~[-1,1] (用已知量级范围, 保证跨 spec 稳定)
    rng_ = [0.1, 1.0, -1.2, 1.2, 1e4, 2.5e6]  # r, r, z, z, I, I (每分量 4 个)
    lo = np.array([0.1]*4 + [-1.2]*4 + [np.log10(1e4)]*4)
    hi = np.array([1.0]*4 + [1.2]*4 + [np.log10(2.5e6)]*4)
    f_norm = 2.0 * (f - lo) / (hi - lo + 1e-12) - 1.0
    return f_norm


def load_designs(reg_path: Path):
    """从 forge registry 读 (归一化特征, score)，只看 feasible。"""
    reg = [json.loads(l) for l in open(reg_path)]
    feas = [r for r in reg if r.get("feasible")]
    rows = []
    for r in feas:
        p = r["params"]
        vec = list(p["radius_m"]) + list(p["z_m"]) + list(p["current_A"])
        rows.append((design_to_feature(vec), r["score"]))
    return rows


def candidate_designs(n: int, seed: int = 0):
    """从 forge 搜索盒采样 n 个随机候选设计（headless 要选的池），归一化。"""
    rng = np.random.default_rng(seed)
    cands = []
    for _ in range(n):
        d = []
        for _ in range(4):
            d.append(rng.uniform(0.1, 1.0))          # r
        for _ in range(4):
            d.append(rng.uniform(-1.2, 1.2))         # z
        for _ in range(4):
            d.append(10 ** rng.uniform(np.log10(1e4), np.log10(2.5e6)))  # I (对数均匀)
        cands.append(d)
    return np.array([design_to_feature(c) for c in cands])


# ───────────────────────── [A'] 设计↔forge 接口 ─────────────────────────
def feature_to_design(f):
    """归一化特征 → forge 原始设计向量（逆变换，电流还原）。"""
    f = np.asarray(f, dtype=float)
    lo = np.array([0.1]*4 + [-1.2]*4 + [np.log10(1e4)]*4)
    hi = np.array([1.0]*4 + [1.2]*4 + [np.log10(2.5e6)]*4)
    raw = (f + 1.0) / 2.0 * (hi - lo + 1e-12) + lo
    out = raw.copy()
    out[8:] = 10 ** raw[8:]
    return out


def write_headless_selection_to_registry(designs_raw, scores, reg_path: Path):
    """把 headless 选的设计写成 forge 能读的 registry（feasible + params + score）。"""
    import time
    reg_path.parent.mkdir(parents=True, exist_ok=True)
    lines = []
    ts = time.strftime("%Y-%m-%dT%H:%M:%S")
    for i, (d, s) in enumerate(zip(designs_raw, scores)):
        rec = {
            "experiment_id": 1, "design_id": f"D{i+1:04d}", "generation": 0,
            "algorithm": "headless_selection", "seed": 0, "eval_index": i, "tag": "headless",
            "timestamp": ts, "score": float(s), "feasible": True,
            "params": {"radius_m": [float(x) for x in d[:4]],
                       "z_m": [float(x) for x in d[4:8]],
                       "current_A": [float(x) for x in d[8:]]},
            "terms": {}, "weighted": {}, "penalties": {},
            "metrics": {}, "note": "headless selection",
        }
        lines.append(json.dumps(rec))
    reg_path.write_text("\n".join(lines) + "\n")
    return reg_path


def run_forge_eval(forge_root: Path, seed_registry: Path, budget: int = 50, seed: int = 0,
                   method: str = "evolution_knowledge") -> dict:
    """驱动 forge benchmark，用 headless 选的设计当初始种群。

    返回:
      best_score    forge 搜索到的 best
      evaluated     forge 评估的所有 feasible (归一化特征, score) —— 全部回流 GVF 学习用
    """
    import subprocess, time
    # 每轮必须用唯一输出目录: forge benchmark 需要空 registry(它写 human baseline 当 lineage root),
    # 复用目录会因旧记录报 "registry already holds N records"。
    out_dir = forge_root / "runs" / f"headless_loop_{int(time.time()*1000)}"
    out_dir.mkdir(parents=True, exist_ok=True)
    cmd = ["go", "run", "./cmd/forge", "benchmark",
           "--budget", str(budget), "--seeds", str(seed),
           "--methods", method,
           "--knowledge", str(seed_registry.parent),
           "--out", str(out_dir), "--tag", f"headless_r{seed}"]
    r = subprocess.run(cmd, cwd=str(forge_root), capture_output=True, text=True)
    best_score = None
    evaluated = []
    for line in r.stdout.splitlines():
        if line.strip().startswith("best:"):
            for tok in line.split():
                if tok.startswith("score="):
                    best_score = float(tok.split("=")[1])
    # 读 forge 评估出的所有 feasible 设计，回流学习
    reg_file = out_dir / "registry.jsonl"
    if reg_file.exists():
        for line in open(reg_file):
            try:
                rec = json.loads(line)
            except json.JSONDecodeError:
                continue
            if not rec.get("feasible"):
                continue
            p = rec.get("params", {})
            if "radius_m" in p and "current_A" in p:
                vec = list(p["radius_m"]) + list(p.get("z_m", [])) + list(p["current_A"])
                if len(vec) == 12:
                    evaluated.append((design_to_feature(vec), rec.get("score", 0.0)))
    return {"best_score": best_score, "exit": r.returncode, "evaluated": evaluated,
            "stderr": r.stderr[-500:], "log_tail": r.stdout[-300:]}


# ───────────────────────── [B] 学习核心 ─────────────────────────
class HeadlessDesignAgent:
    """用 headless 持续学习价值函数 + 选样器，学"哪个设计值得评估"。"""

    def __init__(self, feature_dim: int, seed: int = 0):
        # GVF: 设计价值函数(设计向量 → 价值)。+1 是偏置项。
        self.gvf = GVF(name="design_value", dim=feature_dim + 1, seed=seed)
        self.feature_dim = feature_dim
        self.seed = seed
        self.history = []   # (设计, score, gvf预测)

    def _phi(self, design) -> np.ndarray:
        """设计向量 → GVF 特征（+1 偏置）。"""
        return np.r_[1.0, design]

    def learn(self, design, score):
        """在线学习一条 (设计→score) 经验。TD(λ)：phi_next=phi(视为自助)。"""
        phi = self._phi(design)
        # 用当前设计自身作 next（简化自助），score 作为即时奖励
        self.gvf.update(phi, float(score), phi)
        self.history.append((design, score, self.gvf.predict(phi)))

    def predict(self, design) -> float:
        return self.gvf.predict(self._phi(design))

    def select_topk(self, candidates, k: int) -> list[int]:
        """从候选里选预测价值最高的 k 个（利用）—— headless 作为"设计/操作"选择器。"""
        vals = [self.predict(c) for c in candidates]
        order = np.argsort(-np.array(vals))[:k]
        return [int(i) for i in order]


# ───────────────────────── [C] 循环驱动器 ─────────────────────────
def run_round(agent, registry_path: Path, n_cand: int = 200, k: int = 10,
              round_i: int = 0, forge_root: Path | None = None):
    """一轮: 采样候选 → headless 选 top-K → 评估(forge 真评估 或 最近邻 oracle) → 学习。
    forge_root 给定时用 forge 真评估（headless 操作 forge 的硬验证）。
    """
    designs = load_designs(registry_path)
    known_x = np.array([d for d, _ in designs])
    known_s = np.array([s for _, s in designs])

    cands = candidate_designs(n_cand, seed=round_i)

    # headless 选 top-K（利用 GVF 预测价值）
    selected_idx = agent.select_topk(cands, k)
    selected_feats = cands[selected_idx]
    selected_raw = np.array([feature_to_design(f) for f in selected_feats])

    # 随机基线（同池随机选 k 个）
    rng = np.random.default_rng(round_i)
    rand_idx = rng.choice(n_cand, k, replace=False)
    rand_feats = cands[rand_idx]
    rand_raw = np.array([feature_to_design(f) for f in rand_feats])

    if forge_root is not None:
        # 真评估：把 headless 选的和随机选的各写 registry, 调 forge, 读真实 best_score
        sel_reg = forge_root / "runs" / "headless_sel" / "registry.jsonl"
        rand_reg = forge_root / "runs" / "headless_rand" / "registry.jsonl"
        write_headless_selection_to_registry(selected_raw, [0.0]*k, sel_reg)
        sel_res = run_forge_eval(forge_root, sel_reg, budget=50, seed=round_i)
        write_headless_selection_to_registry(rand_raw, [0.0]*k, rand_reg)
        rand_res = run_forge_eval(forge_root, rand_reg, budget=50, seed=round_i)
        sel_score = sel_res["best_score"] if sel_res["best_score"] is not None else float("nan")
        rand_score = rand_res["best_score"] if rand_res["best_score"] is not None else float("nan")
        # headless 用 forge 评估的全部 feasible 设计回流学习(不只 top-K —— 大幅增学习量)
        for (feat, s) in sel_res.get("evaluated", []):
            agent.learn(feat, s)
        for (feat, s) in rand_res.get("evaluated", []):
            agent.learn(feat, s)
    else:
        # 软验证: 用已知设计的最近邻 score 当 oracle（新候选 forge 没真算）
        oracle_scores = np.array([
            known_s[np.argmin(np.sum((known_x - c) ** 2, axis=1))] for c in cands
        ])
        sel_score = float(np.median(oracle_scores[selected_idx]))
        rand_score = float(np.median(oracle_scores[rand_idx]))
        # headless 学习（用 oracle 给的全池，模拟持续见到数据）
        rng = np.random.default_rng(round_i)
        for i in rng.choice(n_cand, min(n_cand, 50), replace=False):
            agent.learn(cands[i], oracle_scores[i])

    metric = {
        "round": round_i,
        "headless_topk_median": sel_score,
        "random_topk_median": rand_score,
        "lift": sel_score - rand_score,
        "n_learned": len(agent.history),
        "mode": "real_forge" if forge_root else "oracle",
    }
    return metric, agent


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--rounds", type=int, default=5)
    ap.add_argument("--n-cand", type=int, default=200)
    ap.add_argument("--k", type=int, default=10)
    ap.add_argument("--registry", default=str(ROOT / "runs" / "phase1" / "registry.jsonl"))
    ap.add_argument("--out", default=str(ROOT / "artifacts" / "continuous_loop.json"))
    ap.add_argument("--real", action="store_true",
                    help="驱动 forge 真评估(默认: 最近邻 oracle 软验证)")
    ap.add_argument("--forge-root", default=str(ROOT),
                    help="forge 仓库根(真评估时用; 服务器上是 /work/liuyuanjie/forge)")
    args = ap.parse_args()

    forge_root = Path(args.forge_root) if args.real else None
    agent = HeadlessDesignAgent(feature_dim=12)
    metrics = []
    for i in range(args.rounds):
        m, agent = run_round(agent, Path(args.registry), args.n_cand, args.k, i,
                             forge_root=forge_root)
        metrics.append(m)
        mode = "forge真评估" if args.real else "oracle近似"
        print(f"轮 {i} [{mode}]: headless={m['headless_topk_median']:.3f} "
              f"random={m['random_topk_median']:.3f} lift={m['lift']:+.3f} "
              f"已学 {m['n_learned']}")

    # 汇总验证
    lifts = [m["lift"] for m in metrics]
    print(f"\n=== 验证: {args.rounds} 轮 headless vs 随机基线 ===")
    print(f"  headless top-K score 中位数均值: {np.mean([m['headless_topk_median'] for m in metrics]):.3f}")
    print(f"  随机 top-K 中位数均值:           {np.mean([m['random_topk_median'] for m in metrics]):.3f}")
    print(f"  平均 lift:                        {np.mean(lifts):+.3f}")
    print(f"  连续改善(后3轮lift-前3轮lift):     {np.mean(lifts[-3:])-np.mean(lifts[:3]):+.3f}")

    out = Path(args.out)
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(json.dumps({"metrics": metrics, "summary": {
        "headless_mean": float(np.mean([m['headless_topk_median'] for m in metrics])),
        "random_mean": float(np.mean([m['random_topk_median'] for m in metrics])),
        "lift_mean": float(np.mean(lifts)),
        "continuous_improvement": float(np.mean(lifts[-3:])-np.mean(lifts[:3])),
    }}, indent=2))
    print(f"结果 → {out}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
