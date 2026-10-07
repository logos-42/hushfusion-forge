#!/usr/bin/env python3
"""从上游 ProjectionPhysics 的**真实 artifact 文件 + Lean 条目名**读出「交换子世界」
(internal/mu) 的数值锚点。

上游唯一真源（全部直读，不凭记忆、不手抄进 Go 源码）：
  artifacts/mudynamics/report.json      μ 动力学 TD1–TD21（N1–N15 见证 + 诚实边界）
  artifacts/gravitycontrol/report.json  抹平算子族 GCA0–GCA7（N1–N9 见证 + 诚实边界）
  ProjectionPhysics/{PlasmaDynamics,MuFieldCoupling,GravityControl}.lean
                                        条目名（def / theorem）+ 条目的 TD/GCA 标号

输出：testdata/mu_anchors.json（含上游 git commit + 读取时间）。

用法：
  python3 scripts/emit_mu_anchors.py                 # 写 testdata/mu_anchors.json
  python3 scripts/emit_mu_anchors.py --check         # 重新推导并与已提交的锚点文件比对（G21）
  python3 scripts/emit_mu_anchors.py --where         # 只打印解析到的上游根目录（供 verify.sh 用）
  python3 scripts/emit_mu_anchors.py --out PATH      # 写到别处

上游根解析顺序：--repo > 环境变量 PROJECTIONPHYSICS_DIR > 默认路径（见 UPSTREAM_DEFAULT）。
退出码：0 成功 / 1 比对不一致（--check）/ 2 用法错或上游字段读不到 / 3 上游不可用。

--check 的红/黄分界（本脚本与 emit_pp_anchors.py 的差别，也是它存在的理由）：
  **红（FAIL）** = 上游的**数值与判决值**变了（数字、布尔、字符串，以及键的增删）。
                 这些是本仓算子的真出处，变了就必须重发锚点并复核。
  **黄（WARN）** = 上游 commit 前进、时间戳、上游根路径、或 **Lean 条目名集合**变了。
                 上游是**活的**（leo 会持续在里面计算），HEAD 一动就红会造出假红；
                 条目改名同理属于出处漂移，值得提醒，但不动数字就不该拦门。
  于是: 「上游动了但数字没动」只警告；「数字动了」一律红。

两条纪律（延续 emit_pp_anchors.py，不因为"这次简单"就放松）：
1. 上游 artifacts/ 不入上游 git，锚点来自「上游仓库 commit <sha> 的工作区里由上游脚本重跑出来的
   产物」，不是「上游某次提交里版本化的文件」。如实记录。
2. 上游报告里有些字段**没写参数**（例如 N6 的 300 步轨迹只给了终值 μ_N）。本脚本对这类字段
   反推参数、并用闭式解自证（残差 > REL_TOL 就硬报错），反推结果标为 inferred 且写明依据。
   绝不把"猜出来的参数"伪装成上游字段。

本脚本不 import 任何 Go 代码、不调用 Go 二进制、不读 internal/。
"""

import argparse
import json
import math
import os
import re
import subprocess
import sys
from datetime import datetime, timezone
from typing import Any, Dict, List, NoReturn, Optional, Tuple

# 本机上游工作区路径（可用 --repo 或 PROJECTIONPHYSICS_DIR 覆盖）。
UPSTREAM_DEFAULT = "/Users/apple/Downloads/lean/ProjectionPhysics"

# 仓库内锚点文件（相对仓库根）。
ANCHORS_REL = os.path.join("testdata", "mu_anchors.json")

# 上游报告自写的诚实边界：原文照录进锚点（契约要求不许改写、不许省略）。
HONESTY_NOTE = "上游报告自写的诚实边界，原文照录（上游口径，不是本仓库的结论）"

# 数值比对的相对容差。Go 与 CPython 的浮点求值顺序不同（递推 vs 闭式、求和次序），
# 实测派生量相对差 ~2e-15；1e-12 同时容纳跨语言次序差与抓住真实公式错。
REL_TOL = 1e-12

# --check 里**只警告不拦门**的路径前缀（见文件头说明）。
SOFT_PREFIXES: Tuple[Tuple[str, ...], ...] = (("provenance",), ("lean",))

# N10 收敛扫描的步数与起点：μ_300 那一列在 report.json 里没有写参数，本脚本反推 (μ₀, n)。
# 反推必须由闭式解自证（见 infer_scan_params），不自证就硬报错。
SCAN_N = 300

