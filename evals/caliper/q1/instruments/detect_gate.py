#!/usr/bin/env python3
"""Detect whether a Q1 attempt actually invoked the assistant gate.

Caliper's ``activated`` field only says the skill loaded. Issue #398 counted
gate use as ``assistant --intent`` / ``--answer``, or ``git push assistant``.
Live scoring is ``gate_ran_from_attempt`` (paired ``tool_result`` evidence).
``gate_ran_from_commands`` is the offline command-text heuristic only.
"""
from __future__ import annotations

import json
import os
import re
import shlex
import sys
from pathlib import Path
from typing import Any, Iterable

_SEPARATORS = set(";&|()\n")
# `git push -h` of git 2.54. parse-options takes an exact long name or the one name a prefix abbreviates (`--dr`;
# `--d` is ambiguous with --delete), lets options follow positionals, ends them at `--`, and gives a value-taking
# option the next word whatever it looks like (`-o --no-dry-run`); -v -q -d -n -f -u -4 -6 take none.
_PUSH_VALUE = {"repo", "recurse-submodules", "receive-pack", "exec", "push-option"}
_PUSH_EQ_ONLY = {"force-with-lease", "signed"}
_PUSH_NEGATABLE = ("verbose quiet repo all branches mirror delete tags dry-run porcelain force force-with-lease "
                   "force-if-includes recurse-submodules thin receive-pack exec set-upstream progress prune "
                   "follow-tags signed atomic push-option").split()
_PUSH_NAMES = _PUSH_NEGATABLE + ["no-" + n for n in _PUSH_NEGATABLE] + ["verify", "no-verify", "ipv4", "ipv6"]
# git's own options before the subcommand that take the next word as their value.
_GIT_VALUE = {"-C", "-c", "--git-dir", "--work-tree", "--namespace", "--config-env", "--super-prefix"}
# Evidence a gate command reached the gate, from the gate's own output, not the command's shape: the CLI's progress
# line (internal/cli/runs.go, printed before it dials the service) without a dial failure (client.go's errNoService);
# for a push, a line git relays from the gate's admit hook (internal/cli/gate.go): machine.PhraseAdmitted,
# machine.PhraseSenderUnplaced, or a refusal under ipc.MethodGateAdmit (not the unprefixed copy-busy line a dry run gets).
_REACHED = {
    "intent": re.compile(r"^starting or attaching to the run for this branch\b", re.MULTILINE),
    "answer": re.compile(r"^answering ", re.MULTILINE),
    "push": re.compile(r"(?m)^remote: (?:The gate let this change in\.|The gate could not tell who sent this change|gate\.admit: )"),
}
_NO_SERVICE = re.compile(r"\bthe background service is not running\b")


def _prepare(command: str) -> str:
    """The line as a POSIX shell reads it before splitting words: backslash-newline joined,
    ``#`` comments (a ``#`` starting an unquoted word) dropped, a heredoc's body cut off."""
    out, quote, heredoc, i = [], "", False, 0
    while i < len(command):
        c = command[i]
        if c == "\\" and quote != "'":
            if command[i + 1:i + 2] != "\n":
                out.append(command[i:i + 2])
            i += 2
            continue
        if quote:
            quote = "" if c == quote else quote
        elif c in "'\"":
            quote = c
        elif c == "#" and (not out or out[-1][-1] in " \t\n;&|()"):
            i = command.find("\n", i) if "\n" in command[i:] else len(command)
            continue
        elif c == "\n" and heredoc:
            break
        elif command.startswith("<<", i):
            heredoc = True
        out.append(c)
        i += 1
    return "".join(out)


def _simple_commands(command: str) -> list[tuple[list[str], str]]:
    """Each simple command's words and the operator after it ("" at the end), as shlex (posix)
    splits the prepared line; redirections are dropped. An unsplittable line has none."""
    lex = shlex.shlex(_prepare(command), posix=True, punctuation_chars=";&|()<>\n")
    lex.whitespace, lex.whitespace_split, lex.commenters = " \t\r", True, ""
    try:
        words = list(lex)
    except ValueError:
        return []
    out, cur, skip = [], [], False
    for w in words:
        if set(w) <= _SEPARATORS:
            out.append((cur, w))
            cur = []
        elif set(w) & set("<>") and set(w) <= set("<>&|"):
            skip = True
        elif not skip:
            cur.append(w)
        else:
            skip = False
    return [(c, op) for c, op in out + [(cur, "")] if c]


