// Package supervisor is the pure core of PRD section 5's supervision: the
// wake classifier P9 names and the stop guard's predicate P10 names, as two
// functions over typed inputs with no I/O behind them.
//
// Classify is section 5's flowchart as a function. Assess is the question
// the stop guard, the pull guard and arm verification all ask, "is a live
// supervisor holding this home while work is in flight", as a function over
// what a reader of the beacon, the instance file, the lock probe and the
// in-flight set found. Neither reads a clock of its own, a key, a file or a
// record: Classify takes every fact as an input the loop computed, and
// Assess takes now injected, because freshness is a wall-clock comparison
// across processes and the clock has to come from the caller.
//
// Nothing in this package performs I/O. Its direct imports are
// internal/taskstate, internal/home for its lock vocabulary, and the
// standard library; importing a type is not performing I/O, and home's
// LockReading is taken as an input so that vocabulary has one owner. The
// I/O edge is the subpackage feed: it reads the beacon file, the instance
// file, the lock probe and the database, and hands what it found to the
// predicates here. The loop that computes the inputs, writes the queue and
// the markers and the beacon lives in internal/service, in supervise.go; the
// durable tables and their accessors are internal/store's; the hook that
// runs Assess at turn end is #106's. This split is the one internal/graph
// draws: the engine has no git, no network and no agent in it, and the
// supervisor's core has no clock, no store and no file.
//
// # The beacon, and what derives from one configured interval
//
// The beacon's shape is this package's: EncodeBeacon lays it out as one
// fixed-size record, a length, a checksum, the instance, the incarnation
// epoch, a sequence and the write time, and DecodeBeacon reads one back or
// answers ErrBeaconUnreadable for anything that is not exactly one whole
// record, which is what a torn read comes back as. The one configured
// duration is config.KeySupervisionBeaconInterval; the staleness window is
// StalenessMultiple intervals and the first-beacon wait is
// FirstBeaconWaitMultiple intervals, derived through FreshnessFor and
// FirstBeaconWait rather than configured apart, and BeaconAheadTolerance is
// the one constant beside them. All three numbers are chosen, not measured.
//
// # The three readers of Assess
//
// GuardOf is the pull guard: Assess over an Observation, expecting the
// instance the instance file names and the beacon's own epoch, because a
// command outside the service holds no incarnation to expect. ArmOf is arm
// verification's decision over one look, settled or not, with the closed
// Arm vocabulary PRD section 5's row names and the beacon's sequence on the
// answer, which is what tells attached from a beacon written since. The stop
// hook will call Assess itself with the epoch it was told, and that is the
// one reader that compares epochs.
//
// # What is mechanism rather than rule
//
// Evidence is taskstate.Resolution.Progress and nothing else. EvidenceOf is
// the one translation and it reads that field alone, so "the classifier
// re-derives nothing from Basis.Process" is a property of the function and
// not a rule a caller keeps.
//
// A decision verb is a taskstate kind whose role is RoleDecisionOpen, read
// through taskstate.Lookup. Wake's fields are unexported and Of refuses the
// two event kinds, so an event wake exists only through Event, which derives
// the kind from that table; a caller cannot state that an event carries no
// decision verb, and a verb the table has no row for surfaces as
// unrecognized rather than being read as plain.
//
// Never absorbing on elapsed time alone is the shape of the table: no rule
// absorbs on a window. The declared wait reads its window to absorb, and
// rests on the worker's own declaration rather than on time passing; the
// terminal-ish wait that has surfaced once reads its window to absorb too,
// and rests on the surface already made, and so does a terminal gone that
// has surfaced, whose window is the escalation window measured from its
// last surface: a crashed worker that stays silent surfaces again once per
// window rather than once for good, PRD section 5's "a terminal-ish wait
// surfaces once" card. Whether that window has elapsed is an input; the
// loop computes it from a marker internal/store holds, and records each
// surface in the same transaction as the wake. The matrix test's oracle is
// written from the flowchart independently of the kind table, enumerates
// the kinds from the table so a row without an oracle fails, and runs
// counterfeit classifiers to show the matrix can fail: one that absorbs on
// an open window alone, one that absorbs a surfaced terminal gone however
// long it has been silent.
//
// A StopVerdict's Allowed is derived from its Reason, so a verdict cannot
// say allowed beside a reason that blocks. InFlight and Lock have their
// unknown as the zero value, so a reading nobody filled in blocks.
//
// Which tasks are in flight is the closed table in inflight.go, over
// taskstate.States() and the queue together, which gives PRD section 5's
// "work is in flight" one owner. A state is in flight when something other
// than a person moves next, including Unknown; a state whose only waiter is
// a person is not, and what keeps such a task under the guard until the
// person has been told is a queued wake row, queued until an
// acknowledgement closes it, a drain handing it out leaving it queued,
// which store.UnacknowledgedWakes counts and
// InFlightOf reads beside the states. The table is closed in
// both directions by its test, which derives the set from
// taskstate.States() and fails on a state with no row, so a state added
// later cannot land unplaced; Landed and Complete sit beside Ready. A state the table does not decide
// is read as unknown and never as none. Whether a task's state counts at all
// is its Regard, which RegardOf reads off its observation: an open stall
// hold or a housekeeping pass something moves is in flight whatever it
// resolves, and a retired pass nothing moves only while a wake about it is
// queued. feed.Observe takes the two reads, through store.OpenReadOnly.
//
// Which watched tasks have their terminal read is a second closed table, in
// terminal.go, on the same terms: every state but Reported, Landed and
// Complete, because PRD section 6 has a stored report outrank the terminal,
// so a worker that ends after filing is not a terminal gone, and the terminal
// tier outranks it the same way, so a worker that ends after its task landed
// or completed is not one either (the owner's decision on #320). A state the
// table does not decide is answered watched.
//
// The kind table carries one more column, Subordinate: whether a domain's
// subordinate coordinator can act on the kind with the commands PRD section
// 9 gives it, which is what section 8's wake row routes by. The
// progress-class kinds are marked and the decision-class kinds are not, and
// the matrix test holds the column to a recipient oracle written from that
// row. SubordinateActable reads the column out for internal/service, which
// passes it to internal/store as words. Two kinds are the subordinate's own,
// KindSubordinateGone, resurfacing once per window on KindTerminalGone's
// terms, and KindSubordinateEscalated, always actionable; both reach the
// first coordinator and never the subordinate, and the first is carried on
// the queue's second shape, internal/store's domain wake, since a
// subordinate is no task. InFlightOf counts those rows beside the per-task
// ones, so a subordinate gone nobody has acknowledged keeps the home in
// flight.
//
// # What the matrix establishes and what it does not
//
// The matrix is cited for P9 and it establishes the table only. Whether a
// held run, a stalled running record, a finished run or a dead terminal with
// an attributed run ever reach Classify as Progress is taskstate's
// derivation; whether the window and the surfaced flag are computed so that
// a window can elapse at all is the loop's. A loop that advanced the window
// start on every absorbed pass would absorb on elapsed time with this table
// unchanged, and nothing here can see it; internal/service's loop test under
// an injected clock is the other half of P9 and cites it there, and reads
// the window start out of the markers after each absorbed pass. The same
// holds for a terminal gone: the table spaces its surfaces one window apart
// only if the loop restarts the window at a surface and nowhere else, which
// the loop's own test of a dead terminal under the injected clock checks.
// P10 is not cited here
// at all: Assess's inputs are stated by its tests rather than produced by
// the real lock, beacon and instance file, and a test over stated values
// cannot show that the real mechanism produces the shapes it checks. feed's
// test cites it once, through the real files, a lock a second process holds
// and the read-only open.
//
// # Residual gaps
//
//   - KindCheckResult has no producer in this build. Its row says so, and
//     the matrix rows over it test a path nothing walks.
//   - A kind that closes a decision is read as an event with no decision
//     verb, on taskstate's terms that a close claims no state. Whether a
//     close should surface on its own is not decided here.
//   - The declared wait and no change absorb without evidence, by the
//     flowchart's own design: the first is the worker's statement, the
//     second a pass that observed everything and found it identical. What
//     keeps a failed observation out of KindNoChange is the loop's
//     classification of its own pass, which this package can only state.
//   - The repetition count is read for KindTerminalUnchanged alone, and
//     what it counts, consecutive surfaces on one digest, is the marker's
//     definition. An absorbed pass on the same digest neither adds nor
//     resets, which is a reading of PRD section 11's "repeated stall on the
//     same unchanged screen" and not the only one.
//   - Freshness blocks falsely across a suspend, a clock jump or a late
//     loop. That is the safe direction and it is not distinguished.
//   - Assess reads a held lock beside a matching fresh beacon as the service
//     that wrote the beacon holding the lock. A held lock alone may be
//     another probe's instant; internal/home states that gap, and the
//     reader's remedy, probing once more, is the reader's and not this
//     function's.
//   - GuardOf compares no epoch, by design stated on it: a loop replaced in
//     place under the same instance reads as live to the pull guard, which
//     is what it is. ArmOf compares none either. The watchdog in
//     internal/service does compare it, against the incarnation it started,
//     through IsFresh and its own reading of the identity.
//   - The beacon's checksum says a record was written whole and nothing
//     about who wrote it.
package supervisor
