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

# Fail closed on shell grammar: a token of these characters must be one of the operators or redirections listed;
# any parenthesis (subshell, grouping, function definition), any other token, and an operator without a command
# on each side (a trailing ; or & aside) make the line not read.
_PUNCT = set(";&|()<>\n")
_OPERATORS = {";", "&&", "||", "|", "&"}
_REDIRECTS = {"<", ">", ">>", "<>", ">|", ">&", "<&", "&>", "&>>"}
# Fail closed: an option word not listed here, including one that ends the run early (--version, --help, -h), makes
# the command no gate command. Each git push word below was seen to pass git 2.54's option parsing; a value-taking
# option counts only as the exact --name=value word listed. Not listed, so never read: -o, --repo, -n / --dry-run,
# abbreviations, `--`, and every git option before the subcommand except -C <path>.
_PUSH_OPTIONS = set("-u --set-upstream -v --verbose -q --quiet -f --force --force-with-lease --no-verify --follow-tags --tags "
                    "--porcelain --signed=no --recurse-submodules=no".split())
_PUSH_CLUSTER = re.compile(r"-[uvqf]+")
# assistant (internal/cli): exactly one of these flags and a non-empty value, as the next word or after `=`.
_ASSISTANT_FLAGS = {"--intent": "intent", "-intent": "intent", "--answer": "answer", "-answer": "answer"}
# Not read, so never a gate command: a line with one of these marks (a newline, continuations included), or with a command that
# begins with a reserved word (bash's, plus zsh's foreach end repeat nocorrect), so no compound body reads as a command.
_UNREADABLE = (("<<", "heredoc or herestring"), ("`", "command substitution"), ("$(", "command substitution"), ("#", "comment"),
               ("\n", "multiline command"))
_QUOTED = "\ue007"  # where quoting or an escape starts, so an assignment is told from a quoted look-alike
_ASSIGNMENT = re.compile(r"[A-Za-z_]\w*=.*", re.S)
_RESERVED = set("! [[ ]] { } case coproc do done elif else end esac fi for foreach function if in nocorrect repeat select then time until while".split())

def _shield(command: str) -> str:
    """The line with ``_QUOTED`` before each opening quote and each backslash outside quotes. shlex reads it as a
    word character, so a word with any quoting or escape is never empty and never reads as an operator or a
    redirection (``';'``, ``\\;``), and an assignment is told from a quoted look-alike."""
    out, quote, i = [], "", 0
    while i < len(command):
        c = command[i]
        if c == "\\" and quote != "'":
            out.append(("" if quote else _QUOTED) + command[i:i + 2])
            i += 2
            continue
        out.append(_QUOTED + c if not quote and c in "'\"" else c)
        quote = ("" if quote == c else quote) if quote else (c if c in "'\"" else "")
        i += 1
    return "".join(out)


def _simple_commands(command: str, marks: bool = False) -> list[tuple[list[str], str]]:
    """Each simple command's words (keeping ``_QUOTED`` only with ``marks``) and the operator after it ("" at the
    end), as shlex (posix) splits the shielded line; redirections and their targets are dropped. Raises ValueError
    with the reason when shlex cannot split the line or its operators and redirections break the rules at
    ``_PUNCT``, or a redirection has no target word."""
    lex = shlex.shlex(_shield(command), posix=True, punctuation_chars=";&|()<>\n")
    lex.whitespace, lex.whitespace_split, lex.commenters = " \t\r", True, ""
    try:
        words = list(lex)
    except ValueError:
        raise ValueError("unbalanced quoting") from None
    out, cur, skip = [], [], False
    for w in words:
        if set(w) <= _PUNCT:  # an operator or redirection: a word with any quoting or escape carries _QUOTED
            if set(w) & set("()"):
                raise ValueError("parentheses")
            if w not in _OPERATORS | _REDIRECTS:
                raise ValueError("unknown operator")
            if skip:
                raise ValueError("redirection without a target")
            if w in _REDIRECTS:
                skip = True
            elif not cur:
                raise ValueError("operator without a command on each side")
            else:
                out.append((cur, w))
                cur = []
        elif skip:
            skip = False
        else:
            cur.append(w if marks else w.replace(_QUOTED, ""))
    if skip:
        raise ValueError("redirection without a target")
    if out and not cur and out[-1][1] not in (";", "&"):
        raise ValueError("operator without a command on each side")
    return [(c, op) for c, op in out + [(cur, "")] if c]


