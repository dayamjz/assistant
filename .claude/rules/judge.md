---
paths: ["internal/judge/**"]
---

# internal/judge

- The mode is the global-only `decision_model` key.
- The review stage turns answers into findings in `internal/stages`'
  `decision.go`, which may only add.
- Tests reach the provider over the wire through `internal/judge/judgetest`,
  never live.

Read `doc.go` for the measurement and the gaps, and these two it does not
state: the record does not yet keep what the run did with a path afterwards,
and nothing proposes turning a check off.
