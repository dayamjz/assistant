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
  loosen it (`*PostureError`). A housekeeping pass is promoted gated whatever
  the project declares, as a request for gated, because its scope is held at
  the review of the run its push starts and a local task starts no run; the
  project's posture is read for it as for any task, so an unreadable copy
  refuses it too; a request for another posture on a pass is `ErrRequest`,
  and a retired pass is refused in the flip (`store.ErrPassRetired`).
- A local task's cleanup proof is the content check against the working
  copy's default branch and nothing else.
- The remote-branch and merged-pull-request proofs follow a commit the gate
  rebased only through a `store.RunRebase` of a run the snapshot attributes
  to the task, never by searching for a commit with the same content, and
  only when integrating the copy's commit into the record's published head
  would change nothing; `proof.go` owns it.
- This package's own P11 citation claims the assertion refuses and nothing
  about launch; `internal/service`'s claims the launch edge.
- It is P12's decision and the only writer of the cleanup record. An
  inconclusive proof is a `ProofAttempt` on the `*CleanupRefusal`, never a
  proof, and no remedy names a flag.
- It is also the one writer of the kept work record: `Keep` reads what a
  kept copy holds from the copy (the entry's identity), reads what of it
  the default branch, fresh and fetched by identifier, lacks (`lacking` in
  `kept.go`; a stash is uncommitted state and not compared; an unreadable
  branch is recorded as unestablished, never guessed, and established by a
  later `Keep` of the unchanged copy while the entry is open), reads the
  entry already standing for the copy state before deciding anything, and
  records the entry or `NothingKept`, which sees what `vcs.Inventory` sees
  and so not an ignored file; an entry established with nothing lacking
  stays open until answered or until a cleanup removes the copy on a
  proof. And
  `CleanupRefusal.Unlanded` (with `KeptOf`) is the one statement of which
  refusals are a copy kept for its work, and `KeptOnRetirement` the one
  statement for a retired housekeeping pass's copy, which also keeps a copy
  refused for a report or hold its retirement means will never come;
  `kept_test.go` places every reason for both.

Read `doc.go` before changing the floor or the clear condition, and for the
residual gaps; read `promote.go`'s opening comment and `doc.go` before
changing promotion's order, and for its gaps.
