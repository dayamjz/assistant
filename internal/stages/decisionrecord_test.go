package stages_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/judge"
	"github.com/dayamjz/assistant/internal/judge/judgetest"
	"github.com/dayamjz/assistant/internal/pipeline"
)

// A review whose decision-model fan-out reaches its bound sends no more
// requests than the bound, counted at the stand-in provider, names what it did
// not check as not having run, and holds nothing: the review comes to what it
// comes to with no bound reached. The control is the same review with the
// shipped bound, which sends one request more and names nothing.
//
// The subject touches two files and the review traces both, so the review
// asks four questions: two screens, then two traces. A bound of three spends
// two on the screen and one on a trace, and the other trace is the one that
// did not run.
func TestAReviewOverTheRequestBoundNamesWhatDidNotRunAndHoldsNothing(t *testing.T) {
	t.Parallel()
	review := func(opts ...judge.Option) (reviewed, *judgetest.Provider) {
		provider := judgetest.New(t, judgetest.Always(judgetest.Answer(0.95)))
		s := newSubject(t)
		got := run(t, s, script(standin.Report(cleanReview(s))), mode(config.DecisionEnforce), s.start(),
			withJudge(decided(provider, opts...)))
		return got, provider
	}

	bounded, provider := review(judge.WithBounds(3, 0))
	if provider.Count() != 3 {
		t.Fatalf("a review bounded at 3 requests sent %d", provider.Count())
	}
	notRun := withDescription(bounded.report(), "did not run for 1 traced path(s), because this review "+
		"reached a bound")
	if len(notRun) != 1 || notRun[0].Action != findings.ActionNote {
		t.Fatalf("the trace the bound refused is not named in one note: %+v", bounded.report().Findings)
	}
	if !strings.Contains(notRun[0].Description, string(judge.FailureOverBound)) {
		t.Fatalf("the note does not say why: %q", notRun[0].Description)
	}

	unbounded, control := review()
	if control.Count() != 4 {
		t.Fatalf("the same review with the shipped bound sent %d requests, want 4", control.Count())
	}
	if got := withDescription(unbounded.report(), "reached a bound"); len(got) != 0 {
		t.Fatalf("a review inside the bound named a bound: %+v", got)
	}
	if bounded.outcome() != unbounded.outcome() {
		t.Fatalf("reaching the bound brought the review to %s, and inside it the review came to %s",
			bounded.outcome(), unbounded.outcome())
	}
	assertNoWeaker(t, unbounded.report(), bounded.report())
	for _, f := range bounded.report().Findings {
		if strings.Contains(f.Description, "reached a bound") && f.Action != findings.ActionNote {
			t.Fatalf("a bound note holds: %+v", f)
		}
	}
}

// An answer the record refuses is named in a note on the run's report, and
// the review's findings are what they would be had it been kept. The control
// is the same review with a recorder that keeps everything, which says
// nothing about the record.
func TestAnAnswerTheRecordRefusesIsNamedOnTheReport(t *testing.T) {
	t.Parallel()
	review := func(rec *records) reviewed {
		provider := judgetest.New(t, judgetest.Always(judgetest.Answer(0.01)))
		s := newSubject(t)
		return run(t, s, script(standin.Report(cleanReview(s))), mode(config.DecisionEvaluate), s.start(),
			withJudge(decided(provider, judge.WithRecorder(rec))))
	}
	refusing := &records{refuse: func(r judge.Record) error {
		if r.Path == readmePath {
			return errors.New("database is locked")
		}
		return nil
	}}
	lost := review(refusing)
	// One answer of each check was about the refused path, and each check
	// names its own.
	notes := withDescription(lost.report(), "could not keep 1 answer(s)")
	if len(notes) != 2 {
		t.Fatalf("the refused answers are not named one note per check: %+v", lost.report().Findings)
	}
	for _, n := range notes {
		if n.Action != findings.ActionNote || !strings.Contains(n.Description, readmePath) ||
			!strings.Contains(n.Description, "database is locked") {
			t.Fatalf("a note on the refused answers is %+v", n)
		}
	}
	if len(refusing.all()) != 2 {
		t.Fatalf("the recorder kept %d rows, want the two it did not refuse", len(refusing.all()))
	}

	kept := review(&records{})
	if got := withDescription(kept.report(), "could not keep"); len(got) != 0 {
		t.Fatalf("a record that kept everything was reported as losing some: %+v", got)
	}
	if lost.outcome() != kept.outcome() {
		t.Fatalf("losing a record brought the review to %s, and keeping it to %s", lost.outcome(), kept.outcome())
	}
	assertNoWeaker(t, kept.report(), lost.report())
}

// history is a judge.History answering from fixed firings per check, or with
// an error.
type history struct {
	firings map[judge.Check][]judge.RunFirings
	err     error
}

func (h history) Firings(_ context.Context, _ string, check judge.Check) ([]judge.RunFirings, error) {
	return h.firings[check], h.err
}

// silent is n earlier runs in which a check fired with nothing following.
func silent(n int) []judge.RunFirings {
	out := make([]judge.RunFirings, n)
	for i := range out {
		out[i] = judge.RunFirings{Run: "run-" + strconv.Itoa(i+1), Fired: 1}
	}
	return out
}

