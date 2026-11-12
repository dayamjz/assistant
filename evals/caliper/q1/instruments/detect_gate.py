#!/usr/bin/env python3
"""Detect whether a Q1 attempt actually invoked the assistant gate.

Caliper's ``activated`` field only says the skill loaded. Issue #398 counted
gate use as any of ``assistant --intent``, ``assistant --answer``, or
``git push assistant``. Skill activation alone is not gate use.

Command text alone is not enough: a short-circuited shell line, or a heredoc
that only writes the gate command into a file, can name the gate without
launching it. Live scoring requires a paired tool_result that looks usable.
"""
from __future__ import annotations

import json
import re
from typing import Any, Iterable

# Match only at the start of the command or after a same-line shell separator.
# Do not match after an interior newline: that is how a heredoc body names the
# gate without launching it.
_GLOBAL = r"(?:\s+--\w[\w-]*(?:(?:=|\s+)\S+)?)*"
_INTENT = re.compile(rf"(?:^|[;&|]\s*)assistant{_GLOBAL}\s+--intent\b")
_ANSWER = re.compile(rf"(?:^|[;&|]\s*)assistant{_GLOBAL}\s+--answer\b")
_PUSH = re.compile(r"(?:^|[;&|]\s*)git\s+push(?:\s+-\S+)*\s+assistant\b")

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
    if _SHORT_CIRCUIT.search(command):
        return False
    # A multiline command that only embeds the triad after a newline is a
    # script/heredoc body, not a direct launch.
    if "\n" in command:
        first, _, rest = command.partition("\n")
        if not _matches_triad(first) and _matches_triad(rest):
            return False
    return True


def _matches_triad(command: str) -> bool:
    return bool(
        _INTENT.search(command) or _ANSWER.search(command) or _PUSH.search(command)
    )


def gate_ran_from_commands(commands: Iterable[str]) -> bool:
    """True when any executed-looking command matches the #398 triad."""
    for cmd in commands:
        if _command_looks_executed(cmd) and _matches_triad(cmd):
            return True
    return False


def _tool_result_usable(turn: dict[str, Any]) -> bool:
    """True when a tool_result is a real command outcome, not a skip."""
    content = turn.get("content")
    if not isinstance(content, str):
        out = turn.get("tool_output")
        content = out if isinstance(out, str) else ""
    if not content.strip():
        return False
    lower = content.lower()
    if "command not found" in lower:
        return False
    return True


def gate_ran_from_attempt(attempt: dict[str, Any]) -> bool:
    """True when the attempt's transcript shows a gate invocation that ran.

    Live caliper transcripts pair tool_use with tool_result. A triad
    tool_use without a usable following result does not count.
    """
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
        if not cmd or not _matches_triad(cmd):
            continue
        if not _command_looks_executed(cmd):
            continue
        if i + 1 >= len(transcript) or not isinstance(transcript[i + 1], dict):
            continue
        nxt = transcript[i + 1]
        if not (
            nxt.get("role") in ("tool_result", "user") or nxt.get("tool_output")
        ):
            continue
        if _tool_result_usable(nxt):
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
