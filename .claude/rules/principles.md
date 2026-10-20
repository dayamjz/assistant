---
paths: ["internal/principles/**"]
---

# internal/principles

- It fails the build when a principle the PRD lists has no test claiming it,
  and claiming is all a citation is: it says a test says it checks that
  principle, never that it does. Nothing built on it may describe it as
  coverage.
- A test cites with `principles.Cite(t, principles.P6)` inside its own body
  and only that counts, so a comment naming a principle is not a citation.
- Where nothing claims a principle the gap is a row in `unclaimed.go` rather
  than an absence nobody can see.

Read its `doc.go` before changing the rule, and for the residual gaps.
