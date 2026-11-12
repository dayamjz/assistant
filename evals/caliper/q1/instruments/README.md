# Q1 instruments (plan row 8 / issue #895)

Shared measurement helpers beside the agentic Q1 harness.

This PR lands `detect_gate.py`: whether a Caliper attempt actually invoked
the assistant gate (`assistant --intent` / `--answer`, or `git push
assistant`), not merely whether the skill activated (#398).

Live scoring is `gate_ran_from_attempt`: a paired `tool_result` that looks
usable. Short-circuited shell lines (`false && …`, including after `;`),
`--dry-run` / `-n` pushes, and heredoc bodies that only name the gate do
not count. Ordinary `cd work && assistant --intent …` does. Any backtick
or `$(…)` on the line is treated as not executed (residual false negative
for real `cd "$(pwd)" && assistant …`). `gate_ran_from_commands` is only
the offline command-text heuristic (verify / selftest); it does not apply
tool_result pairing.

`python3 detect_gate.py` runs the fail-before selftest. Fuller offline
controls and the activation-smoke / fixed-diff runners stack in follow-up
PRs of 300 code lines or under each.
