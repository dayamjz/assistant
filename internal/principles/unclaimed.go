package principles

// unclaimed is the table of principles no test in this repository claims, and
// what a reader should know about the gap.
//
// A row is a declaration that the gap is known and looked at, and it is
// nothing else. It does not say the principle is unimplemented, does not say
// the gap is acceptable, and does not say the reason beside it is true: the
// reason is prose, written by whoever added the row, and no mechanism here
// checks it against the code. What the table buys is that the gap is
// enumerable and shows up in review as a line somebody had to write, rather
// than as an absence nobody can see.
//
// Check refuses a row whose principle a test does turn out to cite, so a row
// cannot outlive the gap it describes.
var unclaimed = map[Principle]string{
	P5: "The review stage that would re-review a fix round's work is not built. " +
		"internal/pipeline checks the loop that re-runs a stage over the fixer's writes; " +
		"what is unclaimed is that a change the pipeline authored is reviewed as author code.",
	P9: "There is no wake classifier in this repository. Supervision is the orchestrator's, and nothing here supervises.",
	P10: "There is no coordinator and no watcher here, so no turn ends. " +
		"internal/home's lock gives a home one service, which is a different question from whether a healthy watcher holds it.",
	P11: "Nothing here launches a worker into an isolated copy. internal/gate asks who a working copy belongs to, which is a different question.",
	P12: "Nothing here removes an isolated copy. internal/gate's removal is about a gate repository, not about proving a worker's work landed.",
	P13: "internal/cli renders for a person, and no test claims what it prints names the outcome rather than the mechanics: " +
		"run identifiers, stage names and home paths are on that surface today.",
}
