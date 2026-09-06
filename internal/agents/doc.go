// Package agents is the seam between this product and a coding agent. PRD
// section 8 gives it adapters for each coding agent, fallback ordering,
// session reuse, and structured output, and its interface is running a prompt
// and getting back text or a validated report, with what the invocation cost.
//
// Everything that reaches an agent goes through Runner and Fixer. Nothing else
// in the product starts an agent process.
//
// # P4 is arranged rather than remembered
//
// Reviewing and fixing are separate roles with separate memory. The failure
// that rule prevents is a reviewer resuming the session that prescribed the
// fix it is checking, which seats the prescriber as the certifier.
//
// The split is in the types rather than in a rule callers follow. Runner.Run
// takes a purpose and an Invocation, and neither has anywhere to put a
// session: Invocation has no session field, Run has no session parameter, and
// there is no option that gives one to a Run. Fixer is the only route to
// memory that survives a round, and Fixer.Apply takes no purpose, so every
// invocation reachable from a session is a fix. A review invocation therefore
// cannot carry a session, and adding a field or a parameter that would break
// that is a change to this package rather than a mistake at a call site.
//
// The same split answers the adapter that has no sessions at all. Runner has
// no Fixer method: only SessionRunner does, and an adapter that implements
// none offers nothing a caller could reach a session through. So the
// substitution PRD section 8 refuses, running review and fix in one session
// and calling it equivalent, is not a thing such an adapter can express here.
// What it has instead is Run with PurposeFix, which is a fix round that keeps
// no memory of the round before it.
//
// The same fact is visible in what is recorded. Record.Session says what an
// invocation did with the run's durable session, and Run passes nothing that
// could make it anything but SessionNone. That is what PRD section 13 asks
// for: a structural assertion over invocation records rather than a
// behavioral one.
//
// # An adapter declares what it supports, and undeclared means unavailable
//
// Adapters differ, and PRD section 8 makes that difference a declaration the
// program reads before it commits to a path rather than something a stage
// discovers halfway through one. Runner.Capabilities is that declaration, the
// set is the capability table in capability.go, and a path needing something
// absent from it is refused rather than served by a weaker path that would
// still be reported as a pass.
//
// Three things keep the declaration from being a comment.
//
// Resolve holds an adapter to it. A capability whose row carries a probe is
// checked against the Runner's type in both directions, so an adapter that
// declares a mechanism it does not carry and one that carries a mechanism it
// did not declare are both refused, and the whole resolution ends rather than
// passing over the entry. What a caller holds after Resolve therefore agrees
// with itself.
//
// OpenFixer reads it. It is the only route to a session this package offers a
// caller holding a Runner, and it consults the declaration before the type, so
// an adapter carrying the mechanism without declaring it is refused there
// rather than served. Together with Resolve that covers the Runner a run
// actually holds: one Resolve returned has already been held to its
// declaration in both directions, so there is no undeclared session mechanism
// on it for anything to reach.
//
// The tests read it. A conformance case chooses what to assert from what the
// adapter declared, so an adapter that declares nothing is tested as having
// nothing, and a declaration of a capability no row can probe is refused by
// the tests rather than accepted on trust.
//
// Two residual gaps are left, and both are worth stating plainly.
//
// A capability whose row has no probe, which is
// CapabilitySuppressProjectInstructions today, is a declaration this package
// takes at its word: nothing here can tell an adapter that suppresses a
// repository's instruction files from one that says it does. What holds today
// is narrower. No adapter this build ships declares it, and internal/pipeline
// refuses a run that asks for it when it builds the topology, which is where
// that refusal happens and not here: this package has nothing a suppression
// request could arrive in and so nothing to refuse at. The conformance test
// fails on the first adapter that declares it without a probe to hold it to.
//
// SessionRunner is exported, so calling Fixer on it is expressible without
// going through OpenFixer, and a caller that built a Runner itself and asserts
// on it reaches a session with no declaration read at all. The runners in
// internal/agents/standin and in this package's own tests are built that way.
// Resolve is what closes this for a run rather than the type, and only for a
// Runner that came out of Resolve.
//
// # Agent output is untrusted, and one package parses it
//
// An invocation asking for ShapeReport has the agent's final text read by
// internal/findings, which owns the finding vocabulary, the validation, and
// the fail-closed default that turns an unclassified finding into an ask.
// This package writes no second parser, per P14. Output that does not yield a
// valid report is an *InvocationError carrying FailureOutput and wrapping the
// refusal internal/findings raised, never a zero Report returned with a nil
// error.
//
// ShapeReview is the same arrangement with one more rule that also belongs to
// internal/findings: a review report carries the revision it read and the
// paths it actually read, and its findings are bound to them. The Demand that
// binding answers to travels on the Invocation, and Invocation.Validate
// requires it for ShapeReview and refuses it for every other shape, so review
// output cannot be read unbound and a demand cannot be attached to output
// nobody binds. A report of another revision, and one this package could not
// bind, are refused here exactly as an unreadable report is.
//
// # An invocation owns its process tree
//
// Every agent runs in a process group of its own, and the group is terminated
// on completion, on failure, and on cancellation, politely first and
// forcefully after a grace period. An invocation does not return while a
// process it started is still being terminated, so a caller that has a result
// or an error has already had everything this package can do about those
// processes done.
//
// What that covers differs by platform, and the difference is real rather than
// cosmetic. On unix the group is signalled and then polled for emptiness, so
// termination covers a child the agent left behind even when the agent itself
// exited cleanly, and nothing is signalled once the group is empty. On Windows
// there is no comparably cheap way to ask what is left in the group, so
// termination runs only while the agent is known to be running; a descendant
// that outlives an agent which exited on its own is not ended there.
//
// A descendant that puts itself in a new process group escapes group
// termination on either platform. Finding it again needs a scan matched on the
// isolated copy a process's working directory resolves under, never on its
// command line, and that scan needs to know which runs are still active, which
// is state this package does not own. Sweep is the seam it plugs into, and
// until a caller supplies one the gap is open.
//
// # What is recorded
//
// A Record carries purpose, agent, model, session use, timing, failure
// category, and token usage, which is the list PRD section 8 fixes for an
// agent invocation. Prompts, agent output, diffs, and credentials are not
// recorded, and that is a property of the type: Record has no field any of
// them could be written into. Text an agent wrote about its own failure
// travels on *InvocationError instead, where a caller can log it and must not
// copy it into a record.
//
// A record is written for every invocation that got past Invocation.Validate,
// including a cancelled one and one whose process could not be started at all.
// An invocation refused before that, for an unrecognized purpose or for an
// Invocation that cannot be run as written, produces no record.
//
// # Resolution happens before work starts
//
// Resolve takes the ordered list from the agent configuration key, expands
// "auto" to the catalog's own order, and returns the first entry that yields a
// Runner along with everything it passed over and why. Resolving nothing is a
// refusal wrapping ErrNoAgent, which is what keeps a run from starting and
// reporting command-only validation as a pass.
//
// Availability is one thing: the named executable resolves to a file this
// process may execute. An installation that resolves and then fails on its
// first invocation is not caught during resolution and surfaces as a failed
// invocation instead; ClaudeFactory.New says why proving more is not worth
// what it costs.
//
// # What this package does not do
//
// It does not write prompts. A stage owns its own prompt, and this package
// carries whatever it was handed.
//
// It does not sandbox an agent. An agent process can do whatever that agent
// allows in the directory it was pointed at, and containing that belongs to
// whatever chooses the directory and the agent's own permissions.
//
// It does not decide what an agent may be told. Suppressing a repository's own
// instruction files, which PRD section 10 makes a configuration key, needs a
// mechanism per adapter and is not implemented here. What is here is the name
// for it, CapabilitySuppressProjectInstructions, which no adapter declares.
// The refusal that keeps a run configured to suppress instructions from
// running with them still in force is internal/pipeline's, taken when it
// builds the topology; nothing here would refuse such a run, because nothing
// here is asked to suppress anything.
//
// It does not retry. PRD section 8 lists retries alongside fallback ordering
// for this module, and a run's own fix rounds and bounds are the only
// repetition today; adding a second, invisible one underneath them would put
// two owners on the question of how often something is attempted.
package agents
