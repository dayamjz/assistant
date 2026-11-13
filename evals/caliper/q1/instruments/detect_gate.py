#!/usr/bin/env python3
"""Detect whether a Q1 attempt actually invoked the assistant gate.

Caliper's ``activated`` field only says the skill loaded. Issue #398 counted
gate use as ``assistant --intent`` / ``--answer``, or ``git push assistant``.
Live scoring is ``gate_ran_from_attempt`` (paired ``tool_result`` evidence).
``gate_ran_from_commands`` is the offline command-text heuristic only.
"""
from __future__ import annotations

import json
import re
import sys
from typing import Any, Iterable

_GLOBAL = r"(?:\s+--\w[\w-]*(?:(?:=|\s+)\S+)?)*"
_INTENT = re.compile(rf"(?:^|[;&|]\s*)assistant{_GLOBAL}\s+--intent\b")
_ANSWER = re.compile(rf"(?:^|[;&|]\s*)assistant{_GLOBAL}\s+--answer\b")
# Remote is exactly `assistant` (not assistant-backup); dry-run flags count before or after it.
_PUSH = re.compile(r"(?:^|[;&|]\s*)git\s+push((?:\s+-\S+)*)\s+assistant(?![^\s;&|)])([^;&|\n]*)")
_DRY_FLAG = re.compile(r"(?:^|\s)(?:--dry-run|-n(?!\S)|-\w*n\w*)(?:\s|$)")
_FALSE_AND = re.compile(r"false\s*&&")
_TRUE_OR = re.compile(r"true\s*\|\|")
# Evidence that a gate command reached the gate, not only that it started. The CLI
# prints its progress line (internal/cli/runs.go) before it dials the service, so a
# dial failure (internal/cli/client.go's errNoService) after it is not a reach. A push
# reached the receive hook when git 2.54 reported, under its `To` line, an updated
# ref or one the pre-receive hook declined; a local rejection or bad refspec is not.
_REACHED = {
    "intent": re.compile(r"^starting or attaching to the run for this branch\b", re.MULTILINE),
    "answer": re.compile(r"^answering ", re.MULTILINE),
    "push": re.compile(r"(?m)^To \S.*(?:\n .*)*?\n(?: [ *+-] \S| ! \[remote rejected\] .*\(pre-receive hook declined\))"),
}
_NO_SERVICE = re.compile(r"\bthe background service is not running\b")


def _strip_quotes_and_comments(command: str) -> str:
    """Unquote one-word spans (``"--dry-run"``), blank the rest, and drop ``#`` comments."""
    out: list[str] = []
    i, n = 0, len(command)
    while i < n:
        c = command[i]
        if c in "'\"":
            q = c
            i += 1
            start = i
            while i < n and command[i] != q:
                if command[i] == "\\" and q == '"':
                    i += 2
                    continue
                i += 1
            word = command[start:i]
            i += 1
            out.append(word if re.fullmatch(r"[\w./:=@+-]*", word) else " ")
            continue
        if c == "#" and (i == 0 or command[i - 1].isspace()):
            break
        out.append(c)
        i += 1
    return "".join(out)


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
    """False for literal ``false &&`` / ``true ||``, backticks / ``$(…)``, triad-only-later lines."""
    plain = _strip_quotes_and_comments(command)
    if _FALSE_AND.search(plain) or _TRUE_OR.search(plain):
        return False
    if "`" in command or "$(" in command:
        return False
    if "\n" in command:
        first, _, rest = command.partition("\n")
        if not _kinds(first) and _kinds(rest):
            return False
    return True


def _kinds(command: str) -> set[str]:
    """The kinds of gate command on the line (intent, answer, push); a dry-run push is none."""
    plain = _strip_quotes_and_comments(command)
    return {k for k, rx in (("intent", _INTENT), ("answer", _ANSWER), ("push", _PUSH))
            if (m := rx.search(plain)) and not (k == "push" and _DRY_FLAG.search(m.group(1) + m.group(2) + " "))}


def gate_ran_from_commands(commands: Iterable[str]) -> bool:
    """Offline heuristic over command text; not live tool_result pairing."""
    return any(_command_looks_executed(c) and _kinds(c) for c in commands)


