package pipeline

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/graph"
)

// notStartedHold is the shape of the usage ceiling's hold as internal/machine
// builds it: an ask for a person, over a remedy outside the change, standing
// in for an agent call that never started.
func notStartedHold() findings.Finding {
	return findings.Finding{
		ID:                  "usage-ceiling",
		Severity:            findings.SeverityError,
		Action:              findings.ActionAsk,
		RemedyOutsideChange: true,
		NotStarted:          true,
		Description:         "This step did not start the agent call it needed.",
	}
}

// refusedReport is what a stage body the ceiling refused reports instead.
func refusedReport() findings.Report {
	return findings.Report{Summary: "not started", Findings: []findings.Finding{notStartedHold()}}
}

// TestEveryStageHoldIsBuiltWithABoundedRetryCycle is the retry's shape as
// built: every one of the nine holds declares the retry answer, and the cycle
// it starts, hold to retry node to stage, carries RetryRounds on both edges,
// with the edge out of the hold guarded on the answer itself and the edge
// back the cycle's back edge, exempt from convergence as an answered edge.
func TestEveryStageHoldIsBuiltWithABoundedRetryCycle(t *testing.T) {
	for _, fixer := range []bool{false, true} {
		o := Options{Stages: recordingStages(newCalls()), Budget: 100}
		if fixer {
			o.Fixer = recordingFixer(newCalls(), nil, nil, nil)
		}
		g := build(t, o).Graph()
		edges := g.Edges()
		for _, stage := range Order() {
			hold, ok := g.Node(stage.HoldNode())
			if !ok || hold.Halt == nil || !slices.Contains(hold.Halt.Options, string(OutcomeRetry)) {
				t.Fatalf("with a fixer %v, the %s stage's hold offers %+v, want the retry declared", fixer, stage, hold.Halt)
			}
			out, back := -1, -1
			for i, e := range edges {
				switch {
				case e.From == stage.HoldNode() && e.To == stage.RetryNode():
					out = i
				case e.From == stage.RetryNode():
					back = i
				}
			}
			if out < 0 || back < 0 {
				t.Fatalf("the %s stage has no retry cycle: out %d, back %d", stage, out, back)
			}
			e := edges[out]
			if e.Rounds != RetryRounds || e.Guard == nil || e.Guard.Key != string(stage.AnswerKey()) ||
				!e.Guard.Value.Equal(graph.TextValue(string(OutcomeRetry))) || g.IsBackEdge(out) {
				t.Fatalf("the %s stage's edge out of its hold on a retry is %+v (back %v)", stage, e, g.IsBackEdge(out))
			}
			e = edges[back]
			if e.To != stage.Node() || e.Rounds != RetryRounds || !e.Answered || !g.IsBackEdge(back) {
				t.Fatalf("the %s stage's retry return is %+v (back %v), want a bounded answered back edge to the stage",
					stage, e, g.IsBackEdge(back))
			}
		}
	}
}

// TestARetryCycleWithoutItsBoundFailsToBuild is the construction-time half of
// the retry's bound: the pipeline's own topology, rebuilt with nothing but the
// review stage's retry return unbounded, is refused by internal/graph as an
// unbounded cycle naming that edge. The same rebuild unchanged builds, which
// is the control that the refusal is the missing bound's and not the
// rebuild's.
func TestARetryCycleWithoutItsBoundFailsToBuild(t *testing.T) {
	g := build(t, Options{Stages: recordingStages(newCalls()), Budget: 100}).Graph()
	rebuild := func(unbound bool) error {
		b := graph.NewBuilder().Start(g.Start())
		for _, k := range g.Keys() {
			b.Key(k)
		}
		for _, n := range g.Nodes() {
			b.Node(n)
		}
		for _, e := range g.Edges() {
			if unbound && e.From == StageReview.RetryNode() {
				e.Rounds = 0
			}
			b.Edge(e)
		}
		_, err := b.Build()
		return err
	}
	if err := rebuild(false); err != nil {
		t.Fatalf("the pipeline's topology rebuilt unchanged does not build: %v", err)
	}
	err := rebuild(true)
	var built *graph.BuildError
	if !errors.As(err, &built) {
		t.Fatalf("the topology with an unbounded retry return built (%v), want it refused", err)
	}
	for _, v := range built.Violations {
		if v.Rule == graph.RuleBoundedCycle && strings.Contains(v.Detail, StageReview.RetryNode()) {
			return
		}
	}
	t.Fatalf("the refusal is %v, want the bounded-cycle rule naming %q", err, StageReview.RetryNode())
}

