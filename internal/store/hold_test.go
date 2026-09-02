package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRegisterHoldIsIdempotent(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)

	h := Hold{Key: "run-1/ask/finding-3", RunID: Known(run.ID),
		Subject: "a finding nobody classified", Detail: "the reviewer omitted an action"}

	first, err := s.RegisterHold(ctx, h)
	if err != nil {
		t.Fatalf("RegisterHold: %v", err)
	}
	if !first.Open() {
		t.Fatal("a newly registered hold is not open")
	}
	if first.Resolution.IsKnown() || first.ResolvedAt.IsKnown() {
		t.Fatalf("a newly registered hold carries a resolution: %+v", first)
	}

	// Registering the same decision again is the same hold, not a second one,
	// and it does not disturb what is already there.
	h.Detail = "a different description of the same decision"
	second, err := s.RegisterHold(ctx, h)
	if err != nil {
		t.Fatalf("RegisterHold: %v", err)
	}
	if second.Detail != first.Detail || !second.OpenedAt.Equal(first.OpenedAt) {
		t.Fatalf("re-registering changed the hold: %+v then %+v", first, second)
	}
	open, err := s.OpenHolds(ctx)
	if err != nil {
		t.Fatalf("OpenHolds: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("registering the same key twice made %d holds", len(open))
	}
}

// A hold is closed only by an explicit resolution.
func TestResolveHoldRequiresAnExplicitResolution(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	seedRun(t, s)

	if _, err := s.RegisterHold(ctx, Hold{Key: "k", Subject: "a decision"}); err != nil {
		t.Fatalf("RegisterHold: %v", err)
	}
	for _, empty := range []string{"", "   ", "\t\n"} {
		if _, err := s.ResolveHold(ctx, "k", empty); !errors.Is(err, ErrNoResolution) {
			t.Fatalf("ResolveHold accepted %q as a resolution: %v", empty, err)
		}
	}
	held, err := s.Hold(ctx, "k")
	if err != nil {
		t.Fatalf("Hold: %v", err)
	}
	if !held.Open() {
		t.Fatal("a refused resolution closed the hold anyway")
	}

	resolved, err := s.ResolveHold(ctx, "k", "ship it")
	if err != nil {
		t.Fatalf("ResolveHold: %v", err)
	}
	if resolved.Open() {
		t.Fatal("an explicit resolution did not close the hold")
	}
	if answer, known := resolved.Resolution.Get(); !known || answer != "ship it" {
		t.Fatalf("the resolution came back as %q (known=%v)", answer, known)
	}
	if !resolved.ResolvedAt.IsKnown() {
		t.Fatal("a resolved hold has no resolution time")
	}
	open, err := s.OpenHolds(ctx)
	if err != nil {
		t.Fatalf("OpenHolds: %v", err)
	}
	if len(open) != 0 {
		t.Fatalf("a resolved hold is still listed as open: %+v", open)
	}
}

func TestResolveHoldRefusesASecondAnswer(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	if _, err := s.RegisterHold(ctx, Hold{Key: "k", Subject: "a decision"}); err != nil {
		t.Fatalf("RegisterHold: %v", err)
	}
	if _, err := s.ResolveHold(ctx, "k", "ship it"); err != nil {
		t.Fatalf("ResolveHold: %v", err)
	}
	_, err := s.ResolveHold(ctx, "k", "actually, do not")
	if err == nil {
		t.Fatal("ResolveHold overwrote a resolution that was already there")
	}
	if !strings.Contains(err.Error(), "already resolved") {
		t.Fatalf("the refusal does not say why: %v", err)
	}
	held, err := s.Hold(ctx, "k")
	if err != nil {
		t.Fatalf("Hold: %v", err)
	}
	if answer, _ := held.Resolution.Get(); answer != "ship it" {
		t.Fatalf("the first resolution was replaced by %q", answer)
	}

	if _, err := s.ResolveHold(ctx, "no-such-hold", "anything"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ResolveHold on a missing hold: %v", err)
	}
}

// PRD section 8: a hold is never closed by the surrounding work finishing.
func TestFinishingTheWorkDoesNotCloseAHold(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)
	task := seedTask(t, s, "task-1")

	for _, key := range []string{"on-the-run", "on-the-task"} {
		h := Hold{Key: key, Subject: "a decision"}
		if key == "on-the-run" {
			h.RunID = Known(run.ID)
		} else {
			h.TaskID = Known(task.ID)
		}
		if _, err := s.RegisterHold(ctx, h); err != nil {
			t.Fatalf("RegisterHold %s: %v", key, err)
		}
	}

	// Everything the surrounding work can do short of an explicit resolution.
	if err := s.SetRunStatus(ctx, run.ID, RunTerminated); err != nil {
		t.Fatalf("SetRunStatus: %v", err)
	}
	if _, err := s.SetTaskState(ctx, task.ID, "complete", "supervisor", Unknown[string]()); err != nil {
		t.Fatalf("SetTaskState: %v", err)
	}
	if _, err := s.AppendTaskEvent(ctx, task.ID, "finished", "worker exited"); err != nil {
		t.Fatalf("AppendTaskEvent: %v", err)
	}

	open, err := s.OpenHolds(ctx)
	if err != nil {
		t.Fatalf("OpenHolds: %v", err)
	}
	if len(open) != 2 {
		t.Fatalf("%d holds are still open after the work finished, want 2", len(open))
	}
}

func TestRegisterHoldRefusesIncompleteRecords(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	if _, err := s.RegisterHold(ctx, Hold{Subject: "a decision"}); err == nil {
		t.Fatal("RegisterHold accepted a hold with no key")
	}
	if _, err := s.RegisterHold(ctx, Hold{Key: "k"}); err == nil {
		t.Fatal("RegisterHold accepted a hold with no subject")
	}
	if _, err := s.Hold(ctx, "k"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a refused RegisterHold left a row behind: %v", err)
	}
}
