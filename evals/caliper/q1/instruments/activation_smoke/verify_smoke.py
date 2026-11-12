#!/usr/bin/env python3
"""Offline checks for the activation smoke instrument.

Does not call a paid model. Covers gate detection, resolved skill/bin paths,
run-smoke.sh k=1, post-run score_result, and verify_graders.
"""
from __future__ import annotations

import json
import os
import pathlib
import re
import subprocess
import sys
import tempfile

HERE = pathlib.Path(__file__).resolve().parent
SPEC = HERE.parents[1]
REPO = SPEC.parents[2]
FIX = HERE / "fixtures"
sys.path.insert(0, str(SPEC / "instruments"))
sys.path.insert(0, str(HERE))

from detect_gate import gate_ran_from_attempt  # noqa: E402
from check_gate_ran import require_gate_ran  # noqa: E402
from score_result import score  # noqa: E402


def _load(name: str) -> dict:
    return json.loads((FIX / name).read_text(encoding="utf-8"))


def check_detection() -> int:
    failed = 0
    for name, want in (
        ("gated_attempt.json", True),
        ("answer_attempt.json", True),
        ("push_assistant_attempt.json", True),
        ("activated_only_attempt.json", False),
    ):
        got = gate_ran_from_attempt(_load(name))
        if got != want:
            sys.stderr.write(f"FAIL detect/{name}: gate_ran={got}, want {want}\n")
            failed += 1
        else:
            print(f"ok  detect/{name} gate_ran={got}")
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


def check_smoke_paths_and_runner() -> int:
    failed = 0
    text = (HERE / "smoke.eval.yaml").read_text(encoding="utf-8")
    if "activates: [assistant]" not in text:
        sys.stderr.write("FAIL smoke.yaml: missing activates: [assistant]\n")
        failed += 1
    else:
        print("ok  smoke.yaml/activates")
    if "defect" not in text or "benign" not in text:
        sys.stderr.write("FAIL smoke.yaml: missing defect/benign controls\n")
        failed += 1
    else:
        print("ok  smoke.yaml/controls")

    # Resolve skills/extra_path entries and setup/cleanup script paths.
    for line in text.splitlines():
        m = re.match(r"\s*-\s+(\.\./\S+)", line)
        if m:
            rel = m.group(1)
            target = (HERE / rel).resolve()
            if not target.exists():
                sys.stderr.write(
                    f"FAIL smoke.yaml/path {rel}: resolves to missing {target}\n"
                )
                failed += 1
            else:
                print(f"ok  smoke.yaml/path {rel} -> {target.relative_to(REPO)}")
            continue
        m = re.search(r'\$CALIPER_SPEC_DIR/(\.\./[^"\s]+)/(build-subject|cleanup)\.sh', line)
        if m:
            rel = m.group(1)
            target = (HERE / rel / f"{m.group(2)}.sh").resolve()
            if not target.exists():
                sys.stderr.write(
                    f"FAIL smoke.yaml/hook {m.group(2)}.sh via {rel}: "
                    f"missing {target}\n"
                )
                failed += 1
            else:
                print(f"ok  smoke.yaml/hook {m.group(2)}.sh -> {target.relative_to(REPO)}")

    # Assert block resets CALIPER_SPEC_DIR to q1 via join(smoke, "../..").
    if 'os.path.join(smoke, "../..")' not in text:
        sys.stderr.write(
            "FAIL smoke.yaml/assert: missing join(smoke, \"../..\") for q1 graders\n"
        )
        failed += 1
    else:
        q1 = (HERE / "../..").resolve()
        if not (q1 / "check.py").is_file():
            sys.stderr.write(f"FAIL smoke.yaml/assert: {q1}/check.py missing\n")
            failed += 1
        else:
            print("ok  smoke.yaml/assert q1 graders")

    runner = (HERE / "run-smoke.sh").read_text(encoding="utf-8")
    # Comments must not satisfy --k 1; strip them before matching.
    code_only = "\n".join(ln.split("#", 1)[0] for ln in runner.splitlines())
    if not re.search(r"--k\s+1\b", code_only):
        sys.stderr.write("FAIL run-smoke.sh: missing --k 1 outside comments\n")
        failed += 1
    else:
        print("ok  run-smoke.sh/k=1")
    if "score_result.py" not in runner:
        sys.stderr.write("FAIL run-smoke.sh: missing score_result.py post-check\n")
        failed += 1
    else:
        print("ok  run-smoke.sh/score_result")
    if "caliper_rc" not in runner:
        sys.stderr.write(
            "FAIL run-smoke.sh: caliper failure must not skip score_result\n"
        )
        failed += 1
    else:
        print("ok  run-smoke.sh/score_after_caliper_failure")
    return failed


def check_score_result() -> int:
    failed = 0
    with tempfile.TemporaryDirectory(prefix="q1-score-") as td:
        good = pathlib.Path(td) / "good.json"
        bad = pathlib.Path(td) / "bad.json"
        good.write_text(json.dumps({
            "task_results": [{
                "task_name": "smoke-defect",
                "attempts": [{**_load("gated_attempt.json"), "attempt": 1}],
            }]
        }), encoding="utf-8")
        bad.write_text(json.dumps({
            "task_results": [{
                "task_name": "smoke-defect",
                "attempts": [{**_load("activated_only_attempt.json"), "attempt": 1}],
            }]
        }), encoding="utf-8")

        if score(good) != 0:
            sys.stderr.write("FAIL score_result/gated_accepted\n")
            failed += 1
        else:
            print("ok  score_result/gated_accepted")
        if score(bad) == 0:
            sys.stderr.write(
                "FAIL score_result/activated_only_refused: scored gate_ran "
                "from activation alone\n"
            )
            failed += 1
        else:
            print("ok  score_result/activated_only_refused")
    return failed


def check_graders_subset() -> int:
    assistant = REPO / "bin" / "assistant"
    if not os.access(assistant, os.X_OK):
        sys.stderr.write(f"{assistant} missing; run make build\n")
        return 1
    proc = subprocess.run(
        [sys.executable, str(SPEC / "verify_graders.py")],
        cwd=str(SPEC), capture_output=True, text=True,
    )
    if proc.returncode != 0:
        sys.stderr.write(proc.stdout + proc.stderr)
        sys.stderr.write("FAIL graders: verify_graders.py failed\n")
        return 1
    print("ok  graders/verify_graders")
    return 0


def main() -> int:
    failed = 0
    failed += check_detection()
    failed += check_smoke_paths_and_runner()
    failed += check_score_result()
    failed += check_graders_subset()
    if failed:
        sys.stderr.write(f"{failed} smoke check(s) failed\n")
        return 1
    print("all activation smoke checks passed")
    return 0


if __name__ == "__main__":
    with tempfile.TemporaryDirectory(prefix="q1-smoke-verify-") as state:
        os.environ["TMPDIR"] = state
        raise SystemExit(main())
