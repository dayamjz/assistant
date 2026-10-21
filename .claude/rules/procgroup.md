---
paths: ["internal/procgroup/**"]
---

# internal/procgroup

- A group is signalled only while its leader is unreaped: `Run` calls
  `cmd.Wait` only after its last signal, and `AwaitExit` observes an exit
  without reaping. Keep any new path on that side of the line.
- It decides nothing about what an exit means; callers read their own
  context's error.

Read the package comment before changing any of that: it owns the residual
gaps, including the platforms that do not group a call.
