// Package tasks is PRD section 8's tasks module as far as this build takes
// it: intake, queueing, the P11 isolation assertion, the P12 cleanup
// decision and the stored report it verifies against, and promotion. Create
// records a task with the disposition PRD section 6 gives it at intake,
// Queued behind a blocker or Dispatched, and Release dispatches a queued
// task once its blocker has cleared. AssertIsolated is the pure half of
// P11, described below. Dispatching, cutting a copy and asserting it are
// internal/service's, which composes this package's assertion with
// internal/vcs; launching a worker is internal/session's Create over the
// proof made here, which internal/service's launch reaches with a proof it
// asserts again at launch, and nothing in this package does any of them.
// Cleaner.CleanUp and Cleaner.CleanUpRun are the cleanup decision over a
// task's copy and a run's copy, described below, and Cleaner.StoreReport
// stores an investigation's report with the holds it surfaced. Promote is
// PRD section 5's promotion, described below: it flips an investigation
// task to a delivery task in place and shapes the standing copy, and it is
// the one place this package writes into a copy.
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
// dispatch produces an Isolation and holds it in memory, its launch asserts
// the copy again and hands Create that fresh proof, and a worker started
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
// # The cleanup decision
//
// P12 says removing an isolated copy requires positive proof that its work
// is reachable from a remote branch, or its pull request merged with a head
// containing the work, or its content is already present in an up-to-date
// default branch, that anything inconclusive refuses, and that a refusal
// means stop and investigate rather than a flag. CleanUp is that decision
// over a task's copy and CleanUpRun over a run's, one decision with two
// removals: a task's copy is a clone whose work is everything its local
// references and HEAD reach, read through vcs.Inventory, and removing it is
// removing a directory, which is this package's own, behind the proof
// value; a run's copy is a linked worktree whose work is the commit HEAD
// stands on, removed through the non-discarding vcs.RemoveWorktree. The
// three proofs are proof.go's, each read fresh from the upstream the
// repository record names and never from the gate, each fetching the tip it
// compares against by identifier, and each recording the commits it
// examined, which is P6 applied to a removal; the content proof is
// internal/safety's Incorporation over internal/vcs's Incorporated, and the
// merged-pull-request proof reads the code host through internal/forge by
// the number the run's merge record names, holding the pull request's base
// to the default branch the upstream confirms and its head to the head the
// merge was recorded at. A fetch that fails, a provider that cannot answer,
// a state nobody recognizes and a comparison that cannot be computed are
// each an attempt the refusal reports and never a proof.
//
// The proof is Removal, a value whose field only the decision sets, which
// both removal functions require, on the terms Isolation sets for the
// launch: a caller can ask for a cleanup and cannot hand one a proof, and
// the request types carry no field that stands for one, which a test walks
// their types to hold. The record is the claim: store.TaskCleanup and
// store.RunCleanup are written with the proof before the removal and marked
// after, so a crash between the two leaves a record a second call completes,
// and a task's record is anchored to the shape the decision was made over.
// The pre-removal recheck is the inventory unchanged, and a task copy is
// examined only once its terminal probed Dead, because a running worker's
// whole life is otherwise the window between the proof and the removal.
//
// An investigation copy answers to PRD section 11 first: its report must be
// stored, through StoreReport, which parses it with findings.ParseReport so
// P3 applies and registers every ask finding as a hold in the transaction
// that stores the row, and every hold the store recorded with the report
// must have a row; an open hold does not block, because it survives in the
// store. It then skips the proofs only as scratch, when its inventory equals
// the snapshot its dispatch recorded and its record names no run and no
// pull request, on store.CleanupProofScratch; anything else holds it to the
// three like a delivery copy. A fix finding in the report is recorded as
// evidence and reaches no fixer, and a hold keeps the worker's words as
// evidence for a surface to translate rather than relay (P13).
//
// # Promotion flips the contract in place, protected first
//
// PRD section 5 says a report is evidence, not authorization, and that
// promotion is the explicit mechanism that flips an investigation task's
// contract in place, in the same terminal with the same loaded context, so
// delivery's protections start applying. Promote is that mechanism, and
// choosing to promote is the coordinator's judgment (PRD section 7): the
// branch, the repository and the intent all come from the request, and the
// stored report is carried only as a pointer to its row, because it is the
// worker's text and the intent stage reads supplied intent. The order is the
// protection: store.TransitionTaskShape writes the shape, the repository and
// the branch together with the "promoted" entry in one transaction, anchored
// to the event sequence the resolution it decided from had read, and only
// then is the copy written to, given the assistant remote at this home's
// gate and the task's branch at its current HEAD, and asserted on the
// delivery expectation as a dispatch would assert it. Whatever the
// investigation committed is therefore on the task's branch, from a base the
// delivery's intent is written against; the branch is created at HEAD and
// the start snapshot is left as the dispatch recorded it, because nothing
// reads a delivery task's snapshot and a second record of where the copy
// stood would have two owners. The source states are the closed table
// Promotable reads, and the branch is held to intake's rules and the floor:
// a branch no task has worked on that exists in the gate or has a run is a
// person's, and a branch a live task holds refuses with a *BranchHeldError
// naming the holder, so the person queues a delivery task behind it through
// Create rather than promoting onto it, since Task.BlockerTask is written
// once and a promotion cannot queue a task that is already running.
//
// The flip and the cleanup claim exclude each other: RecordTaskCleanup
// reads the shape in the transaction that claims the record, and
// TransitionTaskShape reads the record in the transaction that flips the
// shape, both on the store's one writer connection, so a promotion and a
// cleanup of one task have exactly one loser whichever ran first, and a
// test here holds the two at their claims and releases them together under
// the race detector. The copy is written to between two assertions: one
// before, so that a copy that is or reaches the person's primary checkout
// is refused without the remote being written into the primary's own
// configuration (P1), and one after, which is the assertion that decides. A
// refusal on either side is the task's "isolation-refused" entry, anchored
// to the promoted one, and a *PromotionRefusal; the task is then a delivery
// task that resolves Failed, which its history says, and the copy stands as
// found. A crash between the flip and the remote leaves a delivery task
// whose copy has no remote and a detached HEAD, which is safe: the copy
// pushes nowhere, and a cleanup holds it to P12's three proofs like any
// delivery copy.
//
// Promote has no caller in this build, on Create's terms: no verb and no
// local protocol method promotes a task, because a method a worker could
// call would let an investigation promote itself, which "a report is
// evidence, not authorization" forbids, and a coordinator-only method is a
// row PRD section 9's table does not have (#95 line E). internal/journey's
// coverage says so.
//
// # Every lifecycle move here is an appended event, anchored
//
// A task has no state column (internal/taskstate's doc says why), so a
// transition is an entry appended to its history and the state is whatever
// the resolver makes of the records afterwards. The entries this package
// appends after intake are "dispatched", appended with
// store.AppendTaskEventAfter, and "promoted", appended by
// store.TransitionTaskShape in the transaction that flips the shape, and
// each is anchored to the sequence the resolution it decided from had read,
// Resolution.Basis.EventSequence. Two releases that
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
// terminal in the lifecycle, so internal/service's relaunch re-runs this
// floor, through HoldsBranch, and refuses itself when a live task now holds
// the branch.
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
// that call; the cleanup record's writer is CleanUp, which internal/service
// calls after the same two finishes for the task the merge record landed and
// after a report is stored for an investigation task, and no call to Release
// follows it, because a task it would move is already cleared as Landed. A
// run a new push supersedes is the one finish that does not call it, because
// that push creates the branch's next run under the same exclusion and the
// branch never stops being live; internal/service's tasks.go says why.
// CleanUp has the callers internal/service's cleanup.go names and no verb or
// protocol method, on Create's terms.
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
//   - A task copy is cleaned up only once its terminal probed Dead, and the
//     probe is not wired into internal/service's reader (#91's last PR), so
//     the service passes taskstate.Unobserved and every task copy refuses
//     with ReasonTerminalUnobserved today. Closing the session through the
//     provider first is not done here either, because the service holds no
//     sessions on this base. The probe's wiring is what turns those calls
//     into removals, and nothing here changes when it lands.
//   - vcs.RemoveWorktreeDiscardingChanges is exported and reachable by any
//     importer, as is os.RemoveAll, so only this package calling either
//     behind a Removal is convention; no type stops another package from
//     removing a copy it never proved.
//   - A caller states the terminal's liveness, and one that states Dead of
//     a terminal it never probed is indistinguishable from one that did.
//   - Ignored files are invisible to vcs.Inventory and so to the dirty and
//     untracked refusals; a file git is configured to ignore goes with the
//     copy.
//   - The record claims the shape, and the removal follows the record: a
//     promotion that landed between the two would move a copy proven on an
//     investigation's terms out from under a delivery task. The promotion's
//     own transition is where that is refused, by reading the cleanup record
//     in the transaction that flips the shape, so the window is closed at
//     the store and not here.
//   - A promoted copy is a delivery copy from the flip on, and a cleanup
//     that reads the task after it decides over it as one: a promoted copy
//     holding nothing the default branch lacks passes the content proof, as
//     a fresh delivery copy does, so a promotion is not what keeps such a
//     copy standing; P12's proofs are.
//   - The copy side of a promotion is not repeated. A crash or a failed
//     write between the flip and the assertion leaves a delivery task whose
//     copy lacks the remote, the branch or both, which is safe and which the
//     next assertion of it refuses; a second Promote refuses on the shape,
//     and nothing here shapes the copy again. Repairing it is a person's.
//   - Promote asserts the environment the request hands in. The worker was
//     launched before the promotion and nothing here reads its environment
//     back, so a caller passing something other than what the worker runs
//     under is asserting that instead.
//   - The "promoted" entry is a note, so a promoted task whose newest entry
//     it is resolves Unknown at the event tier until the worker's next
//     entry, as a task does after any note; the process tier answers above
//     it once the probe is wired. The same rule keeps a task whose newest
//     entry is a worker's note out of Promotable until its next state entry.
//   - Nothing tells the running worker its contract changed but its own
//     history and the instructions file. Sending the new contract through
//     the session is #91's route, and nothing here uses it.
//   - The default branch is confirmed by commit, the upstream's HEAD
//     standing on the branch the record names; a second branch at the same
//     commit passes that confirmation.
//   - The references a proof fetches under refs/assistant/cleanup/ are
//     deleted best effort; one left in the gate's repository or in a kept
//     clone is read by nothing and overwritten by the next check.
//   - A task copy's refusal is returned and recorded nowhere, because the
//     surface that would show it is #110's; a run copy's is recorded on the
//     run's cleanup record. Who answers a task's hold is #91's surface.
//   - The evidence copy of a report is written after the row, so a crash
//     between the two leaves a stored report with no copy. The row is the
//     source and nothing reads the copy.
package tasks
