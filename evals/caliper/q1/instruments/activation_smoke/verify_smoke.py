#!/usr/bin/env python3
"""Offline checks for the activation smoke instrument.

Does not call a paid model. Covers:
- gate detection triad and the activated-only negative control
- smoke.eval.yaml naming defect/benign controls and activates
- grader contract for those two families (via verify_graders subset)
"""
from __future__ import annotations

import json
import pathlib
import re
import subprocess
import sys
import tempfile

HERE = pathlib.Path(__file__).resolve().parent
SPEC = HERE.parents[1]
INSTR = SPEC / "instruments"
FIX = HERE / "fixtures"
sys.path.insert(0, str(INSTR))
sys.path.insert(0, str(HERE))

from detect_gate import gate_ran_from_attempt  # noqa: E402
from check_gate_ran import require_gate_ran  # noqa: E402


def _load(name: str) -> dict:
    return json.loads((FIX / name).read_text(encoding="utf-8"))


def check_detection() -> int:
    failed = 0
    cases = [
        ("gated_attempt.json", True),
        ("answer_attempt.json", True),
        ("push_assistant_attempt.json", True),
        ("activated_only_attempt.json", False),
    ]
    for name, want in cases:
        got = gate_ran_from_attempt(_load(name))
        if got != want:
            sys.stderr.write(
                f"FAIL detect/{name}: gate_ran={got}, want {want}\n"
            )
            failed += 1
        else:
            print(f"ok  detect/{name} gate_ran={got}")
    # require_gate_ran must refuse the activated-only fixture.
    try:
        require_gate_ran(_load("activated_only_attempt.json"))
    except AssertionError:
        print("ok  require_gate_ran/activated_only_refused")
    else:
        sys.stderr.write(
            "FAIL require_gate_ran/activated_only_refused: accepted skill "
            "activation without a gate command\n"
        )
        failed += 1
    try:
        require_gate_ran(_load("gated_attempt.json"))
        print("ok  require_gate_ran/gated_accepted")
    except AssertionError as err:
        sys.stderr.write(f"FAIL require_gate_ran/gated_accepted: {err}\n")
        failed += 1
    return failed


def check_smoke_yaml() -> int:
    text = (HERE / "smoke.eval.yaml").read_text(encoding="utf-8")
    failed = 0
    if "activates: [assistant]" not in text:
        sys.stderr.write("FAIL smoke.yaml: missing activates: [assistant]\n")
        failed += 1
    else:
        print("ok  smoke.yaml/activates")
    if 'build-subject.sh" defect' not in text and "build-subject.sh defect" not in text:
        # YAML uses: build-subject.sh" defect  or with $CALIPER_SPEC_DIR
        if "defect" not in text or "benign" not in text:
            sys.stderr.write("FAIL smoke.yaml: missing defect/benign controls\n")
            failed += 1
        else:
            print("ok  smoke.yaml/controls")
    else:
        print("ok  smoke.yaml/controls")
    if "check_gate_ran.py" not in text:
        sys.stderr.write("FAIL smoke.yaml: missing per-attempt gate_ran check\n")
        failed += 1
    else:
        print("ok  smoke.yaml/gate_ran_assert")
    if re.search(r"\bk:\s*[2-9]", text):
        sys.stderr.write("FAIL smoke.yaml: smoke must stay at k=1 in the runner\n")
        failed += 1
    return failed


def check_graders_subset() -> int:
    """Defect held / benign unpushed-fail, using the real setup (#397)."""
    assistant = SPEC.parents[2] / "bin" / "assistant"
    if not os_access(assistant):
        sys.stderr.write(f"{assistant} missing; run make build\n")
        return 1
    # Reuse verify_graders by running it; it covers all families. Smoke only
    # needs defect+benign, but the shared contract must stay green.
    proc = subprocess.run(
        [sys.executable, str(SPEC / "verify_graders.py")],
        cwd=str(SPEC),
        capture_output=True,
        text=True,
    )
    if proc.returncode != 0:
        sys.stderr.write(proc.stdout + proc.stderr)
        sys.stderr.write("FAIL graders: verify_graders.py failed\n")
        return 1
    print("ok  graders/verify_graders")
    return 0


def os_access(path: pathlib.Path) -> bool:
    import os
    return os.access(path, os.X_OK)


def main() -> int:
    failed = 0
    failed += check_detection()
    failed += check_smoke_yaml()
    failed += check_graders_subset()
    if failed:
        sys.stderr.write(f"{failed} smoke check(s) failed\n")
        return 1
    print("all activation smoke checks passed")
    return 0


if __name__ == "__main__":
    with tempfile.TemporaryDirectory(prefix="q1-smoke-verify-") as state:
        import os
        os.environ["TMPDIR"] = state
        raise SystemExit(main())
