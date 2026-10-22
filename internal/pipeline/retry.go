package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/graph"
)

// RetryRounds bounds how many times a person may answer one stage's hold with
// a retry in one run, PRD section 5's Retry and section 14 decision 25. The
// retry is a second cycle through the stage, beside the requested fix round,
// from the hold through the stage's retry node back to the stage, and both of
// its edges carry this bound, on the terms RequestedFixRounds is carried: the
// edge out of the hold is the exit every retry leaves the hold by and counts
// toward, and it reaches the bound first, because every return follows an
// exit it counted.
//
// It is a constant rather than a configuration key because PRD section 10
// declares none: every retry is a person's explicit answer, so the bound is a
// backstop against a loop nobody is driving. An answer asking for one more
// retry than this is refused by internal/graph with graph.ErrRoundsSpent
// while the decision is still open, so the person can still approve, skip, or
// cancel. The run-wide step budget counts every node a retry runs as well.
const RetryRounds = 3

// ErrRetryNotOffered is returned for a retry at a hold whose stage report does
// not hold on work that never started. PRD section 5 offers the retry only at
// the usage ceiling's hold, the one hold this build raises over such work,
// and internal/graph cannot refuse it at another, because a halt point's
// options are fixed when the graph is built; this is the refusal that names
// why.
var ErrRetryNotOffered = errors.New("pipeline: a retry is offered only at a hold over work that never started")

// RetryOffered reports whether the hold a run stands at in state, the stage's,
// offers a retry: the stage's recorded report holds on a finding that stands
// in for work that never started, findings.Finding.NotStarted. Only the
// program marks a finding so, and the usage ceiling's hold is the one finding
// it marks, so this is PRD section 5's "offered only at a hold the usage
// ceiling raised" read off what the stage recorded. A report that cannot be
// read is an error rather than false.
func RetryOffered(s graph.State, stage Stage) (bool, error) {
	report, err := StageReport(s, stage)
	if err != nil {
		return false, err
	}
	return report.HasHeldNotStarted(), nil
}

// CheckRetry refuses a retry at a hold that does not offer one, with
// ErrRetryNotOffered. It is the check a surface answering a hold makes before
// anything is recorded, so the decision stays open; the retry node makes it
// again, so a caller that answers the graph without it fails the node rather
// than re-running a stage nobody was offered a retry of.
func CheckRetry(s graph.State, stage Stage) error {
	offered, err := RetryOffered(s, stage)
	if err != nil {
		return err
	}
	if !offered {
		return fmt.Errorf("%w: the %s stage's hold is not over a call the usage ceiling refused, "+
			"so running the stage again would only ask the same question again", ErrRetryNotOffered, stage)
	}
	return nil
}

// retryEdges are the two edges of a stage's retry cycle: out of the hold on
// the retry answer, and from the retry node back to the stage.
//
// The edge out is guarded on the answer key rather than on the outcome the
// hold node writes from it, for the reason the requested round's edge in is:
// the answer is what graph.Executor.Answer can read before the hold node has
// run, which is what lets it refuse a retry past RetryRounds while the
// decision is still open.
//
// The edge back is graph.Edge.Answered: every traversal follows an answer at
// the hold, which internal/graph checks at build. That is what keeps a retry
// while the ceiling still has no room from parking the run converged. Such a
// retry re-runs the stage, whose call is refused again, and the run stands
// at the hold in the state it stood in before, so a back edge held to the
// convergence check would park the run on the second such retry. Exempt, it
// returns to the hold for the person, and RetryRounds and the run-wide budget
// bound it. A retry an unattended answer gave would be held to convergence
// again, on internal/graph's terms; standing consent never gives one, because
// the ceiling's hold is an ask whose remedy lies outside the change.
func retryEdges(stage Stage) []graph.Edge {
	return []graph.Edge{
		{
			From:   stage.HoldNode(),
			To:     stage.RetryNode(),
			Rounds: RetryRounds,
			Guard: &graph.Guard{
				Key:   string(stage.AnswerKey()),
				Op:    graph.OpEquals,
				Value: graph.TextValue(string(OutcomeRetry)),
			},
		},
		{From: stage.RetryNode(), To: stage.Node(), Rounds: RetryRounds, Answered: true},
	}
}

// retryNode builds the node a retry passes through on its way back to the
// stage. It does no agent work: the stage runs from the top after it, and
// whether that run's agent call starts is the usage ceiling's to decide, as
// for any call, so a retry while there is still no room holds on the same
// question again rather than looping.
//
// What it does is keep the stage's next execution from reporting a stale
// hold. A fix round the ceiling refused leaves the ceiling's hold in the
// stage's held key, which the stage adds to every report it makes until a
// round writes the key again, so without this a retry with room would hold
// again on the refusal it was answering. It drops every hold in that key
// that stands in for work that never started, and keeps the rest: the hold a
// requested round leaves so its result is seen is kept, so a retry never
// carries a round a person asked for past them unseen.
//
// It refuses with ErrRetryNotOffered when the stage's report does not offer
// the retry, which only a caller answering the graph without CheckRetry can
// reach.
func retryNode(stage Stage) graph.Node {
	return graph.Node{
		Name:   stage.RetryNode(),
		Reads:  []string{string(stage.ReportKey()), string(stage.HeldKey())},
		Writes: []string{string(stage.HeldKey())},
		NewBody: graph.Stateless(func(ctx context.Context, r graph.Reader, w graph.Writer) error {
			recorded, err := r.Get(string(stage.ReportKey()))
			if err != nil {
				return err
			}
			text, _ := recorded.Text()
			report, err := decodeReport(text, stage)
			if err != nil {
				return err
			}
			if !report.HasHeldNotStarted() {
				return fmt.Errorf("%w: the %s stage's recorded report holds on nothing that never started",
					ErrRetryNotOffered, stage)
			}
			held, err := readHolds(r, stage)
			if err != nil {
				return err
			}
			kept := make([]findings.Finding, 0, len(held))
			for _, f := range held {
				if !f.NotStarted {
					kept = append(kept, f)
				}
			}
			encoded := ""
			if len(kept) > 0 {
				raw, err := json.Marshal(kept)
				if err != nil {
					return fmt.Errorf("pipeline: record the holds the %s stage keeps across a retry: %w", stage, err)
				}
				encoded = string(raw)
			}
			return w.Set(string(stage.HeldKey()), graph.TextValue(encoded))
		}),
	}
}