// A check that fired in each of the window's runs with nothing ever following
// is proposed off in a note naming the window and the runs, and the note
// holds nothing and changes no mode. The rule's edges are the controls: one
// run short of the window proposes nothing, and one firing something followed
// anywhere in the window proposes nothing either. A record that cannot be read
// is said rather than read as silence.
func TestACheckFiringWithNothingFollowingIsProposedOffAndNeverTurnedOff(t *testing.T) {
	t.Parallel()
	followedOnce := silent(10)
	followedOnce[6].Followed = 1
	cases := []struct {
		name     string
		history  history
		proposed int
		readErr  bool
	}{
		{"ten silent runs", history{firings: map[judge.Check][]judge.RunFirings{judge.CheckScreen: silent(12)}}, 1, false},
		{"nine silent runs", history{firings: map[judge.Check][]judge.RunFirings{judge.CheckScreen: silent(9)}}, 0, false},
		{"one firing followed", history{firings: map[judge.Check][]judge.RunFirings{judge.CheckScreen: followedOnce}}, 0, false},
		{"an unreadable record", history{err: errors.New("database is locked")}, 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			provider := judgetest.New(t, judgetest.Always(judgetest.Answer(0.01)))
			s := newSubject(t)
			cfg := mode(config.DecisionEvaluate)
			got := run(t, s, script(standin.Report(cleanReview(s))), cfg, s.start(),
				withJudge(decided(provider, judge.WithHistory(c.history))))
			proposals := withDescription(got.report(), "Proposal: the decision model's")
			if len(proposals) != c.proposed {
				t.Fatalf("%d proposal(s), want %d: %+v", len(proposals), c.proposed, got.report().Findings)
			}
			for _, p := range proposals {
				if p.Action != findings.ActionNote {
					t.Fatalf("a proposal holds: %+v", p)
				}
				_, window, _ := strings.Cut(p.Description, "are the window: ")
				window, _, _ = strings.Cut(window, ". Check them")
				var want []string
				for _, f := range silent(10) {
					want = append(want, f.Run)
				}
				if window != strings.Join(want, ", ") {
					t.Fatalf("the proposal names the window %q, want the ten newest runs", window)
				}
				if !strings.Contains(p.Description, "10 most recent finished runs") ||
					!strings.Contains(p.Description, "decision_model") {
					t.Fatalf("the proposal does not name the window or the setting: %q", p.Description)
				}
			}
			// Both checks ran, so an unreadable record is said once for each.
			if read := withDescription(got.report(), "could not be read"); (len(read) == 2) != c.readErr ||
				(!c.readErr && len(read) != 0) {
				t.Fatalf("an unreadable record was reported %d time(s): %+v", len(read), got.report().Findings)
			}
			if cfg.DecisionModel != config.DecisionEvaluate || got.outcome() != pipeline.OutcomePassed {
				t.Fatalf("the review came to %s in mode %s", got.outcome(), cfg.DecisionModel)
			}
		})
	}
}

// The pull request records what ran: the mode, the model version, and how
// many files or traces each check covered, read from the record a real review
// wrote. A review with the checks off records that instead, and a run whose
// review wrote nothing says no check ran, so the three read differently.
func TestThePullRequestRecordsWhichDecisionModelChecksRan(t *testing.T) {
	t.Parallel()
	recordOf := func(m config.DecisionModel) graph.Value {
		provider := judgetest.New(t, func(r judgetest.Request) judgetest.Reply {
			switch {
			case r.Question == "reviewer_directed_text" && r.Path == readmePath:
				return judgetest.Answer(0.6)
			case r.Question == "reviewer_directed_text":
				return judgetest.Answer(0.01)
			default:
				return judgetest.Answer(0.95)
			}
		})
		s := newSubject(t)
		got := run(t, s, script(standin.Report(cleanReview(s))), mode(m), s.start(),
			withJudge(decided(provider)))
		v, ok := got.result.State.Get(string(pipeline.KeyDecisionChecks))
		if !ok {
			t.Fatalf("the review in %s wrote no record of the checks", m)
		}
		return v
	}
	section := func(decision graph.Value) string {
		r := aRun()
		r.decision = decision
		h := newHost()
		mustRunPR(t, h, r)
		body := onlyBody(t, h)
		_, after, ok := strings.Cut(body, "### Decision-model checks")
		if !ok {
			t.Fatalf("the body has no decision-model section:\n\n%s", body)
		}
		return after
	}

	on := section(recordOf(config.DecisionEvaluate))
	for _, want := range []string{
		"Mode: `evaluate`", "Model version: `" + judge.ModelVersion + "`",
		"Screen for text aimed at the reviewer: ran over 2 file(s); the model answered for 2 and fired on 1.",
		"Check of the review's trace reasons: ran over 2 trace(s); the model answered for 2 and fired on 0.",
	} {
		if !strings.Contains(on, want) {
			t.Fatalf("the section does not say %q:\n\n%s", want, on)
		}
	}
	off := section(recordOf(config.DecisionOff))
	if !strings.Contains(off, "Mode: `off`") || !strings.Contains(off, "Neither check ran") ||
		strings.Contains(off, "Model version") {
		t.Fatalf("a review with the checks off reads as:\n\n%s", off)
	}
	none := section(graph.Value{})
	if !strings.Contains(none, "no decision-model check ran") || strings.Contains(none, "Mode:") {
		t.Fatalf("a run whose review recorded nothing reads as:\n\n%s", none)
	}
}