# 上游三个 Lean 文件（文件名 → 它们承载的条目族）。
LEAN_FILES = {
    "PlasmaDynamics.lean": "TD1–TD10：μ 状态方程与轨道（muStep / muChain）",
    "MuFieldCoupling.lean": "TD11–TD21：增益=抹平进展 + 顺序差 + FRC 接缝",
    "GravityControl.lean": "GCA0–GCA7：抹平算子族与布尔边界",
}

# 本仓 internal/mu 的每条定义都必须在上游有名字相同的条目；缺一条就硬报错。
# （锚点不是"抄几个数"，而是"这些条目还在上游存在、且数值形态没变"。）
REQUIRED_LEAN_ENTRIES: Dict[str, Tuple[Tuple[str, str], ...]] = {
    "PlasmaDynamics.lean": (
        ("def", "muStep"), ("theorem", "muStep_bounded"), ("theorem", "muStep_strict_mono"),
        ("theorem", "muStep_lt_one"), ("theorem", "muStep_fixed_one"),
        ("theorem", "muStep_full_gain"), ("def", "muChain"), ("theorem", "muChain_closed_form"),
        ("theorem", "muChain_lt_one"),
    ),
    "MuFieldCoupling.lean": (
        ("def", "flattenProgress"), ("theorem", "flattenProgress_self"),
        ("theorem", "flattenProgress_flat"), ("theorem", "flattenProgress_after_flatten"),
        ("def", "muAfterFlatten"), ("def", "muBeforeFlatten"),
        ("theorem", "muAfterFlatten_eq_one"), ("theorem", "mu_order_gap"),
        ("theorem", "mu_order_difference"), ("theorem", "mu_order_matters"),
        ("theorem", "rmf_margin_strictly_decreasing"),
        ("theorem", "mu_scaling_well_defined"), ("theorem", "rmf_window_threshold_iff"),
    ),
    "GravityControl.lean": (
        ("def", "regionMean"), ("def", "flatten"), ("def", "fluctuationEnergy"),
        ("def", "commutes"), ("theorem", "flatten_idempotent"),
        ("theorem", "fluctuationEnergy_nonneg"), ("theorem", "fluctuationEnergy_eq_zero_iff"),
        ("theorem", "flatten_kills_energy"), ("theorem", "flatten_isProjection"),
        ("theorem", "flatten_commute_of_disjoint"), ("theorem", "flatten_commute_of_subset"),
        ("theorem", "flatten_absorb_of_subset"), ("theorem", "flatten_commute_self"),
        ("theorem", "flatten_not_commute_overlap"),
    ),
}


# --------------------------------------------------------------------------- 基础工具


def repo_root() -> str:
    """本脚本所在仓库根（scripts/ 的上一层）。"""
    return os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def die(code: int, msg: str) -> NoReturn:
    """大声失败，绝不留半个文件。"""
    print("emit_mu_anchors: " + msg, file=sys.stderr)
    sys.exit(code)


def read_json(path: str) -> Dict[str, Any]:
    with open(path, "r", encoding="utf-8") as f:
        return json.load(f)


def read_text(path: str) -> str:
    with open(path, "r", encoding="utf-8") as f:
        return f.read()


def pick(obj: Any, path: str, where: str) -> Any:
    """按点号路径取值；缺字段就硬报错，绝不返回 None 冒充一个锚点。

    整串路径本身就是一个 key 时优先按**字面 key** 取 —— 上游有含小数点/括号的 key 名
    （例如 N15 的「D-T 窗口关闭步 n*（阈值 μ ≥ 0.99978）」）。
    """
    if isinstance(obj, dict) and path in obj:
        return obj[path]
    cur = obj
    for part in path.split("."):
        if not isinstance(cur, dict) or part not in cur:
            die(2, f"上游 {where} 里读不到字段 {path!r}（上游口径变了？核对后再改锚点）")
        cur = cur[part]
    return cur


# --------------------------------------------------------------------------- 上游元信息


def upstream_git(root: str) -> Dict[str, Any]:
    """上游 git 事实：当前 commit、其主题、以及 artifacts 是否被 git 跟踪。"""

    def run(args: List[str]) -> Tuple[Optional[str], str]:
        try:
            out = subprocess.run(["git", "-C", root] + args, capture_output=True,
                                 text=True, timeout=30)
        except (OSError, subprocess.SubprocessError) as e:  # git 不在 / 仓库坏了
            return None, str(e)
        if out.returncode != 0:
            return None, (out.stderr or "").strip()
        return out.stdout.strip(), ""

    commit, err = run(["rev-parse", "HEAD"])
    if commit is None:
        die(2, f"读不到上游 git commit（git -C {root} rev-parse HEAD）：{err}")
    subject, _ = run(["log", "-1", "--format=%s"])
    tracked, terr = run(["ls-files", "artifacts/"])

    # 这个数字是「上游 artifacts 有没有版本化」的实测答案，provenance 的措辞依赖它。
    n_tracked = 0 if tracked is None else len([ln for ln in tracked.splitlines() if ln.strip()])
    return {
        "commit": commit,
        "commit_subject": subject or "",
        "artifacts_tracked_by_git": n_tracked,
        "artifacts_tracked_error": terr,
    }


