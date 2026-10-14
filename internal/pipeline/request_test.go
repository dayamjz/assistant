package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/graph"
)

// heldLint is a lint stage that reports one ask and one note on its first
// execution and nothing at all after, so the only thing that can hold it the
// second time is what the requested round left behind.
func heldLint(c *calls) Implementation {
	return recording(c, nil, nil, func(_ Input, call int) (Output, error) {
		if call > 1 {
			return Output{Report: passing()}, nil
		}
		return Output{Report: findings.Report{
			Summary: "two findings",
			Findings: []findings.Finding{
				{ID: "decide", Severity: findings.SeverityError, Action: findings.ActionAsk, Description: "decide this"},
				{ID: "aside", Severity: findings.SeverityInfo, Action: findings.ActionNote, Description: "an aside"},
			},
		}}, nil
	})
}

// encodedRequest encodes a request or fails the test.
func encodedRequest(t *testing.T, r FixRequest) string {
	t.Helper()
	text, err := r.Encode()
	if err != nil {
		t.Fatalf("encoding a fix request: %v", err)
	}
	return text
}

// A fix round a person asks for at a hold is PRD section 5's Fix action: the
// stage's fixer runs over what they selected, with their notes and their own
// findings and with what they left unselected named, the stage runs again, and
// it holds again so they see the result.
//
// The stage passes cleanly on its second execution, so the hold after the
// round is the round's doing and not the stage's. The fixer's own hold - the
// route the judge rule takes - is carried to the same report, which is the
// part of "decided from the commit" this package owns.
func TestAFixRoundAskedForAtAHoldRunsTheFixerAndHoldsAgain(t *testing.T) {
	c := newCalls()
	all := recordingStages(c)
	set(&all, StageLint, heldLint(c))
	var got []FixInput
	p := build(t, Options{
		Stages: all,
		Fixer: recordingFixer(c, nil, nil, func(in FixInput, _ int) (FixOutput, error) {
			got = append(got, in)
			return FixOutput{Summary: "decided it", Holds: []findings.Finding{{
				Action: findings.ActionAsk, Description: "the round changed a_test.go",
			}}}, nil
		}),
		Rounds: rounds(0),
		Budget: 200,
	})
	exec, result := start(t, p, complete())
	if result.Position != StageLint.HoldNode() {
		t.Fatalf("the run stopped at %q, want the lint stage's hold", result.Position)
	}
	if !strings.Contains(strings.Join(result.Decision.Options, ","), string(OutcomeFixRequested)) {
		t.Fatalf("the hold offers %v, and a pipeline with a fixer offers a fix round", result.Decision.Options)
	}

	answered, err := exec.AnswerWith(context.Background(), "run", string(OutcomeFixRequested), encodedRequest(t, FixRequest{
		Selected: []string{"decide"},
		Notes:    "fix the code, not the test",
		Findings: []findings.Finding{{Severity: findings.SeverityWarning, Description: "and rename the helper"}},
	}))
	if err != nil {
		t.Fatalf("answering with a fix round: %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("the fixer ran %d times, want once", len(got))
	}
	in := got[0]
	if in.Stage != StageLint || in.Requested == nil {
		t.Fatalf("the fixer was given stage %s and request %+v, want the lint stage's requested round", in.Stage, in.Requested)
	}
	if len(in.Findings) != 2 || in.Findings[0].ID != "decide" || in.Findings[1].Description != "and rename the helper" {
		t.Fatalf("the round was asked to resolve %+v, want the selected ask and the person's own finding", in.Findings)
	}
	if in.Requested.Notes != "fix the code, not the test" {
		t.Fatalf("the round was given the notes %q", in.Requested.Notes)
	}
	if len(in.Requested.Unselected) != 1 || in.Requested.Unselected[0].ID != "aside" {
		t.Fatalf("the round was told %+v was left unselected, want the note", in.Requested.Unselected)
	}

	if answered.Status != graph.StatusHalted || answered.Position != StageLint.HoldNode() {
		t.Fatalf("after the round the run is %s at %q, want held at the lint stage again",
			answered.Status, answered.Position)
	}
	if n := c.stageCount(StageLint); n != 2 {
		t.Fatalf("the lint stage ran %d times, want twice: once before the round and once after", n)
	}
	report := recordedReport(t, answered.State, StageLint)
	var asked, judged bool
	for _, f := range report.Findings {
		asked = asked || (f.Holds() && strings.Contains(f.Description, "You asked for a fix round"))
		judged = judged || (f.Holds() && strings.Contains(f.Description, "a_test.go"))
	}
	if !asked || !judged {
		t.Fatalf("the report after the round is %+v, want the requested round's hold and the fixer's own", report.Findings)
	}
	if got := FixSummary(answered.State, StageLint); got != "decided it" {
		t.Fatalf("the round's summary reads %q", got)
	}

	// The run carries on from the hold the round left, as from any other.
	done, err := exec.Answer(context.Background(), "run", string(OutcomeApproved))
	if err != nil {
		t.Fatalf("approving after the round: %v", err)
	}
	if done.Status != graph.StatusCompleted {
		t.Fatalf("after approving, the run is %s at %q", done.Status, done.Position)
	}
}

// RequestedFixRounds bounds the rounds a person may ask for at one stage, and
// the answer past it is refused with the decision still open rather than
// parking the run: the person can still approve. The rounds before it are the
// positive control, so a bound that refused every request would fail here.
func TestAFixRoundPastTheBoundIsRefusedAndTheHoldStaysOpen(t *testing.T) {
	c := newCalls()
	all := recordingStages(c)
	set(&all, StageLint, recording(c, nil, nil, func(Input, int) (Output, error) {
		return Output{Report: findings.Report{Summary: "one", Findings: []findings.Finding{
			{ID: "decide", Action: findings.ActionAsk, Description: "decide this"},
		}}}, nil
	}))
	p := build(t, Options{
		Stages: all,
		// Each round writes a different head, so convergence is not what
		// stops the requests: the bound under test is.
		Fixer: recordingFixer(c, nil, []Key{KeyHead}, func(_ FixInput, call int) (FixOutput, error) {
			return FixOutput{Summary: "round", Writes: map[Key]graph.Value{
				KeyHead: graph.TextValue("c" + string(rune('0'+call))),
			}}, nil
		}),
		Rounds: rounds(0),
		Budget: 200,
	})
	exec, _ := start(t, p, complete())
	request := encodedRequest(t, FixRequest{Selected: []string{"decide"}})
	for i := range RequestedFixRounds {
		result, err := exec.AnswerWith(context.Background(), "run", string(OutcomeFixRequested), request)
		if err != nil {
			t.Fatalf("requested round %d of %d was refused: %v", i+1, RequestedFixRounds, err)
		}
		if result.Position != StageLint.HoldNode() {
			t.Fatalf("after requested round %d the run is at %q", i+1, result.Position)
		}
	}
	_, err := exec.AnswerWith(context.Background(), "run", string(OutcomeFixRequested), request)
	if !errors.Is(err, graph.ErrRoundsSpent) {
		t.Fatalf("a round past the bound came to %v, want graph.ErrRoundsSpent", err)
	}
	if n := c.fixCount(StageLint); n != RequestedFixRounds {
		t.Fatalf("the fixer ran %d times, want %d", n, RequestedFixRounds)
	}
	done, err := exec.Answer(context.Background(), "run", string(OutcomeApproved))
	if err != nil {
		t.Fatalf("approving after the refused round: %v", err)
	}
	if done.Status != graph.StatusCompleted {
		t.Fatalf("after approving, the run is %s", done.Status)
	}
}

// A fix answer carries what to fix and every other answer carries nothing, and
// both directions are refused before anything is recorded. A pipeline with no
// fixer does not offer the round at all.
func TestAFixRequestTravelsWithTheFixAnswerAndNoOther(t *testing.T) {
	c := newCalls()
	all := recordingStages(c)
	set(&all, StageLint, heldLint(c))
	p := build(t, Options{Stages: all, Fixer: recordingFixer(c, nil, nil, nil), Rounds: rounds(0), Budget: 200})
	exec, _ := start(t, p, complete())
	request := encodedRequest(t, FixRequest{Selected: []string{"decide"}})

	if _, err := exec.AnswerWith(context.Background(), "run", string(OutcomeFixRequested), ""); !errors.Is(err, graph.ErrDetailRequired) {
		t.Fatalf("a fix answer with no request came to %v, want graph.ErrDetailRequired", err)
	}
	if _, err := exec.AnswerWith(context.Background(), "run", string(OutcomeApproved), request); !errors.Is(err, graph.ErrDetailNotAccepted) {
		t.Fatalf("an approval carrying a request came to %v, want graph.ErrDetailNotAccepted", err)
	}
	if n := c.fixCount(StageLint); n != 0 {
		t.Fatalf("a refused answer ran the fixer %d times", n)
	}

	bare := newCalls()
	without := recordingStages(bare)
	set(&without, StageLint, heldLint(bare))
	_, held := start(t, build(t, Options{Stages: without, Rounds: rounds(0), Budget: 200}), complete())
	for _, option := range held.Decision.Options {
		if option == string(OutcomeFixRequested) {
			t.Fatalf("a pipeline with no fixer offers %v", held.Decision.Options)
		}
	}
}

// Select is the rule both the surface answering a hold and the request node
// apply, so its refusals are the ones a person meets.
func TestAFixRequestIsResolvedAgainstTheStagesReport(t *testing.T) {
	report := findings.Report{Summary: "s", Findings: []findings.Finding{
		{ID: "one", Action: findings.ActionAsk, Description: "first"},
		{ID: "two", Action: findings.ActionNote, Description: "second"},
	}}
	for name, tc := range map[string]struct {
		request FixRequest
		want    error
	}{
		"nothing selected":         {FixRequest{Notes: "just notes"}, ErrEmptyFixRequest},
		"a finding never reported": {FixRequest{Selected: []string{"three"}}, ErrUnknownFinding},
		"an own finding with no description": {FixRequest{Findings: []findings.Finding{{Description: "  "}}},
			ErrUnusableFixRequest},
	} {
		if _, err := tc.request.Select(report); !errors.Is(err, tc.want) {
			t.Errorf("%s: Select returned %v, want %v", name, err, tc.want)
		}
	}
	// A stale identifier is refused naming every unknown one in the order it
	// was selected, and every identifier the report does carry, so the caller
	// can correct the selection from the refusal alone.
	_, err := FixRequest{Selected: []string{"four", "one", "three"}}.Select(report)
	if !errors.Is(err, ErrUnknownFinding) {
		t.Fatalf("a stale selection came to %v", err)
	}
	if want := `"four", "three"; the stage reported "one", "two"`; !strings.Contains(err.Error(), want) {
		t.Fatalf("the refusal %q does not say %q", err, want)
	}
	got, err := FixRequest{Selected: []string{"two"}}.Select(report)
	if err != nil {
		t.Fatalf("selecting a reported finding: %v", err)
	}
	if len(got.Resolve) != 1 || got.Resolve[0].ID != "two" || len(got.Unselected) != 1 || got.Unselected[0].ID != "one" {
		t.Fatalf("the selection is %+v", got)
	}
	if _, err := ParseFixRequest("not json"); !errors.Is(err, ErrUnusableFixRequest) {
		t.Fatalf("an unreadable request came to %v", err)
	}
}

// A requested round that changes nothing returns to the hold rather than
// parking the run, so approve, skip, and cancel stay open, which PRD section
// 14's eighth decision promises. Every round the bound admits is asked for
// and none changes anything, which is the case convergence would otherwise
// park on: from the second round on, the stage's report and everything else
// the round leaves are what the round before left.
//
// The hold after each says the round changed nothing. The approval at the end
// is the positive control that the hold is a live decision and not a park
// reported as one.
func TestARequestedRoundThatChangesNothingReturnsToTheHold(t *testing.T) {
	c := newCalls()
	all := recordingStages(c)
	set(&all, StageLint, recording(c, nil, nil, func(Input, int) (Output, error) {
		return Output{Report: findings.Report{Summary: "one", Findings: []findings.Finding{
			{ID: "decide", Action: findings.ActionAsk, Description: "decide this"},
		}}}, nil
	}))
	p := build(t, Options{
		Stages: all,
		Fixer: recordingFixer(c, nil, nil, func(FixInput, int) (FixOutput, error) {
			return FixOutput{Summary: "changed nothing"}, nil
		}),
		Rounds: rounds(0),
		Budget: 200,
	})
	exec, _ := start(t, p, complete())
	request := encodedRequest(t, FixRequest{Selected: []string{"decide"}})
	for i := range RequestedFixRounds {
		result, err := exec.AnswerWith(context.Background(), "run", string(OutcomeFixRequested), request)
		if err != nil {
			t.Fatalf("requested round %d: %v", i+1, err)
		}
		if result.Status != graph.StatusHalted || result.Position != StageLint.HoldNode() {
			t.Fatalf("after no-op requested round %d the run is %s at %q (%s), want held at the lint stage",
				i+1, result.Status, result.Position, result.Reason)
		}
		report := recordedReport(t, result.State, StageLint)
		said := false
		for _, f := range report.Findings {
			said = said || (f.Holds() && strings.Contains(f.Description, "changed nothing in the run's state"))
		}
		if !said {
			t.Fatalf("the hold after no-op round %d does not say the round changed nothing: %+v", i+1, report.Findings)
		}
	}
	if n := c.fixCount(StageLint); n != RequestedFixRounds {
		t.Fatalf("the fixer ran %d times, want %d", n, RequestedFixRounds)
	}
	done, err := exec.Answer(context.Background(), "run", string(OutcomeApproved))
	if err != nil {
		t.Fatalf("approving after the no-op rounds: %v", err)
	}
	if done.Status != graph.StatusCompleted {
		t.Fatalf("after approving, the run is %s", done.Status)
	}
}

// A fix round can be asked for only at a stage before push. Push forwards a
// commit, so a round there would be pushed by the stage's re-run before the
// person saw it, and pr and ci run after push, so a round's commit there
// would never be pushed; PRD section 5 promises the person sees the result.
// The graph is asked rather than a list: no request node exists at those
// stages, their holds do not offer the round, an answer asking for one there
// is refused while the hold stays open, and CheckRequestable names why. Every
// stage before push is the positive control, so a pipeline that offered no
// round anywhere fails here.
func TestAFixRoundCanBeRequestedOnlyBeforePush(t *testing.T) {
	c := newCalls()
	all := recordingStages(c)
	set(&all, StagePush, recording(c, nil, nil, func(Input, int) (Output, error) {
		return Output{Report: findings.Report{Summary: "one", Findings: []findings.Finding{
			{ID: "decide", Action: findings.ActionAsk, Description: "decide this"},
		}}}, nil
	}))
	p := build(t, Options{Stages: all, Fixer: recordingFixer(c, nil, nil, nil), Rounds: rounds(0), Budget: 200})
	g := p.Graph()
	for _, stage := range Order() {
		_, hasNode := g.Node(stage.RequestNode())
		hold, ok := g.Node(stage.HoldNode())
		if !ok {
			t.Fatalf("the %s stage has no hold node", stage)
		}
		offers := false
		for _, option := range hold.Halt.Options {
			offers = offers || option == string(OutcomeFixRequested)
		}
		before := stage < StagePush
		if hasNode != before || offers != before || stage.Requestable() != before {
			t.Errorf("%s: request node %v, hold offers a round %v, requestable %v; want %v for all three",
				stage, hasNode, offers, stage.Requestable(), before)
		}
		err := CheckRequestable(stage)
		if before && err != nil {
			t.Errorf("%s: CheckRequestable refused a stage before push: %v", stage, err)
		}
		if !before && !errors.Is(err, ErrFixNotRequestable) {
			t.Errorf("%s: CheckRequestable came to %v, want ErrFixNotRequestable", stage, err)
		}
	}

	exec, held := start(t, p, complete())
	if held.Position != StagePush.HoldNode() {
		t.Fatalf("the run stopped at %q, want the push stage's hold", held.Position)
	}
	request := encodedRequest(t, FixRequest{Selected: []string{"decide"}})
	if _, err := exec.AnswerWith(context.Background(), "run", string(OutcomeFixRequested), request); !errors.Is(err, graph.ErrAnswerNotAllowed) {
		t.Fatalf("a fix round asked for at push came to %v, want graph.ErrAnswerNotAllowed", err)
	}
	if n := c.fixCount(StagePush); n != 0 {
		t.Fatalf("a refused request ran the fixer %d times", n)
	}
	if done, err := exec.Answer(context.Background(), "run", string(OutcomeApproved)); err != nil || done.Status != graph.StatusCompleted {
		t.Fatalf("approving after the refused request came to %v, %v", done.Status, err)
	}
}
