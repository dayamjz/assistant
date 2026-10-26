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
  keptWorkWakesMigration's `kept_work_wake` is the third shape, PRD section
  8's kept-work wake about an entry no task names, owned by the entry, once
  per entry; `AppendKeptWorkWake` is its one append and records the entry's
  `surfaced_at` in the same transaction. `untaskedShape` serves the second
  and third shapes' offer, handout and acknowledgement; `DrainAll` and
  `AcknowledgeAll` take every shape in one transaction.
- housekeepingMigration (59) is PRD section 6's housekeeping: the task row's
  `housekeeping` mark (written once at intake; only a marked investigation
  of a repository carries the `housekeeping` role), the
  `housekeeping_retirement` record (`RetireHousekeepingPass`, written once,
  refused by every attempt claim with `ErrPassRetired`), the queue's fourth
  shape `housekeeping_wake` about a repository with its notification twin,
  and `housekeeping_marker`, the due check's comparison base. `CreateTask`
  decides one open pass per repository in its own transaction with the
  record tier passed in as `Disposition.Delivered`; `PassOpen` is the one
  statement of "open".
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
  (`*KeptWorkOpenError`) or an unacknowledged kept-work wake about one
  (`ErrKeptWorkWakeQueued`), and a run's entry, its wake and the wake's
  notification go with its run only once it is answered or closed and the
  wake acknowledged. Eject's preflight is `ReserveRepositoryRemoval`,
  which writes `repository_removal`, naming the eject's process, in the
  transaction that found the repository forgettable; `CreateRun` refuses a
  run of a reserved repository (`ErrRepositoryBeingRemoved`), and nothing
  refuses a kept work entry over a reservation. internal/service releases a
  reservation whose owner is gone when it opens.
- taskMachineMigration's `task.machine` is PRD section 8's machine a task
  runs on, the one place it is stored: written once, for a remote host, by
  `BeginFirstTaskAttempt` (the dispatch, the only first attempt; a carry on
  names its entry through `CarriedOnFrom` and never a machine), sealed by
  trigger from the first attempt on, and NULL for this machine. A remote
  worker's herdr server (`host_pid`, `host_start`) is written with its
  attempt's session reference by `SetAttemptSession` and only for a remote
  task. A session reference and a kept work entry point at the task row and
  hold no copy.
- runRebaseMigration's `run_rebase` is the run rebase record the rebase
  stage writes through `RecordRunRebase`, held to the run row's submitted
  commit (`ErrRebaseNotOfRun`), written once, and gone only with its run.
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