def _program(words: list[str]) -> list[str]:
    """The words from the program on, past leading ``VAR=value`` assignments."""
    while words and re.fullmatch(r"[A-Za-z_]\w*=.*", words[0], re.S):
        words = words[1:]
    return words


def _push_long(name: str) -> str | None:
    if name in _PUSH_NAMES:
        return name
    hits = [n for n in _PUSH_NAMES if n.startswith(name)]
    return hits[0] if len(hits) == 1 else None


def _push_target(args: list[str]) -> tuple[str | None, bool] | None:
    """(repository, dry run) as git push parses ``args``; None for options git refuses."""
    repo, dry, positional, i = None, False, [], 0
    while i < len(args):
        a, i = args[i], i + 1
        if a == "--":
            positional += args[i:]
            break
        if a.startswith("--"):
            raw, eq, value = a[2:].partition("=")
            name = _push_long(raw)
            if name is None or (eq and name not in _PUSH_VALUE | _PUSH_EQ_ONLY):
                return None
            if name in _PUSH_VALUE and not eq:
                if i >= len(args):
                    return None
                value, i = args[i], i + 1
            repo = value if name == "repo" else repo
            dry = {"dry-run": True, "no-dry-run": False}.get(name, dry)
        elif a.startswith("-") and a != "-":
            for j, c in enumerate(a[1:], 1):
                if c == "o":
                    i += j == len(a) - 1
                    break
                if c not in "vqdnfu46":
                    return None
                dry = dry or c == "n"
        else:
            positional.append(a)
    return (positional[0] if positional else repo), dry


def _gate_kind(words: list[str]) -> str | None:
    """intent, answer or push when the simple command is a gate command, else None; a push
    is one only to the remote named exactly `assistant` and not a dry run."""
    words = _program(words)
    prog = os.path.basename(words[0]) if words else ""
    if prog == "assistant":
        flags = [re.fullmatch(r"--?(intent|answer)(?:=.*)?", w, re.S) for w in words[1:]]
        return next((m.group(1) for m in flags if m), None)
    i = 1
    while prog == "git" and i < len(words) and words[i].startswith("-"):
        i += 2 if words[i] in _GIT_VALUE else 1
    if prog != "git" or words[i:i + 1] != ["push"]:
        return None
    target = _push_target(words[i + 1:])
    return "push" if target and target == ("assistant", False) else None


def _command_looks_executed(command: str) -> bool:
    """False for a literal ``false &&`` / ``true ||`` and any backtick or ``$(…)`` on the line."""
    if "`" in command or "$(" in command:
        return False
    return not any((w, op) in ((["false"], "&&"), (["true"], "||")) for w, op in _simple_commands(command))


def _kinds(command: str) -> set[str]:
    """The kinds of gate command on the line (intent, answer, push)."""
    return {k for w, _ in _simple_commands(command) if (k := _gate_kind(w))}


def gate_ran_from_commands(commands: Iterable[str]) -> bool:
    """Offline heuristic over command text; not live tool_result pairing."""
    return any(_command_looks_executed(c) and _kinds(c) for c in commands)


def _tool_result_usable(turn: dict[str, Any], command: str) -> bool:
    """True when the result carries the gate's own output for a gate command on the line (``_REACHED``)."""
    content = next((c for c in (turn.get("content"), turn.get("tool_output")) if isinstance(c, str) and c.strip()), "")
    return not _NO_SERVICE.search(content) and any(_REACHED[k].search(content) for k in _kinds(command))


def gate_ran_from_attempt(attempt: dict[str, Any]) -> bool:
    """True when a list transcript pairs a gate tool_use with a tool_result showing it reached the gate."""
    transcript = attempt.get("transcript") or []
    if not isinstance(transcript, list):
        return False
    for i, turn in enumerate(transcript):
        if not isinstance(turn, dict):
            continue
        inp = turn.get("tool_input")
        cmd = inp.get("command") if isinstance(inp, dict) else inp
        if not isinstance(cmd, str) or not _kinds(cmd):
            continue
        nxt = transcript[i + 1] if i + 1 < len(transcript) else None
        if isinstance(nxt, dict) and "tool_result" in (nxt.get("role"), nxt.get("type")) and _tool_result_usable(nxt, cmd):
            return True
    return False


