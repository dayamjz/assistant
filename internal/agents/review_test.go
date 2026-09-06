package agents_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/findings"
)

// The change these reviews are about: one commit, one path the change touched,
// and one caller it did not.
const (
	reviewedRevision = "c0ffeeb4be"
	reviewedPath     = "internal/total/total.go"
	unreviewedCaller = "internal/report/render.go"
)

// reviewDemand is the run's side of a review: the facts a reviewer is held to
// and does not supply.
func reviewDemand() findings.Demand {
	return findings.Demand{Revision: reviewedRevision, Touched: []string{reviewedPath}}
}

// reviewInvocation is a ShapeReview invocation carrying that demand.
func reviewInvocation(t *testing.T) agents.Invocation {
	t.Helper()
	return agents.Invocation{
		Prompt: "Review the change. " + mustGuidance(t),
		Shape:  agents.ShapeReview,
		Review: reviewDemand(),
		Dir:    t.TempDir(),
	}
}

// mustGuidance is what the reviewer is told, which is the text the binding
// holds it to.
func mustGuidance(t *testing.T) string {
	t.Helper()
	text, err := reviewDemand().Guidance()
	if err != nil {
		t.Fatalf("building the evidence guidance: %v", err)
	}
	return text
}

// crossFileReview is a review that declares reading read and reports one
// fix-eligible finding about a caller the change does not touch.
func crossFileReview(read ...string) findings.Report {
	return findings.Report{
		Summary:  "One pass over the change.",
		Revision: reviewedRevision,
		Read:     read,
		Findings: []findings.Finding{{
			ID:          "caller-sums-twice",
			Severity:    findings.SeverityError,
			Action:      findings.ActionFix,
			Location:    findings.Location{Path: unreviewedCaller, Line: 42},
			Description: "The caller adds the same slice a second time now that Total returns it summed.",
		}},
	}
}

// runReview drives one scripted reviewer through the production adapter and
// returns what the adapter made of its bytes.
func runReview(t *testing.T, reply standin.Reply) agents.Result {
	t.Helper()
	agent := standin.New(t, standin.Script{Steps: []standin.Step{{Reply: reply}}})
	result, err := agent.Runner().Run(t.Context(), agents.PurposeReview, reviewInvocation(t))
	if err != nil {
		t.Fatalf("the reviewer answered and the adapter refused it: %v", err)
	}
	return result
}

// TestAReviewerDeclaringTheWholeDiffForfeitsAFindingReachingPastIt is the
// property this shape exists for, driven end to end. The reviewer is a
// process: it prints bytes, the production adapter reads them, and nothing in
// this test states a typed result the adapter could not have built.
//
// The reviewer here declares exactly the paths the change touched, which is
// the declaration a naive rule accepts and which the run could have derived
// without asking it, and then reports a fix-eligible finding about a caller it
// never said it read.
func TestAReviewerDeclaringTheWholeDiffForfeitsAFindingReachingPastIt(t *testing.T) {
	t.Parallel()
	result := runReview(t, standin.Report(crossFileReview(reviewedPath)))

	if got := result.Report.Fixable(); len(got) != 0 {
		t.Fatalf("a finding the reviewer's own evidence does not support reached a caller "+
			"as fix-eligible: %+v", got)
	}
	if len(result.Binding.Refused) != 1 {
		t.Fatalf("expected one refusal, got %+v", result.Binding.Refused)
	}
	if got := result.Binding.Refused[0].Path; got != unreviewedCaller {
		t.Errorf("the refusal names %q, want the path the finding reached for, %q",
			got, unreviewedCaller)
	}
	if result.Binding.ReadBeyondChange() {
		t.Errorf("a reviewer that declared only the touched paths read nothing beyond them, got %q",
			result.Binding.Beyond)
	}
}

// TestAReviewerThatReadThePathItReportsOnKeepsTheFinding is the control for
// the test above, and it is the reason the refusal there is the evidence rule
// rather than something else about that report. One thing differs, the path
// the reviewer declared reading, and the identical finding survives
// fix-eligible.
func TestAReviewerThatReadThePathItReportsOnKeepsTheFinding(t *testing.T) {
	t.Parallel()
	result := runReview(t, standin.Report(crossFileReview(reviewedPath, unreviewedCaller)))

	if len(result.Binding.Refused) != 0 {
		t.Fatalf("nothing should have been refused, got %+v", result.Binding.Refused)
	}
	got := result.Report.Fixable()
	if len(got) != 1 || got[0].ID != "caller-sums-twice" {
		t.Fatalf("the supported finding should still be fix-eligible, got %+v", got)
	}
	if want := []string{unreviewedCaller}; !equalStrings(result.Binding.Beyond, want) {
		t.Errorf("Beyond is %q, want %q", result.Binding.Beyond, want)
	}
}

