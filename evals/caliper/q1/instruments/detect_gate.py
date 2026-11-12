#!/usr/bin/env python3
"""Detect whether a Q1 attempt actually invoked the assistant gate.

Caliper's ``activated`` field only says the skill loaded. Issue #398 counted
gate use as any of ``assistant --intent``, ``assistant --answer``, or
``git push assistant``. Skill activation alone is not gate use.

A command text is not enough: a short-circuited shell line can name the
gate without launching it. Prefer a tool_use whose following tool_result
shows a real exit status; fall back to bare command matching only when no
tool_result pairing is available (offline command lists).
"""
from __future__ import annotations

import json
import re
from typing import Any, Iterable

# Global flags may sit between `assistant` and --intent/--answer
# (e.g. assistant --json --home H --intent "...").
_INTENT = re.compile(
    r"(?:^|[\n;&|]\s*)assistant(?:\s+--(?!intent\b|answer\b)\S+)*\s+--intent\b"
)
_ANSWER = re.compile(
    r"(?:^|[\n;&|]\s*)assistant(?:\s+--(?!intent\b|answer\b)\S+)*\s+--answer\b"
)
# Allow flags between push and the remote (git push -u assistant BRANCH).
_PUSH = re.compile(r"(?:^|[\n;&|]\s*)git\s+push(?:\s+-\S+)*\s+assistant\b")

# Shell short-circuit / grouping that can name the gate without running it.
_SHORT_CIRCUIT = re.compile(r"(?:&&|\|\||`|\$\(|;\s*then\b)")


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


def _command_looks_executed(command: str) -> bool:
    """False when the shell line can name the gate without launching it."""
    return not _SHORT_CIRCUIT.search(command)


def gate_ran_from_commands(commands: Iterable[str]) -> bool:
    """True when any executed-looking command matches the #398 triad."""
    for cmd in commands:
        if not _command_looks_executed(cmd):
            continue
        if _INTENT.search(cmd) or _ANSWER.search(cmd) or _PUSH.search(cmd):
            return True
    return False


def _tool_result_ran(turn: dict[str, Any]) -> bool:
    """True when a tool_result shows the prior command actually ran."""
    content = turn.get("content")
    if not isinstance(content, str):
        out = turn.get("tool_output")
        if isinstance(out, str):
            content = out
        else:
            return False
    # Caliper Bash results usually begin with "Exit code N".
    if re.match(r"Exit code \d+", content):
        return True
    # A non-empty result that is not an explicit skip still counts as ran.
    return bool(content.strip()) and "command not found" not in content.lower()


def gate_ran_from_attempt(attempt: dict[str, Any]) -> bool:
    """True when the attempt's transcript shows a gate invocation that ran."""
    transcript = attempt.get("transcript") or []
    if not isinstance(transcript, list):
        return gate_ran_from_commands(commands_from_transcript(transcript))

    for i, turn in enumerate(transcript):
        if not isinstance(turn, dict):
            continue
        inp = turn.get("tool_input")
        cmd = None
        if isinstance(inp, dict) and isinstance(inp.get("command"), str):
            cmd = inp["command"]
        elif isinstance(inp, str):
            cmd = inp
        if not cmd:
            continue
        if not (
            _INTENT.search(cmd) or _ANSWER.search(cmd) or _PUSH.search(cmd)
        ):
            continue
        # Prefer evidence the tool actually ran.
        ran = False
        if i + 1 < len(transcript) and isinstance(transcript[i + 1], dict):
            nxt = transcript[i + 1]
            if nxt.get("role") in ("tool_result", "user") or nxt.get("tool_output"):
                ran = _tool_result_ran(nxt)
        if ran:
            return True
        # No paired result: only accept if the command itself cannot short-circuit.
        if _command_looks_executed(cmd):
            return True
    return False


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
