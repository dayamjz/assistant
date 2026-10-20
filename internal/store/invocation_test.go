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
	t.Parallel()
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
	t.Parallel()
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

// A row names the run and stage it served when it was handed them, and a row
// handed neither reads both back unknown rather than as an empty name. A
// known but blank name is refused, because unknown is the one spelling of a
// call that served none.
func TestAnAgentInvocationKeepsTheRunAndStageItServed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	started := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	served := AgentInvocation{Purpose: "fix", Agent: "claude", SessionUse: "resumed", Started: started,
		Run: Known("run-1"), Stage: Known("review")}
	unattributed := AgentInvocation{Purpose: "intent", Agent: "claude", SessionUse: "none", Started: started}
	for _, inv := range []AgentInvocation{served, unattributed} {
		if _, err := s.AppendAgentInvocation(ctx, inv); err != nil {
			t.Fatalf("AppendAgentInvocation: %v", err)
		}
	}
	for name, blank := range map[string]AgentInvocation{
		"run":   {Purpose: "fix", Agent: "claude", Run: Known(" "), Stage: Known("review")},
		"stage": {Purpose: "fix", Agent: "claude", Run: Known("run-1"), Stage: Known("")},
	} {
		if _, err := s.AppendAgentInvocation(ctx, blank); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("a blank known %s was accepted or refused for another reason: %v", name, err)
		}
	}

	rows, err := s.AgentInvocations(ctx)
	if err != nil {
		t.Fatalf("AgentInvocations: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("history holds %d rows, want the 2 accepted", len(rows))
	}
	if run, ok := rows[0].Run.Get(); !ok || run != "run-1" {
		t.Errorf("the run reads back as (%q, %v)", run, ok)
	}
	if stage, ok := rows[0].Stage.Get(); !ok || stage != "review" {
		t.Errorf("the stage reads back as (%q, %v)", stage, ok)
	}
	if rows[1].Run.IsKnown() || rows[1].Stage.IsKnown() {
		t.Errorf("a call naming neither reads back as %v / %v, want unknown for both", rows[1].Run, rows[1].Stage)
	}
}

// The windowed read returns exactly the rows whose call started at or after
// the window's start, in appended order. The row started before it is the
// control that the filter filters; the one started exactly at it pins the
// boundary as inclusive.
func TestAgentInvocationsSinceReadsTheWindowByWhenACallStarted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	since := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	for i, started := range []time.Time{since.Add(-time.Nanosecond), since, since.Add(time.Hour)} {
		if _, err := s.AppendAgentInvocation(ctx, AgentInvocation{Purpose: "review", Agent: "claude",
			SessionUse: "none", Started: started, Model: string(rune('a' + i))}); err != nil {
			t.Fatalf("AppendAgentInvocation: %v", err)
		}
	}
	all, err := s.AgentInvocations(ctx)
	if err != nil || len(all) != 3 {
		t.Fatalf("the whole history reads %d rows (%v), want 3", len(all), err)
	}
	got, err := s.AgentInvocationsSince(ctx, since)
	if err != nil {
		t.Fatalf("AgentInvocationsSince: %v", err)
	}
	if len(got) != 2 || got[0].Model != "b" || got[1].Model != "c" {
		t.Fatalf("the window read %+v, want the two rows started at or after its start", got)
	}
}

// A run's rows are the rows naming it and nothing else: a row naming another
// run and a row naming none are left out, so a per-run total cannot take in a
// call the run did not make.
func TestAgentInvocationsForRunReadsOnlyTheRowsNamingTheRun(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	started := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	for i, run := range []Optional[string]{Known("run-1"), Known("run-2"), Unknown[string](), Known("run-1")} {
		if _, err := s.AppendAgentInvocation(ctx, AgentInvocation{Purpose: "review", Agent: "claude",
			SessionUse: "none", Started: started, Model: string(rune('a' + i)), Run: run}); err != nil {
			t.Fatalf("AppendAgentInvocation: %v", err)
		}
	}
	got, err := s.AgentInvocationsForRun(ctx, "run-1")
	if err != nil {
		t.Fatalf("AgentInvocationsForRun: %v", err)
	}
	if len(got) != 2 || got[0].Model != "a" || got[1].Model != "d" {
		t.Fatalf("run-1 reads %+v, want the two rows naming it", got)
	}
	if none, err := s.AgentInvocationsForRun(ctx, "run-3"); err != nil || len(none) != 0 {
		t.Fatalf("a run with no calls reads %+v, %v; want nothing", none, err)
	}
}

