// Package runs owns two things about a run: where it stands, and the one
// durable fixer session it keeps. PRD section 8 gives its runs module the run
// lifecycle; this package is that lifecycle and the session, and the section
// on what it does not do says where the rest of that module's list lives
// today.
//
// It executes nothing. internal/graph owns walking a topology, halting it, and
// the three bounds on its one loop; internal/pipeline owns the topology.
// Nothing here drives either, and this package imports neither, which is worth
// noticing rather than skipping past: the reason is below.
//
// # A run's status changes are anchored and tabulated
//
// A run is created pending, runs, and ends in exactly one of passed, failed,
// or terminated. Held is where it waits on a person.
//
// The moves between those are a table in lifecycle.go, and every exported
// method that changes a status is one of its rows. No row leads out
// of a terminal status, so a finished run's verdict is not something a later
// call revises, and whether a status is terminal is read off the table rather
// than listed a second time beside it.
//
// Every move is anchored to the status the caller expected to find, and
// store.TransitionRun reads and writes in one transaction, so two callers
// moving one run cannot both find it where they expected and both write. That
// is what makes the table a mechanism rather than a convention. A move to the
// status the run already holds writes nothing and is not an error, so a
// transition retried after a failure that had already committed reports the
// state it established.
//
// What that is not is an enforcement. store.TransitionRun is exported and
// takes the origin set from its caller, so nothing stops another package from
// naming a set of its own; the answer is the one internal/store and
// internal/vcs already give about ownership, which is to add a row here rather
// than a transition elsewhere.
//
// # The fixer session lives here, and where it must not live is the point
//
// config.SessionReuse promises one durable fixer session per run: every fix
// round of one run answered inside one agent conversation, across the segment
// boundaries internal/pipeline builds a new fix body at, and across a restart
// of the process. Something has to keep that promise, and the obvious place is
// the wrong one.
//
// internal/graph fingerprints the whole of a run's state at every back edge,
// and a round that leaves it identical is what ends a fix loop. That is one of
// the three bounds, and it is the one that catches a fixer reporting success
// every round without changing anything, which neither counter would notice
// before exhausting itself. A session reference in that state would change
// whenever the agent named a different conversation, so the state would never
// repeat, so convergence could never fire - and every test would stay green,
// because a bound that cannot fire looks exactly like a bound nothing needed.
// internal/safety shipped a guard that could never fire and passed its tests
// for it; this is the same defect one layer up.
//
// So the reference lives on the run's own record, in store.Run.FixerSession,
// where internal/graph never reads it. Not importing internal/graph or
// internal/pipeline is what makes that structural here rather than careful:
// there is no writer of graph state in this package to put it in.
//
// The bound stays demonstrable rather than argued.
// TestConvergenceFiresWithADurableSessionAndNotWithOneInGraphState runs the
// real loop twice with a real agent session whose reference changes every
// round: once with the reference where this package keeps it, which converges,
// and once with it written into graph state, which runs to its round limit
// instead. The second run is what makes the first one evidence.
//
// # What a Fixer crosses, and what it still does not
//
// A Fixer is one run's fixer role and there is one per run per service. It
// opens the adapter's session from what the run's record holds, and it writes
// back every reference a round reports before that round's result is handed to
// the caller, so there is no window in which a caller holds a fix round's
// result and a restart would resume something else.
//
// That covers both breaks. Within one process a new advance segment asks for
// the same run's Fixer and gets the same object, so the conversation is not
// even reopened. Across a restart the object is gone and the record is what is
// left, which is what it is for.
//
// One break is left and cannot be closed here. A reference exists only once
// the agent has answered, so a round interrupted before it returns leaves
// nothing to record and the next round opens a fresh conversation. The loss is
// bounded to one round's memory, and the round itself is replayed by whatever
// resumes the run.
//
// A round whose reference cannot be recorded fails, and the failure names why
// the reference could not be kept, together with the round's own error when
// the round itself failed. It carries no result, so a round that otherwise
// succeeded is readable from the agents.Recorder's invocation record rather
// than from what Apply returns; handing back a result whose session the run
// does not know about would let a caller read that round as successful. That
// is harder than internal/agents is on a round whose agent reported no
// reference, where the result stands because the loss is visible in the
// invocation record; here the thing that could not be written is the record,
// so nothing is left to make it visible with. The fix that round may already
// have made to the working copy stands, and Apply says so.
//
// # P4 is not loosened here, and could not be from here
//
// Reviewing and fixing are separate roles with separate memory. internal/agents
// keeps that in its types: Runner.Run has no parameter a session could be named
// in, agents.Fixer is the only route to memory that survives a round, and
// Fixer.Apply takes no purpose. Since assistant-adapter-capabilities that is
// stricter still: an adapter's declaration is checked against its type in both
// directions, and agents.OpenFixer reads the declaration before it looks at the
// type, so an adapter without resumable sessions cannot satisfy the fixer path
// at all.
//
// This package is built on that rather than around it. Fixer is an
// agents.Fixer: Apply takes an invocation and no purpose, so every invocation
// reachable through this package's session is a fix, and there is nothing here
// a session could be attached to anything else through. Widening it would have
// to be a change to internal/agents first.
//
// The refusal of a review shape at the fixer holds in both modes. With session
// reuse the adapter's own fixer makes it. Without, the round is an
// agents.Runner.Run with PurposeFix, which asks only Invocation.Validate, so
// this package asks Invocation.ValidateForFixer itself before the call. The
// rule is about the role and not about whether a round happens to keep memory,
// and a session-free mode that answered a review would be the strict stance
// relaxed in the one path nobody looks at.
//
// New refuses a service asked for session reuse against an adapter that has
// not declared resumable sessions, with the capability named, before any run
// exists. That is the early half of PRD section 8's rule; the half that cannot
// be forgotten is agents.OpenFixer's, which reads the adapter's declaration at
// the call. FixerRequires is the same answer in the shape a caller puts on
// pipeline.Fixer.Requires, so the pipeline's refusal and this one are read off
// one table rather than kept in step by hand.
//
// # What this package does not do
//
// It does not start a run, drive one, or decide what it does next. It records
// where one stands when it is told, and hands out the fixer role. Whatever
// drives the graph calls both.
//
// It does not open the database, write SQL, invoke git, or talk to a code
// host. It does not build a prompt: a fix round's invocation arrives whole
// from whoever wrote it, and this package adds a session to it and nothing
// else.
//
// It does not own three of the records PRD section 8 lists beside the run
// lifecycle in the same module row. Stage results, rounds, and holds are
// store.UpsertStageResult, store.AppendRound, and store.RegisterHold today,
// and a
// hold in particular is a record with rules of its own - keyed, idempotent to
// register, and closed only by an explicit resolution - which a run's status
// cannot answer for. Service.Hold records that a run is waiting and nothing
// about what is being decided.
//
// It does not make a run's checkpoints durable. internal/graph's
// CheckpointStore needs a run's whole checkpoint history with anchored writes
// and a fork, and internal/store's checkpoint record is one row per run
// rewritten in place, so nothing in this repository implements that interface
// against a database. A run therefore survives a process restart here as a
// record and not yet as a position, and closing that is not this package's to
// decide alone.
//
// # Residual gaps
//
// One Fixer per run is bounded by one Service. Two Services over one store
// would each hand out one and neither would see the other, which would give a
// run two conversations about the same change. What makes that not happen is
// PRD section 8's exclusive lock on a home, which is an arrangement of the
// program rather than anything this package establishes.
//
// A Fixer already handed out is not revoked when its run ends. Ending a run's
// agent work is the cancellation of the context its rounds run on, which is
// what internal/agents ties a process tree to; a status check per round would
// be a second and slower one that a round already in flight would be past.
// What holds unconditionally is that a role is never handed out for a finished
// run, because the status is read on every hand-out. When the service's own
// reference is dropped is weaker than that and depends on how the run ended: a
// run ended through this service loses its index entry at the move, and a run
// ended out of band loses it on the next hand-out attempt, which may never
// come.
//
// store.SetRunFixerSession is exported, so a caller that has the store can
// write a run's session reference without going through the Fixer that owns
// it. That is the same ownership-not-enforcement this package's status table
// has, and it is the shape internal/store's own comment describes: the answer
// to needing a write is an accessor there, and the answer to needing a session
// is a Fixer here.
package runs
