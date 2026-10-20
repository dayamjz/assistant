---
paths: ["internal/findings/**"]
---

# internal/findings

- `ParseReviewReport` is PRD section 5's "what a review report has to carry",
  with `Demand.Guidance` as the text a reviewer is held to. Two orderings hold
  it together and both are structural:
  - the binding runs before `Normalize`, on the action the reviewer stated,
    which is what keeps demoting an unsupported finding from reaching P3;
  - it is reachable only through that one entry point, so a report already
    normalized, stored, or built by hand cannot be bound.

Read `doc.go` before changing any of that, and for the residual gaps.
