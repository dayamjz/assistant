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
// The two halves are also constructed on different terms, and that asymmetry
// is the rest of P4 expressed as construction rather than as a rule callers
// follow. A stage body is built fresh for every execution of the stage, so a
// stage that takes fix rounds gets a new body each round and cannot carry
// anything from one round to the next in a Go value; what the round before
// established has to be in declared state to survive. A fix body is built once
// per advance segment and therefore does span the rounds within it, which is
// what lets a fixer keep a durable agent session while the review checking it
// starts cold. Only the fixer keeps a session across rounds.
//
// What the fixing role hands the reviewing role is the sanitized summary in
// FixInput.Previous, and that is the only thing crossing in that direction.
// The graph does not build the bodies, this package does, so the asymmetry
// holds for every stage rather than for the ones that remember to honour it.
//
// # The state schema has one owner
//
// The key table in key.go is the whole schema. A key's kind, its merge rule,
// and who may write it are one row, per P14, and a stage that declares a read
// or a write the table does not hold fails to build. A stage that declares a
// write of a key this package owns - a stage's outcome, its report, its hold
// answer, its fix summary - fails to build too, because those are facts this
// package reports and a second author would make them mean two things.
//
// The schema does not depend on configuration: a run's state holds the same
// keys whatever the fix round limits are, so a checkpoint written under one
// configuration is the same shape as one written under another.
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
//     bound on the back edge, and the edge that stops a run is the one into
//     the fixer, because that is where a round is decided: a stage still
//     reporting fix-eligible findings after its last round parks in front of
//     the fixer rather than taking a round nothing would verify. It catches
//     one stage oscillating between two wrong fixes.
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
// so its presence parks the stage rather than being fixed and re-reviewed by
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
// key whenever it parks at a halt point, and refuses a run whose initial state
// already holds one, so a stage never arrives at its hold already answered.
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
// definition and not this package's. A stage whose recorded report differs
// between two rounds that changed nothing else therefore defeats it, because
// the state is not identical. Nothing here trims a report to make convergence
// fire more often: that would decide on a stage's behalf that a differently
// worded report says the same thing. The per-stage round limit and the
// run-wide budget still bound such a loop.
//
// A run a person cancelled completes, as far as internal/graph is concerned,
// because its cancel node is terminal and a terminal node is where a run
// completes. Cancelled is what tells the two ends of a completed run apart.
package pipeline
