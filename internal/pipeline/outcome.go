package pipeline

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/graph"
)

// Outcome is what became of one stage in one run. It is the value a stage's
// outcome key holds, and it is what the topology routes on: the guards leaving
// a stage node compare this key and nothing else.
type Outcome string

const (
	// OutcomePending is the zero Outcome: the stage has not run.
	OutcomePending Outcome = ""
	// OutcomePassed means the stage ran and nothing it found needs anything
	// done about it. A report whose findings are all notes passes.
	OutcomePassed Outcome = "passed"
	// OutcomeFixable means the stage found fix-eligible findings and nothing
	// that needs a person. It routes to the fixer when the stage has one and
	// to the hold when it does not, which is what a fix round limit of zero
	// means: every finding goes to the person.
	OutcomeFixable Outcome = "fixable"
	// OutcomeHeld means the stage found something a person has to decide, so
	// the run halts before the hold node runs.
	OutcomeHeld Outcome = "held"
	// OutcomeSkipped means the stage was passed over. It is not a failure, and
	// it has three producers, one of which is a stage that did run: this run's
	// skip list named it, nothing remained to change after the rebase, or a
	// person answered skipped at its hold, which happens only after the stage
	// ran and reported something a person had to decide.
	//
	// So this value does not say whether the stage ran. StageRan does.
	OutcomeSkipped Outcome = "skipped"
	// OutcomeApproved means a person accepted the stage's findings as they
	// stand and let the run advance.
	OutcomeApproved Outcome = "approved"
	// OutcomeCancelled means a person ended the run at this stage's hold. Work
	// stays recoverable: the run stops, it is not undone.
	OutcomeCancelled Outcome = "cancelled"
	// OutcomeFixRequested means a person answered the stage's hold by asking
	// for a fix round, PRD section 5's Fix action. It stands only while that
	// round runs: the stage then executes again and records what it found,
	// and holds again so the person sees the result. The answer carries what
	// the round is to resolve as a FixRequest, in the hold's detail.
	OutcomeFixRequested Outcome = "fix"
	// OutcomeRebase means the stage found the pull request conflicting with
	// its base after the push and sent the run back to the rebase stage, PRD
	// section 14 decision 7's rebase of a conflicted pull request. Only a
	// stage whose row says Stage.RebasesAfterPush records it, and only over a
	// report that would otherwise have passed, so a stage that found anything
	// to fix or to hold for still does that instead. It stands only while the
	// run takes that path: the stage runs again at the end of it and records
	// what it found then, unless RebaseAfterPushRounds parks the run first, in
	// which case it is the outcome the parked run shows for the stage.
	OutcomeRebase Outcome = "rebase"
)

// holdOptions are the answers a hold accepts, and they are the outcomes a
// person may give a held stage. The halt point's options and what the hold
// node records are therefore the same list read twice rather than two lists
// that could drift: the hold node writes the answer as the outcome and
// translates nothing.
//
// OutcomeFixRequested is offered only by a pipeline that was given a fixer,
// because without one there is nothing the round could run on; holdAnswers
// adds it there and nowhere else.
var holdOptions = []Outcome{OutcomeApproved, OutcomeSkipped, OutcomeCancelled}

// holdAnswers renders the hold options as the graph's answer strings, with the
// requested fix round last when the pipeline can run one.
func holdAnswers(requests bool) []string {
	out := make([]string, 0, len(holdOptions)+1)
	for _, o := range holdOptions {
		out = append(out, string(o))
	}
	if requests {
		out = append(out, string(OutcomeFixRequested))
	}
	return out
}

// classify decides a stage's outcome from what it reported. It runs on a
// normalized report, so every action is one of the three the findings package
// recognizes and an unclassified finding has already become ask.
//
// An ask finding wins over a fix finding: PRD section 5 holds the stage
// immediately for one and never lets it into a fix round, whatever the stage's
// round limit allows.
func classify(r findings.Report) Outcome {
	switch {
	case r.HasHeld():
		return OutcomeHeld
	case len(r.Fixable()) > 0:
		return OutcomeFixable
	default:
		return OutcomePassed
	}
}

