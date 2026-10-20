---
paths: ["internal/ipc/**"]
---

# internal/ipc

- It owns the local protocol: the method table, the event taxonomy, and the
  bounded stream.
- Load-bearing rather than stylistic:
  - An unrecognized event type is state, so a type added later is retained;
    adding a type means adding a row to the class table in `event.go`.
  - Authority over a request comes from `Credentials`, read off the socket,
    and never from `Marker`, which a caller writes for itself.
  - No frame has a field for who resolved a hold: `Request.HoldResolver`
    derives it from the surface, so a caller cannot claim a person decided;
    `Request.WakeAcknowledger` is the same rule for the wake queue.
- Whether a peer is a task's worker, and whether it is in the foreground of
  its controlling terminal, are relations over the same `Credentials`
  decided by the handler, never a third access class; `ErrWorker`,
  `ErrNotWorker` and `ErrNotAtTerminal` are their refusals.
- Whether a peer is a domain's subordinate coordinator is the worker
  relation's second answer, decided by the handler and never a third access
  class; `ErrSubordinate` and `ErrOutsideDomain` are its two refusals, and
  `domain.delegate` and `domain.end` the two coordinator-only methods that
  start and end it.

Read `doc.go` for where state may still be collapsed into a gap marker and
why that is not the strict rule relaxed, for the restricted `wakes.subscribe`
stream, and for which methods each relation decides.
