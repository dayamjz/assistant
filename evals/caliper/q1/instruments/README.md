# Q1 instruments (plan row 8 / issue #895)

Shared measurement helpers beside the agentic Q1 harness.

This PR lands `detect_gate.py`: whether a Caliper attempt actually invoked
the assistant gate (`assistant --intent` / `--answer`, or `git push
assistant`), not merely whether the skill activated (#398).

Live scoring needs a paired `tool_result` that looks usable. Short-circuited
shell lines (`false && …`) and heredoc bodies that only name the gate do
not count. Ordinary `cd work && assistant --intent …` does.

Follow-up PRs stack the activation smoke runner and the fixed-diff
efficacy arms on this detector.

## Offline check

```sh
python3 -c 'from detect_gate import gate_ran_from_commands; assert gate_ran_from_commands(["assistant --intent x"])'
```