// StageOutcome returns what became of a stage in the run this state belongs
// to. A state that does not declare the key, which is a state from some other
// graph, reads as OutcomePending.
func StageOutcome(s graph.State, stage Stage) Outcome {
	v, ok := s.Get(string(stage.OutcomeKey()))
	if !ok {
		return OutcomePending
	}
	text, _ := v.Text()
	return Outcome(text)
}

// StageRan reports whether a stage's body ran in the run this state belongs
// to. It is the question a stage's outcome alone cannot answer, because
// OutcomeSkipped covers both a stage that was passed over and one that ran and
// had a person answer skipped at its hold.
//
// It reads whether the stage recorded a report, which is the fact that
// distinguishes them: the stage node records one for every execution that
// completed, and records none for a stage it skipped. A hold answer never
// writes one, so a stage skipped at its hold still carries the report it ran
// to produce.
//
// PRD section 5's pull request stage needs this rather than the outcome: it
// narrates what every stage found, and cannot narrate a stage that never
// looked.
//
// It answers that one question and no other. It does not tell a stage that ran
// clean from one a person waved past at its hold, because both recorded a
// report. Whether a stage was approved is the outcome key's answer, not this
// one's.
func StageRan(s graph.State, stage Stage) bool {
	v, ok := s.Get(string(stage.ReportKey()))
	if !ok {
		return false
	}
	text, _ := v.Text()
	return text != ""
}

// StageReport returns the report a stage recorded. A stage whose body never
// ran reports the zero Report; a stage that ran and was then skipped past at
// its hold keeps the report it recorded, which is the fact StageRan reads.
//
// A recorded report that cannot be decoded is refused with an error wrapping
// ErrBadReport rather than returned empty, because an empty report reads as a
// stage that found nothing.
func StageReport(s graph.State, stage Stage) (findings.Report, error) {
	v, ok := s.Get(string(stage.ReportKey()))
	if !ok {
		return findings.Report{}, fmt.Errorf("%w: %s: the state declares no report key", ErrBadReport, stage)
	}
	text, _ := v.Text()
	return decodeReport(text, stage)
}

// ReadStageReport returns the report a stage recorded, read through the Reader
// a stage body is given rather than off a whole graph.State. It is
// StageReport's answer on the surface a body actually holds, and it decodes
// through the same one owner, so the two cannot come to differ about what a
// recorded report means.
//
// A body reaches it by declaring a read of the stage's ReportKey, and it is
// refused with ErrUndeclaredRead when it did not. That is the review stage's
// path to the findings a fix round was sent to act on, which PRD section 5
// makes claims for the round's re-review to check.
//
// A stage whose body never ran recorded nothing, which reads back as the zero
// Report rather than as a failure: no round has run, so there is nothing to
// check. A recorded report that cannot be decoded is refused with an error
// wrapping ErrBadReport, on the same terms as StageReport.
func ReadStageReport(state Reader, stage Stage) (findings.Report, error) {
	v, err := state.Get(stage.ReportKey())
	if err != nil {
		return findings.Report{}, err
	}
	text, _ := v.Text()
	return decodeReport(text, stage)
}

// FixSummary returns the sanitized summary the last fix round of a stage
// wrote, empty when there was none.
func FixSummary(s graph.State, stage Stage) string {
	v, ok := s.Get(string(stage.FixKey()))
	if !ok {
		return ""
	}
	text, _ := v.Text()
	return text
}

// StageResult is everything one stage left behind in a run's state: whether
// its body ran, what became of it, the report it recorded, and the summary of
// the last fix round it took.
//
// It exists for PRD section 5's pull request stage, which narrates the run for
// a reviewer who was not present and therefore needs all four facts about
// every stage. The facts live in four keys of this package's schema and the
// report is JSON this package encoded, so gathering and decoding them here is
// what keeps a consumer from becoming a second reader of that encoding, per
// P14.
//
// It is a snapshot of the state it was read from and not a history. A stage
// that took several fix rounds recorded a report and a fix summary each round
// and each write replaced the last, so what a StageResult carries is the final
// one of each. Nothing in this package keeps the earlier ones, so nothing
// built on this may describe it as the rounds.
type StageResult struct {
	// Stage is the stage this describes.
	Stage Stage
	// Ran reports whether the stage's body ran, on the terms StageRan states:
	// it is read from whether a report was recorded, and it does not tell a
	// stage that ran clean from one a person waved past at its hold.
	Ran bool
	// Outcome is what became of the stage. A stage that has not run reads as
	// OutcomePending, which is also what a stage the run has not reached yet
	// reads as, so Ran is what separates those from a stage that was skipped.
	Outcome Outcome
	// Report is what the stage recorded. A stage whose body never ran carries
	// the zero Report, which is not a report of nothing found: Ran is the
	// field that says which.
	Report findings.Report
	// Fix is the sanitized summary the stage's last fix round wrote, automatic
	// or requested at its hold, empty when the stage took none.
	Fix string
}

