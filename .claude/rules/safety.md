---
paths: ["internal/safety/**"]
---

# internal/safety

- Owns three questions: whether a branch update may proceed and on what anchor,
  and whether a set of commits is already incorporated in a tip, which is the
  git half of P12's content proof and fails closed through its own
  `IncorporationRefusal` matching `ErrNotEstablished` and never `ErrRefused`;
  and whether a kept copy is the one its registry recorded
  (`VerifyKeptCopy`, `keptcopy.go`, refusing with a `CopyRefusal` matching
  `ErrCopyNotVerified`), which every path that reads or writes a kept copy
  asks first.
- Its anchor is an `Observation`, and only `Guard.Observe` or
  `RestoreObservedFromCheckpoint` produces one.

Read `doc.go` before changing any of that, for why the wrong anchor is not
unrepresentable, and for the residual gaps that leaves; `local.go`'s opening
comment owns `DecideFastForward`.
