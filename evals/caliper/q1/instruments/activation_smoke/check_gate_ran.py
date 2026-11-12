#!/usr/bin/env python3
"""Assert the current Caliper attempt invoked the assistant gate.

Prefers live evidence from the attempt's ``.assistant`` home (a non-empty
``runs`` list). Falls back to a transcript triad check (#398) when the
caller passes an attempt dict (offline fixtures and post-run scoring).
Skill ``activated`` alone is never enough.
"""
from __future__ import annotations

import json
import os
import pathlib
import subprocess
import sys

_SPEC = pathlib.Path(__file__).resolve().parents[2]
_INSTR = _SPEC / "instruments"
if str(_INSTR) not in sys.path:
    sys.path.insert(0, str(_INSTR))

from detect_gate import (  # noqa: E402
    gate_ran_from_attempt,
    gate_ran_from_commands,
    gate_ran_from_runs_dump,
)


def _attempt_home() -> pathlib.Path | None:
    workdir = os.environ.get("CALIPER_WORKDIR")
    if workdir:
        return pathlib.Path(workdir).resolve().parent / ".assistant"
    # Assert cwd is the attempt root that holds work/ and remote.git.
    here = pathlib.Path.cwd()
    if (here / ".assistant").is_dir():
        return here / ".assistant"
    if (here / "work").is_dir() and (here / ".assistant").is_dir():
        return here / ".assistant"
    return None


def _runs_nonempty(home: pathlib.Path) -> bool:
    assistant = os.environ.get("ASSISTANT_BIN") or "assistant"
    proc = subprocess.run(
        [assistant, "--home", str(home), "--json", "runs"],
        capture_output=True,
        text=True,
    )
    if proc.returncode != 0 or not proc.stdout.strip():
        return False
    try:
        data = json.loads(proc.stdout)
    except json.JSONDecodeError:
        return False
    if isinstance(data, list):
        return len(data) > 0
    if isinstance(data, dict):
        runs = data.get("runs")
        return isinstance(runs, list) and len(runs) > 0
    return False


def require_gate_ran(attempt: dict | None = None) -> None:
    """Fail when the attempt never invoked the gate."""
    if attempt is not None:
        if gate_ran_from_attempt(attempt):
            print("gate_ran=True source=transcript")
            return
        raise AssertionError(
            "gate never ran on this attempt (transcript has no "
            "assistant --intent / --answer / git push assistant)"
        )

    home = _attempt_home()
    if home is not None and home.is_dir() and _runs_nonempty(home):
        print(f"gate_ran=True source=runs home={home.name}")
        return

    # Cleanup may already have written the dump for this attempt.
    if home is not None:
        dump = (
            pathlib.Path(os.environ.get("TMPDIR", "/tmp"))
            / "caliper-q1-gate"
            / f"{home.parent.name}-runs.json"
        )
        if dump.is_file() and gate_ran_from_runs_dump(str(dump)):
            print(f"gate_ran=True source=runs_dump path={dump.name}")
            return

    raw = os.environ.get("CALIPER_ATTEMPT_JSON")
    if raw:
        data = json.loads(raw)
        if gate_ran_from_attempt(data):
            print("gate_ran=True source=CALIPER_ATTEMPT_JSON")
            return

    raise AssertionError(
        "gate never ran on this attempt "
        "(no runs under the attempt home, no runs dump, no transcript triad)"
    )


def require_gate_ran_from_commands(commands: list[str]) -> None:
    assert gate_ran_from_commands(commands), (
        "gate never ran: no assistant --intent / --answer / git push assistant"
    )
