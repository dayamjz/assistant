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
)

// holdOptions are the answers a hold accepts, and they are the outcomes a
// person may give a held stage. The halt point's options and what the hold
// node records are therefore the same list read twice rather than two lists
// that could drift: the hold node writes the answer as the outcome and
// translates nothing.
var holdOptions = []Outcome{OutcomeApproved, OutcomeSkipped, OutcomeCancelled}

// holdAnswers renders the hold options as the graph's answer strings.
func holdAnswers() []string {
	out := make([]string, len(holdOptions))
	for i, o := range holdOptions {
		out[i] = string(o)
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
	case r.HasParked():
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