def _tool_result_usable(turn: dict[str, Any], command: str) -> bool:
    """True when the result shows a gate command on the line reached the gate (``_REACHED``): a push
    only as the line's one ``git push`` (another's report looks the same), an intent or answer only if dialed."""
    content = turn.get("content")
    if not isinstance(content, str) or not content.strip():
        out = turn.get("tool_output")
        content = out if isinstance(out, str) else ""
    one_push = len(re.findall(r"\bgit\s+push\b", _strip_quotes_and_comments(command))) == 1
    return any(_REACHED[k].search(content) and (one_push if k == "push" else not _NO_SERVICE.search(content))
               for k in _kinds(command))


def _is_tool_result_turn(turn: dict[str, Any]) -> bool:
    return turn.get("role") == "tool_result" or turn.get("type") == "tool_result"


def gate_ran_from_attempt(attempt: dict[str, Any]) -> bool:
    """True when a list transcript pairs a gate tool_use with a tool_result showing it reached the gate."""
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
        if not cmd or not _command_looks_executed(cmd) or not _kinds(cmd):
            continue
        if i + 1 >= len(transcript) or not isinstance(transcript[i + 1], dict):
            continue
        nxt = transcript[i + 1]
        if _is_tool_result_turn(nxt) and _tool_result_usable(nxt, cmd):
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
    runs = data.get("runs") if isinstance(data, dict) else None
    return isinstance(runs, list) and len(runs) > 0


def _attempt(cmd: str, nxt: dict[str, Any]) -> dict[str, Any]:
    return {"transcript": [{"tool_input": {"command": cmd}}, nxt]}


def _tr(content: str, **extra: Any) -> dict[str, Any]:
    return {"role": "tool_result", "content": content, **extra}


_TO = "To ../gate.git\n * [new branch]      HEAD -> probe"
_INTENT_RAN = "starting or attaching to the run for this branch; this blocks while the run advances"
_NO_SVC = "the background service is not running at /h/socket: ipc: dialing /h/socket: dial unix /h/socket: connect: invalid argument\nStart it with: assistant service start"
_FAILED = "error: failed to push some refs to '../gate.git'"
_PREHOOK = [("git push assistant does-not-exist", f"Exit code 1\nerror: src refspec does-not-exist does not match any\n{_FAILED}"),
            ("git push assistant HEAD:refs/heads/probe", f"Exit code 1\nTo ../gate.git\n ! [rejected]        HEAD -> probe (non-fast-forward)\n{_FAILED}"),
            ("git push origin HEAD:x && git push assistant nope", f"To ../o.git\n * [new branch]      HEAD -> x\nerror: src refspec nope does not match any\n{_FAILED}")]
_DECLINED = "Exit code 1\nTo ../gate.git\n ! [remote rejected] HEAD -> d (pre-receive hook declined)\n" + _FAILED


