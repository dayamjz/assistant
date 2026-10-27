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
- housekeepingMigration (63) is PRD section 6's housekeeping mark: the task
  row's `housekeeping` mark (written once at intake; only a marked
  investigation of a repository carries the `housekeeping` role).
  `CreateTask` decides one open pass per repository in its own transaction
  with the record tier passed in as `Disposition.Delivered`; `PassOpen` is
  the one statement of "open". housekeepingRetirementMigration (64) is the
  `housekeeping_retirement` record (`RetireHousekeepingPass`, written once,
  only while nothing moves the pass as its transaction reads it,
  `ErrPassMoves`, and answering `stop` under the same resolver on every
  open hold `Retirement.Relaunches` names, which `internal/service` keys),
  which `PassOpen` reads as closing the pass, refused by every attempt
  claim, by `SetAttemptSession` and by `TransitionTaskShape` with
  `ErrPassRetired`, and anchored to the latest attempt's session so the
  two writes have exactly one loser and no session is recorded for an
  attempt of a retired pass after its retirement.
  housekeepingHoldMigration (65) is the retirement hold's mark on the
  `hold` row (`OfferRetirementHold` writes it, fenced and anchored, only
  to a pass still open and still; every attempt claim refuses a pass with
  one open, `ErrPassHeld`, so the offer and a claim have one loser in
  either order). housekeepingWakesMigration (66) is the queue's fourth
  shape `housekeeping_wake` about a repository (`AppendHousekeepingWake`
  is its one append; `untaskedShape` serves its offer, handout and
  acknowledgement; `DrainAll` and `AcknowledgeAll` take every shape in
  one transaction; `ForgetRepository` refuses a repository with one
  queued).
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
  its one revision closes it, once: a person's answer, written only with
  its outcome after a claim on the open entry, or
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
  reservation whose owner is gone when it opens. `keptworkhold.go` is the
  dispatch hold's one predicate: a task naming a project, none of whose
  attempts recorded a session (a claim alone proves no worker started),
  whose project has an open entry; the dispatch claims
  (`BeginFirstTaskAttempt`, and `BeginDispatchAttempt` for a retried
  dispatch) and `ClaimAttemptLaunch` (the last write before a worker starts)
  refuse it with `*DispatchHeldError` in their own transactions, and
  `BeginTaskAttempt`, the relaunch claim, is never held. Every answer is
  two facts, so an entry closes only in the write that records its
  outcome and an answer under way leaves it open and listed. keptWorkLandingMigration
  records a landing so: the claim (`landing_claimed_at`, who and when),
  written whole on an open entry by `ClaimKeptWorkLanding`, once, so two
  landings of an entry claim once between them, and withdrawn whole by
  `WithdrawKeptWorkLanding` when no run could be started; and the answer,
  written only with `landed_run` by `RecordKeptWorkLanding`, which the
  triggers enforce. `KeptWork.Key` and `ParseKeptWorkKey` are the one
  spelling of `--hold kept-work/N`. keptWorkCarryOnMigration records a carry on as two
  facts: the claim (`carried_on_task`, the task's reserved identifier, with
  the copy's identity, who and when), written whole on an open entry by
  `ClaimKeptWorkCarryOn`, which leaves it open and listed, and withdrawn
  whole by `WithdrawKeptWorkCarryOn` only while the task's row does not
  exist; and the answer, written only with `carried_on_at` by
  `MarkKeptWorkCarriedOn`, given the task the claim names, once that task
  exists and owns the copy, which the triggers enforce, and
  `KeptWorkCarriedOnBy` reads the answered entries back for that task, the
  copy identity dispatch holds the adopted copy to. A carry on moves the
  whole copy, so its mark answers and marks every other open entry of the
  copy, whenever it was kept, since its state moved with the copy, the set
  `OpenKeptWorkOfCopy` lists for the caller to hold the copy to first and
  name to the mark, which refuses an open entry it was not given
  (`ErrKeptWorkUnverified`) and writes nothing; and no
  answer or claim is written while an entry of the copy holds one not yet
  carried out, a claim on the entry itself included
  (`ErrKeptWorkCopyOwed`, the one condition `owedOnCopy`), and
  `TransitionTaskShape` refuses a promotion over one by that same
  condition, a landing claim as a carry-on claim. Each
  answer's claim and outcome are its own accessors, and there is no
  generic answer write, so nothing writes an answer of discard: its
  removal and record are deferred to AGE-486. `TransitionTaskShape` answers the promoted task's own copy's open
  entries carry-on, naming that task, claim, answer and adoption in one
  write in the flip's transaction, as PRD
  section 6's promotion paragraph says; it takes the promoter's `Resolver`
  for that, and the entries the caller verified the copy for, refusing
  any other open entry of the task with `ErrKeptWorkUnverified` and
  writing nothing.
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
- nothingToDeliverMigration's `nothing_to_deliver` is PRD section 8's
  nothing-to-deliver record, written once by
  `TransitionRunOnNothingToDeliver` in the transaction that moves the run to
  passed, held to the run row's submitted commit and to a checkpoint its
  history holds (`ErrNothingNotOfRun`), and sealed like `merge_record`; its
  seen mark kind is `nothing-to-deliver`.
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
