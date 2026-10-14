// Package session is the terminal session a worker runs in: a terminal
// session, not a conversation session. Nothing here opens, resumes, or
// carries the conversation memory internal/agents calls a session, and
// nothing here feeds P4's fixer session; the word is PRD section 8's module
// row, "terminal session provider abstraction: create, capture, send, probe,
// close", and that row is the whole of Provider.
//
// # What a provider is
//
// Provider has exactly the five operations and nothing else. There is no
// capability table here and no optional row: the five methods are the
// interface, and a provider that offers more does so through
// internal/agents' capability table, which is the one owner of what an
// adapter declares, when it adds its declarer and its probe there. Pipe is
// the reference provider PRD section 12's phase 1 names, and the only one in
// this build.
//
// Only internal/agents starts an agent process. Pipe starts nothing: Create
// asks the agents.Runner it was built over for a worker through
// agents.OpenWorker, which refuses an adapter that has not declared
// agents.CapabilityWorkerSessions, and wraps the agents.Worker that comes
// back. The process tree, the environment the worker runs in, the removal of
// the variables that would point git elsewhere, and the bound on retained
// output are all that package's, and this one adds no second builder for any
// of them.
//
// Which adapter answers is stated rather than implied. In this build only
// the scripted adapter, agents.ScriptedFactory, declares worker sessions, and
// the tests stand it up through internal/agents/standin's worker mode. No
// shipped adapter declares the capability, so nothing here can launch a real
// worker: whether the real claude binary runs usefully as a long-lived,
// stdin-driven process is #95 line K, and the Claude Code adapter's
// declaration is a change of its own, made when the invocation mode is known
// and a probe can check it. Everything this package establishes, it
// establishes against the stand-in.
//
// # Create takes a proof, not a path
//
// PRD principle P11: a worker does not launch unless the resolved path is a
// real isolated copy. Create takes a tasks.Isolation and refuses one that
// Proven does not report, with ErrUnproven, before it asks for anything; only
// tasks.AssertIsolated produces a proven value, so a path nobody asserted
// cannot be launched through this package. What Create does not do is
// re-derive the proof: it launches in Isolation.Copy as proven and compares
// nothing itself.
//
// # Capture is bytes and a digest over the output stream
//
// Capture returns the worker's retained output, bounded by the agents
// runner's output limit with what fell off the front counted, and a digest
// over the normalized output stream. It is an output stream and not a
// screen: a pipe-backed worker has no terminal to draw on, so PRD section 5's
// "terminal unchanged" is read here as "the normalized stream is unchanged"
// and never as screen diffing. The comparison stays in the supervisor; this
// package returns bytes and a digest and no verdict.
//
// The normalization is derived from what the stand-in's worker mode emits,
// which is newline-terminated lines of plain text on both streams,
// interleaved in arrival order, and from one shape of redraw a line-oriented
// process can put on a pipe, a carriage return that overwrites the line it
// is on. Normalize states the rules; what it deliberately does not normalize
// is listed there too, and the reason is the same each time: a rule for
// output nothing in this repository has been shown to produce would be
// designed against a guess. The real adapter's output format is not known
// here, and when it is, the rules are revisited against it rather than
// extended now.
//
// # Probe answers from what the provider can see
//
// Probe returns a taskstate.Liveness, which is that package's word and not a
// second vocabulary, with a typed Reason beside it. The rule is strict in one
// direction and the Reason table is the one place it is written down:
//
//   - Live is answered only for a session this provider created and still
//     holds, whose agent process has not exited. That handle is the one
//     Capture and Send reach the process through, so Live is positive
//     evidence a caller can act on, which is what P9 absorbs a wake on.
//   - Dead is answered only when something proves absence: the held
//     process has exited, no process has the reference's identifier, or the
//     process with that identifier started at a different time and is
//     therefore another process that was given the number.
//   - Everything else is Unobserved: a reference this provider holds no
//     handle for whose process is present, a reference from another home, a
//     reference that cannot be decoded, and every failure to read. A probe
//     error is never Dead.
//
// Live from a persisted identity alone is refused by construction. After a
// service restart the new provider holds no handle, so a survivor it can
// confirm exists is one it cannot capture from or send to, and reporting it
// Live would absorb wakes on a worker nobody can see, which is P9's named
// failure. Such a survivor reads Unobserved until it is gone, and then Dead.
//
// The process this provider holds is the agent itself. The agents runner
// starts the configured binary directly as the leader of its own process
// group, with no shell between, so the leader exiting is the agent exiting
// and there is no pane in which a shell could outlive it. A provider that
// launched through a shell would have to answer differently, and the tests
// model no such pane because agents.OpenWorker cannot produce one.
//
// # The identity is the attempt's session reference
//
// A Reference is the process identifier and the process's start time, with
// a marker for the home root and the task identifier, encoded as one opaque
// string. It is the attempt's session reference: the caller records it
// through store.SetAttemptSession, once, after Create returns, and nowhere
// else, and internal/service's launch is that caller. No file under the
// home holds it, and this package writes no record. The one record serves
// every reader: taskstate's Reader probes by that reference, and
// internal/service's terminal hookup, its Terminals over this provider,
// probes and captures by it, through Probe and Pipe.CaptureReference, which
// answer out of one held set, so the reference a probe answers Live for is
// the one a capture reads. A reference whose home marker is another home's
// probes Unobserved here, never Dead, so a colliding task identifier in a
// second home on the same machine cannot be relaunched over from this one.
//
// The start time is internal/agents' Identity, read through agents.Identify
// at Create and compared through Identity.Presence at Probe, so this package
// carries the one PID-reuse check that package owns rather than a second
// read of its own; which platform reads what is stated there. It is compared
// for equality with the value read at Create and carries no other meaning.
// On a platform that package has no read for, Create records zero, and a
// probe of a reference this provider does not hold answers Unobserved there,
// never Dead, because the identifier alone proves nothing.
//
// # The worker group set
//
// Workers is the set of live worker groups, keyed by the process group a
// worker leads and naming the task it was created for, and Pipe is its one
// producer: Create joins the group when the session is created and the
// membership ends when the worker's process does or Close ends the session.
// It is the authority input the worker-to-program paths derive a task from:
// a caller's kernel credentials name a process, the process has a group,
// and the set says whose worker that group is, so no caller states a task.
// Reading the group is the platform's question and internal/service's,
// which asks it the way its containment registry does; this package holds
// the set and answers only by group. It is deliberately not that registry,
// and Workers' doc says why: a worker registered as a stage would have every
// push it makes refused before admission.
//
// # Restart
//
// Nothing here arranges for the worker to end with the service. A survivor
// after a hard kill is an orphan the new service holds no handle to, and
// "terminal gone with the copy preserved" is the normal answer for it rather
// than an error. Two windows are stated rather than closed. The record-first
// order PRD section 8 gives a launch means the attempt row is written before
// the process starts and the reference is filled after Create returns, so a
// crash between the two leaves a process no record names. And an orphan
// whose record does name it is observable but not reachable. Only an
// identity-based sweep closes either, and that is #112's and not this
// package's.
//
// # Residual gaps
//
//   - The proof is as of its assertion, not of the launch. Create refuses an
//     unproven Isolation and does not re-derive a proven one, so a caller
//     holding a proof whose copy was retargeted since, by a remote added or
//     a link swapped under it, is launched into it. The one caller in this
//     build, internal/service's launch, re-runs vcs.Observe and
//     tasks.AssertIsolated at launch and hands Create the proof that read
//     produced rather than the one its dispatch holds, which narrows the
//     window on that path to the step between its assertion and the process
//     starting; nothing here enforces that of a caller, and nothing here
//     closes the step that remains.
//   - Nothing bounds how long an orphan goes on executing, and nothing here
//     ends one: Close ends the sessions this provider holds and no other.
//   - On Windows, Close delivers what agents' tree termination delivers
//     there and no more: a descendant that outlives a leader which exited
//     on its own is not ended, as internal/agents states for that platform.
//   - A reference is as of Create. A process that exits and whose
//     identifier is reused by another process started at the same tick on
//     Linux, or the same microsecond on macOS, would read as present; the
//     start time narrows the reuse to that coincidence and does not close it.
//   - The set is as current as the worker's done signal. Member answers
//     false for a group whose worker has exited before the leave has run,
//     and a group identifier the operating system reuses for a worker this
//     provider started replaces the old entry; one reused for a process this
//     provider did not start is not a member, so nothing is placed by a
//     number alone.
//   - The route walk over the handle types reads exported fields and the
//     results of exported methods, so what Pipe holds in its unexported
//     fields, the agents.Runner among them, is outside what it inspects, and
//     a value held behind an interface-typed field is invisible to it; the
//     test states both beside the walk.
package session