def _selftest() -> int:
    failed = 0
    rows: list[tuple[str, bool]] = [
        ("neg/dry_run_before_remote", not any(gate_ran_from_commands([f"git push {f} assistant"]) for f in ("--dry-run", "-n", "-fn"))),
        ("pos/push", gate_ran_from_commands(["git push assistant"])),
        ("pos/push_pipe_n", gate_ran_from_commands(["git push assistant 2>&1 | head -n 40"])),
        ("pos/intent_n_text", gate_ran_from_commands(['assistant --intent "add -n"'])),
        ("neg/grep_text", not gate_ran_from_commands(['grep -rn "git push assistant" skills'])),
        ("neg/echo_quoted", not gate_ran_from_commands(['echo "next: ; assistant --intent ship"'])),
        ("pos/attempt", gate_ran_from_attempt(_attempt('assistant --intent "x"', _tr(_INTENT_RAN)))),
        ("neg/attempt_dry_run", not gate_ran_from_attempt(_attempt("git push --dry-run assistant", _tr("ok")))),
        ("neg/user_next", not gate_ran_from_attempt(_attempt('assistant --intent "x"', {"role": "user", "content": "go"}))),
        ("neg/bare_tool_output", not gate_ran_from_attempt(_attempt('assistant --intent "x"', {"tool_output": "ok"}))),
        ("neg/declined_or_not_found", not any(gate_ran_from_attempt(_attempt('assistant --intent "x"', _tr(r))) for r in (
            "User declined to run this command", "sh: 1: assistant: not found", "bash: assistant: command not found"))),
        ("pos/later_not_found", gate_ran_from_attempt(_attempt('assistant --intent "x"; mytool', _tr(f"{_INTENT_RAN}\nbash: mytool: command not found")))),
        ("neg/push_no_remote", not gate_ran_from_attempt(_attempt("git push assistant", _tr("fatal: 'assistant' does not appear to be a git repository")))),
        ("pos/empty_content_tool_output", gate_ran_from_attempt(_attempt('assistant --intent "x"', _tr("", tool_output=_INTENT_RAN)))),
        # One row per class reviews found; results recorded in Q1 or captured from zsh, git 2.54, this CLI.
        ("neg/class_short_circuit", not gate_ran_from_attempt(_attempt('assistant --cancel && assistant --intent "x"', _tr("Exit code 1\nending 9182f3851ae51d97\nThis run was ended before the gate reached a verdict.")))),
        ("neg/class_failed_cd", not any(gate_ran_from_attempt(_attempt(f"cd /missing && {c}", _tr("Exit code 1\n(eval):cd:1: no such file or directory: /missing"))) for c in ("assistant --intent ship", "git push assistant HEAD"))),
        ("neg/class_not_found", not gate_ran_from_attempt(_attempt("assistant --intent ship", _tr("Exit code 127\n(eval):1: command not found: assistant")))),
        ("neg/class_flags_after_remote", not any(gate_ran_from_attempt(_attempt(f"git push assistant {f} HEAD:refs/heads/probe", _tr(_TO))) for f in ("--dry-run", "-n", "-fn"))),
        ("neg/class_remote_prefix", not gate_ran_from_attempt(_attempt("git push assistant-backup HEAD:refs/heads/probe", _tr(_TO)))),
        ("neg/class_up_to_date", not gate_ran_from_attempt(_attempt("git push assistant HEAD", _tr("Everything up-to-date")))),
        ("neg/class_prehook_push", not any(gate_ran_from_attempt(_attempt(c, _tr(r))) for c, r in _PREHOOK)),
        ("neg/class_quoted_flag", not gate_ran_from_attempt(_attempt('cd . && git push assistant "--dry-run" HEAD:refs/heads/probe', _tr(_TO)))),
        ("neg/class_no_service", not gate_ran_from_attempt(_attempt("assistant --intent ship", _tr(f"Exit code 1\n{_INTENT_RAN}\n{_NO_SVC}")))),
        ("pos/push_quiet_hook", gate_ran_from_attempt(_attempt("git push assistant HEAD:refs/heads/probe", _tr(_TO)))),
        ("pos/push_hook_declined", gate_ran_from_attempt(_attempt("git push assistant HEAD:refs/heads/d", _tr(_DECLINED)))),
        ("pos/cd_intent_ran", gate_ran_from_attempt(_attempt("cd w && assistant --intent ship", _tr(f"Exit code 1\n{_INTENT_RAN}\nrun.start: refused")))),
        ("pos/cd_push_ran", gate_ran_from_attempt(_attempt("cd w && git push assistant HEAD", _tr(f"remote: gate hook ran\n{_TO}")))),
        ("pos/quoted_false_and", gate_ran_from_attempt(_attempt('assistant --intent "fix false && branch"', _tr(_INTENT_RAN)))),
        ("neg/bare_list", not gate_ran_from_runs_dump('[{"id":1}]', is_path=False)),
        ("pos/runs", gate_ran_from_runs_dump('{"runs":[{"id":1}]}', is_path=False)),
    ]
    for name, cond in rows:
        if cond:
            print(f"ok  {name}")
        else:
            print(f"FAIL {name}", file=sys.stderr)
            failed += 1
    if failed:
        print(f"{failed} detect_gate selftest(s) failed", file=sys.stderr)
        return 1
    print("all detect_gate selftests passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(_selftest())
