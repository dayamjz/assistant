#!/usr/bin/env python3
"""Detect whether a Q1 attempt actually invoked the assistant gate.

Caliper's ``activated`` field only says the skill loaded. Issue #398 counted
gate use as ``assistant --intent`` / ``--answer``, or ``git push assistant``.
Skill activation alone is not gate use.

Live scoring is ``gate_ran_from_attempt`` (usable paired ``tool_result``).
``gate_ran_from_commands`` is the offline command-text heuristic only.
"""
from __future__ import annotations

import json
import re
import sys
from typing import Any, Iterable

# Match only at BOL or after a same-line shell separator; not after a newline
# (heredoc body naming the gate without launching it).
_GLOBAL = r"(?:\s+--\w[\w-]*(?:(?:=|\s+)\S+)?)*"
_INTENT = re.compile(rf"(?:^|[;&|]\s*)assistant{_GLOBAL}\s+--intent\b")
_ANSWER = re.compile(rf"(?:^|[;&|]\s*)assistant{_GLOBAL}\s+--answer\b")
# Real push only: --dry-run / -n never hits the receive hook.
_PUSH = re.compile(
    r"(?:^|[;&|]\s*)git\s+push(?!\s+(?:.*\s)?(?:--dry-run|-n)\b)(?:\s+-\S+)*\s+assistant\b"
)
_FALSE_AND = re.compile(r"false\s*&&")
_TRUE_OR = re.compile(r"true\s*\|\|")
_DECLINED_RUN = re.compile(r"\b(?:user\s+)?declined to run\b", re.IGNORECASE)
_COMMAND_NOT_FOUND = re.compile(r"\bcommand not found\b", re.IGNORECASE)
# ash / bare: "sh: assistant: not found" or "assistant: not found"
_ASH_BINARY_NOT_FOUND = re.compile(
    r"(?:^|\n)\s*(?:(?:/bin/)?(?:sh|bash|zsh|ash|dash)\b[^:\n]*:\s+)?\S+:\s+not found\b",
    re.IGNORECASE,
)


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
    """False for no-op shapes this detector rejects.

    Rejects ``false &&`` / ``true ||``, backticks / ``$(…)`` (also rejects
    real ``cd "$(pwd)" && assistant …``), and a multiline line whose triad
    appears only after the first newline. Dry-run pushes are excluded by
    ``_PUSH``, not here (so ``--intent "add -n"`` still counts).
    """
    if _FALSE_AND.search(command) or _TRUE_OR.search(command):
        return False
    if "`" in command or "$(" in command:
        return False
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
    """Offline heuristic over command text; not live tool_result pairing."""
    for cmd in commands:
        if _command_looks_executed(cmd) and _matches_triad(cmd):
            return True
    return False


def _tool_result_usable(turn: dict[str, Any]) -> bool:
    """True for a real command outcome, not a skip.

    Rejects empty text, declined-to-run, ``command not found``, and
    ``<binary>: not found``. Prefers ``content``, then ``tool_output``.
    """
    content = turn.get("content")
    if not isinstance(content, str) or not content.strip():
        out = turn.get("tool_output")
        content = out if isinstance(out, str) else ""
    if not content.strip():
        return False
    if _DECLINED_RUN.search(content):
        return False
    if _COMMAND_NOT_FOUND.search(content):
        return False
    if _ASH_BINARY_NOT_FOUND.search(content):
        return False
    return True


def _is_tool_result_turn(turn: dict[str, Any]) -> bool:
    return turn.get("role") == "tool_result" or turn.get("type") == "tool_result"


def gate_ran_from_attempt(attempt: dict[str, Any]) -> bool:
    """True when a list transcript shows a triad tool_use with usable tool_result."""
    transcript = attempt.get("transcript") or []
    if not isinstance(transcript, list):
        return False
    for i, turn in enumerate(transcript):
        if not isinstance(turn, dict):
            continue
        inp = turn.get("tool_input")
        cmd = None
        if isinstance(inp, dict) and isinstance(inp.get("command"), str):
            cmd = inp["command"]
        elif isinstance(inp, str):
            cmd = inp
        if not cmd or not _matches_triad(cmd) or not _command_looks_executed(cmd):
            continue
        if i + 1 >= len(transcript) or not isinstance(transcript[i + 1], dict):
            continue
        nxt = transcript[i + 1]
        if _is_tool_result_turn(nxt) and _tool_result_usable(nxt):
            return True
    return False


def gate_ran_from_runs_dump(path_or_text: str, *, is_path: bool = True) -> bool:
    """True for ``{"runs":[…]}`` with a non-empty list; bare JSON lists do not count."""
    if is_path:
        with open(path_or_text, encoding="utf-8") as f:
            text = f.read()
    else:
        text = path_or_text
    data = json.loads(text)
    if isinstance(data, dict):
        runs = data.get("runs")
        if isinstance(runs, list):
            return len(runs) > 0
    return False


def _selftest() -> int:
    failed = 0

    def expect(name: str, cond: bool) -> None:
        nonlocal failed
        if cond:
            print(f"ok  {name}")
        else:
            print(f"FAIL {name}", file=sys.stderr)
            failed += 1

    expect("neg/dry_run_push", not gate_ran_from_commands(["git push --dry-run assistant"]))
    expect("neg/dry_run_n", not gate_ran_from_commands(["git push -n assistant"]))
    expect("pos/push", gate_ran_from_commands(["git push assistant"]))
    expect(
        "pos/intent_with_n_flag_text",
        gate_ran_from_commands(['assistant --intent "add -n / --dry-run support"']),
    )
    ok = {
        "transcript": [
            {"tool_input": {"command": 'assistant --intent "x"'}},
            {"role": "tool_result", "content": "ok"},
        ]
    }
    expect("pos/attempt", gate_ran_from_attempt(ok))
    dry_att = {
        "transcript": [
            {"tool_input": {"command": "git push --dry-run assistant"}},
            {"role": "tool_result", "content": "To github.com:x.git"},
        ]
    }
    expect("neg/attempt_dry_run", not gate_ran_from_attempt(dry_att))
    user_next = {
        "transcript": [
            {"tool_input": {"command": 'assistant --intent "x"'}},
            {"role": "user", "content": "please continue"},
        ]
    }
    expect("neg/user_next", not gate_ran_from_attempt(user_next))
    bare_out = {
        "transcript": [
            {"tool_input": {"command": 'assistant --intent "x"'}},
            {"tool_output": "ok"},
        ]
    }
    expect("neg/bare_tool_output", not gate_ran_from_attempt(bare_out))
    declined = {
        "transcript": [
            {"tool_input": {"command": 'assistant --intent "x"'}},
            {"role": "tool_result", "content": "User declined to run this command"},
        ]
    }
    expect("neg/declined", not gate_ran_from_attempt(declined))
    expect("neg/bare_list", not gate_ran_from_runs_dump('[{"id":1}]', is_path=False))
    expect("pos/runs", gate_ran_from_runs_dump('{"runs":[{"id":1}]}', is_path=False))
    if failed:
        print(f"{failed} detect_gate selftest(s) failed", file=sys.stderr)
        return 1
    print("all detect_gate selftests passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(_selftest())