// TestARetryAtTheCeilingsHoldRunsTheStageAgain is PRD section 5's Retry: a
// stage whose call never started holds, the hold offers the retry, and the
// answer runs the stage again from the top in the same run, which goes on as
// it would have had the call started the first time.
func TestARetryAtTheCeilingsHoldRunsTheStageAgain(t *testing.T) {
	c := newCalls()
	all := recordingStages(c)
	set(&all, StageReview, recording(c, nil, nil, func(_ Input, call int) (Output, error) {
		if call == 1 {
			return Output{Report: refusedReport()}, nil
		}
		return Output{Report: passing()}, nil
	}))
	p := build(t, Options{Stages: all, Fixer: recordingFixer(c, nil, nil, nil), Budget: 200})
	exec, held := start(t, p, complete())
	if held.Position != StageReview.HoldNode() {
		t.Fatalf("the run stopped at %q, want the review stage's hold", held.Position)
	}
	if offered, err := RetryOffered(held.State, StageReview); err != nil || !offered {
		t.Fatalf("the ceiling's hold offers a retry: %v (%v), want true", offered, err)
	}
	if err := CheckRetry(held.State, StageReview); err != nil {
		t.Fatalf("CheckRetry at the ceiling's hold: %v", err)
	}

	retried, err := exec.Answer(context.Background(), "run", string(OutcomeRetry))
	if err != nil {
		t.Fatalf("answering the ceiling's hold with a retry: %v", err)
	}
	if n := c.stageCount(StageReview); n != 2 {
		t.Fatalf("the review stage ran %d times, want twice", n)
	}
	if retried.Status != graph.StatusCompleted {
		t.Fatalf("after the retry the run is %s at %q, want it completed", retried.Status, retried.Position)
	}
	if got := StageOutcome(retried.State, StageReview); got != OutcomePassed {
		t.Fatalf("the retried review's outcome is %s, want passed", got)
	}
}

// TestARetryWithNoRoomHoldsAgainUntilTheBound is the retry while the ceiling
// still has no room: each retry runs the stage, whose call is refused again,
// and the run holds on the same question rather than looping or parking
// converged, though the state it holds in is the state it held in before.
// The retry past RetryRounds is refused by internal/graph with the decision
// still open, and approve still answers it.
func TestARetryWithNoRoomHoldsAgainUntilTheBound(t *testing.T) {
	c := newCalls()
	all := recordingStages(c)
	set(&all, StageReview, recording(c, nil, nil, func(Input, int) (Output, error) {
		return Output{Report: refusedReport()}, nil
	}))
	p := build(t, Options{Stages: all, Budget: 200})
	exec, _ := start(t, p, complete())
	for i := 1; i <= RetryRounds; i++ {
		again, err := exec.Answer(context.Background(), "run", string(OutcomeRetry))
		if err != nil {
			t.Fatalf("retry %d: %v", i, err)
		}
		if again.Status != graph.StatusHalted || again.Position != StageReview.HoldNode() {
			t.Fatalf("after retry %d the run is %s at %q, want held at review again", i, again.Status, again.Position)
		}
		if !recordedReport(t, again.State, StageReview).HasHeldNotStarted() {
			t.Fatalf("after retry %d the hold is not the ceiling's again", i)
		}
	}
	if n := c.stageCount(StageReview); n != 1+RetryRounds {
		t.Fatalf("the review stage ran %d times, want once and once per retry", n)
	}
	if _, err := exec.Answer(context.Background(), "run", string(OutcomeRetry)); !errors.Is(err, graph.ErrRoundsSpent) {
		t.Fatalf("the retry past the bound returned %v, want graph.ErrRoundsSpent", err)
	}
	resumed, err := exec.Resume(context.Background(), "run")
	if err != nil || resumed.Status != graph.StatusHalted || resumed.Decision == nil {
		t.Fatalf("after the refused retry the run is %+v (%v), want its decision still open", resumed.Status, err)
	}
	approved, err := exec.Answer(context.Background(), "run", string(OutcomeApproved))
	if err != nil || approved.Status != graph.StatusCompleted {
		t.Fatalf("approving after the bound: %v, run %s", err, approved.Status)
	}
}

