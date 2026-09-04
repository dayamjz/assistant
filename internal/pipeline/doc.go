// Package pipeline is the delivery gate expressed as a graph: the contract one
// stage implements, and the nine stages wired into internal/graph with their
// halt points, their fix loops, and the bounds on those loops. Its contract is
// PRD section 5; this comment restates the parts that are load-bearing so a
// reader of the code does not have to open the PRD to know what must stay
// true.
//
// This package defines a topology. It executes nothing: internal/graph owns
// the executor, the checkpoints, the halt mechanism, and all three bounds, and
// this package hands it a graph. It runs no agent, invokes no git, and opens
// no database. The nine stage implementations do that, behind the contract
// below, and each of them is written and tested separately from this one.
//
// # The order is fixed, and that is the mechanism rather than a rule
//
// The nine stages run in one order: intent, rebase, review, test, document,
// lint, push, pr, ci. PRD principle P2 makes that not configurable, because
// "it passed the gate" has to mean the same thing in every repository, and
// because the order is load-bearing in at least one place: review runs before
// test so that it reads the code the person wrote rather than code a fixer
// wrote.
//
// The order is not enforced by a check, and there is no list a caller passes.
// A Stages is a struct of nine named fields, one per stage, so ten stages,
// eight, and the nine in another order are all unsayable. The order itself
// lives in the stage table in stage.go, which is unexported: adding a stage
// means adding a row and a field, in this package, and nothing a repository
// configures can reach it. What a repository configures is what each stage
// runs and how many fix rounds it gets.
//
// The last two stages are named pr and ci here. PRD section 5 calls them "Pull
// request" and "Checks"; the short names are what appear in node names, state
// keys, and reports, and the round limit governing ci is
// config.FixRounds.Checks.
//
// # The stage contract
//
// An Implementation declares the state keys it reads, the state keys it
// writes, and a constructor for the body that does the work. A body reads
// through the declared keys, does its work, and returns a findings.Report plus
// the writes it wants. It returns an error only when it could not run: a stage
// that ran and found something wrong reports it, and what the report holds is
// what decides whether the run fixes, holds, or advances.
//
// The declarations are data rather than something a body reports about itself,
// so they are checked against the state schema before anything runs, and they
// bound the body rather than the node: a node also declares the keys this
// package's own adapter needs, and a body that read one of those would be
// reading outside its declaration, so the reader it is handed refuses them.
//
// Fixing is a separate contract. P4 keeps reviewing and fixing apart with
// separate memory, and that lives in the type split rather than in a rule
// callers follow: an Implementation cannot fix and a Fixer cannot report
// findings.
//
// The two halves are also constructed on different terms. A stage body is
// built fresh for every execution of the stage, so a stage that takes fix
// rounds gets a new body each round and this package hands it nothing the
// round before it held; what that round established has to be in declared
// state to survive. A fix body is built once per fix node per advance segment,
// so it spans the rounds of the one stage's loop it serves that fall within
// one segment. That is what lets a fixer keep a durable agent session while
// the review checking it starts cold.
//
// A loop can straddle a segment, so a fix body does not always span the whole
// of one. A round whose body errors, or a run interrupted mid-loop, leaves the
// latest checkpoint standing inside the loop still running, and the graph's
// Resume continues it in a new segment with a newly built fix body. That is
// the residual gap, and it is why agents.Fixer.Reference is persisted: the
// session outlives the Go value that opened it, and only because it is written
// down.
//
// The split is a narrowing, not P4 itself, and the difference matters. NewBody
// is a caller-supplied closure on both sides, so it may capture anything that
// outlives every round and every run, and nothing in this package refuses it.
// What the per-execution build removes is the easiest way a stage keeps a Go
// value between rounds. P4's load-bearing half is enforced downstream and
// typed, at agents.Runner.Run, which has no parameter a session could be named
// in, with agents.Fixer the only route to one.
//
// The sanitized summary in FixInput.Previous crosses from one fix round to the
// next round of the same stage. Nothing here routes it to a stage body: PRD
// section 5 has the re-review check the previous findings and the fix summary
// as claims, and a stage that wants them declares a read of its own FixKey and
// ReportKey, which the schema permits: it bounds what a declaration may write
// and never what it may read.
//
// # The state schema has one owner
//
// The key table in key.go is the whole schema. A key's kind, its merge rule,
// and who may write it are one row, per P14, and a stage that declares a read
// or a write the table does not hold fails to build. Which keys a stage may
// write is the same rows read again: a stage that declares a write of a key
// the table does not mark writable by a stage fails to build too. Stating it
// as the rule rather than as a list is deliberate, because a key added later
// is then covered without this comment changing.
//
// The schema does not depend on configuration: a run's state holds the same
// keys whatever the fix round limits are.
//
// The topology does depend on it. This package adds exactly one edge, the back
// edge from a stage's fix node to that stage's node, for each stage whose fix
// round limit is above zero, so whether a limit is zero decides the graph's
// edge count, and a checkpoint's traversal and fingerprint counters are sized
// by that count. internal/graph refuses a checkpoint whose counter vectors are
// not the length of its edge list, so a run checkpointed while one stage had
// rounds cannot be resumed once that stage's limit reads zero, or the other
// way round. It also refuses a traversal count already past the bound on the
// edge sitting at that index.
//
// That check is a length comparison and nothing more. internal/graph compares
// the length of the per-edge traversal and fingerprint vectors against its
// number of edges, and nothing compares edge identity. So a configuration
// change that alters the edge count is refused, and a change that preserves it
// is not.
//
// The gap that leaves is reachable. Lowering fix_rounds.review from 1 to 0
// while raising fix_rounds.lint from 0 to 1 removes one back edge and adds
// another, so the edge count is unchanged and a checkpoint standing at a node
// that still exists validates cleanly. The stages are wired in table order, so
// review's block losing an edge shifts every edge between review and lint down
// one index, and the persisted counters are then read against different edges
// than the ones that produced them, so a fix loop that has not finished can
// start from another edge's traversal count and its round limit fires early or
// late.
//
// Take the checkpoint written once document's hold is answered, which stands
// at lint with the traversal into lint already counted at one. The segment has
// to end on that checkpoint, either because lint's body returns an error and
// no further checkpoint is written or because the process is interrupted. On
// resume under the new configuration the counter vectors are still the same
// length, and validation admits the checkpoint because one is not greater than
// one. Lint runs, reports fix-eligible findings, and takes its entry edge,
// which under the new indices carries a count of zero. The fixer runs. The
// back edge, which under the new indices is the slot already holding one, then
// reads its bound as reached and parks the run rounds-exhausted.
//
// So the run parks with the fix applied and never re-reviewed: the bound stops
// the loop after the fix but before the re-run that would verify it, so the
// round is taken and never completed, leaving a commit the pipeline authored
// on the branch that no stage looked at, and a person's fork deciding what
// happens to it. Reaching this needs a configuration change between a
// checkpoint and a resume, which P7 makes reachable, because configuration is
// re-read from the default branch rather than carried forward from the run
// that checkpointed.
//
// The shift does not reach convergence, and the reasoning is short enough to
// check rather than take on trust. internal/graph writes and reads a
// fingerprint only on a back edge and skips the comparison when the slot is
// empty. The only back edges here are the fix nodes' returns, and wire emits
// that return first in a stage's block: six edges for a stage whose limit is
// above zero, and the five every stage has when it is zero, because the fix
// node's return is there only when there is a fixer. So a back-edge index is
// five times the stage's position plus the number of earlier stages taking
// rounds, rather than six times its position. At most five stages can take
// rounds, so an index that is a back edge under two different configurations
// must belong to the same stage: a fingerprint that is read is never another
// edge's, and a slot the new graph reads but the old one never wrote is empty
// and skipped.
//
// The claim withdrawn there was that a fingerprint read from the wrong slot
// could defeat convergence, and it is worth saying why that counts as a
// defect. Confessing a failure the mechanism cannot produce is the same
// defect as promising a protection it does not deliver: both are claims the
// code does not support. A disclosure that overclaims danger sends a reader
// chasing a bound that is not broken, and teaches them to discount the next
// one.
//
// The fix is not here. internal/graph would have to carry a topology identity
// in the checkpoint and check it on read; this package hands the graph a
// topology and never sees a checkpoint.
//
// The rows the table marks as run inputs are the ones no node may write: what
// the run was started with, including whether the intent was supplied. That
// last one is there for a reason worth stating, because it is semantic rather
// than defensive. That bit asserts that a person supplied the acceptance criteria,
// and no stage can make that true, so no stage should be able to say it. A
// stage that set it would have review check a diff against a guess while every
// downstream prompt framed the guess as requirements, which is exactly the
// distinction PRD section 5 draws between a supplied intent and an inferred
// one. What a stage may still write is the intent itself, because recording
// what it inferred is the intent stage's job; what it may not do is promote
// that inference to authoritative.
//
// NewState refuses a run claiming a supplied intent with none behind it. What
// the schema row adds is narrower than that refusal: no stage can assert the
// flag, in any run, whatever it was started with.
//
// The intent text is a gap and stays one. It is stage-writable on purpose, so
// a stage may overwrite or empty a supplied intent while the flag still stands
// beside it, and nothing in this package sees that happen. The key table has
// no way to say writable only while intent.supplied is false, so this is a gap
// to state rather than a row to add.
//
// # The fix loop and its three bounds
//
// A stage that returns fix-eligible findings, has rounds remaining, and is
// under budget runs a fixer and re-runs to verify. That is the one cycle in
// the whole design, and internal/graph requires every back edge to carry a
// bound at construction, so none of the three bounds can be left until a run
// starts:
//
//   - Per-stage rounds. Each stage's limit bounds the one cycle it sits on.
//     Both of that cycle's edges carry the limit, because the graph requires a
//     bound on the back edge, and within one configuration the edge that stops
//     a run is the one into the fixer, because that is where a round is
//     decided: a stage still reporting fix-eligible findings after its last
//     round parks in front of the fixer rather than taking a round nothing
//     would verify. Across a resume under changed limits the back edge can be
//     the one that stops it, which the counter-misattribution paragraph above
//     traces. It catches one stage oscillating between two wrong fixes.
//   - The run-wide step budget. It is carried to the executor and counts every
//     node execution however they are distributed. It catches several stages
//     each staying under their own limit while the run as a whole never
//     finishes.
//   - Convergence. internal/graph fingerprints state at every back edge, and a
//     round that leaves state identical ends the loop. It catches a fixer that
//     reports success each round without changing anything, which neither
//     counter would notice before exhausting itself.
//
// Five stages take automatic fix rounds: rebase, review, test, lint, and ci.
// They are exactly the five fields config.FixRounds declares, and the stage
// table is what says so. A limit of zero builds no fixer node at all, so every
// finding that stage reports goes to the person, which is what a limit of zero
// means.
//
// Review's default is one round over fix findings only. An ask finding never
// enters the loop: classify holds the stage for one whatever the limit allows,
// so its presence holds the stage rather than being fixed and re-reviewed by
// the session that prescribed the fix.
//
// # Halt points
//
// A stage that found something a person must decide routes to its hold node,
// which is a halt point. internal/graph stops before a halt point runs, so the
// node does not start, its inputs are already in state, and a resume replays
// nothing. There is no second pause mechanism here: P14 gives the question one
// owner and the graph already owns it.
//
// The hold's options are exactly the outcomes a person may give a held stage,
// so the hold node records the answer as the stage's outcome and translates
// nothing. The graph refuses an answer outside the options, clears the answer
// key on every checkpoint that stands at a halt point without running it,
// whether the run halted there for the decision or a bound parked it in front
// of one, and refuses a run whose initial state already holds an answer, so a
// stage never arrives at its hold already answered.
//
// # Two behaviours from PRD section 5 that are easy to miss
//
// A stage does not run when this run skips it or when nothing remains to
// change. Both are read by the stage node itself rather than routed around it,
// so a stage that does not run still records that it was skipped rather than
// leaving the question to whoever reads the state afterwards.
//
// The skip list is a run input. P2 lets a person skip a stage on purpose for
// one run and never lets a standing configuration skip one on their behalf,
// which is why Options has no field that can: the list is on Start.
//
// Once the rebase stage reports that nothing remains to change, every stage
// after it is skipped and the run completes. That is a success with the rest
// skipped, not a failure.
//
// # P3 lands in one place
//
// Every stage's report is normalized before it is recorded, so a finding with
// a missing, empty, or unrecognized action becomes ask and holds the stage.
// That is the findings package's rule applied once here rather than nine times
// in nine stages, and it applies to a report an implementation built by hand
// exactly as it applies to one parsed from an agent.
//
// The same adapter then validates what it normalized, so every stage returns a
// report and a report that does not validate fails the step. A stage author
// owes a summary, a description on every finding, a path on every evidence
// entry, and a risk that is a recognized word or left unstated, since
// normalizing does not resolve an unrecognized one.
//
// # What this package does not promise
//
// The intent stage never blocking a run is owed by the intent stage
// implementation, not by this topology. Every one of the nine stages is wired
// the same way, so intent routes to a hold node exactly as the other eight do,
// and an intent report carrying any finding that is not a note halts the run
// there waiting on a person. PRD section 5 says stage 1 never blocks a run;
// nothing here enforces that, and nothing here will.
//
// So the implementation written against this contract owes two things. It must
// never return a finding whose action is anything but note, including when it
// could not read its own output: P3 normalizes a missing, empty, or
// unrecognized action to ask, and an ask finding holds. And it owes a test
// proving both, the unparseable case included, because that is the one a stage
// falls into by accident rather than by choice.
//
// Not enforcing it here is deliberate. A row saying intent may not hold would
// have to do something with an ask finding intent reported, and the only thing
// left to do with it is drop it - which is the failure P3 exists to prevent,
// bought for tidiness. An unclassified finding failing closed to a person is
// worth more than a stage that is structurally unable to interrupt one.
//
// The fixer's agent session reference is not pipeline state, and the schema
// declares no key for it on purpose. agents.Fixer.Reference is persisted so a
// restarted service can resume the same session, and the service that owns
// runs carries it, alongside the run records PRD section 8 puts in
// internal/store.
//
// The reason is not layering taste. Graph state is what internal/graph
// fingerprints for the convergence bound, and a session reference that changes
// across a resume would make the state never repeat, so convergence would stop
// being able to fire while every run still looked green. A bound that cannot
// fire is worse than no bound, because it reads as a bound.
//
// A bound that stops a run parks it, and internal/graph parks a run rather
// than halting it for a decision: a parked run is resumed by forking it, not
// by answering it. PRD section 5's hold offers four actions, and only three of
// them are reachable here. Approve, skip, and cancel are hold answers. A
// requested fix round is not offered at all: it would be a second entry into
// the same bounded cycle, and the bound it would need could never fire before
// the bound on the round the loop already counts, which is a guard that exists
// and never runs. So a person who wants a round the automatic loop would not
// take does it by forking the run. That is a real gap against section 5's hold
// table, and closing it needs a bound section 10 does not declare.
//
// Convergence is computed over the whole state, which is internal/graph's
// definition and not this package's. Two things a round writes therefore
// defeat it even when the round changed nothing else, because the state is
// then not identical. A stage whose recorded report differs between two such
// rounds is one. The other is the fixer's own summary: the fix node writes it
// to the stage's fix key every round, and that key is declared, so it is in
// the fingerprint. A summary naming a round number or a count costs its Fixer
// this bound.
//
// Nothing here trims either one to make convergence fire more often: that
// would decide on a stage's behalf that a differently worded report says the
// same thing. The per-stage round limit and the run-wide budget still bound
// such a loop.
//
// A run a person cancelled completes, as far as internal/graph is concerned,
// because its cancel node is terminal and a terminal node is where a run
// completes. Cancelled is what tells the two ends of a completed run apart.
package pipeline