def _program(words: list[str]) -> list[str]:
    """The words from the program on, past leading ``VAR=value`` assignments."""
    while words and _ASSIGNMENT.fullmatch(words[0]):
        words = words[1:]
    return words


def _push_remote(args: list[str]) -> str | None:
    """The remote of a git push whose every option word is on the allowlist, else None."""
    if any(a.startswith("-") and a not in _PUSH_OPTIONS and not _PUSH_CLUSTER.fullmatch(a) for a in args):
        return None
    return next((a for a in args if not a.startswith("-")), None)


def _gate_kind(words: list[str]) -> str | None:
    """intent, answer or push when the simple command is a gate command on the allowlists, else None; a push is
    one only to the remote named exactly `assistant`."""
    words = _program(words)
    prog = os.path.basename(words[0]) if words else ""
    if prog == "assistant":
        args = words[1].split("=", 1) if len(words) == 2 else words[1:]
        return _ASSISTANT_FLAGS.get(args[0]) if len(args) == 2 and args[1] else None
    rest = words[3:] if words[1:2] == ["-C"] else words[1:]
    return "push" if prog == "git" and rest[:1] == ["push"] and _push_remote(rest[1:]) == "assistant" else None


def _unreadable(command: str) -> str | None:
    """Why the parser cannot fully read the line, or None when it can."""
    if hit := next((why for mark, why in _UNREADABLE if mark in command), None):
        return hit
    try:
        cmds = _simple_commands(command, marks=True)
    except ValueError as refused:
        return str(refused)
    if any(_quoted_assignment(w) for w, _ in cmds):
        return "quoted assignment"
    return "compound command" if any(w[0] in _RESERVED for w, _ in cmds) else None


def _quoted_assignment(words: list[str]) -> bool:
    """True when a word in command position looks like ``NAME=value`` but is quoted or escaped up to its ``=``, so
    the shell runs it as a command name (not found) instead of assigning."""
    for w in words:
        if not _ASSIGNMENT.fullmatch(w.replace(_QUOTED, "")):
            return False
        if _QUOTED in w.split("=", 1)[0]:
            return True
    return False