// TestAReviewOfAnotherRevisionIsRefusedByTheAdapter checks that a report of a
// commit the run did not ask about never becomes a Result at all. A reading of
// some other commit is not a review of this change, so the adapter reports it
// as output it could not use rather than as a review that found nothing.
func TestAReviewOfAnotherRevisionIsRefusedByTheAdapter(t *testing.T) {
	t.Parallel()
	stale := crossFileReview(reviewedPath, unreviewedCaller)
	stale.Revision = "d15ea5e000"
	agent := standin.New(t, standin.Script{Steps: []standin.Step{{Reply: standin.Report(stale)}}})

	_, err := agent.Runner().Run(t.Context(), agents.PurposeReview, reviewInvocation(t))
	var failure *agents.InvocationError
	if !errors.As(err, &failure) {
		t.Fatalf("error is %v, want an *agents.InvocationError", err)
	}
	if failure.Failure != agents.FailureOutput {
		t.Errorf("failure is %q, want %q", failure.Failure, agents.FailureOutput)
	}
	if !errors.Is(err, findings.ErrWrongRevision) {
		t.Errorf("the refusal should carry why internal/findings refused it, got %v", err)
	}
}

// TestP3SurvivesTheReviewBindingOnTheWire is the boundary the evidence rule
// may not cross, checked on the path an agent actually reaches it by. The
// reviewer prints a finding with no action and no location: the evidence rule
// would demote an unsupported finding to a note, and P3 outranks it.
func TestP3SurvivesTheReviewBindingOnTheWire(t *testing.T) {
	t.Parallel()
	unclassified := findings.Report{
		Summary:  "One pass over the change.",
		Revision: reviewedRevision,
		Read:     []string{reviewedPath},
		Findings: []findings.Finding{{
			ID:          "unclassified",
			Severity:    findings.SeverityError,
			Description: "Something about this change worries me.",
		}},
	}
	result := runReview(t, standin.Report(unclassified))

	if !result.Report.HasParked() {
		t.Fatalf("an unclassified finding must still hold the stage for a person: %+v",
			result.Report.Findings)
	}
	if len(result.Binding.Demoted) != 0 {
		t.Errorf("P3's ask was demoted by the evidence rule: %+v", result.Binding.Demoted)
	}
}

// TestAReviewReadInProseIsBoundToo checks that the binding does not depend on
// the reviewer printing nothing but JSON. The shapes internal/findings accepts
// are its own; what this pins is that the review path reaches them all rather
// than only the bare object.
func TestAReviewReadInProseIsBoundToo(t *testing.T) {
	t.Parallel()
	result := runReview(t, standin.Prose(
		"I read the diff and one of its callers.", crossFileReview(reviewedPath)))

	if len(result.Binding.Refused) != 1 {
		t.Fatalf("expected the finding refused for want of evidence, got %+v", result.Binding.Refused)
	}
	if !strings.Contains(result.Text, "I read the diff") {
		t.Errorf("Result.Text should still be the text the report was read out of, got %q", result.Text)
	}
}

// TestAReviewDemandBelongsToTheReviewShapeAlone pins the other half of the
// arrangement: review output is never read unbound, and a demand is never
// attached to output nobody binds. Both are refused before an agent starts.
func TestAReviewDemandBelongsToTheReviewShapeAlone(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		inv  agents.Invocation
	}{
		{"review with no demand", agents.Invocation{
			Prompt: "review the change", Shape: agents.ShapeReview, Dir: t.TempDir()}},
		{"review with a demand naming no revision", agents.Invocation{
			Prompt: "review the change", Shape: agents.ShapeReview, Dir: t.TempDir(),
			Review: findings.Demand{Touched: []string{reviewedPath}}}},
		{"review with a demand naming no touched path", agents.Invocation{
			Prompt: "review the change", Shape: agents.ShapeReview, Dir: t.TempDir(),
			Review: findings.Demand{Revision: reviewedRevision}}},
		{"a report shape carrying a demand", agents.Invocation{
			Prompt: "report on the change", Shape: agents.ShapeReport, Dir: t.TempDir(),
			Review: reviewDemand()}},
		{"a text shape carrying a demand", agents.Invocation{
			Prompt: "say what you think", Shape: agents.ShapeText, Dir: t.TempDir(),
			Review: reviewDemand()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := tc.inv.Validate(); !errors.Is(err, agents.ErrInvalidInvocation) {
				t.Fatalf("Validate is %v, want ErrInvalidInvocation", err)
			}
			agent := standin.New(t, standin.Script{Steps: []standin.Step{{
				Reply: standin.Report(crossFileReview(reviewedPath)),
			}}})
			if _, err := agent.Runner().Run(t.Context(), agents.PurposeReview, tc.inv); err == nil {
				t.Fatal("the invocation ran, want it refused before an agent started")
			}
			if calls := agent.Calls(); len(calls) != 0 {
				t.Errorf("an agent was started for a refused invocation: %+v", calls)
			}
		})
	}
}

