# Q1 instruments (plan row 8 / issue #895)

Shared measurement helpers beside the agentic Q1 harness.

This PR lands `detect_gate.py`: whether a Caliper attempt actually invoked
the assistant gate (`assistant --intent` / `--answer`, or `git push
assistant`), not merely whether the skill activated (#398).

Live scoring needs a paired `tool_result` that looks usable. Short-circuited
shell lines (`false && …`, including after `;`) and heredoc bodies that
only name the gate do not count. Ordinary `cd work && assistant --intent …`
does. Any backtick or `$(…)` on the line is treated as not executed
(residual false negative for real `cd "$(pwd)" && assistant …`).

Offline negative controls and the activation-smoke / fixed-diff runners
stack in follow-up PRs of 300 code lines or under each.
