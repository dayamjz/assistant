#!/usr/bin/env python3
"""Detect whether a Q1 attempt actually invoked the assistant gate.

Caliper's ``activated`` field only says the skill loaded. Live scoring is
``attempt_verdict`` / ``gate_ran_from_attempt``: proven only by the gate's own
admit-hook output in the result paired by id with a push the parser fully read. ``gate_ran_from_commands``
is the offline command-text heuristic only.
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
# The only proof of a gate run (owner decision, round 6): a line git relays from the gate's admit hook
# (internal/cli/gate.go): machine.PhraseAdmitted, machine.PhraseSenderUnplaced, or a refusal under
# ipc.MethodGateAdmit. Not the CLI's progress line (printed before it dials the service) or a dry run's
# unprefixed copy-busy line.
_ADMIT = re.compile(r"(?m)^remote: (?:The gate let this change in\.|The gate could not tell who sent this change|gate\.admit: )")
# Not read, so not proven: a line with one of these marks (a newline, continuations included), or with a command that
# begins with a reserved word (bash's, plus zsh's foreach end repeat nocorrect), so no compound body reads as a command.
_UNREADABLE = (("<<", "heredoc or herestring"), ("`", "command substitution"), ("$(", "command substitution"), ("#", "comment"),
               ("\n", "multiline command"))
_RESERVED = set("! [[ ]] { } case coproc do done elif else end esac fi for foreach function if in nocorrect repeat select then time until while".split())

def _simple_commands(command: str) -> list[tuple[list[str], str]] | None:
    """Each simple command's words and the operator after it ("" at the end), as shlex (posix)
    splits the line; redirections are dropped. None when shlex cannot split it."""
    lex = shlex.shlex(command, posix=True, punctuation_chars=";&|()<>\n")
    lex.whitespace, lex.whitespace_split, lex.commenters = " \t\r", True, ""
    try:
        words = list(lex)
    except ValueError:
        return None
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


def _unreadable(command: str) -> str | None:
    """Why the parser cannot fully read the line, or None when it can."""
    if hit := next((why for mark, why in _UNREADABLE if mark in command), None):
        return hit
    if (cmds := _simple_commands(command)) is None:
        return "unbalanced quoting"
    return "compound command" if any(w[0] in _RESERVED for w, _ in cmds) else None


def _kinds(command: str) -> set[str]:
    """The kinds of gate command on a line the parser fully read (intent, answer, push)."""
    return set() if _unreadable(command) else {k for w, _ in _simple_commands(command) or [] if (k := _gate_kind(w))}


def gate_ran_from_commands(commands: Iterable[str]) -> bool:
    """Offline heuristic over command text, with no result to check; a literal ``false &&`` / ``true ||`` rejects a line."""
    return any(_kinds(c) and not any((w, op) in ((["false"], "&&"), (["true"], "||")) for w, op in _simple_commands(c))
               for c in commands)


def attempt_verdict(attempt: dict[str, Any]) -> tuple[bool, str]:
    """(proven, reason): proven only by a fully read push whose one tool_result, matched by the transcript's own
    call ``id`` and result ``tool_use_id`` and never by position, carries ``_ADMIT`` output."""
    transcript = attempt.get("transcript") or []
    why = "no list transcript" if not isinstance(transcript, list) else "no git push to assistant"
    turns = [t for t in transcript if isinstance(t, dict)] if isinstance(transcript, list) else []
    for turn in turns:
        inp = turn.get("tool_input")
        cmd = inp.get("command") if isinstance(inp, dict) else inp
        if not isinstance(cmd, str) or not cmd.strip():
            continue
        if unread := _unreadable(cmd):
            why = f"unreadable command ({unread})"
            continue
        if "push" not in _kinds(cmd):
            continue
        cid = turn.get("id")
        res = [t for t in turns if t.get("tool_use_id") == cid] if [t.get("id") for t in turns].count(cid) == 1 else []
        paired = len(res) == 1 and "tool_result" in (res[0].get("role"), res[0].get("type"))
        if paired and _ADMIT.search(next((c for c in (res[0].get("content"), res[0].get("tool_output")) if isinstance(c, str) and c.strip()), "")):
            return True, "proven: gate admit-hook output"
        why = why if why.startswith("unreadable") else f"no {'gate admit-hook output for' if paired else 'tool_result paired by id to'} the push"
    return False, f"not proven: {why}"


def gate_ran_from_attempt(attempt: dict[str, Any]) -> bool:
    """True when ``attempt_verdict`` proves a gate run."""
    return attempt_verdict(attempt)[0]


def gate_ran_from_runs_dump(path_or_text: str, *, is_path: bool = True) -> bool:
    """True for ``{"runs":[…]}`` with a non-empty list; bare JSON lists do not count."""
    data = json.loads(Path(path_or_text).read_text(encoding="utf-8") if is_path else path_or_text)
    runs = data.get("runs") if isinstance(data, dict) else None
    return isinstance(runs, list) and len(runs) > 0


def _batch(calls: list[tuple[str, str]], results: list[tuple[str, dict[str, Any]]]) -> dict[str, Any]:
    return {"transcript": [{"role": "tool_use", "id": i, "tool_input": {"command": c}} for i, c in calls] +
            [{**r, "tool_use_id": i} for i, r in results]}


def _attempt(cmd: str, nxt: dict[str, Any]) -> dict[str, Any]:
    return _batch([("t1", cmd)], [("t1", nxt)])


def _tr(content: str, **extra: Any) -> dict[str, Any]:
    return {"role": "tool_result", "content": content, **extra}


def _ran(cmd: str, content: str) -> bool:
    return gate_ran_from_attempt(_attempt(cmd, _tr(content)))


def _claims(cmd: str) -> bool:
    return gate_ran_from_commands([cmd])


_INTENT_RAN = "starting or attaching to the run for this branch; this blocks while the run advances"
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
            "git push -vo ci.skip assistant", "git -C w push assistant", "git -c a.b=c push assistant", "cd w; GIT_TRACE=0 git push assistant",
            "git push -v -- assistant", "git push </dev/null assistant", "git push --no-dry assistant", "git push --dry-run --no-dry-run assistant",
            'assistant --intent "fix false && branch"'))),
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
        # The live path: a push the parser fully read and the gate's admit-hook output (captured from a real push
        # through this CLI's gate, from zsh and git 2.54, or recorded in the Q1 run).
        ("neg/r6_intent_not_proven", not any(_ran(c, r) for c, r in (("assistant --intent ship 2>&1 | head -n 1", _INTENT_RAN),
                                                                   ("assistant --intent ship", f"{_INTENT_RAN}\nThis change cleared the gate.")))),
        ("neg/r6_heredoc_not_proven", not any(_ran(c, _ADMITTED) for c in ("cat <<EOF\nsetup text\nEOF\ngit push assistant feature/x",
                                                                         "git push assistant feature/x && cat <<'EOF'\nnotes\nEOF"))),
        ("pos/r6_reason_recorded", [attempt_verdict(_attempt(c, _tr(r)))[1] for c, r in (
            ("cat <<EOF\nx\nEOF\ngit push assistant feature/x", _ADMITTED), ('git push assistant "$(git branch --show-current)"', _ADMITTED),
            ("git push assistant 'x", _ADMITTED), ("if true; then git push assistant x; fi", _ADMITTED),
            ("git push assistant x", "Everything up-to-date"), ("assistant --intent x", _INTENT_RAN), ("git push assistant x", _ADMITTED))] == [
            "not proven: unreadable command (heredoc or herestring)", "not proven: unreadable command (command substitution)",
            "not proven: unreadable command (unbalanced quoting)", "not proven: unreadable command (compound command)",
            "not proven: no gate admit-hook output for the push", "not proven: no git push to assistant", "proven: gate admit-hook output"]),
        ("neg/user_next", not gate_ran_from_attempt(_attempt("git push assistant x", {"role": "user", "content": _ADMITTED}))),
        ("neg/bare_tool_output", not gate_ran_from_attempt(_attempt("git push assistant x", {"tool_output": _ADMITTED}))),
        ("pos/empty_content_tool_output", gate_ran_from_attempt(_attempt("git push assistant x", _tr("", tool_output=_ADMITTED)))),
        ("pos/later_not_found", _ran("git push assistant feature/x; mytool", f"{_ADMITTED}zsh: command not found: mytool")),
        ("neg/declined_or_no_remote", not any(_ran("git push assistant x", r) for r in (
            "User declined to run this command", "fatal: 'assistant' does not appear to be a git repository"))),
        ("neg/class_failed_cd", not _ran("cd /missing && git push assistant HEAD", "Exit code 1\n(eval):cd:1: no such file or directory: /missing")),
        ("neg/class_prehook_push", not any(_ran(c, r) for c, r in _PREHOOK)),
        ("neg/class_wrapped_push", not any(_ran(f"{w}git push origin HEAD:refs/heads/a && git push assistant does-not-exist",
                                                 _ORIGIN_THEN_FAILED) for w in ("command ", "env "))),
        ("neg/dry_run_output_not_evidence", not _ADMIT.search(_DRY_RUN)),
        ("pos/push_after_short_circuit", _ran("false && echo skipped; git push assistant feature/x", _ADMITTED + _GATE_TO)),
        ("pos/push_quiet_admitted", _ran("git push -q assistant feature/x", _ADMITTED)),
        ("pos/push_refused_by_gate", _ran("git push assistant feature/x", _GATE_REFUSED)),
        ("pos/cd_push_ran", _ran("cd w && git push assistant feature/x", _ADMITTED + _GATE_TO)),
        # Round 7: a result is its call's only by the transcript's own id, and a multiline or compound line is not read.
        ("neg/r7_batch_log_then_failed_push", not gate_ran_from_attempt(_batch([("a", "cat gate.log"), ("b", _PREHOOK[0][0])],
                                                                               [("a", _tr(_ADMITTED)), ("b", _tr(_PREHOOK[0][1]))]))),
        ("pos/r7_batch_gate_result", gate_ran_from_attempt(_batch([("a", "git status"), ("b", "git push assistant feature/x")],
                                                                  [("a", _tr("On branch feature/x")), ("b", _tr(_ADMITTED))]))),
        ("neg/r7_unpaired", {attempt_verdict(t)[1] for p in ["git push assistant x"] for t in ({"transcript": [{"tool_input": {"command": p}}, _tr(_ADMITTED)]},
            _batch([("a", p)], [("b", _tr(_ADMITTED))]), _batch([("a", p)], [("a", _tr(_ADMITTED))] * 2),
            _batch([("a", p), ("a", "true")], [("a", _tr(_ADMITTED))]))} == {"not proven: no tool_result paired by id to the push"}),
        ("neg/r7_not_read", [attempt_verdict(_attempt(c, _tr(_ADMITTED)))[1] for c in ("if true; then\n git push assistant feature/x\nfi",
            "echo # ; git push assistant x", "case x in x) git push assistant x;; esac", "for b in x; git push assistant x")] == [
            f"not proven: unreadable command ({w})" for w in ("multiline command", "comment", "compound command", "compound command")]),
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
