#!/usr/bin/env python3
"""What the repeat gate runs for a pull request's change.

Run: git diff --name-only BASE...HEAD | python3 scripts/repeat-plan.py [--github-output FILE]

It reads changed paths on standard input and prints the plan as JSON:
the Go packages to run with -race -count=N on both OSes, whether to repeat
the leak check's own tests, and why. With --github-output it also writes
the plan as step outputs (run, packages, leak_check_tests).

RISKY is the one list of what the gate covers, each package with the
issues that put it there: every package here produced a regression on main
or a flake in the week before the gate existed, almost always in that
package's own tests. A change to a package outside it, or to a file that is
not Go source or test data, runs nothing. A change to the gate itself runs
every package here, so the gate's own pull request shows it working.
"""

import argparse
import json
import os
import sys

RISKY = {
    "internal/agents/standin": "#549 #558 #561 failed in its own tests after a change to it; #569 #459",
    "internal/agents": "#469 #515 #539",
    "internal/procgroup": "process starts; #547 changed its fork lock to fix #539",
    "internal/procgroup/guard": "the starter guard beside internal/procgroup; #547",
    "internal/service": "#552 (from #540) and #562 (from #557) failed in its own tests; #563 #548 #451",
}
LEAK_CHECK = {
    "scripts/leak-check.py": "#553 #551",
    "scripts/leak-check-test.py": "#553",
}
GATE = {".github/workflows/repeat.yml", "scripts/repeat-plan.py"}


def package_of(path):
    """The package directory a changed file belongs to: its directory, or
    the directory above testdata for a file under it."""
    parts = path.split("/")
    if "testdata" in parts[:-1]:
        return "/".join(parts[:parts.index("testdata")])
    return os.path.dirname(path)


def plan(paths):
    packages, why, leak = set(), [], False
    for path in paths:
        path = path.strip()
        if not path:
            continue
        if path in GATE:
            packages.update(RISKY)
            leak = True
            why.append(f"{path}: the gate itself changed, so it runs every package it covers")
            continue
        if path in LEAK_CHECK:
            leak = True
            why.append(f"{path}: the leak check ({LEAK_CHECK[path]})")
            continue
        package = package_of(path)
        in_testdata = package != os.path.dirname(path)
        if package in RISKY and (path.endswith(".go") or in_testdata):
            packages.add(package)
            why.append(f"{path}: {package} ({RISKY[package]})")
    ordered = sorted(packages)
    return {
        "run": bool(ordered or leak),
        "packages": " ".join(f"./{p}" for p in ordered),
        "leak_check_tests": leak,
        "why": why,
    }


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("--github-output", metavar="FILE", help="also append the plan as step outputs here")
    args = parser.parse_args(argv)
    result = plan(sys.stdin)
    print(json.dumps(result, indent=2))
    if args.github_output:
        with open(args.github_output, "a", encoding="utf-8") as out:
            out.write(f"run={str(result['run']).lower()}\n")
            out.write(f"packages={result['packages']}\n")
            out.write(f"leak_check_tests={str(result['leak_check_tests']).lower()}\n")
    return 0


if __name__ == "__main__":
    sys.exit(main())