# --------------------------------------------------------------------------- Lean 条目名

DOC_RX = re.compile(r"^/--\s*(.*?)\s*-/\s*$")
ENTRY_RX = re.compile(r"^(theorem|def|lemma|abbrev)\s+([A-Za-z_][A-Za-z0-9_'.]*)")
TAG_RX = re.compile(r"((?:TD|GCA)\d+[a-z]?)")


def lean_entries(root: str) -> Dict[str, Any]:
    """从上游 Lean 文件里读出条目名（def/theorem）+ 条目的 TD/GCA 标号 + 文档首行。

    只读**名字与标号**，不试图解析证明项：判据要的是"这些条目还在不在"，不是"证明还能不能过"
    （证明由上游 Lean 自己负责）。
    """
    out: Dict[str, Any] = {
        "repo_rel_dir": "ProjectionPhysics",
        "files": {},
        "required_entries": {f: [f"{k} {n}" for k, n in v] for f, v in REQUIRED_LEAN_ENTRIES.items()},
        "note": ("条目名是本仓 internal/mu 的每条定义在上游的对应物；名字集合变了只在 --check 里"
                 "警告（出处漂移），数值变了才红。"),
    }
    found: Dict[str, set] = {f: set() for f in LEAN_FILES}
    for fname, family in LEAN_FILES.items():
        rel = os.path.join("ProjectionPhysics", fname)
        path = os.path.join(root, rel)
        if not os.path.isfile(path):
            die(2, f"上游 Lean 文件不存在: {path}")
        entries: List[Dict[str, Any]] = []
        doc = ""
        for line in read_text(path).splitlines():
            m = DOC_RX.match(line.strip())
            if m:
                doc = m.group(1)
                continue
            m = ENTRY_RX.match(line)
            if m:
                kind, name = m.group(1), m.group(2)
                tag = TAG_RX.search(doc)
                entries.append({
                    "kind": kind,
                    "name": name,
                    "tag": tag.group(1) if tag else "",
                    "doc": doc,
                })
                found[fname].add(f"{kind} {name}")
                doc = ""
            elif line.strip() and not line.strip().startswith("--"):
                # 只保留紧邻条目的那一行文档注释（跨过别的语句就不再算这个条目的说明）。
                if not line.strip().startswith(("import", "namespace", "open", "end", "@[", "/-", "-/")):
                    doc = ""
        out["files"][fname] = {
            "family": family,
            "repo_rel": rel,
            "n_entries": len(entries),
            "entries": entries,
        }
    # 本仓依赖的条目必须还在（这是硬检查：条目没了 = 代数的出处没了）。
    for fname, wanted in REQUIRED_LEAN_ENTRIES.items():
        missing = [f"{k} {n}" for k, n in wanted if f"{k} {n}" not in found[fname]]
        for name in missing:
            die(2, f"上游 {fname} 里找不到条目 {name!r}（名字被改？—— internal/mu 的对应物失去出处）")
    return out


def lean_names(anchors: Dict[str, Any]) -> Dict[str, List[str]]:
    """给 --check 打印用的条目名摘要。"""
    return {f: [f"{e['kind']} {e['name']}" for e in blk["entries"]]
            for f, blk in anchors["lean"]["files"].items()}


# --------------------------------------------------------------------------- μ 动力学


def mu_step(mu: float, eta: float) -> float:
    """上游 TD1 的状态方程（本脚本自己的实现，用来做参数反推的自证）。"""
    return mu + eta * (1 - mu)


def mu_closed_form(mu0: float, eta: float, n: int) -> float:
    """上游 TD7 的闭式解 μ_n = 1 − (1−η)^n (1−μ₀)。"""
    return 1 - (1 - eta) ** n * (1 - mu0)


