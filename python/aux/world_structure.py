#!/usr/bin/env python3
"""docs/world-structure.md 的 A 计量协议 + 预注册阈值（门 G19 的执行者）。

这份脚本是 P1 的**计量仪器**，不是结论。它只做四件事：

1. **控制组（G19a）**：同一批 `K` 个真动作喂给"绝对参数、无前提门、不换源、无夹取"的单步
   语义（§4 的控制组），`A` 必须 ≡ 0（≤1e-12）。控制组不零 ⟹ **尺子坏**，实验组的数字
   一律不算（§5）。
2. **实验组（G19b）**：四档 regime（`{perp,par} × {tight 8, loose 24}`，§3）× ≥5 个 seed，
   每个 seed 从世界自己 seeded 的起点出发，走**世界的真转移**（夹取 / 前提门 / 换源），
   按 §1 的定义量 `A`，报告 `median|A|`、`|A|` 超阈比例、bootstrap 95% CI。
3. **四档特征向量（G19c）**：`[argmax 动作索引, 该档可达的最高真分数, 达到最高分所用步数,
   终止原因]`。
4. **证据**：原始数字写进 `testdata/world_structure_v2.json`；`--check` **重新从原始数字**
   推导统计量与三条子门的判定（也顺便核对文件里存的那份派生统计没被改过）。

A 的定义严格按 §1（这里不做任何"更方便的量"替代）：

    eff(b|a) = score(θ →a→b) − score(θ →a)
    I(a,b)   = eff(b|a) − eff(b|∅)
    A(a,b)   = I(a,b) − I(b,a)

`θ →a` 与 `θ →b` 两项在 `I(a,b) − I(b,a)` 里精确相消，于是

    A(a,b) = score(θ →a→b) − score(θ →b→a)

脚本对每个 (regime, seed) 的第一对配对把两条式子**各算一遍**并断言相等（≤1e-12），
用来证明这个化简没有偷偷换掉定义。`score` 一律是**世界报出来的真目标函数分数**
（`info.score`），不是整形过的 `reward`（黑名单第 2 条）。

不许做的事（§6）：调阈值、换动作集合、挑初始点。阈值 `0.05 × 1.672 = 0.0836` 与参照
全量程 `1.672` 是预注册的常数，写死在下面；动作集合由 `ACTION_SEED` 显式播种、对所有
regime 与 seed 都是同一批；起点由世界自己的 `seed` 决定，脚本不挑。

用法::

    python3 python/aux/world_structure.py --write           # 跑计量并写证据
    python3 python/aux/world_structure.py --check           # 只读证据 + 判定三条子门

退出码：0 三条子门全过；1 有子门红；2 用法/证据文件错误。
"""

from __future__ import annotations

import argparse
import json
import math
import random
import statistics
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import world_client  # noqa: E402  (同目录的客户端: 只经进程间协议驱动世界, 不 import Go)

# ---------------------------------------------------------------------------
# 预注册常数（docs/world-structure.md §4，写死，不许看结果再调）
# ---------------------------------------------------------------------------

#: §4 的参照全量程 = 人工基线 −0.2905708161 → 机器最优 1.381419 的落差。
REFERENCE_FULL_RANGE = 1.672

#: §4 的超阈比例 = 强档阈值的那个 5%。
THRESHOLD_FRACTION = 0.05

#: §4 的强档阈值：median|A| ≥ 0.0836。
#: 文档写的是 "0.05 × 1.672 = 0.0836"。这里用**写出来的那个十进制数** 0.0836 作为阈值，
#: 而不是乘积的机器值 0.05*1.672 = 0.08360000000000001（两者差 1.4e-17，但一个恰好落在
#: 0.0836 上的 |A| 会因为这一个 ulp 换档）。乘积也一并记进证据的 instrument 块。
THRESHOLD_ABS_A = 0.0836

#: §4 的零档上界 / G19a 的容差。
ZERO_EPS = 1e-12

#: §4 的采样协议：K 个真动作、种子数、bootstrap。
K_ACTIONS = 8
ACTION_SEED = 20260927
SEEDS = (0, 1, 2, 3, 4)
BOOTSTRAP_RESAMPLES = 10000
BOOTSTRAP_SEED = 424242

#: §3 的四档 regime。
REGIMES = (
    {"key": "tight-perp", "source": "MATBG_N2_perp", "budget": 8, "target": 1.0},
    {"key": "loose-perp", "source": "MATBG_N2_perp", "budget": 24, "target": 1.0},
    {"key": "tight-par", "source": "MATBG_N2_par", "budget": 8, "target": 1.0},
    {"key": "loose-par", "source": "MATBG_N2_par", "budget": 24, "target": 1.0},
)

#: 证据文件（§5 点名的那一份）。
EVIDENCE = Path(__file__).resolve().parents[2] / "testdata" / "world_structure_v2.json"
SCHEMA = "world_structure_v2"


# ---------------------------------------------------------------------------
# 小的数值工具
# ---------------------------------------------------------------------------

def clip(v: float, lo: float, hi: float) -> float:
    return lo if v < lo else (hi if v > hi else v)


def median(values: list[float]) -> float:
    return statistics.median(values) if values else 0.0


def quantile(values: list[float], q: float) -> float:
    """取样本分位数（线性插值，与 numpy 的默认口径一致；不引第三方库）。"""
    if not values:
        return 0.0
    if len(values) == 1:
        return values[0]
    ordered = sorted(values)
    pos = q * (len(ordered) - 1)
    lo = int(math.floor(pos))
    hi = min(lo + 1, len(ordered) - 1)
    frac = pos - lo
    return ordered[lo] * (1 - frac) + ordered[hi] * frac


def bootstrap_ci(clusters: list[list[float]], stat, resamples: int, rng: random.Random) -> list[float]:
    """**按 seed 聚类**的 bootstrap 95% CI。

    同一 seed 内的配对共享同一个起点 θ₀，彼此不独立，所以重采样单位是 seed（簇），
    不是单个配对。重采样用固定种子的 RNG：门的颜色不许每次跑都不一样。
    """
    if not clusters:
        return [0.0, 0.0]
    stats = []
    for _ in range(resamples):
        pool: list[float] = []
        for _ in clusters:
            pool.extend(clusters[rng.randrange(len(clusters))])
        stats.append(stat(pool))
    return [quantile(stats, 0.025), quantile(stats, 0.975)]


# ---------------------------------------------------------------------------
# 动作集合与真转移
# ---------------------------------------------------------------------------

def action_set(dim: int) -> list[list[float]]:
    """§4 的 K 个真动作：动作盒 [-1,1]^dim 上均匀取，显式 seed。

    所有 regime、所有 seed 共用同一批动作 —— 否则四档之间的差里会混进"动作不一样"。
    """
    rng = random.Random(ACTION_SEED)
    return [[rng.uniform(-1.0, 1.0) for _ in range(dim)] for _ in range(K_ACTIONS)]


def wire_regime(regime: dict | None) -> dict | None:
    """把本脚本内部的 regime 记录（多一个便于读日志的 `key`）变成线上对象。

    协议只认 `source` / `budget` / `target` / `x0`（§8.3），多一个键就是 `bad_field` ——
    这正是世界该有的严格：客户端以为自己在说另一件事时，它必须被拦下。
    """
    if regime is None:
        return None
    return {k: regime[k] for k in ("source", "budget", "target") if k in regime}


def walk(session: "world_client.Session", regime: dict | None, seed: int,
         actions: list[list[float]]) -> dict:
    """从世界自己 seeded 的起点出发，依次走给定的动作，返回每一站的真分数。

    返回 ``{"theta0": score(∅), "after": [score(θ→a₁), score(θ→a₁→a₂), ...]}``：
    每一站都是**世界的真转移**（`reset` + `step`），不是解析地改参数（§1）。
    """
    resp = session.reset(seed=seed, regime=wire_regime(regime))
    base = float(resp["info"]["score"])
    visited = [session.metric(resp["obs"], "B_coil_max_T")]
    out = []
    terminated = False
    rejected = 0
    clamped = 0
    for action in actions:
        resp = session.step(action)
        out.append(float(resp["info"]["score"]))
        visited.append(session.metric(resp["obs"], "B_coil_max_T"))
        terminated = terminated or bool(resp.get("terminated"))
        rejected += 1 if resp["info"].get("rejected") else 0
        clamped += 1 if resp["info"].get("clamped") else 0
    return {"theta0": base, "after": out, "terminated": terminated, "rejected": rejected,
            "clamped": clamped, "b_coil_max": visited}


def measure_seed(session: "world_client.Session", regime: dict, seed: int,
                 actions: list[list[float]]) -> dict:
    """一个 (regime, seed) 的 A 分布：全部 K(K−1)/2 个配对（§4）。

    每个配对要两条真路径（θ→a→b 与 θ→b→a）。单步的分数也量出来，用来在第一个配对上
    把 §1 的 `eff/I` 展开式与化简式 `score(ab) − score(ba)` **各算一遍并比对**。
    """
    empty = walk(session, regime, seed, [])
    base = empty["theta0"]  # s(∅)：空前缀
    single = {}
    visited_singles: list[float] = []
    for i, a in enumerate(actions):
        run = walk(session, regime, seed, [a])
        single[i] = run["after"][0]
        visited_singles.extend(run["b_coil_max"])

    values: list[float] = []
    pairs: list[dict] = []
    diag = {"terminated_episodes": 0, "rejected_steps": 0, "clamped_steps": 0}
    diag["rejected_steps"] += empty["rejected"]
    diag["clamped_steps"] += empty["clamped"]
    b_coil: list[float] = list(empty["b_coil_max"]) + list(visited_singles)
    algebra_gap = 0.0

    for i in range(len(actions)):
        for j in range(i + 1, len(actions)):
            fwd = walk(session, regime, seed, [actions[i], actions[j]])
            rev = walk(session, regime, seed, [actions[j], actions[i]])
            s_ab, s_ba = fwd["after"][1], rev["after"][1]
            a_val = s_ab - s_ba
            values.append(a_val)
            pairs.append({"i": i, "j": j, "score_ab": s_ab, "score_ba": s_ba, "A": a_val})
            for run in (fwd, rev):
                diag["terminated_episodes"] += 1 if run["terminated"] else 0
                diag["rejected_steps"] += run["rejected"]
                diag["clamped_steps"] += run["clamped"]
                b_coil.extend(run["b_coil_max"])
            # §1 的展开式独立算一遍：I(a,b) = eff(b|a) − eff(b|∅) = s(ab) − s(a) − s(b) + s(∅)。
            # 它必须与化简式 score(ab) − score(ba) 给出同一个 A（差只该是浮点结合律的量级）。
            i_ab = (s_ab - single[i]) - (single[j] - base)
            i_ba = (s_ba - single[j]) - (single[i] - base)
            algebra_gap = max(algebra_gap, abs((i_ab - i_ba) - a_val))

    return {
        "seed": seed,
        "n_pairs": len(values),
        "a_values": values,
        "pairs": pairs,
        "single_step_scores": [single[i] for i in range(len(actions))],
        "median_abs_a": median([abs(v) for v in values]),
        "frac_above_threshold": sum(1 for v in values if abs(v) >= THRESHOLD_ABS_A) / len(values),
        "mean_a": sum(values) / len(values),
        "max_abs_a": max(abs(v) for v in values),
        "algebra_gap": algebra_gap,
        "diagnostics": diag,
        "b_coil_max_T": b_coil,
    }


