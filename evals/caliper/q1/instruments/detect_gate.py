#!/usr/bin/env python3
"""Read whether a Q1 command line is an assistant gate command, and whether a runs dump lists a run.

Caliper's ``activated`` field only says the skill loaded. ``gate_ran_from_commands`` is the offline
command-text heuristic: it sees no result, so it says a line claims a gate command, never that the gate ran.
Live scoring of an attempt's results is a later link (``feat/q1-895-live``).
"""
from __future__ import annotations

import json
import os
import re
import shlex
import sys
from pathlib import Path
from typing import Iterable

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
# Not read, so never a gate command: a line with one of these marks (a newline, continuations included), or with a command that
# begins with a reserved word (bash's, plus zsh's foreach end repeat nocorrect), so no compound body reads as a command.
_UNREADABLE = (("<<", "heredoc or herestring"), ("`", "command substitution"), ("$(", "command substitution"), ("#", "comment"),
               ("\n", "multiline command"))
_STAND_IN = str.maketrans(";&|()<>", "\ue000\ue001\ue002\ue003\ue004\ue005\ue006")
_RESERVED = set("! [[ ]] { } case coproc do done elif else end esac fi for foreach function if in nocorrect repeat select then time until while".split())

def _shield(command: str) -> str:
    """The line with each quoted or backslash-escaped ``;&|()<>`` swapped for a stand-in that shlex reads as a word
    character, so only a bare one is an operator or a redirection."""
    out, quote, i = [], "", 0
    while i < len(command):
        c = command[i]
        if c == "\\" and quote != "'":
            out.append(command[i:i + 2].translate(_STAND_IN))
            i += 2
            continue
        quote = ("" if quote == c else quote) if quote else (c if c in "'\"" else "")
        out.append(c.translate(_STAND_IN) if quote else c)
        i += 1
    return "".join(out)


def _simple_commands(command: str) -> list[tuple[list[str], str]] | None:
    """Each simple command's words and the operator after it ("" at the end), as shlex (posix)
    splits the shielded line; redirections are dropped. None when shlex cannot split it."""
    lex = shlex.shlex(_shield(command), posix=True, punctuation_chars=";&|()<>\n")
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
                if c == "o":  # its value is the rest of the word, else the next word; git refuses a missing one
                    if j == len(a) - 1 and i >= len(args):
                        return None
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


def gate_ran_from_runs_dump(path_or_text: str, *, is_path: bool = True) -> bool:
    """True for ``{"runs":[…]}`` with a non-empty list; bare JSON lists do not count."""
    data = json.loads(Path(path_or_text).read_text(encoding="utf-8") if is_path else path_or_text)
    runs = data.get("runs") if isinstance(data, dict) else None
    return isinstance(runs, list) and len(runs) > 0


def _claims(cmd: str) -> bool:
    return gate_ran_from_commands([cmd])


_NOT_READ = ("cat <<'EOF'\nx\nEOF\ngit push assistant", 'git push assistant "$(git branch --show-current)"', "git push assistant `echo x`",
             "echo # ; git push assistant x", "if true; then\n git push assistant x\nfi", "git push assistant 'x",
             "case x in x) git push assistant x;; esac", "for b in x; git push assistant x")


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
        ("neg/class_missing_value", not any(_claims(f"git push {f}") for f in ("assistant -o", "assistant -vo", "-o", "assistant --push-option"))
            and all(_claims(f"git push {f}") for f in ("assistant -ofoo", "assistant -o foo", "assistant -vo --", "-o x assistant"))),
        ("neg/class_quoted_operator", not any(_claims(c) for c in ("echo ';' assistant --intent ship", "echo '|' git push assistant",
            "echo '(' git push assistant", 'echo "&&" git push assistant', "echo \\; git push assistant")) and _claims('echo "a\\";" ; git push assistant')),
        # A line the reader does not fully read is never a gate command, whatever it holds.
        ("neg/not_read", [_unreadable(c) for c in _NOT_READ] == ["heredoc or herestring", "command substitution", "command substitution", "comment",
            "multiline command", "unbalanced quoting", "compound command", "compound command"] and not any(_claims(c) for c in _NOT_READ)),
        ("neg/literal_short_circuit", not any(_claims(f"cd w; {c} git push assistant") for c in ("false &&", "true ||"))),
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
