package stages_test

import (
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
)

// passStart is the subject's start as a housekeeping pass's push makes it,
// with an intent that says the change touched nothing but the README, which
// is the worker's account and not what its commits did.
func passStart(s *subject) pipeline.Start {
	start := s.start()
	start.Intent = "correct the README; this change touches nothing but README.md"
	start.HousekeepingPass = true
	return start
}

// widenedConfig resolves the configuration a pass's run would from a pushed
// copy that tries to widen housekeeping.paths to every file: trusted-only,
// so it has no effect, and the default list governs (P7).
func widenedConfig(t *testing.T) config.Config {
	t.Helper()
	pushed, err := config.Parse(config.OriginPushed, []byte(`{"housekeeping": {"paths": ["**/*"]}}`))
	if err != nil {
		t.Fatal(err)
	}
	res, err := config.ResolveRun(config.Absent(config.OriginGlobal), config.Absent(config.OriginTrusted), pushed)
	if err != nil {
		t.Fatal(err)
	}
	return res.Config
}

// asksAt is every ask in a report, by the path it is located at.
func asksAt(report findings.Report) map[string]findings.Finding {
	out := map[string]findings.Finding{}
	for _, f := range report.Findings {
		if f.Action == findings.ActionAsk {
			out[f.Location.Path] = f
		}
	}
	return out
}

// PRD section 13's housekeeping row: a promoted pass whose commits touch a
// path outside housekeeping.paths holds at review with an ask naming it,
// decided from the paths the commits touched, including when the worker's
// account says it touched nothing and the reviewer found nothing, and a
// pushed copy cannot widen the scope. The path inside the scope raises no
// ask, and the same change of a run that is no pass's raises none at all.
//
// Every review invocation of the pass's run is session-free (P4): the
// reviewer is started fresh, with no flag that resumes a session, whatever
// the run is.
func TestAPassChangeOutsideItsScopeHoldsAtReviewWhateverItsAccountSays(t *testing.T) {
	principles.Cite(t, principles.P7, principles.P4)
	cfg := widenedConfig(t)
	s := newSubject(t)
	got := run(t, s, script(standin.Report(cleanReview(s))), cfg, passStart(s))
	if outcome := got.outcome(); outcome != pipeline.OutcomeHeld {
		t.Fatalf("a pass's change outside its scope came to %s at review, want held: %+v", outcome, got.report().Findings)
	}
	asks := asksAt(got.report())
	if len(asks) != 1 {
		t.Fatalf("the review carries asks at %v, want one, at %s", asks, totalPath)
	}
	if ask, ok := asks[totalPath]; !ok || !strings.Contains(ask.Description, totalPath) ||
		!strings.Contains(ask.Description, "housekeeping.paths") {
		t.Fatalf("the ask is %+v, want one naming %s as outside housekeeping.paths", asks, totalPath)
	}

	calls := got.agent.Calls()
	if len(calls) == 0 {
		t.Fatal("the pass's review invoked no reviewer, so nothing here shows how one is started")
	}
	for _, call := range calls {
		for _, arg := range call.Args {
			if arg == "--resume" || arg == "--session-id" || arg == "--continue" || arg == "resume" {
				t.Fatalf("a review invocation of the pass's run resumed a session: %q", call.Args)
			}
		}
	}

	plain := newSubject(t)
	control := run(t, plain, script(standin.Report(cleanReview(plain))), cfg, plain.start())
	if control.outcome() != pipeline.OutcomePassed || len(asksAt(control.report())) != 0 {
		t.Fatalf("the same change of a run that is no pass's came to %s with %v, want passed with no ask",
			control.outcome(), control.report().Findings)
	}
}

// The same rule's other side: a pass's change that stays inside its scope
// raises no ask and passes.
func TestAPassChangeInsideItsScopeDoesNotHold(t *testing.T) {
	cfg := config.Defaults()
	s := newSubject(t)
	// The change is cut afresh from main and touches the README alone.
	git(t, s.dir, "checkout", "--quiet", "main")
	git(t, s.dir, "checkout", "--quiet", "-B", "topic")
	write(t, s.dir, readmePath, "Total sums every value.\n")
	s.head = s.commit(t, "correct the README")
	report := cleanReview(s)
	report.Read = []string{readmePath}
	report.Traced = []findings.Trace{{Path: readmePath, Reason: "the intent covers it"}}
	got := run(t, s, script(standin.Report(report)), cfg, passStart(s))
	if got.outcome() != pipeline.OutcomePassed || len(asksAt(got.report())) != 0 {
		t.Fatalf("a pass's change inside its scope came to %s with %v, want passed with no ask", got.outcome(), got.report().Findings)
	}
}