def check_scan_row(tag: str, eta: float, mu_n: float) -> Dict[str, Any]:
    """检验"N10 扫描行的参数是 (μ₀=0.1, n=300)"这个**假设**。

    注意这里不是"反推参数"：report.json 没写 μ₀/n，本脚本提出一个假设（μ₀ 取 0.1、n 取
    SCAN_N）然后用**递推**去撞上游字段。撞不上就硬报错 —— 假设是可证伪的，而"解出参数再
    代回去"是恒真式的伪验证（这一点是本脚本第一版写错、被实测纠正过的地方）。

    同时记两个量，别混为一谈：
      - iteration_residual：递推（上游 TD1 的定义式）与上游字段的相对差；
      - closed_form_residual：闭式解（TD7）与上游字段的相对差。
    两者不同不是矛盾，是"递推 vs 闭式"的浮点次序差（N5 那一行说的就是这件事）。
    """
    iterated = 0.1
    for _ in range(SCAN_N):
        iterated = mu_step(iterated, eta)
    closed = mu_closed_form(0.1, eta, SCAN_N)
    it_res = abs(iterated - mu_n) / max(abs(mu_n), 1e-300)
    cf_res = abs(closed - mu_n) / max(abs(mu_n), 1e-300)
    if it_res > REL_TOL:
        die(2, f"上游 {tag} 的行为 (η={eta}, μ_N={mu_n}) 对不上假设 (μ₀=0.1, n={SCAN_N}) 的递推值 "
               f"{iterated}（相对差 {it_res:.3g} > {REL_TOL:g}）—— 假设被证伪，别硬套旧锚点")
    # 闭式解在该行是否被双精度钉住：μ_N 的分辨率撑不住 (1−η)^n 时, 闭式解只有 1.0 可给。
    sat = abs((1 - eta) ** SCAN_N) < 2.220446049250313e-16 or (1 - eta) ** SCAN_N > 1e16
    return {
        "hypothesis_mu0": 0.1,
        "hypothesis_n": SCAN_N,
        "iteration_value": iterated,
        "iteration_residual": it_res,
        "iteration_bit_exact": it_res == 0.0,
        "closed_form_value": closed,
        "closed_form_residual": cf_res,
        "closed_form_saturated": sat,
        "source": ("本脚本提出的参数假设：report.json 的 N10 行只给 (η, μ_300)，没给 μ₀/n。"
                   "假设 (μ₀=0.1, n=300) 由 TD1 的递推逐行撞击上游字段（撞不上就报错）；"
                   "闭式解残差另记，两者不同是「递推 vs 闭式」的次序差。"),
    }


def check_n6_track() -> Dict[str, Any]:
    """检验 N6 的 300 步轨迹假设 (μ₀=0, η=0.05, n=300)。"""
    n = 300
    eta = 0.05
    iterated = 0.0
    for _ in range(n):
        iterated = mu_step(iterated, eta)
    return {"mu0": 0.0, "eta": eta, "n": n, "iteration_value": iterated, "closed_form_value": mu_closed_form(0.0, eta, n)}