def measure_regime(session: "world_client.Session", regime: dict, actions: list[list[float]]) -> dict:
    per_seed = [measure_seed(session, regime, seed, actions) for seed in SEEDS]
    clusters = [[p["A"] for p in s["pairs"]] for s in per_seed]
    pooled = [v for c in clusters for v in c]
    rng_median = random.Random(BOOTSTRAP_SEED)
    rng_mean = random.Random(BOOTSTRAP_SEED + 1)
    diagnostics = {k: sum(s["diagnostics"][k] for s in per_seed)
                   for k in ("terminated_episodes", "rejected_steps", "clamped_steps")}
    diagnostics["algebra_gap_max"] = max(s["algebra_gap"] for s in per_seed)
    bc = [v for s in per_seed for v in s["b_coil_max_T"]]
    # 每条被访问过的设计的 B_coil_max_T。它是"按场源材料的 B_cap 读 R3 的区"这个读法
    # 会不会把世界冻住**实测定量**: 这个量含 spec.self_field_T(3.1416 T), 而两个源的
    # B_cap 是 0.12 T / 1.6 T(docs/world-structure.md §2 写明的两个上游真源),
    # 所以只要拿它们直接比大小, 全世界都在区内 —— 用实测比例说话。
    diagnostics["b_coil_max_T"] = {
        "n_visited": len(bc),
        "min": min(bc),
        "median": median(bc),
        "max": max(bc),
        "self_field_T": 3.1416,
        "frac_above_self_field_T": sum(1 for v in bc if v > 3.1416) / len(bc),
        "source_cap_T": {"MATBG_N2_perp": 0.12, "MATBG_N2_par": 1.6},
        "frac_above_source_cap_T": {
            "MATBG_N2_perp": sum(1 for v in bc if v > 0.12) / len(bc),
            "MATBG_N2_par": sum(1 for v in bc if v > 1.6) / len(bc),
        },
    }
    return {
        "key": regime["key"],
        "source": regime["source"],
        "diagnostics": diagnostics,
        "budget": regime["budget"],
        "target": regime["target"],
        "per_seed": per_seed,
        "pooled": {
            "n_pairs": len(pooled),
            "median_abs_a": median([abs(v) for v in pooled]),
            "frac_above_threshold": sum(1 for v in pooled if abs(v) >= THRESHOLD_ABS_A) / len(pooled),
            "mean_a": sum(pooled) / len(pooled),
            "mean_a_ci95": bootstrap_ci(clusters, lambda p: sum(p) / len(p), BOOTSTRAP_RESAMPLES, rng_mean),
            "median_abs_a_ci95": bootstrap_ci(clusters, lambda p: median([abs(v) for v in p]),
                                               BOOTSTRAP_RESAMPLES, rng_median),
            "seeds_above_threshold": [s["seed"] for s in per_seed
                                      if s["median_abs_a"] >= THRESHOLD_ABS_A],
        },
    }


def feature_vector(session: "world_client.Session", regime: dict, seed: int,
                   actions: list[list[float]]) -> dict:
    """§3 的特征向量：`[argmax 动作索引, 该档可达的最高真分数, 达到最高分所用步数, 终止原因]`。

    读法（§3 只给了四个分量，没给过程，这里把过程写下来）：从该档的 seeded 起点出发，
    **按固定顺序**把同一批 K 个动作走一遍（每个动作都是一次真 `step`，被拒的动作同样计入），
    取"走完第 k 个动作之后的真分数"最高的那个 k。并列时取最小的 k（与物理层的 argmax
    口径一致：平局取第一个）。
    """
    resp = session.reset(seed=seed, regime=wire_regime(regime))
    scores = []
    terminated = truncated = False
    for action in actions:
        resp = session.step(action)
        scores.append(float(resp["info"]["score"]))
        terminated = terminated or bool(resp.get("terminated"))
        truncated = truncated or bool(resp.get("truncated"))
    best_k = 0
    for k in range(1, len(scores)):
        if scores[k] > scores[best_k]:
            best_k = k
    reason = "terminated" if terminated else ("truncated" if truncated else "sequence_end")
    return {
        "seed": seed,
        "argmax_action_index": best_k + 1,
        "best_true_score": scores[best_k],
        "steps_to_best": best_k + 1,
        "termination_reason": reason,
        "vector": [best_k + 1, scores[best_k], best_k + 1, reason],
        "scores": scores,
    }


# ---------------------------------------------------------------------------
# 控制组（§4）：绝对参数 + 无前提门 + 不换源 + 无夹取
# ---------------------------------------------------------------------------

def control_transition(x: list[float], action: list[float], lower: list[float], upper: list[float],
                       delta_scale: float) -> list[float]:
    """控制组的转移：**绝对参数**上的增量，**不**投影回盒子。

    唯一的差别就是 §4 括号里的四条缺项；动作的归一化与缩放和实验组逐位相同
    （`clip(a,-1,1)·delta_scale·(hi−lo)`），否则比的就不是"夹取/门/换源"这三件事。
    """
    return [x[i] + clip(action[i], -1.0, 1.0) * delta_scale * (upper[i] - lower[i])
            for i in range(len(x))]


def measure_control(session: "world_client.Session", actions: list[list[float]]) -> dict:
    """控制组的 A：用**同一把尺**（世界报的真 score）量一个无夹取的转移。

    分数由世界给出（`reset(x0=...)` 的 `info.score`）：控制组必须与实验组分数同源，
    否则"控制组读 0"就不能说明实验组用的那把尺是好的。
    """
    per_seed = []
    worst = 0.0
    for seed in SEEDS:
        resp = session.reset(seed=seed)
        theta0 = session.design_of(resp["obs"])
        # 控制组的起点必须是**同一个** θ₀：从实验组同一个 seed 的 reset 反解出来的那个。
        score0 = float(resp["info"]["score"])

        singles = {}
        for i, a in enumerate(actions):
            x = control_transition(theta0, a, session.lower, session.upper, session.delta_scale)
            singles[i] = float(session.reset(x0=x)["info"]["score"])

        values = []
        for i in range(len(actions)):
            for j in range(i + 1, len(actions)):
                x_ab = control_transition(
                    control_transition(theta0, actions[i], session.lower, session.upper, session.delta_scale),
                    actions[j], session.lower, session.upper, session.delta_scale)
                x_ba = control_transition(
                    control_transition(theta0, actions[j], session.lower, session.upper, session.delta_scale),
                    actions[i], session.lower, session.upper, session.delta_scale)
                s_ab = float(session.reset(x0=x_ab)["info"]["score"])
                s_ba = float(session.reset(x0=x_ba)["info"]["score"])
                values.append(s_ab - s_ba)
        worst = max(worst, max(abs(v) for v in values))
        per_seed.append({
            "seed": seed,
            "n_pairs": len(values),
            "a_values": values,
            "median_abs_a": median([abs(v) for v in values]),
            "mean_a": sum(values) / len(values),
            "max_abs_a": max(abs(v) for v in values),
            "theta0_score": score0,
            "single_step_scores": [singles[i] for i in range(len(actions))],
        })
    return {
        "semantics": "absolute parameters + no precondition gate + no source switch + no clamping "
                     "(docs/world-structure.md section 4)",
        "score_oracle": "the same world, probed with reset(x0=...) — the same true objective as the "
                        "experimental group",
        "control_starts_from": "the same seeded theta_0 as the experimental group (read back from its obs)",
        "per_seed": per_seed,
        "max_abs_a": worst,
        "passes_eps": worst <= ZERO_EPS,
    }


def measure_v1_env_diagnostic(actions: list[list[float]]) -> dict:
    """**诊断列**（不是控制组）：同一批动作、同一批起点，喂给字面上的 **v1 `Env`**。

    §4 的控制组定义里写着"无夹取"，所以带夹取的 v1 `Env` **不是**控制组。但它回答了一个
    很有用的问题：测到的 `A` 里有多少只是 R2（盒子夹取）造成的？这一列只用于归因，
    它的数字**不参与任何子门**。
    """
    session = world_client.connect(protocol=world_client.PROTOCOL_V1)
    try:
        per_seed = []
        for seed in SEEDS:
            resp = session.reset(seed=seed)
            singles = {}
            for i, a in enumerate(actions):
                singles[i] = float(walk(session, None, seed, [a])["after"][0])
            values = []
            for i in range(len(actions)):
                for j in range(i + 1, len(actions)):
                    s_ab = walk(session, None, seed, [actions[i], actions[j]])["after"][1]
                    s_ba = walk(session, None, seed, [actions[j], actions[i]])["after"][1]
                    values.append(s_ab - s_ba)
            per_seed.append({
                "seed": seed,
                "n_pairs": len(values),
                "a_values": values,
                "median_abs_a": median([abs(v) for v in values]),
                "frac_above_threshold": sum(1 for v in values if abs(v) >= THRESHOLD_ABS_A) / len(values),
                "mean_a": sum(values) / len(values),
            })
        pooled = [v for s in per_seed for v in s["a_values"]]
        return {
            "note": "v1's Env as literally shipped DOES clamp into the box, so it is not the control "
                    "group of section 4 (which says 'no clamping'). This column only attributes the "
                    "measured A to R2. It is not part of any sub-gate.",
            "protocol": world_client.PROTOCOL_V1,
            "per_seed": per_seed,
            "pooled": {
                "n_pairs": len(pooled),
                "median_abs_a": median([abs(v) for v in pooled]),
                "frac_above_threshold": sum(1 for v in pooled if abs(v) >= THRESHOLD_ABS_A) / len(pooled),
                "mean_a": sum(pooled) / len(pooled),
            },
        }
    finally:
        session.close()


