# Q1 instruments (plan row 8 / issue #895)

Shared measurement helpers beside the agentic Q1 harness.

This PR lands `detect_gate.py`: whether a Caliper attempt provably ran the
assistant gate, not merely whether the skill activated (#398).

Live scoring is `attempt_verdict` (proven or not, with the reason) and
`gate_ran_from_attempt` (its boolean). By owner decision (round 6), only the
gate's own admit-hook output proves a gate run: a line git relays from
`assistant gate admit` (`internal/cli/gate.go`), namely `remote: The gate let
this change in.` (`machine.PhraseAdmitted`), `machine.PhraseSenderUnplaced`,
or `remote: gate.admit: ...` (the service refusing the push under
`ipc.MethodGateAdmit`; a refusal counts, since the gate ran and said no). It
must be paired with a `git push` to `assistant` that the parser fully read.
These lines were captured from a real push through this CLI's gate, with and
without `-q`. A dry run prints only the receive command's copy-busy sentence,
without the `remote:` prefix, and that is not proof.

Not proven, with the reason `attempt_verdict` gives:

- `assistant --intent` / `--answer`: the CLI's progress line is printed before
  it dials the service, so it is not proof (`assistant --intent ship 2>&1 |
  head -n 1` with no service shows only that line). An attempt that used the
  gate only through `--intent` therefore scores not proven. The recorded Q1
  runs used only `--intent`, so they score 0 of 35 here (the earlier,
  progress-line detector scored 11).
- A line the parser does not fully read: a heredoc or herestring (`<<`), a
  command substitution (backtick or `$(`), or quoting `shlex` cannot split. Such
  a line is not proven rather than guessed at, even when gate output follows.
- A push with no admit-hook line in its result: the shell skipped it (a failed
  `cd`, `false && ...`), it failed before the hook (bad refspec,
  non-fast-forward, `Everything up-to-date`, no such remote), the call was
  declined, or the line beside it is another remote's report. An unrelated
  short-circuit earlier on the line does not hide a push that ran.

How a command line is read. Nothing is matched against the raw text: the
line is read the way a POSIX shell splits it, then each command's words are
read the way the program parses them.

- Shell: backslash-newline continuations are joined; a `#` that starts an
  unquoted word begins a comment (a `#` inside a word does not); then `shlex`
  (POSIX mode) splits words, removing
  quotes and escapes (`"--dry-run"`, `\--dry-run`, `--"dry"-run` are all
  `--dry-run`), and `;`, `&&`, `||`, `|`, `&`, parentheses and newlines
  separate commands. Redirections and their targets are dropped. A compound
  command (`if`, `while`, `{ ...; }`, `!`) or a process substitution never
  parses as a push claim.
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

Residual false negatives beyond the not-proven cases above: a wrapper before
the program (`env`, `command`, `exec`, `sudo`, `sh -c '...'`) or a git alias,
which the parser does not read as a push; output redirected away or filtered
through a pipe that drops the admit-hook lines; a file-descriptor redirection
written before the remote (`2>log`, which splits as a word `2`); a refusal the
service words without `gate.admit:`; and a run started in the background,
whose output goes to a file. The admit-hook phrases are matched as
`internal/machine` words them today; rewording them makes pushes score not
proven, and nothing here detects that drift.

Residual false positive: earlier gate output replayed by another command on
the same line (for example `cat` of a saved push log beside a push to
`assistant` that fails) is read as this push's proof.

`gate_ran_from_commands` is only the offline command-text heuristic (verify /
selftest). It sees no result, so it still counts `--intent` / `--answer` and
`cd missing && assistant --intent`. It rejects a line it cannot fully read and
one with a literal `false &&` / `true ||`.

`python3 detect_gate.py` runs the fail-before selftest. Parser rows go
through `gate_ran_from_commands` (flags, flag spelling, continuation, escape,
option value, cluster, quoting, remote-name prefix, heredoc text). Live rows go
through `gate_ran_from_attempt` / `attempt_verdict` with result text captured
from a real push through this repository's CLI and gate, from zsh and git
2.54, or recorded in the Q1 run. They cover intent not proven, heredoc not
proven, the reasons, failed `cd`, up-to-date, a push that failed before the
hook, a wrapped push beside another remote's report, and a dry run's output.
Fuller offline controls and the activation-smoke / fixed-diff runners stack
in follow-up PRs of 300 code lines or under each.