def _kinds(command: str) -> set[str]:
    """The kinds of gate command on a line the parser fully read (intent, answer, push)."""
    return set() if _unreadable(command) else {k for w, _ in _simple_commands(command) if (k := _gate_kind(w))}


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
            "git -C w push assistant", "cd w; GIT_TRACE=0 git push assistant", "git push </dev/null assistant", 'assistant --intent "fix false && branch"',
            "assistant --intent=ship", "assistant -intent ship", "/usr/local/bin/assistant --answer yes", "git push -uvf assistant HEAD"))),
        ("pos/listed_options", all(_claims(f"git push {o} assistant feature/x") for o in "-u --set-upstream -v --verbose -q --quiet -f --force "
            "--force-with-lease --no-verify --follow-tags --tags --porcelain --signed=no --recurse-submodules=no".split())),
        ("neg/text_naming_gate", not any(_claims(c) for c in ('grep -rn "git push assistant" skills', 'echo "next: ; assistant --intent ship"',
                                                             "cat <<'EOF'\ngit push assistant HEAD\nEOF"))),
        ("neg/class_flags", not any(_claims(f"git push {f}") for f in ("--dry-run assistant", "-n assistant", "-fn assistant",
                                                                       "assistant --dry-run", "assistant -n", "assistant -fn"))),
        ("neg/class_flag_spelling", not any(_claims(f"git push assistant {f}") for f in ("--dr", "--dry", "--dry-ru", "--no-dry-run --dry"))),
        ("neg/class_continuation", not _claims("git push assistant \\\n  --dry-run HEAD")),
        ("neg/class_escape", not _claims("git push assistant \\--dry-run HEAD")),
        ("neg/class_option_value", not any(_claims(f"git push assistant {f}") for f in (
            "--dry-run -o --no-dry-run", "-n --push-option --no-dry", "-n --push-o=--no-dry-run", "-n -o--no-dry-run"))),
        ("neg/class_cluster", not any(_claims(f"git push {f} assistant") for f in ("-vn", "-fnu", "-un"))),
        ("neg/class_quoting", not any(_claims(f"git push {f}") for f in ("assistant --\"dry\"-run", "assistant '-n'", "'assistant' --dry-run",
                                                                         "assistant HEAD#x --dry-run", 'assistant "--dry-run"'))),
        ("neg/class_remote_prefix", not _claims("git push assistant-backup HEAD")),
        ("neg/class_missing_value", not any(_claims(f"git push {f}") for f in ("assistant -o", "assistant -vo", "-o", "assistant --push-option"))
            and not any(_claims(f"assistant {f}") for f in ("--intent", "--intent=", "--intent ''", "--answer"))),
        ("neg/class_quoted_operator", not any(_claims(c) for c in ("echo ';' assistant --intent ship", "echo '|' git push assistant",
            "echo '(' git push assistant", 'echo "&&" git push assistant', "echo \\; git push assistant")) and _claims('echo "a\\";" ; git push assistant')),
        # Allowlist round: an option or value not listed, or one that ends the run early, is never a gate command.
        ("neg/class_bad_value", not any(_claims(f"git push assistant {o}") for o in ("--signed=banana", "--recurse-submodules=banana"))),
        ("neg/class_unlisted_value", not any(_claims(f"git push assistant {o}") for o in ("--signed=yes", "--recurse-submodules=check"))),
        ("neg/class_empty_word", not any(_claims(c) for c in ("echo '' git push assistant", 'echo "" assistant --intent ship'))),
        ("neg/class_ends_early", not any(_claims(c) for c in ("assistant --version --intent ship", "git --version push assistant",
                                                             "git push --version assistant"))),
        ("neg/class_unknown_option", not any(_claims(c) for c in ("assistant --frobnicate --intent ship", "git --frobnicate push assistant"))),
        ("neg/class_help", not any(_claims(c) for c in ("assistant --help --intent ship", "assistant -h --intent ship", "git --help push assistant",
                                                       "git push assistant --help", "git push -h assistant"))),
        # Round 4: a function definition, a quoted or escaped assignment, or a redirection with no target is not read.
        ("neg/class_function_def", [_unreadable(c) for c in ("f() (git push assistant)", "f ( ) { git push assistant; }", "function f { git push assistant; }")]
            == ["parentheses", "parentheses", "compound command"] and _claims('git commit -m "f()" && git push assistant')),
        ("neg/class_quoted_assignment", [_unreadable(c) for c in ('"X=y" git push assistant', "X\\=y git push assistant", "'X'=y git push assistant",
            'A=1 "X=y" git push assistant')] == ["quoted assignment"] * 4 and _claims('X="a b" git push assistant')),
        ("neg/class_redirect_no_target", [_unreadable(c) for c in ("git push assistant >", "git push assistant 2>", "git push assistant > ; true",
            "git push assistant > > x")] == ["redirection without a target"] * 4 and _claims("git push assistant > out")),
        # Round 5: any bare parenthesis or brace group, and an operator without a command on each side, is not read.
        ("neg/class_parentheses", [_unreadable(c) for c in ("(git push assistant", "git push assistant)", "(git push assistant)",
            "true && (git push assistant)", "{ git push assistant; }")] == ["parentheses"] * 4 + ["compound command"]),
        ("neg/class_operator_sequence", [_unreadable(c) for c in ("git push assistant ;; true", "true &&& git push assistant", "true && && git push assistant",
            "; git push assistant", "git push assistant &&", "git push assistant |", "git push assistant >; true", "cat <(git push assistant")] ==
            ["unknown operator"] * 2 + ["operator without a command on each side"] * 4 + ["unknown operator", "parentheses"]
            and all(_claims(f"git push assistant {op}") for op in (";", "&", "2>&1 | cat", "&>/dev/null", ">> log &"))),
        # A line the reader does not fully read is never a gate command, whatever it holds.
        ("neg/not_read", [_unreadable(c) for c in _NOT_READ] == ["heredoc or herestring", "command substitution", "command substitution", "comment",
            "multiline command", "unbalanced quoting", "parentheses", "compound command"] and not any(_claims(c) for c in _NOT_READ)),
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
