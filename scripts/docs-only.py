#!/usr/bin/env python3
"""Whether a pull request changes only documentation paths.

A docs-only change is one whose every path is under docs/ or ends in .md.
The macOS check job skips its test steps for those pull requests; Linux
still runs the full suite (it includes the Go tests that parse
docs/prd.md). Decide from the pull request's diff against its base.

Run: python3 scripts/docs-only.py --base BASE --head HEAD [--github-output FILE]
(in the checkout), or with changed paths on standard input instead.

Outputs docs_only=true when every changed path is documentation, or
docs_only=false otherwise. Fail closed: an unreadable diff, or no paths
at all, sets docs_only=false so the suite runs.
"""

from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys


def changed_paths(base, head):
    """Every path the change from BASE's merge base to HEAD touches."""
    out = subprocess.run(
        ["git", "diff", "--name-only", "--no-renames", "-z", f"{base}...{head}"],
        capture_output=True,
        text=True,
        check=True,
        timeout=120,
    ).stdout
    return [path for path in out.split("\0") if path]


def is_docs_path(path: str) -> bool:
    """True when path is under docs/ or is a Markdown file anywhere."""
    path = path.strip()
    return path.startswith("docs/") or path.endswith(".md")


def docs_only(paths) -> bool:
    """True when there is at least one path and every path is documentation.

    An empty path list is not docs-only: it usually means the diff could
    not be seen, and the suite must run (fail closed).
    """
    seen = False
    for path in paths:
        path = path.strip()
        if not path:
            continue
        seen = True
        if not is_docs_path(path):
            return False
    return seen


def main(argv=None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("--github-output", metavar="FILE")
    parser.add_argument("--base", help="the pull request's base commit (with --head)")
    parser.add_argument("--head", help="the pull request's head commit (with --base)")
    args = parser.parse_args(argv)
    if bool(args.base) != bool(args.head):
        parser.error("--base and --head go together")

    reason = ""
    try:
        paths = changed_paths(args.base, args.head) if args.base else list(sys.stdin)
        only = docs_only(paths)
        if not only and not any(p.strip() for p in paths):
            reason = "no changed paths; running the suite"
        elif not only:
            reason = "change includes non-documentation paths"
        else:
            reason = "every changed path is under docs/ or ends in .md"
    except (OSError, subprocess.SubprocessError) as error:
        only = False
        reason = f"diff not read ({error}); running the suite"
        print(f"docs-only: {reason}", file=sys.stderr)

    result = {"docs_only": only, "reason": reason}
    print(json.dumps(result, indent=2))
    if args.github_output:
        with open(args.github_output, "a", encoding="utf-8") as out:
            out.write(f"docs_only={str(only).lower()}\n")
            out.write(f"reason={reason}\n")
    return 0


if __name__ == "__main__":
    sys.exit(main())
