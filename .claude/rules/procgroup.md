---
paths: ["internal/procgroup/**"]
---

# internal/procgroup

- A group is signalled only while its leader is unreaped: `Run` calls
  `cmd.Wait` only after its last signal, and `AwaitExit` observes an exit
  without reaping. Keep any new path on that side of the line.
- It decides nothing about what an exit means; callers read their own
  context's error.
- The reap `ErrAbandoned` leaves never calls `cmd.Wait`, so it writes no
  field of `cmd` and closes nothing os/exec would: `check` refuses every shape
  that leaves `cmd.Wait` something to finish (a context, a `*Pipe()` stream).
  A new such shape is refused there too.

Read the package comment before changing any of that: it owns the residual
gaps, including the platforms that do not group a call.
