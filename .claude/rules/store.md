---
paths: ["internal/store/**"]
---

# internal/store

- Its driver is pure Go on purpose, so `make check` needs no cgo on any
  platform.
- `RecordTaskCleanup` takes the shape it was decided for and refuses
  `ErrTaskShapeMoved` in the claiming transaction. `TransitionTaskShape`,
  promotion's one write, flips shape, repository, branch and posture with the
  `promoted` entry in one anchored transaction, one way only, and refuses
  `ErrCleanupRecorded` by reading the claim the same way, so the two writes
  have exactly one loser in either order.
- Migration 47's `delegation`, `delegation_attempt` and `escalation` are PRD
  section 8's subordinate-domain records, each written once and sealed, with
  `EndDelegation` the delegation's one revision; the task row's `domain` is
  written at `CreateTask` and never moved. Migration 48's `domain_wake` is the
  queue's second shape, a wake about a domain, owned by the delegation row and
  under the wake table's seals; `AppendDomainWakeWithMarkers` is its one
  append and advances `delegation_marker` in the same transaction.
- A wake's recipient is decided at `DrainWakes` by its task's domain and a
  `WakeRoute` of kinds the caller passes as words, stored on no row (P14).
- `AcknowledgedBySubordinate` and `SentBySubordinate` are the two closed sets'
  members for a subordinate, produced only by `internal/service` from
  placement.
- The task row's `mode` column is the launch notation and never the delivery
  posture: the posture is the `delivery` column localLandingMigration adds,
  written once by `CreateTask` or, for a promoted task, by
  `TransitionTaskShape`, which promotedPostureMigration's trigger enforces.
- localLandingMigration's `local_landing` is PRD section 8's local landing
  record, written once by `RecordLocalLanding` in the transaction that closes
  the approval hold and sealed like `merge_record`.
- keptWorkMigration's `kept_work` is PRD section 8's kept work record: one
  row per kept copy and the commits it holds (`RecordKeptWork` finds the row
  already written for the same pair), naming one owner, a task or a run,
  with project and branch read off that owner's row. `holds` is the copy's
  own state and the identity; the basis (keptWorkAgainstMigration) is no
  part of it: `lacks` against `against`, or `unestablished`, exactly one on
  a new row, and a row from before carries neither and reads unestablished.
  `EstablishKeptWork` fills an open entry's unestablished basis in once,
  the only move the basis makes, and refuses an answered or closed one
  (`ErrKeptWorkAnswered`, `ErrKeptWorkClosed`), as the trigger does;
  `KeptWorkOf` is the read of the entry standing for a copy state. It is
  otherwise written once;
  its one revision closes it, once: a person's answer (`AnswerKeptWork`) or
  the program's `KeptRemovedOnProof`, written only by `MarkCleanupRemoved`
  and `MarkRunCleanupRemoved` in the removal's transaction, never a discard.
  `surfaced_at` is the one other column that moves, only forward. `ForgetRepository` and
  `RepositoryRemovable` refuse a repository with an open entry
  (`*KeptWorkOpenError`), and a run's entry goes with its run only once it
  is answered or closed. Eject's preflight is `ReserveRepositoryRemoval`,
  which writes `repository_removal`, naming the eject's process, in the
  transaction that found the repository forgettable; `CreateRun` refuses a
  run of a reserved repository (`ErrRepositoryBeingRemoved`), and nothing
  refuses a kept work entry over a reservation. internal/service releases a
  reservation whose owner is gone when it opens. `keptworkhold.go` is the
  dispatch hold's one predicate: a task naming a project, none of whose
  attempts recorded a session (a claim alone proves no worker started),
  whose project has an open entry; `BeginDispatchAttempt`
  and `ClaimAttemptLaunch` (the last write before a worker starts) refuse
  it with `*DispatchHeldError` in their own transactions, and
  `BeginTaskAttempt`, the relaunch claim, is never held. Migration 53's `landed_run` is the run a
  land answer started, written once by `RecordKeptWorkLanding` and only on
  an entry answered land; `KeptWork.Key` and `ParseKeptWorkKey` are the one
  spelling of `--hold kept-work/N`. Migration 55's `carried_on_task` is
  named with the carry-on answer (`AnswerKeptWorkCarryOn`) and
  `carried_on_at` once the task owns the copy; migration 56's
  `discard_digest` is what the discard answer was shown
  (`AnswerKeptWorkDiscard`) and `discarded_at` once the copy went. Each
  answer's record is its own accessor, and `AnswerKeptWork` takes land
  alone. `TransitionTaskShape` answers the promoted task's own copy's open
  entries carry-on, naming that task, in the flip's transaction, as PRD
  section 6's promotion paragraph says; it takes the promoter's `Resolver`
  for that.
- A schema change is a new migration appended to `schema.go`, never an edit to
  one that has shipped; the tests number
  what they append from the shipped list rather than spelling a version out.
- The run checkpoint history is the only record of a run's position: opaque
  payloads indexed by run and sequence, with the anchor a property of the
  request that no column holds.
- The one-row `checkpoint` table migration 1 created is still declared because
  a migration may not remove a table, so migration 6 seals it with a trigger
  instead; a drop would mean weakening `verifyAdditive`, which is what protects
  every other table's rows.

Read `doc.go` before changing any of that, and for the hold resolver, the wake
queue and its incarnation fence, the read-only open, the message and process
records, and the residual gaps.
