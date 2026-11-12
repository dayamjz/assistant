#!/usr/bin/env python3
"""Offline guards for detect_gate (positive and negative controls)."""
from __future__ import annotations

import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
from detect_gate import (  # noqa: E402
    gate_ran_from_attempt,
    gate_ran_from_commands,
    gate_ran_from_runs_dump,
)


def expect(name: str, cond: bool, detail: str = "") -> int:
    if cond:
        print(f"ok  {name}")
        return 0
    sys.stderr.write(f"FAIL {name}" + (f": {detail}" if detail else "") + "\n")
    return 1


def main() -> int:
    failed = 0
    failed += expect(
        "pos/bare_intent",
        gate_ran_from_commands(['assistant --intent "x"']),
    )
    failed += expect(
        "pos/cd_and_intent",
        gate_ran_from_commands(['cd work && assistant --intent "x"']),
    )
    failed += expect(
        "neg/bol_false_and",
        not gate_ran_from_commands(['false && assistant --intent "x"']),
    )
    failed += expect(
        "neg/compound_false_and",
        not gate_ran_from_commands(['cd work; false && assistant --intent "x"']),
    )
    failed += expect(
        "neg/true_or",
        not gate_ran_from_commands(['true || assistant --intent "x"']),
    )
    failed += expect(
        "neg/heredoc_body",
        not gate_ran_from_commands(["cat <<EOF\nassistant --intent x\nEOF"]),
    )
    attempt_ok = {
        "transcript": [
            {
                "role": "assistant",
                "tool_input": {"command": 'assistant --intent "x"'},
            },
            {"role": "tool_result", "content": "ok"},
        ]
    }
    attempt_no_result = {
        "transcript": [
            {
                "role": "assistant",
                "tool_input": {"command": 'assistant --intent "x"'},
            },
        ]
    }
    failed += expect("pos/attempt_with_result", gate_ran_from_attempt(attempt_ok))
    failed += expect(
        "neg/attempt_without_result",
        not gate_ran_from_attempt(attempt_no_result),
    )
    failed += expect(
        "neg/runs_bare_list",
        not gate_ran_from_runs_dump("[]", is_path=False),
    )
    failed += expect(
        "pos/runs_dict",
        gate_ran_from_runs_dump('{"runs":[{"id":1}]}', is_path=False),
    )
    failed += expect(
        "neg/runs_empty_dict",
        not gate_ran_from_runs_dump('{"runs":[]}', is_path=False),
    )
    if failed:
        sys.stderr.write(f"{failed} detect_gate check(s) failed\n")
        return 1
    print("all detect_gate checks passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
