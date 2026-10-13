package stages_test

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/judge"
	"github.com/dayamjz/assistant/internal/judge/judgetest"
	"github.com/dayamjz/assistant/internal/pipeline"
)

// decisionKey is the provider key the clients here carry.
const decisionKey = "ts_stage_test_key_0123456789"

// decided builds a client against the stand-in provider.
func decided(p *judgetest.Provider, opts ...judge.Option) *judge.Client {
	return judge.New(decisionKey, append([]judge.Option{judge.WithEndpoint(p.URL())}, opts...)...)
}

// mode is a configuration whose only setting is the decision-model mode.
func mode(m config.DecisionModel) config.Config { return config.Config{DecisionModel: m} }

// PRD section 13's "Decision model" row. Against a provider that answers over
// the wire, every answer from certain-yes to certain-no, plus an error, a
// timeout, an unreadable answer, and a missing key, leaves the findings at
// least as strict as with the model off: every finding the run reports with
// the model off is still there at an action no lower, and no stage passes
// that would have held. In evaluate no answer holds the run.
//
// The baseline is a review that traces and reports something, so there is a
// finding for an answer to remove or lower if it could: the reviewer's own
// note, and the scope lens's note on the path it did not trace.
func TestADecisionModelAnswerLeavesTheReviewAtLeastAsStrictAsWithItOff(t *testing.T) {
	t.Parallel()
	report := func(s *subject) findings.Report {
		r := cleanReview(s)
		r.Traced = r.Traced[:1]
		r.Findings = []findings.Finding{{
			ID: "total-comment", Severity: findings.SeverityInfo, Action: findings.ActionNote,
			Location: findings.Location{Path: totalPath, Line: 5}, Description: "The comment restates the code.",
		}}
		return r
	}
	off := func() reviewed {
		s := newSubject(t)
		return run(t, s, script(standin.Report(report(s))), mode(config.DecisionOff), s.start())
	}()
	if off.outcome() != pipeline.OutcomePassed || len(scopeNotes(off.report())) != 1 {
		t.Fatalf("the baseline came to %s with %+v, want a pass carrying a scope note", off.outcome(), off.report().Findings)
	}

	type behaviour struct {
		name  string
		reply judgetest.Reply
		key   bool
	}
	var cases []behaviour
	for _, p := range []float64{0, 0.1, 0.29, 0.3, 0.5, 0.79, 0.8, 0.95, 1} {
		cases = append(cases, behaviour{name: "an answer of " + strconv.FormatFloat(p, 'g', -1, 64), reply: judgetest.Answer(p), key: true})
	}
	cases = append(cases,
		behaviour{"a server error", judgetest.Status(http.StatusInternalServerError), true},
		behaviour{"a timeout", judgetest.Hang(), true},
		behaviour{"an unreadable answer", judgetest.Raw(http.StatusOK, `{"model":"jev-1.13.0","answers":{}}`), true},
		behaviour{"an answer from another model", judgetest.AnswerFrom("jev-latest", 0.9), true},
		behaviour{"a missing key", judgetest.Answer(0.99), false},
	)
	for _, m := range []config.DecisionModel{config.DecisionEvaluate, config.DecisionEnforce} {
		for _, c := range cases {
			t.Run(string(m)+"/"+c.name, func(t *testing.T) {
				t.Parallel()
				provider := judgetest.New(t, judgetest.Always(c.reply))
				client := decided(provider, judge.WithTimeout(100*time.Millisecond))
				if !c.key {
					client = judge.New("", judge.WithEndpoint(provider.URL()))
				}
				s := newSubject(t)
				got := run(t, s, script(standin.Report(report(s))), mode(m), s.start(), withJudge(client))

				assertNoWeaker(t, off.report(), got.report())
				switch {
				case m == config.DecisionEvaluate && got.outcome() != off.outcome():
					t.Fatalf("in evaluate the review came to %s, and with the model off to %s: "+
						"evaluate holds nothing", got.outcome(), off.outcome())
				case got.outcome() != off.outcome() && got.outcome() != pipeline.OutcomeHeld:
					t.Fatalf("the review came to %s, and with the model off to %s", got.outcome(), off.outcome())
				}
				for _, f := range got.report().Findings {
					if f.Action == findings.ActionAsk && !strings.Contains(f.Description, "A decision model flagged") {
						t.Fatalf("an ask that is not a flagged file appeared: %+v", f)
					}
					if f.Action == findings.ActionFix {
						t.Fatalf("a decision-model check produced a fix finding: %+v", f)
					}
				}
				if !c.key {
					if provider.Count() != 0 {
						t.Fatalf("with no key %d requests left the machine", provider.Count())
					}
					if len(withDescription(got.report(), "no key for the provider is in "+judge.KeyVariable)) != 1 {
						t.Fatalf("a missing key is not reported: %+v", got.report().Findings)
					}
				}
			})
		}
	}
}

