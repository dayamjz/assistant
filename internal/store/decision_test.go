package store

import (
	"context"
	"math"
	"testing"
	"time"
)

// The record keeps an answer and a failed call apart: an answer of zero reads
// back as a known zero, and a failed call reads back with no probability at
// all, so a call that failed never reads as the model answering certain-no.
func TestDecisionAnswersKeepAZeroAndAFailureApart(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	zero := DecisionAnswer{
		Run: "run-1", Stage: "review", Batch: "b-1", Check: "reviewer-directed-text", Path: "a.go",
		Model: "jev-1.13.0", Probability: Known(0.0), Cutoff: 0.3,
	}
	failed := DecisionAnswer{
		Run: "run-1", Stage: "review", Batch: "b-1", Check: "trace-reason", Path: "b.go",
		Model: "jev-1.13.0", Cutoff: 0.8, Failure: "timeout",
	}
	flagged := DecisionAnswer{
		Run: "run-2", Stage: "review", Batch: "b-1", Check: "reviewer-directed-text", Path: "c.go",
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
	if got[0].Batch != "b-1" {
		t.Fatalf("an answer's batch read back as %q", got[0].Batch)
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
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	base := DecisionAnswer{Run: "r", Stage: "review", Batch: "b-1", Check: "trace-reason", Path: "a.go", Model: "jev-1.13.0", Cutoff: 0.8}
	both := base
	both.Probability, both.Failure = Known(0.5), "timeout"
	unattributed := base
	unattributed.Run, unattributed.Probability = "", Known(0.5)
	unbatched := base
	unbatched.Batch, unbatched.Probability = " ", Known(0.5)
	for name, a := range map[string]DecisionAnswer{"both": both, "neither": base, "no run": unattributed,
		"no batch": unbatched} {
		if _, err := s.AppendDecisionAnswer(ctx, a); err == nil {
			t.Errorf("a row with %s was stored", name)
		}
	}
	if got, err := s.DecisionAnswers(ctx, "r"); err != nil || len(got) != 0 {
		t.Fatalf("refused rows reached the record: %+v (%v)", got, err)
	}
}

// A row's measures and rate read back as written, every field apart. A row
// appended with no measures, as every row before migration 70 was and as a
// path the stage could not ask about is, reads back unmeasured rather than as
// a call that sent nothing and took no time, and a row with no rate reads back
// unpriced rather than as a price of zero.
func TestDecisionMeasuresReadBackAsWrittenAndUnknownStaysUnknown(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	measures := DecisionMeasures{
		Requests: 3, ReportedRequests: 1, UnreportedRequests: 1, ReportedInputTokens: 120,
		ReportedOutputTokens: 4, UnreportedSentBytes: 900, UnreportedReceivedBytes: 70,
		Elapsed: 1500 * time.Millisecond,
	}
	rate := DecisionRate{InputUSDPerMillionTokens: 0.5, OutputUSDPerMillionTokens: 2, Source: "a test's figures"}
	reported := DecisionMeasures{Requests: 2, ReportedRequests: 2, ReportedInputTokens: 300,
		ReportedOutputTokens: 2, Elapsed: 900 * time.Millisecond}
	priced := DecisionAnswer{Run: "run-1", Stage: "review", Batch: "b-1", Check: "trace-reason", Path: "a.go",
		Model: "jev-1.13.0", Probability: Known(0.4), Cutoff: 0.8, Measures: Known(reported), Rate: Known(rate)}
	unpriced := priced
	unpriced.Path, unpriced.Measures, unpriced.Rate = "b.go", Known(measures), Unknown[DecisionRate]()
	unmeasured := unpriced
	unmeasured.Path, unmeasured.Measures = "c.go", Unknown[DecisionMeasures]()
	for _, a := range []DecisionAnswer{priced, unpriced, unmeasured} {
		if _, err := s.AppendDecisionAnswer(ctx, a); err != nil {
			t.Fatalf("appending %+v: %v", a, err)
		}
	}
	got, err := s.DecisionAnswers(ctx, "run-1")
	if err != nil || len(got) != 3 {
		t.Fatalf("the record is %+v (%v), want three rows", got, err)
	}
	for i, want := range []DecisionAnswer{priced, unpriced, unmeasured} {
		if got[i].Measures != want.Measures || got[i].Rate != want.Rate {
			t.Fatalf("row %d read back measures %+v and rate %+v, want %+v and %+v", i,
				got[i].Measures, got[i].Rate, want.Measures, want.Rate)
		}
	}
}

// The store owns the rule that a row carries a rate only when it was priced,
// so it holds every writer to it: a rate on a row with no measures, on a row
// where any request reported no tokens, with no source, or with a figure that
// is negative or not a finite number is refused, and a sourced rate on a fully
// reported row is kept.
func TestADecisionRateIsKeptOnlyOnAPricedRow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	reported := DecisionMeasures{Requests: 2, ReportedRequests: 2, ReportedInputTokens: 300, ReportedOutputTokens: 2}
	sourced := DecisionRate{InputUSDPerMillionTokens: 1, OutputUSDPerMillionTokens: 4, Source: "a test's figures"}
	row := func(path string, m Optional[DecisionMeasures], r DecisionRate) DecisionAnswer {
		return DecisionAnswer{Run: "r", Stage: "review", Batch: "b-1", Check: "trace-reason", Path: path, Model: "jev-1.13.0",
			Probability: Known(0.5), Cutoff: 0.8, Measures: m, Rate: Known(r)}
	}
	partly := reported
	partly.ReportedRequests, partly.UnreportedRequests, partly.UnreportedSentBytes = 1, 1, 800
	unanswered := reported
	unanswered.Requests = 3
	with := func(in, out float64, source string) DecisionRate {
		return DecisionRate{InputUSDPerMillionTokens: in, OutputUSDPerMillionTokens: out, Source: source}
	}
	for name, a := range map[string]DecisionAnswer{
		"no measures":                      row("a.go", Unknown[DecisionMeasures](), sourced),
		"a request reported without usage": row("a.go", Known(partly), sourced),
		"a request with no reply":          row("a.go", Known(unanswered), sourced),
		"no source":                        row("a.go", Known(reported), with(1, 4, " ")),
		"a negative figure":                row("a.go", Known(reported), with(-1, 4, "x")),
		"a figure that is not a number":    row("a.go", Known(reported), with(1, math.NaN(), "x")),
		"an infinite figure":               row("a.go", Known(reported), with(math.Inf(1), 4, "x")),
	} {
		if _, err := s.AppendDecisionAnswer(ctx, a); err == nil {
			t.Errorf("a rate on a row with %s was stored", name)
		}
	}
	kept := row("b.go", Known(reported), sourced)
	if _, err := s.AppendDecisionAnswer(ctx, kept); err != nil {
		t.Fatalf("a sourced rate on a fully reported row was refused: %v", err)
	}
	got, err := s.DecisionAnswers(ctx, "r")
	if err != nil || len(got) != 1 || got[0].Rate != kept.Rate || got[0].Measures != kept.Measures {
		t.Fatalf("the record is %+v (%v), want only the priced row as written", got, err)
	}
}

// A batch reads back as written, a batch whose checks were off included, and a
// batch naming no batch, run, stage, or mode, one whose batch is already
// appended, or one with a negative time, is refused.
func TestDecisionBatchesReadBackAndRefuseWhatNamesNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	on := DecisionBatch{Batch: "b-1", Run: "run-1", Stage: "review", Mode: "evaluate", Sends: true, Elapsed: 2 * time.Second}
	off := DecisionBatch{Batch: "b-2", Run: "run-1", Stage: "review", Mode: "off"}
	for _, b := range []DecisionBatch{on, off} {
		if _, err := s.AppendDecisionBatch(ctx, b); err != nil {
			t.Fatalf("appending %+v: %v", b, err)
		}
	}
	for name, b := range map[string]DecisionBatch{
		"no batch": {Run: "run-1", Stage: "review", Mode: "off"}, "no run": {Batch: "b-3", Stage: "review", Mode: "off"},
		"no stage": {Batch: "b-3", Run: "run-1", Mode: "off"}, "no mode": {Batch: "b-3", Run: "run-1", Stage: "review"},
		"a batch already appended": {Batch: "b-1", Run: "run-1", Stage: "push", Mode: "off"},
		"negative elapsed":         {Batch: "b-3", Run: "run-1", Stage: "review", Mode: "off", Elapsed: -time.Second},
	} {
		if _, err := s.AppendDecisionBatch(ctx, b); err == nil {
			t.Errorf("a batch with %s was stored", name)
		}
	}
	got, err := s.DecisionBatches(ctx, "run-1")
	if err != nil || len(got) != 2 {
		t.Fatalf("run-1's batches are %+v (%v), want two", got, err)
	}
	for i, want := range []DecisionBatch{on, off} {
		g := got[i]
		if g.Batch != want.Batch || g.Run != want.Run || g.Stage != want.Stage || g.Mode != want.Mode ||
			g.Sends != want.Sends || g.Elapsed != want.Elapsed || g.Seq == 0 || g.Recorded.IsZero() {
			t.Fatalf("batch %d read back as %+v, want %+v", i, g, want)
		}
	}
}