// TestAReviewShapeIsRefusedByTheFixer is P4 on the one path the type split
// does not cover by itself. Fixer.Apply takes the same Invocation Runner.Run
// takes, so a review spelled as a shape rather than as a purpose can be handed
// to the entry point whose memory survives the round, which would seat the
// agent that prescribed a fix as the reviewer checking it.
//
// The control is the same Invocation value: one thing differs, the entry point
// it is given to, and through Runner.Run it is answered and bound as usual. So
// the refusal is this rule firing rather than anything else about the
// invocation or the reviewer's report.
func TestAReviewShapeIsRefusedByTheFixer(t *testing.T) {
	t.Parallel()
	agent := standin.New(t, standin.Script{Steps: []standin.Step{{
		Times: standin.Always,
		Reply: standin.Report(crossFileReview(reviewedPath, unreviewedCaller)),
	}}})
	inv := reviewInvocation(t)

	// Both fixer states, because they carry the session differently and each
	// has to refuse. A fixer resuming an earlier round is answered inside a
	// conversation that already exists; a fresh one carries no conversation
	// yet and keeps the one this round reports. Only the second tells apart a
	// rule that asks whether an invocation carries a session at all from one
	// that asks for both facts at once.
	for _, opened := range []string{"a-session-an-earlier-fix-round-opened", ""} {
		fixer, err := agents.OpenFixer(t.Context(), agent.Runner(), opened)
		if err != nil {
			t.Fatalf("opening the fixer session: %v", err)
		}
		_, refusal := fixer.Apply(t.Context(), inv)
		if !errors.Is(refusal, agents.ErrReviewInFixerSession) {
			t.Fatalf("Fixer.Apply resuming %q is %v, want ErrReviewInFixerSession", opened, refusal)
		}
		// The same refusal answers to the class every pre-start refusal
		// belongs to, so a caller handling "we built an invocation that cannot
		// be run" is not silently missing this one for having a name of its
		// own.
		if !errors.Is(refusal, agents.ErrInvalidInvocation) {
			t.Errorf("the refusal is %v, want it in the ErrInvalidInvocation class as well", refusal)
		}
		if calls := agent.Calls(); len(calls) != 0 {
			t.Fatalf("an agent was started for a review handed to the fixer, so the refusal "+
				"came after the process rather than before it: %+v", calls)
		}
	}

	result, err := agent.Runner().Run(t.Context(), agents.PurposeReview, inv)
	if err != nil {
		t.Fatalf("the same invocation should still be answerable session-free: %v", err)
	}
	if got := result.Report.Fixable(); len(got) != 1 {
		t.Fatalf("the session-free review should have produced its finding, got %+v", got)
	}
	if len(agent.Calls()) != 1 {
		t.Errorf("expected exactly the one agent the session-free review started, got %+v",
			agent.Calls())
	}
}

// TestTheFixerStillRunsAnOrdinaryFixRound is the other half of the guard's
// discrimination: refusing the review shape does not refuse the shapes a fix
// round is made of, and the round still opens the session it is for.
func TestTheFixerStillRunsAnOrdinaryFixRound(t *testing.T) {
	t.Parallel()
	agent := standin.New(t, standin.Script{Steps: []standin.Step{{
		Times: standin.Always,
		Reply: standin.Text("applied the finding"),
	}}})
	fixer, err := agents.OpenFixer(t.Context(), agent.Runner(), "")
	if err != nil {
		t.Fatalf("opening the fixer session: %v", err)
	}

	result, err := fixer.Apply(t.Context(), agents.Invocation{
		Prompt: "apply the review's findings",
		Shape:  agents.ShapeText,
		Dir:    t.TempDir(),
	})
	if err != nil {
		t.Fatalf("an ordinary fix round was refused: %v", err)
	}
	if result.Text != "applied the finding" {
		t.Errorf("the fix round's text is %q, want what the agent printed", result.Text)
	}
	if fixer.Reference() == "" {
		t.Error("the fix round should have opened the session it reported")
	}
}

// equalStrings compares two path lists element by element.
func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
