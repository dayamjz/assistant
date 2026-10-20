---
paths: ["internal/session/**"]
---

# internal/session

- `Provider` is the five operations and nothing else; an optional behaviour
  a later provider has is a row in `internal/agents`' table with its probe
  and declarer.
- `Pipe` and `Tmux` both embed one held set (`held.go`), so Probe's rule, the
  reference and the worker set have one implementation.
- Its one creating caller is `internal/service`'s `launch.go`; `Create`
  itself re-derives nothing. Its one observing caller is that package's
  `terminals.go`, which probes through `Probe` and captures through
  `CaptureReference` out of the same held set `Probe` answers `Live` from, so
  the reference to handle lookup has one owner.
- Three things are mechanism rather than rule:
  - The `reasons` table is the one place a reason's liveness is decided:
    `Live` only through a handle this provider holds whose process has not
    exited, `Dead` only where absence is proven, everything else
    `Unobserved`, a probe error among it.
  - The identity is the attempt's session `Reference`, recorded by the
    caller through `store.SetAttemptSession` and nowhere else, with no file
    under the home.
  - The providers' `Create` produce `Workers`; the join is
    unexported, so nothing a caller states is a membership. It is the
    authority input every worker-to-program path derives a task from and it
    is not `internal/service`'s containment registry.
- `Subordinates` is the second held set, beside `Workers` on its terms,
  joined only by `Host.Delegate` (PRD section 9's `domain.delegate`), which
  starts a subordinate coordinator's session the way `Create` starts a
  worker's but in a directory of the home's rather than a proven copy; a
  subordinate's `Reference` names its domain under a prefix of its own.

Read `doc.go` before changing any of that (its tmux section owns the
placement and containment answers), for the restart answer, and for the
residual gaps.