def mudynamics(root: str) -> Dict[str, Any]:
    """μ 动力学 TD1–TD21：状态方程 / 闭式解 / 收敛扫描 / 顺序差见证。"""
    rel = "artifacts/mudynamics/report.json"
    rep = read_json(os.path.join(root, rel))
    res = pick(rep, "results", rel)

    scan_rows = pick(res, "N10_convergence_scan.扫描", rel)
    scan = []
    for row in scan_rows:
        check = check_scan_row("N10_convergence_scan", row["η"], row["μ_300"])
        scan.append({
            "eta": row["η"],
            "mu_300_upstream": row["μ_300"],
            "overshoot": bool(row["超调"]),
            "converges": bool(row["收敛到 1"]),
            "verdict": row["判定"],
            "params": check,
        })

    n6 = pick(res, "N6_N7_N8_never_reach", rel)
    # N6 的轨迹参数也没写：假设 (μ₀=0, η=0.05, n=300) 由 TD1 的递推撞击上游终值。
    # η=0.05 与本报告 N10/N11 里出现的 η 同族（不是从 μ_N 反解出来的 —— 那是恒真式）。
    n6_track = check_n6_track()
    n6_up = float(n6["终值 μ_N"])
    n6_it_res = abs(n6_track["iteration_value"] - n6_up) / abs(n6_up)
    if n6_it_res > REL_TOL:
        die(2, f"N6 的终值 μ_N={n6_up} 对不上假设 (μ₀=0, η={n6_track['eta']}, n={n6_track['n']}) 的"
               f"递推值 {n6_track['iteration_value']}（相对差 {n6_it_res:.3g}）—— 假设被证伪")

    return {
        "report_json": rel,
        "title": pick(rep, "title", rel),
        "state_equation": pick(rep, "state_equation", rel),
        "closed_form": pick(rep, "closed_form", rel),
        "bridge": pick(rep, "bridge", rel),
        "n1_bounded": pick(res, "N1_bounded", rel),
        "n2_strict_mono": pick(res, "N2_strict_mono", rel),
        "n3_n4_reachable_overshoot": pick(res, "N3_N4_reachable_overshoot", rel),
        "n5_closed_form": pick(res, "N5_closed_form", rel),
        "n10_convergence_scan": scan,
        "n6_never_reach": {
            "raw": n6,
            "track_hypothesis": n6_track,
            "iteration_residual": n6_it_res,
            "iteration_bit_exact": n6_it_res == 0.0,
            "source": ("本脚本提出的参数假设：report.json 的 N6 只给终值 μ_N 与步数，没给 (μ₀, η)。"
                       "假设 (μ₀=0, η=0.05, n=300) 由 TD1 的递推撞击上游字段（撞不上就报错）；"
                       "η=0.05 与本报告 N10/N11 出现的 η 同族。"),
            # 这一条特别值得记：上游自己写了"双精度下 (1−η)^n 下溢令 μ 在机器精度内 = 1"。
            "float_saturation_note": n6.get("浮点饱和说明"),
        },
        # N9 满增益 / N11 桥 / N13 顺序见证 / N14 顺序差公式 —— 本世界构造性非零的四块砖。
        "n9_full_gain": pick(res, "N9_full_gain", rel),
        "n11_n12_bridge": pick(res, "N11_N12_bridge", rel),
        "n13_order_witness": pick(res, "N13_order_witness", rel),
        "n14_gap_and_cost": pick(res, "N14_gap_and_cost", rel),
        "n15_frc_interface": pick(res, "N15_frc_interface", rel),
        "honesty": pick(rep, "honest", rel),
        "honesty_note": HONESTY_NOTE,
        # 本仓复算与上游字段的已知差别 —— 如实记，不藏（详见 internal/mu/mu_test.go 的 ulpBand）。
        "recompute_note": (
            "上游把「抹平一次后 max|η − 1|」与「P² = P」的残差记成逐位 0.0。本仓用朴素左到右"
            "求和复算时它们是 1 ULP 量级（对一串相同的数再取均值，朴素求和一般不等于那个数）。"
            "这不是矛盾，是**求和次序**的差别；本仓的代数判据因此用尺子自己的零带（与 G19a 的"
            "控制组同口径），而不是逐位相等。"),
    }


# --------------------------------------------------------------------------- 控制代数


def gravitycontrol(root: str) -> Dict[str, Any]:
    """控制引力场的代数 GCA0–GCA7：基元 / 交换子扫描 / 见证向量。"""
    rel = "artifacts/gravitycontrol/report.json"
    rep = read_json(os.path.join(root, rel))
    res = pick(rep, "results", rel)

    scan_rows = pick(res, "N6_commutator_scan.扫描", rel)
    scan = [{
        "region_b_start": row["区域 B 起点"],
        "overlap_degree": row["重叠度"],
        "commutator_norm_upstream": row["‖[P_A,P_B]v‖"],
    } for row in scan_rows]

    n9 = pick(res, "N9_commute_absorb_witnesses", rel)
    return {
        "report_json": rel,
        "title": pick(rep, "title", rel),
        "primitive": pick(rep, "primitive", rel),
        "derivation": pick(rep, "derivation", rel),
        "n1_idempotent": pick(res, "N1_idempotent", rel),
        "n2_decision_functional": pick(res, "N2_decision_functional", rel),
        "n3_polarization": pick(res, "N3_polarization", rel),
        "n5_complementary_projection": pick(res, "N5_complementary_projection", rel),
        "n6_commutator_scan": {
            "scan": scan,
            "lamellar_is_zero": pick(res, "N6_commutator_scan.层状族（不交或嵌套）⟹ 交换子 = 0", rel),
            "overlap_is_positive": pick(res, "N6_commutator_scan.部分重叠 ⟹ 交换子 > 0（全部）", rel),
            # 上游扫描的几何是**从重叠度那一列反推**出来的（report.json 只给了"区域 B 起点"）：
            # 起点 16 重叠 1.0、18→0.875、20→0.75、24→0.5、28→0.25、32/36/40/44/48→0.0
            # 对应 A = {16,..,31}（16 格）+ B = 环上从 s 起的 16 格。这条反推是 anchor_test.go
            # 里"形态复现"用的，标为 inferred，不冒充上游字段。
            "geometry_inferred": {
                "region_a": "16..31 (16 格)",
                "region_b": "环上从 s 起的 16 格",
                "ring_min_cells": 64,
                "source": "从重叠度列反推（1.0, 0.875, 0.75, 0.5, 0.25, 0.0 ⟺ |A∩B|/16）",
            },
        },
        "n7_boolean_vs_not": pick(res, "N7_boolean_vs_not", rel),
        "n8_quadratic_signature": pick(res, "N8_quadratic_signature", rel),
        "n9_witnesses": {
            "raw": n9,
            "partial_overlap_ab": n9["部分重叠 A={0,1},B={1,2} v=[1,0,0]：先A后B"],
            "partial_overlap_ba": n9["部分重叠：先B后A"],
            "nested_ab": n9["嵌套 A={0}⊂B={0,1} v=[1,1]：先A后B"],
            "nested_ba": n9["嵌套：先B后A"],
            # 键名的读法：上游把 flatten(A, flatten(B, v)) 写作"先A后B"；按定义它是 **B 先作用**。
            # 本仓一律不用键名推语义，只对组合式本体（anchor_test.go 逐位断言两个向量）。
            "key_naming_note": (
                "上游键名「先A后B」对应组合式 flatten(A, flatten(B, v))（B 先作用、A 后作用）。"
                "本仓不用键名推语义：锚点同时记键名原文与该组合式，判据只看组合式。"),
        },
        "honesty": pick(rep, "honest", rel),
        "honesty_note": HONESTY_NOTE,
    }


