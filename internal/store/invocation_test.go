package store

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The history keeps the distinction the recording package is built around: a
// count the agent reported, a reported zero, and a count nothing was reported
// for are three different rows, and reading them back tells them apart.
func TestAgentInvocationsKeepReportedAndUnreportedCountsApart(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	started := time.Date(2026, 9, 19, 18, 30, 0, 123456789, time.UTC)

	reported := AgentInvocation{
		Purpose:             "review",
		Agent:               "claude",
		Model:               "some-model",
		SessionUse:          "none",
		Started:             started,
		Duration:            90 * time.Second,
		Failure:             "",
		InputTokens:         Known[int64](1200),
		OutputTokens:        Known[int64](0),
		CacheReadTokens:     Known[int64](88000),
		CacheCreationTokens: Known[int64](300),
		Turns:               Known[int64](4),
	}
	silent := AgentInvocation{
		Purpose:    "fix",
		Agent:      "claude",
		SessionUse: "resumed",
		Started:    started.Add(2 * time.Minute),
		Duration:   5 * time.Second,
		Failure:    "process",
	}
	first, err := s.AppendAgentInvocation(ctx, reported)
	if err != nil {
		t.Fatalf("AppendAgentInvocation: %v", err)
	}
	second, err := s.AppendAgentInvocation(ctx, silent)
	if err != nil {
		t.Fatalf("AppendAgentInvocation: %v", err)
	}
	if first.Seq >= second.Seq {
		t.Errorf("sequences are %d then %d, want appended order", first.Seq, second.Seq)
	}

	rows, err := s.AgentInvocations(ctx)
	if err != nil {
		t.Fatalf("AgentInvocations: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("history holds %d rows, want 2", len(rows))
	}

	got := rows[0]
	if got.Purpose != "review" || got.Agent != "claude" || got.Model != "some-model" ||
		got.SessionUse != "none" || got.Failure != "" {
		t.Errorf("first row reads back %+v", got)
	}
	if !got.Started.Equal(started) || got.Duration != 90*time.Second {
		t.Errorf("first row timing reads back %v, %v", got.Started, got.Duration)
	}
	if n, ok := got.OutputTokens.Get(); !ok || n != 0 {
		t.Errorf("a reported zero reads back as (%d, %v), want a known zero", n, ok)
	}
	if n, ok := got.CacheReadTokens.Get(); !ok || n != 88000 {
		t.Errorf("cache reads read back as (%d, %v), want 88000 reported", n, ok)
	}

	got = rows[1]
	if got.Failure != "process" || got.SessionUse != "resumed" {
		t.Errorf("second row reads back %+v", got)
	}
	for name, count := range map[string]Optional[int64]{
		"input":          got.InputTokens,
		"output":         got.OutputTokens,
		"cache read":     got.CacheReadTokens,
		"cache creation": got.CacheCreationTokens,
		"turns":          got.Turns,
	} {
		if count.IsKnown() {
			t.Errorf("%s tokens of a silent invocation read back as %s, want unreported", name, count)
		}
	}
}

// A cost attributed to nothing answers no question the history exists for, so
// the append refuses it rather than storing it.
func TestAppendAgentInvocationRefusesAnUnattributedCost(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)

	_, err := s.AppendAgentInvocation(ctx, AgentInvocation{Agent: "claude", Started: time.Now()})
	if err == nil || !strings.Contains(err.Error(), "no purpose") {
		t.Errorf("append with no purpose: %v, want a refusal naming it", err)
	}
	_, err = s.AppendAgentInvocation(ctx, AgentInvocation{Purpose: "review", Started: time.Now()})
	if err == nil || !strings.Contains(err.Error(), "no agent") {
		t.Errorf("append with no agent: %v, want a refusal naming it", err)
	}
	rows, err := s.AgentInvocations(ctx)
	if err != nil {
		t.Fatalf("AgentInvocations: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("refused appends left %d rows", len(rows))
	}
}