// TestARetryRunsTheRoundTheCeilingRefused is the retry over an automatic
// fix round the ceiling refused, where the stage still has something to fix.
// The round leaves the ceiling's hold for the stage's next execution, which
// holds on it; the retry drops that hold, the stage runs first and reports
// the finding again, and the round is taken again over the fresh report's
// fix-eligible findings, then the stage runs to see what it did, so with room
// the run goes on rather than holding again on the refusal it was answering.
// The round limit is one, the round the refusal already counted, so a round
// counted a second time would park the run rounds-exhausted in front of the
// fixer.
func TestARetryRunsTheRoundTheCeilingRefused(t *testing.T) {
	c := newCalls()
	all := recordingStages(c)
	set(&all, StageReview, recording(c, nil, nil, func(_ Input, call int) (Output, error) {
		if call >= 4 {
			return Output{Report: passing()}, nil
		}
		return Output{Report: reportWith(findings.ActionFix, "rename the helper")}, nil
	}))
	var retried []findings.Finding
	p := build(t, Options{
		Stages: all,
		Fixer: recordingFixer(c, nil, nil, func(in FixInput, call int) (FixOutput, error) {
			if call == 1 {
				return FixOutput{Summary: "no fix round ran", Holds: []findings.Finding{notStartedHold()}}, nil
			}
			retried = in.Findings
			return FixOutput{Summary: "renamed it"}, nil
		}),
		Rounds: rounds(1),
		Budget: 200,
	})
	exec, held := start(t, p, complete())
	if held.Position != StageReview.HoldNode() || !recordedReport(t, held.State, StageReview).HasHeldNotStarted() {
		t.Fatalf("the run stopped at %q, want the review stage's hold over the refused round", held.Position)
	}
	done, err := exec.Answer(context.Background(), "run", string(OutcomeRetry))
	if err != nil {
		t.Fatalf("answering with a retry: %v", err)
	}
	if done.Status != graph.StatusCompleted {
		t.Fatalf("after the retry the run is %s at %q (%s), want it completed", done.Status, done.Position, done.Reason)
	}
	if n := c.fixCount(StageReview); n != 2 {
		t.Fatalf("the review stage's fixer ran %d times, want the refused round and the retried one", n)
	}
	if len(retried) != 1 || retried[0].Description != "rename the helper" {
		t.Fatalf("the retried round was given %+v, want the finding the stage recorded", retried)
	}
	if n := c.stageCount(StageReview); n != 4 {
		t.Fatalf("the review stage ran %d times, want once, once after the refused round, once for the retry, "+
			"and once after the retried round", n)
	}
}

