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

The reader fails closed on shell syntax, with no grammar parser: it reads only
lines made of characters it knows, and every other line is not read, so never
a gate command, with the reason `_unreadable` gives.

- First, a line holding any private-use character (Unicode category `Co`:
  U+E000-U+F8FF and the two supplementary private-use planes), quoted or not,
  is not read. The reader marks quoting internally with one such character,
  so raw input that already held one could otherwise be rewritten (a remote
  `'assi<U+E007>stant'` read as `assistant`); refusing the whole category means
  no marker the reader uses can collide with input.

- Outside quotes, only ASCII letters and digits, space, tab, `- _ . / = : @ ,
  + %`, the operator characters `; & |`, and quotes are on the allowlist.
  Anything else is not read: `$` (any expansion: `${...}`, `$x`, `$'...'`;
  `${unset:-x; git push assistant; y}` only prints text), backtick and `$(`
  (command substitution), `\` (escape, so a continuation too), `(` `)`
  (subshell, grouping, function definition, process substitution), `{` `}`
  (brace group or expansion), `<` `>` (redirection, heredoc), `*` `?` `[` `]`
  (glob; bash and zsh treat an unmatched one differently), `~` (tilde), `!`
  (negation or history), `#` (comment), a newline, and any other character.
- Inside single quotes every character is literal and read
  (`git commit -m '#1 $(x)'` is plain text). Inside double quotes everything
  is read except `$`, backtick and `\`, which expand or escape there. An
  unclosed quote is not read.
- Of the operator characters, only `;`, `&&`, `||`, `|` and `&` are read, each
  with a command on each side (a trailing `;` or `&` is read): `;;`, `&&&`,
  `|&`, `true && && x`, a leading `;` and a trailing `&&` or `|` are not. bash
  refuses each of these; zsh accepts a leading `;` and a trailing `&&`, which
  are not read anyway.
- A command that begins with a reserved word (bash's made of letters: `case
  coproc do done elif else esac fi for function if in select then time until
  while`, plus zsh's `foreach end repeat nocorrect`) is not read, so the body
  of a compound command (`for b in x; git push assistant`) is never read as a
  command of its own. bash's other reserved words, `! { } [[ ]]`, are off the
  character allowlist and refused before this.
- A word in command position shaped `NAME=value` but quoted up to its `=`
  (`"X=y" git push assistant`, `'X'=y`) is not read: the shell runs a command
  named `X=y`, which is not found.

Of a line that passes, nothing more is matched against the raw text: the line
is split the way a POSIX shell splits it, then each command's words are read
the way the program parses them.

- Shell: `shlex` (POSIX mode) splits words, removing quotes
  (`"--dry-run"` and `--"dry"-run` are both `--dry-run`), and the operators
  separate commands. A quoted operator character is literal text, so
  `echo ';' assistant --intent ship` is one `echo` command. An empty quoted
  word (`''`) is a word, never a separator.
- A command's program is the first word after any `VAR=value` assignments
  written with no quote up to the `=`.
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
  `u`, `v`, `q`, `f` (`-uvf`; one letter alone is the same rule). Each was seen to pass git 2.54's option
  parsing. Not accepted, so never a gate command: a dry run (`-n`,
  `--dry-run`), `-o` / `--push-option`, `--repo`, any other value
  (`--signed=banana`, which git refuses, and `--signed=yes`, which it would
  accept), an abbreviation (`--forc`), and `--`. The first word that is not
  an option is the remote, which must be exactly `assistant`.

Residual misses: a wrapper before the program (`env`, `command`, `exec`,
`sudo`, `sh -c '...'`) or a git alias, which the reader does not see as a
push, and every valid line the allowlist does not read (a redirection such
as `2>&1`, a variable such as `"$BRANCH"`, a subshell, a glob in a refspec):
failing closed under-counts by design.

## Selftest

`python3 detect_gate.py` runs the fail-before selftest: what is a gate
command (each listed option, dry-run flags, flag spelling, continuation,
escape, option value, missing value, cluster, quoting, quoted or escaped
operator characters, an empty quoted word, remote-name prefix, text that
only names the gate, a refused or unlisted value, an unknown option, and
options that end the run early such as `--version` and `--help`), each
not-read class with the reason the reader gives (private-use characters,
expansion, command substitution, escape, redirection, glob, tilde, brace,
`!`, comment, other characters, parentheses, unknown operators, operators
without a command on each side, quoted assignments, reserved words), a
positive control that each
allowlisted character (every letter, digit and punctuation mark) is read,
each `assistant` flag form and the gate command kind it reads as, each
operator, each reserved word, the literal short-circuit rule, and the runs
dump. Removing any one entry of the reader's tables (`_PLAIN`, `_OPERATORS`,
`_PUSH_OPTIONS`, the `_PUSH_CLUSTER` letters, `_RESERVED`,
`_ASSISTANT_FLAGS`, `_CHAR_REASONS`, `_MARK_REASONS`) fails a row, as does
each guard reverted one at a time in a 60-mutation run; the rows list the
entries literally rather than reading the tables. Fuller offline controls, live scoring, and the
activation-smoke / fixed-diff runners stack in follow-up PRs of 300 code
lines or under each.
