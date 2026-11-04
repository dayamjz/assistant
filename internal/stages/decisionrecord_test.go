package stages_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

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

// An answer the record refuses is reported in a note on the run's report, and
// the review's findings are what they would be had it been kept. The note's
// text is fixed - the service log names what was lost - so it carries neither
// the path nor the store's error. The control is the same review with a
// recorder that keeps everything, which says nothing about the record.
func TestAnAnswerTheRecordRefusesIsReportedOnTheRun(t *testing.T) {
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
	// says so in its own note.
	notes := withDescription(lost.report(), "could not keep every answer")
	if len(notes) != 2 {
		t.Fatalf("the refused answers are not reported one note per check: %+v", lost.report().Findings)
	}
	for _, n := range notes {
		if n.Action != findings.ActionNote || strings.Contains(n.Description, "database is locked") ||
			strings.Contains(n.Description, readmePath) {
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

// Every review that reaches the decision-model checks and finishes them
// records one batch row timing them as a whole, which every answer it
// recorded names: with the checks on, a batch that says it could send,
// answers whose requests are as many as the provider received, and a batch at
// least as long as the slowest answer took; with the checks off or no key, a
// row of its own that says it could not send and that no answer names.
func TestEveryReviewRecordsOneBatchOfItsChecks(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		mode  config.DecisionModel
		key   string
		sends bool
	}{
		{"checks on in evaluate", config.DecisionEvaluate, decisionKey, true},
		{"checks on in enforce", config.DecisionEnforce, decisionKey, true},
		{"checks off", config.DecisionOff, decisionKey, false},
		{"no key", config.DecisionEvaluate, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			provider := judgetest.New(t, judgetest.Always(judgetest.Answer(0.01)))
			rec := &records{}
			s := newSubject(t)
			run(t, s, script(standin.Report(cleanReview(s))), mode(tc.mode), s.start(),
				withJudge(judge.New(tc.key, judge.WithEndpoint(provider.URL()), judge.WithRecorder(rec))))
			batches := rec.allBatches()
			if len(batches) != 1 {
				t.Fatalf("the review recorded %d batches, want 1: %+v", len(batches), batches)
			}
			b := batches[0]
			if b.ID == "" || b.Run != s.run || b.Stage != "review" || b.Mode != string(tc.mode) || b.Sends != tc.sends ||
				b.Elapsed < 0 {
				t.Fatalf("the batch is %+v", b)
			}
			requests := 0
			for _, r := range rec.all() {
				if r.Batch != b.ID || r.Elapsed <= 0 || b.Elapsed < r.Elapsed {
					t.Fatalf("an answer of batch %q took %v, and batch %q took %v", r.Batch, r.Elapsed, b.ID, b.Elapsed)
				}
				requests += r.Requests
			}
			if requests != provider.Count() || (requests > 0) != tc.sends {
				t.Fatalf("the batch's answers count %d requests and the provider received %d", requests, provider.Count())
			}
		})
	}
}

// A batch row the record refuses is reported in a note on the run's report,
// and the review's findings are what they would be had it been kept. The
// note's text is fixed, as the lost-answer note's is. The control is the
// same review with a recorder that keeps everything.
func TestABatchTheRecordRefusesIsReportedOnTheRun(t *testing.T) {
	t.Parallel()
	review := func(rec *records) reviewed {
		provider := judgetest.New(t, judgetest.Always(judgetest.Answer(0.01)))
		s := newSubject(t)
		return run(t, s, script(standin.Report(cleanReview(s))), mode(config.DecisionEvaluate), s.start(),
			withJudge(decided(provider, judge.WithRecorder(rec))))
	}
	lost := review(&records{refuseBatch: errors.New("database is locked")})
	notes := withDescription(lost.report(), "could not keep this review's row timing the checks")
	if len(notes) != 1 || notes[0].Action != findings.ActionNote ||
		strings.Contains(notes[0].Description, "database is locked") {
		t.Fatalf("the refused batch is not reported in one fixed note: %+v", lost.report().Findings)
	}
	kept := review(&records{})
	if got := withDescription(kept.report(), "could not keep"); len(got) != 0 {
		t.Fatalf("a record that kept everything was reported as losing some: %+v", got)
	}
	if lost.outcome() != kept.outcome() {
		t.Fatalf("losing the batch brought the review to %s, and keeping it to %s", lost.outcome(), kept.outcome())
	}
	assertNoWeaker(t, kept.report(), lost.report())
}

// The decision-model checks keep text that varies between fix rounds out of
// the review's report, so the convergence bound still fires. The loop runs
// for real: a review that keeps reporting the same fix-eligible finding, a
// fix round that changes nothing, the checks on, and a record that refuses
// every answer with an error sentence that differs on every call - the shape
// a store failing under load has, and the text the lost-answer note used to
// carry. The run converges after its second round rather than spending its
// round limit.
//
// The control is the same loop with the review's own report carrying text
// that differs each round, which runs to the round limit: it shows this loop
// is one the bound reads the report of, so a note quoting the record's error
// would have defeated it in the same way.
func TestTheDecisionChecksLeaveAFixLoopThatChangedNothingConvergent(t *testing.T) {
	t.Parallel()
	const limit = 5
	for _, c := range []struct {
		name    string
		varying bool
		want    graph.Status
	}{
		{"with the checks' text fixed", false, graph.StatusConverged},
		{"with the review's own text varying", true, graph.StatusRoundsExhausted},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			s := newSubject(t)
			reviewWith := func(words string) findings.Report {
				r := cleanReview(s)
				r.Findings = []findings.Finding{{
					ID: "sum-loses-the-last-element", Severity: findings.SeverityError, Action: findings.ActionFix,
					Location: findings.Location{Path: totalPath, Line: 4}, Description: words,
				}}
				return r
			}
			sc := script(standin.Report(reviewWith("The loop stops one element early.")))
			if c.varying {
				var steps []standin.Step
				for i := range limit + 2 {
					steps = append(steps, standin.Step{Times: 1,
						Reply: standin.Report(reviewWith("The loop stops one element early, round " + strconv.Itoa(i) + "."))})
				}
				sc = standin.Script{Steps: steps}
			}
			calls := 0
			var mu sync.Mutex
			rec := &records{refuse: func(judge.Record) error {
				mu.Lock()
				defer mu.Unlock()
				calls++
				return errors.New("database is locked after " + strconv.Itoa(calls) + " attempts")
			}}
			provider := judgetest.New(t, judgetest.Always(judgetest.Answer(0.01)))
			got := run(t, s, sc, mode(config.DecisionEvaluate), s.start(),
				withJudge(decided(provider, judge.WithRecorder(rec))),
				withRounds(config.FixRounds{Review: limit}, summarizingFixer("nothing needed changing")))
			if got.result.Status != c.want {
				t.Fatalf("the run ended %s (%q), want %s", got.result.Status, got.result.Reason, c.want)
			}
			if calls < 4 {
				t.Fatalf("the record refused %d answers, so the error text never had a chance to vary", calls)
			}
			if len(withDescription(got.report(), "could not keep every answer")) != 2 {
				t.Fatalf("the lost answers are not reported: %+v", got.report().Findings)
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

// A review stopped during the decision-model checks writes no batch row, and
// the answers it recorded before stopping, requests and all, name the batch
// it drew, so they belong to a review rather than to none. The record shows
// only that their batch has no row; a review whose batch row the store
// refused leaves the same shape, and only the run's report notes that one
// (TestABatchTheRecordRefusesIsReportedOnTheRun). The provider answers one screen and
// holds the other, and the run is stopped once both have reached it. The
// control is the same review left to finish, whose answers name the batch it
// wrote.
func TestAReviewStoppedDuringTheChecksLeavesAnswersNamingABatchItNeverWrote(t *testing.T) {
	t.Parallel()
	review := func(answer func(judgetest.Request) judgetest.Reply, stop bool) (*records, error) {
		provider := judgetest.New(t, answer)
		rec := &records{}
		s := newSubject(t)
		p := prepare(t, s, script(standin.Report(cleanReview(s))), mode(config.DecisionEvaluate), s.start(),
			withJudge(decided(provider, judge.WithRecorder(rec))))
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan error, 1)
		go func() { _, err := p.execute(ctx); done <- err }()
		if stop {
			for provider.Count() < 2 {
				select {
				case err := <-done:
					t.Fatalf("the run ended before both screens reached the provider: %v", err)
				case <-time.After(5 * time.Millisecond):
				}
			}
			cancel()
		}
		return rec, <-done
	}
	stopped, err := review(func(r judgetest.Request) judgetest.Reply {
		if r.Path == readmePath {
			return judgetest.Answer(0.01)
		}
		return judgetest.Hang()
	}, true)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("the stopped run ended with %v, want context.Canceled", err)
	}
	if b := stopped.allBatches(); len(b) != 0 {
		t.Fatalf("a review stopped during the checks wrote batches %+v", b)
	}
	answers, requests := stopped.all(), 0
	for _, r := range answers {
		if r.Batch == "" || r.Batch != answers[0].Batch {
			t.Fatalf("the stopped review's answers name batches %q and %q", answers[0].Batch, r.Batch)
		}
		requests += r.Requests
	}
	if len(answers) == 0 || requests < 2 {
		t.Fatalf("the stopped review recorded %d answers sending %d requests, want both screens", len(answers), requests)
	}

	finished, err := review(judgetest.Always(judgetest.Answer(0.01)), false)
	if err != nil {
		t.Fatalf("the finished run ended with %v", err)
	}
	batches := finished.allBatches()
	if len(batches) != 1 || len(finished.all()) == 0 {
		t.Fatalf("the finished review wrote batches %+v and %d answers", batches, len(finished.all()))
	}
	for _, r := range finished.all() {
		if r.Batch != batches[0].ID {
			t.Fatalf("an answer names batch %q and the review wrote %q", r.Batch, batches[0].ID)
		}
	}
}

// The batch row itself tells a review that sent requests apart from one
// without the checks, whatever answer rows the record kept: a keyed review
// whose every answer row is refused and a review with no key both leave a
// batch no answer names, in the same mode, and only the keyed one's says it
// could send. The keyed one did send: the provider received its requests.
func TestABatchSaysItCouldSendEvenWhenEveryAnswerWasRefused(t *testing.T) {
	t.Parallel()
	batchOf := func(key string, rec *records) (judge.Batch, int) {
		provider := judgetest.New(t, judgetest.Always(judgetest.Answer(0.01)))
		s := newSubject(t)
		run(t, s, script(standin.Report(cleanReview(s))), mode(config.DecisionEvaluate), s.start(),
			withJudge(judge.New(key, judge.WithEndpoint(provider.URL()), judge.WithRecorder(rec))))
		b := rec.allBatches()
		if len(b) != 1 || len(rec.all()) != 0 {
			t.Fatalf("the review kept batches %+v and %d answers, want one batch and none", b, len(rec.all()))
		}
		return b[0], provider.Count()
	}
	keyed, sent := batchOf(decisionKey, &records{refuse: func(judge.Record) error {
		return errors.New("database is locked")
	}})
	keyless, none := batchOf("", &records{})
	if sent == 0 || none != 0 {
		t.Fatalf("the keyed review sent %d requests and the keyless one %d", sent, none)
	}
	if keyed.Mode != keyless.Mode || !keyed.Sends || keyless.Sends {
		t.Fatalf("the keyed review's batch is %+v and the keyless one's %+v", keyed, keyless)
	}
}
