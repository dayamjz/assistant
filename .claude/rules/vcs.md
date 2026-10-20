---
paths: ["internal/vcs/**"]
---

# internal/vcs

- `Incorporated` and `Inventory` decide nothing, and nothing in this package
  removes a clone.
- `Observe` is taken under this package's own environment, so a
  `vcs.Observation` says what a copy on disk is and never what a worker's
  environment would make git see there.

Read `doc.go` before changing any of that: the package comment states the two
rules applied to every invocation and the residual gaps in them.
