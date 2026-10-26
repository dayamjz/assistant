---
paths: ["internal/supervisor/**"]
---

# internal/supervisor and internal/supervisor/feed

- `internal/supervisor` is the pure core and performs no I/O: its direct
  imports are `internal/taskstate`, `internal/home` for its lock vocabulary,
  and the standard library. Its subpackage `feed` is the I/O edge.
- The two durations derive from the one configured interval,
  `config.KeySupervisionBeaconInterval`, through `FreshnessFor` and
  `FirstBeaconWait`; staleness and the first-beacon wait are named multiples
  and never rows of their own.
- `inflight.go` (which states owe a supervision cycle) and `terminal.go` (which
  states have their terminal read at all) are closed tables over
  `taskstate.States()`, so a new state fails until placed.
- The kind table's `Subordinate` column is what PRD section 8's wake row
  routes by, read out by `SubordinateActable` and held to its own oracle in
  the matrix test. `subordinate-gone` and `subordinate-escalated` are the
  subordinate's two kinds; the first is carried on `internal/store`'s domain
  wake, and `InFlightOf` counts those rows as a third input.
- `kept-work` is PRD section 6's kept work, actionable and never a
  subordinate's, raised once per kept work entry: on the task wake, or on
  `internal/store`'s kept-work wake for an entry no task names, whose
  queued rows `InFlightOf` counts with the domain wakes.
- `housekeeping-due` is PRD section 6's housekeeping, actionable and never
  a subordinate's, carried on `internal/store`'s housekeeping wake about a
  repository, whose queued rows `InFlightOf` counts with the other untasked
  shapes. `PassMoves` is the one statement of "something moves the pass",
  open or retired, and `RegardOf` of how a task's state counts toward in
  flight (`Regardless` for an open stall hold or a pass something moves,
  `QueueOnly` for a retired pass nothing moves), which the loop, the stop
  guard and the catch-up read all count through `TaskInFlight`.

Read both `doc.go` files (`doc.go` and `feed/doc.go`) before changing any of
that, and for the residual gaps; `kind.go` is the one owner of what a wake kind
means.
