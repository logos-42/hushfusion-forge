#!/usr/bin/env python3
"""把一个世界 trace 的 hello 响应改坏一位, 用来做**反向断言**。

用法: world_hello_tamper.py <trace> <out>

抓的失败模式: 客户端把 trace 里的握手当装饰 —— 收到一个 declaration.sha256 与内容不符、
或 observation_dim 与维度表不符的 hello 却照跑。**改坏后回放必须非零退出**,
无论是被握手自校验拒掉, 还是逐字节比对发现不一致。

写成独立脚本而不是 verify.sh 里的 heredoc: 门里的 heredoc 嵌在 ``bash -c "..."`` 里,
外层的 ``\"`` 转义会先被拆一遍, 那层转义一旦写错, 报出来的是"heredoc 未终止",
看不出真正坏在哪(实测踩过)。脚本可以被直接跑、直接单测。
"""
from __future__ import annotations

import pathlib
import re
import sys

HEX64 = re.compile(r'"sha256":"([0-9a-f]{64})"')


def main(argv: list[str]) -> int:
    if len(argv) != 3:
        print(f"usage: {argv[0]} <trace> <out>", file=sys.stderr)
        return 2
    src, dst = pathlib.Path(argv[1]), pathlib.Path(argv[2])
    text = src.read_text(encoding="utf-8")
    m = HEX64.search(text)
    if m is None:
        print(f"world_hello_tamper: {src} 里找不到 64 位十六进制的 sha256 —— "
              f"探针没有改到任何东西, 这个反向断言就是空的", file=sys.stderr)
        return 3
    h = m.group(1)
    flip = "b" if h[0] != "b" else "c"
    broken = text[:m.start(1)] + flip + h[1:] + text[m.end(1):]
    assert broken != text
    dst.write_text(broken, encoding="utf-8")
    print(f"  tampered declaration.sha256: {h[:8]}… → {flip}{h[1:9]}…")
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
