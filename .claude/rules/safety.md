---
paths: ["internal/safety/**"]
---

# internal/safety

- Owns two questions: whether a branch update may proceed and on what anchor,
  and whether a set of commits is already incorporated in a tip, which is the
  git half of P12's content proof and fails closed through its own
  `IncorporationRefusal` matching `ErrNotEstablished` and never `ErrRefused`.
- Its anchor is an `Observation`, and only `Guard.Observe` or
  `RestoreObservedFromCheckpoint` produces one.

Read `doc.go` before changing any of that, for why the wrong anchor is not
unrepresentable, and for the residual gaps that leaves; `local.go`'s opening
comment owns `DecideFastForward`.
