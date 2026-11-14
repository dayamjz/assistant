# Q1 instruments (plan row 8 / issue #895)

Shared measurement helpers beside the agentic Q1 harness.

This PR lands the part of `detect_gate.py` that needs no tool results: a
reader that says whether a command line is an assistant gate command, the
offline heuristic built on it, and a check of a runs dump. Caliper's
`activated` field only says the skill loaded (#398); these helpers look at
what the attempt typed instead.

- `gate_ran_from_commands(commands)`: true when some line is a gate command
  the reader fully read and whose every option is on its allowlist (a `git
  push` to `assistant`, or `assistant --intent` / `--answer` with a value). It sees no result, so it says an attempt
  claimed a gate command, never that the gate ran: `cd missing && assistant
  --intent` still counts. A line with a literal `false &&` or `true ||` is
  rejected.
- `gate_ran_from_runs_dump(path_or_text)`: true for `{"runs": [...]}` with a
  non-empty list; a bare JSON list does not count.

Live scoring, deciding from an attempt's tool results that the gate actually
ran, is a later link and not in this PR. Its code is kept on the branch
`feat/q1-895-live`. It needs each tool result paired with its call by the
transcript's own call and result ids, and Caliper 0.19.0 records neither: its
`TranscriptTurn` (`caliper/schema/results.py`) lists only `role`, `content`,
`tool_name`, `tool_input` and `tool_output`, and the four recorded results here
carry no `id` or `tool_use_id`. That link waits for a way to record those ids
in Caliper attempts, which is a decision for the owner.

## How a command line is read

Not read, so never a gate command: a line holding a heredoc or herestring
(`<<`), a command substitution (backtick or `$(`), a `#` anywhere, a newline
anywhere (a backslash-newline continuation or a trailing newline too), quoting
`shlex` cannot split, or a command that begins with a reserved word (bash's
list: `! [[ ]] { } case coproc do done elif else esac fi for function if in
select then time until while`, plus zsh's `foreach end repeat nocorrect`), so
the body of a compound command such as `case x in x) git push assistant x;;
esac` is never read as a command of its own. Also not read, with no grammar
parser behind it:

- any `(` or `)` outside quotes: a subshell or grouping (`(git push
  assistant)`, which runs, is not read either), an unbalanced one (`(git push
  assistant`, a syntax error), a function definition (`f() (git push
  assistant)` only defines `f`), or a process substitution (`<(...)`);
- an operator or redirection token other than `;`, `&&`, `||`, `|`, `&` and
  `<`, `>`, `>>`, `<>`, `>|`, `>&`, `<&`, `&>`, `&>>` (`git push assistant ;;
  true`, `>;`, bash's `|&`);
- an operator without a command on each side (`true && && git push
  assistant`, a leading `;`, a trailing `&&` or `|`); a trailing `;` or `&` is
  read. bash refuses each of these; zsh accepts a leading `;` and a trailing
  `&&`, which are not read anyway;
- a word in command position shaped `NAME=value` but quoted or escaped up to
  its `=` (`"X=y" git push assistant`, `X\=y`, `'X'=y`: the shell runs a
  command named `X=y`, which is not found);
- a redirection with no target word (`git push assistant >`, a parse error),
  including one written before an operator or another redirection.

Apart from those marks, nothing is matched against the raw text: the line is
read the way a POSIX shell splits it, then each command's words are read the
way the program parses them.

- Shell: `shlex` (POSIX mode) splits words, removing quotes and escapes
  (`"--dry-run"`, `\--dry-run`, `--"dry"-run` are all `--dry-run`), and `;`,
  `&&`, `||`, `|` and `&` separate commands. Redirections and their targets
  are dropped (so a command that is only a redirection, such as `> out;`,
  leaves its operator without a command and the line is not read). Only a
  bare operator character counts: a `;`, `&`, `|`, `(`, `)`, `<` or `>` inside
  quotes or after a backslash is literal text, so `echo ';' assistant --intent ship` and `echo \; git push assistant`
  are one `echo` command, not a gate command. An empty quoted word (`''`) is
  a word, never a separator.
- A command's program is the first word after any `VAR=value` assignments
  written with no quote or escape up to the `=`.
  The reader fails closed: any option word not listed below, including one
  that ends the run before a gate command (`--version`, `--help`, `-h`), or
  any value not listed, makes the command no gate command. It is never
  parsed further.
- `assistant` (or a path ending in `/assistant`): exactly one of `--intent`,
  `-intent`, `--answer`, `-answer`, with a non-empty value as the next word
  or after `=`, and no other word. Checked against this repository's CLI:
  those forms get past its flag parsing, `--version` or `--help` first only
  prints, and a missing value or an extra word is refused. Valid flags the
  reader does not list (`--json`, `--home PATH`) make the command no gate
  command, so they under-count.
- `git`: the only option before the subcommand is `-C <path>` (a missing
  path fails when git runs, which this offline heuristic cannot see, like a
  failed `cd`). After `push`, each option word must be one of `-u`,
  `--set-upstream`, `-v`, `--verbose`, `-q`, `--quiet`, `-f`, `--force`,
  `--force-with-lease`, `--no-verify`, `--follow-tags`, `--tags`,
  `--porcelain`, `--signed=no`, `--recurse-submodules=no`, or a cluster of
  `u`, `v`, `q`, `f` (`-uvf`). Each was seen to pass git 2.54's option
  parsing. Not accepted, so never a gate command: a dry run (`-n`,
  `--dry-run`), `-o` / `--push-option`, `--repo`, any other value
  (`--signed=banana`, which git refuses, and `--signed=yes`, which it would
  accept), an abbreviation (`--forc`), and `--`. The first word that is not
  an option is the remote, which must be exactly `assistant`.

Residual misses: a wrapper before the program (`env`, `command`, `exec`,
`sudo`, `sh -c '...'`) or a git alias, which the reader does not see as a
push, and a file-descriptor redirection written before the remote (`2>log`,
which splits as a word `2`).

## Selftest

`python3 detect_gate.py` runs the fail-before selftest: what is a gate
command (each listed option, dry-run flags, flag spelling, continuation,
escape, option value, missing value, cluster, quoting, quoted or escaped
operator characters, an empty quoted word, remote-name prefix, text that
only names the gate, a refused or unlisted value, an unknown option, and
options that end the run early such as `--version` and `--help`), each
not-read shape with the reason the reader gives (parentheses, unknown
operators, operators without a command on each side, function definitions,
quoted assignments and target-less redirections among them), the literal
short-circuit rule, and the runs dump. Reverting any one guard fails a row;
in the reserved-word list, an opener such as `case` is also caught by its
closer (`esac`). Fuller offline controls, live scoring, and the
activation-smoke / fixed-diff runners stack in follow-up PRs of 300 code
lines or under each.
