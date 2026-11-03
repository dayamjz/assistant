---
paths: ["internal/judge/**"]
---

# internal/judge

- The mode is the global-only `decision_model` key.
- The review stage turns answers into findings in `internal/stages`'
  `decision.go`, which may only add.
- Tests reach the provider over the wire through `internal/judge/judgetest`,
  never live.

- A model's price is set only in `rate.go`'s `rates`, and whether a call is
  priced is `store.PricedAt`'s to say.

Read `doc.go` for the measurement and the gaps. What the record keeps beyond
the answer lives outside this package: `internal/service`'s `observe.go`
writes what the run did with a path afterwards
(`store.AppendDecisionFollowUp`), and `decision.go`'s `proposals` reads it
back (`store.DecisionFirings`) to propose turning a check off.
