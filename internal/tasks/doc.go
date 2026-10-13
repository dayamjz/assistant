// Package tasks is PRD section 8's tasks module as far as this build takes
// it: intake, queueing, and the P11 isolation assertion. Create records a
// task with the disposition PRD section 6 gives it at intake, Queued behind a
// blocker or Dispatched, and Release dispatches a queued task once its
// blocker has cleared. AssertIsolated is the pure half of P11, described
// below. Dispatching, cutting a copy and asserting it are internal/service's,
// which composes this package's assertion with internal/vcs; launching a
// worker is internal/session's Create over the proof made here, which no
// service path reaches yet, and nothing in this package does any of them;
// promotion and cleanup proofs are later still.
//
// # The isolation assertion
//
// P11 says a worker does not launch unless the resolved path is a real
// isolated copy, distinct from the primary checkout, and that being a linked
// copy is not proof when the tool can be pointed at its own repository.
// Expect computes, from the task's record and the home, what its copy must
// be (Expectation) and the shell line a worker runs as the principle's second
// check, out of one call so the two cannot name different paths.
// AssertIsolated takes a vcs.Observation of the directory, the environment
// the launcher will use, and that Expectation, and returns an Isolation or a
// typed *IsolationError naming the comparison that failed. The comparisons
// are the ones isolation.go lists in order; every path comparison is by file
// identity and never by string, so a symbolic link into the primary and a
// case variant of it on a case-insensitive filesystem are both the primary.
// The remote set is exact, assistant at this home's gate for a delivery copy
// and none for an investigation copy, because any other route is a push the
// gate's hooks never see. The environment is refused when vcs.StripRedirecting
// would change it, so the list of what points git elsewhere stays that
// package's (P14); the observation itself is taken under internal/vcs's own
// environment and answers what the copy is, never what a worker would see.
//
// The assertion is the proof and not the gate. Isolation's unexported field
// is set only here, so the launch seam that takes one, internal/session's
// Create, cannot be handed a value nobody asserted: internal/service's
// dispatch produces an Isolation and holds it in memory for that seam, the
// wiring from the one to the other is a later change, and a worker started
// through internal/agents directly is still started where it is told, which
// Isolation's doc states. The second check is detection, obeyed by an
// obedient worker and ignored by any other, and Expect's doc says so.
// Refusal maps the comparison a refused assertion failed on to the closed
// token internal/taskstate gives the isolation-refused event, and is the one
// owner of that mapping.
//
// Until #91's worker set exists, "own branch, only through the gate" is
// instruction text and nothing enforces which ref a worker's push may move:
// the gate admits a push on what it is a push of and not on who made it, so
// a worker with the gate as its remote can move any branch the gate holds.
// The second check and the instructions are what a worker is told; this
// package and the gate enforce neither.
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
// live task on the branch are both waited on when both apply. Among tasks
// queued on one branch the earlier, by task row, holds the later, so one
// pass releases at most one of them onto the branch and none waits on a
// later one.
//
// A blocker clears when it resolves Failed, Reported, Landed or Complete.
// Ready never clears, because a Ready task's branch still carries an open
// pull request; what moves it past Ready is the merge record of a run
// attributed to it, which internal/taskstate resolves as Landed, and the
// cleanup record after that, which it resolves as Complete. Both are terminal
// for the task, so a successor dispatched onto the branch shares it with
// nothing live. Clears' doc states the consequence Failed carries: it is not
// terminal in the lifecycle, so a relaunch (#94) must re-run this floor and
// queue itself when a live task now holds the branch.
//
// # No caller in this build
//
// Create is a library entry. No verb, no local protocol method and no
// command creates a task: PRD section 9's table has no such verb, and adding
// one is a finding against the specification (#95 line A), not a row. The
// consequence for the end-to-end harness is recorded in internal/journey's
// coverage: no journey walk can create a task, so what the binary is driven
// through there is written through internal/store. Release does have a
// caller: internal/service calls it when a run finishes and when it opens.
// A merge record is written only at two of those finishes, internal/service's
// settle for a run whose checks stage read the merge and its checks-hold
// closure for one read at recovery or before a resume, and both call Release
// after the transaction that writes it, so a blocker Landed is observed by
// that call; the cleanup record has no writer in this build (#108) and so no
// call after it, and a task it would move is already cleared as Landed. A run a new push
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
//   - AssertIsolated proves the copy at the moment it was observed. A
//     worker may add a linked worktree to its clone, retarget its remote, or
//     be launched later into a directory that has since changed, and none of
//     that reaches an Isolation already returned; the second check is the
//     only thing that runs afterwards, and it is detection by a worker that
//     chooses to run it.
//   - The second check compares the remote URL as a string, after the same
//     file:// and trailing-separator normalization AssertIsolated applies,
//     and cannot compare by identity: a gate spelled through a symbolic link,
//     or with letter case or a volume written another way, passes the
//     assertion and fails the line.
//   - The gate comparison refuses a copy equal to or inside the gate
//     repository and not one containing it, because the home layout keeps
//     the gate under repos/ and the copy under tasks/ as siblings, so a copy
//     containing the gate is unrepresentable today. If that layout changes,
//     the primary rule, which refuses all three, is the stricter template.
package tasks