// With the mode off no request leaves the machine and the report says nothing
// about the checks; with no key none leaves either, and the report says
// which checks did not run and why. The positive control is the same run with
// a key in evaluate, which does send.
func TestOffOrNoKeySendsNothing(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		mode  config.DecisionModel
		key   bool
		sends bool
		said  bool
	}{
		{"off with a key", config.DecisionOff, true, false, false},
		{"an unset mode with a key", "", true, false, false},
		{"evaluate with no key", config.DecisionEvaluate, false, false, true},
		{"enforce with no key", config.DecisionEnforce, false, false, true},
		{"evaluate with a key", config.DecisionEvaluate, true, true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			provider := judgetest.New(t, judgetest.Always(judgetest.Answer(0.99)))
			client := judge.New("", judge.WithEndpoint(provider.URL()))
			if c.key {
				client = decided(provider)
			}
			s := newSubject(t)
			got := run(t, s, script(standin.Report(cleanReview(s))), mode(c.mode), s.start(), withJudge(client))
			if sent := provider.Count() > 0; sent != c.sends {
				t.Fatalf("%d requests left the machine, want sending %v", provider.Count(), c.sends)
			}
			said := len(withDescription(got.report(), "decision-model checks did not run")) > 0
			if said != c.said {
				t.Fatalf("the report says the checks did not run: %v, want %v: %+v", said, c.said, got.report().Findings)
			}
		})
	}
}

// Every file the reviewer is shown is screened, one per request, excluded
// paths included because the reviewer is shown the whole diff; and every
// trace naming a touched path is checked with that path's own diff.
func TestEveryShownFileIsScreenedAndEveryTraceChecked(t *testing.T) {
	t.Parallel()
	provider := judgetest.New(t, judgetest.Always(judgetest.Answer(0.9)))
	s := newSubject(t)
	r := cleanReview(s)
	r.Read = findings.Paths{totalPath}
	r.Traced = r.Traced[:1]
	cfg := mode(config.DecisionEvaluate)
	cfg.IgnorePatterns = patterns(t, readmePath)
	run(t, s, script(standin.Report(r)), cfg, s.start(), withJudge(decided(provider)))

	screened := map[string]string{}
	traced := map[string]string{}
	for _, req := range provider.Requests() {
		switch req.Question {
		case "reviewer_directed_text":
			screened[req.Path] = req.Diff
		case "trace_reason":
			traced[req.Path] = req.Reason
		}
		if strings.Contains(req.Diff, "diff --git") && strings.Count(req.Diff, "diff --git") != 1 {
			t.Fatalf("a request carried more than one file: %q", req.Diff)
		}
	}
	if len(screened) != 2 || !strings.Contains(screened[readmePath], "Total sums every value") ||
		!strings.Contains(screened[totalPath], "start empty") {
		t.Fatalf("the screen saw %v, want both files with their own changes", screened)
	}
	if strings.Contains(screened[totalPath], "Total sums every value") {
		t.Fatalf("a file's request carries another file's change: %q", screened[totalPath])
	}
	if len(traced) != 1 || traced[totalPath] == "" {
		t.Fatalf("the trace check saw %v, want the one trace on the touched path", traced)
	}
}

