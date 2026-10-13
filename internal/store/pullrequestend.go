package store

import (
	"fmt"
	"slices"
)

// PullRequestEnd says which state of a run's pull request ended the run, when
// one did. PRD section 5's "Waiting on checks" has two: a pull request read as
// merged completes the run without validating it, and one read as closed
// without merging fails it. It is recorded on the run's own row, beside the
// status it moved to, so the record says what ended the run rather than leaving
// a reader to infer it from where the checkpoint stopped.
//
// It is the record's vocabulary and not the code host's. internal/forge says
// where a pull request stands, in its own closed set, and the one place that
// reads that set decides whether what it read is one of these two; this type
// holds only what a run can be ended by, so a value here is never "open".
//
// The zero value names no end, which is what every run that ended some other
// way holds. It is not a member of the set: a transition asked to record it
// refuses, so the column never holds an empty word standing in for nothing.
type PullRequestEnd string

const (
	// PullRequestMerged is the run's pull request was read as merged. The run
	// is complete and not newly validated: its checks were not read to a
	// verdict, and the rule that only a no_ci declaration makes an empty
	// check list green was never applied.
	PullRequestMerged PullRequestEnd = "merged"
	// PullRequestClosed is the run's pull request was read as closed without
	// being merged, which ends the run as failed.
	PullRequestClosed PullRequestEnd = "closed"
)

// pullRequestEnds is the closed set, in the order PullRequestEnds reports.
var pullRequestEnds = []PullRequestEnd{PullRequestMerged, PullRequestClosed}

// PullRequestEnds returns the closed set. The result is a copy, so a caller
// cannot add to the set by writing to it.
func PullRequestEnds() []PullRequestEnd { return slices.Clone(pullRequestEnds) }

// Recognized reports whether e is one of the two ends this package defines.
func (e PullRequestEnd) Recognized() bool { return slices.Contains(pullRequestEnds, e) }

// String renders the end as it is stored.
func (e PullRequestEnd) String() string { return string(e) }

// Reason is the one sentence a record gives for a run this end ended, in the
// words PRD section 5 fixes for it: a run failed by its pull request closing
// is recorded with the reason "pull request closed without merging". It is
// rendered from the stored end rather than stored beside it, so the column and
// the sentence cannot disagree. An end outside the set renders as nothing.
func (e PullRequestEnd) Reason() string {
	switch e {
	case PullRequestMerged:
		return "pull request merged"
	case PullRequestClosed:
		return "pull request closed without merging"
	default:
		return ""
	}
}

// pullRequestEndValue turns the stored column back into a PullRequestEnd. A
// column that is absent is unknown, which is what every run ended by anything
// but its pull request reads back as, and what a run recorded before the
// column existed reads back as. A column that is present and outside the set
// is an error rather than an unknown, for the reason resolverValue gives:
// reporting a row nobody can account for as "nothing was recorded" is the
// fabrication Optional exists to prevent.
func pullRequestEndValue(o Optional[string], column string) (Optional[PullRequestEnd], error) {
	s, ok := o.Get()
	if !ok {
		return Unknown[PullRequestEnd](), nil
	}
	end := PullRequestEnd(s)
	if !end.Recognized() {
		return Unknown[PullRequestEnd](), fmt.Errorf("store: column %s: %w: %q", column, ErrUnknownPullRequestEnd, s)
	}
	return Known(end), nil
}