// TestARetryKeepsTheHoldARequestedRoundLeft is the other half of what the
// retry node keeps: a fix round a person asked for leaves a hold so its
// result is seen, and when the stage's next execution is refused by the
// ceiling, a retry with room runs the stage again and holds on that round's
// hold still, rather than carrying the round past the person unseen.
func TestARetryKeepsTheHoldARequestedRoundLeft(t *testing.T) {
	c := newCalls()
	all := recordingStages(c)
	set(&all, StageLint, recording(c, nil, nil, func(_ Input, call int) (Output, error) {
		switch call {
		case 1:
			return Output{Report: findings.Report{Summary: "one ask", Findings: []findings.Finding{
				{ID: "decide", Action: findings.ActionAsk, Description: "decide this"},
			}}}, nil
		case 2:
			return Output{Report: refusedReport()}, nil
		}
		return Output{Report: passing()}, nil
	}))
	p := build(t, Options{
		Stages: all,
		Fixer:  recordingFixer(c, nil, nil, func(FixInput, int) (FixOutput, error) { return FixOutput{Summary: "decided it"}, nil }),
		Rounds: rounds(0),
		Budget: 200,
	})
	exec, _ := start(t, p, complete())
	refused, err := exec.AnswerWith(context.Background(), "run", string(OutcomeFixRequested),
		encodedRequest(t, FixRequest{Selected: []string{"decide"}}))
	if err != nil || refused.Position != StageLint.HoldNode() {
		t.Fatalf("the requested round left the run at %q (%v), want the lint stage's hold", refused.Position, err)
	}
	retried, err := exec.Answer(context.Background(), "run", string(OutcomeRetry))
	if err != nil {
		t.Fatalf("answering with a retry: %v", err)
	}
	if retried.Status != graph.StatusHalted || retried.Position != StageLint.HoldNode() {
		t.Fatalf("after the retry the run is %s at %q, want held at lint for the round's result", retried.Status, retried.Position)
	}
	report := recordedReport(t, retried.State, StageLint)
	if report.HasHeldNotStarted() {
		t.Fatalf("the retried lint report still holds on the ceiling: %+v", report.Findings)
	}
	if _, ok, err := FixReviewFinding(retried.State, StageLint); err != nil || !ok {
		t.Fatalf("the retried lint report carries no hold for the requested round's result (%v): %+v", err, report.Findings)
	}
}

// TestARetryIsNotOfferedAtAnOrdinaryHold is the retry refused where PRD
// section 5 does not offer it: a hold over an ask the stage found offers no
// retry, CheckRetry refuses it with ErrRetryNotOffered, and a caller that
// answers the graph without asking is refused again by the retry node, so
// the stage does not run again on that answer.
func TestARetryIsNotOfferedAtAnOrdinaryHold(t *testing.T) {
	c := newCalls()
	all := recordingStages(c)
	set(&all, StageLint, heldLint(c))
	p := build(t, Options{Stages: all, Budget: 200})
	exec, held := start(t, p, complete())
	if offered, err := RetryOffered(held.State, StageLint); err != nil || offered {
		t.Fatalf("an ordinary hold offers a retry: %v (%v), want false", offered, err)
	}
	if err := CheckRetry(held.State, StageLint); !errors.Is(err, ErrRetryNotOffered) {
		t.Fatalf("CheckRetry at an ordinary hold returned %v, want ErrRetryNotOffered", err)
	}
	if _, err := exec.Answer(context.Background(), "run", string(OutcomeRetry)); !errors.Is(err, ErrRetryNotOffered) {
		t.Fatalf("a retry answered past the check returned %v, want ErrRetryNotOffered from the retry node", err)
	}
	if n := c.stageCount(StageLint); n != 1 {
		t.Fatalf("the lint stage ran %d times, want the refused retry to have run it no further", n)
	}
}

