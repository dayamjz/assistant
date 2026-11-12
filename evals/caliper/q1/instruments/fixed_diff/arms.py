#!/usr/bin/env python3
"""Drive one fixed-diff efficacy arm on a built Q1 subject.

The harness assigns the arm. Oracle review uses planted-shape detectors on
the local work tree (wiring and reporting only; not live review quality).
"""
from __future__ import annotations

import os
import pathlib
import subprocess
import sys
from dataclasses import dataclass

SPEC = pathlib.Path(__file__).resolve().parents[2]
sys.path.insert(0, str(SPEC / "instruments"))
from detect_gate import gate_ran_from_commands  # noqa: E402


@dataclass
class ArmResult:
    assigned_arm: str
    run_arm: str
    efficacy_ok: bool
    adoption_ok: bool
    gate_ran: bool
    detail: str


def _env(home: pathlib.Path) -> dict:
    env = os.environ.copy()
    env.update({
        "HOME": str(home),
        "GIT_CONFIG_COUNT": "2",
        "GIT_CONFIG_KEY_0": "gc.auto", "GIT_CONFIG_VALUE_0": "0",
        "GIT_CONFIG_KEY_1": "maintenance.auto", "GIT_CONFIG_VALUE_1": "false",
    })
    return env


def _ci_ok(work: pathlib.Path) -> tuple[bool, str]:
    p = subprocess.run(
        ["go", "test", "-count=1", "-p", "2", "./..."],
        cwd=work, capture_output=True, text=True, timeout=60,
    )
    return p.returncode == 0, ("ci=pass" if p.returncode == 0 else f"ci=fail/{p.returncode}")


def _grade(dest: pathlib.Path, variant: str) -> tuple[bool, str]:
    env = os.environ.copy()
    env["CALIPER_SPEC_DIR"] = str(SPEC)
    script = f'import runpy; runpy.run_path("{SPEC / "check.py"}")["grade"]("{variant}")'
    p = subprocess.run(
        [sys.executable, "-c", script], cwd=dest, env=env, capture_output=True, text=True,
    )
    return p.returncode == 0, (p.stdout + p.stderr)[-300:]


def oracle_holds(work: pathlib.Path, variant: str) -> bool:
    if variant == "defect":
        src = (work / "limits" / "limits.go").read_text(encoding="utf-8")
        return "defaultLimit" in src and 'case "":' in src
    if variant == "benign":
        return "Unlimited" not in (work / "limits" / "limits.go").read_text(encoding="utf-8")
    if variant in ("overstated", "fake-shape"):
        sys.path.insert(0, str(SPEC))
        import check as c
        return (c._overstated_still_planted if variant == "overstated" else c._fake_shape_still_planted)(work)
    if variant == "boundary":
        s = (work / "internal" / "reports" / "summary.go").read_text(encoding="utf-8")
        return "database/sql" in s or "SELECT COUNT(*)" in s
    return False


def _push(work: pathlib.Path, env: dict) -> None:
    subprocess.run(
        ["git", "-C", str(work), "push", "-q", "origin", "feature/unlimited"],
        check=True, env=env,
    )


def _finish(assigned: str, cmds: list[str], efficacy: bool, adoption: bool, detail: str) -> ArmResult:
    gate = gate_ran_from_commands(cmds)
    if assigned == "ci_alone":
        run = "ci_alone" if not gate else "unknown"
    elif assigned == "ci_plus_review":
        run = "ci_plus_review" if not gate else "unknown"
    else:
        run = "full_gate" if gate else "unknown"
    return ArmResult(assigned, run, efficacy, adoption, gate, detail)


def run_ci_alone(dest: pathlib.Path, variant: str) -> ArmResult:
    work, env = dest / "work", _env(dest / "home")
    ci_ok, ci = _ci_ok(work)
    cmds: list[str] = []
    if ci_ok:
        _push(work, env)
        cmds.append("git push origin feature/unlimited")
    ok, g = _grade(dest, variant)
    return _finish("ci_alone", cmds, ok, ci_ok, f"{ci}; {g.strip()}")


def run_ci_plus_review(dest: pathlib.Path, variant: str) -> ArmResult:
    work, env = dest / "work", _env(dest / "home")
    ci_ok, ci = _ci_ok(work)
    holds = oracle_holds(work, variant)
    cmds = ["oracle_review"]
    if ci_ok and not holds:
        _push(work, env)
        cmds.append("git push origin feature/unlimited")
    ok, g = _grade(dest, variant)
    return _finish(
        "ci_plus_review", cmds, ok, ci_ok,
        f"{ci}; review={'hold' if holds else 'approve'}; {g.strip()}",
    )


def run_full_gate(dest: pathlib.Path, variant: str) -> ArmResult:
    """Oracle stand-in for the full-gate arm.

    Does not launch ``assistant``. Adoption is CI plus the oracle review
    step this driver always runs; ``gate_ran`` stays false so a planted
    intent string cannot counterfeit live gate use. Paid mode is what
    will set ``gate_ran`` once it actually invokes the binary.
    """
    work, env = dest / "work", _env(dest / "home")
    ci_ok, ci = _ci_ok(work)
    holds = oracle_holds(work, variant)
    cmds: list[str] = ["oracle_review"]
    if ci_ok and not holds:
        _push(work, env)
        cmds.append("git push origin feature/unlimited")
    ok, g = _grade(dest, variant)
    gate = gate_ran_from_commands(cmds)
    return ArmResult(
        "full_gate",
        "full_gate" if not gate else "unknown",
        ok,
        ci_ok,  # oracle review always ran
        False,  # assistant was not launched
        (
            f"{ci}; review={'hold' if holds else 'approve'}; "
            f"oracle_standin=True; gate_ran=False; {g.strip()}"
        ),
    )


DRIVERS = {
    "ci_alone": run_ci_alone,
    "ci_plus_review": run_ci_plus_review,
    "full_gate": run_full_gate,
}