// A trace the model does not confirm silences nothing: its path gets the scope
// lens's untraced note quoting the reason. A confirmed one stays silent. The
// pair is the discriminator, and in every mode the note is a note.
func TestATraceTheModelDoesNotConfirmGetsTheUntracedNote(t *testing.T) {
	t.Parallel()
	const falseReason = "the intent asked for the change to be written down"
	for _, m := range []config.DecisionModel{config.DecisionEvaluate, config.DecisionEnforce} {
		provider := judgetest.New(t, func(r judgetest.Request) judgetest.Reply {
			if r.Question == "trace_reason" && r.Path == readmePath {
				return judgetest.Answer(0.05)
			}
			if r.Question == "trace_reason" {
				return judgetest.Answer(0.97)
			}
			return judgetest.Answer(0.01)
		})
		s := newSubject(t)
		got := run(t, s, script(standin.Report(cleanReview(s))), mode(m), s.start(), withJudge(decided(provider)))
		notes := scopeNotes(got.report())
		if len(notes) != 1 || notes[0].Location.Path != readmePath {
			t.Fatalf("in %s the scope lens noted %+v, want one note on %s", m, notes, readmePath)
		}
		if !strings.Contains(notes[0].Description, `"`+falseReason+`"`) ||
			!strings.Contains(notes[0].Description, "did not bear it out") {
			t.Fatalf("the note does not quote the reason and say the changes did not bear it out: %q",
				notes[0].Description)
		}
		if notes[0].Action != findings.ActionNote || got.outcome() != pipeline.OutcomePassed {
			t.Fatalf("in %s an unconfirmed trace came to %s with %s: the trace check is note-only",
				m, notes[0].Action, got.outcome())
		}
	}
}

// A file the screen flags is reported naming the file: a note in evaluate,
// which holds nothing, and an ask in enforce, which holds the review.
func TestAFlaggedFileIsANoteInEvaluateAndAnAskInEnforce(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		mode   config.DecisionModel
		action findings.Action
		want   pipeline.Outcome
	}{
		{config.DecisionEvaluate, findings.ActionNote, pipeline.OutcomePassed},
		{config.DecisionEnforce, findings.ActionAsk, pipeline.OutcomeHeld},
	} {
		provider := judgetest.New(t, func(r judgetest.Request) judgetest.Reply {
			if r.Question == "reviewer_directed_text" && r.Path == readmePath {
				return judgetest.Answer(0.6)
			}
			if r.Question == "reviewer_directed_text" {
				return judgetest.Answer(0.29)
			}
			return judgetest.Answer(0.95)
		})
		s := newSubject(t)
		got := run(t, s, script(standin.Report(cleanReview(s))), mode(c.mode), s.start(), withJudge(decided(provider)))
		flagged := withDescription(got.report(), "A decision model flagged")
		if len(flagged) != 1 || flagged[0].Location.Path != readmePath || flagged[0].Action != c.action {
			t.Fatalf("in %s the flagged findings are %+v, want one %s on %s", c.mode, flagged, c.action, readmePath)
		}
		if got.outcome() != c.want {
			t.Fatalf("in %s a flagged file brought the review to %s, want %s", c.mode, got.outcome(), c.want)
		}
	}
}

// Every answer is recorded with the check, the path, the pinned model, the
// probability, the cutoff, and the action of the finding it produced, and a
// failed call is recorded with no probability.
func TestEveryAnswerIsRecordedWithTheFindingItProduced(t *testing.T) {
	t.Parallel()
	provider := judgetest.New(t, func(r judgetest.Request) judgetest.Reply {
		switch {
		case r.Question == "reviewer_directed_text" && r.Path == readmePath:
			return judgetest.Answer(0.6)
		case r.Question == "reviewer_directed_text":
			return judgetest.Status(http.StatusServiceUnavailable)
		case r.Path == readmePath:
			return judgetest.Answer(0.1)
		default:
			return judgetest.Answer(0.9)
		}
	})
	rec := &records{}
	s := newSubject(t)
	got := run(t, s, script(standin.Report(cleanReview(s))), mode(config.DecisionEnforce), s.start(),
		withJudge(decided(provider, judge.WithRecorder(rec))))
	want := map[string]struct {
		answered bool
		finding  string
	}{
		"reviewer-directed-text " + readmePath: {true, "ask"},
		"reviewer-directed-text " + totalPath:  {false, ""},
		"trace-reason " + readmePath:           {true, "note"},
		"trace-reason " + totalPath:            {true, ""},
	}
	all := rec.all()
	if len(all) != len(want) {
		t.Fatalf("recorded %d answers, want %d: %+v", len(all), len(want), all)
	}
	for _, r := range all {
		w, ok := want[string(r.Check)+" "+r.Path]
		if !ok || r.Answered != w.answered || r.Finding != w.finding || r.Run != s.run ||
			r.Stage != "review" || r.Model != judge.ModelVersion || r.Cutoff != r.Check.Cutoff() {
			t.Fatalf("recorded %+v, want %+v", r, w)
		}
		if !r.Answered && r.Failure != judge.FailureStatus {
			t.Fatalf("a failed call was recorded as %+v", r)
		}
	}
	if len(withDescription(got.report(), "did not answer for 1 file(s)")) != 1 {
		t.Fatalf("the screen's failure on one file is not reported: %+v", got.report().Findings)
	}
}

