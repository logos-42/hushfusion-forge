#!/usr/bin/env python3
"""HUSHFUSION Forge 设计流水线的独立数值 oracle。

本模块是 Forge 的物理 + 目标函数定义在 numpy/scipy 上的 *重新实现*。
它不从 ``internal/`` (Go) 导入任何东西，也从不调用 Go 二进制：
跨语言 oracle 的全部意义就在于，它与被它检验的那个东西
不共享任何代码路径。

设备参数的唯一真实来源是 ``testdata/golden_spec.json``
(``config.Spec.AsMap()``)。这里不携带任何默认常量的私有副本 ——
每一个数字都来自传进来的 spec 文件，因此 Go 侧和 Python 侧
不可能悄悄漂移开。

模型 (v0.1)：圆电流丝的精确真空静磁学。没有等离子体、没有压强、
没有有限 beta 修正、没有涡流，也没有导体共享。
本文件必须命中的锚点见 CONTRACT.md §5。

CLI
    python3 python/aux/oracle.py --check-golden testdata/     # 验收门 G5
    python3 python/aux/oracle.py --compare-go <go_xcheck.json>
    python3 python/aux/oracle.py --emit-golden <dir>          # 供人工复核
"""

from __future__ import annotations

import argparse
import json
import math
import sys
from pathlib import Path

import numpy as np
from scipy.optimize import brentq
from scipy.special import ellipe, ellipk

MU0 = 4.0e-7 * math.pi

# 冻结的目标函数常量 (internal/objective/api.go)。
MIRROR_FLOOR = 0.2
MIRROR_MIN = 1.1
TERM_FIELD, TERM_MIRROR, TERM_VOLUME, TERM_RIPPLE, TERM_COST = (
    "field", "mirror", "volume", "ripple", "cost")
PEN_CONDUCTOR, PEN_SEPARATION, PEN_NOT_MIRROR = (
    "conductor_field", "coil_separation", "not_a_mirror")
TERM_KEYS = (TERM_FIELD, TERM_MIRROR, TERM_VOLUME, TERM_RIPPLE, TERM_COST)
PENALTY_KEYS = (PEN_CONDUCTOR, PEN_SEPARATION, PEN_NOT_MIRROR)

# 必须与 golden_baseline.json 匹配的指标键 (physics.Metrics 的 JSON tag)。
METRIC_KEYS = ("B_mid_T", "B_throat_T", "z_throat_m", "mirror_ratio", "volume_good",
               "ripple", "B_coil_max_T", "min_coil_gap_m", "cost_proxy", "mu0")

# ripple 结构显著性由指标层传入 (internal/physics/api.go)。
RIPPLE_PROMINENCE = 0.05
# 奇异闭式解在这个中心间距 (m) 处取下限 —— 与 Go 侧同一规则。
COIL_PROXIMITY_FLOOR = 5.0e-3

REPO_ROOT = Path(__file__).resolve().parents[2]


# --------------------------------------------------------------------------- #
# spec（设备规格）
# --------------------------------------------------------------------------- #

class Spec:
    """被设计的设备 + 评估窗口，从 AsMap 风格的 JSON 读入。"""

    def __init__(self, raw: dict):
        s = dict(raw)
        b = s["bounds"]
        self.raw = s
        self.n_coils = int(s["n_coils"])
        self.radius_bounds = tuple(float(v) for v in b["radius"])
        self.z_bounds = tuple(float(v) for v in b["z"])
        self.current_bounds = tuple(float(v) for v in b["current"])
        self.b_ref = float(s["b_ref"])
        self.mirror_ref = float(s["mirror_ref"])
        self.coil_field_limit = float(s["coil_field_limit"])
        self.j_eng = float(s["j_eng"])
        self.t_pack = float(s["t_pack"])
        self.r_plasma = float(s["r_plasma"])
        self.z_mid = float(s["z_mid"])
        self.z_cell = float(s["z_cell"])
        self.z_axis_max = float(s["z_axis_max"])
        self.n_axis = int(s["n_axis"])
        self.n_vol_r = int(s["n_vol_r"])
        self.n_vol_z = int(s["n_vol_z"])
        self.confine_factor = float(s["confine_factor"])
        self.min_coil_sep = float(s["min_coil_sep"])
        self.weights = {k: float(v) for k, v in s["weights"].items()}
        self.self_field = float(s["self_field_T"]) if "self_field_T" in s else self.self_field_anchor()

    # -- 构造函数 ----------------------------------------------------------- #
    @classmethod
    def load(cls, path) -> "Spec":
        return cls(json.loads(Path(path).read_text())["spec"])

    @classmethod
    def from_map(cls, m: dict) -> "Spec":
        return cls(m)

    @property
    def n_params(self) -> int:
        return 3 * self.n_coils

    def self_field_anchor(self) -> float:
        """mu0 * j_eng * t_pack / 2 -- 绕组包导体场的锚点。"""
        return MU0 * self.j_eng * self.t_pack / 2.0

    def lower(self):
        return np.array([self.radius_bounds[0]] * self.n_coils
                        + [self.z_bounds[0]] * self.n_coils
                        + [self.current_bounds[0]] * self.n_coils)

    def upper(self):
        return np.array([self.radius_bounds[1]] * self.n_coils
                        + [self.z_bounds[1]] * self.n_coils
                        + [self.current_bounds[1]] * self.n_coils)