// TestARetryAfterTheStageWasRefusedTooStillTakesTheRefusedRound is the retry
// when the ceiling refused a stage's automatic fix round and then the
// stage's own call when it ran again, so the hold carries both refusals. The
// retry runs the stage first, which with room reports the finding again, and
// the refused round is still owed, so it is taken as the round the refusal
// counted: with a round limit of one, the run goes on rather than parking
// rounds-exhausted in front of the fixer.
func TestARetryAfterTheStageWasRefusedTooStillTakesTheRefusedRound(t *testing.T) {
	c := newCalls()
	all := recordingStages(c)
	set(&all, StageReview, recording(c, nil, nil, func(_ Input, call int) (Output, error) {
		switch {
		case call == 2:
			return Output{Report: refusedReport()}, nil
		case call >= 4:
			return Output{Report: passing()}, nil
		}
		return Output{Report: reportWith(findings.ActionFix, "rename the helper")}, nil
	}))
	p := build(t, Options{
		Stages: all,
		Fixer: recordingFixer(c, nil, nil, func(_ FixInput, call int) (FixOutput, error) {
			if call == 1 {
				return FixOutput{Summary: "no fix round ran", Holds: []findings.Finding{notStartedHold()}}, nil
			}
			return FixOutput{Summary: "renamed it"}, nil
		}),
		Rounds: rounds(1),
		Budget: 200,
	})
	exec, held := start(t, p, complete())
	if held.Position != StageReview.HoldNode() || !recordedReport(t, held.State, StageReview).HasHeldNotStarted() {
		t.Fatalf("the run stopped at %q, want the review stage's hold over both refusals", held.Position)
	}
	done, err := exec.Answer(context.Background(), "run", string(OutcomeRetry))
	if err != nil {
		t.Fatalf("answering with a retry: %v", err)
	}
	if done.Status != graph.StatusCompleted {
		t.Fatalf("after the retry the run is %s at %q (%s), want it completed", done.Status, done.Position, done.Reason)
	}
	if n := c.fixCount(StageReview); n != 2 {
		t.Fatalf("the fixer ran %d times, want the refused round and the one taken in its place", n)
	}
	if n := c.stageCount(StageReview); n != 4 {
		t.Fatalf("the review stage ran %d times, want twice before the hold, once for the retry, once after the round", n)
	}
}

// TestARetryOverARefusedRoundHoldsUntilTheBound is the retry while the
// ceiling still has no room for a stage's fixer rather than its body: the
// lint stage runs a command, reports a fix-eligible finding every time, and
// takes the default three automatic rounds, and every fix round's agent call
// is refused. Each retry runs the stage, which takes the refused round again,
// whose call is refused again, and the run holds on the same question, as
// decision 25 says, rather than parking converged on the round's way back,
// which repeats the round before the retry, or rounds-exhausted in front of
// the fixer: neither is a loop nobody is driving, and either park would
// take the hold, and approve, skip, and cancel with it. The retry past
// RetryRounds is refused with the decision still open.
func TestARetryOverARefusedRoundHoldsUntilTheBound(t *testing.T) {
	c := newCalls()
	all := recordingStages(c)
	set(&all, StageLint, recording(c, nil, nil, func(Input, int) (Output, error) {
		return Output{Report: reportWith(findings.ActionFix, "lint exits 1")}, nil
	}))
	p := build(t, Options{
		Stages: all,
		Fixer: recordingFixer(c, nil, nil, func(FixInput, int) (FixOutput, error) {
			return FixOutput{Summary: "no fix round ran", Holds: []findings.Finding{notStartedHold()}}, nil
		}),
		Rounds: rounds(3),
		Budget: 200,
	})
	exec, held := start(t, p, complete())
	if held.Status != graph.StatusHalted || held.Position != StageLint.HoldNode() {
		t.Fatalf("the run is %s at %q, want held at lint over the refused round", held.Status, held.Position)
	}
	for i := 1; i <= RetryRounds; i++ {
		again, err := exec.Answer(context.Background(), "run", string(OutcomeRetry))
		if err != nil {
			t.Fatalf("retry %d: %v", i, err)
		}
		if again.Status != graph.StatusHalted || again.Position != StageLint.HoldNode() {
			t.Fatalf("after retry %d the run is %s at %q (%s), want held at lint again",
				i, again.Status, again.Position, again.Reason)
		}
		if !recordedReport(t, again.State, StageLint).HasHeldNotStarted() {
			t.Fatalf("after retry %d the hold is not the ceiling's again", i)
		}
	}
	if n := c.fixCount(StageLint); n != 1+RetryRounds {
		t.Fatalf("the lint fixer was asked %d times, want once and once per retry", n)
	}
	if _, err := exec.Answer(context.Background(), "run", string(OutcomeRetry)); !errors.Is(err, graph.ErrRoundsSpent) {
		t.Fatalf("the retry past the bound returned %v, want graph.ErrRoundsSpent", err)
	}
	approved, err := exec.Answer(context.Background(), "run", string(OutcomeApproved))
	if err != nil || approved.Status != graph.StatusCompleted {
		t.Fatalf("approving after the bound: %v, run %s", err, approved.Status)
	}
}

