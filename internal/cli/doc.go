// Package cli is the command surface: the verbs PRD section 9 specifies, the
// flags that parameterize them, the two renderings of one answer, the
// subcommands a gate's hooks invoke, the host verb's turn-end hook and its
// installer, and the coordinator's client.
//
// A person at a terminal and an agent driving programmatically are both
// first-class users of the same commands. They share the same decisions, the
// same options, and the same authority; only the rendering differs. So there
// is one verb table here, every verb produces one of internal/machine's
// shapes, and --json decides whether that shape is written as a document or
// read out in prose. There is no second family of commands for an agent, and
// no verb one surface has that the other does not.
//
// # The verb table is the PRD's, and nothing else is in it
//
// verbs in cli.go is the whole of what a caller drives. Every row is a command
// PRD section 9's table names, and a command that section does not name is not
// here: a verb this product needs and that section does not describe is a
// finding to raise against the specification, not a row to add quietly. The
// three things this binary answers to that are not in that table are the
// gate hook interface, the host hook interface and the coordinator's client,
// which the sections below are about; section 9 names all three outside its
// table. The first two are not a precedent for the third, which the owner
// decided on #227, and the three are not a precedent for a fourth.
//
// Six things the section leaves to a verb's parameters are flags or arguments
// rather than verbs.
//
// Answering a held run is --answer on the command that attaches to it, and
// ending one is --cancel on the same command. Attaching is where a person is
// shown the decision, and an agent driving the same gate needs the same
// decision with the same options and no terminal to give it in. The
// specification's table names no command for either, which is a gap against
// section 9 worth raising rather than a licence to add a verb. A hold a task
// raised with no run behind it, an investigation's or a task's at its
// relaunch bound, is answered by the same flag given --task, which section 9
// says in as many words: answering takes a task as well as a run, and no
// verb is added for it. Given a task with no hold of its own open, the same
// flag answers the hold on the task's current run, which section 9 says too,
// so a run a task produced is answered without a checkout of its branch.
//
// Reading one run or one task in full is an argument to the command that lists
// them, because that section gives runs one row and tasks one row.
//
// Catching up is the read assistant watch starts with, and its two flags are
// the ones section 9's catching up names: --once prints the read and exits,
// and --mark-seen marks seen exactly the landed records that read showed.
// The section says no verb is added for it and status does not carry it, so
// neither does this surface; answering a decision the read lists is the
// --answer above, and the read names it rather than offering a second way.
//
// The usage view is --usage on runs, which is where section 9 puts it, taking
// a window as --usage=WINDOW (24h, 90m, 7d) and the seven-day default alone.
// It reads the agent invocation record of the whole home, so it names no run
// and takes no --limit, and a window that does not parse is incorrect usage.
//
// Running the service in the foreground is --foreground on service start,
// because the process that serves and the command that launches it are one
// binary.
//
// One thing the protocol serves has no command at all: returning a stage's
// result, which internal/ipc leaves open to a caller contained by an active
// validation stage. Section 9's table names no command for it, and no agent a
// stage launches would need one: the one body that launches an agent, the
// review stage's, reads the report back off the invocation itself, and no run
// in this build carries that body to a launch besides. So none is invented
// here.
//
// # The gate hook verbs are a second table on purpose
//
// gateHooks in gate.go is the other one, and it holds "assistant gate admit",
// "assistant gate notify" and "assistant gate receive". They are not commands
// PRD section 9's table names and they are not candidates for it: they are the
// interface internal/gate requires of this command surface, invoked by the
// hooks it installs in a gate repository and by the receive command it writes
// into a working copy's assistant remote, and never typed. A gate whose hooks call a
// command that is not served installs executable files that fail, which is a
// push refused before anything starts and every other verb answering about a
// product nobody can reach.
//
// They are two tables rather than one row added to the first, because the
// first is a claim - that the surface is the specification's and nothing else
// - and a row added there would quietly weaken it. They are in the usage text
// under a heading saying what they are for, because a command this binary
// answers to and does not mention is worse than either.
//
// Neither decides anything about the push, on the same terms as everything
// else here. The reference update lines come off standard input and are read
// by internal/gate, which owns the hook protocol; which working copy a gate
// belongs to and whether the push may proceed are the service's answers.
// Admission's exit status is what git reads, so everything that is not a
// definite admission - a refusal, a failure, a service that did not answer -
// is a non-zero status and a rejected push.
//
// # The host hook verb is the second
//
// hostHooks in host.go holds "assistant host stop", which the coordinator's
// host runs from the hook it fires at turn end. It is PRD section 5's stop
// guard as the host sees it: P10 says a coordinator about to stop while work
// is under way and no healthy supervisor is the service holding this home's
// lock is blocked from stopping until its host supplies a re-entry signal,
// and this command is where that verdict becomes an exit status the host
// reads. It is dispatched beside the table for the gate verb's reason, and
// PRD section 9 says the two together are not a precedent for a third: each
// is the interface a hook this program installed or asked for requires. The
// coordinator's client below is a third because the owner decided it, not
// because these two allowed it.
//
// It decides nothing of its own. The verdict is internal/supervisor's
// Assess, fed by internal/supervisor/feed's reads of the beacon file, the
// instance file, the lock probe and the in-flight set through the read-only
// open, under the operator's own supervision_beacon_interval; the same guard
// the status command computes with the service down, and not a second
// predicate. It consults no socket, because the service is what may be dead.
// Everything that is not a definite answer blocks: a dead service, a stale
// beacon under a live one, a file or a database that cannot be read, and an
// operator file that does not parse.
//
// The re-entry signal arrives one of two ways, and either is the signal:
// stop_hook_active true in the JSON the host writes to the hook's standard
// input, which is how the host the installer configures documents it, and
// HostReentryVar in the environment, for a host that bridges its own
// protocol by setting it. hostReentryField names where the field is
// documented and holds the reading to one direction: input that is absent,
// empty or an object without the member is no signal, and input that is
// there and cannot be read - not one JSON object, the member twice or as
// another type, past the size bound, or still open after the wait - is no
// signal either, with the answer saying why, so unreadable input blocks and
// never allows. The read is bounded in size and time so a hook never holds a
// turn on its input. What consumes the signal is the host's own re-entered
// turn, since this process stores nothing and judges each invocation from
// what it was given. With the signal the turn may end and the message says
// why; without it a turn with work under way unwatched is blocked, and PRD
// section 5 says that is the only safe reading.
//
// The host's protocol is this package's adapter and not internal/machine's
// shape. machine.HostStop is the verdict and the message, and
// machine.HostStopOf is the one translation to it; hostExit maps that onto
// the host's convention, success when the turn may end and hostBlockCode
// with the message on standard error when it may not, so every answer maps
// to exactly one status and the message reaches the coordinator in the
// vocabulary P13 holds the phrase table to.
//
// # Installing the hook is a person's consent
//
// hostHooks also holds "assistant host install", the installer PRD section
// 6's stop guard row asks for, and it is the one row there a person types. A
// change to the host's configuration is one a push never consented to (P1),
// so by default it prints the exact line the host's settings need and the
// file it belongs in and writes nothing; only --write merges the line into
// that file, keeping every other setting, and a file holding this program's
// stop guard under another command line, or one it cannot read as settings,
// is refused as a typed result with the file left as it was. The host is
// Claude Code, and the settings shape is what that host documents about
// itself; hostInstall names the source and its three gaps. The line sets no
// re-entry signal of its own, because that host documents one on the hook's
// standard input and stop reads it there. PRD section 9's row for this verb
// says a person never types it, which predates the installer, so that is a
// finding against the specification rather than a precedent for typed
// subcommands here.
//
// Three gaps are deliberate and written down rather than rounded off. The
// block status coincides with the usage status, so a host that runs a
// command line this surface refuses reads a block; that is the safe
// direction, and hostBlockCode says so. The command cannot place its caller:
// a worker ending the coordinator's turn would be deciding for the
// coordinator, but the worker group set is the service's and is reached over
// the socket by peer credentials, and a process with no socket has no peer
// to place, so the command answers the same whoever invokes it and refuses
// nobody. And the read of the host's input is bounded, so a re-entered turn
// whose event is larger than hostInputLimit, or whose input stays open past
// hostInputWait, is read as no signal and blocked again; the bounds are
// chosen, not measured against a host.
//
// # The coordinator's client is the third, by decision
//
// coordinatorActions in coordinator.go holds "assistant coordinator" and its
// subcommands, one for each of the coordinator's service methods that has
// no command in the table: create, message, stop, relaunch and promote a
// task, list, wait on and acknowledge the wake queue, and verify
// supervision. The
// owner decided on #227 to keep PRD section 9's table as it is and ship this
// client beside it, as the gate and host verbs are, and section 9 says so
// beside the table. It is one verb so the door beside the table is one door.
// Listing and reading fleet work are not in it, because the table serves
// both as assistant tasks, and neither is answering a task's hold, which is
// --answer given --task; task.interrupt is not in it, because the service
// refuses it on every call.
//
// It decides nothing. Every subcommand is one call, the request is
// internal/machine's shape for the method, and what the service refuses
// arrives typed: a caller the service places inside a task's worker group
// is refused with ipc.ErrWorker, decided from the socket's credentials, and
// nothing here holds a second copy of that rule. The command line is
// checked before the service is asked: operands, a flag and its -file
// sibling given together, a file that cannot be read, wake identifiers
// that are not numbers, and a wait bound outside
// machine.WakeWaitRequest.Bounds' range are incorrect usage. await is
// the one subcommand that blocks: it is one request that the service
// answers when the queue holds a queued wake or its bound elapses, so the
// coordinator can block a turn on it instead of polling, and a service that
// stops under it ends the call as an operational failure that the caller
// re-arms. The refusal rests on the
// service's placement of its caller, and coordinatorVerbName names the gap
// in that placement: a parent chain the kernel rewrote.
//
// The subcommand comes first after the verb, for the gate verb's reason:
// several subcommands take flags with values, and a scan for the subcommand
// would read a value as one.
//
// # Which verbs need the service, and which cannot
//
// A verb that acts on a run or on fleet state is a call to the service, which
// owns the home's lock and every run that is executing. A verb that acts on
// the working copy in front of you is done in this process against the home's
// database directly: creating a gate, removing one, and reporting whether a
// run could start are all things a person asks precisely when the service is
// not running, and a doctor that needs the thing it is diagnosing is no doctor.
//
// # Exit codes and where output goes
//
// Three codes, per PRD section 9, and internal/machine owns them: success and
// normal decision points, operational failure, and incorrect usage. The host
// hook's block status is the one exit this package maps itself, because it
// is the host's protocol rather than section 9's; see hostBlockCode. Which of
// the two failure codes a failure ends with is decided once, in failureExit:
// a command line refused here and a request the service answered
// invalid-request are both incorrect usage, whatever method answered, as is
// init's refusal when the working copy holds no default branch to read, or
// no origin to take the upstream from and no --upstream naming one, and
// every other failure is operational. A run that
// stopped to ask something is a success, because the answer goes back through
// the same surface and an agent that read a decision as a failure would stop
// driving exactly when it should carry on. A run that is still advancing is a
// success for the same reason, and PRD section 9 gives it an outcome of its
// own rather than one of the four that say a run is finished with. An answer
// to a task's hold exits with the operational failure exactly when it was a
// retry that began no work, machine.AnswerEffect.RetryNotBegun, and a retry
// that began an attempt, by relaunch or by dispatch, is a success. Arm
// verification through the coordinator's client exits with the operational
// failure when it could not establish a live loop, so a coordinator's
// session start reads the verdict from the code.
//
// A verb's answer goes to standard output as a document under --json, and
// progress goes to standard error, so a caller redirecting standard output
// gets documents from the verbs and nothing else. A verb that answers nothing
// writes nothing, so a consumer reading one document per line is never handed
// one that decodes to nothing.
//
// Asking for the version or for the help is answered rather than refused: both
// go to standard output and exit successfully, because incorrect usage is what
// the third code means and neither of those is that. Both have a document
// shape, machine.Version and machine.Help, and write it when --json was read
// before them.
//
// That last clause is a gap and not a nicety, so it is written down rather
// than rounded off: "assistant --json --version" writes a document, and
// "assistant --version --json" writes the plain line, because the scan of the
// flags that come before a verb returns at --version, -h or --help the moment
// it recognizes one and never reaches a --json behind it. The same is true of
// --home there. So the one-document-per-invocation contract holds for every
// verb and for these two only in that order. Closing it means reworking that
// scan, which is its own task and deliberately not done here; nothing in this
// package works around it in the meantime.
//
// --json and --home apply to every verb and are accepted before it or after
// it. They are declared on each verb's own flag set as well as read ahead of
// the verb, so which home a command acts on is settled once the verb's flags
// have been parsed and not before. --version and --help are not verbs and do
// not get that second reading, which is the gap above.
//
// # Text a stage's agent wrote
//
// A finding's text is whatever an agent put there. Both renderings escape
// every control character, on the same predicate: the structured one through
// internal/machine's encoder, which makes that pass itself rather than leaving
// it to encoding/json, and the one a person reads through this package's own.
// A description carrying an escape sequence is therefore shown rather than
// acted on by the terminal reading either of them.
//
// Several things reach that rendering carrying text this build did not
// write, and all of them go through it: the findings a decision carries, the
// summary a fix round wrote, a branch name, a project, a reference a change
// carried and a repository's default branch, which are the person's own
// words, and what a paused worker said it is waiting on, which PRD section 6
// has relayed in place of the state's word. A decision's answers come off a
// checkpoint and are escaped with the row they are filled into. What is not escaped is
// everything else a line is built from - a phrase from internal/machine's
// table, a stage name, a park's reason - because those are this build's own
// vocabulary rather than text that came from outside it.
//
// # What a person reads is phrased, renderer by renderer
//
// P13 has what a person reads name the outcome, the consequence and the
// next decision, with no task identifier, copy path, stage name, state label
// or watcher's word in it. The vocabulary that does that is internal/machine's
// phrase table, and this package has one owner of which renderers speak
// through it: the renderings table in render.go, which readOut dispatches
// through and Renderings reads out. Every row but four is phrased, and
// every line those rows write is a row of the table or a row beside the
// person's own word - a branch, a project, a reference a change carried, the
// repository's default branch - escaped. The stop guard's renderer writes no
// line at all, because the host reads its message on standard error, where
// hostStop writes it; that message is the turn row and then the pull guard's
// row, so it is phrased on the same terms. assistant watch renders an event
// through the same table rather than through a smaller one of its own,
// because a stream is a sequence of the states the listing shows and a second
// vocabulary for it would be a second owner of the same phrases; the type and
// revision of an event are the structured rendering's. An answer no row
// claims is not printed at all: readOut refuses it, and render writes the
// table's refusal row on standard error and exits with the operational
// failure code, so a type added without a renderer fails loudly rather than
// reaching a person as a Go value.
//
// The four that are not phrased are named in the renderings table, each
// with its voice, and all four are evidence: a run's details, the findings
// holding a decision, the usage view and a stage's log are the detail view,
// the relay of a finding that needs a decision, the usage view and the log
// view, the four detailed evidence documents P13 names, which it lets keep
// exact identifiers and stage names. Each row carries the phrase-table row
// that points a person at it, and writeEvidence, which the run renderer
// reaches the first two through, and writePointed, which readOut reaches the
// usage view and the log view through, write that row first, so the message pointing at the
// evidence is phrased like every other line; what a run says about a request
// it did not apply is a status line and is a row beside the evidence rather
// than part of it. A plan, which was the last unsettled row, is phrased:
// internal/custody's classes, refusals and steps each have a row.
//
// The refusal and usage text written on standard error is outside the table
// too, and is not held to P13 by any test: a failure's sentence is composed
// by whichever package refused, and no closed set of them exists to phrase;
// the next-action line render may print after it is a row of nextActions in
// client.go, which is a closed set but not the phrase table and is not held
// to the derived forbidden set either. The renderer test cites P13 for the
// phrased renderers by enumerating the table, the evidence test cites it for
// what points at the evidence, and the renderers that are not phrased are
// shown printing this build's words, so the enumeration is held in both
// directions; internal/journey's coverage row holds its note to the same
// enumeration.
//
// What that leaves is deliberate and worth knowing. The human rendering of a
// listing names no run and no task, so a verb that takes an identifier -
// runs by name, tasks by name - is driven from the structured listing, which
// keeps every identifier by design. A run's listing row reads from the
// record alone, so a merged run reads as having cleared the gate, and a run a
// bound parked reads as that bound only when its record names the park, which
// machine.RecordPhrase states; a run parked before the record held one reads
// as ended before a verdict. The run's own rendering has the merge record and
// the checkpoint and reads them. The structured listing carries, beside each
// record naming a park, the report the parked stage last recorded, and status
// carries the branch's latest run whole when a bound parked it, so the
// findings a park stopped on are readable after the run has ended.
//
// # What this package does not do
//
// It decides nothing about a change. Every verb here either reports what
// another package answered or asks the service to do something; there is no
// validation logic in the command surface, and a verb that reimplemented a
// check internal/gate, internal/pipeline, internal/graph, internal/safety or
// internal/runs already owns would be the defect rather than the shortcut.
//
// It holds no terminal interface. PRD section 9's four-question screen is
// internal/ui's, and this package renders answers as text; assistant watch
// prints a stream of events rather than drawing over them.
package cli
