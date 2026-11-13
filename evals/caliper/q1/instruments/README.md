# Q1 instruments (plan row 8 / issue #895)

Shared measurement helpers beside the agentic Q1 harness.

This link adds offline `verify_detect.py` controls for `detect_gate`.

`detect_gate.py` (prior link): whether a Caliper attempt actually invoked
the assistant gate (`assistant --intent` / `--answer`, or `git push
assistant`), not merely whether the skill activated (#398).

Live scoring is `gate_ran_from_attempt`: a paired `tool_result` that looks
usable. Short-circuited shell lines (`false && …`, including after `;`),
`git push --dry-run` / `-n` / combined short flags like `-fn` (push flags
only), text that only names the gate inside quotes or comments, and heredoc
bodies that only name the gate do not count. Ordinary
`cd work && assistant --intent …` does, including intent text that mentions
`-n`.

Residual false negatives (real gate runs that still score false): any
backtick or `$(…)` on the line; a `false &&` substring inside quoted intent
text; leading whitespace before the triad; a gate command only on a later
line of a multi-line command; an env prefix (`VAR=val assistant …`); a path
invocation (`./bin/assistant …`); and `git -C dir push assistant`.
`gate_ran_from_commands` is only the offline command-text heuristic (verify /
selftest); it does not apply tool_result pairing.

`python3 detect_gate.py` runs the fail-before selftest. Fuller offline
controls and the activation-smoke / fixed-diff runners stack in follow-up
PRs of 300 code lines or under each.