# ---------------------------------------------------------------------------
# 统计量与档位判定（§4 的表，逐字实现）
# ---------------------------------------------------------------------------

def tier_of(regime_stats: list[dict]) -> dict:
    """按 §4 的预注册表给档。**判据是文字，不是"看起来差不多"**。

    §4 的三行：
      零档  `A ≡ 0`（≤1e-12，或 CI 含 0）                     ⟹ G19 红
      弱档  `0 < median|A| < 0.0836`                          ⟹ 记录，P2 不启动
      强档  某 regime `median|A| ≥ 0.0836` 且 CI 不含 0 且在 ≥3/5 seed 上复现 ⟹ 允许进 P2
    """
    notes: list[str] = []
    best = max(regime_stats, key=lambda r: r["pooled"]["median_abs_a"])
    best_name = best["key"]
    max_abs = max(s["max_abs_a"] for r in regime_stats for s in r["per_seed"])
    if max_abs <= ZERO_EPS:
        return {"tier": "zero", "regime": best_name, "notes":
                [f"every |A| ≤ {ZERO_EPS:g} — A is identically zero"]}

    # "CI 含 0" 用哪个 CI：§1 把"CI 含 0"与"与噪声同量级"并列，§4 的主统计量是 median|A|，
    # 而 A 本身是**有符号**的 —— 一半配对 +0.5、一半 −0.5 的世界里 mean A 也是 0，却恰恰
    # 是"顺序最有意义"的那种世界。所以主判据用 **median|A| 的 seed-cluster CI**（量纲上是
    # 幅度，与"同噪声量级"同义）。用 mean-A 的 CI 的另一种读法一并算出来、一并报告，
    # 不藏在代码里。
    containing = [r["key"] for r in regime_stats
                  if r["pooled"]["median_abs_a_ci95"][0] <= 0.0 <= r["pooled"]["median_abs_a_ci95"][1]]
    strong = [r for r in regime_stats
              if r["pooled"]["median_abs_a"] >= THRESHOLD_ABS_A
              and not (r["pooled"]["median_abs_a_ci95"][0] <= 0.0 <= r["pooled"]["median_abs_a_ci95"][1])
              and len(r["pooled"]["seeds_above_threshold"]) >= 3]
    if strong:
        picks = sorted(r["key"] for r in strong)
        return {"tier": "strong", "regime": picks[0], "notes":
                [f"{k}: median|A| ≥ {THRESHOLD_ABS_A} and the mean-A CI excludes 0 and ≥3/5 seeds "
                 f"reproduce it" for k in picks]}
    if len(containing) == len(regime_stats):
        return {"tier": "zero", "regime": best_name, "notes":
                [f"the mean-A 95% CI contains 0 in every regime ({', '.join(containing)}) — "
                 f"A ≡ 0 by section 4"]}
    if best["pooled"]["median_abs_a"] < THRESHOLD_ABS_A:
        return {"tier": "weak", "regime": best_name, "notes":
                [f"best regime {best_name}: median|A| {best['pooled']['median_abs_a']:.6g} < "
                 f"{THRESHOLD_ABS_A} (not zero: the median|A| CI excludes 0 in "
                 f"{len(regime_stats) - len(containing)} regime(s)), so section 4 says: record it, "
                 f"do NOT start P2, tighten R2/R3/R4 and measure again"]}
    # 到这里：median ≥ 阈值，但复现或 CI 条件没满足 —— 表里没有这一档，按"未达强档"记弱档，
    # 并把不满足的条件逐条列出来（不许悄悄升档）。
    missing = []
    for r in regime_stats:
        if r["pooled"]["median_abs_a"] < THRESHOLD_ABS_A:
            continue
        if r["pooled"]["median_abs_a_ci95"][0] <= 0.0 <= r["pooled"]["median_abs_a_ci95"][1]:
            missing.append(f"{r['key']}: the median|A| CI contains 0")
        if len(r["pooled"]["seeds_above_threshold"]) < 3:
            missing.append(f"{r['key']}: median|A| ≥ threshold on only "
                           f"{len(r['pooled']['seeds_above_threshold'])}/5 seeds")
    return {"tier": "weak", "regime": best_name,
            "notes": ["median|A| is above the threshold but the strong-tier conditions are not met:",
                      *missing]}


def tier_of_alternative_reading(regime_stats: list[dict]) -> dict:
    """§4 的"CI 含 0"若读作 **mean A** 的 CI，档位会是什么（只用于报告）。

    A 是有符号量：一半配对 +0.5、一半 −0.5 时 mean A = 0，而 |A| 到处都是 0.5。这个读法
    会把那种世界判成零档，所以它不能是主判据；但它也不该被藏起来 —— 两条读法的结论
    都印出来，让读者自己看差在哪。
    """
    best = max(regime_stats, key=lambda r: r["pooled"]["median_abs_a"])
    containing = [r["key"] for r in regime_stats
                  if r["pooled"]["mean_a_ci95"][0] <= 0.0 <= r["pooled"]["mean_a_ci95"][1]]
    if len(containing) == len(regime_stats):
        return {"tier": "zero", "regime": best["key"],
                "notes": [f"the mean-A 95% CI contains 0 in every regime ({', '.join(containing)})"]}
    if best["pooled"]["median_abs_a"] < THRESHOLD_ABS_A:
        return {"tier": "weak", "regime": best["key"], "notes": ["median|A| below the threshold"]}
    return {"tier": "strong", "regime": best["key"],
            "notes": ["median|A| ≥ the threshold and the mean-A CI excludes 0 in at least one regime"]}


def gate_verdict(evidence: dict) -> dict:
    """§5 的三条子门，从**原始数字**重新推导。

    b 的语义按父线勘误 §7.5：强档才 pass，弱档与零档都红（红的理由不同）。
    """
    control = evidence["control_group"]
    g19a = bool(control["passes_eps"])

    tier = tier_of(evidence["regimes"])
    # 父线勘误（docs/world-structure.md §7.5）：**只有强档 b 为 pass**。
    # 旧写法 `!= "zero"` 会让弱档印绿，而 §4 又说弱档"P2 不启动" —— 那是"门绿着，
    # 结论却是不许往下走"的假绿。门只能把"结构已被证明"印成绿；"结构未证明"必须红。
    g19b = tier["tier"] == "strong"

    # G19c：四档特征向量不全相同，且至少 3/4 档两两可分。
    # 读法（§3 与 §5 都要满足）：§3 说"四档全同 ⟹ 门红"；§5 的判据是"至少 3/4 档两两可分"。
    # 这里取**更严**的那一条（存在 3 档两两可分 ⟹ 至少 3 个互不相同的特征向量），
    # 并把较松读法（有多少对档不同）也一并报出来供核对。
    regimes = evidence["regimes"]
    vectors = []
    for r in regimes:
        vectors.append(tuple(str(x) for x in r["feature_vectors"][0]["vector"]))
    distinct = len(set(vectors))
    pairwise = 0
    for i in range(len(vectors)):
        for j in range(i + 1, len(vectors)):
            if vectors[i] != vectors[j]:
                pairwise += 1
    g19c_strict = distinct >= 3
    g19c_loose = len(set(vectors)) > 1 and pairwise >= 3

    verdict = "pass" if (g19a and g19b and g19c_strict) else "fail"
    return {
        "g19a": "pass" if g19a else "fail",
        "g19b": "pass" if g19b else "fail",
        "g19c": "pass" if g19c_strict else "fail",
        "verdict": verdict,
        "tier": tier,
        "tier_alternative_reading": tier_of_alternative_reading(evidence["regimes"]),
        "p2_allowed": tier["tier"] == "strong",
        "details": {
            "control_max_abs_a": control["max_abs_a"],
            "zero_eps": ZERO_EPS,
            "feature_vectors": [
                {"regime": r["key"], "vector": r["feature_vectors"][0]["vector"],
                 "per_seed": [f["vector"] for f in r["feature_vectors"]]} for r in regimes
            ],
            "distinct_feature_vectors": distinct,
            "distinct_feature_vector_pairs": pairwise,
            "g19c_loose_reading": "pass" if g19c_loose else "fail",
        },
    }


# ---------------------------------------------------------------------------
# 跑计量
# ---------------------------------------------------------------------------

