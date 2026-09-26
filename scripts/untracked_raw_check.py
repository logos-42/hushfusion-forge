from __future__ import annotations
# llm-wiki-version: 2.0.0
# runtime: ci-safe (scans only what exists; missing PROJECT_RAW_ROOT is fine)

import csv
import os
import sys
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
MANIFEST = ROOT / "manifests" / "raw_sources.csv"
IGNORE_FILE = ROOT / "manifests" / ".rawcheckignore"

# 应当登记进 manifest 的原件扩展名
RAW_EXTENSIONS = {
    ".pdf", ".xlsx", ".xls", ".xlsm", ".csv", ".tsv",
    ".doc", ".docx", ".ppt", ".pptx",
    ".jpg", ".jpeg", ".png", ".gif", ".bmp", ".svg", ".webp",
    ".zip", ".rar", ".7z", ".tar", ".gz",
    ".mp3", ".mp4", ".wav", ".mov",
}

# 整目录跳过
SKIP_DIRS = {
    ".git", "node_modules", "__pycache__", ".venv", "venv",
    ".obsidian", ".next", "dist", "build",
    "manifests",  # manifest 自身是索引,不是原件
}

# 生成物目录:这些目录里的文件是**流水线跑出来的产物**,不是"收到/提到/引用"进来的原件,
# 因此不该被要求登记进 raw_sources.csv。原件与生成物混为一谈,会让 manifest 失去意义
# (它回答的是"这份东西从哪来",而生成物的答案永远是"我们自己的 solver")。
# 生成物的来源由 wiki 页(docs/wiki/sources-and-data.md 一类)负责说明。
# 需要覆盖时不要改这里,写 manifests/.rawcheckignore(每行一个路径前缀)。
GENERATED_DIRS = {
    "runs", "outputs", "out", "artifacts", "coverage", "htmlcov", "site",
}


def load_manifest_filenames() -> set[str]:
    if not MANIFEST.exists():
        return set()
    names: set[str] = set()
    with MANIFEST.open("r", encoding="utf-8-sig", newline="") as f:
        for row in csv.DictReader(f):
            fn = (row.get("filename") or "").strip()
            if fn:
                names.add(fn.lower())
    return names


def load_ignore_prefixes() -> set[str]:
    """生成物路径前缀 = 内置默认集 + manifests/.rawcheckignore 的逐行声明。"""
    prefixes = set(GENERATED_DIRS)
    if IGNORE_FILE.exists():
        for line in IGNORE_FILE.read_text(encoding="utf-8").splitlines():
            line = line.strip()
            if not line or line.startswith("#"):
                continue
            prefixes.add(line.strip("/"))
    return prefixes


def is_ignored(rel_dir: str, child: str, prefixes: set[str]) -> bool:
    """rel_dir 是扫描根下的相对目录('' 表示根),child 是它的子目录名。"""
    candidate = f"{rel_dir}/{child}".lstrip("/")
    for p in prefixes:
        if candidate == p or candidate.startswith(p + "/"):
            return True
    return False


def main() -> int:
    known = load_manifest_filenames()
    prefixes = load_ignore_prefixes()
    scan_roots = [ROOT]

    raw_root_env = os.environ.get("PROJECT_RAW_ROOT")
    if raw_root_env:
        raw_root = Path(raw_root_env).expanduser().resolve()
        if raw_root.exists():
            scan_roots.append(raw_root)

    untracked: list[tuple[str, Path]] = []
    skipped_dirs: set[str] = set()

    for scan_root in scan_roots:
        # 生成物排除只对仓库根生效:PROJECT_RAW_ROOT 是"原件该在的地方",
        # 那里出现什么文件都是有意放进去的,不该被忽略规则静默放过。
        apply_ignore = scan_root == ROOT
        for dirpath, dirnames, filenames in os.walk(scan_root):
            here = Path(dirpath)
            rel_dir = "" if here == scan_root else here.relative_to(scan_root).as_posix()
            kept = []
            for d in dirnames:
                if d in SKIP_DIRS:
                    continue
                if apply_ignore and is_ignored(rel_dir, d, prefixes):
                    skipped_dirs.add(f"{rel_dir}/{d}".lstrip("/"))
                    continue
                kept.append(d)
            dirnames[:] = kept
            for fn in filenames:
                ext = Path(fn).suffix.lower()
                if ext in RAW_EXTENSIONS and fn.lower() not in known:
                    full = Path(dirpath) / fn
                    rel = full.relative_to(scan_root) if full.is_relative_to(scan_root) else full
                    untracked.append((fn, rel))

    if untracked:
        print(f"untracked_raw_check: FOUND {len(untracked)} untracked raw file(s)")
        print("These files exist in the project but are NOT registered in manifests/raw_sources.csv:")
        print()
        for fn, rel in sorted(untracked, key=lambda x: str(x[1])):
            print(f"  {rel}")
        print()
        print("To fix: add each file to manifests/raw_sources.csv with status 'new',")
        print("then compile its key information into the relevant wiki page.")
        print("若它其实是本项目**生成**的产物而不是外来原件,不要登记进 manifest,")
        print("而是把它的目录写进 manifests/.rawcheckignore(生成物的来源归 wiki 页说明)。")
        return 1

    if skipped_dirs:
        print(f"untracked_raw_check: OK (no untracked raw files; "
              f"跳过 {len(skipped_dirs)} 个生成物目录: {', '.join(sorted(skipped_dirs))})")
    else:
        print("untracked_raw_check: OK (no untracked raw files found)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
