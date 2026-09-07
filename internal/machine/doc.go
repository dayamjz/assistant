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