# --------------------------------------------------------------------------- #
# 场计算
# --------------------------------------------------------------------------- #

def elliptic_ke(m):
    """参数为 m = k^2 的完全椭圆积分 K(m)、E(m)。

    scipy.special.ellipk/ellipe 接受 m (= k^2)，与 internal/physics/api.go 中
    冻结的约定一致。这里是一份 *独立* 实现，与 Go 侧被要求使用的 AGM
    方案不同 —— 算法不同，函数相同。
    """
    return ellipk(m), ellipe(m)


def loop_field(radius, current, r, z, proximity_floor=0.0):
    """一个圆心在 z = 0、轴沿 z 的圆电流环的 (Br, Bz)。

    闭式解 (Simpson et al., NASA/TM-2001-211135)，其中
        s      = a^2 + r^2 + z^2
        alpha2 = s - 2 a r ,  beta2 = s + 2 a r ,  m = 1 - alpha2/beta2
        C      = mu0 I / pi

        Br = C z / (2 alpha2 beta r) [ s E(m) - alpha2 K(m) ]
        Bz = C   / (2 alpha2 beta)   [ (a^2 - r^2 - z^2) E(m) + alpha2 K(m) ]

    关于一处文档 bug 的说明：internal/physics/api.go 打印的 Br 括号式 *没有*
    1/r 因子 ("B_r = C*z/(2*alpha2*beta) * [...]")。这一形式与 golden 场样本
    相差恰好 r 倍（例如在 golden_field_samples.json 的近喉部探针点上，它
    返回 -1.2185 T，而 golden 文件以及直接做 Biot-Savart 求积都给出
    -4.05 T）。golden 值胜出：这里实现的是物理上正确的 1/r，而这个遗漏
    被当作契约缺陷上报，而不是被复现。
    （阶段 A 的 magnet.go 独立得出了同样的结论，并同样恢复了 1/r。）

    proximity_floor：当 > 0 时，到最近导线点的平方距离 (alpha2) 低于 floor^2
    的样本点，会在 alpha2 被夹到 floor^2 的情况下求值，这正是 Go 的
    AnalyticSolver 在导线附近的做法（闭式解在那里是奇异的）。默认 0.0 =
    精确闭式解 —— golden 锚点正是用它生成的，而且它也是更严格的比较。
    要跟包含近导线探针的 Go 场导出做同口径对比，就传 5e-3。

    在轴上 (r -> 0) 使用精确极限：Br = 0, Bz = mu0 I a^2 /
    (2 (a^2 + z^2)^1.5)。
    """
    r = np.asarray(r, dtype=float)
    z = np.asarray(z, dtype=float)
    on_axis = r == 0.0
    rs = np.where(on_axis, 1.0, r)          # 占位值，在轴上永远不会被用到
    s = radius * radius + r * r + z * z
    alpha2 = s - 2.0 * radius * r
    beta2 = s + 2.0 * radius * r
    if proximity_floor > 0.0:
        alpha2 = np.maximum(alpha2, proximity_floor * proximity_floor)
    m = 1.0 - alpha2 / beta2
    with np.errstate(invalid="ignore", divide="ignore", over="ignore"):
        k, e = elliptic_ke(m)
        pref = MU0 * current / (math.pi * 2.0 * alpha2 * np.sqrt(beta2))
        br = pref * (z / rs) * (s * e - alpha2 * k)
        bz = pref * ((radius * radius - r * r - z * z) * e + alpha2 * k)
    bz_axis = MU0 * current * radius * radius / (2.0 * (radius * radius + z * z) ** 1.5)
    return np.where(on_axis, 0.0, br), np.where(on_axis, bz_axis, bz)


