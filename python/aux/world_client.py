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
import shlex
import subprocess
import sys
from pathlib import Path

# ---------------------------------------------------------------------------
# 冻结常量（抄自 docs/world-protocol.md 与 internal/world/world.go，不从 Go 导入）
# ---------------------------------------------------------------------------

PROTOCOL_VERSION = 1
HELLO_KEYS = ("action_dim", "observation_dim", "obs_metric_keys", "obs_metric_refs",
              "max_steps", "delta_scale", "spec", "engine", "protocol")

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
    # Go 用 json.Marshal 做字符串转义；对协议里出现的 ASCII 键/值两者一致。
    return json.dumps(s, ensure_ascii=False)


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

    for key in HELLO_KEYS:
        if key not in hello:
            errs.append(f"{label}: hello is missing {key!r}")
    if errs:
        return errs

    if hello["protocol"] != PROTOCOL_VERSION:
        errs.append(f"{label}: protocol {hello['protocol']!r}, this client speaks {PROTOCOL_VERSION}")

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

    if obs_dim != action_dim + len(keys):
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

    def request(self, raw_line: str) -> str:
        """发一行原始请求字节，返回原始响应行（不含换行）。"""
        assert self.proc.stdin is not None and self.proc.stdout is not None
        self.proc.stdin.write(raw_line.encode("utf-8") + b"\n")
        self.proc.stdin.flush()
        line = self.proc.stdout.readline()
        if not line:
            raise EOFError("the world closed stdout without answering")
        return line.rstrip(b"\n").decode("utf-8")

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

def _world_from_args(args) -> World:
    extra = list(args.world_arg or [])
    if args.max_steps is not None:
        extra += ["--max-steps", str(args.max_steps)]
    if args.delta_scale is not None:
        extra += ["--delta-scale", repr(args.delta_scale)]
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

    # 2) 驱动一个新世界，逐行逐字节比对。
    print(f"world_client: replaying {args.replay} ({len(lines)} requests) byte for byte",
          file=sys.stderr)
    world = _world_from_args(args)
    bad = 0
    bytes_compared = 0
    fatal = None
    try:
        for ln in lines:
            got = world.request(ln.req)
            bytes_compared += len(ln.resp)
            if got != ln.resp:
                bad += 1
                print(f"world_client: line {ln.no} MISMATCH (first difference at byte {first_diff(got, ln.resp)})\n"
                      f"  want: {ln.resp[:200]}\n"
                      f"  got:  {got[:200]}", file=sys.stderr)
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
