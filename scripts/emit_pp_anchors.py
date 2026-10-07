#!/usr/bin/env python3
"""从上游 ProjectionPhysics 的**真实 artifact 文件**读出内部设计判决层的数值锚点。

上游唯一真源（全部直读，不凭记忆、不手抄进 Go 源码）：
  artifacts/moirefield/{report.json,summary.txt}      七层账本 M1–M8
  artifacts/mudynamics/{report.json,summary.txt}      μ 动力学 TD1–TD21
  artifacts/fusionroadmap/{report.json,summary.txt}   判决量 D1/D2/D3

输出：testdata/projectionphysics_anchors.json（含上游 git commit + 读取时间）。

用法：
  python3 scripts/emit_pp_anchors.py                 # 写 testdata/projectionphysics_anchors.json
  python3 scripts/emit_pp_anchors.py --check         # 重新推导并与已提交的锚点文件逐条比对（G17）
  python3 scripts/emit_pp_anchors.py --where         # 只打印解析到的上游根目录（供 verify.sh 用）
  python3 scripts/emit_pp_anchors.py --out PATH      # 写到别处

上游根解析顺序：--repo > 环境变量 PROJECTIONPHYSICS_DIR > 默认路径（见 UPSTREAM_DEFAULT）。
退出码：0 成功 / 1 比对不一致（--check）/ 2 用法错或上游字段读不到 / 3 上游不可用。

两条纪律（都写进 provenance，供复核者一眼看穿）：
1. 上游 artifacts/ 被上游 .gitignore 忽略（`git ls-files artifacts/` = 0 个文件），所以这些
   锚点来自「上游仓库 commit <sha> 的工作区里、由 scripts/verify_moire_field.py 重跑出来的
   产物」——**不是**「上游某次提交里版本化的文件」。如实记录，不美化。
2. 重放上游脚本后 report.json 与重放前逐字节相同，唯一差异是 meta.date。因此 --check
   **不做整文件比对**（那样每天都会假红），只比锚点字段的数值并显式忽略时间戳。

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
ANCHORS_REL = os.path.join("testdata", "projectionphysics_anchors.json")

# 上游报告自写的诚实边界：原文照录进锚点（契约要求不许改写、不许省略）。
HONESTY_NOTE = "上游报告自写的诚实边界，原文照录（上游口径，不是本仓库的结论）"

# --check 时忽略的字段：读取时间本身就是差异源；上游根目录在不同机器上可以不同。
IGNORED_CHECK_PATHS = {
    ("provenance", "generated_at_utc"),
    ("provenance", "generated_at_local"),
    ("provenance", "upstream_root"),
}

# 数值比对的相对容差。Go 与 CPython 的浮点求值顺序不同（Python 的 **2 与 Go 的 x*x、
# 常数折叠次序），实测派生量相对差 ~2e-15；1e-12 足以同时容纳跨语言次序差与抓住真实公式错。
REL_TOL = 1e-12

# mudynamics/report.json 里那个整数关闭步字段的原文 key（含它的阈值口径）。
N_CLOSE_KEY = "D-T 窗口关闭步 n*（阈值 μ ≥ 0.99978）"


# --------------------------------------------------------------------------- 基础工具


def repo_root() -> str:
    """本脚本所在仓库根（scripts/ 的上一层）。"""
    return os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def die(code: int, msg: str) -> NoReturn:
    """大声失败，绝不留半个文件。"""
    print("emit_pp_anchors: " + msg, file=sys.stderr)
    sys.exit(code)


def read_json(path: str) -> Dict[str, Any]:
    with open(path, "r", encoding="utf-8") as f:
        return json.load(f)


def read_text(path: str) -> str:
    with open(path, "r", encoding="utf-8") as f:
        return f.read()


def pick(obj: Any, path: str, where: str) -> Any:
    """按点号路径取值；缺字段就硬报错，绝不返回 None 冒充一个锚点。

    整串路径本身就是一个 key 时优先按**字面 key** 取 —— 上游有含小数点的 key 名
    （例如 fusionroadmap R3 的「…μ≈0.999（Δ≈4 个数量级）…」），点号切分会把它切碎。
    """
    if isinstance(obj, dict) and path in obj:
        return obj[path]
    cur = obj
    for part in path.split("."):
        if not isinstance(cur, dict) or part not in cur:
            die(2, f"上游 {where} 里读不到字段 {path!r}（上游口径变了？核对后再改锚点）")
        cur = cur[part]
    return cur


def match_groups(rx: str, text: str, where: str, label: str) -> Tuple[str, ...]:
    """从上游 summary.txt 里抽数字；抽不到就硬报错（不许猜、不许给默认值）。"""
    m = re.search(rx, text)
    if m is None:
        die(2, f"在上游 {where} 里匹配不到 {label}（正则 {rx!r}）—— 上游文本口径变了")
    return m.groups()


def num(s: str) -> float:
    """把上游文本里的数字字面量转成 float（支持 1.000e+04 / 9.999e-05 这类写法）。"""
    return float(s)


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


# --------------------------------------------------------------------------- 三个 artifact 目录


def moirefield(root: str) -> Dict[str, Any]:
    """七层账本 M1–M8：场-密度 / μ 窗口 / 功率体积的锚点。"""
    rel = "artifacts/moirefield/report.json"
    src = os.path.join(root, rel)
    rep = read_json(src)
    res = pick(rep, "results", rel)
    m2 = pick(res, "M2_field_density", rel)
    m3 = pick(res, "M3_power", rel)
    m4 = pick(res, "M4_mu_window", rel)
    m5 = pick(res, "M5_death_threshold", rel)
    m8 = pick(res, "M8_summary", rel)

    # 场源材料类：M2 行给 B_cap/label/n_max，M4 行给 τ_L/X_req/χ_μ，M3 行给 P_rel/V_rel，
    # M8 行给上游自己的判决文本。
    m2_rows = {r["key"]: r for r in pick(m2, "rows", rel)}
    m3_rows = {r["key"]: r for r in pick(m3, "rows", rel)}
    m4_rows = {r["key"]: r for r in pick(m4, "rows", rel)}
    m8_rows = {r["source"]: r for r in pick(m8, "rows", rel)}
    citations = pick(rep, "meta.sources", rel)

    sources = []
    for key, r2 in m2_rows.items():          # 顺序 = 上游 SOURCES dict 的插入顺序
        r3, r4 = m3_rows[key], m4_rows[key]
        m8r = m8_rows.get(r2["label"])
        sources.append({
            "key": key,
            "label": r2["label"],
            "citation": citations[key],
            "B_cap_T": r2["B_cap_T"],
            "n_max_perm3": r2["n_max_perm3"],
            "n_op_perm3": r2["n_op_perm3"],
            "chi_B_required_over_cap": r2["chi_B_required_over_cap"],
            "beta_impossible": r2["beta_impossible"],
            "tau_lawson_s": r4["tau_lawson_s"],
            "X_req_cap": r4["X_req_cap"],
            "chi_mu": r4["chi_mu"],
            "feasible": r4["feasible"],
            "p_rel": r3["p_rel"],
            "V_rel_for_same_power": r3["V_rel_for_same_power"],
            "m8_mu_feasible": m8r["mu_feasible"] if m8r else None,
            "m8_verdict": m8r["verdict"] if m8r else None,
        })

    # M5 死活判据扫描：每个 a 的 B_death 与「哪些场源被关死」。上游的 verdict_sources 是
    # {key: "open"|"CLOSED"}; 拆成两个有序列表（字典顺序不参与比对）。
    scan = []
    for row in pick(m5, "scan", rel):
        vs = pick(row, "verdict_sources", rel)
        scan.append({
            "a_m": row["a_m"],
            "B_death_T": row["B_death_T"],
            "closed_sources": sorted([k for k, v in vs.items() if v == "CLOSED"]),
            "open_sources": sorted([k for k, v in vs.items() if v == "open"]),
        })

    srel = "artifacts/moirefield/summary.txt"
    summary = read_text(os.path.join(root, srel))
    g = match_groups(r"τ₀ = ([0-9.eE+-]+) s；FC11 地板 m_e/m_i = ([0-9.eE+-]+)",
                     summary, srel, "τ₀ / FC11 地板")

    return {
        "report_json": rel,
        "summary_txt": srel,
        "constants": pick(rep, "meta.constants", rel),
        "n_design_perm3": pick(m2, "n_design_perm3", rel),
        "b_min_design_T": pick(m2, "B_min_design_T", rel),
        "x_req_at_design": pick(m4, "x_req_at_design", rel),
        "chi_mu_at_design": pick(m4, "chi_mu_at_design", rel),
        "floor_FC11": pick(rep, "meta.constants.floor_FC11", rel),
        "tau0_s": pick(rep, "meta.constants.tau0_s", rel),
        "b_death_ref_T": pick(rep, "meta.constants.B_death_ref_T", rel),
        "sources": sources,
        "death_scan": scan,
        "honesty": pick(rep, "honesty", rel),
        "honesty_note": HONESTY_NOTE,
        # summary.txt 里的数字是**打过折的**（3–4 位有效数字），只作「文本口径一致」核对，
        # 精度低：锚定容差按打印精度给，不与 report.json 的高精度字段混为一谈。
        "summary_text": {
            "tau0_s": num(g[0]),
            "floor_FC11": num(g[1]),
            "b_min_design_T": num(match_groups(r"B_min = ([0-9.eE+-]+) T", summary, srel, "B_min")[0]),
            "b_death_ref_T": num(match_groups(r"B<([0-9.eE+-]+)T 任何密度都无解",
                                              summary, srel, "B_death")[0]),
            "chi_mu_at_design": num(match_groups(r"χ_μ = ([0-9.eE+-]+)", summary, srel, "χ_μ")[0]),
        },
    }


def mudynamics(root: str, floor: float) -> Dict[str, Any]:
    """μ 动力学 TD19–TD21：窗口余量 / 锁定因子 / 关闭步。"""
    rel = "artifacts/mudynamics/report.json"
    rep = read_json(os.path.join(root, rel))
    n15 = pick(rep, "results.N15_frc_interface", rel)
    if N_CLOSE_KEY not in n15:
        die(2, f"上游 {rel} 的 results.N15_frc_interface 里没有 {N_CLOSE_KEY!r}")

    srel = "artifacts/mudynamics/summary.txt"
    summary = read_text(os.path.join(root, srel))
    # N11 行：「关闭步 n*=165（η=0.05, D-T）」—— η 只出现在 summary 文本里。
    n_star, eta = match_groups(r"关闭步 n\*=([0-9]+)（η=([0-9.]+), D-T）",
                               summary, srel, "N11 关闭步/η")

    # 解析值由本脚本用**上游常数**独立复算（不是上游文件里的字段，也不读 Go）：
    #   n = ln((1−μ₀)/FLOOR) / ln(1/(1−η))
    eta_f, mu0 = num(eta), 0.0
    analytic = math.log((1 - mu0) / floor) / math.log(1 / (1 - eta_f))

    return {
        "report_json": rel,
        "summary_txt": srel,
        "state_equation": pick(rep, "state_equation", rel),
        "closed_form": pick(rep, "closed_form", rel),
        "n_close": {
            # 上游字段：整数步（字段名自带阈值口径）。
            "upstream_field_int": n15[N_CLOSE_KEY],
            "upstream_field_path": "results.N15_frc_interface[%r]" % N_CLOSE_KEY,
            "summary_txt_int": int(n_star),
            "mu_threshold_printed": 0.99978,
            "mu_threshold_printed_source": "results.N15_frc_interface 的字段名「" + N_CLOSE_KEY + "」",
            "eta": eta_f,
            "eta_source": "artifacts/mudynamics/summary.txt N11「关闭步 n*=165（η=0.05, D-T）」",
            # 解析复算：来源是**本脚本**，不是上游文件 —— 两个来源分开记，别混为一谈。
            "analytic_recompute": analytic,
            "analytic_recompute_mu0": mu0,
            "analytic_recompute_source": (
                "emit 脚本独立复算 n = ln((1−μ₀)/FLOOR)/ln(1/(1−η))，FLOOR 取上游 "
                "moirefield report.json 的 meta.constants.floor_FC11，η 取上游 "
                "mudynamics summary.txt N11"),
            "analytic_recompute_note": (
                "解析值是实数（164.2411…），上游字段是**整数步**（165）—— 两者说的不是同一件事，"
                "不许互相冒充；Go 侧 FirstClosedStep = ceil(解析值) 必须复现上游那个整数。"),
        },
        "window_margin_strictly_decreasing": bool(n15["窗口余量 m_i(1−μ_n) 严格递减"]),
        "locking_factor_well_defined": bool(n15["锁定因子 1/√(1−μ_n) 分母恒正（良定义）"]),
        "locking_factor_strictly_increasing": bool(n15["锁定因子严格递增（逼近发散点）"]),
        "threshold_equivalent_to_FC11b": bool(n15["阈值判据等价（逐点，闭式 ⟺ FC11b 阈值）"]),
        "total_steps_below_one": n15.get("总步数（400 步内恒 μ_n<1）"),
        "honesty": pick(rep, "honest", rel),
        "honesty_note": HONESTY_NOTE,
    }


def fusionroadmap(root: str) -> Dict[str, Any]:
    """判决量 D1/D2/D3。注意：这个 report.json **没有 results 包裹**，R1..R6 直接在顶层。"""
    rel = "artifacts/fusionroadmap/report.json"
    rep = read_json(os.path.join(root, rel))
    r1 = pick(rep, "R1_D1_cyclotron_signature", rel)
    r2 = pick(rep, "R2_D2_locking_ratio", rel)
    r3 = pick(rep, "R3_D3_power_scaling", rel)
    r4 = pick(rep, "R4_mu_ladder_and_person_months", rel)

    mu_min = {}
    for label, row in pick(r1, "诊断精度 → 可探测 μ 下限", rel).items():
        mu_min[label] = {"mu_min": row["μ_min"], "R_ci_mu_min": row["R_ci(μ_min)"]}

    rci = {}
    for label, row in pick(r1, "μ → 签名对照", rel).items():
        rci[label] = {"R_ci": row["R_ci"], "tauE_gain": row["τ_E 增益=1/√(1−μ)"]}

    # Λ（D2）：FC5 预言 ≡ 1；几何捕获 Λ = 1/√(1−μ) 是**未证**的主张 —— 本层一律 unknown。
    lam = {}
    for label, row in pick(r2, "Λ 数值（两条假说）", rel).items():
        lam[label] = {"FC5": row["FC5：Λ=1"], "geometric_capture": row["几何捕获：Λ=1/√(1−μ)"]}

    srel = "artifacts/fusionroadmap/summary.txt"
    summary = read_text(os.path.join(root, srel))
    mu_max = num(match_groups(r"μ_max = ([0-9.]+)（D-T）", summary, srel, "μ_max")[0])
    delta, mu_min_txt = match_groups(r"δ=([0-9eE.+-]+) ⟹ 可探测 μ_min = ([0-9eE.+-]+)",
                                     summary, srel, "δ=1e-4 的 μ_min")
    power_txt = {}
    for m in re.finditer(r"k=([0-9.]+): ([0-9.eE+]+)×", summary):
        power_txt["k=" + m.group(1)] = num(m.group(2))

    return {
        "report_json": rel,
        "summary_txt": srel,
        "mu_min_from_delta": mu_min,
        "rci_at_mu": rci,
        "lambda_hypotheses": lam,
        "fc5_prediction": r2["FC5 预言（μ 不可绕开）"],
        "geometric_capture_claim": r2["本设计主张（几何捕获 AMC7）"],
        "power_multiple": pick(r3, "从桌面 μ≈1e-4 到聚变级 μ≈0.999（Δ≈4 个数量级）所需输入功率倍数", rel),
        "power_multiple_per_decade": pick(r3, "每推进一个数量级 μ 所需功率倍数", rel),
        # μ 阶梯与人·月是**排程假设**（契约 §6：不进本层判据）。进锚点只为留证：它确实在
        # 上游存在，且本层刻意不用它。
        "ladder": pick(r4, "阶梯表", rel),
        "ladder_note": "排程假设，不是判据 —— internal/design 不得使用（契约 §6）",
        "honesty": pick(rep, "诚实边界", rel),
        "honesty_note": HONESTY_NOTE,
        "summary_text": {
            "mu_max_FC11_DT": mu_max,
            "mu_min_delta": num(delta),
            "mu_min": num(mu_min_txt),
            "power_multiple": power_txt,
        },
    }


def sinkmuopt(root: str) -> Dict[str, Any]:
    """CR9 自抹平优化（2026-10-07）：η_sink=2λ−λ² 接进双流环 μ 递推。

    锚点 = 上游 artifacts/sinkmuopt/report.json 的 工作点 / 对比结果 / C1-C4 checks。
    这些是**上游数值评估的产物**（候选机制进装置模型），不是闭式解 ——
    Go 侧 SinkMuAfterSteps/SinkMuWorkSteps 是**复算**，锚点按相对容差 1e-12 比对。
    """
    rel = "artifacts/sinkmuopt/report.json"
    rep = read_json(os.path.join(root, rel))
    work = pick(rep, "工作点", rel)
    comp = pick(rep, "对比结果", rel)

    # checks: C1/C2/C3/C4 的 通过/细节 原文照录。
    checks = {}
    for c in pick(rep, "checks", rel):
        label = c["检查"].split(" ", 1)[0]  # "C1 自抹平 ⟹ ..." → "C1"
        checks[label] = {"通过": c["通过"], "细节": c["细节"]}

    return {
        "report_json": rel,
        "工作点": work,
        "对比结果": comp,
        "checks": checks,
        "honesty": pick(rep, "诚实边界", rel),
        "honesty_note": HONESTY_NOTE,
    }


# --------------------------------------------------------------------------- 组装


def build(root: str) -> Dict[str, Any]:
    """把三个上游目录的锚点 + provenance 组装成一份完整锚点。"""
    git = upstream_git(root)
    moire = moirefield(root)
    # 解析关闭步用的是**上游文件里的 FLOOR**，不是 Go 里的，也不是本脚本硬写的。
    mudyn = mudynamics(root, moire["floor_FC11"])
    road = fusionroadmap(root)
    sink = sinkmuopt(root)

    now = datetime.now(timezone.utc).astimezone()

    return {
        "provenance": {
            "generator": "scripts/emit_pp_anchors.py",
            "upstream_root": root,
            "upstream_commit": git["commit"],
            "upstream_commit_subject": git["commit_subject"],
            "upstream_artifacts_tracked_by_git": git["artifacts_tracked_by_git"],
            "generated_at_utc": now.astimezone(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
            "generated_at_local": now.isoformat(),
            "read_from": [
                "artifacts/moirefield/report.json",
                "artifacts/moirefield/summary.txt",
                "artifacts/mudynamics/report.json",
                "artifacts/mudynamics/summary.txt",
                "artifacts/fusionroadmap/report.json",
                "artifacts/fusionroadmap/summary.txt",
                "artifacts/sinkmuopt/report.json",
            ],
            "skipped_fields": [],
            # 出处的正确写法（不许写成「来自上游仓库提交的 artifacts」—— 那是假出处）：
            "source_of_truth": (
                "上游仓库 commit " + git["commit"] + " + 本地重跑 scripts/verify_moire_field.py "
                "的产物。上游 artifacts/ 不入上游 git（同一 commit 的 `git ls-files artifacts/` = "
                + str(git["artifacts_tracked_by_git"]) + " 个文件），因此这些数字来自该 commit 的"
                "**工作区重跑结果**，可由该脚本重放。"),
            "check_note": (
                "重放上游脚本后 report.json 与重放前逐字节相同，唯一差异是 meta.date。因此 "
                "--check 不做整文件比对（那样每天都会假红），只比锚点字段并显式忽略 "
                "generated_at_* 与 upstream_root。"),
            "tolerance_note": (
                "上游字段本身是逐位拷贝；Go 侧**复算**的派生量与上游差 ~2e-15 相对"
                "（Go 的无精度常数算术 vs CPython 的 float64 逐步求值、x*x vs **2），"
                "因此 anchor_test.go 用 rel 1e-12 锚定；summary.txt 里打过折的数字按打印精度锚定。"),
        },
        "moirefield": moire,
        "mudynamics": mudyn,
        "fusionroadmap": road,
        "sinkmuopt": sink,
    }


# --------------------------------------------------------------------------- --check


def compare(want: Any, got: Any, path: Tuple[str, ...], problems: List[str]) -> None:
    """递归比对：数值用相对容差，其余用相等；字典键集合必须一致（多一个键也算漂移）。"""
    if path in IGNORED_CHECK_PATHS:
        return
    where = ".".join(path) if path else "(root)"
    if isinstance(want, dict) and isinstance(got, dict):
        for k in sorted(set(want) | set(got)):
            if k not in got:
                problems.append(f"{where}.{k}: 锚点文件里缺这个键")
            elif k not in want:
                problems.append(f"{where}.{k}: 锚点文件多出这个键（上游已没有）")
            else:
                compare(want[k], got[k], path + (k,), problems)
        return
    if isinstance(want, list) and isinstance(got, list):
        if len(want) != len(got):
            problems.append(f"{where}: 长度 {len(got)} != 上游 {len(want)}")
            return
        for i, (a, b) in enumerate(zip(want, got)):
            compare(a, b, path + (f"[{i}]",), problems)
        return
    if isinstance(want, bool) or isinstance(got, bool):
        if want != got:
            problems.append(f"{where}: {got!r} != 上游 {want!r}")
        return
    if isinstance(want, (int, float)) and isinstance(got, (int, float)):
        if want == got:
            return
        rel = abs(want - got) / max(abs(want), 1e-300)
        if rel > REL_TOL:
            problems.append(f"{where}: {got!r} != 上游 {want!r}（相对差 {rel:.3g} > {REL_TOL:g}）")
        return
    if want != got:
        problems.append(f"{where}: {got!r} != 上游 {want!r}")


def cmd_check(root: str, out_path: str) -> int:
    if not os.path.isfile(out_path):
        die(1, f"锚点文件不存在: {out_path}（先跑 python3 scripts/emit_pp_anchors.py）")
    stored = read_json(out_path)
    fresh = build(root)
    problems: List[str] = []
    compare(fresh, stored, (), problems)

    stored_commit = (stored.get("provenance") or {}).get("upstream_commit")
    fresh_commit = fresh["provenance"]["upstream_commit"]
    if stored_commit != fresh_commit:
        problems.append(f"provenance.upstream_commit: 锚点文件写的是 {stored_commit}，"
                        f"当前上游 HEAD 是 {fresh_commit}（上游动了 → 锚点必须重发并复核）")

    if problems:
        print(f"emit_pp_anchors --check: FAIL（{len(problems)} 处与上游不一致）")
        for p in problems:
            print("  - " + p)
        print("  说明: 锚点只能由上游 artifacts 重新推导出来；要更新就跑 "
              "python3 scripts/emit_pp_anchors.py 并复核 diff。")
        return 1

    n = fresh["mudynamics"]["n_close"]
    print(f"emit_pp_anchors --check: PASS（上游 commit {fresh_commit[:12]}）")
    print(f"  场源材料类 {len(fresh['moirefield']['sources'])} 行 / "
          f"B_death 扫描 {len(fresh['moirefield']['death_scan'])} 个 a / "
          f"moirefield 诚实边界 {len(fresh['moirefield']['honesty'])} 条 / "
          f"fusionroadmap 诚实边界 {len(fresh['fusionroadmap']['honesty'])} 条")
    print(f"  关闭步: 上游字段整数 {n['upstream_field_int']} / 解析复算 "
          f"{n['analytic_recompute']:.6f}（η={n['eta']}）")
    print("  忽略字段: " + ", ".join(".".join(p) for p in sorted(IGNORED_CHECK_PATHS)) +
          "（重放后唯一差异就是时间戳）")
    return 0


# --------------------------------------------------------------------------- main


def main(argv: List[str]) -> int:
    ap = argparse.ArgumentParser(
        description="从上游 ProjectionPhysics 的 artifact 文件读内部设计判决层的数值锚点")
    ap.add_argument("--repo", default=os.environ.get("PROJECTIONPHYSICS_DIR", UPSTREAM_DEFAULT),
                    help="上游 ProjectionPhysics 仓库根（默认 %(default)s）")
    ap.add_argument("--out", default=os.path.join(repo_root(), ANCHORS_REL),
                    help="锚点输出路径（默认 testdata/projectionphysics_anchors.json）")
    ap.add_argument("--check", action="store_true",
                    help="重新推导并与已提交的锚点文件逐条比对（G17）")
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
    moire = anchors["moirefield"]
    n = anchors["mudynamics"]["n_close"]
    print(f"emit_pp_anchors: 写入 {args.out}")
    print(f"  上游 commit  {prov['upstream_commit']}  ({prov['upstream_commit_subject']})")
    print(f"  上游根目录   {root}")
    print(f"  artifacts 被上游 git 跟踪的文件数: {prov['upstream_artifacts_tracked_by_git']}")
    print(f"  读取时间     {prov['generated_at_local']}")
    print(f"  读不到的字段 {prov['skipped_fields'] if prov['skipped_fields'] else '（无）'}")
    print(f"  锚点: 场源 {len(moire['sources'])} 行 / B_death 扫描 "
          f"{len(moire['death_scan'])} 个 a / moirefield 诚实边界 {len(moire['honesty'])} 条 / "
          f"fusionroadmap 诚实边界 {len(anchors['fusionroadmap']['honesty'])} 条")
    print(f"  关闭步: 上游整数字段 {n['upstream_field_int']}，解析复算 "
          f"{n['analytic_recompute']:.6f}（η={n['eta']}）")
    print("  下一步: go test ./internal/design/ （逐条比对锚点）")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
