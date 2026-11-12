#!/usr/bin/env python3
"""Fixed-diff three-arm efficacy runner (issue #895 / plan row 8).

Oracle mode is the self-test default. Paid mode prints a cost line and
exits 3 until live drivers are wired (Q1_EFFICACY_PAID_OK alone is not
enough).
"""
from __future__ import annotations

import argparse
import json
import os
import pathlib
import subprocess
import sys
import tempfile

HERE = pathlib.Path(__file__).resolve().parent
SPEC = HERE.parents[1]
sys.path.insert(0, str(SPEC / "instruments"))
sys.path.insert(0, str(HERE))
from arms import DRIVERS  # noqa: E402
from report import AttemptRow, summarize  # noqa: E402

VARIANTS = ("defect", "benign")
ARMS = ("ci_alone", "ci_plus_review", "full_gate")
_COST = (
    "cost: 3 arms x 2 variants x 1 attempt = 6 cells; "
    "paid review/full_gate estimate ~1.5M-3M tokens and ~$8-$20 "
    "API-equivalent at Sonnet list rates (illustrative; confirm with quota-axi)"
)

def build(variant: str, dest: pathlib.Path) -> None:
    env = os.environ.copy()
    env["HOME"] = str(dest / "home")
    (dest / "home").mkdir(parents=True, exist_ok=True)
    subprocess.run(
        ["sh", str(SPEC / "build-subject.sh"), variant],
        cwd=dest, env=env, check=True,
    )

def run_matrix() -> dict:
    rows: list[AttemptRow] = []
    with tempfile.TemporaryDirectory(prefix="q1-efficacy-") as root:
        root_path = pathlib.Path(root)
        os.environ["TMPDIR"] = str(root_path / "tmp")
        (root_path / "tmp").mkdir()
        for variant in VARIANTS:
            for arm in ARMS:
                dest = root_path / f"{variant}-{arm}"
                dest.mkdir()
                build(variant, dest)
                r = DRIVERS[arm](dest, variant)
                rows.append(AttemptRow(
                    variant, r.assigned_arm, r.run_arm,
                    r.efficacy_ok, r.adoption_ok, r.gate_ran, r.detail,
                ))
                print(
                    f"{variant}/{arm}: efficacy={r.efficacy_ok} "
                    f"adoption={r.adoption_ok} gate_ran={r.gate_ran} run={r.run_arm}"
                )
    return summarize(rows)

def main() -> int:
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--mode", choices=("oracle", "paid"), default="oracle")
    p.add_argument("--dry-cost", action="store_true")
    p.add_argument("--output", type=pathlib.Path, default=None)
    args = p.parse_args()
    print(_COST, file=sys.stderr)
    if args.dry_cost:
        return 0
    if args.mode == "paid":
        if os.environ.get("Q1_EFFICACY_PAID_OK") != "1":
            print(
                "refusing paid efficacy run: wait for the coordinator, then "
                "rerun with Q1_EFFICACY_PAID_OK=1",
                file=sys.stderr,
            )
            return 3
        print(
            "paid drivers are not wired yet; oracle mode measures arm "
            "isolation and reporting; live full_gate waits on coordinator OK "
            "and the row 11 agentic rerun",
            file=sys.stderr,
        )
        return 3
    summary = run_matrix()
    text = json.dumps(summary, indent=2) + "\n"
    if args.output:
        args.output.write_text(text, encoding="utf-8")
    else:
        sys.stdout.write(text)
    return 0

if __name__ == "__main__":
    raise SystemExit(main())