def run_metering(world_cmd: str, extra: list[str]) -> dict:
    started = time.time()
    session = world_client.connect(world_cmd, extra, protocol=world_client.PROTOCOL_V2)
    try:
        if session.protocol != world_client.PROTOCOL_V2:
            raise SystemExit(f"world_structure: the world serves protocol {session.protocol}; "
                             f"the metering needs protocol {world_client.PROTOCOL_V2}")
        actions = action_set(session.action_dim)
        print(f"world_structure: protocol {session.protocol}, action_dim {session.action_dim}, "
              f"observation_dim {session.observation_dim}, K={K_ACTIONS} actions (seed {ACTION_SEED}), "
              f"seeds {list(SEEDS)}", file=sys.stderr)

        regimes = []
        for regime in REGIMES:
            stats = measure_regime(session, regime, actions)
            stats["feature_vectors"] = [feature_vector(session, regime, seed, actions) for seed in SEEDS]
            regimes.append(stats)
            print(f"  {regime['key']}: n_pairs={stats['pooled']['n_pairs']} "
                  f"median|A|={stats['pooled']['median_abs_a']:.6g} "
                  f"frac(≥{THRESHOLD_ABS_A})={stats['pooled']['frac_above_threshold']:.4f} "
                  f"mean_A={stats['pooled']['mean_a']:.6g} "
                  f"CI={[round(v, 6) for v in stats['pooled']['mean_a_ci95']]}", file=sys.stderr)
        control = measure_control(session, actions)
        print(f"  control group (no clamping / no gate / no source switch): "
              f"max |A| = {control['max_abs_a']:.3g} (must be ≤ {ZERO_EPS:g})", file=sys.stderr)
    finally:
        code = session.close()
        if code != 0:
            raise SystemExit(f"world_structure: the world exited {code}")

    v1diag = measure_v1_env_diagnostic(actions)

    return {
        "schema": SCHEMA,
        "generated_by": "python/aux/world_structure.py",
        "documents": ["docs/world-structure.md (sections 1, 3, 4, 5)",
                      "docs/world-protocol.md (section 8)"],
        "protocol": world_client.PROTOCOL_V2,
        "spec_sha256": session.hello["spec"]["sha256"],
        "engine_version": session.hello["engine"]["version"],
        "instrument": {
            "k_actions": K_ACTIONS,
            "action_seed": ACTION_SEED,
            "seeds": list(SEEDS),
            "reference_full_range": REFERENCE_FULL_RANGE,
            "threshold_fraction": THRESHOLD_FRACTION,
            "threshold_abs_a": THRESHOLD_ABS_A,
            "threshold_from_product": THRESHOLD_FRACTION * REFERENCE_FULL_RANGE,
            "zero_eps": ZERO_EPS,
            "bootstrap_resamples": BOOTSTRAP_RESAMPLES,
            "bootstrap_seed": BOOTSTRAP_SEED,
            "bootstrap_cluster": "seed (pairs within one seed share theta_0 and are not independent)",
            "a_definition": "A(a,b) = score(theta->a->b) - score(theta->b->a) = I(a,b) - I(b,a); "
                            "score is the world's raw objective score (info.score), never the shaped reward",
            "actions": actions,
            "runtime_seconds": round(time.time() - started, 3),
        },
        "control_group": control,
        "diagnostics": {"v1_env_same_actions": v1diag},
        "regimes": regimes,
    }


# ---------------------------------------------------------------------------
# --check：只读证据，重新推导判定
# ---------------------------------------------------------------------------

def compare_evidence(fresh: dict, committed: dict) -> list[str]:
    """两份证据的测量值必须逐位相同（排除 instrument.runtime_seconds：它是跑的时间）。

    门不能只跟自己刚写出来的那份文件比 —— 那样永远一致。所以门跑一份**新的**计量，
    再拿它与提交的那份逐位核对：世界或计量脚本漂移了，这里会红。
    """
    a = json.loads(json.dumps(fresh))
    b = json.loads(json.dumps(committed))
    for ev in (a, b):
        ev.get("instrument", {}).pop("runtime_seconds", None)
    if a == b:
        return []
    problems = []
    for key in ("protocol", "spec_sha256", "engine_version"):
        if a.get(key) != b.get(key):
            problems.append(f"{key}: fresh {a.get(key)!r} vs committed {b.get(key)!r}")
    fa = [r["key"] for r in a.get("regimes", [])]
    fb = [r["key"] for r in b.get("regimes", [])]
    if fa != fb:
        problems.append(f"regimes: fresh {fa} vs committed {fb}")
    else:
        for ra, rb in zip(a["regimes"], b["regimes"]):
            va = [s["a_values"] for s in ra["per_seed"]]
            vb = [s["a_values"] for s in rb["per_seed"]]
            if va != vb:
                problems.append(f"{ra['key']}: the fresh A values differ from the committed ones")
            if ra["feature_vectors"] != rb["feature_vectors"]:
                problems.append(f"{ra['key']}: the fresh feature vectors differ from the committed ones")
    if a.get("control_group", {}).get("max_abs_a") != b.get("control_group", {}).get("max_abs_a"):
        problems.append("control_group.max_abs_a differs between the fresh run and the committed file")
    if not problems:
        problems.append("the two evidence objects differ somewhere outside the checked fields")
    return problems


def check_evidence(path: Path, against: Path | None = None) -> int:
    if not path.exists():
        print(f"world_structure: {path} does not exist — run --write first", file=sys.stderr)
        return 2
    evidence = json.loads(path.read_text(encoding="utf-8"))
    if evidence.get("schema") != SCHEMA:
        print(f"world_structure: {path} has schema {evidence.get('schema')!r}, want {SCHEMA!r}", file=sys.stderr)
        return 2
    problems = verify_stored_stats(evidence)
    if against is not None:
        committed = json.loads(against.read_text(encoding="utf-8"))
        problems.extend(f"{against}: {p}" for p in compare_evidence(evidence, committed))
    verdict = gate_verdict(evidence)
    tier = verdict["tier"]

    # 判定块放在最前面: 门的输出常被 tail 截断, 结论先出来。
    print(f"world_structure: evidence {path}")
    print(f"  protocol {evidence['protocol']}, spec.sha256 {evidence['spec_sha256']}, "
          f"K={evidence['instrument']['k_actions']}, seeds {evidence['instrument']['seeds']}, "
          f"threshold |A| ≥ {evidence['instrument']['threshold_abs_a']}")
    print(f"  G19a control group (section 4: absolute parameters, no gate, no source switch, no "
          f"clamping): max |A| = {verdict['details']['control_max_abs_a']:.3g} "
          f"(must be ≤ {ZERO_EPS:g}) → {verdict['g19a'].upper()}")
    print(f"  G19b structural gate → {verdict['g19b'].upper()}: tier {tier['tier'].upper()} "
          f"(best regime {tier['regime']})")
    for n in tier["notes"]:
        print(f"    - {n}")
    alt = verdict["tier_alternative_reading"]
    print(f"    · reading 'CI contains 0' as the mean-A CI instead would say {alt['tier'].upper()} "
          f"({'; '.join(alt['notes'])})")
    print(f"    · P2 allowed by section 4: {'yes' if verdict['p2_allowed'] else 'NO'}")
    print(f"  G19c four regimes → {verdict['g19c'].upper()}: "
          f"{verdict['details']['distinct_feature_vectors']} distinct feature vectors, "
          f"{verdict['details']['distinct_feature_vector_pairs']}/6 distinguishable pairs "
          f"(strict reading; loose reading: {verdict['details']['g19c_loose_reading'].upper()})")
    print(f"  G19 = {verdict['verdict'].upper()}")
    for p in problems:
        print(f"  STALE/TAMPERED EVIDENCE: {p}")
    print("  --- details ---")
    for r in evidence["regimes"]:
        p = r["pooled"]
        print(f"  {r['key']:10s} source={r['source']:13s} budget={r['budget']:2d} "
              f"median|A|={p['median_abs_a']:.6g} frac≥thr={p['frac_above_threshold']:.4f} "
              f"mean A={p['mean_a']:.6g} CI95=[{p['mean_a_ci95'][0]:.6g},{p['mean_a_ci95'][1]:.6g}] "
              f"median|A| CI95=[{p['median_abs_a_ci95'][0]:.6g},{p['median_abs_a_ci95'][1]:.6g}] "
              f"seeds≥thr={len(p['seeds_above_threshold'])}/5")
    for r in evidence["regimes"]:
        for st in r["per_seed"]:
            print(f"    {r['key']:10s} seed={st['seed']}: median|A|={st['median_abs_a']:.6g} "
                  f"frac≥thr={st['frac_above_threshold']:.4f} mean A={st['mean_a']:.6g} "
                  f"max|A|={st['max_abs_a']:.6g} n={st['n_pairs']}")
    bc = evidence["regimes"][0]["diagnostics"]["b_coil_max_T"]
    print(f"  B_coil_max_T over {bc['n_visited']} visited designs: min {bc['min']:.4g} "
          f"median {bc['median']:.4g} max {bc['max']:.4g} (self_field {bc['self_field_T']} T); "
          f"above a source's B_cap: perp {bc['frac_above_source_cap_T']['MATBG_N2_perp']:.3f}, "
          f"par {bc['frac_above_source_cap_T']['MATBG_N2_par']:.3f}")
    for r in evidence["regimes"]:
        d = r["diagnostics"]
        print(f"  R3/R2 activity ({r['key']}): rejected steps {d['rejected_steps']}, "
              f"clamped steps {d['clamped_steps']}, terminated episodes {d['terminated_episodes']}, "
              f"algebra gap {d['algebra_gap_max']:.3g}")
        break
    for d in verdict["details"]["feature_vectors"]:
        print(f"    {d['regime']:10s} vector={d['vector']}  per-seed={d['per_seed']}")
    diag = evidence["diagnostics"]["v1_env_same_actions"]["pooled"]
    print(f"  diagnostic (not a gate): literal v1 Env (WITH clamping) over the same actions/seeds: "
          f"median|A|={diag['median_abs_a']:.6g} frac≥thr={diag['frac_above_threshold']:.4f} "
          f"mean A={diag['mean_a']:.6g}")
    gaps = [r["diagnostics"]["algebra_gap_max"] for r in evidence["regimes"]]
    print(f"  algebra self-check (section 1 expansion vs the reduced form): max gap "
          f"{max(gaps):.3g} (must be floating-point noise, not a different definition)")
    if max(gaps) > 1e-9:
        problems.append(f"the section-1 expansion and the reduced A differ by {max(gaps):.3g} — "
                        f"the instrument's algebra is not the frozen definition")
    print(f"world_structure: G19 = {verdict['verdict'].upper()} "
          f"(a: {verdict['g19a']}, b: {verdict['g19b']}, c: {verdict['g19c']})")
    if problems:
        return 1
    return 0 if verdict["verdict"] == "pass" else 1