// TestTheRetryBoundIsSpentBeforeTheRerunEntrysBound is rerunEntry's bound
// against the worst case the mechanism allows: every retry runs the stage,
// which takes the refused round again over rerunEntry, and the ceiling
// refuses that round too. Read off the run's counters, the hold's retry exit
// and rerunEntry reach RetryRounds together, and the retry that would let
// rerunEntry be taken a fourth time is refused with the decision still open,
// so the run never parks on rerunEntry's bound. The count of three on
// rerunEntry is also the positive control that the edge was really taken
// rather than left at zero.
func TestTheRetryBoundIsSpentBeforeTheRerunEntrysBound(t *testing.T) {
	c := newCalls()
	all := recordingStages(c)
	set(&all, StageLint, recording(c, nil, nil, func(Input, int) (Output, error) {
		return Output{Report: reportWith(findings.ActionFix, "lint exits 1")}, nil
	}))
	p := build(t, Options{
		Stages: all,
		Fixer: recordingFixer(c, nil, nil, func(FixInput, int) (FixOutput, error) {
			return FixOutput{Summary: "no fix round ran", Holds: []findings.Finding{notStartedHold()}}, nil
		}),
		Rounds: rounds(3),
		Budget: 200,
	})
	retryExit, rerun := -1, -1
	for i, e := range p.Graph().Edges() {
		switch {
		case e.From == StageLint.HoldNode() && e.To == StageLint.RetryNode():
			retryExit = i
		case e.From == StageLint.Node() && e.Guard != nil &&
			e.Guard.Value.Equal(graph.TextValue(string(OutcomeRerun))):
			rerun = i
		}
	}
	if retryExit < 0 || rerun < 0 {
		t.Fatalf("the lint stage has no retry exit (%d) or no rerun entry (%d)", retryExit, rerun)
	}
	if got := p.Graph().Edges()[rerun].Rounds; got != RetryRounds {
		t.Fatalf("rerunEntry is bounded at %d, want RetryRounds", got)
	}
	store := graph.NewMemoryStore()
	exec, err := p.Executor(store)
	if err != nil {
		t.Fatalf("Executor: %v", err)
	}
	state, err := p.NewState(complete())
	if err != nil {
		t.Fatalf("NewState: %v", err)
	}
	if _, err := exec.Run(context.Background(), "run", state); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for i := 1; i <= RetryRounds; i++ {
		again, err := exec.Answer(context.Background(), "run", string(OutcomeRetry))
		if err != nil {
			t.Fatalf("retry %d: %v", i, err)
		}
		if again.Status != graph.StatusHalted || again.Position != StageLint.HoldNode() {
			t.Fatalf("after retry %d the run is %s at %q (%s), want held at lint again",
				i, again.Status, again.Position, again.Reason)
		}
	}
	cp, err := store.Latest(context.Background(), "run")
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if got := cp.Counters.Traversals[retryExit]; got != RetryRounds {
		t.Fatalf("the hold's retry exit was taken %d times, want %d", got, RetryRounds)
	}
	if got := cp.Counters.Traversals[rerun]; got != RetryRounds {
		t.Fatalf("rerunEntry was taken %d times, want %d, once per retry", got, RetryRounds)
	}
	if _, err := exec.Answer(context.Background(), "run", string(OutcomeRetry)); !errors.Is(err, graph.ErrRoundsSpent) {
		t.Fatalf("the retry past the bound returned %v, want graph.ErrRoundsSpent", err)
	}
	after, err := store.Latest(context.Background(), "run")
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if after.Position != StageLint.HoldNode() || after.Counters.Traversals[rerun] != RetryRounds {
		t.Fatalf("after the refused retry the run stands at %q with rerunEntry taken %d times, want the hold unchanged",
			after.Position, after.Counters.Traversals[rerun])
	}
}

