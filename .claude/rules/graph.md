---
paths: ["internal/graph/**"]
---

# internal/graph

- `Counters` also carries the run's usage budget, a run-level bound beside the
  loop bounds and not a fourth loop bound: before every node it is read against
  the run's own invocation rows through the `UsageMeter`, and no spend is kept
  in the checkpoint.
- The two budgets are the only bounds a caller may change on a live run, and
  only through `Executor.AdoptBudget`, which adopts both and writes the change
  into the run's history. A difference nobody asked for is `ErrBudgetChanged`,
  or for the usage budget `ErrUsageBudgetChanged`, which matches
  `ErrBudgetChanged`.

Read `doc.go` before changing any of that, and for the residual gaps; read
`identity.go` for what the edge digest covers and what it deliberately leaves
out.