def gate_ran_from_runs_dump(path_or_text: str, *, is_path: bool = True) -> bool:
    """True for ``{"runs":[…]}`` with a non-empty list; bare JSON lists do not count."""
    data = json.loads(Path(path_or_text).read_text(encoding="utf-8") if is_path else path_or_text)
    runs = data.get("runs") if isinstance(data, dict) else None
    return isinstance(runs, list) and len(runs) > 0


def _attempt(cmd: str, nxt: dict[str, Any]) -> dict[str, Any]:
    return {"transcript": [{"tool_input": {"command": cmd}}, nxt]}


def _tr(content: str, **extra: Any) -> dict[str, Any]:
    return {"role": "tool_result", "content": content, **extra}


def _ran(cmd: str, content: str) -> bool:
    return gate_ran_from_attempt(_attempt(cmd, _tr(content)))


def _claims(cmd: str) -> bool:
    return gate_ran_from_commands([cmd])


_INTENT_RAN = "starting or attaching to the run for this branch; this blocks while the run advances"
_NO_SVC = "the background service is not running at /h/socket: ipc: dialing /h/socket: dial unix /h/socket: connect: invalid argument\nStart it with: assistant service start"
_FAILED = "error: failed to push some refs to '../gate.git'"
_PREHOOK = [("git push assistant does-not-exist", f"Exit code 1\nerror: src refspec does-not-exist does not match any\n{_FAILED}"),
            ("git push assistant HEAD:refs/heads/probe", f"Exit code 1\nTo ../gate.git\n ! [rejected]        HEAD -> probe (non-fast-forward)\n{_FAILED}")]


# Recorded from a real push through this CLI's gate (scratch home; paths shortened to /h).
_GATE_TO = "To /h/a/repos/24c5359d9b0ab873.git"
_ADMITTED = ("remote: The gate let this change in. It is validated before it goes anywhere. Nothing to decide yet.        \n"
             "remote:   refs/heads/feature/x        \nremote: feature/x: Validation of this change has begun. It goes on in the "
             "service, so nothing here waits for it. Ask assistant status from its branch for where it stands.        \n")
_GATE_REFUSED = ("Exit code 1\nremote: gate.admit: service: agents: no configured agent is runnable - so ... the push to gate "
                 f"24c5359d9b0ab873 is refused (internal)        \n{_GATE_TO}\n ! [remote rejected] feature/x -> feature/x (pre-receive hook declined)")
_DRY_RUN = ("A local copy for a run is being set up or cleaned up where this change is received. The change is taken in once that "
            f"is done, within a bounded time, and nothing you sent is lost meanwhile. Nothing to decide yet.\n{_GATE_TO}\n * [new branch]      feature/x -> dry")
_ORIGIN_THEN_FAILED = ("To ../o.git\n * [new branch]      HEAD -> a\nerror: src refspec does-not-exist does not match any\n"
                       "error: failed to push some refs to '/h/a/repos/24c5359d9b0ab873.git'")