// TestARetryDoesNotRunARefusedRequestedRoundAgain is the other refused round:
// one a person asked for, which the ceiling refused. Its request is not what
// a retry carries, so the retry runs no round; it drops the ceiling's hold
// and keeps the hold the requested round left, and the stage holds on that,
// so the person sees that their round did not run and can ask for it again.
// The stage takes an automatic round and reports a fix-eligible finding
// beside the ask every time, so this is decided by what the held key carries
// and not by the stage having no fixer to run or nothing to run it over.
func TestARetryDoesNotRunARefusedRequestedRoundAgain(t *testing.T) {
	c := newCalls()
	all := recordingStages(c)
	set(&all, StageLint, recording(c, nil, nil, func(Input, int) (Output, error) {
		return Output{Report: findings.Report{Summary: "an ask and a fix", Findings: []findings.Finding{
			{ID: "decide", Severity: findings.SeverityError, Action: findings.ActionAsk, Description: "decide this"},
			{ID: "rename", Severity: findings.SeverityError, Action: findings.ActionFix, Description: "rename the helper"},
		}}}, nil
	}))
	p := build(t, Options{
		Stages: all,
		Fixer: recordingFixer(c, nil, nil, func(FixInput, int) (FixOutput, error) {
			return FixOutput{Summary: "no fix round ran", Holds: []findings.Finding{notStartedHold()}}, nil
		}),
		Rounds: rounds(1),
		Budget: 200,
	})
	exec, _ := start(t, p, complete())
	refused, err := exec.AnswerWith(context.Background(), "run", string(OutcomeFixRequested),
		encodedRequest(t, FixRequest{Selected: []string{"decide"}}))
	if err != nil || refused.Position != StageLint.HoldNode() {
		t.Fatalf("the refused requested round left the run at %q (%v), want the lint stage's hold", refused.Position, err)
	}
	if !recordedReport(t, refused.State, StageLint).HasHeldNotStarted() {
		t.Fatalf("the refused requested round did not leave the ceiling's hold")
	}
	retried, err := exec.Answer(context.Background(), "run", string(OutcomeRetry))
	if err != nil {
		t.Fatalf("answering with a retry: %v", err)
	}
	if retried.Status != graph.StatusHalted || retried.Position != StageLint.HoldNode() {
		t.Fatalf("after the retry the run is %s at %q, want held at lint for the requested round", retried.Status, retried.Position)
	}
	if n := c.fixCount(StageLint); n != 1 {
		t.Fatalf("the lint fixer ran %d times, want only the refused requested round", n)
	}
	if recordedReport(t, retried.State, StageLint).HasHeldNotStarted() {
		t.Fatalf("after the retry the lint report still holds on the ceiling")
	}
	if _, ok, err := FixReviewFinding(retried.State, StageLint); err != nil || !ok {
		t.Fatalf("after the retry the lint report carries no hold for the requested round (%v)", err)
	}
}

