# Q1 instruments (plan row 8 / issue #895)

Shared measurement helpers beside the agentic Q1 harness.

This PR lands `detect_gate.py`: whether a Caliper attempt actually invoked
the assistant gate (`assistant --intent` / `--answer`, or `git push
assistant`), not merely whether the skill activated (#398).

Live scoring is `gate_ran_from_attempt`. A command counts when it parses as a
gate command (below) and its paired `tool_result` carries the gate's own
output, so the evidence is tied to the gate rather than to the command's shape:

- `--intent` / `--answer`: a progress line `internal/cli/runs.go` prints
  (`starting or attaching ...`, `answering ...`), with no
  `the background service is not running` failure
  (`internal/cli/client.go`). The CLI prints the progress line before it
  dials the service, so the line alone shows only that it tried.
- A push: a line git relays from the gate's admit hook
  (`internal/cli/gate.go`, `assistant gate admit`): `remote: The gate let this
  change in.` (`machine.PhraseAdmitted`), `machine.PhraseSenderUnplaced`, or
  `remote: gate.admit: ...`, the service refusing the push under
  `ipc.MethodGateAdmit`. A refusal counts: the gate ran and said no. Captured
  from a real push through this CLI's gate, these lines appear with and
  without `-q`. No other remote, dry run, push the shell skipped, or push that
  failed before the hook prints them. A dry run does print the receive
  command's copy-busy sentence, but without the `remote:` prefix, and that
  line is not evidence.

So a command the shell skipped (a failed `cd`, `assistant --cancel && ...`,
`false && ...`), a missing `assistant` binary in any shell's wording, a
declined tool call, a push that failed locally (bad refspec, non-fast-forward,
`Everything up-to-date`, no such remote) and another push's report on the same
line all score false, because none carries that output; and an unrelated
short-circuit earlier on the line does not hide a gate command that ran.

How a command line is read. Nothing is matched against the raw text: the
line is read the way a POSIX shell splits it, then each command's words are
read the way the program parses them.

- Shell: backslash-newline continuations are joined; a `#` that starts an
  unquoted word begins a comment (a `#` inside a word does not); a heredoc's
  body is data, not commands; then `shlex` (POSIX mode) splits words, removing
  quotes and escapes (`"--dry-run"`, `\--dry-run`, `--"dry"-run` are all
  `--dry-run`), and `;`, `&&`, `||`, `|`, `&`, parentheses and newlines
  separate commands. Redirections and their targets are dropped. A line
  `shlex` cannot split (an unclosed quote) has no gate command.
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
  like, so `-o --no-dry-run` is a push option, not a negation. `-n` /
  `--dry-run` and `--no-dry-run` set the dry run, the last one winning. The
  first positional (or `--repo` when there is none) is the remote, which must
  be exactly `assistant`; a dry run is not a gate command.

Residual false negatives (real gate runs that still score false): a wrapper
before the program (`env`, `command`, `exec`, `sudo`, `sh -c '...'`) or a git
alias, which the parser does not read as a gate command; output redirected
away or filtered through a pipe that drops the evidence; a file-descriptor
redirection written before the remote (`2>log`, which splits as a word `2`);
a herestring (`<<<`), whose following lines are cut like a heredoc body; a
refusal the service words without `gate.admit:`; and a run started in the
background, whose output goes to a file. The progress lines and gate phrases
are matched as `internal/cli` and `internal/machine` word them today;
rewording them makes those commands score false, and nothing here detects
that drift.

Residual false positive: earlier gate output replayed by another command on
the same line (for example `cat` of a saved push log beside a push to
`assistant` that fails) is read as this push's evidence.

`gate_ran_from_commands` is only the offline command-text heuristic (verify /
selftest); it sees no result, so it counts `cd missing && assistant --intent`,
and it rejects a line with a literal `false &&` / `true ||` or any backtick or
`$(...)`. The live path does not apply that rejection.

`python3 detect_gate.py` runs the fail-before selftest. Parser rows go
through `gate_ran_from_commands` (what is a gate command: flags, flag
spelling, continuation, escape, option value, cluster, quoting, remote-name
prefix). Live rows go through `gate_ran_from_attempt` with result text
recorded in the Q1 run or captured from zsh, git 2.54, and a real push through
this repository's CLI and gate (short-circuit, failed `cd`, not found,
up-to-date, a push that failed before the hook, no service, a wrapped push
beside another remote's report, a dry run's output). Fuller offline controls
and the activation-smoke / fixed-diff runners stack in follow-up PRs of 300
code lines or under each.