def loop_field_discrete(radius, current, r, z, n_seg=512):
    """对每个环的 n_seg 段直导线做独立的 Biot-Savart 求和。

    在精确的 Biot-Savart 积分上用中点法则，在笛卡尔坐标下求值 ——
    全程不出现椭圆积分，这正是它构成对 loop_field 的独立校验的原因。
    """
    phi = (np.arange(n_seg) + 0.5) * (2.0 * math.pi / n_seg)
    sx, sy = radius * np.cos(phi), radius * np.sin(phi)
    dlx, dly = -(2.0 * math.pi * radius / n_seg) * np.sin(phi), (2.0 * math.pi * radius / n_seg) * np.cos(phi)
    r = np.atleast_1d(np.asarray(r, dtype=float))
    z = np.atleast_1d(np.asarray(z, dtype=float))
    br = np.zeros_like(r)
    bz = np.zeros_like(z)
    for i in range(r.size):
        rx, ry, rz = r[i] - sx, -sy, z[i] - np.zeros_like(sx)
        rn = (rx * rx + ry * ry + rz * rz) ** 1.5
        cx, cy, cz = dly * rz, -dlx * rz, dlx * ry - dly * rx
        fac = MU0 * current / (4.0 * math.pi)
        br[i] = fac * np.sum(cx / rn)
        bz[i] = fac * np.sum(cz / rn)
    return br, bz


def coilset_field(coils, r, z, proximity_floor=0.0):
    """一个线圈组的矢量和 (Br, Bz)；线圈是 (radius, z, current)。"""
    r = np.asarray(r, dtype=float)
    z = np.asarray(z, dtype=float)
    br = np.zeros_like(r)
    bz = np.zeros_like(r)
    for a, zc, cur in coils:
        b1, b2 = loop_field(a, cur, r, z - zc, proximity_floor=proximity_floor)
        br = br + b1
        bz = bz + b2
    return br, bz


def coilset_magnitude(coils, r, z, proximity_floor=0.0):
    br, bz = coilset_field(coils, r, z, proximity_floor=proximity_floor)
    return np.sqrt(br * br + bz * bz)


def on_axis_field(coils, z):
    return coilset_magnitude(coils, np.zeros_like(np.asarray(z, dtype=float)), z)


# --------------------------------------------------------------------------- #
# 设计向量编解码
# --------------------------------------------------------------------------- #

def vector_to_coils(x, spec: Spec):
    """解码设计向量：夹到盒内，按 z 升序做规范化。"""
    x = np.asarray(x, dtype=float)
    if x.size != spec.n_params:
        raise ValueError(f"design vector has {x.size} entries, spec wants {spec.n_params}")
    k = spec.n_coils
    clipped = np.clip(x, spec.lower(), spec.upper())
    coils = np.column_stack([clipped[:k], clipped[k:2 * k], clipped[2 * k:3 * k]])
    order = np.argsort(coils[:, 1], kind="stable")
    return [(float(a), float(zz), float(i)) for a, zz, i in coils[order]]


def coils_to_vector(coils):
    c = sorted(coils, key=lambda t: t[1])
    k = len(c)
    return [c[i][0] for i in range(k)] + [c[i][1] for i in range(k)] + [c[i][2] for i in range(k)]


# --------------------------------------------------------------------------- #
# 网格 + 指标
# --------------------------------------------------------------------------- #

class Grids:
    """堆叠的样本点：[轴 (r=0) | 中平面体积 | 单元体积]。"""

    def __init__(self, spec: Spec):
        self.axis_z = np.linspace(-spec.z_axis_max, spec.z_axis_max, spec.n_axis)
        self.axis_in_cell = np.abs(self.axis_z) <= spec.z_cell
        mr = np.linspace(0.0, spec.r_plasma, spec.n_vol_r)
        mz = np.linspace(-spec.z_mid, spec.z_mid, 5)
        cr = np.linspace(0.0, spec.r_plasma, spec.n_vol_r)
        cz = np.linspace(-spec.z_cell, spec.z_cell, spec.n_vol_z)
        MR, MZ = np.meshgrid(mr, mz, indexing="ij")
        CR, CZ = np.meshgrid(cr, cz, indexing="ij")
        self.mid_r, self.mid_z = MR.ravel(), MZ.ravel()
        self.cell_r, self.cell_z = CR.ravel(), CZ.ravel()
        self.n_axis = self.axis_z.size
        self.n_mid = self.mid_r.size
        self.stack_r = np.concatenate([np.zeros(self.n_axis), self.mid_r, self.cell_r])
        self.stack_z = np.concatenate([self.axis_z, self.mid_z, self.cell_z])

    def split_magnitude(self, mag):
        a = self.n_axis
        b = a + self.n_mid
        return mag[:a], mag[a:b], mag[b:]


def axis_ripple(b_axis_cell, b_mid, prominence=RIPPLE_PROMINENCE):
    """轴上非单调结构的归一化幅度。

    内部极值由三点比较找出；相邻且交替出现的 (max, min) 极值，在结构深度
    超过 prominence * b_mid 时贡献 |peak - valley|。结果再除以 b_mid，
    因此单调或只有一个峰的剖面会恰好返回 0。
    """
    b = np.asarray(b_axis_cell, dtype=float)
    if b.size < 3 or not np.isfinite(b_mid) or b_mid <= 0.0:
        return 0.0
    ext = []
    for i in range(1, b.size - 1):
        if b[i - 1] < b[i] and b[i] > b[i + 1]:
            ext.append((i, +1))
        elif b[i - 1] > b[i] and b[i] < b[i + 1]:
            ext.append((i, -1))
    total = 0.0
    for (i0, s0), (i1, s1) in zip(ext, ext[1:]):
        if s0 == s1:
            continue
        depth = abs(b[i0] - b[i1])
        if depth > prominence * b_mid:
            total += depth
    return total / b_mid