# --------------------------------------------------------------------------- 组装


# --------------------------------------------------------------------------- CR9 自抹平优化


def sinkmuopt(root: str) -> Dict[str, Any]:
    """CR9 自抹平优化：双流环 μ 递推加环流电子自抹平增益（上游 sinkmuopt 报告）。

    读出上游 artifacts/sinkmuopt/report.json 的对比结果：
      C1 到达工作窗口 μ=0.999 更快 (132→85 步)
      C3 外部 RMF 减半仍可达 (267→150 步) —— 自供能方向
      关键负面发现: η_sink 恒定会破坏硬界(μ 冲过 FC11 到 1); 必须随 (1−μ) 衰减。
    """
    rel = "artifacts/sinkmuopt/report.json"
    rep = read_json(os.path.join(root, rel))
    res = pick(rep, "对比结果", rel)

    return {
        "report_json": rel,
        "title": pick(rep, "产物", rel),
        "candidate_mechanism": pick(rep, "候选机制（诚实标注）", rel),
        "working_point": pick(rep, "工作点", rel),
        "comparison": {
            "n_work_old": res.get("n_work_old"),
            "n_work_new": res.get("n_work_new"),
            "n_work_saved": res.get("工作窗口节省步数"),
            "external_halved_still_reaches": res.get("对外部依赖下降（η_ext 减半仍达工作窗口）"),
        },
        "conclusion": pick(rep, "结论", rel),
        "honesty": pick(rep, "诚实边界", rel),
        "honesty_note": HONESTY_NOTE,
    }