// StageResultKeys returns the state keys ReadStageResult reads for a stage, in
// declaration order. An implementation that calls ReadStageResult declares
// these in Implementation.Reads, so the two cannot drift apart: a key added to
// the result is a key every caller then declares.
//
// A Stage that names no stage yields keys the schema does not hold, so a
// caller that declares them is refused with ErrUndeclaredKey rather than
// quietly reading nothing.
func StageResultKeys(stage Stage) []Key {
	return []Key{stage.ReportKey(), stage.OutcomeKey(), stage.FixKey()}
}

// ReadStageResult returns what a stage left behind, read through a body's own
// declared reads rather than off a whole state. StageRan, StageOutcome,
// StageReport, and FixSummary are the same four facts for a caller holding a
// graph.State; a stage body holds a Reader instead, which is why this exists
// beside them rather than replacing them.
//
// It returns the reader's own error unchanged when a key was not declared, so
// a body that forgot StageResultKeys fails the step rather than reading a
// stage as having done nothing. A recorded report that cannot be decoded is an
// error wrapping ErrBadReport, on the same terms as StageReport: an empty
// report reads as a stage that found nothing.
func ReadStageResult(r Reader, stage Stage) (StageResult, error) {
	out := StageResult{Stage: stage}
	recorded, err := r.Get(stage.ReportKey())
	if err != nil {
		return StageResult{}, err
	}
	text, _ := recorded.Text()
	out.Ran = text != ""
	if out.Report, err = decodeReport(text, stage); err != nil {
		return StageResult{}, err
	}
	outcome, err := r.Get(stage.OutcomeKey())
	if err != nil {
		return StageResult{}, err
	}
	outcomeText, _ := outcome.Text()
	out.Outcome = Outcome(outcomeText)
	fix, err := r.Get(stage.FixKey())
	if err != nil {
		return StageResult{}, err
	}
	out.Fix, _ = fix.Text()
	return out, nil
}

// PullRequestEnd returns what the CI stage wrote under KeyPullRequestEnd and
// KeyPullRequestEndHead: which state of the pull request ended its wait, as
// the store's vocabulary spells it, and the head it was read at, or two empty
// strings when nothing did. It carries text rather than the store's type
// because this package does not own that vocabulary; the reader that acts on
// it turns it into one and refuses what it cannot.
func PullRequestEnd(s graph.State) (end, head string) {
	return textOf(s, KeyPullRequestEnd), textOf(s, KeyPullRequestEndHead)
}

// textOf reads a text key off a state, empty when the state does not declare
// it.
func textOf(s graph.State, key Key) string {
	v, ok := s.Get(string(key))
	if !ok {
		return ""
	}
	text, _ := v.Text()
	return text
}

// Cancelled reports whether a person cancelled the run at a hold.
func Cancelled(s graph.State) bool {
	v, ok := s.Get(string(KeyCancelled))
	if !ok {
		return false
	}
	flag, _ := v.Bool()
	return flag
}

// decodeReport reads a recorded report. The empty string is the key's zero
// value and means the stage recorded nothing, which is not a decoding failure.
func decodeReport(text string, stage Stage) (findings.Report, error) {
	if text == "" {
		return findings.Report{}, nil
	}
	var r findings.Report
	if err := json.Unmarshal([]byte(text), &r); err != nil {
		return findings.Report{}, fmt.Errorf("%w: %s: %w", ErrBadReport, stage, err)
	}
	return r, nil
}

// sortedKeys returns a map's keys in a fixed order.
func sortedKeys[V any](m map[Key]V) []Key {
	out := make([]Key, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
