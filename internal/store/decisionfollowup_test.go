package store

import (
	"context"
	"testing"
)

// A follow-up attaches to every answer about its path that was recorded up to
// the mark it was given, and to none after it: the work being recorded is
// what produced a later answer, so that answer is not followed by it. The
// answers themselves are untouched.
func TestAFollowUpAttachesToTheAnswersBeforeItsMark(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	appendAnswer := func(run, path, finding string) DecisionAnswer {
		t.Helper()
		a, err := s.AppendDecisionAnswer(ctx, DecisionAnswer{
			Run: run, Stage: "review", Check: "reviewer-directed-text", Path: path,
			Model: "jev-1.13.0", Probability: Known(0.9), Cutoff: 0.3, Finding: finding,
		})
		if err != nil {
			t.Fatalf("appending an answer: %v", err)
		}
		return a
	}
	first := appendAnswer("run-1", "a.go", "note")
	appendAnswer("run-1", "b.go", "")
	appendAnswer("run-2", "a.go", "note")
	mark, err := s.LastDecisionAnswer(ctx, "run-1")
	if err != nil || mark < first.Seq {
		t.Fatalf("the mark is %d (%v), want at least %d", mark, err, first.Seq)
	}
	later := appendAnswer("run-1", "a.go", "note")

	n, err := s.AppendDecisionFollowUp(ctx, "run-1", "a.go", FollowUpFix, "test", "c0ffee", mark)
	if err != nil || n != 1 {
		t.Fatalf("the follow-up attached to %d answer(s) (%v), want the one before the mark", n, err)
	}
	if n, err := s.AppendDecisionFollowUp(ctx, "run-1", "never-answered.go", FollowUpFinding, "lint", "fix", later.Seq); err != nil || n != 0 {
		t.Fatalf("a path no answer was about attached to %d answer(s) (%v)", n, err)
	}
	got, err := s.DecisionFollowUps(ctx, "run-1")
	if err != nil {
		t.Fatalf("reading follow-ups: %v", err)
	}
	if len(got) != 1 || got[0].Answer != first.Seq || got[0].Kind != FollowUpFix || got[0].Detail != "c0ffee" {
		t.Fatalf("run-1's follow-ups are %+v", got)
	}
	if others, _ := s.DecisionFollowUps(ctx, "run-2"); len(others) != 0 {
		t.Fatalf("run-2's answer was followed by run-1's work: %+v", others)
	}
	if answers, _ := s.DecisionAnswers(ctx, "run-1"); len(answers) != 3 {
		t.Fatalf("recording a follow-up changed the answers: %+v", answers)
	}
}

// A kind outside the three is refused, as is a follow-up naming nothing.
func TestAFollowUpOfNoKnownKindIsRefused(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	if _, err := s.AppendDecisionFollowUp(ctx, "run-1", "a.go", FollowUpAnswer, "review", "approve", 0); err != nil {
		t.Fatalf("a well-formed follow-up was refused: %v", err)
	}
	if _, err := s.AppendDecisionFollowUp(ctx, "run-1", "a.go", FollowUpKind("waved"), "review", "", 0); err == nil {
		t.Fatal("a follow-up of an unknown kind was accepted")
	}
	if _, err := s.AppendDecisionFollowUp(ctx, "run-1", "", FollowUpFix, "review", "", 0); err == nil {
		t.Fatal("a follow-up naming no path was accepted")
	}
}

// The firings of a check are counted per run of one repository, newest first,
// with how many anything followed; an answer that produced no finding is not
// a firing, and another check's firings are not counted.
func TestDecisionFiringsCountFiredAndFollowedPerRun(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)
	second := run
	second.ID = "run-2"
	if _, err := s.CreateRun(ctx, second); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	add := func(run, check, path, finding string) {
		t.Helper()
		if _, err := s.AppendDecisionAnswer(ctx, DecisionAnswer{
			Run: run, Stage: "review", Check: check, Path: path,
			Model: "jev-1.13.0", Probability: Known(0.5), Cutoff: 0.3, Finding: finding,
		}); err != nil {
			t.Fatalf("appending: %v", err)
		}
	}
	add("run-1", "screen", "a.go", "note")
	add("run-1", "screen", "b.go", "note")
	add("run-1", "screen", "c.go", "")
	add("run-1", "trace", "a.go", "note")
	add("run-2", "screen", "a.go", "note")
	mark, _ := s.LastDecisionAnswer(ctx, "run-1")
	if _, err := s.AppendDecisionFollowUp(ctx, "run-1", "b.go", FollowUpAnswer, "review", "approve", mark); err != nil {
		t.Fatalf("appending a follow-up: %v", err)
	}

	got, err := s.DecisionFirings(ctx, "repo-1", "screen")
	if err != nil {
		t.Fatalf("DecisionFirings: %v", err)
	}
	if len(got) != 2 || got[0].Run != "run-2" || got[1].Run != "run-1" {
		t.Fatalf("the firings are %+v, want run-2 then run-1", got)
	}
	if got[1].Fired != 2 || got[1].Followed != 1 || got[0].Fired != 1 || got[0].Followed != 0 {
		t.Fatalf("the counts are %+v", got)
	}
	if got[1].Status != RunPending {
		t.Fatalf("run-1 reads back as %s", got[1].Status)
	}
}
