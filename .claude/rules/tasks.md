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
  refusals are a copy kept for its work; `kept_test.go` places every reason.
- `kept_discard.go` is the copy's half of PRD section 6's discard: `Loss`
  reads what removing the copy would lose, the commits the default branch
  lacks that any name the removal destroys reaches (HEAD, a reference, a
  reflog entry) and every other object such a name points at, read from
  the copy now (`lostFrom`), every path whose content or deletion nothing
  else holds (`kept_lost.go`, decided from the copy against HEAD's tree, the
  index and what git writes into a new repository, ignored files and
  deletions included) and the
  inventory's three indicators, with a digest over everything the discard
  would destroy, decided from what the copy holds and never from what git
  reports changed (`copyState`: the whole copy walked by the filesystem,
  ignored files included, its repository directory too, and what git
  rewrites there sealed by content: every reference, every reflog entry,
  the stash list whole, the index at every stage; then the commits
  lacking; the header names what it does not cover), refusing an
  unestablished entry; `Discard`
  removes the copy only on that digest, the one use of
  `vcs.RemoveWorktreeDiscardingChanges`, behind a person's answer and no
  proof.
- Intake writes the instructions file whole and never through a link,
  `writeWhole` (an exclusively created partial of a fresh name, then a
  rename; a path standing as a link is `*InstructionsLinkError`), so a file
  under the name is never partial, and `CompleteIntake` is intake's second
  step for a row a stop left without its file, removing stale partials as
  names; `InterruptAfterRecordForTest` is the seam between the two.

Read `doc.go` before changing the floor or the clear condition, and for the
residual gaps; read `promote.go`'s opening comment and `doc.go` before
changing promotion's order, and for its gaps.
