// Package cli is the command surface: the verbs PRD section 9 specifies, the
// flags that parameterize them, the two renderings of one answer, the two
// subcommands a gate's hooks invoke, and the host verb's turn-end hook and its
// installer.
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
// finding to raise against the specification, not a row to add quietly. The two
// things this binary answers to that are not in that table are the gate hook
// interface and the host hook interface, which the two sections below are
// about; section 9 names both outside its table and says they are not a
// precedent for a third.
//
// Four things the section leaves to a verb's parameters are flags or arguments
// rather than verbs.
//
// Answering a held run is --answer on the command that attaches to it, and
// ending one is --cancel on the same command. Attaching is where a person is
// shown the decision, and an agent driving the same gate needs the same
// decision with the same options and no terminal to give it in. The
// specification's table names no command for either, which is a gap against
// section 9 worth raising rather than a licence to add a verb.
//
// Reading one run or one task in full is an argument to the command that lists
// them, because that section gives runs one row and tasks one row.
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
// # The host hook verb is the second, and the last
//
// hostHooks in host.go holds "assistant host stop", which the coordinator's
// host runs from the hook it fires at turn end. It is PRD section 5's stop
// guard as the host sees it: P10 says a coordinator about to stop while work
// is under way and no healthy supervisor is the service holding this home's
// lock is blocked from stopping until its host supplies a re-entry signal,
// and this command is where that verdict becomes an exit status the host
// reads. It is dispatched beside the table for the gate verb's reason, and
// PRD section 9 says the two together are not a precedent for a third: each
// is the interface a hook this program installed or asked for requires.
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
// The re-entry signal is HostReentryVar in the environment the host gives the
// process, and that constant says why it is a variable rather than a flag and
// what consumes it: the host's own re-entered turn, since this process
// stores nothing and judges each invocation from what it was given. With the
// signal the turn may end and the message says why; without it a turn with
// work under way unwatched is blocked, and PRD section 5 says that is the
// only safe reading.
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
// itself; hostInstall names the source and its four gaps, the one that
// matters most being that the line arranges no re-entry signal. PRD section
// 9's row for this verb says a person never types it, which predates the
// installer, so that is a finding against the specification rather than a
// precedent for typed subcommands here.
//
// Three gaps are deliberate and written down rather than rounded off. The
// block status coincides with the usage status, so a host that runs a
// command line this surface refuses reads a block; that is the safe
// direction, and hostBlockCode says so. The command cannot place its caller:
// a worker ending the coordinator's turn would be deciding for the
// coordinator, but the worker group set is the service's and is reached over
// the socket by peer credentials, and a process with no socket has no peer
// to place, so the command answers the same whoever invokes it and refuses
// nobody. And the installer arranges no re-entry signal, so a host
// configured through it is never told a turn was re-entered and every turn
// that ends with work under way unwatched is blocked; hostInstall says why.
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
// is the host's protocol rather than section 9's; see hostBlockCode. A run that
// stopped to ask something is a success, because the answer goes back through
// the same surface and an agent that read a decision as a failure would stop
// driving exactly when it should carry on. A run that is still advancing is a
// success for the same reason, and PRD section 9 gives it an outcome of its
// own rather than one of the four that say a run is finished with.
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
// through and Renderings reads out. Every row but three is phrased, and
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
// The three that are not phrased are a run's details, the findings behind a
// decision, and a plan's steps, and the renderings table says why for each:
// the specification has not settled their wording. PRD section 9's terminal
// interface names every stage and its relay-verbatim row has a finding
// relayed in full, both against P13's text, and the module that would write
// a plan's steps does not exist in this build. The refusal and usage text
// written on standard error is outside the table too: a failure's sentence
// is composed by whichever package refused, and no closed set of them exists
// to phrase. The renderer test cites P13 for the phrased renderers by
// enumerating the table, and shows the three that are not phrased printing
// this build's words, so the enumeration is held in both directions;
// internal/journey's coverage row holds its note to the same enumeration.
//
// What that leaves is deliberate and worth knowing. The human rendering of a
// listing names no run and no task, so a verb that takes an identifier -
// runs by name, tasks by name - is driven from the structured listing, which
// keeps every identifier by design. A run's listing row reads from the
// record alone, so a merged run reads as having cleared the gate and a parked
// run as ended before a verdict, which machine.RecordPhrase states; the run's
// own rendering has the merge record and the checkpoint and reads them.
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
