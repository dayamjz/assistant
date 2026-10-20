---
paths: ["internal/agents/**"]
---

# internal/agents

- It owns the process tree, the per-invocation environment, and what is
  recorded about a call.
- P4 lives in its type split rather than in a rule callers follow:
  `Runner.Run` cannot be given a session and `Fixer.Apply` cannot be given a
  purpose, so keep any new entry point, and any new `Shape`, on one side of
  that line. A task's worker sits on the fixer side by its types.
- Add a capability by adding a row to the table in `capability.go` and a
  conformance probe in `capability_test.go`.
- `ReadLineage` is the one read of a process's parent, group and its
  terminal's foreground group, and `Lineage.Foreground` is the predicate
  `internal/service`'s foreground relation decides from.
- Mechanism other packages rely on: `Identity` is the one PID-reuse check,
  and `EndGroup` the only way to end a group it started from outside the
  invocation, signalling nothing until `Identity.Presence` verifies the
  leader. `ErrSweepUnsupported` means "could not prove", and its zero
  `SweepResult` is not `Clear`, so never read it as "none found". A sweep
  refusal's blocker command name is for diagnosis only and must never feed a
  decision.

Read `doc.go` before changing the P4 split, the capability declarations, the
worker, the sweep or `EndGroup`, and for the residual gaps. `grant.go` owns
what a started agent may do, `terminal.go` and `claudeterminal.go` the
terminal worker's terms, and `capability.go` how the stand-in's model and an
installed-CLI probe meet.