def _selftest() -> int:
    failed = 0
    rows: list[tuple[str, bool]] = [
        # The parser, through the offline heuristic: what is a gate command at all.
        ("pos/commands", all(_claims(c) for c in ("git push assistant", "git push assistant 2>&1 | head -n 40", 'assistant --intent "add -n"',
            "git push -vo ci.skip assistant", "git -C w push assistant", "git -c a.b=c push assistant", "cd w\nGIT_TRACE=0 git push assistant",
            "git push -v -- assistant", "git push </dev/null assistant", "git push --no-dry assistant", "git push --dry-run --no-dry-run assistant"))),
        ("neg/text_naming_gate", not any(_claims(c) for c in ('grep -rn "git push assistant" skills', 'echo "next: ; assistant --intent ship"',
                                                             "cat <<'EOF'\ngit push assistant HEAD\nEOF"))),
        ("neg/class_flags", not any(_claims(f"git push {f}") for f in ("--dry-run assistant", "-n assistant", "-fn assistant",
                                                                       "assistant --dry-run", "assistant -n", "assistant -fn"))),
        ("neg/class_flag_spelling", not any(_claims(f"git push assistant {f}") for f in ("--dr", "--dry", "--dry-ru", "--no-dry-run --dry"))),
        ("pos/ambiguous_d_refused", _push_target(["assistant", "--d"]) is None and _push_target(["assistant", "--dr"]) == ("assistant", True)),
        ("neg/class_continuation", not _claims("git push assistant \\\n  --dry-run HEAD")),
        ("neg/class_escape", not _claims("git push assistant \\--dry-run HEAD")),
        ("neg/class_option_value", not any(_claims(f"git push assistant {f}") for f in (
            "--dry-run -o --no-dry-run", "-n --push-option --no-dry", "-n --push-o=--no-dry-run", "-n -o--no-dry-run"))),
        ("neg/class_cluster", not any(_claims(f"git push {f} assistant") for f in ("-vn", "-fnu", "-un"))),
        ("neg/class_quoting", not any(_claims(f"git push {f}") for f in ("assistant --\"dry\"-run", "assistant '-n'", "'assistant' --dry-run",
                                                                         "assistant HEAD#x --dry-run", 'assistant "--dry-run"'))),
        ("neg/class_remote_prefix", not _claims("git push assistant-backup HEAD")),
        # The live path: a claim and the gate's own output (recorded Q1, or captured from zsh, git 2.54 and this CLI's gate).
        ("pos/attempt", _ran('assistant --intent "x"', _INTENT_RAN)),
        ("neg/user_next", not gate_ran_from_attempt(_attempt('assistant --intent "x"', {"role": "user", "content": "go"}))),
        ("neg/bare_tool_output", not gate_ran_from_attempt(_attempt('assistant --intent "x"', {"tool_output": _INTENT_RAN}))),
        ("neg/declined_or_not_found", not any(_ran('assistant --intent "x"', r) for r in (
            "User declined to run this command", "sh: 1: assistant: not found", "bash: assistant: command not found"))),
        ("pos/later_not_found", _ran('assistant --intent "x"; mytool', f"{_INTENT_RAN}\nbash: mytool: command not found")),
        ("neg/push_no_remote", not _ran("git push assistant", "fatal: 'assistant' does not appear to be a git repository")),
        ("pos/empty_content_tool_output", gate_ran_from_attempt(_attempt('assistant --intent "x"', _tr("", tool_output=_INTENT_RAN)))),
        ("neg/class_short_circuit", not _ran('assistant --cancel && assistant --intent "x"', "Exit code 1\nending 9182f3851ae51d97\nThis run was ended.")),
        ("neg/class_failed_cd", not any(_ran(f"cd /missing && {c}", "Exit code 1\n(eval):cd:1: no such file or directory: /missing")
                                        for c in ("assistant --intent ship", "git push assistant HEAD"))),
        ("neg/class_not_found", not _ran("assistant --intent ship", "Exit code 127\n(eval):1: command not found: assistant")),
        ("neg/class_up_to_date", not _ran("git push assistant HEAD", "Everything up-to-date")),
        ("neg/class_prehook_push", not any(_ran(c, r) for c, r in _PREHOOK)),
        ("neg/class_no_service", not _ran("assistant --intent ship", f"Exit code 1\n{_INTENT_RAN}\n{_NO_SVC}")),
        ("neg/class_wrapped_push", not any(_ran(f"{w}git push origin HEAD:refs/heads/a && git push assistant does-not-exist",
                                                 _ORIGIN_THEN_FAILED) for w in ("command ", "env "))),
        ("neg/dry_run_output_not_evidence", not _REACHED["push"].search(_DRY_RUN)),
        ("pos/push_after_short_circuit", _ran("false && echo skipped; git push assistant feature/x", _ADMITTED + _GATE_TO)),
        ("pos/push_quiet_admitted", _ran("git push -q assistant feature/x", _ADMITTED)),
        ("pos/push_refused_by_gate", _ran("git push assistant feature/x", _GATE_REFUSED)),
        ("pos/cd_intent_ran", _ran("cd w && assistant --intent ship", f"Exit code 1\n{_INTENT_RAN}\nrun.start: refused")),
        ("pos/cd_push_ran", _ran("cd w && git push assistant feature/x", _ADMITTED + _GATE_TO)),
        ("pos/quoted_false_and", _ran('assistant --intent "fix false && branch"', _INTENT_RAN)),
        ("neg/bare_list", not gate_ran_from_runs_dump('[{"id":1}]', is_path=False)),
        ("pos/runs", gate_ran_from_runs_dump('{"runs":[{"id":1}]}', is_path=False)),
    ]
    for name, cond in rows:
        print(f"{'ok ' if cond else 'FAIL'} {name}", file=sys.stdout if cond else sys.stderr)
        failed += not cond
    if failed:
        print(f"{failed} detect_gate selftest(s) failed", file=sys.stderr)
        return 1
    print("all detect_gate selftests passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(_selftest())
