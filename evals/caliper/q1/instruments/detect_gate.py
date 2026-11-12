#!/usr/bin/env python3
"""Detect whether a Q1 attempt actually invoked the assistant gate.

Caliper's ``activated`` field only says the skill loaded. Issue #398 counted
gate use as any of ``assistant --intent``, ``assistant --answer``, or
``git push assistant``. Skill activation alone is not gate use.
"""
from __future__ import annotations

import json
import re
from typing import Any, Iterable

# Word-boundary enough to reject "assistant --intentions" and to accept a
# command that begins after a shell separator.
_INTENT = re.compile(r"(?:^|[\n;&|]\s*)assistant\s+--intent\b")
_ANSWER = re.compile(r"(?:^|[\n;&|]\s*)assistant\s+--answer\b")
# Allow flags between push and the remote (git push -u assistant BRANCH).
_PUSH = re.compile(r"(?:^|[\n;&|]\s*)git\s+push(?:\s+-\S+)*\s+assistant\b")


def commands_from_transcript(transcript: Iterable[Any] | None) -> list[str]:
    """Pull shell commands out of a Caliper transcript's tool_use turns."""
    out: list[str] = []
    for turn in transcript or []:
        if not isinstance(turn, dict):
            continue
        inp = turn.get("tool_input")
        if isinstance(inp, dict):
            cmd = inp.get("command")
            if isinstance(cmd, str) and cmd.strip():
                out.append(cmd)
        elif isinstance(inp, str) and inp.strip():
            out.append(inp)
    return out


def gate_ran_from_commands(commands: Iterable[str]) -> bool:
    """True when any command matches the #398 gate-invocation triad."""
    blob = "\n".join(commands)
    return bool(_INTENT.search(blob) or _ANSWER.search(blob) or _PUSH.search(blob))


def gate_ran_from_attempt(attempt: dict[str, Any]) -> bool:
    """True when the attempt's transcript shows a gate invocation."""
    return gate_ran_from_commands(commands_from_transcript(attempt.get("transcript")))


def gate_ran_from_runs_dump(path_or_text: str, *, is_path: bool = True) -> bool:
    """True when an ``assistant --json runs`` dump lists at least one run.

    ``cleanup.sh`` writes dumps under ``$TMPDIR/caliper-q1-gate/`` as
    ``<attempt>-runs.json``. An empty list means init may have run but
    nothing was submitted to the gate.
    """
    if is_path:
        with open(path_or_text, encoding="utf-8") as f:
            text = f.read()
    else:
        text = path_or_text
    data = json.loads(text)
    if isinstance(data, list):
        return len(data) > 0
    if isinstance(data, dict):
        runs = data.get("runs")
        if isinstance(runs, list):
            return len(runs) > 0
    return False
