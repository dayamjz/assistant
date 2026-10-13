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
// through SupervisionOf; a row per taskstate.State, with the domain derived
// from that package so a state added there fails until it is phrased, and
// two translations TaskPhrase makes - Paused is never the word but what the
// worker said it is waiting on, carried on Task.Waiting, and Unknown after a
// note the program appended is NoWordYet rather than a failure; a row per
// Outcome, with OutcomeExecuting split three ways on the facts NextActionOf
// reads, through RunPhrase and Run.Phrase; the two hold rows; the kept and
// removed rows for a run's cleanup record; the stream's own rows; and the
// status rows. No task row carries an outcome word, because PRD section 6
// makes a task's state its own vocabulary and a task with no run has no
// outcome. The vocabulary test cites P13 for the rows here, and which of
// internal/cli's renderers speak through them is that package's enumeration.
//
// What a row does not promise: it is what a person reads and not the
// mechanism, so the next decision a row names is the person's and the next
// action a driving agent reads stays on Run.NextAction, in that agent's
// terms; the two are written for different readers and are not held equal.
// RecordPhrase reads a record alone and says which two readings it cannot
// make.
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
