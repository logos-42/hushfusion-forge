#!/usr/bin/env python3
"""复现门 (G16)：同一个 tag 的 run，今天重跑一遍必须**逐位相同**。

为什么这道门存在
----------------
Phase 0 的核心主张是"机器赢了人工基线"——一个只由 `runs/<tag>/registry.jsonl`
里那 12001 行数字支撑的主张。若那些数字不能在另一时刻被重新生成出来，它就只是
一次性的观测记录，不是可复核的证据。这道门把"可复核"变成一条会变红的判据。

判据
----
1) 用 `runs/<tag>/results.json` 的 meta 里记录的 budget / seeds / methods / workers
   （**不硬编码**，避免"用不同参数复现出一致结果"这种自欺）重跑一次到临时目录；
2) 逐条记录比对，**剔除 `tag` 与 `timestamp`** 两个必然不同的字段（tag 是这次复跑的
   标签，timestamp 是墙钟时间），其余全部字段（含浮点数的最低位、design_id、
   parent_design 谱系、terms/metrics 全部 12 项）必须逐位相同；
3) `results.json` 剔除 meta 块后也必须整体相同。

退出码 0 = 复现成功；1 = 数字变了（不许"容差内近似"，本项目要求逐位）。
"""
from __future__ import annotations

import hashlib
import json
import os
import shutil
import subprocess
import sys
import tempfile

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
VOLATILE = ("tag", "timestamp")  # 唯一允许不同的字段


def normalize_registry(path: str) -> tuple[str, int]:
    rows = []
    with open(path, encoding="utf-8") as fh:
        for line in fh:
            line = line.strip()
            if not line:
                continue
            rec = json.loads(line)
            for key in VOLATILE:
                rec.pop(key, None)
            rows.append(json.dumps(rec, sort_keys=True))
    blob = "\n".join(rows)
    return hashlib.sha256(blob.encode()).hexdigest(), len(rows)


def compare_results(reference: str, fresh: str) -> bool:
    def strip_meta(path: str):
        doc = json.load(open(path, encoding="utf-8"))
        doc.pop("meta", None)
        return doc

    return strip_meta(reference) == strip_meta(fresh)


def main(argv=None) -> int:
    argv = sys.argv[1:] if argv is None else argv
    tag = argv[0] if argv else "phase0"
    run_dir = os.path.join(ROOT, "runs", tag)
    ref_registry = os.path.join(run_dir, "registry.jsonl")
    ref_results = os.path.join(run_dir, "results.json")
    if not os.path.exists(ref_registry) or not os.path.exists(ref_results):
        print(f"repro: 缺少 runs/{tag}/registry.jsonl 或 results.json", file=sys.stderr)
        return 2

    meta = json.load(open(ref_results, encoding="utf-8")).get("meta") or {}
    budget = meta.get("budget")
    seeds = meta.get("seeds")
    methods = meta.get("methods")
    workers = meta.get("workers", 1)
    if not budget or not seeds or not methods:
        print(f"repro: runs/{tag}/results.json 的 meta 里没有 budget/seeds/methods，无法复现",
              file=sys.stderr)
        return 2

    print(f"repro: tag={tag} budget={budget} seeds={seeds} methods={methods} workers={workers}")
    tmp = tempfile.mkdtemp(prefix="forge-repro-")
    try:
        cmd = [
            "go", "run", "./cmd/forge", "benchmark",
            "--budget", str(budget),
            "--seeds", ",".join(str(s) for s in seeds),
            "--methods", ",".join(methods),
            "--workers", str(workers),
            "--out", tmp,
            "--tag", "repro",
        ]
        print("repro: " + " ".join(cmd))
        proc = subprocess.run(cmd, cwd=ROOT, capture_output=True, text=True)
        if proc.returncode != 0:
            print("repro: 复跑失败，退出码 %d" % proc.returncode, file=sys.stderr)
            print(proc.stdout[-2000:], file=sys.stderr)
            print(proc.stderr[-2000:], file=sys.stderr)
            return 1

        h_ref, n_ref = normalize_registry(ref_registry)
        h_new, n_new = normalize_registry(os.path.join(tmp, "registry.jsonl"))
        print(f"  registry 记录数    : {n_ref} vs {n_new}")
        print(f"  registry 规范化哈希: {h_ref}")
        print(f"                       {h_new}")
        same_registry = (h_ref == h_new) and (n_ref == n_new)
        print(f"  registry 逐位一致  : {'是' if same_registry else '否'}")
        if not same_registry:
            print("  （只排除 tag 与 timestamp；任何数字变动都算失败）", file=sys.stderr)

        same_results = compare_results(ref_results, os.path.join(tmp, "results.json"))
        print(f"  results.json 一致  : {'是' if same_results else '否'}（已排除 meta 块）")
        if not same_results:
            print("  results.json（去 meta）不一致", file=sys.stderr)

        if same_registry and same_results:
            print("repro OK: 本次重跑与存档逐位相同")
            return 0
        print("repro FAIL: 重跑结果与存档不同——存档的证据不再可复核", file=sys.stderr)
        return 1
    finally:
        shutil.rmtree(tmp, ignore_errors=True)


if __name__ == "__main__":
    sys.exit(main())
