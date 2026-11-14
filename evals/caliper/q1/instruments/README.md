# Q1 instruments (plan row 8 / issue #895)

Shared measurement helpers beside the agentic Q1 harness.

This PR lands `detect_gate.py`: whether a Caliper attempt actually invoked
the assistant gate (`assistant --intent` / `--answer`, or `git push
assistant`), not merely whether the skill activated (#398).

Live scoring is `gate_ran_from_attempt`. A gate command counts only when its
paired `tool_result` shows it reached the gate, wherever it sits on its line:

- `--intent` / `--answer`: a progress line `internal/cli/runs.go` prints
  (`starting or attaching ...`, `answering ...`), with no
  `the background service is not running` failure
  (`internal/cli/client.go`). The CLI prints the progress line before it
  dials the service, so the line alone shows only that it tried.
- `git push assistant`: under git's `To <remote>` line, a ref that was
  updated (` * `, `   `, ` + `, ` - `) or that the pre-receive hook declined
  (`! [remote rejected] ... (pre-receive hook declined)`). The push must be
  the line's only `git push`, because another push's report looks the same.
  With git 2.54, a bad refspec, a local `! [rejected]` (non-fast-forward),
  `Everything up-to-date` and a missing remote all left the hook unrun and
  printed none of these. A dry run also left it unrun but does print
  ` * [new branch]`, so dry runs are excluded by their flags (below), not by
  this evidence.

So a command the shell skipped (a failed `cd`, `assistant --cancel && ...`,
`false && ...`), a missing `assistant` binary in any shell's wording, and a
declined tool call all score false, because none prints that evidence.

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

A real-git differential run (42 command lines through `zsh -c` against a bare
repository whose `pre-receive` hook records each run) found no line the
detector counted that git did not deliver to the hook; the misses are the
residuals below.

Residual false negatives (real gate runs that still score false): any
backtick or `$(...)` on the line; a wrapper before the program (`env`,
`command`, `exec`, `sudo`, `sh -c '...'`); a git alias for push; a line with
more than one `git push`; `git push -q` or `--porcelain` (git 2.54 printed no
status block for `-q`); output redirected away or filtered through a pipe that
drops the evidence; a file-descriptor redirection written before the remote
(`2>log`, which splits as a word `2`); a herestring (`<<<`), whose following
lines are cut like a heredoc body; and a run started in the background, whose
output goes to a file. The progress lines are matched as
`internal/cli/runs.go` words them today; rewording them makes intents and
answers score false, and nothing here detects that drift.

`gate_ran_from_commands` is only the offline command-text heuristic (verify /
selftest); it sees no result, so it counts `cd missing && assistant --intent`
and rejects only literal `false &&` / `true ||` short-circuits.

`python3 detect_gate.py` runs the fail-before selftest: one `class_*` row per
command shape the reviews found (short-circuit, failed `cd`, not found, flags
after the remote, flag spelling, remote-name prefix, up-to-date, a push that
failed before the hook, a quoted flag, no service, continuation, escape,
option value, cluster, quoting), each with result text recorded in the
Q1 run or captured from zsh, git 2.54 or this repository's CLI. Fuller offline
controls and the activation-smoke / fixed-diff runners stack in follow-up PRs
of 300 code lines or under each.
