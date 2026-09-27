#!/usr/bin/env python3
"""门 G18（世界协议）的 Python 一侧：驱动 ``forge world serve`` 的薄客户端。

为什么需要一个客户端：本仓纪律禁止 Python ``import`` Go 的任何东西，也禁止"用 Go
二进制去验证 Go"。于是跨语言唯一诚实的形状是进程间协议 —— 而这个文件就是那条协议的
另一种语言的实现：它把一个**新建的**世界喂成同一段请求序列，并把响应**逐字节**比对。
两端逐字节相同，才说明 Python 客户端看到的世界与 Go 写下的是同一个世界。

它做三件事：

* ``--replay <trace>``    逐字节回放 ``testdata/world_trace_golden.jsonl``：先把 trace
  里那条 ``hello`` 过一遍握手自校验，再驱动一个新世界逐行比对（任一行不同即非零退出）；
* ``--requests <file>``   把请求行喂给一个新世界并打印响应（"能驱动 serve 进程"）；
* ``--handshake``         只做握手自校验。

握手自校验（契约 §3.1）不是可选项：``action_dim`` / ``observation_dim`` /
``obs_metric_keys`` / ``obs_metric_refs`` / ``spec.sha256`` 任何一个对不上，客户端就
**立刻非零退出**，绝不"先跑再看"。握手错了却照跑，是比崩溃更坏的失败。

只用标准库。全程**字符串拼接与切片**：trace 的原始请求/响应字节从行里切片取回，绝不
``json.dumps`` 重新序列化（那正是丢字节的地方）。

用法::

    python3 python/aux/world_client.py --replay testdata/world_trace_golden.jsonl
    python3 python/aux/world_client.py --requests runs/scratch/reqs.jsonl

退出码：0 通过；1 校验失败（握手 / 逐字节 / 世界异常）；2 用法错误。
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import shlex
import subprocess
import sys
from pathlib import Path

# ---------------------------------------------------------------------------
# 冻结常量（抄自 docs/world-protocol.md 与 internal/world/world.go，不从 Go 导入）
# ---------------------------------------------------------------------------

#: 协议 1 = 参考的单步语义（docs/world-protocol.md 正文）；协议 2 = §8 的顺序语义；
#: 协议 3 = **另一条世界**：交换子世界（候选，docs/world-commutator-candidates.md）。
#: 客户端按 hello 里的 protocol 选维度表，绝不"先跑再看"（§8.2）。
PROTOCOL_V1 = 1
PROTOCOL_V2 = 2
PROTOCOL_V3 = 3
SUPPORTED_PROTOCOLS = (PROTOCOL_V1, PROTOCOL_V2, PROTOCOL_V3)

#: 兼容旧名字：v1 是参考语义。
PROTOCOL_VERSION = PROTOCOL_V1

#: hello 的冻结键（§3.1）；协议 2 在此之上还要有 §8.2 的五个键；协议 3 不要 spec
#: （交换子世界没有设计参数），改要一份**世界声明**。
HELLO_KEYS = ("action_dim", "observation_dim", "obs_metric_keys", "obs_metric_refs",
              "max_steps", "delta_scale", "spec", "engine", "protocol")
HELLO_KEYS_V2 = HELLO_KEYS + ("obs_keys_v2", "budget", "target", "sources", "clamp_zones")
HELLO_KEYS_V3 = ("action_dim", "observation_dim", "obs_keys_v3", "budget", "target",
                 "declaration", "engine", "protocol")

#: 协议 3 观测尾部 8 个槽位的冻结键名（§3 = hello.declaration，形状见 world.go 的 MuObsTailKeys）。
V3_TAIL_KEYS = ("mu", "eta", "window_margin", "budget_remaining_norm", "depth_norm",
                "resid_mu_window", "source_onehot_perp", "source_onehot_par")

#: 协议 3 的字段键前缀（场向量的 n 个格点）。
V3_FIELD_PREFIX = "field_"

#: 协议 3 声明里分数三项的权重键名（必须与本仓 internal/config 的那三个权重一致）。
V3_SCORE_WEIGHT_KEYS = ("mu", "gain", "closed")

#: 协议 2 观测尾部 7 个槽位的冻结键名（§8.2）。
V2_TAIL_KEYS = ("budget_remaining_norm", "depth_norm",
                "resid_death", "resid_mu_window", "resid_coil_ceiling",
                "source_onehot_perp", "source_onehot_par")

#: 协议 2 的两个场源，顺序 = hello.sources 的顺序 = source_onehot 的位序（§8.2）。
V2_SOURCES = ("MATBG_N2_perp", "MATBG_N2_par")

#: 近导线钳位半径（§8.2 的 clamp_zones.coil_proximity_floor_m）。
COIL_PROXIMITY_FLOOR_M = 0.005

# 世界命令：默认用 go run（与 scripts/verify.sh 的 G9/G10 同一做法）。
DEFAULT_WORLD = "go run ./cmd/forge world serve"
REPO_ROOT = Path(__file__).resolve().parents[2]

_JSON_DECODER = json.JSONDecoder()
_INF = float("inf")


# ---------------------------------------------------------------------------
# 规范 JSON：Go 侧 internal/world/json.go 的逐字镜像
# ---------------------------------------------------------------------------

def _shortest_digits(v: float) -> tuple[str, int]:
    """返回 (有效数字, 小数点在数字串之后的位数)。

    ``repr(float)`` 与 Go 的 Ryū 一样给出"最短且能往返"的十进制数字；这里只是把它重新
    排成 Go 的 ``'g'`` 需要的形状。
    """
    s = repr(v)
    if "e" in s or "E" in s:
        mant, _, exp = s.partition("e")
        e = int(exp)
    else:
        mant, e = s, 0
    if "." in mant:
        int_part, _, frac_part = mant.partition(".")
    else:
        int_part, frac_part = mant, ""
    digits = list(int_part + frac_part)
    dp = len(int_part) + e
    # 掐掉尾零(小数点位置不受影响: dp 数的是小数点**之前**有多少位)与前导零(每掐一位
    # 小数点就左移一位)。
    while len(digits) > 1 and digits[-1] == "0":
        digits.pop()
    while len(digits) > 1 and digits[0] == "0":
        digits.pop(0)
        dp -= 1
    return "".join(digits), dp


def go_g_format(v: float) -> str:
    """复刻 Go 的 ``strconv.FormatFloat(v, 'g', -1, 64)``。

    契约 §2 冻结的就是这个函数：最短往返 + 指数形式当且仅当十进制指数 < -4 或 >= 6
    （Go 对 ``'g'``/``prec=-1`` 的内部阈值）。spec.sha256 要求两种语言对同一批数字
    得到同一段字节，所以这条规则必须被独立重实现，而不是"相信 Go"。
    """
    if isinstance(v, bool):
        raise TypeError("go_g_format: bool is not a float")
    v = float(v)
    if v != v or v == _INF or v == -_INF:
        raise ValueError(f"go_g_format: refusing to format a non-finite value ({v!r})")
    if v == 0.0:
        return "-0" if str(v)[0] == "-" else "0"
    neg = v < 0
    digits, dp = _shortest_digits(abs(v))
    exp10 = dp - 1
    if exp10 < -4 or exp10 >= 6:
        mant = digits[0] if len(digits) == 1 else f"{digits[0]}.{digits[1:]}"
        body = f"{mant}e{exp10:+03d}"
    elif dp <= 0:
        body = "0." + "0" * (-dp) + digits
    elif dp >= len(digits):
        body = digits + "0" * (dp - len(digits))
    else:
        body = f"{digits[:dp]}.{digits[dp:]}"
    return "-" + body if neg else body


def _json_string(s: str) -> str:
    # Go 用 json.Marshal 做字符串转义。除了引号/反斜杠/控制字符之外, json.Marshal 还做
    # **HTML 转义**（<, >, & → \u003c/\u003e/\u0026），json.dumps 不做。协议 3 的世界声明里
    # 有 ">=" （目标函数那一行），跨语言复算 sha256 时这个差别会直接变成"哈希不一致"，
    # 所以这里把 Go 的转义补齐。键序与数值格式的镜像见 canonical_json。
    return (json.dumps(s, ensure_ascii=False)
            .replace("<", "\\u003c").replace(">", "\\u003e").replace("&", "\\u0026"))


def canonical_json(value) -> str:
    """spec 的规范 JSON：键排序 + 最短往返浮点（契约 §3.1）。"""
    if isinstance(value, bool):
        return "true" if value else "false"
    if value is None:
        return "null"
    if isinstance(value, int):
        return str(value)
    if isinstance(value, float):
        return go_g_format(value)
    if isinstance(value, str):
        return _json_string(value)
    if isinstance(value, list):
        return "[" + ",".join(canonical_json(v) for v in value) + "]"
    if isinstance(value, dict):
        return "{" + ",".join(f"{_json_string(k)}:{canonical_json(value[k])}"
                              for k in sorted(value)) + "}"
    raise TypeError(f"canonical_json: unsupported type {type(value).__name__}")


def canonical_spec_json(spec: dict) -> str:
    """被哈希的 spec 对象：sha256 自己不参与（那是循环的）。"""
    body = {k: v for k, v in spec.items() if k != "sha256"}
    return canonical_json(body)


def spec_sha256(spec: dict) -> str:
    return hashlib.sha256(canonical_spec_json(spec).encode("utf-8")).hexdigest()


# ---------------------------------------------------------------------------
# 握手自校验（契约 §3.1）
# ---------------------------------------------------------------------------

def check_handshake(hello: dict, label: str) -> list[str]:
    """核对一个 hello 响应。返回问题列表（空 = 通过）。"""
    errs: list[str] = []
    if not isinstance(hello, dict):
        return [f"{label}: hello is not a JSON object"]
    if hello.get("ok") is not True:
        return [f"{label}: hello is not ok: {json.dumps(hello)[:200]}"]

    protocol = hello.get("protocol")
    if protocol not in SUPPORTED_PROTOCOLS:
        return [f"{label}: protocol {protocol!r} is not one this client supports {SUPPORTED_PROTOCOLS}"]

    if protocol == PROTOCOL_V3:
        return check_handshake_v3(hello, label)

    # 维度表按协议版本选（§8.2）：协议 2 的 hello 少一个 §8.2 键就是"说不清自己是几维"。
    wanted = HELLO_KEYS_V2 if protocol == PROTOCOL_V2 else HELLO_KEYS
    for key in wanted:
        if key not in hello:
            errs.append(f"{label}: hello is missing {key!r} (protocol {protocol})")
    if errs:
        return errs

    spec = hello["spec"]
    if not isinstance(spec, dict):
        return errs + [f"{label}: hello.spec is not an object"]

    action_dim = hello["action_dim"]
    obs_dim = hello["observation_dim"]
    keys = hello["obs_metric_keys"]
    refs = hello["obs_metric_refs"]

    if not isinstance(keys, list) or not isinstance(refs, list):
        return errs + [f"{label}: obs_metric_keys/refs must be arrays"]
    if len(keys) != len(refs):
        errs.append(f"{label}: {len(keys)} obs_metric_keys vs {len(refs)} obs_metric_refs")
    if any((not isinstance(r, (int, float))) or r == 0 for r in refs):
        errs.append(f"{label}: obs_metric_refs contains a zero (the observation divides by it): {refs}")

    if protocol == PROTOCOL_V2:
        # 26 = v1 的 19 + §8.2 的 7。尾部键名必须逐字对上 —— 观测的**语义**就是这份列表。
        if obs_dim != action_dim + len(keys) + len(V2_TAIL_KEYS):
            errs.append(f"{label}: observation_dim {obs_dim} != action_dim {action_dim} + {len(keys)} metric slots "
                        f"+ {len(V2_TAIL_KEYS)} v2 slots")
        obs_keys = hello.get("obs_keys_v2")
        if not isinstance(obs_keys, list):
            errs.append(f"{label}: obs_keys_v2 must be an array")
        else:
            if len(obs_keys) != obs_dim:
                errs.append(f"{label}: obs_keys_v2 has {len(obs_keys)} entries, observation_dim is {obs_dim}")
            tail = obs_keys[len(obs_keys) - len(V2_TAIL_KEYS):]
            if tuple(tail) != V2_TAIL_KEYS:
                errs.append(f"{label}: obs_keys_v2 tail {tail} != the frozen {list(V2_TAIL_KEYS)}")
            if tuple(obs_keys[action_dim:action_dim + len(keys)]) != tuple(keys):
                errs.append(f"{label}: obs_keys_v2 does not carry the v1 metric keys at the v1 offsets "
                            f"({obs_keys[action_dim:action_dim + len(keys)]} vs {keys})")
        sources = hello.get("sources")
        if not isinstance(sources, list) or tuple(sources) != V2_SOURCES:
            errs.append(f"{label}: sources {sources!r} != the frozen {list(V2_SOURCES)}")
        budget = hello.get("budget")
        if not isinstance(budget, int) or isinstance(budget, bool) or budget <= 0:
            errs.append(f"{label}: budget {budget!r} is not a positive integer")
        target = hello.get("target")
        if not isinstance(target, (int, float)) or isinstance(target, bool) or target != target:
            errs.append(f"{label}: target {target!r} is not a number")
        zones = hello.get("clamp_zones")
        if not isinstance(zones, dict):
            errs.append(f"{label}: clamp_zones must be an object")
        elif zones.get("coil_proximity_floor_m") != COIL_PROXIMITY_FLOOR_M:
            errs.append(f"{label}: clamp_zones.coil_proximity_floor_m "
                        f"{zones.get('coil_proximity_floor_m')!r} != {COIL_PROXIMITY_FLOOR_M}")
    elif obs_dim != action_dim + len(keys):
        errs.append(f"{label}: observation_dim {obs_dim} != action_dim {action_dim} + {len(keys)} metric slots")

    n_params = spec.get("n_params")
    if action_dim != n_params:
        errs.append(f"{label}: action_dim {action_dim} != spec.n_params {n_params}")
    for bound in ("lower", "upper"):
        values = spec.get(bound)
        if not isinstance(values, list):
            errs.append(f"{label}: spec.{bound} must be an array")
        elif len(values) != n_params:
            errs.append(f"{label}: spec.{bound} has {len(values)} entries, spec.n_params is {n_params}")
    if len(spec.get("lower", [])) != len(spec.get("upper", [])):
        errs.append(f"{label}: spec.lower and spec.upper have different lengths")

    declared = spec.get("sha256")
    if not isinstance(declared, str):
        errs.append(f"{label}: spec.sha256 is not a string")
    else:
        got = spec_sha256(spec)
        if got != declared:
            errs.append(f"{label}: spec.sha256 {declared} != the sha256 of the canonical spec JSON ({got}); "
                        f"canonical bytes: {canonical_spec_json(spec)[:160]}")

    engine = hello.get("engine")
    if not isinstance(engine, dict) or not isinstance(engine.get("version"), str) or not engine["version"]:
        errs.append(f"{label}: hello.engine.version is missing")

    if not isinstance(hello["max_steps"], int) or hello["max_steps"] <= 0:
        errs.append(f"{label}: max_steps {hello['max_steps']!r} is not a positive integer")
    if not isinstance(hello["delta_scale"], (int, float)) or float(hello["delta_scale"]) <= 0:
        errs.append(f"{label}: delta_scale {hello['delta_scale']!r} is not positive")
    return errs


# ---------------------------------------------------------------------------
# 协议 3 的握手自校验：世界声明必须自己说得清
# ---------------------------------------------------------------------------

def _check_action_space(decl: dict, label: str, errs: list[str]) -> None:
    """核对声明里的动作空间：形状、one-hot、区域引用、与 regions 的一致性。"""
    space = decl.get("action_space")
    if not isinstance(space, dict):
        errs.append(f"{label}: declaration.action_space must be an object")
        return
    keys, vectors = space.get("keys"), space.get("vectors")
    regions, ops = space.get("regions"), space.get("primitives")
    if not isinstance(keys, list) or not keys or not all(isinstance(k, str) for k in keys):
        errs.append(f"{label}: action_space.keys must be a non-empty array of strings")
        return
    space_kind = space.get("kind")
    if space_kind != "declared_finite_set":
        errs.append(f"{label}: action_space.kind {space_kind!r} is not 'declared_finite_set' "
                    f"(this world's actions are a declared set, not a box)")
    if not isinstance(regions, list) or len(regions) != len(keys):
        errs.append(f"{label}: action_space.regions must have one entry per key")
        regions = [None] * len(keys)
    if not isinstance(vectors, list) or len(vectors) != len(keys):
        errs.append(f"{label}: action_space.vectors must have one vector per key")
        return
    if not isinstance(ops, list) or not ops:
        errs.append(f"{label}: action_space.primitives must be a non-empty array")
    for i, vec in enumerate(vectors):
        if not isinstance(vec, list) or len(vec) != len(keys):
            errs.append(f"{label}: action_space.vectors[{i}] has {len(vec) if isinstance(vec, list) else '?'} "
                        f"entries, want {len(keys)}")
            continue
        # one-hot: 第 i 个动作 = e_i。声明式动作空间的线编码就是它的位序。
        want = [1.0 if j == i else 0.0 for j in range(len(keys))]
        if vec != want:
            errs.append(f"{label}: action_space.vectors[{i}] = {vec} is not the one-hot encoding {want}")
    # 每个 flatten 动作引用的区域必须在 regions 里存在（名字对不上 = 客户端会以为在驱动另一个世界）。
    have = {r.get("key") for r in decl.get("regions", []) if isinstance(r, dict)}
    for i, key in enumerate(keys):
        if ":" not in key:
            continue
        op, _, target = key.partition(":")
        if op not in (ops or []):
            errs.append(f"{label}: action key {key!r} names an undeclared primitive {op!r}")
        if target not in have:
            errs.append(f"{label}: action key {key!r} names an undeclared region {target!r}")
        if regions[i] != target:
            errs.append(f"{label}: action_space.regions[{i}] = {regions[i]!r} != the region in key {key!r}")


def check_handshake_v3(hello: dict, label: str) -> list[str]:
    """核对协议 3 的 hello（交换子世界）。

    这里没有 spec：那个世界的状态是 (场向量, 区域族, μ, 预算, 源)，与设计参数无关。取而代之
    的是一份**世界声明**（declaration），它必须自己说得清三件事：
      1. 动作空间是**声明的有限集合**（每个动作一个 one-hot 线编码，区域名可解析）；
      2. 目标函数读 μ **也**读场（权重与项名都在声明里，客户端可独立复算 score）；
      3. declaration 的 sha256 能跨语言复算 —— 证明两边说的是同一个世界。
    """
    errs: list[str] = []
    for key in HELLO_KEYS_V3:
        if key not in hello:
            errs.append(f"{label}: hello is missing {key!r} (protocol {PROTOCOL_V3})")
    if errs:
        return errs

    decl = hello["declaration"]
    if not isinstance(decl, dict):
        return [f"{label}: hello.declaration is not an object"]
    dims = hello["action_dim"]
    obs_dim = hello["observation_dim"]

    _check_action_space(decl, label, errs)

    keys = hello["obs_keys_v3"]
    if not isinstance(keys, list):
        errs.append(f"{label}: obs_keys_v3 must be an array")
    else:
        if len(keys) != obs_dim:
            errs.append(f"{label}: obs_keys_v3 has {len(keys)} entries, observation_dim is {obs_dim}")
        fields = [k for k in keys if isinstance(k, str) and k.startswith(V3_FIELD_PREFIX)]
        tail = keys[len(keys) - len(V3_TAIL_KEYS):]
        n_cells = decl.get("n_cells")
        if not isinstance(n_cells, int) or isinstance(n_cells, bool) or n_cells <= 0:
            errs.append(f"{label}: declaration.n_cells {n_cells!r} is not a positive integer")
        elif len(fields) != n_cells or obs_dim != n_cells + len(V3_TAIL_KEYS):
            errs.append(f"{label}: {len(fields)} field slots != declaration.n_cells {n_cells}, or "
                        f"observation_dim {obs_dim} != n_cells + {len(V3_TAIL_KEYS)}")
        if tuple(tail) != V3_TAIL_KEYS:
            errs.append(f"{label}: obs_keys_v3 tail {tail} != the frozen {list(V3_TAIL_KEYS)}")
        if dims != len(decl.get("action_space", {}).get("keys", [])):
            errs.append(f"{label}: action_dim {dims} != the number of declared action keys "
                        f"{len(decl.get('action_space', {}).get('keys', []))}")

    score = decl.get("score")
    if not isinstance(score, dict):
        errs.append(f"{label}: declaration.score must be an object")
    else:
        weights = score.get("weights")
        terms = score.get("terms")
        if not isinstance(weights, dict) or set(weights) != set(V3_SCORE_WEIGHT_KEYS):
            errs.append(f"{label}: declaration.score.weights {weights!r} != {list(V3_SCORE_WEIGHT_KEYS)}")
        elif not all(isinstance(weights[k], (int, float)) and weights[k] == weights[k]
                     for k in V3_SCORE_WEIGHT_KEYS):
            errs.append(f"{label}: declaration.score.weights must be finite numbers: {weights!r}")
        if not isinstance(terms, list) or not all(isinstance(t, str) for t in terms):
            errs.append(f"{label}: declaration.score.terms must be an array of strings")
        if not isinstance(score.get("formula"), str) or not score.get("formula"):
            errs.append(f"{label}: declaration.score.formula is missing")

    ceiling = decl.get("mu_ceiling")
    if not isinstance(ceiling, (int, float)) or isinstance(ceiling, bool) or not 0 < ceiling < 1:
        errs.append(f"{label}: declaration.mu_ceiling {ceiling!r} is not in (0,1)")
    weight_mu = (score or {}).get("weights", {}).get("mu") if isinstance(score, dict) else None
    if isinstance(weight_mu, (int, float)) and not isinstance(weight_mu, bool) and weight_mu <= 0:
        # score 对 μ 单调 ⟸ μ 那一项系数为正；系数为 0 或负就没有"构造性非零"可言。
        errs.append(f"{label}: declaration.score.weights.mu {weight_mu!r} is not positive: "
                    f"the score would not be monotone in mu")

    sources = decl.get("sources")
    if not isinstance(sources, list) or tuple(sources) != V2_SOURCES:
        errs.append(f"{label}: declaration.sources {sources!r} != the frozen {list(V2_SOURCES)}")
    if hello.get("budget") != decl.get("budget"):
        errs.append(f"{label}: hello.budget {hello.get('budget')!r} != declaration.budget {decl.get('budget')!r}")
    if hello.get("target") != decl.get("target"):
        errs.append(f"{label}: hello.target {hello.get('target')!r} != declaration.target {decl.get('target')!r}")
    if isinstance(decl.get("mu_initial"), (int, float)) and isinstance(ceiling, (int, float)):
        if not 0 <= decl["mu_initial"] < ceiling:
            errs.append(f"{label}: declaration.mu_initial {decl['mu_initial']!r} is not in [0, mu_ceiling)")

    declared = decl.get("sha256")
    if not isinstance(declared, str):
        errs.append(f"{label}: declaration.sha256 is not a string")
    else:
        got = spec_sha256(decl)  # 同一条规则：键排序 + 最短往返浮点，sha256 自己不参与
        if got != declared:
            errs.append(f"{label}: declaration.sha256 {declared} != the sha256 of the canonical "
                        f"declaration JSON ({got}); canonical bytes: {canonical_spec_json(decl)[:200]}")

    engine = hello.get("engine")
    if not isinstance(engine, dict) or not isinstance(engine.get("version"), str) or not engine["version"]:
        errs.append(f"{label}: hello.engine.version is missing")
    return errs


# ---------------------------------------------------------------------------
# trace：原始字节的切片提取（不重新序列化）
# ---------------------------------------------------------------------------

class TraceLine:
    __slots__ = ("no", "req", "resp")

    def __init__(self, no: int, req: str, resp: str) -> None:
        self.no = no
        self.req = req
        self.resp = resp


def _value_slice(line: str, key: str, start: int = 0) -> tuple[str, int]:
    """取回 ``line`` 里 ``key`` 的值的**原始文本**与其结束下标。

    用的是 ``raw_decode`` 定位边界 + 字符串切片：得到的字节与文件里逐字节相同。
    绝不 ``json.dumps`` 重来一遍。
    """
    marker = f'"{key}"'
    at = line.index(marker, start)
    colon = line.index(":", at + len(marker))
    begin = colon + 1
    while begin < len(line) and line[begin] in " \t":
        begin += 1
    _, end = _JSON_DECODER.raw_decode(line, begin)
    return line[begin:end], end


def read_trace(path: str | Path) -> list[TraceLine]:
    """读一个 trace 文件。格式冻结在契约 §6：每行 ``{"req":<原始>,"resp":<原始>}``。"""
    path = Path(path)
    if not path.exists():
        raise SystemExit(f"world_client: trace {path} does not exist")
    out: list[TraceLine] = []
    for no, raw in enumerate(path.read_text(encoding="utf-8").splitlines(), start=1):
        if raw.strip() == "":
            raise SystemExit(f"world_client: {path} line {no}: empty line (the frozen format is one object per line)")
        try:
            req, pos = _value_slice(raw, "req")
            resp, _ = _value_slice(raw, "resp", pos)
        except ValueError as exc:
            raise SystemExit(f"world_client: {path} line {no}: cannot slice req/resp out of the line: {exc}")
        out.append(TraceLine(no, req, resp))
    return out


def trace_protocol(lines: list[TraceLine]) -> int:
    """读出 trace 里那条 hello 的**响应**声明的协议版本（§8.1）。

    ``--replay`` 忽略命令行/默认版本，以 trace 自己的 hello 响应为准 —— trace 是那段
    历史的权威。一条说不清版本的 trace 不是"回放失败"，而是没法判断该按哪套语义回放。
    """
    hello = _hello_of_trace(lines)
    if hello is None:
        raise SystemExit("world_client: the trace has no hello request (a session must start with one)")
    try:
        obj = json.loads(hello.resp)
    except json.JSONDecodeError as exc:
        raise SystemExit(f"world_client: trace line {hello.no}: the hello response is not JSON: {exc}")
    protocol = obj.get("protocol")
    if protocol not in SUPPORTED_PROTOCOLS:
        raise SystemExit(f"world_client: trace line {hello.no} declares protocol {protocol!r}, "
                         f"which this client does not support {SUPPORTED_PROTOCOLS}")
    return protocol


class Session:
    """一个已经握手过的新世界：一行请求换一行响应，外加维度表。

    计量脚本（``python/aux/world_structure.py``）用它驱动真的世界转移；它也把"协议版本
    决定维度表"这件事钉在唯一一个地方。
    """

    def __init__(self, world: "World", hello: dict) -> None:
        self.world = world
        self.hello = hello
        self.protocol = hello["protocol"]
        self.action_dim = hello["action_dim"]
        self.observation_dim = hello["observation_dim"]
        # 协议 3 是交换子世界：它没有 spec（没有设计参数），取而代之的是一份世界声明。
        # 那些只在参数世界里有意义的字段在这里诚实地缺席（lower/upper/delta_scale），
        # 而不是硬塞一个空盒子 —— 一个空盒子会让"设计向量"看上去存在。
        self.spec = hello.get("spec")
        self.declaration = hello.get("declaration")
        self.lower = list((self.spec or {}).get("lower", []))
        self.upper = list((self.spec or {}).get("upper", []))
        self.delta_scale = float(hello["delta_scale"]) if "delta_scale" in hello else 1.0
        self.sources = tuple(hello.get("sources", ("MATBG_N2_perp", "MATBG_N2_par")) if self.spec is None
                             else hello.get("sources", ()))

    # --- 只有协议 3 有的东西 -------------------------------------------------

    def action_keys(self) -> list[str]:
        """声明的动作键（协议 3）。动作**只能**从这份声明里取 —— 世界真有的动作就是它。"""
        return list((self.declaration or {}).get("action_space", {}).get("keys", []))

    def action_vectors(self) -> list[list[float]]:
        """声明的动作线编码（one-hot，位序 = action_keys 的顺序）。"""
        return [list(v) for v in (self.declaration or {}).get("action_space", {}).get("vectors", [])]

    def field_of(self, obs: list[float]) -> list[float]:
        """从协议 3 的观测里取场向量（前 n_cells 维）。"""
        n = int((self.declaration or {}).get("n_cells", 0))
        return [float(x) for x in obs[:n]]

    def request(self, line: str) -> dict:
        """发一行请求，返回解析后的响应对象（ok 不是 true 时大声失败）。"""
        raw = self.world.request(line)
        try:
            resp = json.loads(raw)
        except json.JSONDecodeError as exc:
            raise SystemExit(f"world_client: the world answered non-JSON ({exc}): {raw[:200]}")
        if resp.get("ok") is not True:
            raise SystemExit(f"world_client: the world refused {line}: {raw[:300]}")
        return resp

    def reset(self, seed: int | None = None, regime: dict | None = None,
              x0: list[float] | None = None) -> dict:
        body = {"op": "reset"}
        if x0 is not None:
            body["x0"] = list(x0)
        if seed is not None:
            body["seed"] = seed
        if regime is not None:
            body["regime"] = regime
        return self.request(json.dumps(body, separators=(",", ":")))

    def step(self, action) -> dict:
        return self.request(json.dumps({"op": "step", "action": list(action)}, separators=(",", ":")))

    def set_source(self, source: str) -> dict:
        return self.request(json.dumps({"op": "set_source", "source": source}, separators=(",", ":")))

    def close(self) -> int:
        code, err = self.world.finish()
        # WORLD_CLIENT_QUIET=1 时不再回显世界的启动横幅（world stderr 的前几行），
        # 供计量/门这类会打印长报告的场景用 —— 世界**真出错**时客户端是抛异常并把
        # stderr 带在异常里的，所以静音不会把失败吞掉。默认仍然回显。
        if err.strip() and not os.environ.get("WORLD_CLIENT_QUIET"):
            print("world_client: world stderr:", file=sys.stderr)
            for line in err.strip().splitlines():
                print(f"  {line}", file=sys.stderr)
        return code

    def metric(self, obs: list[float], key: str) -> float:
        """按 hello 的 obs_metric_keys/refs 从观测里取一个真 metric（不硬编码下标）。"""
        idx = list(self.hello["obs_metric_keys"]).index(key)
        return float(obs[self.action_dim + idx]) * float(self.hello["obs_metric_refs"][idx])

    def design_of(self, obs: list[float]) -> list[float]:
        """从观测的前 action_dim 维反解出**盒内**的设计向量（观测是归一化的）。"""
        out = []
        for i in range(self.action_dim):
            lo, hi = self.lower[i], self.upper[i]
            out.append((float(obs[i]) + 1.0) / 2.0 * (hi - lo) + lo)
        return out


def connect(command: str = DEFAULT_WORLD, extra: list[str] | None = None, protocol: int | None = None) -> Session:
    """起一个新世界、握手、自校验，返回一个 Session。

    握手不过就**不返回**：拿一个说不清维度的世界去做计量，得到的数字没有意义。
    """
    argv = list(extra or [])
    if protocol is not None:
        argv += ["--protocol", str(protocol)]
    world = World(command, argv)
    try:
        hello = json.loads(world.request('{"op":"hello"}'))
    except EOFError as exc:
        raise SystemExit(f"world_client: {exc}")
    errors = check_handshake(hello, "live hello")
    if errors:
        print("world_client: HANDSHAKE MISMATCH — refusing to use this world:", file=sys.stderr)
        for e in errors:
            print(f"  - {e}", file=sys.stderr)
        world.finish()
        raise SystemExit(1)
    return Session(world, hello)


def connect_v3(command: str = DEFAULT_WORLD, extra: list[str] | None = None) -> Session:
    """起**交换子世界**（协议 3）并握手自校验。

    --world mu 是必须的：协议 3 是另一条世界的语义，用默认世界接一条协议 3 的 hello 只会
    拿到 unsupported_protocol（那是本仓期望的失败方式 —— 大声，而不是按 v2 硬跑）。
    """
    return connect(command, ["--world", "mu"] + list(extra or []), protocol=PROTOCOL_V3)


def first_diff(got: str, want: str) -> int:
    for i, (a, b) in enumerate(zip(got, want)):
        if a != b:
            return i
    return min(len(got), len(want))


# ---------------------------------------------------------------------------
# 驱动 serve 进程
# ---------------------------------------------------------------------------

class World:
    """一个 ``forge world serve`` 子进程，按行请求/响应。"""

    def __init__(self, command: str, extra: list[str]) -> None:
        argv = shlex.split(command) + list(extra)
        self.argv = argv
        self.proc = subprocess.Popen(
            argv, cwd=str(REPO_ROOT),
            stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
            bufsize=0)
        if self.proc.stdin is None or self.proc.stdout is None:
            raise SystemExit("world_client: cannot pipe into the world process")

    def request_raw(self, raw_line: str) -> bytes:
        """发一行原始请求字节，返回**原始响应字节**（不含换行）。

        契约 §2 说逐位复现的对象是**响应字节**。所以比对的入口必须是字节：
        先解码成 str 再比，会把 `\u2212` 转义与原始 `−` 判成相同（一个字节级不同、
        字符级相同的差异），那就漏掉了唯一要抓的东西 —— 在 v1/v2 的纯 ASCII trace 上
        看不出来，在含非 ASCII 的 trace（mu 世界的 hello 里有 `−` 与中文）上就会漏。
        """
        assert self.proc.stdin is not None and self.proc.stdout is not None
        self.proc.stdin.write(raw_line.encode("utf-8") + b"\n")
        self.proc.stdin.flush()
        line = self.proc.stdout.readline()
        if not line:
            raise EOFError("the world closed stdout without answering")
        return line.rstrip(b"\n")

    def request(self, raw_line: str) -> str:
        """同 request_raw，但解码成 str（给不需要字节级判据的调用方）。"""
        return self.request_raw(raw_line).decode("utf-8")

    def finish(self) -> tuple[int, str]:
        """等世界退出，返回 (退出码, stderr)。"""
        if self.proc.stdin is not None:
            self.proc.stdin.close()
        code = self.proc.wait(timeout=120)
        err = b""
        if self.proc.stderr is not None:
            err = self.proc.stderr.read() or b""
            self.proc.stderr.close()
        return code, err.decode("utf-8", "replace")


# ---------------------------------------------------------------------------
# 三种模式
# ---------------------------------------------------------------------------

def _world_from_args(args, protocol: int | None = None) -> World:
    extra = list(args.world_arg or [])
    effective = protocol if protocol is not None else getattr(args, "protocol", None)
    if effective is not None:
        extra += ["--protocol", str(effective)]
    if effective == PROTOCOL_V3:
        # 协议 3 是**另一条世界**（交换子世界）的语义: 少一句 --world mu, 世界就会正确地
        # 拒绝（"protocol 3 belongs to the mu world"），而那时"回放失败"的理由就变成了命令行
        # 而不是协议 —— 回放必须落在**同一条世界**里, 这一句是把它钉住的那一处。
        extra += ["--world", "mu"]
    if args.max_steps is not None:
        extra += ["--max-steps", str(args.max_steps)]
    if args.delta_scale is not None:
        extra += ["--delta-scale", repr(args.delta_scale)]
    if getattr(args, "source", None) is not None:
        extra += ["--source", args.source]
    if getattr(args, "budget", None) is not None:
        extra += ["--budget", str(args.budget)]
    if getattr(args, "target", None) is not None:
        extra += ["--target", repr(args.target)]
    return World(args.world, extra)


def _hello_of_trace(lines: list[TraceLine]) -> TraceLine | None:
    for ln in lines:
        try:
            req = json.loads(ln.req)
        except json.JSONDecodeError:
            continue
        if isinstance(req, dict) and req.get("op") == "hello":
            return ln
    return None


def mode_replay(args) -> int:
    """逐字节回放（门 G18 第 1/2 条 + 握手自校验）。"""
    lines = read_trace(args.replay)
    if not lines:
        print(f"world_client: {args.replay} holds no records", file=sys.stderr)
        return 1

    # 1) 握手自校验：先核对 trace 里那条 hello 是否自洽。改坏的 hello 必须在这里死掉，
    #    而不是被当成一次普通的字节不匹配混过去。
    hello = _hello_of_trace(lines)
    if hello is None:
        print(f"world_client: {args.replay} has no hello request (a session must start with one)",
              file=sys.stderr)
        return 1
    try:
        hello_obj = json.loads(hello.resp)
    except json.JSONDecodeError as exc:
        print(f"world_client: {args.replay} line {hello.no}: hello response is not JSON: {exc}", file=sys.stderr)
        return 1
    errors = check_handshake(hello_obj, f"trace line {hello.no}")
    if errors:
        print("world_client: HANDSHAKE MISMATCH — refusing to run:", file=sys.stderr)
        for e in errors:
            print(f"  - {e}", file=sys.stderr)
        return 1

    # 2) 驱动一个新世界（协议 = trace 自己声明的那一个），逐行逐字节比对。
    traced = trace_protocol(lines)
    if hello_obj.get("protocol") != traced:
        print(f"world_client: {args.replay} line {hello.no}: the handshake passed, but it declares "
              f"protocol {hello_obj.get('protocol')!r} while the trace protocol is {traced}", file=sys.stderr)
        return 1
    print(f"world_client: replaying {args.replay} ({len(lines)} requests) byte for byte "
          f"(protocol {traced}, the trace's own)", file=sys.stderr)
    world = _world_from_args(args, protocol=traced)
    bad = 0
    bytes_compared = 0
    fatal = None
    try:
        for ln in lines:
            # 按**字节**比（契约 §2）：want 是 trace 里 resp 值的原始文本重新编回 UTF-8
            # 的字节 —— 对原始 UTF-8 与 \uXXXX 转义两种写法都逐字节还原文件内容。
            want = ln.resp.encode("utf-8")
            got = world.request_raw(ln.req)
            bytes_compared += len(want)
            if got != want:
                bad += 1
                print(f"world_client: line {ln.no} MISMATCH (first difference at byte {first_diff(got, want)})\n"
                      f"  want: {want.decode('utf-8', 'backslashreplace')[:200]}\n"
                      f"  got:  {got.decode('utf-8', 'backslashreplace')[:200]}", file=sys.stderr)
    except EOFError as exc:
        fatal = str(exc)
    code, world_err = world.finish()

    if fatal is not None:
        print(f"world_client: {fatal}", file=sys.stderr)
    if world_err.strip():
        print("world_client: world stderr:", file=sys.stderr)
        for line in world_err.strip().splitlines():
            print(f"  {line}", file=sys.stderr)
    if fatal is not None or code != 0:
        print(f"world_client: the world exited {code} (want 0 after close); replay is not evidence",
              file=sys.stderr)
        return 1
    if bad:
        print(f"world_client: FAIL — {bad} of {len(lines)} responses differ from the trace", file=sys.stderr)
        return 1
    print(f"world_client: PASS — {len(lines)} requests, {bytes_compared} bytes of responses "
          f"reproduced byte for byte by the Python client", file=sys.stderr)
    return 0


def mode_requests(args) -> int:
    """把请求行喂给一个新世界并把响应写到 stdout（stdout 上只有协议行）。"""
    reqs = [l for l in Path(args.requests).read_text(encoding="utf-8").splitlines() if l.strip() != ""]
    if not reqs:
        print(f"world_client: {args.requests} holds no requests", file=sys.stderr)
        return 1
    world = _world_from_args(args)
    checked_hello = False
    try:
        for raw in reqs:
            got = world.request(raw)
            if not checked_hello:
                try:
                    obj = json.loads(raw)
                except json.JSONDecodeError:
                    obj = None
                if isinstance(obj, dict) and obj.get("op") == "hello":
                    errors = check_handshake(json.loads(got), "live hello")
                    if errors:
                        print("world_client: HANDSHAKE MISMATCH — refusing to continue:", file=sys.stderr)
                        for e in errors:
                            print(f"  - {e}", file=sys.stderr)
                        return 1
                    checked_hello = True
            sys.stdout.write(got + "\n")
            sys.stdout.flush()
    except EOFError as exc:
        print(f"world_client: {exc}", file=sys.stderr)
        return 1
    code, world_err = world.finish()
    if world_err.strip():
        print("world_client: world stderr:", file=sys.stderr)
        for line in world_err.strip().splitlines():
            print(f"  {line}", file=sys.stderr)
    if not checked_hello:
        print("world_client: the request file never sent a hello, so nothing was handshake-checked "
              "(that is a client error, not a pass)", file=sys.stderr)
        return 1
    return 0 if code == 0 else 1


def mode_handshake(args) -> int:
    """只做握手自校验：把 hello 打印出来并核对。"""
    world = _world_from_args(args)
    try:
        got = world.request('{"op":"hello"}')
    except EOFError as exc:
        print(f"world_client: {exc}", file=sys.stderr)
        return 1
    print(got)
    code, world_err = world.finish()
    if world_err.strip():
        print("world_client: world stderr:", file=sys.stderr)
        for line in world_err.strip().splitlines():
            print(f"  {line}", file=sys.stderr)
    errors = check_handshake(json.loads(got), "live hello")
    if errors:
        print("world_client: HANDSHAKE MISMATCH:", file=sys.stderr)
        for e in errors:
            print(f"  - {e}", file=sys.stderr)
        return 1
    print("world_client: handshake OK (dims, metric keys/refs, spec.sha256)", file=sys.stderr)
    return 0 if code == 0 else 1


def build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(
        prog="world_client.py",
        description="Thin client for the frozen world protocol (docs/world-protocol.md); "
                    "the Python half of gate G18.")
    mode = p.add_mutually_exclusive_group(required=True)
    mode.add_argument("--replay", metavar="TRACE", help="replay a trace and compare every response byte for byte")
    mode.add_argument("--requests", metavar="FILE", help="feed request lines from FILE to a fresh world")
    mode.add_argument("--handshake", action="store_true", help="only hand-shake a fresh world")
    p.add_argument("--world", default=DEFAULT_WORLD,
                   help=f"the world command to drive (default: {DEFAULT_WORLD!r})")
    p.add_argument("--world-arg", action="append", metavar="ARG",
                   help="extra argument appended to the world command (repeatable)")
    p.add_argument("--protocol", type=int, choices=list(SUPPORTED_PROTOCOLS),
                   help="pass --protocol to the world (ignored by --replay: a trace is replayed "
                        "under the protocol its own hello response declares)")
    p.add_argument("--source", help="pass --source to the world (protocol 2's starting field source)")
    p.add_argument("--budget", type=int, help="pass --budget to the world (protocol 2's step budget)")
    p.add_argument("--target", type=float, help="pass --target to the world (protocol 2's termination target)")
    p.add_argument("--max-steps", type=int, help="pass --max-steps to the world (must match the trace)")
    p.add_argument("--delta-scale", type=float, help="pass --delta-scale to the world (must match the trace)")
    return p


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    if args.replay:
        return mode_replay(args)
    if args.requests:
        return mode_requests(args)
    return mode_handshake(args)


if __name__ == "__main__":
    sys.exit(main())
