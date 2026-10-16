#!/usr/bin/env python3
"""Lightweight secret scan for local and CI gates.

The scanner checks tracked files by default and avoids printing matched secret
material. Findings include only file, line, and rule name.
"""

from __future__ import annotations

import argparse
import os
import re
import subprocess
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Iterable, Sequence


@dataclass(frozen=True)
class Rule:
    name: str
    pattern: re.Pattern[str]
    allowlist: Sequence[re.Pattern[str]]


RULES: list[Rule] = [
    Rule(
        name="google_oauth_client_secret",
        pattern=re.compile(r"GOCSPX-[0-9A-Za-z_-]{24,}"),
        allowlist=(
            re.compile(r"GOCSPX-your-"),
            re.compile(r"GOCSPX-REDACTED"),
        ),
    ),
    Rule(
        name="google_api_key",
        pattern=re.compile(r"AIza[0-9A-Za-z_-]{35}"),
        allowlist=(
            re.compile(r"AIza\.\.\."),
            re.compile(r"AIza-your-"),
            re.compile(r"AIza-REDACTED"),
        ),
    ),
]


def iter_git_files(repo_root: Path) -> list[Path]:
    try:
        out = subprocess.check_output(
            ["git", "ls-files"], cwd=repo_root, stderr=subprocess.DEVNULL, text=True
        )
    except Exception:
        return []
    files: list[Path] = []
    for line in out.splitlines():
        path = (repo_root / line).resolve()
        if path.is_file():
            files.append(path)
    return files


def iter_walk_files(repo_root: Path) -> Iterable[Path]:
    for dirpath, _dirnames, filenames in os.walk(repo_root):
        if "/.git/" in dirpath.replace("\\", "/"):
            continue
        for name in filenames:
            yield Path(dirpath) / name


def should_skip(path: Path, repo_root: Path) -> bool:
    rel = path.relative_to(repo_root).as_posix()
    if any(rel.endswith(suffix) for suffix in (".png", ".jpg", ".jpeg", ".gif", ".pdf", ".zip")):
        return True
    if rel.startswith("backend/bin/"):
        return True
    return False


def scan_file(path: Path, repo_root: Path) -> list[str]:
    try:
        raw = path.read_bytes()
    except Exception:
        return []
    try:
        text = raw.decode("utf-8")
    except UnicodeDecodeError:
        return []

    findings: list[str] = []
    for line_no, line in enumerate(text.splitlines(), start=1):
        for rule in RULES:
            if not rule.pattern.search(line):
                continue
            if any(allowed.search(line) for allowed in rule.allowlist):
                continue
            rel = path.relative_to(repo_root).as_posix()
            findings.append(f"{rel}:{line_no} ({rule.name})")
    return findings


def main(argv: Sequence[str]) -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument(
        "--repo-root",
        default=str(Path(__file__).resolve().parents[1]),
        help="repository root directory",
    )
    args = parser.parse_args(argv)

    repo_root = Path(args.repo_root).resolve()
    files = iter_git_files(repo_root)
    if not files:
        files = list(iter_walk_files(repo_root))

    findings: list[str] = []
    for path in files:
        if should_skip(path, repo_root):
            continue
        findings.extend(scan_file(path, repo_root))

    if findings:
        sys.stderr.write("Secret scan FAILED. Potential secrets detected:\n")
        for finding in findings:
            sys.stderr.write(f"- {finding}\n")
        sys.stderr.write("\nRemove secrets or replace examples with explicit placeholders.\n")
        return 1

    print("Secret scan OK")
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
