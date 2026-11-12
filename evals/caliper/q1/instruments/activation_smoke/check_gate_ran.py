#!/usr/bin/env python3
"""Helpers for the #398 gate-invocation check.

Live smoke scoring uses ``score_result.py`` on the caliper result JSON
(transcript available after the run). This module keeps the attempt-dict
API ``verify_smoke.py`` and offline fixtures call.
"""
from __future__ import annotations

import pathlib
import sys

_SPEC = pathlib.Path(__file__).resolve().parents[2]
_INSTR = _SPEC / "instruments"
if str(_INSTR) not in sys.path:
    sys.path.insert(0, str(_INSTR))

from detect_gate import gate_ran_from_attempt, gate_ran_from_commands  # noqa: E402


def require_gate_ran(attempt: dict) -> None:
    """Fail when the attempt dict's transcript never invoked the gate."""
    if gate_ran_from_attempt(attempt):
        print("gate_ran=True source=transcript")
        return
    raise AssertionError(
        "gate never ran on this attempt (transcript has no "
        "assistant --intent / --answer / git push assistant)"
    )


def require_gate_ran_from_commands(commands: list[str]) -> None:
    assert gate_ran_from_commands(commands), (
        "gate never ran: no assistant --intent / --answer / git push assistant"
    )
