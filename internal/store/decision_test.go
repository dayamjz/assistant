package store

import (
	"context"
	"testing"
)

// The record keeps an answer and a failed call apart: an answer of zero reads
// back as a known zero, and a failed call reads back with no probability at
// all, so a call that failed never reads as the model answering certain-no.
func TestDecisionAnswersKeepAZeroAndAFailureApart(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	zero := DecisionAnswer{
		Run: "run-1", Stage: "review", Check: "reviewer-directed-text", Path: "a.go",
		Model: "jev-1.13.0", Probability: Known(0.0), Cutoff: 0.3,
	}
	failed := DecisionAnswer{
		Run: "run-1", Stage: "review", Check: "trace-reason", Path: "b.go",
		Model: "jev-1.13.0", Cutoff: 0.8, Failure: "timeout",
	}
	flagged := DecisionAnswer{
		Run: "run-2", Stage: "review", Check: "reviewer-directed-text", Path: "c.go",
		Model: "jev-1.13.0", Probability: Known(0.97), Cutoff: 0.3, Finding: "ask",
	}
	for _, a := range []DecisionAnswer{zero, failed, flagged} {
		if _, err := s.AppendDecisionAnswer(ctx, a); err != nil {
			t.Fatalf("appending %+v: %v", a, err)
		}
	}
	got, err := s.DecisionAnswers(ctx, "run-1")
	if err != nil {
		t.Fatalf("reading the record: %v", err)
	}
	if len(got) != 2 || got[0].Path != "a.go" || got[1].Path != "b.go" {
		t.Fatalf("run-1's record is %+v, want its two rows in order", got)
	}
	if p, ok := got[0].Probability.Get(); !ok || p != 0 {
		t.Fatalf("an answer of zero read back as (%v, %v)", p, ok)
	}
	if got[1].Probability.IsKnown() || got[1].Failure != "timeout" {
		t.Fatalf("a failed call read back as %+v", got[1])
	}
	other, err := s.DecisionAnswers(ctx, "run-2")
	if err != nil || len(other) != 1 || other[0].Finding != "ask" || other[0].Cutoff != 0.3 {
		t.Fatalf("run-2's record is %+v (%v)", other, err)
	}
}

// A row that is both an answer and a failure, or neither, or that names
// nothing it is attributed to, is refused rather than stored.
func TestADecisionAnswerThatSaysTwoThingsOrNothingIsRefused(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	base := DecisionAnswer{Run: "r", Stage: "review", Check: "trace-reason", Path: "a.go", Model: "jev-1.13.0", Cutoff: 0.8}
	both := base
	both.Probability, both.Failure = Known(0.5), "timeout"
	unattributed := base
	unattributed.Run, unattributed.Probability = "", Known(0.5)
	for name, a := range map[string]DecisionAnswer{"both": both, "neither": base, "no run": unattributed} {
		if _, err := s.AppendDecisionAnswer(ctx, a); err == nil {
			t.Errorf("a row with %s was stored", name)
		}
	}
	if got, err := s.DecisionAnswers(ctx, "r"); err != nil || len(got) != 0 {
		t.Fatalf("refused rows reached the record: %+v (%v)", got, err)
	}
}
