---
paths: ["internal/scope/**"]
---

# internal/scope

- The review stage's scope lens, not a tenth stage. It ships on and has no off
  switch.
- Its findings are `note` by construction, and a change that is also wrong is
  reported for being wrong through the ordinary review path.

Read `doc.go` before changing what fires, and for the residual gaps.
