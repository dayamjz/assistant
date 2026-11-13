# Q1 instruments (plan row 8 / issue #895)

Shared measurement helpers beside the agentic Q1 harness.

This PR lands `detect_gate.py`: whether a Caliper attempt actually invoked
the assistant gate (`assistant --intent` / `--answer`, or `git push
assistant`), not merely whether the skill activated (#398).

Live scoring is `gate_ran_from_attempt`: a paired `tool_result` that looks
usable. What does not count:

- A gate command that is not the first command of its line (after `&&`,
  `||`, `;` or `|`) unless its own output is in the result: the progress
  line `internal/cli/runs.go` prints before calling the service
  (`starting or attaching ...`, `answering ...`), or for a push one more
  `To <remote>` line than there are earlier `git push` commands on the line.
  Text alone cannot say whether `cd dir && assistant --intent ...` ran, so a
  failed `cd`, `assistant --cancel && ...` and `false && ...` all score false.
- A result saying the `assistant` binary was not found, in any shell's
  wording (`(eval):1: command not found: assistant`, `bash: assistant:
  command not found`, `sh: 1: assistant: not found`), or that the user
  declined to run it.
- A push that never reached the gate's receive hook: dry-run flags
  (`--dry-run`, `-n`, combined short flags like `-fn`) before or after the
  remote; a remote that is not exactly `assistant` (`assistant-backup`); no
  `assistant` remote; `Everything up-to-date`. With git 2.54 none of these
  ran a `pre-receive` hook.
- Text that only names the gate inside quotes, comments or heredoc bodies.
  Quoted intent text is ignored, so `assistant --intent "fix false && x"`
  counts.

Residual false negatives (real gate runs that still score false): any
backtick or `$(...)` on the line; leading whitespace before the triad; a gate
command only on a later line of a multi-line command; an env prefix
(`VAR=val assistant ...`); a path invocation (`./bin/assistant ...`);
`git -C dir push assistant`; and a gate command after another command whose
evidence is missing from the result: `git push -q` (git 2.54 printed no `To`
line), output filtered through a pipe, an earlier `git push -q` on the line,
or a run started in the background. The progress lines are matched as
`internal/cli/runs.go` words them today; rewording them makes such commands
score false, and nothing here detects that drift. A push line where another
push prints `Everything up-to-date` also scores false.

`gate_ran_from_commands` is only the offline command-text heuristic (verify /
selftest); it sees no result, so it counts `cd missing && assistant --intent`
and rejects only literal `false &&` / `true ||` short-circuits.

`python3 detect_gate.py` runs the fail-before selftest: one `class_*` row per
command shape earlier reviews found (short-circuit, failed `cd`, not found,
flags after the remote, remote-name prefix, up-to-date), each with result text
taken from the recorded Q1 run or captured from zsh and git 2.54. Fuller
offline controls and the activation-smoke / fixed-diff runners stack in
follow-up PRs of 300 code lines or under each.