def build(root: str) -> Dict[str, Any]:
    """把上游三份 report.json + 三个 Lean 文件的条目名组装成一份锚点。"""
    git = upstream_git(root)
    mudyn = mudynamics(root)
    gc = gravitycontrol(root)
    sm = sinkmuopt(root)
    lean = lean_entries(root)

    now = datetime.now(timezone.utc).astimezone()

    return {
        "provenance": {
            "generator": "scripts/emit_mu_anchors.py",
            "upstream_root": root,
            "upstream_commit": git["commit"],
            "upstream_commit_subject": git["commit_subject"],
            "upstream_artifacts_tracked_by_git": git["artifacts_tracked_by_git"],
            "generated_at_utc": now.astimezone(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
            "generated_at_local": now.isoformat(),
            "read_from": [
                "artifacts/mudynamics/report.json",
                "artifacts/gravitycontrol/report.json",
                "artifacts/sinkmuopt/report.json",
                "ProjectionPhysics/PlasmaDynamics.lean",
                "ProjectionPhysics/MuFieldCoupling.lean",
                "ProjectionPhysics/GravityControl.lean",
            ],
            "skipped_fields": [],
            "source_of_truth": (
                "上游仓库 commit " + git["commit"] + " 的工作区：两份 report.json 由上游 "
                "scripts/verify_gravity_control.py / verify_mu_field_coupling.py 重跑得到"
                "（上游 artifacts/ 不入上游 git：同一 commit 的 `git ls-files artifacts/` = "
                + str(git["artifacts_tracked_by_git"]) + " 个文件）；Lean 条目名直读三个 .lean 源文件。"
                "锚点里的数字要么是上游字段原文，要么由本脚本反推并自证（带 inferred 标记）。"),
            "check_note": (
                "上游是**活的**（leo 会持续在里面计算），所以 --check 分两级："
                "数值/判决值/键集合变了 → 红（FAIL，必须重发锚点并复核）；"
                "只有 provenance（commit/时间/路径）或 Lean 条目名集合变了 → 黄（WARN，不拦门）。"
                "这样「上游 HEAD 前进但数字没动」不会造成假红。"),
            "tolerance_note": (
                "上游字段是逐位拷贝；Go 侧**复算**的派生量（μ 轨道、交换子见证、闭式解）与上游差"
                "~2e-15 相对（递推 vs 闭式、求和次序的差别），因此 anchor_test.go 用 rel 1e-12 锚定。"),
        },
        "mudynamics": mudyn,
        "gravitycontrol": gc,
        "sinkmuopt": sm,
        "lean": lean,
    }


# --------------------------------------------------------------------------- --check


def is_soft(path: Tuple[str, ...]) -> bool:
    """软路径 = 只警告：provenance（含 commit/时间/路径）与 Lean 条目名集合。"""
    return any(path[:len(pre)] == pre for pre in SOFT_PREFIXES)


def compare(want: Any, got: Any, path: Tuple[str, ...], hard: List[str], soft: List[str]) -> None:
    """递归比对：数值用相对容差，其余用相等；字典键集合必须一致（增删都算漂移）。

    hard = 数值/判决值/键集合（拦门）；soft = provenance 与 Lean 条目名（只警告）。
    """
    where = ".".join(path) if path else "(root)"
    sink = soft if is_soft(path) else hard
    if isinstance(want, dict) and isinstance(got, dict):
        for k in sorted(set(want) | set(got)):
            if k not in got:
                sink.append(f"{where}.{k}: 锚点文件里缺这个键")
            elif k not in want:
                sink.append(f"{where}.{k}: 锚点文件多出这个键（上游已没有）")
            else:
                compare(want[k], got[k], path + (k,), hard, soft)
        return
    if isinstance(want, list) and isinstance(got, list):
        if len(want) != len(got):
            sink.append(f"{where}: 长度 {len(got)} != 上游 {len(want)}")
            return
        for i, (a, b) in enumerate(zip(want, got)):
            compare(a, b, path + (f"[{i}]",), hard, soft)
        return
    if isinstance(want, bool) or isinstance(got, bool):
        if want != got:
            sink.append(f"{where}: {got!r} != 上游 {want!r}")
        return
    if isinstance(want, (int, float)) and isinstance(got, (int, float)):
        if want == got:
            return
        rel = abs(want - got) / max(abs(want), 1e-300)
        if rel > REL_TOL:
            sink.append(f"{where}: {got!r} != 上游 {want!r}（相对差 {rel:.3g} > {REL_TOL:g}）")
        return
    if want != got:
        sink.append(f"{where}: {got!r} != 上游 {want!r}")


def cmd_check(root: str, out_path: str) -> int:
    if not os.path.isfile(out_path):
        die(1, f"锚点文件不存在: {out_path}（先跑 python3 scripts/emit_mu_anchors.py）")
    stored = read_json(out_path)
    fresh = build(root)

    hard: List[str] = []
    soft: List[str] = []
    compare(fresh, stored, (), hard, soft)

    stored_commit = (stored.get("provenance") or {}).get("upstream_commit")
    fresh_commit = fresh["provenance"]["upstream_commit"]
    if stored_commit != fresh_commit:
        # 只警告：上游 HEAD 前进本身不是锚点失效的证据（数字没动就不该拦门）。
        soft.append(f"provenance.upstream_commit: 锚点文件写的是 {stored_commit}，"
                    f"当前上游 HEAD 是 {fresh_commit}（数字未动 → 只警告；要刷新就重跑本脚本）")

    if hard:
        print(f"emit_mu_anchors --check: FAIL（{len(hard)} 处**数值/判决值**与上游不一致）")
        for p in hard:
            print("  - " + p)
        if soft:
            print(f"  （另有 {len(soft)} 处软漂移，见下）")
            for p in soft:
                print("  ~ " + p)
        print("  说明: 锚点只能由上游 artifacts/Lean 重新推导出来；数值真变了就必须跑 "
              "python3 scripts/emit_mu_anchors.py 并复核 diff（别只改容差）。")
        return 1

    n = fresh["mudynamics"]
    gc = fresh["gravitycontrol"]
    print(f"emit_mu_anchors --check: PASS（上游 commit {fresh_commit[:12]}）")
    print(f"  μ 动力学: 状态方程「{n['state_equation']}」/ 闭式解「{n['closed_form']}」")
    print(f"  收敛扫描 {len(n['n10_convergence_scan'])} 行（含 2 个临界值 η=1 / η=2）；"
          f"参数假设 (μ₀=0.1, n=300) 逐行撞击上游，"
          f"最大相对残差 {max(r['params']['iteration_residual'] for r in n['n10_convergence_scan']):.3g}")
    print(f"  控制代数: 基元「{gc['primitive']}」/ 交换子扫描 {len(gc['n6_commutator_scan']['scan'])} 行")
    print(f"  Lean 条目: " + " / ".join(f"{f} {blk['n_entries']} 条"
                                       for f, blk in fresh["lean"]["files"].items()))
    if soft:
        print(f"  软漂移（{len(soft)} 处，不拦门）:")
        for p in soft:
            print("  ~ " + p)
    return 0


# --------------------------------------------------------------------------- main


def main(argv: List[str]) -> int:
    ap = argparse.ArgumentParser(
        description="从上游 ProjectionPhysics 的 artifact 文件 + Lean 条目名读「交换子世界」的锚点")
    ap.add_argument("--repo", default=os.environ.get("PROJECTIONPHYSICS_DIR", UPSTREAM_DEFAULT),
                    help="上游 ProjectionPhysics 仓库根（默认 %(default)s）")
    ap.add_argument("--out", default=os.path.join(repo_root(), ANCHORS_REL),
                    help="锚点输出路径（默认 testdata/mu_anchors.json）")
    ap.add_argument("--check", action="store_true",
                    help="重新推导并与已提交的锚点文件比对（G21）")
    ap.add_argument("--where", action="store_true",
                    help="只打印解析到的上游根目录（供 scripts/verify.sh 决定是否 SKIP）")
    args = ap.parse_args(argv)

    root = os.path.abspath(args.repo)

    if args.where:
        # 上游不可用时以退出码 3 告知（verify.sh 据此 SKIP 并打印原因），不假装成功。
        if not os.path.isdir(root):
            print(f"upstream ProjectionPhysics not found at {root}", file=sys.stderr)
            return 3
        print(root)
        return 0

    if not os.path.isdir(os.path.join(root, "artifacts")):
        die(3, f"上游根目录里没有 artifacts/: {root}（用 --repo 或 PROJECTIONPHYSICS_DIR 指定）")

    if args.check:
        return cmd_check(root, args.out)

    anchors = build(root)
    os.makedirs(os.path.dirname(args.out), exist_ok=True)
    with open(args.out, "w", encoding="utf-8") as f:
        json.dump(anchors, f, ensure_ascii=False, indent=2)
        f.write("\n")

    prov = anchors["provenance"]
    mudyn = anchors["mudynamics"]
    gc = anchors["gravitycontrol"]
    print(f"emit_mu_anchors: 写入 {args.out}")
    print(f"  上游 commit  {prov['upstream_commit']}  ({prov['upstream_commit_subject']})")
    print(f"  上游根目录   {root}")
    print(f"  artifacts 被上游 git 跟踪的文件数: {prov['upstream_artifacts_tracked_by_git']}")
    print(f"  读取时间     {prov['generated_at_local']}")
    print(f"  μ 动力学: N13 见证 {mudyn['n13_order_witness'].get('先抹平再更新 μ（时序 A）')} / "
          f"{mudyn['n13_order_witness'].get('先更新 μ 再抹平（时序 B）')}，"
          f"N14 顺序差残差 {mudyn['n14_gap_and_cost'].get('顺序差公式 max 残差（300 组）')}")
    print(f"  控制代数: 交换子扫描 {len(gc['n6_commutator_scan']['scan'])} 行，"
          f"层状⟹0 = {gc['n6_commutator_scan']['lamellar_is_zero']}，"
          f"部分重叠⟹>0 = {gc['n6_commutator_scan']['overlap_is_positive']}")
    print(f"  Lean 条目: " + " / ".join(f"{f} {blk['n_entries']} 条"
                                       for f, blk in anchors["lean"]["files"].items()))
    print("  下一步: go test ./internal/mu/ （逐条比对锚点）")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
