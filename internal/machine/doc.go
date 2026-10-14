// Package machine is the agent-facing half of the command surface: the shapes
// a structured answer takes, the three exit codes, and the outcome vocabulary.
// PRD section 9 gives its module structured output and stable exit codes, and
// this is both.
//
// A person at a terminal and an agent driving programmatically share the same
// decisions, the same options, and the same authority; only the rendering
// differs. So the shapes here are what the service answers with and what the
// command line prints in its structured mode, and the human rendering is a
// second reading of the same values rather than a second source of them.
//
// # It composes records rather than restating them
//
// Every field that is a record another package owns is that package's type.
// A run is a store.Run, a decision carries a graph.Decision, a stage's report
// is a findings.Report, and a push's reference updates are gate.RefUpdate. P14
// gives each of those one owner, and a wire shape that redeclared their fields
// would be a second one that drifts. What this package adds is the envelope:
// which records answer which call, and the four things a driving agent needs
// that no record holds - the outcome, whether anything is advancing the run,
// the next action, and whether to stop driving.
//
// The first three of those are one decision rather than three fields a surface
// fills in separately. Run.Decide derives them together from a Standing, which
// is the run's record, where its execution stands, and whether a segment of it
// is executing; the action it writes is unexported, so a surface outside this
// package has no assignment site for one that disagrees with the outcome
// beside it. Run.Decide and the fields it writes say the rest.
//
// # The five parts of the contract, and where each of them is
//
// PRD section 9 states the machine interface as five parts. Two are here.
// Structured output is Encoder, which writes one document per answer to
// standard output; progress belongs on standard error and is the command
// line's to write. Stable exit codes are Code, which has three members and
// never overloads one.
//
// Outcomes are here as the closed set Outcome, with the table on that type
// saying which of them this build can produce.
//
// # The vocabulary a person is spoken to in
//
// P13 makes what a person reads name the outcome, the consequence and the
// next decision, with no task identifier, path, stage name, state label or
// watcher's word in it. The rows that do that are the Phrase values in
// phrases.go, each three sentences in three fields so the required wording
// is structural, and TablePhrases lists every one so a test can hold it to
// the rule, with the forbidden words derived from the packages that own them
// rather than kept in a list beside the phrases. One table holds every row:
// the pull guard's three, exported as strings in vocabulary.go and reached
// through SupervisionOf; the stop guard's two turn rows, exported as strings
// in host.go and reached through HostStopOf, which composes SupervisionOf
// rather than making a second translation, so each turn row carries an
// outcome and a consequence and its decision points at the pull guard's row
// appended after it, while host.go holds HostStop, the answer's shape, and
// says what the host's protocol is not; the stop guard installer's rows, one
// per HostInstallResult and one per HostInstallRefusal, reached through
// HostInstallPhrase, with HostInstall beside them in host.go and the file and
// the line kept out of every row; a row per taskstate.State, with the domain derived
// from that package so a state added there fails until it is phrased, and
// three translations TaskPhrase makes - Paused is never the word but what the
// worker said it is waiting on, carried on Task.Waiting, Unknown after a
// note the program appended is NoWordYet rather than a failure, and Held
// from internal/taskstate's hold tier is the row saying the work waits on
// the person's answer given with --task and --answer; a row per
// Outcome, with OutcomeExecuting split three ways on the facts NextActionOf
// reads and OutcomeFailed split by the bound a run's record says parked it,
// one row per store.ParkBound, through RunPhrase and Run.Phrase; the hold rows, an open one naming
// a task saying the same command; a row per AnswerEffect that does more than
// close a task's hold, through TaskAnsweredPhrase, each claiming only what
// that effect's doc says the mechanism does; the kept and
// removed rows for a run's cleanup record; the one row for a run's copy sweep
// record that did not establish the copy clear, through CopySweepPhrase,
// whose counts and occasion stay the structured rendering's; the stream's
// own rows; the
// status rows; and the rows for the rest of the command surface - a decision
// and a fix round's offer, a row per doctor check and result keyed on the
// check names declared beside them and the verdict, the service lifecycle, a
// gate created, reattached and removed, the gate's own rows for a change sent
// to it, the rows pointing at the three evidence documents section 9 names -
// the detail view, the findings relayed for a decision, and the usage view,
// whose shape is Usage in usage.go - and the refusal for an answer with no
// renderer. No task row carries an outcome word, because PRD section 6
// makes a task's state its own vocabulary and a task with no run has no
// outcome. The vocabulary test cites P13 for the rows here, and which of
// internal/cli's renderers speak through them is that package's enumeration.
//
// What a row does not promise: it is what a person reads and not the
// mechanism, so the next decision a row names is the person's and the next
// action a driving agent reads stays on Run.NextAction, in that agent's
// terms; the two are written for different readers and are not held equal.
// RecordPhrase reads a record alone and says which two readings it cannot
// make. A park is not one of them once the record names it: the record is
// where the bound that ended a run is written, so a listing reads a parked
// run as its bound the way a run's whole answer does, and ParkAction is the
// driving agent's reading of the same record.
//
// Blocking calls and relaying verbatim are not shapes, so they are not here.
// A call blocks because the service advances the run before it answers, and a
// call that advanced one answers where that run stopped rather than while it
// moves; a start that finds the run already advancing is answered with the run
// as it stands instead. Relaying verbatim is Decision carrying the stage's
// findings as the stage reported them: unsummarized, unjudged, and not
// filtered by anything on the way through.
//
// # Control characters are escaped rather than emitted
//
// An answer carries text an agent wrote, so it carries whatever that agent
// put in it. Encoder writes JSON, whose string encoding escapes every control
// character, so a finding holding an escape sequence arrives as characters a
// reader can see rather than as an instruction to the terminal reading it.
// TestControlCharactersInAnAnswerAreEscaped is what holds that.
//
// # What this package does not do
//
// It does not decide anything. It holds no policy about when a run may start,
// what a finding means, or who may answer a hold; those belong to the packages
// PRD section 8 gives them to, and a value arriving here has already been
// decided.
//
// It does not talk to anything. It opens no socket, reads no database, and
// runs no command: it is the vocabulary the surfaces on either side of the
// socket agree in.
package machine
