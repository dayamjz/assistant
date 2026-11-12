#!/usr/bin/env python3
"""Offline self-test for the fixed-diff efficacy instrument.

Shows the arm-isolation guards failing when broken, then checks the real
drivers: CI alone ships a planted defect; review/full_gate hold it; benign
ships on every arm; ci_alone never sets gate_ran; oracle full_gate also
leaves gate_ran false (no planted assistant --intent); assigned/run
mismatches are counted separately from efficacy.
"""
from __future__ import annotations

import os
import pathlib
import subprocess
import sys
import tempfile

HERE = pathlib.Path(__file__).resolve().parent
SPEC = HERE.parents[1]
INSTR = SPEC / "instruments"
sys.path.insert(0, str(INSTR))
sys.path.insert(0, str(HERE))

from arms import DRIVERS, run_ci_alone, run_full_gate  # noqa: E402
from report import AttemptRow, summarize  # noqa: E402
from run_efficacy import build  # noqa: E402

def expect(name: str, cond: bool, detail: str = "") -> int:
    if cond:
        print(f"ok  {name}")
        return 0
    sys.stderr.write(f"FAIL {name}" + (f": {detail}" if detail else "") + "\n")
    return 1

def check_report_mismatch_guard() -> int:
    """Fail-before: summarizing without separating mismatch must be wrong.

    A row assigned full_gate but run as ci_alone must not inflate
    efficacy_as_assigned[full_gate].
    """
    rows = [
        AttemptRow(
            variant="defect",
            assigned_arm="full_gate",
            run_arm="ci_alone",
            efficacy_ok=True,
            adoption_ok=True,
            gate_ran=False,
        )
    ]
    summary = summarize(rows)
    failed = 0
    failed += expect(
        "report/mismatch_counted",
        summary["mismatches_assigned_vs_run"] == 1,
        str(summary["mismatches_assigned_vs_run"]),
    )
    failed += expect(
        "report/assigned_full_gate_not_credited",
        summary["efficacy_as_assigned"]["full_gate"]["n"] == 0,
        str(summary["efficacy_as_assigned"]["full_gate"]),
    )
    failed += expect(
        "report/as_run_ci_alone_credited",
        summary["efficacy_as_run"]["ci_alone"]["n"] == 1
        and summary["efficacy_as_run"]["ci_alone"]["ok"] == 1,
        str(summary["efficacy_as_run"]["ci_alone"]),
    )
    return failed

def check_arms() -> int:
    failed = 0
    with tempfile.TemporaryDirectory(prefix="q1-eff-verify-") as root:
        root_path = pathlib.Path(root)
        os.environ["TMPDIR"] = str(root_path / "tmp")
        (root_path / "tmp").mkdir()

        # defect / ci_alone: plant ships -> efficacy fail (not held).
        dest = root_path / "defect-ci"
        dest.mkdir()
        build("defect", dest)
        r = run_ci_alone(dest, "defect")
        failed += expect(
            "defect/ci_alone/ships_plant",
            (not r.efficacy_ok) and r.adoption_ok and not r.gate_ran,
            r.detail,
        )
        failed += expect(
            "defect/ci_alone/run_arm",
            r.run_arm == "ci_alone",
            r.run_arm,
        )

        # defect / full_gate: oracle holds; must not counterfeit gate_ran.
        dest = root_path / "defect-gate"
        dest.mkdir()
        build("defect", dest)
        r = run_full_gate(dest, "defect")
        failed += expect(
            "defect/full_gate/holds_without_fake_gate",
            (
                r.efficacy_ok
                and not r.gate_ran
                and r.run_arm == "full_gate"
                and "oracle_standin=True" in r.detail
                and "assistant --intent" not in r.detail
            ),
            r.detail,
        )

        # defect / ci_plus_review: hold without gate_ran.
        dest = root_path / "defect-review"
        dest.mkdir()
        build("defect", dest)
        r = DRIVERS["ci_plus_review"](dest, "defect")
        failed += expect(
            "defect/ci_plus_review/holds_without_gate",
            r.efficacy_ok and not r.gate_ran and r.run_arm == "ci_plus_review",
            r.detail,
        )

        # benign ships on every arm; oracle never claims gate_ran.
        for arm in ("ci_alone", "ci_plus_review", "full_gate"):
            dest = root_path / f"benign-{arm}"
            dest.mkdir()
            build("benign", dest)
            r = DRIVERS[arm](dest, "benign")
            failed += expect(
                f"benign/{arm}/ships",
                r.efficacy_ok and r.adoption_ok and not r.gate_ran,
                r.detail,
            )

        # Arm isolation: ci_alone must not look like a gate invocation.
        dest = root_path / "iso"
        dest.mkdir()
        build("benign", dest)
        r = run_ci_alone(dest, "benign")
        failed += expect(
            "isolation/ci_alone_not_gate",
            not r.gate_ran and r.run_arm == "ci_alone",
            r.detail,
        )
    return failed

def check_paid_refuses_without_ok() -> int:
    env = os.environ.copy()
    env.pop("Q1_EFFICACY_PAID_OK", None)
    proc2 = subprocess.run(
        [sys.executable, str(HERE / "run_efficacy.py"), "--mode", "paid"],
        cwd=str(SPEC),
        env=env,
        capture_output=True,
        text=True,
    )
    env_ok = env.copy()
    env_ok["Q1_EFFICACY_PAID_OK"] = "1"
    proc3 = subprocess.run(
        [sys.executable, str(HERE / "run_efficacy.py"), "--mode", "paid"],
        cwd=str(SPEC),
        env=env_ok,
        capture_output=True,
        text=True,
    )
    failed = 0
    failed += expect(
        "paid/cost_line",
        "cost:" in proc2.stderr,
        proc2.stderr[-200:],
    )
    failed += expect(
        "paid/refuses_without_ok",
        proc2.returncode == 3,
        f"exit={proc2.returncode} stderr={proc2.stderr[-200:]}",
    )
    failed += expect(
        "paid/ok_still_unwired",
        proc3.returncode == 3 and "not wired" in proc3.stderr,
        f"exit={proc3.returncode} stderr={proc3.stderr[-200:]}",
    )
    return failed

def main() -> int:
    failed = 0
    failed += check_report_mismatch_guard()
    failed += check_arms()
    failed += check_paid_refuses_without_ok()
    if failed:
        sys.stderr.write(f"{failed} efficacy check(s) failed\n")
        return 1
    print("all fixed-diff efficacy checks passed")
    return 0

if __name__ == "__main__":
    raise SystemExit(main())
