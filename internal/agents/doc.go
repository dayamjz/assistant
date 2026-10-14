// Package agents is the seam between this product and a coding agent. PRD
// section 8 gives it adapters for each coding agent, fallback ordering,
// session reuse, and structured output, and its interface is running a prompt
// and getting back text or a validated report, with what the invocation cost.
//
// Everything that reaches an agent goes through Runner, Fixer, and Worker.
// Nothing else in the product starts an agent process.
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
// The split covers Purpose because Purpose is a parameter of one entry point.
// Shape is not: it rides on the Invocation both entry points take, so
// ShapeReview is a second way to say "this is a review" that the split does
// not reach on its own, and Invocation.Validate cannot close it because it is
// asked the same question on both sides and cannot see which one it is on.
// Invocation.ValidateForFixer is that one rule, and the fixer path asks it
// instead: a review shape reaching a session that outlives the round is
// refused with ErrReviewInFixerSession before any process starts. So a review
// stays out of the fixer's memory whether it is spelled as a purpose or as a
// shape, and a Shape added later belongs on one side of that line the same
// way an entry point does.
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
// SessionRunner and WorkerRunner are exported, so calling Fixer or Start on
// one is expressible without going through OpenFixer or OpenWorker, and a
// caller that built a Runner itself and asserts on it reaches the mechanism
// with no declaration read at all. The runners in internal/agents/standin and
// in this package's own tests are built that way. Resolve is what closes this
// for a run rather than the type, and only for a Runner that came out of
// Resolve.
//
// # A worker is the fixer side of the split, started once and driven
//
// PRD section 6 gives a task a worker: a long-lived agent in an isolated
// copy, driven over its terminal, that pushes to the gate the way a person
// does. The process is this package's to start, because nothing else starts
// an agent process, and the layer that owns a task wraps what this returns.
// CapabilityWorkerSessions names the mechanism, WorkerRunner carries it, and
// OpenWorker is the one route to it from a Runner, reading the declaration
// before the type on the terms OpenFixer states.
//
// It sits on the fixer side of P4 by its types rather than by a rule. Start
// takes no purpose, and WorkerSpec has no shape and no purpose, so a worker
// cannot be told it is a review in either of the two ways an Invocation can
// say so; Invocation.ValidateForFixer's refusal of a review shape is
// therefore not asked, because there is no shape to refuse. The handle a
// caller holds is a Worker, which is a process and nothing else: no Runner,
// no session, no Result, and internal/agents/route's walk finds no route out
// of it, with agents.Resolution and WorkerRunner itself, which embeds Runner,
// as the controls. The walk's blind spot is its own, a value held in an
// interface field, and Worker holds none.
//
// The environment is built by the one builder with one rule more. A worker
// runs git itself, in the copy it was started in, and a GIT_DIR it inherited
// from a hook or from the service would make every one of those invocations
// act on the other repository while the worker believes it is in its own,
// which is the "can be pointed at its own repository" P11 names. So Start
// removes the redirecting variables through vcs.StripRedirecting, which is
// the one owner of that list, after the spec's own Env has been applied, so a
// GIT_DIR a caller put in the spec is removed too. What that buys is exactly
// what the list buys internal/vcs and no more: the names on it, and not a
// variable a later git introduces until it is added there. It is not the
// isolation proof P11 asks for before a worker launches, and no test here
// claims that principle: this package starts a worker where it is told and
// asks nothing about the directory, so P11 stays declared unclaimed in
// internal/principles until that proof has an owner.
//
// The process group is the handle's to report and nobody's here to register.
// Which task a worker belongs to is not a fact this package holds, so Worker
// reports its group and whoever starts one keeps the set. One thing about
// that set is settled here rather than left open: it is not the containment
// registry internal/service keeps for validation stages. A worker's push is
// the one thing the gate exists to admit, and a worker registered as a stage
// would have every push refused before admission, so internal/service's
// wrapper around the resolved agent carries no worker arm and the handle is
// obtained from the agent it wraps; its own tests hold that.
//
// Four gaps are left open and named.
//
// A send its context cut off is not taken back. The bytes already in the
// pipe are the worker's, so the rest of that write follows them and every
// later send waits behind it, under its own context, until it drains or
// Close shuts the input, and a send waiting its turn behind another send
// waits under its own context too. Order is kept; what is lost is that a
// worker which never reads its input turns every later send into a wait,
// and nothing here ends that wait but the caller's own context or Close.
//
// No adapter this build ships declares the capability. The scripted adapter
// ScriptedFactory builds does, over the stand-in, so the conformance suite
// holds the mechanism to the declaration and a layer built on workers can
// run against something real; the Claude Code adapter's declaration is a
// change of its own, made when the invocation mode it runs as a worker in is
// known and a probe can check it. Until then nothing here launches a real
// worker.
//
// A worker produces no Record. The Recorder's unit is an invocation with a
// purpose, and a worker has neither: it is started once and what it costs
// is spent over a life this package does not see the end of. Nothing here
// accounts for it, and a consumer that needs the accounting is a finding
// against this package rather than a field to add quietly.
//
// Runner.Run's environment still passes a redirecting variable through. A
// stage launched from a git hook inherits one pointing at the gate, and the
// one builder applies the rule above to the worker entry only, because
// applying it to Run would change the environment every existing run's
// stages see. Whether a stage body is ever harmed by it is a question with
// its own test, not yet written, and TestTheWorkerEnvironmentCarriesNoRedirectingGitVariable
// reads the run path as its control, so the day it stops passing the
// variable through that test says so.
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
// command line, and the next section's Sweeper is that scan. No runner here
// is built with it, for the reason Sweep gives, so a stage invocation's
// detached descendant still outlives the invocation.
//
// # A sweep ends what runs in one copy, on Linux, and proves nothing elsewhere
//
// Sweeper ends the processes whose working directory resolves under one
// isolated copy of its home, and nothing else: the directory must be one
// home.CopyAt accepts, a task's copy or a run's, standing where the layout
// puts it and reached through no symbolic link, and a process is matched by
// the working directory the platform reports for it, resolved the same way.
// It reads no command line. Everything under the copy is in scope, a shell a
// person opened there included, and Sweeper's comment says so. Each process
// found is ended by itself, only after Identity.Presence verifies the
// identity the listing read, so a reused identifier is reported and not
// signalled.
//
// Linux is the one platform with a listing, read from /proc. This package
// uses no route to another process's working directory on macOS or Windows,
// none having been established here without cgo and exercised on their CI,
// so Sweep answers
// ErrSweepUnsupported there with the zero SweepResult, which is not Clear: a
// caller cannot read an unsupported sweep as one that found nothing. Sweeper's
// comment lists the residual gaps, among them a process that left the copy
// before the listing.
//
// A process whose working directory the listing is refused is not looked
// at, and it keeps the sweep from Clear unless one of two carve-outs sets
// it aside: it is on this process's parent chain, verified by identity, or
// it shares no user identifier with this process and refused a zero signal.
// A process of this process's own user off the parent chain always blocks.
// The first carve-out is what lets a service running as the same user as an
// init process whose working directory is refused to it clear at all; its
// residual, and the second's, is a set-aside process that did reach the
// copy, which the sweep does not see.
//
// # A process is named by its identity, and ended only after it is verified
//
// Identity is the one PID-reuse check in this module: a process identifier
// and the platform's reading of that process's start time, read by Identify
// and compared by Identity.Presence. internal/session embeds one in a
// terminal session's reference and internal/store's run process record
// carries one, and neither reads a start time of its own. Linux reads the
// process's stat entry, macOS the kernel's process table, Windows the
// process's creation time; another platform has no read, Identify answers
// ErrIdentityUnreadable there, and nothing that needs an identity can be
// verified. A process that has exited and not been reaped counts as not
// running wherever the platform lets that be seen. The check narrows reuse to
// a process given the same identifier and started within the reading's
// resolution, and does not close it.
//
// EndGroup is the one way to end, from outside the invocation that started
// it, a group this package started: internal/service's recovery ends a group
// an earlier service recorded and did not live to terminate. It signals
// nothing until Presence has verified the leader, and then only by group
// identifier, with the escalation an invocation applies to its own tree,
// and it matches no process on its command line, working directory or user. A reused identifier, a gone leader, and a read that failed are
// answered without a signal, each as its own GroupEnd or error. On unix it
// asks between steps whether anything of the group is still running rather
// than whether signal zero still reaches it, and on Linux it lists the
// group's members to count one that exited and waits on a parent that is not
// this process, which is what an orphan is wherever nothing reaps orphans.
// macOS and another unix count no such member: macOS's process-table reading
// by group did not identify one on CI and is not used, and another unix has
// no listing, so on both a group holding only such a member is reported
// survived or outlived rather than gone, which is the over-cautious answer.
// On Windows there is no group to signal, so what is ended is the tree under
// the verified leader, and a leader that is gone is answered GroupLeaderGone
// with nothing of its tree looked for. EndGroup's own comment states the
// residual gaps: reuse between the read and the signal, a descendant that
// left the group, and what Windows does not reach.
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
// "auto" to the catalog's auto-eligible adapters in its own order, and
// returns the first entry that yields a
// Runner along with everything it passed over and why. Resolving nothing is a
// refusal wrapping ErrNoAgent, which is what keeps a run from starting and
// reporting command-only validation as a pass.
//
// "auto" is limited to adapters whose factory declares AutoEligible. Only
// the Claude adapter declares it, because PRD section 12 makes Claude Code the
// only phase-one adapter; the others resolve when an entry names them. That is
// a scope decision, not a verification: the conformance suite drives a test
// helper binary for every adapter, nothing in this repository drives a real
// CLI, and so for no adapter, Claude included, does resolving on PATH show
// that the CLI emits the envelope its parser reads.
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