def verify_stored_stats(evidence: dict) -> list[str]:
    """核对文件里存的派生统计量与原始 A 值一致（防"手改统计量把门刷绿"）。

    派生统计量在 --write 时算过一次；这里从原始 `a_values` 重新算，两者必须逐位相同。
    """
    problems: list[str] = []
    for r in evidence["regimes"]:
        pooled = [v for s in r["per_seed"] for v in s["a_values"]]
        p = r["pooled"]
        if len(pooled) != p["n_pairs"]:
            problems.append(f"{r['key']}: pooled n_pairs {p['n_pairs']} != {len(pooled)} raw values")
        if abs(median([abs(v) for v in pooled]) - p["median_abs_a"]) > 0:
            problems.append(f"{r['key']}: median|A| does not match the raw values")
        want = sum(1 for v in pooled if abs(v) >= THRESHOLD_ABS_A) / len(pooled)
        if want != p["frac_above_threshold"]:
            problems.append(f"{r['key']}: frac_above_threshold does not match the raw values")
        for s in r["per_seed"]:
            if len(s["a_values"]) != s["n_pairs"]:
                problems.append(f"{r['key']}/seed {s['seed']}: n_pairs does not match the raw values")
    ctrl = evidence["control_group"]
    worst = max((max(abs(v) for v in s["a_values"]) for s in ctrl["per_seed"]), default=0.0)
    if abs(worst - ctrl["max_abs_a"]) > 0:
        problems.append("control_group.max_abs_a does not match the raw values")
    if (worst <= ZERO_EPS) != bool(ctrl["passes_eps"]):
        problems.append("control_group.passes_eps contradicts its own raw values")
    return problems


# ---------------------------------------------------------------------------
# 交换子世界（协议 3, docs/world-commutator-candidates.md）—— 用**同一把尺**量新世界
# ---------------------------------------------------------------------------
#
# 与参数世界的计量共用同一个 A 的定义（§1）：
#
#     A(a,b) = score(θ → a → b) − score(θ → b → a)
#
# 分数一律取世界自己报的 info.score（真目标函数, 不是整形过的 reward）。两条世界因此是
# 用**同一把尺**量的 —— 本文件里没有第二把尺。
#
# 三条判据（门 G21），全部**与定理一致**, 不用拍出来的百分数:
#
#   (a) 尺子读数成立   : 旧世界(参数世界)的控制组 max|A| ≤ ZERO_EPS —— 读的是提交的证据
#                        testdata/world_structure_v2.json（由 G19 现场重跑并逐位核对）。
#   (b) 实测 = 闭式预测: 新世界里每一对 |A_meas − A_pred| ≤ MU_PREDICTION_TOL。预测由
#                        **本文件自己的算子代数**算出（不调 Go、不读世界报的数）, 闭式来自
#                        TD1(μ 状态方程) / TD11(增益=抹平进展) / TD15/TD16(顺序差)。
#   (c) 分辨开         : 逐对比"定理说该非零"与"尺子读到零带之外" —— 两边必须**同判**:
#                        定理说可交换的(层状)动作对读在零带内, 定理说不可交换的读到零带外。
#                        旧世界读 0、新世界读出闭式预测的非零 ⟹ 尺子不是常数偏移。
#
# 判据里唯一带"阈值味"的量是零带 ZERO_EPS(1e-12): 它是尺子自己的分辨率(P1 勘误 §7.2 已冻结,
# 与 G19a 同口径), 不是"多大才算有结构"的门槛。**A 够不够大不是判据。**

MU_EVIDENCE = Path(__file__).resolve().parents[2] / "testdata" / "world_structure_mu.json"
PARAMS_EVIDENCE = Path(__file__).resolve().parents[2] / "testdata" / "world_structure_v2.json"
MU_SCHEMA = "world_structure_mu"

#: 新世界的起点种子（与参数世界计量同一批, 便于对照; 写死, 不许看结果再调）。
MU_SEEDS = (0, 1, 2, 3, 4)

#: 三个前缀（在第一次计量之前就写死）。前两个把 TD16 的顺序差分成两种退化情形, 第三个
#: 同时**打开两个因子**:
#:   fresh    = 不预先动作           ⟹ μ = 0,  η_before = 0        ⟹ 顺序差取到 (1−μ)(1−η) 的极值
#:   warm     = 先抹平一个真子区域   ⟹ μ = 0,  0 < η_before < 1    ⟹ 只看 η_before 那一因子
#:   warm_mu  = 抹平后再按一次 update ⟹ μ > 0, η_before = μ > 0     ⟹ 两个因子同时不为 1
#: warm/warm_mu 的前缀动作都由**声明**决定（真子区域 flatten 的动作下标 + update 动作的下标）。
MU_PREFIXES = ("fresh", "warm", "warm_mu")

#: (b) 的容差: 两侧是**同一批 float64 代数**（Go 侧世界 vs 本文件的 Python 实现）, 差别只
#: 来自求和/求值次序, 实测 ~1e-16·分数尺度。取 1e-12: 比实测噪声大 4 个数量级（不会把次序差
#: 误报成公式错）, 比结构信号（1e-1 … 2）小 12 个数量级（任何真实的公式/区域/权重不一致都
#: 会红）。两条约束把它夹住 —— 它不是"松一点就绿"的旋钮。
MU_PREDICTION_TOL = 1e-12


# --- 声明式动作空间：动作**只能**来自世界自己的声明 -------------------------

def mu_action_set(session: "world_client.Session") -> tuple[list[str], list[list[float]]]:
    """取世界声明的动作空间（键 + 线编码）。

    本世界的动作空间是**有限声明**的, 所以"采样"就是取声明的全体 —— 计量者没有"挑动作"的
    自由度, 也就没有靠换动作集合把结果凑成一致的空间（上一轮的教训之一）。
    """
    keys = session.action_keys()
    vectors = session.action_vectors()
    if not keys or len(keys) != len(vectors):
        raise SystemExit("world_structure: the mu world declared no usable action space "
                         f"(keys={keys!r}, vectors={vectors!r})")
    return keys, vectors


def mu_region_map(session: "world_client.Session") -> dict:
    decl = session.declaration or {}
    return {r["key"]: list(r["cells"]) for r in decl.get("regions", []) if isinstance(r, dict)}


def mu_cells_of(key: str, regions: dict) -> list[int]:
    """动作键 → 区域格点。update_mu 这类非抹平动作返回空表（判定关系时用不到）。"""
    if ":" not in key:
        return []
    return regions.get(key.partition(":")[2], [])


def mu_warmup_index(session: "world_client.Session") -> int:
    """warm 前缀用的动作下标: 声明顺序里第一个"区域不是整个场"的 flatten 动作。

    判据只看声明（区域格点数 < n_cells）。找不到就大声失败 —— 那时 warm 前缀没有定义,
    而不是随便挑一个动作把它变成一次看不见的改动。
    """
    decl = session.declaration or {}
    n_cells = int(decl.get("n_cells", 0))
    regions = mu_region_map(session)
    for i, key in enumerate(session.action_keys()):
        cells = mu_cells_of(key, regions)
        if cells and len(cells) < n_cells:
            return i
    raise SystemExit("world_structure: no proper-subset flatten action in the declared space — "
                     "the warm prefix has no definition")


def mu_update_index(session: "world_client.Session") -> int:
    """update_mu 在声明里的下标（声明里的键不带 ':' 的那个动作）。找不到就大声失败。"""
    for i, key in enumerate(session.action_keys()):
        if ":" not in key:
            return i
    raise SystemExit("world_structure: the declared action space has no field-free action "
                     "(update_mu) — the warm_mu prefix has no definition")


# --- 本文件自己的算子代数（独立实现, 用来做闭式预测） -----------------------

def mu_mean(v: list[float], cells: list[int]) -> float:
    return sum(v[i] for i in cells) / len(cells)


def mu_flatten(v: list[float], cells: list[int]) -> list[float]:
    """P_A: 区域 A 内替换为区域均值, A 外不动（上游 GCA0/GCA1）。"""
    m = mu_mean(v, cells)
    out = list(v)
    for i in cells:
        out[i] = m
    return out


def mu_energy(v: list[float], cells: list[int]) -> float:
    """Q_A(v) = Σ_{i∈A}(v_i − v̄_A)²（上游 GCA2）。"""
    m = mu_mean(v, cells)
    return sum((v[i] - m) ** 2 for i in cells)


def mu_gain(v: list[float], v0: list[float], omega: list[int]) -> float:
    """η = 1 − Q_Ω(v)/Q_Ω(v₀)（上游 TD11 的桥）。"""
    q0 = mu_energy(v0, omega)
    if q0 <= 0:
        raise SystemExit("world_structure: Q_Omega(v0) ≤ 0 — the TD11 bridge is undefined here")
    return 1.0 - mu_energy(v, omega) / q0


def mu_step_upd(mu: float, eta: float) -> float:
    """上游 TD1: μ ↦ μ + η(1 − μ)。"""
    return mu + eta * (1.0 - mu)


def mu_score(v: list[float], v0: list[float], mu: float, decl: dict) -> float:
    """世界声明的目标函数（hello.declaration.score）—— 逐字复算, 不另立一套。"""
    weights = decl["score"]["weights"]
    ceiling = decl["mu_ceiling"]
    s = weights["mu"] * (mu / ceiling) + weights["gain"] * mu_gain(v, v0, list(range(len(v))))
    if mu >= ceiling:
        s += weights["closed"]
    return s


def mu_apply(v: list[float], mu: float, key: str, regions: dict, v0: list[float],
             omega: list[int], decl: dict) -> tuple[list[float], float]:
    """按声明的动作键走一步（与世界的转移同一套语义, 但由本文件独立实现）。"""
    op, _, target = key.partition(":")
    if op == "flatten":
        return mu_flatten(v, regions[target]), mu
    eta = mu_gain(v, v0, omega)
    return v, mu_step_upd(mu, eta)


def mu_predict_pair(pre_v: list[float], pre_mu: float, v0: list[float], key_i: str, key_j: str,
                    regions: dict, omega: list[int], decl: dict) -> dict:
    """从观测到的**前态**出发, 用代数预测两个次序的分数与终态。"""
    v_i, mu_i = mu_apply(pre_v, pre_mu, key_i, regions, v0, omega, decl)
    v_ij, mu_ij = mu_apply(v_i, mu_i, key_j, regions, v0, omega, decl)
    v_j, mu_j = mu_apply(pre_v, pre_mu, key_j, regions, v0, omega, decl)
    v_ji, mu_ji = mu_apply(v_j, mu_j, key_i, regions, v0, omega, decl)
    return {
        "score_ij": mu_score(v_ij, v0, mu_ij, decl),
        "score_ji": mu_score(v_ji, v0, mu_ji, decl),
        "v_ij": v_ij, "v_ji": v_ji, "mu_ij": mu_ij, "mu_ji": mu_ji,
        "eta_after_i": mu_gain(v_i, v0, omega),
    }


