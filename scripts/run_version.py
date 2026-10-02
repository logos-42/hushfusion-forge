#!/usr/bin/env python3
"""打印一个 run 是用哪一版**判据**产出的（results.json 的 meta.forge_version）。

用法:
    python3 scripts/run_version.py <tag>          # 打印版本，未知时打印 unknown

为什么需要它（docs/version-0.1.2.md §3）：0.1.2 给目标函数加了一条可造性罚项，
于是「同一个设计的分数」变了。runs/phase0 是 **0.1.0 判据下的证据** —— 拿 0.1.2 的
判据去要求它 schema 一致 / 逐位复现，是**用错了尺子**，不是它坏了。门需要能区分
「同一判据下不可复现」（真红）与「判据换了所以本来就不该相同」（跳过）。

退出码: 0 = 打印成功（含 unknown）；2 = 用法错。
"""

from __future__ import annotations

import json
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]


def main(argv: list[str]) -> int:
    if len(argv) != 1:
        print("usage: python3 scripts/run_version.py <tag>", file=sys.stderr)
        return 2
    tag = argv[0]
    path = ROOT / "runs" / tag / "results.json"
    if not path.is_file():
        print("unknown")
        return 0
    try:
        meta = json.loads(path.read_text(encoding="utf-8")).get("meta") or {}
    except (OSError, ValueError) as e:
        print(f"unknown ({e})")
        return 0
    v = meta.get("forge_version")
    # 0.1.0 的 run 里没有这个字段：那是「判据版本还没被记录」的时代，名字如实写出来，
    # 而不是替它猜一个版本（猜出来的版本会让门绿在一个不存在的事实上）。
    print(v if v else "unknown(pre-forge_version)")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