def min_coil_gap(coils):
    if len(coils) < 2:
        return math.inf
    best = math.inf
    for i in range(len(coils)):
        for j in range(i + 1, len(coils)):
            best = min(best, math.hypot(coils[i][0] - coils[j][0], coils[i][1] - coils[j][1]))
    return best


def _displace_to_floor(coils, k, floor=COIL_PROXIMITY_FLOOR):
    """如果线圈 k 与另一个中心的距离小于 `floor`，就返回一个代理点。"""
    a, zc, _ = coils[k]
    for j, (aj, zj, _) in enumerate(coils):
        if j == k:
            continue
        d = math.hypot(a - aj, zc - zj)
        if d < floor:
            if d == 0.0:
                return a + floor, zc
            s = floor / d
            return a + (a - aj) * (s - 1.0), zc + (zc - zj) * (s - 1.0)
    return a, zc


def metrics_for(coils, spec: Spec, grids: Grids, proximity_floor=0.0):
    """一个线圈组所有与目标函数相关的指标（与 physics.Metrics 对齐）。

    `proximity_floor` 镜像 Go 求解器的 alpha2 夹取。Go 侧对 *每一个* 距离
    导线小于该下限的样本点都做夹取，因此任何「网格触到导体」的设计做
    同口径跨语言比较时都必须传同一个下限；不传的话，精确闭式解会返回
    未夹取的（奇异的）值，两边就按构造「不一致」了。默认 0.0 使得冻结
    的 golden（由精确形式生成）继续有效。
    """
    mag = coilset_magnitude(coils, grids.stack_r, grids.stack_z,
                            proximity_floor=proximity_floor)
    axis, mid, cell = grids.split_magnitude(mag)
    b_mid = float(mid.mean())
    i_throat = int(np.argmax(axis))
    b_throat = float(axis[i_throat])
    volume_good = float(np.mean(cell <= spec.confine_factor * b_mid)) if cell.size else 0.0
    ripple = axis_ripple(axis[grids.axis_in_cell], b_mid, RIPPLE_PROMINENCE)
    self_field = spec.self_field

    proximity_floor_hit = False
    b_coil_max = 0.0
    for k in range(len(coils)):
        pt_r, pt_z = _displace_to_floor(coils, k)
        if (pt_r, pt_z) != (coils[k][0], coils[k][1]):
            proximity_floor_hit = True
        total = 0.0
        for j in range(len(coils)):
            if j == k:
                continue
            aj, zj, ij = coils[j]
            br, bz = loop_field(aj, ij, np.array([pt_r]), np.array([pt_z - zj]))
            total += float(math.hypot(br[0], bz[0]))   # 幅值之和，不是 |向量和|
        b_coil_max = max(b_coil_max, total + self_field)

    cost = float(sum(i * i * a for a, _, i in coils))
    return {
        "B_mid_T": b_mid,
        "B_throat_T": b_throat,
        "z_throat_m": float(grids.axis_z[i_throat]) if axis.size else 0.0,
        "mirror_ratio": b_throat / b_mid if b_mid else 0.0,
        "volume_good": volume_good,
        "ripple": float(ripple),
        "B_coil_max_T": float(b_coil_max),
        "min_coil_gap_m": float(min_coil_gap(coils)),
        "cost_proxy": cost,
        "coil_proximity_floor_hit": bool(proximity_floor_hit),
        "n_coils": int(len(coils)),
        "mu0": MU0,
    }


# --------------------------------------------------------------------------- #
# 目标函数
# --------------------------------------------------------------------------- #