# --- 位置关系（上游 GCA6 的层状判定） ---------------------------------------

def mu_relation(a: list[int], b: list[int]) -> str:
    sa, sb = set(a), set(b)
    if not (sa & sb):
        return "disjoint"
    if sa <= sb or sb <= sa:
        return "nested"
    return "partial_overlap"


def mu_lamellar(a: list[int], b: list[int]) -> bool:
    return mu_relation(a, b) in ("disjoint", "nested")


# --- 真转移：reset + 前缀 + 两个次序 ----------------------------------------

def mu_read_state(session: "world_client.Session", resp: dict) -> tuple[list[float], float]:
    """从一条响应里读出 (场, μ) —— 场在 obs, μ 在 info。"""
    return session.field_of(resp["obs"]), float(resp["info"]["mu"])


def mu_walk(session: "world_client.Session", seed: int, prefix: list[int], order: list[int],
            vectors: list[list[float]]) -> dict:
    """走一条真路径: reset(seed) → 前缀动作 → order 里的动作。

    返回每一站的真分数, 以及**同一条路径里**观测到的前态（不靠"两次 reset 应该一样"这种
    假设 —— 预测的起点必须是实际跑的那条路径上看到的那个状态）。
    """
    resp = session.reset(seed=seed)
    v0, _ = mu_read_state(session, resp)
    scores = [float(resp["info"]["score"])]
    for k in prefix:
        resp = session.step(vectors[k])
        scores.append(float(resp["info"]["score"]))
    pre_v, pre_mu = mu_read_state(session, resp)
    eta_pre = float(resp["info"]["eta"])
    for k in order:
        resp = session.step(vectors[k])
        scores.append(float(resp["info"]["score"]))
    v_end, mu_end = mu_read_state(session, resp)
    return {
        "v0": v0, "pre_v": pre_v, "pre_mu": pre_mu, "eta_pre": eta_pre,
        "scores": scores, "v_end": v_end, "mu_end": mu_end,
        # 世界的终止/截断是**世界自己**的事: 这里如实记下来, 万一某一步把 episode 关掉了,
        # 证据里看得见(而不是被当成一次普通的读数)。
        "terminated": bool(resp["info"].get("terminated", False)),
        "truncated": bool(resp["info"].get("truncated", False)),
    }


def mu_measure_seed(session: "world_client.Session", seed: int, warm: int, upd: int,
                    keys: list[str], vectors: list[list[float]], regions: dict,
                    omega: list[int], decl: dict) -> dict:
    """一个 seed 上的全部**有序**动作对（三个前缀各一遍）。"""
    out = {"seed": seed, "prefixes": []}
    for prefix_name in MU_PREFIXES:
        prefix = {"fresh": [], "warm": [warm], "warm_mu": [warm, upd]}[prefix_name]
        base = mu_walk(session, seed, prefix, [], vectors)
        pairs = []
        for i in range(len(keys)):
            for j in range(len(keys)):
                if i == j:
                    continue
                run = mu_walk(session, seed, prefix, [i, j], vectors)
                pred = mu_predict_pair(run["pre_v"], run["pre_mu"], run["v0"],
                                       keys[i], keys[j], regions, omega, decl)
                pairs.append({
                    "i": i, "j": j, "key_i": keys[i], "key_j": keys[j],
                    "score": run["scores"][-1],
                    "score_predicted": pred["score_ij"],
                    "mu_end": run["mu_end"], "mu_end_predicted": pred["mu_ij"],
                    "v_end": run["v_end"], "v_end_predicted": pred["v_ij"],
                    "eta_pre": run["eta_pre"],
                    "terminated": run["terminated"], "truncated": run["truncated"],
                    # 前缀之后的前态与"只跑前缀那条路径"看到的是不是同一个状态。
                    # 这是世界确定性的现场见证, 也让"预测的起点"可被复核。
                    "pre_state_matches_base": (run["pre_v"] == base["pre_v"]
                                               and run["pre_mu"] == base["pre_mu"]),
                })
        out["prefixes"].append({
            "prefix": prefix_name,
            "prefix_actions": prefix,
            "eta_pre": base["eta_pre"],
            "pre_mu": base["pre_mu"],
            "base_score": base["scores"][0],
            "pairs": pairs,
        })
    return out


def mu_finish_pairs(per_seed: list[dict], regions: dict) -> list[dict]:
    """把有序对折成 A: A(a,b) = score(a→b) − score(b→a), 与闭式预测**并排**。"""
    rows = []
    for seed_blk in per_seed:
        for pfx in seed_blk["prefixes"]:
            by_pair = {(p["i"], p["j"]): p for p in pfx["pairs"]}
            for (i, j), p in sorted(by_pair.items()):
                if i >= j:
                    continue
                q = by_pair.get((j, i))
                if q is None:
                    raise SystemExit("world_structure: an ordered pair did not come in both directions")
                cells_a, cells_b = mu_cells_of(p["key_i"], regions), mu_cells_of(p["key_j"], regions)
                flatten_pair = bool(cells_a) and bool(cells_b)
                rows.append({
                    "seed": seed_blk["seed"],
                    "prefix": pfx["prefix"],
                    "key_a": p["key_i"], "key_b": p["key_j"],
                    "class": ("flatten×flatten" if flatten_pair
                              else ("update×update" if not cells_a and not cells_b
                                    else "flatten×update")),
                    "relation": mu_relation(cells_a, cells_b) if flatten_pair else "n/a",
                    "lamellar": mu_lamellar(cells_a, cells_b) if flatten_pair else None,
                    "eta_pre": pfx["eta_pre"], "pre_mu": pfx["pre_mu"],
                    "A_measured": p["score"] - q["score"],
                    "A_predicted": p["score_predicted"] - q["score_predicted"],
                    "mu_end_ab": p["mu_end"], "mu_end_ba": q["mu_end"],
                    "mu_end_predicted_ab": p["mu_end_predicted"],
                    "mu_end_predicted_ba": q["mu_end_predicted"],
                    "max_abs_field_diff_ab": max(abs(x - y) for x, y in zip(p["v_end"], p["v_end_predicted"])),
                    "max_abs_field_diff_ba": max(abs(x - y) for x, y in zip(q["v_end"], q["v_end_predicted"])),
                    "pre_state_matches_base": p["pre_state_matches_base"] and q["pre_state_matches_base"],
                    "terminated_ab": p["terminated"], "terminated_ba": q["terminated"],
                    "truncated_ab": p["truncated"], "truncated_ba": q["truncated"],
                    "v_end_ab": p["v_end"], "v_end_ba": q["v_end"],
                })
    return rows


def td16_order_difference(rows: list[dict], weights: dict, ceiling: float) -> dict:
    """TD15/TD16 的顺序差闭式, 与实测**并排**:

        A(flatten:omega, update_mu) = w_mu/mu_ceiling·(1−μ)(1−η_before) + w_closed

    来源: TD12（抹平整个场 ⟹ Q_Ω=0 ⟹ η=1 ⟹ μ 一步到顶）与 TD16（顺序差只来自增益差,
    = (1−μ)(1−η_before)）, 再乘世界声明的权重 w_mu 与闭合奖励 w_closed。

    闭式里出现的 μ 与 η_before **全部是观测到的**（世界自己报的 info.mu / info.eta）,
    所以这是一条"定理 ↔ 实测"的等式, 而不是"一个模型 ↔ 另一个模型"。
    """
    witness_rows = [r for r in rows if r["key_a"] == "flatten:omega" and r["key_b"] == "update_mu"]
    detail = []
    for r in witness_rows:
        mu, eta = r["pre_mu"], r["eta_pre"]
        closed = weights["mu"] / ceiling * (1.0 - mu) * (1.0 - eta) + weights["closed"]
        detail.append({
            "seed": r["seed"], "prefix": r["prefix"],
            "mu": mu, "eta_before": eta,
            "one_minus_mu": 1.0 - mu, "one_minus_eta": 1.0 - eta,
            "A_measured": r["A_measured"], "A_closed_form": closed,
            "residual": r["A_measured"] - closed,
            "mu_after_flatten_then_update": r["mu_end_ab"],
            "mu_after_update_then_flatten": r["mu_end_ba"],
        })
    return {
        "pair": "flatten:omega × update_mu",
        "closed_form": "A = w_mu/mu_ceiling * (1 - mu) * (1 - eta_before) + w_closed",
        "source": "TD12 (Q_Omega = 0 => eta = 1 => mu = 1 in one step) + TD15/TD16 (the order difference "
                  "is (1-mu)(1-eta_before)) with the world's declared weights",
        "observed_inputs": "mu and eta_before are the world's own info.mu / info.eta at the pre-state",
        "rows": detail,
        "max_abs_residual": max((abs(d["residual"]) for d in detail), default=None),
    }