// The run-and-stage migration is additive: that one migration is undone by
// hand, a row is written without its columns, and the shipped list is
// applied again, which runs only the undone migration since every other is
// recorded. The earlier row reads back unchanged with its run and stage
// unknown, never a guess, and a row naming both can be written beside it.
// The migration is found by name, so the test does not spell a version the
// shipped list may grow past.
func TestTheInvocationRunAndStageMigrationKeepsEarlierRowsUnknown(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	var added migration
	for _, m := range schema {
		if m.name == invocationServedMigration {
			added = m
		}
	}
	if added.version == 0 {
		t.Fatalf("the shipped schema has no migration named %q", invocationServedMigration)
	}
	// Back to the shape the migration before it left. A migration may not do
	// this, which is why it is done here by hand and not through one.
	for _, column := range []string{"run", "stage"} {
		if _, err := s.write.ExecContext(ctx, `ALTER TABLE agent_invocation DROP COLUMN `+column); err != nil {
			t.Fatalf("rolling the schema back: %v", err)
		}
	}
	if _, err := s.write.ExecContext(ctx, `DELETE FROM schema_migration WHERE version = ?`, added.version); err != nil {
		t.Fatalf("forgetting the migration: %v", err)
	}
	started := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	if _, err := s.write.ExecContext(ctx, `
		INSERT INTO agent_invocation (purpose, agent, model, session_use, started, duration_ns, failure, input_tokens)
		VALUES ('review', 'claude', 'some-model', 'none', ?, 5, '', 900)`, encodeTime(started)); err != nil {
		t.Fatalf("writing a row the old way: %v", err)
	}
	if err := migrate(ctx, s.write, schema); err != nil {
		t.Fatalf("applying the shipped migration over the old row: %v", err)
	}
	if _, err := s.AppendAgentInvocation(ctx, AgentInvocation{Purpose: "fix", Agent: "claude", SessionUse: "none",
		Started: started, Run: Known("run-1"), Stage: Known("review")}); err != nil {
		t.Fatalf("writing a row naming its run and stage: %v", err)
	}
	rows, err := s.AgentInvocations(ctx)
	if err != nil {
		t.Fatalf("AgentInvocations: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("history holds %d rows, want 2", len(rows))
	}
	old := rows[0]
	if old.Purpose != "review" || old.Model != "some-model" || !old.Started.Equal(started) || old.Duration != 5 {
		t.Errorf("the earlier row reads back as %+v", old)
	}
	if n, ok := old.InputTokens.Get(); !ok || n != 900 {
		t.Errorf("the earlier row's input tokens read back as (%d, %v)", n, ok)
	}
	if old.Run.IsKnown() || old.Stage.IsKnown() {
		t.Errorf("the earlier row reads its run and stage as %v / %v, want unknown for both", old.Run, old.Stage)
	}
	if !rows[1].Run.IsKnown() || !rows[1].Stage.IsKnown() {
		t.Errorf("the later row lost its run or stage: %+v", rows[1])
	}
	// The mixed case the per-run read meets across the migration: the row
	// written before it names no run and is in no run's rows, while a call
	// recorded with the run's id after it is that run's, so a run that began
	// before the column still has the spend of every call attributed to it
	// afterwards.
	own, err := s.AgentInvocationsForRun(ctx, "run-1")
	if err != nil {
		t.Fatalf("AgentInvocationsForRun: %v", err)
	}
	if len(own) != 1 || own[0].Purpose != "fix" {
		t.Errorf("run-1 reads %+v, want only the call recorded with its id after the migration", own)
	}
}

// A launch that names no provider stores that as not named, which reads back
// known and empty, and a row written before the columns reads each of them
// unknown. The two are not the same fact.
func TestAnInvocationRecordsTheRoleItWasAskedFor(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	started := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	named := AgentInvocation{Purpose: "review", Agent: "claude", SessionUse: "none", Started: started,
		Role: Known("review"), Provider: Known("anthropic"), Type: Known("opus"), Version: Known("5.5"), Effort: Known("high")}
	unnamed := AgentInvocation{Purpose: "review", Agent: "claude", SessionUse: "none", Started: started,
		Role: Known("review"), Provider: Known(""), Type: Known(""), Version: Known(""), Effort: Known("")}
	for _, inv := range []AgentInvocation{named, unnamed} {
		if _, err := s.AppendAgentInvocation(ctx, inv); err != nil {
			t.Fatalf("AppendAgentInvocation: %v", err)
		}
	}
	rows, err := s.AgentInvocations(ctx)
	if err != nil {
		t.Fatalf("AgentInvocations: %v", err)
	}
	if provider, ok := rows[0].Provider.Get(); !ok || provider != "anthropic" || rows[0].Effort.String() != "high" || rows[0].Type.String() != "opus" {
		t.Fatalf("the named launch reads back as provider %v type %v effort %v", rows[0].Provider, rows[0].Type, rows[0].Effort)
	}
	if provider, ok := rows[1].Provider.Get(); !ok || provider != "" || rows[1].Effort.String() != "" {
		t.Fatalf("the unnamed launch reads back as provider %v effort %v, want known and empty", rows[1].Provider, rows[1].Effort)
	}

	var added migration
	for _, m := range schema {
		if m.name == invocationRoleMigration {
			added = m
		}
	}
	if added.version == 0 {
		t.Fatalf("the shipped schema has no migration named %q", invocationRoleMigration)
	}
	for _, column := range []string{"role", "provider", "model_type", "version", "effort"} {
		if _, err := s.write.ExecContext(ctx, `ALTER TABLE agent_invocation DROP COLUMN `+column); err != nil {
			t.Fatalf("rolling the schema back: %v", err)
		}
	}
	if _, err := s.write.ExecContext(ctx, `DELETE FROM schema_migration WHERE version = ?`, added.version); err != nil {
		t.Fatalf("forgetting the migration: %v", err)
	}
	if _, err := s.write.ExecContext(ctx, `
		INSERT INTO agent_invocation (purpose, agent, model, session_use, started, duration_ns, failure)
		VALUES ('lint', 'claude', '', 'none', ?, 1, '')`, encodeTime(started)); err != nil {
		t.Fatalf("writing a row the old way: %v", err)
	}
	if err := migrate(ctx, s.write, schema); err != nil {
		t.Fatalf("applying the shipped migration over the old row: %v", err)
	}
	rows, err = s.AgentInvocations(ctx)
	if err != nil {
		t.Fatalf("AgentInvocations after the migration: %v", err)
	}
	old := rows[len(rows)-1]
	if old.Purpose != "lint" || old.Role.IsKnown() || old.Provider.IsKnown() || old.Type.IsKnown() || old.Version.IsKnown() || old.Effort.IsKnown() {
		t.Fatalf("the earlier row reads its role attribution as %+v, want unknown", old)
	}
}
