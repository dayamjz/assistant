package pipeline

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/graph"
)

// rebasingChecks is a checks stage that asks for the path back to rebase on
// every execution, over a report that would otherwise pass.
func rebasingChecks(c *calls) Implementation {
	return recording(c, nil, nil, func(Input, int) (Output, error) {
		return Output{Report: findings.Report{Summary: "the pull request conflicts with its base"}, Rebase: true}, nil
	})
}

// movingRebase is a rebase stage that writes a head no earlier execution
// wrote, which is what a rebase onto a base that moved does to the run's
// state: without it every pass would leave the state as the last one did and
// the convergence bound would park the run before the round bound could.
func movingRebase(c *calls) Implementation {
	return recording(c, nil, []Key{KeyHead}, func(_ Input, call int) (Output, error) {
		return Output{
			Report: passing(),
			Writes: map[Key]graph.Value{KeyHead: graph.TextValue(fmt.Sprintf("rebased-%d", call))},
		}, nil
	})
}

// The path back from the checks stage to rebase is the cycle the build
// refuses without a bound, so it is built carrying RebaseAfterPushRounds and
// read by internal/graph as a back edge; and the run that keeps asking for it
// parks on that bound in front of the rebase stage, naming the edge, after the
// first pass and exactly RebaseAfterPushRounds passes back. Each pass runs
// the stages between rebase and checks again, which is the second review and
// the second push.
func TestThePathBackFromTheChecksStageIsBoundedAndParksOnItsBound(t *testing.T) {
	c := newCalls()
	s := recordingStages(c)
	set(&s, StageRebase, movingRebase(c))
	set(&s, StageCI, rebasingChecks(c))
	p := build(t, Options{Stages: s, Budget: 100})

	var path *graph.Edge
	for i, e := range p.Graph().Edges() {
		if e.From == StageCI.Node() && e.To == StageRebase.Node() {
			if !p.Graph().IsBackEdge(i) {
				t.Fatalf("the path back from checks to rebase is not a back edge, so no bound is required of it")
			}
			path = &e
		}
	}
	if path == nil || path.Rounds != RebaseAfterPushRounds {
		t.Fatalf("the path back from checks to rebase is %+v, want an edge bounded by %d", path, RebaseAfterPushRounds)
	}

	_, result := start(t, p, complete())
	if result.Status != graph.StatusRoundsExhausted || result.Position != StageRebase.Node() {
		t.Fatalf("the run is %s at %q, want parked rounds-exhausted in front of %q", result.Status, result.Position, StageRebase.Node())
	}
	if result.ParkedAt == nil || result.ParkedAt.From != StageCI.Node() || result.ParkedAt.To != StageRebase.Node() {
		t.Fatalf("the park names edge %+v, want the path back from checks to rebase", result.ParkedAt)
	}
	passes := 1 + RebaseAfterPushRounds
	for _, stage := range []Stage{StageRebase, StageReview, StagePush, StageCI} {
		if got := c.stageCount(stage); got != passes {
			t.Errorf("%s ran %d times, want %d: the first pass and one per pass back", stage, got, passes)
		}
	}
	if got := c.stageCount(StageIntent); got != 1 {
		t.Errorf("intent ran %d times, want once: the path back starts at rebase", got)
	}
	if got := StageOutcome(result.State, StageCI); got != OutcomeRebase {
		t.Errorf("the parked run shows the checks stage as %q, want %q", got, OutcomeRebase)
	}
}

// A pass back that leaves the run's state exactly as the pass before it is a
// loop doing nothing, and the convergence bound parks it on the path's own
// edge before the round bound is reached. The control is the test above,
// whose rebase moves the head every pass and parks on the round bound instead.
func TestAPassBackThatChangesNothingParksConverged(t *testing.T) {
	c := newCalls()
	s := recordingStages(c)
	set(&s, StageCI, rebasingChecks(c))
	_, result := start(t, build(t, Options{Stages: s, Budget: 100}), complete())

	if result.Status != graph.StatusConverged || result.ParkedAt == nil || result.ParkedAt.From != StageCI.Node() {
		t.Fatalf("the run is %s on edge %+v, want converged on the path back from checks", result.Status, result.ParkedAt)
	}
	if got := c.stageCount(StageRebase); got != 2 {
		t.Fatalf("rebase ran %d times, want twice: the first pass and the one pass back that changed nothing", got)
	}
}

// Only the stage the table gives the path may ask for it. A request from any
// other stage fails the step naming ErrRebaseNotOffered rather than being
// dropped, which is the positive control for the routing above: the request
// is read, not ignored.
func TestOnlyTheChecksStageMayAskToGoBackToRebase(t *testing.T) {
	for _, stage := range Order() {
		if stage.RebasesAfterPush() {
			continue
		}
		t.Run(stage.String(), func(t *testing.T) {
			c := newCalls()
			s := recordingStages(c)
			set(&s, stage, recording(c, nil, nil, func(Input, int) (Output, error) {
				return Output{Report: passing(), Rebase: true}, nil
			}))
			p := build(t, Options{Stages: s, Budget: 100})
			exec, err := p.Executor(graph.NewMemoryStore())
			if err != nil {
				t.Fatalf("Executor: %v", err)
			}
			state, err := p.NewState(complete())
			if err != nil {
				t.Fatalf("NewState: %v", err)
			}
			if _, err := exec.Run(context.Background(), "run", state); !errors.Is(err, ErrRebaseNotOffered) {
				t.Fatalf("%s asking for the path back: %v, want ErrRebaseNotOffered", stage, err)
			}
		})
	}
	if !StageCI.RebasesAfterPush() {
		t.Fatalf("the checks stage is not given the path back, so nothing can take it")
	}
}

// A hold is never traded for a rebase: a checks report that holds routes to
// the hold whatever the body asked, and a report with a fix finding routes as
// it would without the request. The control is the bounded-path test above,
// where the same request over a passing report takes the path.
func TestAHoldOrAFixWinsOverARequestToGoBackToRebase(t *testing.T) {
	for _, tc := range []struct {
		action findings.Action
		want   string
	}{
		{findings.ActionAsk, StageCI.HoldNode()},
		{findings.ActionFix, StageCI.HoldNode()},
	} {
		t.Run(string(tc.action), func(t *testing.T) {
			c := newCalls()
			s := recordingStages(c)
			set(&s, StageCI, recording(c, nil, nil, func(Input, int) (Output, error) {
				return Output{Report: reportWith(tc.action, "something the stage found"), Rebase: true}, nil
			}))
			_, result := start(t, build(t, Options{Stages: s, Budget: 100}), complete())
			if result.Status != graph.StatusHalted || result.Position != tc.want {
				t.Fatalf("the run is %s at %q, want halted at %q", result.Status, result.Position, tc.want)
			}
			if got := c.stageCount(StageRebase); got != 1 {
				t.Fatalf("rebase ran %d times, want once: the request should not have been taken", got)
			}
		})
	}
}