def evaluate(x, spec: Spec, cost_ref: float, grids: Grids, proximity_floor=0.0):
    """一个设计的 score / terms / weighted / penalties / feasible / metrics。"""
    coils = vector_to_coils(x, spec)
    m = metrics_for(coils, spec, grids, proximity_floor=proximity_floor)
    w = spec.weights
    terms = {
        TERM_FIELD: math.log10(max(m["B_mid_T"], 1e-9) / spec.b_ref),
        TERM_MIRROR: math.log10(max(m["mirror_ratio"], MIRROR_FLOOR) / spec.mirror_ref),
        TERM_VOLUME: m["volume_good"],
        TERM_RIPPLE: m["ripple"],
        TERM_COST: m["cost_proxy"] / cost_ref if cost_ref else 0.0,
    }
    weighted = {
        TERM_FIELD: w["field"] * terms[TERM_FIELD],
        TERM_MIRROR: w["mirror"] * terms[TERM_MIRROR],
        TERM_VOLUME: w["volume"] * terms[TERM_VOLUME],
        TERM_RIPPLE: -w["ripple"] * terms[TERM_RIPPLE],
        TERM_COST: -w["cost"] * terms[TERM_COST],
    }
    penalties = {
        PEN_CONDUCTOR: max(0.0, m["B_coil_max_T"] / spec.coil_field_limit - 1.0),
        PEN_SEPARATION: max(0.0, (spec.min_coil_sep - m["min_coil_gap_m"]) / spec.min_coil_sep),
        PEN_NOT_MIRROR: max(0.0, (MIRROR_MIN - m["mirror_ratio"]) / MIRROR_MIN),
    }
    score = (sum(weighted.values()) - w["penalty"] * sum(penalties.values()))
    return {
        "score": score,
        "terms": terms,
        "weighted": weighted,
        "penalties": penalties,
        "feasible": all(p <= 0.0 for p in penalties.values()),
        "metrics": m,
        "design": coils_to_vector(coils),
        "coils": coils,
    }


# --------------------------------------------------------------------------- #
# 人工基线
# --------------------------------------------------------------------------- #

DEFAULT_GEOM = {"r_cell": 0.50, "half_gap_cell": 0.25, "r_throat": 0.30,
                "z_throat": 1.00, "throat_current_ratio": 3.5}


def textbook_mirror(spec: Spec, geom=None, grids=None):
    """类 Helmholtz 的中心单元 + 两个镜像喉部，单元电流是 *求解* 出来的。

    单元电流不是猜的：brentq 把中平面体积平均 |B| 精确驱动到 spec.b_ref
    （区间 [current_min, current_max/ratio] 让喉部电流留在搜索盒内，因此
    设计在被重新编码时不会被夹断 —— 门 G6）。喉部电流是单元电流的
    `ratio` 倍。
    """
    g = dict(DEFAULT_GEOM)
    if geom:
        g.update(geom)
    grids = grids or Grids(spec)
    a_cell, half_gap = g["r_cell"], g["half_gap_cell"]
    a_throat, z_throat, ratio = g["r_throat"], g["z_throat"], g["throat_current_ratio"]

    def b_mid_of(cell_current):
        coils = [(a_throat, -z_throat, ratio * cell_current),
                 (a_cell, -half_gap, cell_current),
                 (a_cell, half_gap, cell_current),
                 (a_throat, z_throat, ratio * cell_current)]
        return float(coilset_magnitude(coils, grids.mid_r, grids.mid_z).mean())

    lo = spec.current_bounds[0]
    hi = spec.current_bounds[1] / ratio
    f_lo, f_hi = b_mid_of(lo) - spec.b_ref, b_mid_of(hi) - spec.b_ref
    if f_lo * f_hi > 0:                       # pragma: no cover - config guard
        raise RuntimeError(f"cannot bracket the cell current: f({lo})={f_lo}, f({hi})={f_hi}")
    cell = brentq(lambda i: b_mid_of(i) - spec.b_ref, lo, hi,
                  xtol=1e-12, rtol=8.881784197001252e-16, maxiter=300)
    coils = [(a_throat, -z_throat, ratio * cell), (a_cell, -half_gap, cell),
             (a_cell, half_gap, cell), (a_throat, z_throat, ratio * cell)]
    coils = [(a, z, i) for a, z, i in sorted(coils, key=lambda t: t[1])]
    return {
        "name": "textbook_mirror",
        "coils": [{"radius_m": a, "z_m": z, "current_A": i} for a, z, i in coils],
        "design": coils_to_vector(coils),
        "cost_proxy": float(sum(i * i * a for a, _, i in coils)),
        "cell_current_A": cell,
        "throat_current_A": ratio * cell,
    }


# --------------------------------------------------------------------------- #
# 比较辅助函数
# --------------------------------------------------------------------------- #

def compare_series(mine, ref, rel_tol, abs_tol, rel_floor=1e-12):
    """比较两个等长序列。

    在 |ref| >= rel_floor 的地方使用相对误差；低于它时相对误差没有意义
    （次正规数 / 恰好为零的参照），因此改用绝对容差。
    返回 (max_rel, argmax_rel, n_bad, max_abs_over_floor)。
    """
    mine = np.asarray(mine, dtype=float)
    ref = np.asarray(ref, dtype=float)
    if mine.shape != ref.shape:
        raise ValueError(f"shape mismatch: {mine.shape} vs {ref.shape}")
    big = np.abs(ref) >= rel_floor
    max_rel, i_rel = 0.0, -1
    n_bad = 0
    for i in range(ref.size):
        d = abs(float(mine[i]) - float(ref[i]))
        if big[i]:
            rel = d / abs(float(ref[i]))
            if rel > max_rel:
                max_rel, i_rel = rel, i
            if not rel <= rel_tol:
                n_bad += 1
        elif d > abs_tol:
            n_bad += 1
    return max_rel, i_rel, n_bad