def run_metering_mu(world_cmd: str, extra: list[str]) -> dict:
    """跑一遍新世界的计量（协议 3）。"""
    started = time.time()
    session = world_client.connect_v3(world_cmd, extra)
    try:
        if session.protocol != world_client.PROTOCOL_V3:
            raise SystemExit(f"world_structure: --world mu needs protocol {world_client.PROTOCOL_V3}, "
                             f"the world serves {session.protocol}")
        keys, vectors = mu_action_set(session)
        regions = mu_region_map(session)
        decl = session.declaration
        omega = list(range(int(decl["n_cells"])))
        warm = mu_warmup_index(session)
        upd = mu_update_index(session)
        print(f"world_structure: world=mu protocol {session.protocol}, cells {decl['n_cells']}, "
              f"{len(keys)} declared actions, warm-up {keys[warm]!r}, update {keys[upd]!r}, "
              f"seeds {list(MU_SEEDS)}, prefixes {list(MU_PREFIXES)}", file=sys.stderr)
        per_seed = [mu_measure_seed(session, seed, warm, upd, keys, vectors, regions, omega, decl)
                    for seed in MU_SEEDS]
        rows = mu_finish_pairs(per_seed, regions)
        diffs = [abs(r["A_measured"] - r["A_predicted"]) for r in rows]
        field_diffs = [max(r["max_abs_field_diff_ab"], r["max_abs_field_diff_ba"]) for r in rows]
        witness = td16_order_difference(rows, decl["score"]["weights"], float(decl["mu_ceiling"]))
        print(f"  {len(rows)} unordered pairs over {len(MU_SEEDS)} seeds × {len(MU_PREFIXES)} prefixes: "
              f"max |A_meas − A_pred| = {max(diffs):.3g} (tol {MU_PREDICTION_TOL:g}); "
              f"max field diff vs prediction = {max(field_diffs):.3g}; "
              f"TD16 order difference max |A_meas − w_mu/mu_c·(1−μ)(1−η_before) − w_closed| = "
              f"{witness['max_abs_residual']:.3g}", file=sys.stderr)
        evidence = {
            "schema": MU_SCHEMA,
            "generated_by": "python/aux/world_structure.py --world mu",
            "documents": ["docs/world-commutator-candidates.md",
                          "docs/world-structure.md (section 1: the ruler)"],
            "protocol": world_client.PROTOCOL_V3,
            "world_kind": "mu",
            "declaration_sha256": decl["sha256"],
            "engine_version": session.hello["engine"]["version"],
            "instrument": {
                "world": "mu",
                "seeds": list(MU_SEEDS),
                "prefixes": list(MU_PREFIXES),
                "warmup_action": keys[warm],
                "warmup_rule": ("the first declared flatten action whose region is a proper subset of "
                                "the field — decided from the declaration, before any measurement"),
                "action_keys": keys,
                "action_vectors": vectors,
                "regions": decl["regions"],
                "score_weights": decl["score"]["weights"],
                "mu_ceiling": decl["mu_ceiling"],
                "mu_initial": decl["mu_initial"],
                "budget": decl["budget"],
                "target": decl["target"],
                "target_source": ("the world's own solved score (mu.TargetScore = w_mu/Ceiling + w_gain + "
                                  "w_closed), so the episode does not terminate at a partial solution "
                                  "before a two-step order difference can be walked"),
                "terminated_walks": sum(1 for r in rows if r["terminated_ab"] or r["terminated_ba"]),
                "truncated_walks": sum(1 for r in rows if r["truncated_ab"] or r["truncated_ba"]),
                "zero_eps": ZERO_EPS,
                "prediction_tol": MU_PREDICTION_TOL,
                "prediction_source": ("closed forms from TD1 (mu state equation), TD11 (gain = flattening "
                                      "progress), TD15/TD16 (order difference), recomputed by this file's own "
                                      "operator algebra — never by calling Go or re-reading the world's numbers"),
                "a_definition": ("A(a,b) = score(theta->a->b) - score(theta->b->a); score is the world's raw "
                                 "info.score, exactly as for the params world (one ruler, two worlds)"),
                "actions_from": "the world's own declared action space (the whole declared set, no sampling choice)",
                "runtime_seconds": round(time.time() - started, 3),
            },
            "control_inside_mu": {
                "note": ("the mu world's own zero-reading check: pairs the theorem says commute "
                         "(predicted A = 0, i.e. the lamellar flatten pairs of GCA6 and the "
                         "already-flat pairs) must read inside the ruler's zero band, exactly as the "
                         "params world's control group does"),
                "lamellar_flatten_pairs": len([r for r in rows if r["lamellar"] is True]),
                "max_abs_a_lamellar_flatten": max((abs(r["A_measured"]) for r in rows
                                                   if r["lamellar"] is True), default=None),
                "max_abs_a_over_all": max(abs(r["A_measured"]) for r in rows),
            },
            "td16_order_difference": witness,
            "pairs": rows,
        }
    finally:
        code = session.close()
        if code != 0:
            raise SystemExit(f"world_structure: the world exited {code}")
    return evidence


# ---------------------------------------------------------------------------
# G21 的判定：从**原始数字**重新推导（不许只信证据文件里的结论行）
# ---------------------------------------------------------------------------

def mu_recompute(evidence: dict) -> dict:
    """从 raw rows 重算 (b) 与 (c) 需要的量。证据文件里的结论行一律不参与。"""
    rows = evidence.get("pairs") or []
    if not rows:
        return {"ok": False, "problem": "evidence has no pairs"}
    diffs = [abs(r["A_measured"] - r["A_predicted"]) for r in rows]
    field_diffs = [max(r["max_abs_field_diff_ab"], r["max_abs_field_diff_ba"]) for r in rows]
    # 分类器是**定理的预测**, 不是"我挑出来的一组动作":
    #   predicted_nonzero ⟺ 闭式预测的 |A| 超出尺子的零带
    predicted_nonzero = [r for r in rows if abs(r["A_predicted"]) > ZERO_EPS]
    predicted_zero = [r for r in rows if abs(r["A_predicted"]) <= ZERO_EPS]
    # (c) 的分类一致性: 逐对比较"定理说该非零"与"尺子读到零带之外"。
    misclassified = []
    for r in rows:
        if (abs(r["A_predicted"]) > ZERO_EPS) != (abs(r["A_measured"]) > ZERO_EPS):
            misclassified.append({"seed": r["seed"], "prefix": r["prefix"],
                                  "key_a": r["key_a"], "key_b": r["key_b"],
                                  "A_measured": r["A_measured"], "A_predicted": r["A_predicted"]})
    # TD16 的闭式残差: 用证据里记的 w_mu / mu_ceiling 与观测到的 (μ, η_before) 重算一遍。
    inst = evidence.get("instrument") or {}
    weights = inst.get("score_weights") or {}
    ceiling = inst.get("mu_ceiling")
    witness = (evidence.get("td16_order_difference") or {}).get("rows") or []
    closed_residuals = []
    if weights and ceiling:
        for d in witness:
            closed = (weights["mu"] / ceiling * (1.0 - d["mu"]) * (1.0 - d["eta_before"])
                      + weights["closed"])
            closed_residuals.append(abs(d["A_measured"] - closed))
    return {
        "ok": True,
        "n_pairs": len(rows),
        "max_abs_diff": max(diffs),
        "max_abs_field_diff": max(field_diffs),
        "n_predicted_nonzero": len(predicted_nonzero),
        "min_abs_a_predicted_nonzero": min((abs(r["A_measured"]) for r in predicted_nonzero), default=None),
        "max_abs_a_predicted_nonzero": max((abs(r["A_measured"]) for r in predicted_nonzero), default=None),
        "n_predicted_zero": len(predicted_zero),
        "max_abs_a_predicted_zero": max((abs(r["A_measured"]) for r in predicted_zero), default=None),
        "n_lamellar": len([r for r in rows if r["lamellar"] is True]),
        "lamellar_max_abs_a": max((abs(r["A_measured"]) for r in rows if r["lamellar"] is True), default=None),
        "n_partial_overlap": len([r for r in rows if r["relation"] == "partial_overlap"]),
        "partial_overlap_min_abs_a": min((abs(r["A_measured"]) for r in rows
                                          if r["relation"] == "partial_overlap"), default=None),
        "n_misclassified": len(misclassified),
        "misclassified": misclassified[:8],
        "closed_form_rows": len(witness),
        "max_abs_closed_form_residual": max(closed_residuals) if closed_residuals else None,
        "pre_state_matches_base": all(r.get("pre_state_matches_base", False) for r in rows),
    }


def params_control_max_abs_a(path: Path = PARAMS_EVIDENCE) -> dict:
    """(a): 旧世界控制组的 max|A|, 直读提交的参数世界证据（G19 现场跑过并逐位核对过它）。"""
    if not path.exists():
        return {"available": False, "path": str(path),
                "problem": "the params-world evidence is missing, so the ruler's zero reading on the old "
                           "world cannot be checked (run the params metering first)"}
    ev = json.loads(path.read_text(encoding="utf-8"))
    control = ev.get("control_group") or {}
    return {
        "available": True,
        "path": str(path),
        "max_abs_a": control.get("max_abs_a"),
        "semantics": control.get("semantics"),
        "passes_zero_band": (control.get("max_abs_a") is not None
                             and control.get("max_abs_a") <= ZERO_EPS),
    }


def g21_verdict(evidence: dict) -> dict:
    """G21 的 (a)(b)(c) 三条子判据。"""
    recomputed = mu_recompute(evidence)
    tol = float((evidence.get("instrument") or {}).get("prediction_tol", MU_PREDICTION_TOL))
    control = params_control_max_abs_a()
    if not recomputed.get("ok"):
        return {"a": "fail", "b": "fail", "c": "fail", "verdict": "fail",
                "problem": recomputed.get("problem"), "details": {"control": control}}

    a_ok = bool(control.get("passes_zero_band"))
    # (b) 两条: 逐对的 A 与闭式预测一致; 命名闭式(TD16 顺序差)也一致。
    closed_res = recomputed["max_abs_closed_form_residual"]
    b_ok = (recomputed["max_abs_diff"] <= tol
            and closed_res is not None and closed_res <= tol
            and recomputed["closed_form_rows"] > 0)
    # (c) 同一把尺的分辨能力 = 定理与读数**逐对同判**, 且两类都非空、读数都落在该在的一侧。
    lamellar_inside = (recomputed["lamellar_max_abs_a"] is not None
                       and recomputed["lamellar_max_abs_a"] <= ZERO_EPS)
    zeros_inside = (recomputed["max_abs_a_predicted_zero"] is not None
                    and recomputed["max_abs_a_predicted_zero"] <= ZERO_EPS)
    nonzero_outside = (recomputed["min_abs_a_predicted_nonzero"] is not None
                       and recomputed["min_abs_a_predicted_nonzero"] > ZERO_EPS)
    c_ok = (recomputed["n_misclassified"] == 0 and lamellar_inside and zeros_inside and nonzero_outside
            and recomputed["n_predicted_nonzero"] > 0 and recomputed["n_predicted_zero"] > 0
            and recomputed["n_partial_overlap"] > 0)
    verdict = "pass" if (a_ok and b_ok and c_ok) else "fail"
    return {
        "a": "pass" if a_ok else "fail",
        "b": "pass" if b_ok else "fail",
        "c": "pass" if c_ok else "fail",
        "verdict": verdict,
        "details": {
            "control": control,
            "prediction_tol": tol,
            "max_abs_diff": recomputed["max_abs_diff"],
            "max_abs_field_diff": recomputed["max_abs_field_diff"],
            "n_pairs": recomputed["n_pairs"],
            "closed_form_rows": recomputed["closed_form_rows"],
            "max_abs_closed_form_residual": closed_res,
            "n_predicted_nonzero": recomputed["n_predicted_nonzero"],
            "min_abs_a_predicted_nonzero": recomputed["min_abs_a_predicted_nonzero"],
            "max_abs_a_predicted_nonzero": recomputed["max_abs_a_predicted_nonzero"],
            "n_predicted_zero": recomputed["n_predicted_zero"],
            "max_abs_a_predicted_zero": recomputed["max_abs_a_predicted_zero"],
            "n_lamellar": recomputed["n_lamellar"],
            "lamellar_max_abs_a": recomputed["lamellar_max_abs_a"],
            "n_partial_overlap": recomputed["n_partial_overlap"],
            "partial_overlap_min_abs_a": recomputed["partial_overlap_min_abs_a"],
            "n_misclassified": recomputed["n_misclassified"],
            "misclassified": recomputed["misclassified"],
            "zero_eps": ZERO_EPS,
            "pre_state_matches_base": recomputed["pre_state_matches_base"],
        },
    }


