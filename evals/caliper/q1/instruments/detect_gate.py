#!/usr/bin/env python3
"""Detect whether a Q1 attempt actually invoked the assistant gate.

Caliper's ``activated`` field only says the skill loaded. Issue #398 counted
gate use as any of ``assistant --intent``, ``assistant --answer``, or
``git push assistant``. Skill activation alone is not gate use.

Command text alone is not enough: a short-circuited shell line, or a heredoc
that only writes the gate command into a file, can name the gate without
launching it. Live scoring requires a paired tool_result that looks usable
(``gate_ran_from_attempt``). ``gate_ran_from_commands`` is only the offline
command-text heuristic used by verify guards; it does not apply tool_result
pairing and must not score live transcripts.
"""
from __future__ import annotations

import json
import re
import sys
from typing import Any, Iterable

# Match only at the start of the command or after a same-line shell separator.
# Do not match after an interior newline: that is how a heredoc body names the
# gate without launching it.
_GLOBAL = r"(?:\s+--\w[\w-]*(?:(?:=|\s+)\S+)?)*"
_INTENT = re.compile(rf"(?:^|[;&|]\s*)assistant{_GLOBAL}\s+--intent\b")
_ANSWER = re.compile(rf"(?:^|[;&|]\s*)assistant{_GLOBAL}\s+--answer\b")
# Real push only: --dry-run / -n never hits the receive hook.
_PUSH = re.compile(
    r"(?:^|[;&|]\s*)git\s+push(?!\s+(?:.*\s)?(?:--dry-run|-n)\b)(?:\s+-\S+)*\s+assistant\b"
)

# No-op short-circuits that skip the right-hand side. Match anywhere on the
# line (not only at BOL) so `cd work; false && assistant --intent` is rejected.
_FALSE_AND = re.compile(r"false\s*&&")
_TRUE_OR = re.compile(r"true\s*\|\|")
_DRY_RUN = re.compile(r"(?:^|\s)(?:--dry-run|-n)(?:\s|$)")

# tool_result skip shapes (not free-text "not found" / "declined" anywhere).
_DECLINED_RUN = re.compile(r"\b(?:user\s+)?declined to run\b", re.IGNORECASE)
_COMMAND_NOT_FOUND = re.compile(r"\bcommand not found\b", re.IGNORECASE)
# ash: "sh: assistant: not found"; also bare "assistant: not found".
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
    """Best-effort: false for the no-op shapes this detector rejects.

    Rejects: ``false &&`` / ``true ||`` anywhere on the line, backticks or
    ``$(…)`` anywhere (residual false negative: real ``cd "$(pwd)" &&
    assistant --intent`` is also rejected), ``--dry-run`` / ``-n``, and a
    multiline line whose triad appears only after the first newline
    (heredoc/script body). Does not claim to catch every shell non-launch.
    """
    if _FALSE_AND.search(command) or _TRUE_OR.search(command):
        return False
    if "`" in command or "$(" in command:
        return False
    if _DRY_RUN.search(command):
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
    """Offline heuristic: True when any executed-looking command matches the triad.

    Does not apply live tool_result pairing or declined/not-found rejection.
    Live scoring must use ``gate_ran_from_attempt``.
    """
    for cmd in commands:
        if _command_looks_executed(cmd) and _matches_triad(cmd):
            return True
    return False


def _tool_result_usable(turn: dict[str, Any]) -> bool:
    """True when a tool_result is a real command outcome, not a skip.

    Rejects empty text, a declined-to-run message, bash ``command not found``,
    and ``<binary>: not found`` (ash / bare). Other uses of ``not found`` or
    ``declined`` (for example ``Repository not found``) still count as usable.
    Prefers ``content``, then nonempty ``tool_output``.
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
    """True only for a Caliper tool_result turn (not user, not bare tool_output)."""
    if turn.get("role") == "tool_result" or turn.get("type") == "tool_result":
        return True
    return False


def gate_ran_from_attempt(attempt: dict[str, Any]) -> bool:
    """True when the attempt's transcript shows a gate invocation that ran.

    Live caliper transcripts pair tool_use with tool_result. A triad
    tool_use without a usable following tool_result does not count. A
    non-list transcript cannot prove that pairing, so it never counts.
    """
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
        if not cmd or not _matches_triad(cmd):
            continue
        if not _command_looks_executed(cmd):
            continue
        if i + 1 >= len(transcript) or not isinstance(transcript[i + 1], dict):
            continue
        nxt = transcript[i + 1]
        if not _is_tool_result_turn(nxt):
            continue
        if _tool_result_usable(nxt):
            return True
    return False


def gate_ran_from_runs_dump(path_or_text: str, *, is_path: bool = True) -> bool:
    """True when an ``assistant --json runs`` dump lists at least one run.

    Expects the ``machine.Runs`` shape ``{"runs":[…]}`` that
    ``assistant --json runs`` emits (and that ``cleanup.sh`` writes under
    ``$TMPDIR/caliper-q1-gate/<attempt>-runs.json``). A bare JSON list is
    not that shape and does not count.
    """
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
    """Fail-before locks for the live attempt path and runs-dump shape."""
    failed = 0

    def expect(name: str, cond: bool) -> None:
        nonlocal failed
        if cond:
            print(f"ok  {name}")
        else:
            print(f"FAIL {name}", file=sys.stderr)
            failed += 1

    expect(
        "neg/dry_run_push",
        not gate_ran_from_commands(["git push --dry-run assistant"]),
    )
    expect(
        "neg/dry_run_push_short",
        not gate_ran_from_commands(["git push -n assistant"]),
    )
    expect(
        "pos/push_assistant",
        gate_ran_from_commands(["git push assistant"]),
    )
    triad = {
        "transcript": [
            {"tool_input": {"command": 'assistant --intent "x"'}},
            {"role": "tool_result", "content": "ok"},
        ]
    }
    expect("pos/attempt_tool_result", gate_ran_from_attempt(triad))
    user_next = {
        "transcript": [
            {"tool_input": {"command": 'assistant --intent "x"'}},
            {"role": "user", "content": "please continue"},
        ]
    }
    expect("neg/attempt_user_next", not gate_ran_from_attempt(user_next))
    declined = {
        "transcript": [
            {"tool_input": {"command": 'assistant --intent "x"'}},
            {"role": "tool_result", "content": "User declined to run this command"},
        ]
    }
    expect("neg/attempt_declined", not gate_ran_from_attempt(declined))
    # commands heuristic stays weaker on purpose; live path must not follow it
    expect(
        "contract/commands_ignores_decline_text",
        gate_ran_from_commands(['assistant --intent "x"']),
    )
    expect(
        "neg/runs_bare_list",
        not gate_ran_from_runs_dump('[{"id":1}]', is_path=False),
    )
    expect(
        "pos/runs_dict",
        gate_ran_from_runs_dump('{"runs":[{"id":1}]}', is_path=False),
    )
    if failed:
        print(f"{failed} detect_gate selftest(s) failed", file=sys.stderr)
        return 1
    print("all detect_gate selftests passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(_selftest())
