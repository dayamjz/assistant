# Q1 instruments (plan row 8 / issue #895)

Shared measurement helpers beside the agentic Q1 harness.

This PR lands the part of `detect_gate.py` that needs no tool results: a
reader that says whether a command line is an assistant gate command, the
offline heuristic built on it, and a check of a runs dump. Caliper's
`activated` field only says the skill loaded (#398); these helpers look at
what the attempt typed instead.

- `gate_ran_from_commands(commands)`: true when some line is a gate command
  the reader fully read (a `git push` to `assistant` that is not a dry run, or
  `assistant --intent` / `--answer`). It sees no result, so it says an attempt
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
esac` is never read as a command of its own.

Apart from those marks, nothing is matched against the raw text: the line is
read the way a POSIX shell splits it, then each command's words are read the
way the program parses them.

- Shell: `shlex` (POSIX mode) splits words, removing quotes and escapes
  (`"--dry-run"`, `\--dry-run`, `--"dry"-run` are all `--dry-run`), and `;`,
  `&&`, `||`, `|`, `&` and parentheses separate commands. Redirections and
  their targets are dropped. Only a bare operator character splits: a `;`,
  `&`, `|`, `(`, `)`, `<` or `>` inside quotes or after a backslash is literal
  text, so `echo ';' assistant --intent ship` and `echo \; git push assistant`
  are one `echo` command, not a gate command.
- A command's program is the first word after any `VAR=value` assignments;
  `assistant` or a path ending in `/assistant` is the CLI, whose `--intent` /
  `--answer` (also `-intent`, `--intent=...`) make it a gate command.
- `git`: its own options before the subcommand are skipped (`-C <path>` and
  `-c <name>=<value>` take the next word). After `push`, arguments are walked
  with `git push -h`'s option table from git 2.54: an exact long name or the
  one name a prefix abbreviates (`--dr` is `--dry-run`; `--d` is ambiguous with
  `--delete` and git refuses it, so that push is not a gate command), options
  after positionals, `--` ending options, short clusters (`-vn`), and
  value-taking options (`-o`, `--push-option`, `--repo`, `--receive-pack`,
  `--exec`, `--recurse-submodules`) consuming the next word whatever it looks
  like, so `-o --no-dry-run` is a push option, not a negation. `-o` is the
  only short option with a value: the rest of its word (`-ofoo`), else the
  next word; with neither (`git push assistant -o`, `-vo` last) git exits 129
  "requires a value", so that push is not a gate command, and the same holds
  for a long value-taking option with nothing after it. `-n` /
  `--dry-run` and `--no-dry-run` set the dry run, the last one winning. The
  first positional (or `--repo` when there is none) is the remote, which must
  be exactly `assistant`; a dry run is not a gate command.

Residual misses: a wrapper before the program (`env`, `command`, `exec`,
`sudo`, `sh -c '...'`) or a git alias, which the reader does not see as a
push, and a file-descriptor redirection written before the remote (`2>log`,
which splits as a word `2`).

## Selftest

`python3 detect_gate.py` runs the fail-before selftest: what is a gate
command (flags, flag spelling, continuation, escape, option value, missing
option value, cluster, quoting, quoted or escaped operator characters,
remote-name prefix, text that only names the gate), each not-read
mark with the reason the reader gives, the literal short-circuit rule, and
the runs dump. Reverting any one guard fails a row; in the reserved-word
list, an opener such as `case` is also caught by its closer (`esac`). Fuller offline controls,
live scoring, and the activation-smoke / fixed-diff runners stack in
follow-up PRs of 300 code lines or under each.