def _cmp_line(label, mine, ref, rel_tol, abs_tol):
    diff = abs(float(mine) - float(ref))
    if abs(float(ref)) >= 1e-12:
        shown = f"rel={diff / abs(float(ref)):.3e}"
        ok = diff <= rel_tol * abs(float(ref))
    else:
        shown = f"abs={diff:.3e}"
        ok = diff <= abs_tol
    flag = "OK  " if ok else "FAIL"
    print(f"  [{flag}] {label:<24} reference={ref!r:<24} oracle={mine!r:<24} {shown}")
    return ok


# --------------------------------------------------------------------------- #
# 门禁
# --------------------------------------------------------------------------- #

def check_golden(data_dir, verbose=True):
    """门 G5：重新计算 golden 数值并比较。返回退出码。"""
    data_dir = Path(data_dir)
    spec_path = data_dir / "golden_spec.json"
    base_path = data_dir / "golden_baseline.json"
    samp_path = data_dir / "golden_field_samples.json"
    for p in (spec_path, base_path, samp_path):
        if not p.is_file():
            print(f"error: missing {p}", file=sys.stderr)
            return 2
    spec = Spec.load(spec_path)
    golden = json.loads(base_path.read_text())
    grids = Grids(spec)

    base = textbook_mirror(spec, grids=grids)
    got = evaluate(base["design"], spec, base["cost_proxy"], grids)
    ok = True

    if verbose:
        print(f"G5 cross-language oracle vs golden: spec={spec_path} "
              f"({spec.n_coils} coils, {spec.n_params} params)")
        print(f"  cell current solved by brentq: {base['cell_current_A']!r} "
              f"(golden {golden['coils'][1]['current_A']!r})")
    ok &= _cmp_line("cost_proxy", base["cost_proxy"], golden["cost_proxy"], 1e-6, 1e-12)
    for key in METRIC_KEYS:
        ok &= _cmp_line(key, got["metrics"][key], golden["metrics"][key], 1e-6, 1e-12)
    for key in TERM_KEYS:
        ok &= _cmp_line(f"terms.{key}", got["terms"][key], golden["terms"][key], 1e-6, 1e-12)
    for key in TERM_KEYS:
        ok &= _cmp_line(f"weighted.{key}", got["weighted"][key], golden["weighted"][key], 1e-6, 1e-12)
    for key in PENALTY_KEYS:
        ok &= _cmp_line(f"penalties.{key}", got["penalties"][key], golden["penalties"][key], 1e-6, 1e-12)
    ok &= _cmp_line("design(max|dI|)", base["design"][2 * spec.n_coils], golden["design"][2 * spec.n_coils], 1e-6, 1e-12)
    ok &= _cmp_line("score", got["score"], golden["score"], 1e-6, 1e-6)

    # ---- field samples: 3 个设计 x 32 个点，相对误差 < 1e-9 ---------------- #
    samples = json.loads(samp_path.read_text())["samples"]
    if verbose:
        print("  field samples (analytic closed form vs golden):")
    worst = 0.0
    for s in samples:
        n = len(s["design"]) // 3
        coils = [(float(s["design"][i]), float(s["design"][n + i]), float(s["design"][2 * n + i]))
                 for i in range(n)]
        r = np.asarray(s["points_r"], dtype=float)
        z = np.asarray(s["points_z"], dtype=float)
        br, bz = coilset_field(coils, r, z)
        mag = np.sqrt(br * br + bz * bz)
        n_bad = 0
        rels = []
        for mine, key in ((br, "br"), (bz, "bz"), (mag, "b_mag")):
            rel, _, bad = compare_series(mine, s[key], 1e-9, 1e-12)
            rels.append(rel)
            n_bad += bad
        worst = max(worst, max(rels))
        ok &= n_bad == 0
        if verbose:
            print(f"    [{ 'OK  ' if n_bad == 0 else 'FAIL'}] {s['design_name']:<16} "
                  f"n={r.size} max_rel={max(rels):.3e} (br/bz/b_mag)")
    print(f"G5 {'PASS' if ok else 'FAIL'}  (worst sample relative error {worst:.3e})")
    return 0 if ok else 1


def _iter_go_cases(doc):
    """Go 场交叉校验导出的宽容读取器。

    接受一个顶层列表，或者一个 dict，其 samples/points/records/cases/
    comparisons/designs 之一持有该列表。每个 case 需要一个 12 项的 `design`，
    加上逐点的 r/z 以及 Go 侧的场值。r/z 可以嵌套在 `points`/`points_r`/`r`
    之下（接受这些别名）；场值可以是 `b_mag`/`magnitude`、`br`、`bz`。
    """
    if isinstance(doc, list):
        return doc
    if not isinstance(doc, dict):
        raise ValueError("compare-go input must be a JSON object or array")
    for key in ("samples", "points", "records", "cases", "comparisons", "designs", "results"):
        if isinstance(doc.get(key), list):
            return doc[key]
    raise ValueError(f"no case list found; top-level keys = {sorted(doc)}")


