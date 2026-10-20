---
paths: ["internal/custody/**"]
---

# internal/custody

- PRD section 11's custody of a person's branch: section 6's approved local
  landing and `assistant sync` both go through it.
- The service reads its facts from their owners and serves it as the
  restricted `branch.sync`.

Read `doc.go` before changing any of that, for what each class is read from in
this build, and for the residual gaps; `steps.go`'s `Steps` owns why the plan
confirmed and the steps taken cannot differ.
