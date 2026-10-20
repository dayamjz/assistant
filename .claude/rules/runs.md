---
paths: ["internal/runs/**"]
---

# internal/runs

- A move that leaves a run finished calls `Options.OnFinish` with the row it
  wrote, outside the lock, never for a move that does not.
- The fixer session reference is on the run's own row and must never move into
  `internal/pipeline` graph state: that state is the convergence fingerprint,
  and a reference that changed across a resume would silently disable the
  bound.

Read `doc.go` before changing any of that, and for the residual gaps.
