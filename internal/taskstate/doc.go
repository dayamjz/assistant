// Package taskstate resolves what is true of a task now, and is the one writer
// of the record that says so.
//
// PRD section 6 gives a task two records that answer different questions: an
// append-only event log that says what happened, and a state record that says
// what is true now, "resolved deterministically, with the source of the answer
// attached". This package is the second. P8 is the reason the two stay apart:
// the log's last line is not an answer, because a worker handed off to the gate
// stops writing to its log, so its tail goes on saying whatever it said last.
//
// # Two functions, two verbs
//
// Resolve is pure. It takes Inputs and returns a Resolution, with no store, no
// context and no clock, and it is the only place the precedence is written
// down. Reader reads the stored answer and never computes. Settler is the one
// writer: it runs Resolve over a snapshot read inside the store transaction
// that stores the result. The PRD's description of this module as read-only and
// free of side effects is true of Resolve and of Reader. The record's existence
// is Settler's, and it is a different verb on purpose, so a reader of the code
// cannot take a write for a read.
//
// # The precedence
//
// See Resolve for the rungs. Source on every answer names the rung that gave
// it. Superseded lists the events that rung outranked: every decision-open
// event with no decision-closed event after it anywhere in the log, and the
// newest event that claims a state, when the answer is a run's or the
// process's and differs from what the event claimed. The whole log is read to
// do it; reading its tail alone would make the unclosed-decision question
// unanswerable. kind.go owns what each event kind means, and a kind it does not
// list never answers and is never an error.
//
// # When the record is settled
//
// Settling is explicit. The record changes when something calls Settler, and
// the service does at two points: when it opens (SettleAll), and after any run
// its run service creates or moves (runs.Options.OnMove, through SettleForRun).
// Append settles in the one transaction that appends an event; nothing in the
// service appends task events yet, so it has no caller there today. A settle that reaches the same answer moves no revision, so the
// revision counts changes of the answer.
//
// # What this does not guarantee
//
// The stored answer restates facts owned elsewhere: a run's status and a hold's
// openness. That is a second record of a fact, which PRD section 8 calls the
// P14 failure for the checkpoint, and this package takes it knowingly because
// the revision the event stream needs and the source attached to an answer have
// nowhere else to live. What it does to limit the cost is bounded:
//
//   - A writer that changes an input and does not settle leaves the record
//     stale. store.TransitionRun, store.SetTaskRun, store.SetTaskSession,
//     store.SetTaskPullRequest, store.RegisterHold and store.ResolveHold are
//     exported and none of them settles. runs.Options.OnMove covers the run
//     service only, and a failure inside it is logged, not returned. A stale
//     record is repaired by the next settle of that task, and Reader.Audit
//     reports it as a *DriftError; nothing runs Audit on a schedule, and
//     nothing blocks a read of a stale record.
//   - store.SettleTaskState is exported, so a package can write the record with
//     a decision of its own. The claim is only that the store's methods taking
//     a store.TaskDecision are CreateTask, SettleTaskState and
//     AppendTaskEventAndSettle, which a test pins by parameter type, and that
//     none takes a state as a plain argument; that no other path writes the
//     table rests on internal/store being the only package that writes SQL.
//   - The terminal probe, when one is given, runs before the transaction, so
//     the process input is as of the probe. This build wires none, so the
//     process rung is skipped in practice.
//   - A hold registered against the task itself (store.Hold.TaskID) with no run
//     is not read: PRD section 6's precedence has no rung for it.
//   - Run attribution is the task's repository and branch plus the creation
//     time: a run of that branch created at or after the task is its own,
//     whichever push produced it. A task re-dispatched on a branch with an old
//     failed run newer than the re-dispatch outranks the fresh work until a
//     newer run exists.
//   - Rows written before the resolver existed carry no Superseded or Basis;
//     Current.Unrecorded says so, Audit reports them as drift, and SettleAll on
//     open rewrites them.
package taskstate
