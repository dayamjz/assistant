// Package tasks is PRD section 8's tasks module as far as this build takes
// it: intake and queueing. Create records a task with the disposition PRD
// section 6 gives it at intake, Queued behind a blocker or Dispatched, and
// Release dispatches a queued task once its blocker has cleared. Dispatch,
// cutting a copy, the P11 isolation assertion and launching a worker are
// #98 and #104, and nothing here does any of them; promotion and cleanup
// proofs are later still.
//
// # Every lifecycle move here is an appended event, anchored
//
// A task has no state column (internal/taskstate's doc says why), so a
// transition is an entry appended to its history and the state is whatever
// the resolver makes of the records afterwards. The one entry this package
// appends after intake is "dispatched", and it is appended with
// store.AppendTaskEventAfter anchored to the sequence the resolution it
// decided from had read, Resolution.Basis.EventSequence. Two releases that
// read the same tip therefore append one entry between them, and the other is
// refused with a *store.TaskMovedError it reports rather than retries (P6).
// Intake's own disposition is written by store.CreateTask in one transaction
// with the task row, anchored to the newest task row the intake saw on the
// branch, so a task is never recorded without its disposition and an intake
// cannot be overtaken between its read and its write.
//
// The anchor covers the history and nothing else. A resolution is also a
// function of runs, holds and a terminal probe, and those move without an
// entry, so Release appends only past a resolution the event tier decided and
// refuses, with an *OutrankedError, one whose queued claim a higher tier
// overrode. It learns that from what the resolver reported as superseded, not
// from the history's tail (P8); nothing in this package reads an event to
// decide what is true.
//
// # What one owner each fact has
//
// The identifier is internal/home's: NewTaskID mints it and ValidTaskID is
// the one rule, applied where the record is first written and where every
// path under tasks/ is composed. The shape is store.TaskShape, decoded at
// this package's edge and refused outside the set. The state is
// internal/taskstate's, read through a Reader; the live set and the clear
// condition here are predicates over its State and never a reading of a
// record. The copy path is home.TaskCopy, recorded at intake as a
// reservation. The per-branch exclusion is internal/service's branch gate,
// handed in as Exclude, and whether a branch is in the gate is read through
// BranchInGate, which internal/service answers from the gate repository;
// this package opens no repository and invokes no git.
//
// # The floor and the clear condition
//
// Which work overlaps is the coordinator's judgment (PRD section 7), and
// Create honours a claim of overlap as given. The one mechanical floor is
// that a delivery task on a live task's repository and branch is queued
// behind it whatever the claim says, because two live tasks on one branch
// share their runs (internal/taskstate's attribution gap) and neither's state
// could then be trusted. Live is any state Clears does not, so Unknown is
// live: a floor that read "the resolver declined to answer" as "cleared"
// would dispatch onto a branch whose worker may still be there. A queued
// task's Created and Outcome both carry the blocker's state, so a task stuck
// behind an Unknown blocker is visible to the coordinator rather than merely
// waiting. The record names one blocker, the claimed task while it is live
// and otherwise the floor's, and Release re-runs the floor before it
// dispatches, so a task is dispatched past neither: a claim of overlap and a
// live task on the branch are both waited on when both apply.
//
// A blocker clears when it resolves Failed or Reported. Ready never clears,
// because a Ready task's branch still carries an open pull request. Landed
// and Complete are the states that would clear a blocker past Ready, and
// neither is resolved in this build: #107 derives Landed from the merge
// record PR #113's merge_record holds and Complete from the cleanup record,
// so until then a blocker that reaches Ready holds its successors, and a
// merge record on its run changes nothing here. Clears' doc states both
// consequences, including the one Failed carries: it is not terminal in the
// lifecycle, so a relaunch (#94) must re-run this floor and queue itself when
// a live task now holds the branch.
//
// # No caller in this build
//
// Create is a library entry. No verb, no local protocol method and no
// command creates a task: PRD section 9's table has no such verb, and adding
// one is a finding against the specification (#95 line A), not a row. The
// consequence for the end-to-end harness is recorded in internal/journey's
// coverage: no journey walk can create a task, so what the binary is driven
// through there is written through internal/store. Release does have a
// caller: internal/service calls it when a run finishes and when it opens,
// which are two of the three moments the design names; the third, when a
// merge record or a cleanup record is written, is #107's and #108's, since
// nothing here resolves what those records mean yet. A run a new push
// supersedes is the one finish that does not call it, because that push
// creates the branch's next run under the same exclusion and the branch
// never stops being live; internal/service's tasks.go says why.
//
// # Residual gaps
//
//   - A person's own push on a task's branch is not a task and is outside
//     the floor: the refusal of a branch that exists in the gate or has a run
//     is asked only about a branch no task names, because a task's branch
//     acquires both by design, and nothing here tells a person's push on it
//     from the task's.
//   - The instructions file is written after the record, so a crash between
//     the two leaves a recorded task with no instructions. The reverse
//     cannot happen, and the dispatch is what finds the gap.
//   - The exclusion is one service's, as internal/service's own doc says of
//     its branch gate. Two services over one store would not see each
//     other's holds; the store's anchor then refuses one of two concurrent
//     intakes rather than queueing it, which is a refusal to read again on
//     and not a lost write.
//   - Release reads every task on every call. That is the resolver's one
//     consistent read and it is what keeps a queued task and its blocker as
//     of one moment; at the fleet sizes PRD section 12 contemplates it is
//     cheap, and nothing here has measured it past them.
//   - Which run belongs to a task is internal/store's attribution, and the
//     floor exists because that attribution cannot tell two live tasks on
//     one branch apart. The floor keeps the arrangement out of this
//     package's own intakes and nothing else: a task given the branch some
//     other way is outside it.
package tasks
