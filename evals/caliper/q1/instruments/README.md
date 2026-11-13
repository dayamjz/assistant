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

What is not a gate command at all: a push with dry-run flags (`--dry-run`,
`-n`, combined short flags like `-fn`) before or after the remote, quoted or
not (`"--dry-run"`); a remote that is not exactly `assistant`
(`assistant-backup`); and text that names the gate only inside a multi-word
quoted span, a comment or a heredoc body. A one-word quoted span is read as
that word, so `git push "assistant"` is a push to `assistant`, and quoted
intent text is ignored, so `assistant --intent "fix false && x"` counts.

Residual false negatives (real gate runs that still score false): any
backtick or `$(...)` on the line; leading whitespace before the gate command;
a gate command only on a later line of a multi-line command; an env prefix
(`VAR=val assistant ...`); a path invocation (`./bin/assistant ...`);
`git -C dir push assistant`; a line with more than one `git push`; `git push
-q` or `--porcelain` (git 2.54 printed no status block for `-q`); output
filtered through a pipe that drops the evidence; and a run started in the
background, whose output goes to a file. The progress lines are matched as
`internal/cli/runs.go` words them today; rewording them makes intents and
answers score false, and nothing here detects that drift.

`gate_ran_from_commands` is only the offline command-text heuristic (verify /
selftest); it sees no result, so it counts `cd missing && assistant --intent`
and rejects only literal `false &&` / `true ||` short-circuits.

`python3 detect_gate.py` runs the fail-before selftest: one `class_*` row per
command shape the reviews found (short-circuit, failed `cd`, not found, flags
after the remote, remote-name prefix, up-to-date, a push that failed before
the hook, a quoted flag, no service), each with result text recorded in the
Q1 run or captured from zsh, git 2.54 or this repository's CLI. Fuller offline
controls and the activation-smoke / fixed-diff runners stack in follow-up PRs
of 300 code lines or under each.
