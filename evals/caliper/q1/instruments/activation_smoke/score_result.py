#!/usr/bin/env python3
"""Fail when any usable smoke attempt never invoked the gate (#398).

Caliper 0.19.0 runs assert: before cleanup and does not give assert the
transcript, so gate-ran is scored here against the result JSON after the run.
"""
from __future__ import annotations

import json
import pathlib
import sys

HERE = pathlib.Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parents[1] / "instruments"))
from detect_gate import gate_ran_from_attempt  # noqa: E402


def score(path: pathlib.Path) -> int:
    data = json.loads(path.read_text(encoding="utf-8"))
    failed = 0
    usable = 0
    for tr in data.get("task_results") or []:
        name = tr.get("task_name") or tr.get("task_id") or "?"
        for a in tr.get("attempts") or []:
            if a.get("outcome") == "infra_error":
                continue
            usable += 1
            if gate_ran_from_attempt(a):
                print(f"ok  gate_ran {name} attempt={a.get('attempt')}")
                continue
            sys.stderr.write(
                f"FAIL gate_ran {name} attempt={a.get('attempt')}: "
                "transcript has no assistant --intent / --answer / "
                "git push assistant "
                f"(activated={a.get('activated')!r})\n"
            )
            failed += 1
    if usable == 0:
        sys.stderr.write("FAIL score_result: no usable attempts in result\n")
        return 1
    if failed:
        sys.stderr.write(f"{failed}/{usable} usable attempt(s) never ran the gate\n")
        return 1
    print(f"all {usable} usable attempt(s) invoked the gate")
    return 0


def main() -> int:
    if len(sys.argv) != 2:
        sys.stderr.write(f"usage: {sys.argv[0]} result.json\n")
        return 2
    return score(pathlib.Path(sys.argv[1]))


if __name__ == "__main__":
    raise SystemExit(main())