// records is a judge.Recorder that keeps what it is handed.
type records struct {
	mu   sync.Mutex
	rows []judge.Record
}

func (r *records) RecordAnswer(rec judge.Record) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows = append(r.rows, rec)
}

func (r *records) all() []judge.Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]judge.Record(nil), r.rows...)
}

// withDescription returns the findings whose description carries text.
func withDescription(report findings.Report, text string) []findings.Finding {
	var out []findings.Finding
	for _, f := range report.Findings {
		if strings.Contains(f.Description, text) {
			out = append(out, f)
		}
	}
	return out
}

// rank orders actions by how much they ask of a person.
func rank(a findings.Action) int {
	switch a {
	case findings.ActionAsk:
		return 2
	case findings.ActionFix:
		return 1
	default:
		return 0
	}
}

// assertNoWeaker fails unless every finding of base is in got, at the same
// path and with the same description, at an action no lower. The scope note
// may carry more text in got, because an unconfirmed trace extends it.
func assertNoWeaker(t *testing.T, base, got findings.Report) {
	t.Helper()
	for _, b := range base.Findings {
		found := false
		for _, g := range got.Findings {
			if g.Location.Path == b.Location.Path && strings.HasPrefix(g.Description, b.Description) &&
				rank(g.Action) >= rank(b.Action) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("the finding %+v went away or was lowered: %+v", b, got.Findings)
		}
	}
}

// The judge lens and the decision-model checks both add to the one review
// report, and neither drops the other's findings. The change edits a judge
// file, and the screen flags that same file, so the report has to carry two
// findings at that path: the judge lens's note that the file was modified, and
// the screen's note that it was flagged. Each feature's own tests pass with the
// other's findings absent, which is why the combination is checked here.
func TestTheJudgeLensAndTheDecisionChecksBothReachTheReport(t *testing.T) {
	t.Parallel()
	provider := judgetest.New(t, func(r judgetest.Request) judgetest.Reply {
		switch {
		case r.Question == "reviewer_directed_text" && r.Path == judgeTestPath:
			return judgetest.Answer(0.9)
		case r.Question == "reviewer_directed_text":
			return judgetest.Answer(0.0)
		default:
			return judgetest.Answer(0.95)
		}
	})
	cfg := config.Defaults()
	cfg.DecisionModel = config.DecisionEvaluate
	s := newJudgeSubject(t, func(dir string) {
		write(t, dir, judgeTestPath, strings.Replace(judgeTestBody, "got != 6", "got < 0", 1))
	})
	got := run(t, s, script(standin.Report(judgeReview(s, judgeTestPath))), cfg, s.start(),
		withJudge(decided(provider)))

	var judged, flagged int
	for _, found := range got.report().Findings {
		if found.Location.Path != judgeTestPath {
			continue
		}
		switch {
		case strings.Contains(found.Description, "modified "+judgeTestPath):
			judged++
		case strings.Contains(found.Description, "A decision model flagged "+judgeTestPath):
			flagged++
		}
	}
	if judged != 1 || flagged != 1 {
		t.Fatalf("the report carries %d judge finding(s) and %d screen finding(s) at %s, want one of each: %+v",
			judged, flagged, judgeTestPath, got.report().Findings)
	}
	if outcome := got.outcome(); outcome != pipeline.OutcomePassed {
		t.Fatalf("in evaluate, a judge edit and a flagged file brought the review to %s, want passed: %+v",
			outcome, got.report().Findings)
	}
}