def _pick(d, *names, default=None):
    for n in names:
        if n in d:
            return d[n]
    return default


def nearest_wire_distance(coils, r, z):
    """每个样本点到任意线圈最近导线点的距离。"""
    r = np.asarray(r, dtype=float)
    z = np.asarray(z, dtype=float)
    best = np.full(r.shape, np.inf)
    for a, zc, _ in coils:
        alpha2 = a * a + r * r + (z - zc) ** 2 - 2.0 * a * r
        best = np.minimum(best, np.sqrt(np.maximum(alpha2, 0.0)))
    return best


def compare_go(path, verbose=True, proximity_floor=0.0):
    """把 Go 导出的场表与 scipy oracle 逐点比较。

    proximity_floor 镜像 Go 的近导线夹取（见 loop_field）：当导出里含有
    距离导线小于 5 mm 的探针时，传 5e-3 做同口径比较。落在下限内的点
    总会被计数并单独上报，因此同口径运行无法隐藏夹取在哪里起了作用。
    """
    path = Path(path)
    if not path.is_file():
        print(f"error: missing {path}", file=sys.stderr)
        return 2
    doc = json.loads(path.read_text())
    cases = _iter_go_cases(doc)
    if not cases:                                          # pragma: no cover
        print("error: no cases in the cross-check file", file=sys.stderr)
        return 2
    spec_path = _pick(doc, "spec_path", default=None) if isinstance(doc, dict) else None
    spec = Spec.load(Path(spec_path)) if spec_path else None

    ok = True
    worst_rel = 0.0
    worst_abs = 0.0
    n_floored = 0
    n_points = 0
    print(f"cross-language field check: {path} ({len(cases)} case(s), "
          f"proximity floor {proximity_floor or 0.0:g} m)")
    for ci, case in enumerate(cases):
        design = _pick(case, "design", "x", "vector")
        if design is None:
            print(f"error: case {ci} has no design vector", file=sys.stderr)
            return 2
        n = len(design) // 3
        coils = [(float(design[i]), float(design[n + i]), float(design[2 * n + i])) for i in range(n)]
        pts = _pick(case, "points", default=None)
        r = _pick(case, "points_r", "r", "radii")
        z = _pick(case, "points_z", "z", "zs")
        if pts is not None and (r is None or z is None):
            r = [p["r"] for p in pts] if not isinstance(pts[0], dict) else [p.get("r", p.get("radius")) for p in pts]
            z = [p["z"] for p in pts] if not isinstance(pts[0], dict) else [p.get("z") for p in pts]
        if r is None or z is None:
            print(f"error: case {ci} has no sample points (r/z)", file=sys.stderr)
            return 2
        r = np.asarray(r, dtype=float)
        z = np.asarray(z, dtype=float)
        mine_br, mine_bz = coilset_field(coils, r, z, proximity_floor=proximity_floor)
        mine_mag = np.sqrt(mine_br ** 2 + mine_bz ** 2)
        dist = nearest_wire_distance(coils, r, z)
        inside = dist < COIL_PROXIMITY_FLOOR
        n_floored += int(np.sum(inside))
        n_points += r.size
        name = _pick(case, "design_name", "name", default=f"case{ci}") if isinstance(case, dict) else f"case{ci}"
        checked = []
        case_ok = True
        for mine, keys in ((mine_br, ("br", "B_r")), (mine_bz, ("bz", "B_z")),
                           (mine_mag, ("b_mag", "magnitude", "|B|", "mag"))):
            ref = _pick(case, *keys)
            if ref is None:
                continue
            ref = np.asarray(ref, dtype=float)
            rel, i_rel, bad = compare_series(mine, ref, 1e-9, 1e-9)
            absmax = float(np.max(np.abs(mine - ref)))
            worst_rel = max(worst_rel, rel)
            worst_abs = max(worst_abs, absmax)
            case_ok &= bad == 0
            checked.append(f"{keys[0]}: rel={rel:.3e} abs={absmax:.3e}"
                           + ("" if bad == 0 else f" BAD={bad} at point {i_rel}"))
        if not checked:
            print(f"  error: case {ci} carries no Go field values (looked for br/bz/b_mag)", file=sys.stderr)
            return 2
        ok &= case_ok
        flag = "OK  " if case_ok else "FAIL"
        print(f"  [{flag}] {name:<34} n={r.size} " + " | ".join(checked))
        if np.any(inside):
            k = int(np.argmin(dist))
            note = ("clamped to the floor" if proximity_floor > 0.0
                    else "NOT clamped: re-run with --proximity-floor 0.005 for like-for-like")
            print(f"         {int(np.sum(inside))} point(s) closer than "
                  f"{COIL_PROXIMITY_FLOOR:g} m to a wire (nearest {dist[k]:.3e} m at point {k}); "
                  f"Go's solver clamps alpha2 there, the exact closed form does not ({note})")
    print(f"cross-language field check {'PASS' if ok else 'FAIL'} "
          f"(max relative {worst_rel:.3e}, max absolute {worst_abs:.3e} T; "
          f"{n_floored}/{n_points} point(s) inside the {COIL_PROXIMITY_FLOOR:g} m wire floor)")
    return 0 if ok else 1


