---
paths: ["internal/gate/**"]
---

# internal/gate

- It composes `internal/store` for the ownership index without opening a
  database.
- Two things are mechanism rather than rule, because this package wrote both
  rules down and then broke them:
  - No operation that changes a gate takes a gate's path, only the working copy
    it is asked about: one unexported seam resolves the gate, settles who it
    belongs to, and seals it, and an operation that cannot obtain a handle
    cannot skip any of that. `WorkingCopyFor` is the one exported operation
    outside the seam, and it is confined to a read that seals nothing.
  - Nothing this package produced counts as evidence of ownership, so neither
    the `assistant` remote nor the path hash is weighed: the question is one
    shared rule both askers run, over the store's ownership index and the
    gate's own record, and every answer either gives is checked against the
    working copy actually standing there.

Read `doc.go` before changing any of that, and `copies.go` before changing the
copies lock, and both for the residual gaps.
