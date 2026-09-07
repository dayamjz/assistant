// Package cli is the command surface: the verbs PRD section 9 specifies, the
// flags that parameterize them, and the two renderings of one answer.
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
// verbs in cli.go is the whole surface. Every row is a command PRD section 9's
// table names, and a command that section does not name is not here: a verb
// this product needs and that section does not describe is a finding to raise
// against the specification, not a row to add quietly.
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
// validation stage. Section 9's table names no command for it, and no stage in
// this build launches an agent that would need one, so none is invented here.
//
// # The gate hook verbs are a second table on purpose
//
// gateHooks in gate.go is the other one, and it holds "assistant gate admit"
// and "assistant gate notify". They are not commands PRD section 9's table
// names and they are not candidates for it: they are the interface
// internal/gate requires of this command surface, invoked by the hooks it
// installs in a gate repository and never typed. A gate whose hooks call a
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
// normal decision points, operational failure, and incorrect usage. A run that
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
// Three things reach that rendering carrying text this build did not write,
// and all three go through it: the findings a decision carries, the summary a
// fix round wrote, and the payload an event on assistant watch carries, which
// is a record holding the intent a person or a driving agent supplied. What is
// not escaped is everything else a line is built from - a stage name, an
// outcome, a park's reason - because those are this build's own vocabulary
// rather than text that came from outside it.
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