def emit_golden(out_dir, force=False):
    """从 oracle 重新生成 golden 文件，供人工复核。"""
    out_dir = Path(out_dir)
    if out_dir.resolve() == (REPO_ROOT / "testdata").resolve() and not force:
        print(f"refusing to overwrite frozen golden values in {out_dir}; pass --force to do it anyway",
              file=sys.stderr)
        return 2
    out_dir.mkdir(parents=True, exist_ok=True)
    spec_path = REPO_ROOT / "testdata" / "golden_spec.json"
    spec = Spec.load(spec_path)
    grids = Grids(spec)
    prov = {"generated_by": "python/aux/oracle.py --emit-golden",
            "spec_source": str(spec_path), "numpy": np.__version__,
            "python": sys.version.split()[0], "note": "independent scipy recomputation"}

    (out_dir / "golden_spec.json").write_text(json.dumps(
        {"provenance": prov, "spec": spec.raw}, indent=2, sort_keys=True) + "\n")

    base = textbook_mirror(spec, grids=grids)
    got = evaluate(base["design"], spec, base["cost_proxy"], grids)
    baseline = {
        "name": base["name"], "note": prov["note"] + " (helmoltz cell + mirror throats)",
        "coils": base["coils"], "design": base["design"],
        "cost_proxy": base["cost_proxy"], "cost_ref": base["cost_proxy"],
        "score": got["score"], "terms": got["terms"], "weighted": got["weighted"],
        "penalties": got["penalties"], "feasible": got["feasible"], "metrics": got["metrics"],
    }
    (out_dir / "golden_baseline.json").write_text(json.dumps(baseline, indent=2, sort_keys=True) + "\n")

    frozen = REPO_ROOT / "testdata" / "golden_field_samples.json"
    if not frozen.is_file():                               # pragma: no cover
        print(f"error: {frozen} needed for the sample layout", file=sys.stderr)
        return 2
    samples = []
    for s in json.loads(frozen.read_text())["samples"]:
        n = len(s["design"]) // 3
        coils = [(float(s["design"][i]), float(s["design"][n + i]), float(s["design"][2 * n + i]))
                 for i in range(n)]
        r = np.asarray(s["points_r"], dtype=float)
        z = np.asarray(s["points_z"], dtype=float)
        br, bz = coilset_field(coils, r, z)
        samples.append({"design_name": s["design_name"], "design": s["design"],
                        "solver": "oracle-scipy-closed-form",
                        "points_r": list(r), "points_z": list(z),
                        "br": list(br), "bz": list(bz),
                        "b_mag": list(np.sqrt(br * br + bz * bz))})
    (out_dir / "golden_field_samples.json").write_text(json.dumps(
        {"provenance": prov, "samples": samples}, indent=2) + "\n")
    print(f"emitted golden_spec.json / golden_baseline.json / golden_field_samples.json -> {out_dir}")
    return 0


# --------------------------------------------------------------------------- #
# cli（命令行入口）
# --------------------------------------------------------------------------- #

def main(argv=None) -> int:
    ap = argparse.ArgumentParser(description="Forge independent numerical oracle (stage G)")
    g = ap.add_mutually_exclusive_group(required=True)
    g.add_argument("--check-golden", metavar="DIR", help="recompute and compare against testdata/golden_*.json")
    g.add_argument("--compare-go", metavar="FILE", help="compare a Go field export point by point")
    g.add_argument("--emit-golden", metavar="DIR", help="regenerate golden files into DIR")
    ap.add_argument("--force", action="store_true", help="allow --emit-golden into testdata/")
    ap.add_argument("--proximity-floor", type=float, default=0.0, metavar="M",
                    help="clamp the near-wire singularity at M metres for --compare-go "
                         "(Go uses 5e-3; default 0 = exact closed form)")
    args = ap.parse_args(argv)
    if args.check_golden:
        return check_golden(args.check_golden)
    if args.compare_go:
        return compare_go(args.compare_go, proximity_floor=args.proximity_floor)
    return emit_golden(args.emit_golden, force=args.force)


if __name__ == "__main__":
    sys.exit(main())
