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
// Classify and Assess perform no I/O. The package's direct imports are
// internal/taskstate, internal/home for its lock vocabulary, and the
// standard library; importing a type is not performing I/O, and home's
// LockReading is taken as an input so that vocabulary has one owner. The
// loop that computes the inputs, writes the queue and the
// markers and the beacon is #100's and lives in internal/service; the
// durable tables are #99's in internal/store; the beacon's shape, its reader
// and the in-flight table are #105's; the hook that runs Assess at turn end
// is #106's. This split is the one internal/graph draws: the engine has no
// git, no network and no agent in it, and the supervisor's core has no clock,
// no store and no provider.
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
// and rests on the surface already made. The matrix test's oracle is written
// from the flowchart independently of the kind table, enumerates the kinds
// from the table so a row without an oracle fails, and runs two
// counterfeit classifiers to show the matrix can fail: one that absorbs on an
// open window alone, one that absorbs a terminal gone after its first
// surface.
//
// A StopVerdict's Allowed is derived from its Reason, so a verdict cannot
// say allowed beside a reason that blocks. InFlight and Lock have their
// unknown as the zero value, so a reading nobody filled in blocks.
//
// # What the matrix establishes and what it does not
//
// The matrix is cited for P9 and it establishes the table only. Whether a
// held run, a stalled running record, a finished run or a dead terminal with
// an attributed run ever reach Classify as Progress is taskstate's
// derivation; whether the window and the surfaced flag are computed so that
// a window can elapse at all is the loop's. A loop that advanced the window
// start on every absorbed pass would absorb on elapsed time with this table
// unchanged, and nothing here can see it; #100's loop test under an injected
// clock is the other half of P9 and cites it there. P10 is not cited here
// at all: Assess's inputs are stated by its tests rather than produced by
// the real lock, beacon and instance file, and a test over stated values
// cannot show that the real mechanism produces the shapes it checks. #105
// cites it once, through the real files.
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
package supervisor