// TestARetryRunsTheStageBeforeTheRoundTheCeilingRefused is PRD section 5's
// Retry, "the stage runs from the top", over an automatic fix round the
// ceiling refused. Lint's failure clears before the retry, so the stage,
// run first, passes, and the run goes on without the fixer: nothing is left
// for a round to fix. Every fixer call is refused here, which is the same run
// with room or without it, because no fixer call is made.
func TestARetryRunsTheStageBeforeTheRoundTheCeilingRefused(t *testing.T) {
	c := newCalls()
	all := recordingStages(c)
	set(&all, StageLint, recording(c, nil, nil, func(_ Input, call int) (Output, error) {
		if call <= 2 {
			return Output{Report: reportWith(findings.ActionFix, "lint exits 1")}, nil
		}
		return Output{Report: passing()}, nil
	}))
	p := build(t, Options{
		Stages: all,
		Fixer: recordingFixer(c, nil, nil, func(FixInput, int) (FixOutput, error) {
			return FixOutput{Summary: "no fix round ran", Holds: []findings.Finding{notStartedHold()}}, nil
		}),
		Rounds: rounds(3),
		Budget: 200,
	})
	exec, held := start(t, p, complete())
	if held.Position != StageLint.HoldNode() || !recordedReport(t, held.State, StageLint).HasHeldNotStarted() {
		t.Fatalf("the run stopped at %q, want the lint stage's hold over the refused round", held.Position)
	}
	done, err := exec.Answer(context.Background(), "run", string(OutcomeRetry))
	if err != nil {
		t.Fatalf("answering with a retry: %v", err)
	}
	if done.Status != graph.StatusCompleted {
		t.Fatalf("after the retry the run is %s at %q (%s), want it completed", done.Status, done.Position, done.Reason)
	}
	if n := c.fixCount(StageLint); n != 1 {
		t.Fatalf("the lint fixer was asked %d times, want only the refused round: the stage passed first", n)
	}
	if n := c.stageCount(StageLint); n != 3 {
		t.Fatalf("lint ran %d times, want once, once after the refused round, and once for the retry", n)
	}
}

// TestARoundTakenAgainSpendsTheRoundTheRefusalCounted is the other side of
// taking a refused round again: it is that round, not one more. The review
// stage reports a fix-eligible finding every time and its round limit is
// one; the first round is refused, and after the retry the round taken in
// its place starts. The stage still reports the finding, so the run parks
// rounds-exhausted in front of the fixer, as a stage that ran its one round
// does, rather than running a second round with the mark still set.
func TestARoundTakenAgainSpendsTheRoundTheRefusalCounted(t *testing.T) {
	c := newCalls()
	all := recordingStages(c)
	set(&all, StageReview, recording(c, nil, nil, func(Input, int) (Output, error) {
		return Output{Report: reportWith(findings.ActionFix, "rename the helper")}, nil
	}))
	p := build(t, Options{
		Stages: all,
		Fixer: recordingFixer(c, nil, nil, func(_ FixInput, call int) (FixOutput, error) {
			if call == 1 {
				return FixOutput{Summary: "no fix round ran", Holds: []findings.Finding{notStartedHold()}}, nil
			}
			return FixOutput{Summary: "renamed it"}, nil
		}),
		Rounds: rounds(1),
		Budget: 200,
	})
	exec, held := start(t, p, complete())
	if held.Position != StageReview.HoldNode() {
		t.Fatalf("the run stopped at %q, want the review stage's hold over the refused round", held.Position)
	}
	parked, err := exec.Answer(context.Background(), "run", string(OutcomeRetry))
	if err != nil {
		t.Fatalf("answering with a retry: %v", err)
	}
	if parked.Status != graph.StatusRoundsExhausted || parked.Position != StageReview.FixNode() {
		t.Fatalf("after the retry the run is %s at %q (%s), want it parked rounds-exhausted in front of the fixer",
			parked.Status, parked.Position, parked.Reason)
	}
	if n := c.fixCount(StageReview); n != 2 {
		t.Fatalf("the fixer ran %d times, want the refused round and the one taken in its place", n)
	}
}
