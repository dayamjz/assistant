package pipeline

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/dayamjz/assistant/internal/findings"
)

// RequestedFixRounds bounds how many fix rounds a person may ask for at one
// stage's hold in one run. Both edges of the requested-round cycle carry it,
// on the terms the automatic loop's edges carry a stage's limit, because
// internal/graph refuses a cycle with an unbounded back edge.
//
// It is a constant rather than a configuration key because PRD section 10
// declares none and PRD section 14 records why: every one of these rounds is
// a person's explicit answer, so the bound is a backstop against a loop no
// person is driving rather than a policy a repository tunes. An answer asking
// for one more round than this is refused by internal/graph with
// graph.ErrRoundsSpent while the decision is still open, so the person can
// still approve, skip, or cancel. The run-wide step budget bounds these rounds
// as well, as it bounds every node.
const RequestedFixRounds = 3

// Errors a FixRequest is refused with. Each is a typed result a caller
// answering a hold handles before anything is recorded.
var (
	// ErrEmptyFixRequest is returned for a request that selects no finding and
	// writes none of its own, which would be a round with nothing to resolve.
	ErrEmptyFixRequest = errors.New("pipeline: the fix request selects no finding and adds none")
	// ErrUnknownFinding is returned for a request selecting an identifier the
	// stage's recorded report does not carry.
	ErrUnknownFinding = errors.New("pipeline: the fix request selects a finding the stage did not report")
	// ErrUnusableFixRequest is returned for a request that cannot be read, or
	// that adds a finding of its own with no description.
	ErrUnusableFixRequest = errors.New("pipeline: the fix request cannot be used")
	// ErrFixNotRequestable is returned for a fix request at the hold of a
	// stage that is not Stage.Requestable: push, pr, or ci. Such a hold does
	// not offer the round, so internal/graph would refuse the answer as well;
	// this is the refusal that names why.
	ErrFixNotRequestable = errors.New("pipeline: a fix round cannot be requested at this stage's hold")
)

// CheckRequestable refuses a fix request at a stage whose hold does not offer
// one, with ErrFixNotRequestable. It is the check a surface answering a hold
// makes before anything is recorded.
func CheckRequestable(stage Stage) error {
	if stage.Requestable() {
		return nil
	}
	return fmt.Errorf("%w: the %s stage runs at or after push, where a requested round's result "+
		"would be pushed before you saw it or never pushed at all", ErrFixNotRequestable, stage)
}

// FixRequest is what a person asks for when they answer a stage's hold with a
// fix round: PRD section 5's selection, notes, and findings of their own.
// It travels as the hold's detail, encoded by Encode, and the request node
// reads it back with ParseFixRequest; Select is the one rule both the caller
// answering the hold and that node apply.
type FixRequest struct {
	// Selected names findings from the stage's recorded report, by the
	// identifiers that report carries. Any action may be selected: choosing
	// which findings a round takes on is the person's decision here, which is
	// what separates this round from the automatic one.
	Selected []string `json:"selected,omitempty"`
	// Notes is free text for the fixer, such as "fix the code, not the test".
	Notes string `json:"notes,omitempty"`
	// Findings are findings the person wrote themselves. Each needs a
	// description; its identifier is dropped, and it is put to the fixer as
	// something to resolve whatever action it was written with.
	Findings []findings.Finding `json:"findings,omitempty"`
}

// Selection is a FixRequest resolved against the report it was made about.
type Selection struct {
	// Resolve is what the round is asked to resolve: the selected findings in
	// the order the stage reported them, then the person's own.
	Resolve []findings.Finding
	// Unselected is every finding of the report the person did not select,
	// which the fixer is told about so it leaves them alone.
	Unselected []findings.Finding
	// Notes is the person's notes, trimmed.
	Notes string
}

// Select resolves the request against the stage's recorded report. It refuses
// an identifier the report does not carry, a finding of the person's own with
// no description, and a request that comes to nothing to resolve.
func (r FixRequest) Select(report findings.Report) (Selection, error) {
	chosen := make(map[string]bool, len(r.Selected))
	for _, id := range r.Selected {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		chosen[id] = false
	}
	var out Selection
	for _, f := range report.Findings {
		if _, ok := chosen[f.ID]; ok {
			chosen[f.ID] = true
			out.Resolve = append(out.Resolve, f)
			continue
		}
		out.Unselected = append(out.Unselected, f)
	}
	for id, found := range chosen {
		if !found {
			return Selection{}, fmt.Errorf("%w: %q", ErrUnknownFinding, id)
		}
	}
	for _, own := range r.Findings {
		own.ID = ""
		own.Description = strings.TrimSpace(own.Description)
		if own.Description == "" {
			return Selection{}, fmt.Errorf("%w: a finding of the request's own has no description",
				ErrUnusableFixRequest)
		}
		own.Action = findings.ActionFix
		out.Resolve = append(out.Resolve, own)
	}
	if len(out.Resolve) == 0 {
		return Selection{}, ErrEmptyFixRequest
	}
	out.Notes = strings.TrimSpace(r.Notes)
	return out, nil
}

// Encode renders the request as the text the hold's detail key holds.
func (r FixRequest) Encode() (string, error) {
	encoded, err := json.Marshal(r)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrUnusableFixRequest, err)
	}
	return string(encoded), nil
}

// ParseFixRequest reads back what Encode wrote. Text that does not decode is
// refused rather than read as an empty request.
func ParseFixRequest(text string) (FixRequest, error) {
	var r FixRequest
	if err := json.Unmarshal([]byte(text), &r); err != nil {
		return FixRequest{}, fmt.Errorf("%w: %w", ErrUnusableFixRequest, err)
	}
	return r, nil
}