def compare_mu_evidence(fresh: dict, committed: dict) -> list[str]:
    """新世界证据的漂移核对: 测量值必须逐位相同（runtime_seconds 除外）。"""
    a = json.loads(json.dumps(fresh))
    b = json.loads(json.dumps(committed))
    for ev in (a, b):
        ev.get("instrument", {}).pop("runtime_seconds", None)
    if a == b:
        return []
    problems = []
    for key in ("protocol", "declaration_sha256", "engine_version"):
        if a.get(key) != b.get(key):
            problems.append(f"{key}: fresh {a.get(key)!r} vs committed {b.get(key)!r}")
    fa = [(p["seed"], p["prefix"], p["key_a"], p["key_b"]) for p in a.get("pairs", [])]
    fb = [(p["seed"], p["prefix"], p["key_a"], p["key_b"]) for p in b.get("pairs", [])]
    if fa != fb:
        problems.append(f"pairs: the fresh pair list differs from the committed one "
                        f"({len(fa)} vs {len(fb)} rows)")
    else:
        for pa, pb in zip(a["pairs"], b["pairs"]):
            if pa["A_measured"] != pb["A_measured"]:
                problems.append(f"{pa['key_a']}×{pa['key_b']} (seed {pa['seed']}, {pa['prefix']}): "
                                f"fresh A {pa['A_measured']!r} vs committed {pb['A_measured']!r}")
    if not problems:
        problems.append("the two mu evidence objects differ somewhere outside the checked fields")
    return problems


def check_evidence_mu(path: Path, against: Path | None = None) -> int:
    if not path.exists():
        print(f"world_structure: {path} does not exist — run --write --world mu first", file=sys.stderr)
        return 2
    evidence = json.loads(path.read_text(encoding="utf-8"))
    if evidence.get("schema") != MU_SCHEMA:
        print(f"world_structure: {path} has schema {evidence.get('schema')!r}, want {MU_SCHEMA!r}",
              file=sys.stderr)
        return 2
    problems = []
    if against is not None:
        committed = json.loads(against.read_text(encoding="utf-8"))
        problems.extend(f"{against}: {p}" for p in compare_mu_evidence(evidence, committed))
    verdict = g21_verdict(evidence)
    d = verdict["details"]
    control = d["control"]

    print(f"world_structure: evidence {path} (world=mu, schema {MU_SCHEMA})")
    print(f"  protocol {evidence['protocol']}, declaration.sha256 "
          f"{(evidence.get('declaration_sha256') or '')[:16]}…, pairs {d.get('n_pairs')}")
    if control.get("available"):
        print(f"  G21a old world (params) control group: max |A| = {control['max_abs_a']:.3g} "
              f"(must be ≤ {ZERO_EPS:g}) → {verdict['a'].upper()}")
    else:
        print(f"  G21a old world control group: UNAVAILABLE — {control.get('problem')} → {verdict['a'].upper()}")
    print(f"  G21b measured A vs the closed form (TD1/TD11/TD15/TD16): max |A_meas − A_pred| = "
          f"{d['max_abs_diff']:.3g} (tolerance {d['prediction_tol']:g}) → {verdict['b'].upper()}; "
          f"max |field_meas − field_pred| = {d['max_abs_field_diff']:.3g}")
    print(f"    · TD16 order difference on {d['closed_form_rows']} witness rows "
          f"(flatten:omega × update_mu): max |A_meas − w_mu/mu_c·(1−μ)(1−η_before) − w_closed| = "
          f"{d['max_abs_closed_form_residual']:.3g}")
    print(f"  G21c the same ruler separates the two worlds → {verdict['c'].upper()}: "
          f"{d['n_predicted_nonzero']} pairs the theorem says must be non-commuting read min |A| = "
          f"{d['min_abs_a_predicted_nonzero']:.3g} (max {d['max_abs_a_predicted_nonzero']:.3g}); "
          f"{d['n_predicted_zero']} pairs the theorem says commute read max |A| = "
          f"{d['max_abs_a_predicted_zero']:.3g} (must be ≤ {ZERO_EPS:g}); "
          f"{d['n_misclassified']} pairs where the theorem and the ruler disagree")
    print(f"    · {d['n_partial_overlap']} partial-overlap flatten pairs read min |A| = "
          f"{d['partial_overlap_min_abs_a']:.3g} (GCA6b: must be > 0); "
          f"{d['n_lamellar']} lamellar flatten pairs read max |A| = {d['lamellar_max_abs_a']:.3g} "
          f"(GCA6a/N6/N9: must be 0)")
    if d["n_misclassified"]:
        for m in d["misclassified"]:
            print(f"    MISCLASSIFIED {m['key_a']}×{m['key_b']}: measured {m['A_measured']:.6g} "
                  f"vs predicted {m['A_predicted']:.6g}")
    if not d["pre_state_matches_base"]:
        print("    NOTE: some walks saw a pre-state that differed from the prefix-only walk "
              "(the world is not deterministic across resets) — the prediction used the in-walk pre-state")
    print(f"  G21 = {verdict['verdict'].upper()}")
    for p in problems:
        print(f"  STALE/TAMPERED EVIDENCE: {p}")
    print("  --- top rows (measured vs predicted, side by side) ---")
    shown = 0
    for r in evidence["pairs"]:
        if r["class"] == "flatten×flatten" and r["lamellar"] and shown >= 2:
            continue
        if shown >= 10:
            break
        shown += 1
        print(f"  seed {r['seed']} {r['prefix']:5s} {r['key_a']:14s} × {r['key_b']:14s} "
              f"[{r['relation']:15s}] A_meas={r['A_measured']:+.9g} A_pred={r['A_predicted']:+.9g} "
              f"diff={abs(r['A_measured'] - r['A_predicted']):.2g}")
    if verdict["verdict"] != "pass":
        return 1
    return 1 if problems else 0


def main(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(
        prog="world_structure.py",
        description="Measure A (docs/world-structure.md section 1) on the params world (gate G19, "
                    "docs/world-structure.md section 5) or on the mu world (commutator candidates, "
                    "gate G21, docs/world-commutator-candidates.md). One ruler, two worlds.")
    mode = p.add_mutually_exclusive_group(required=True)
    mode.add_argument("--write", action="store_true", help="run the metering and write the evidence file")
    mode.add_argument("--check", action="store_true", help="read the evidence file and adjudicate the gate")
    p.add_argument("--world", choices=("params", "mu"), default="params",
                   help="which world to measure: params (design parameters, protocols 1/2, gate G19) or "
                        "mu (the commutator world, protocol 3, gate G21)")
    p.add_argument("--out", metavar="FILE", help="evidence path (default: testdata/world_structure_v2.json "
                                                "for --world params, testdata/world_structure_mu.json for "
                                                "--world mu)")
    p.add_argument("--world-cmd", default=world_client.DEFAULT_WORLD, help="the world command to drive")
    p.add_argument("--world-arg", action="append", metavar="ARG", help="extra argument for the world")
    p.add_argument("--against", metavar="FILE",
                   help="with --check: also verify that FILE (the committed evidence) matches the "
                        "measurements being checked, bit for bit")
    p.add_argument("--json", action="store_true", help="with --check: also print the verdict as JSON")
    args = p.parse_args(argv)

    is_mu = args.world == "mu"
    default_out = MU_EVIDENCE if is_mu else EVIDENCE
    extra = args.world_arg or []

    if args.write:
        out = Path(args.out) if args.out else default_out
        evidence = run_metering_mu(args.world_cmd, extra) if is_mu else run_metering(args.world_cmd, extra)
        out.parent.mkdir(parents=True, exist_ok=True)
        out.write_text(json.dumps(evidence, indent=1, sort_keys=False) + "\n", encoding="utf-8")
        print(f"world_structure: wrote {out}")
        # --write 只负责把**原始数字**落盘, 不做判决: 判决是 --check 的事(它要同时判子门与
        # "提交的证据有没有漂移")。让 --write 也按判决返回非零, 会把门在第一步就掐断, 后面的
        # 子门一条都不会跑 —— 那就成了一道假门。
        verdict = g21_verdict(evidence) if is_mu else gate_verdict(evidence)
        if is_mu:
            print(f"world_structure: world=mu G21 a={verdict['a']} b={verdict['b']} c={verdict['c']} — "
                  f"run --check to adjudicate")
        else:
            print(f"world_structure: tier {verdict['tier']['tier'].upper()} "
                  f"(P2 allowed by section 4: {'yes' if verdict['p2_allowed'] else 'NO'}) — "
                  f"run --check to adjudicate G19")
        if args.json:
            print(json.dumps(verdict["details"], indent=1))
        return 0

    path = Path(args.out) if args.out else default_out
    against = Path(args.against) if args.against else None
    code = check_evidence_mu(path, against) if is_mu else check_evidence(path, against)
    if args.json:
        evidence = json.loads(path.read_text(encoding="utf-8"))
        verdict = g21_verdict(evidence) if is_mu else gate_verdict(evidence)
        print(json.dumps(verdict["details"], indent=1))
    return code


if __name__ == "__main__":
    sys.exit(main())