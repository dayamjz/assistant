---
paths: ["internal/tasks/**"]
---

# internal/tasks

- PRD section 8's tasks module as far as this build takes it: intake,
  queueing, the P11 assertion, the P12 cleanup decision and promotion, with
  dispatch and the copy in `internal/service` and the launch in
  `internal/session`.
- Intake and promotion record a delivery task's delivery posture from
  `Deps.Posture`, the operator's layer tightened by the project's trusted copy
  as `internal/service` resolves them (the key is `config.TrustGlobalLoosens`:
  a repository file may tighten it and a looser one is a
  `*config.LoosenError`). They refuse with `ErrPostureUnreadable` when that
  read fails rather than assuming one, and let a request tighten it and never
  loosen it (`*PostureError`).
- A local task's cleanup proof is the content check against the working
  copy's default branch and nothing else.
- This package's own P11 citation claims the assertion refuses and nothing
  about launch; `internal/service`'s claims the launch edge.
- It is P12's decision and the only writer of the cleanup record. An
  inconclusive proof is a `ProofAttempt` on the `*CleanupRefusal`, never a
  proof, and no remedy names a flag.

Read `doc.go` before changing the floor or the clear condition, and for the
residual gaps; read `promote.go`'s opening comment and `doc.go` before
changing promotion's order, and for its gaps.
