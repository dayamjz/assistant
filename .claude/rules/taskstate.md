---
paths: ["internal/taskstate/**"]
---

# internal/taskstate

- `internal/store` only returns the facts, through `TaskSnapshot`, and learns
  no meaning; `kind.go` is the one owner of what an event kind means.

Read `doc.go` before changing the tiers, for the residual gaps, and for why PRD
section 8's "task state" row is read as a resolution rather than a row;
`Resolve`'s doc comment in `resolve.go` is the precedence contract.
