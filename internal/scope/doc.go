// Package scope is the review stage's scope lens: every changed line should
// trace to the stated intent. Its contract is PRD section 5, "Scope as a
// review lens"; this comment restates the parts that are load-bearing so a
// reader of the code does not have to open the PRD to know what must stay
// true.
//
// It is a lens and not a stage. P2 fixes the stage list at nine and this adds
// none: the intent stage has already recorded what the change set out to do,
// and that intent plus the paths the change touched are the whole input.
// Guidance is text the review stage puts in front of its reviewer, and Observe
// is what the reviewer's answer becomes.
//
// What the intent stage records may be that the run carries no intent, which
// it reports rather than filling in. A run started without one therefore
// reaches the lens with nothing to trace to, and ErrNoIntent owns what that
// costs and whose decision it is.
//
// # It ships on, and no key turns it off
//
// The lens is shipped guidance rather than a configured value, so a repository
// that has never heard of it still gets it once a review stage puts it in
// front of a reviewer. internal/stages' review body is that wiring: it puts
// Guidance in front of every reviewer it asks, reads the answer back off the
// review report's traces, and appends what Observe makes of them. That is
// deliberate and not an oversight of the configuration schema: a repository
// layer replaces a list rather than appending to it, so a scope rule shipped as
// the default of config's "review.path_rules" would be erased by any
// repository that set that key for an unrelated reason, which is exactly the
// opt-out PRD section 5 says the lens does not have. Path-scoped review
// rules still strengthen it, the way they strengthen review anywhere.
//
// The one run that gets none of that is the one this lens has nothing to say
// about. A run carrying no intent gets ErrNoIntent from Guidance, so that body
// leaves the scope section out of the prompt entirely, along with the report
// field asking for traces, rather than putting a question there that has no
// answer, and it reports that the lens did not run instead of calling Observe.
// That is this package's own refusal and not an opt-out: what is missing is
// the thing to trace to, no configuration can produce it or withhold it, and
// the run says so in its own report where a person reads it.
//
// # A scope observation is a note, and can be nothing else
//
// Observe builds its findings itself and builds them with findings.ActionNote,
// so a scope observation informs and blocks nothing: it never enters the
// automatic fix loop and never holds a run for a decision. A line being
// unexplained is not the bar for fix or ask. When a change nobody asked for is
// also wrong, the wrongness is an ordinary review finding reported on its own
// merits, by the reviewer, through the ordinary path; it does not come from
// here and this package does not cap it.
//
// P3 is untouched, because this is not a parse path. Observe reads no agent's
// action field and resolves none, so there is no missing, empty, or
// unrecognized action here to fail closed on. A finding the reviewer wrote
// still goes through findings.Normalize and still becomes ask when its action
// is unreadable.
//
// # The check has to be able to fail
//
// The paths the change touched come from the run, not from the reviewer. A
// reviewer therefore cannot make a note disappear by omitting a file: silence
// on a path requires an affirmative trace naming that path and saying what
// part of the intent it follows from. That claim is recorded in the reviewer's
// own words and is attributable to it. Only the path is asked for verbatim,
// and the guidance says so where the reviewer meets the list, because exact
// equality after trimming surrounding space is the comparison Observe makes.
//
// A lens asked about no path at all is refused with ErrNoTouched rather than
// answered with silence, on the same terms as a missing intent. That error
// owns why, and owns what a caller does when a run legitimately reaches it
// with nothing left to ask about.
//
// What the claim is not is verified. This package does not read the intent or
// the reason for sense, so a reviewer that writes a false reason for a path
// silences the note for that path. The lens turns an untraced change into a
// note; it does not adjudicate a trace.
//
// # The rest of what it does not promise
//
//   - Its granularity is the path, not the line. One traced change and one
//     untraced change in the same file produce no note, because the path was
//     traced. The heading of PRD section 5 is about lines; this mechanism is
//     about paths, and the difference is a gap rather than a restatement.
//   - Paths are compared exactly, after trimming surrounding space and
//     nothing else. The guidance asks for the path verbatim, but asking is all
//     it does: a trace whose path differs from the run's by letter case or by
//     any other spelling matches nothing and so silences nothing, which fails
//     toward a note rather than toward silence.
//   - A trace naming a path the change did not touch silences nothing and is
//     not itself reported. It is a claim about nothing.
//   - The ceiling on what the lens is worth at all - that it checks a change
//     against the recorded intent and never the provenance of that intent - is
//     PRD section 5, "What the gate does not establish". That section owns the
//     fact, per P14, and it is not restated here.
//
// This package is pure. It performs no I/O, runs no agent, reads no file, and
// reads no configuration; a caller that excludes ignored paths from review
// excludes them before calling here.
package scope
