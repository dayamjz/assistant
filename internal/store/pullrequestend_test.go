package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// runPullRequestEndVersion is the shipped migration the older-database test
// below is named for, pinned on the terms runFixerSessionVersion is.
const runPullRequestEndVersion = 13

// The two ends this package defines are recognized, each renders the one
// reason PRD section 5 fixes for it, and nothing else is a member: the zero
// value in particular is not, so a transition asked to record "no end" as an
// end is refused rather than storing an empty word.
func TestPullRequestEndsAreAClosedSet(t *testing.T) {
	for _, end := range PullRequestEnds() {
		if !end.Recognized() {
			t.Errorf("%s is in the set and not recognized", end)
		}
		if end.Reason() == "" {
			t.Errorf("%s renders no reason", end)
		}
	}
	if got := PullRequestClosed.Reason(); got != "pull request closed without merging" {
		t.Errorf("a close renders %q, want the sentence PRD section 5 fixes", got)
	}
	for _, outside := range []PullRequestEnd{"", "open", "MERGED", "unknown"} {
		if outside.Recognized() {
			t.Errorf("%q is recognized as an end", outside)
		}
		if outside.Reason() != "" {
			t.Errorf("%q renders a reason: %q", outside, outside.Reason())
		}
	}
	ends := PullRequestEnds()
	ends[0] = "open"
	if again := PullRequestEnds(); again[0] != PullRequestMerged {
		t.Errorf("writing to the result of PullRequestEnds changed the set: it now starts with %s", again[0])
	}
}

// A run its pull request ended records the end with the status it moved to,
// in one write, and a run moved any other way records none. The control is
// TransitionRun on the same run: the column stays unknown through an ordinary
// move, so what the test reads back after the other transition is that
// transition's write and not a default.
func TestTransitionRunOnPullRequestRecordsTheEndWithTheStatus(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	seedRepository(t, s)
	r, err := s.CreateRun(ctx, completeRun())
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	running, err := s.TransitionRun(ctx, r.ID, []RunStatus{RunPending}, RunRunning)
	if err != nil {
		t.Fatalf("pending to running: %v", err)
	}
	if running.PullRequestEnd.IsKnown() {
		t.Fatalf("an ordinary move recorded a pull request end: %v", running.PullRequestEnd)
	}

	ended, err := s.TransitionRunOnPullRequest(ctx, r.ID, []RunStatus{RunRunning, RunHeld}, RunFailed, PullRequestClosed)
	if err != nil {
		t.Fatalf("running to failed on a closed pull request: %v", err)
	}
	if ended.Status != RunFailed {
		t.Fatalf("the run came back %s, want failed", ended.Status)
	}
	if end, known := ended.PullRequestEnd.Get(); !known || end != PullRequestClosed {
		t.Fatalf("the run records the end %v, want closed", ended.PullRequestEnd)
	}
	read, err := s.Run(ctx, r.ID)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if end, known := read.PullRequestEnd.Get(); !known || end != PullRequestClosed {
		t.Fatalf("reading the run back finds the end %v, want closed", read.PullRequestEnd)
	}
}

// The transition is anchored on TransitionRun's terms and refuses an end
// outside the set before it touches the run, so the record cannot come to
// hold a status its end does not account for or an end the read would refuse.
func TestTransitionRunOnPullRequestRefusesWhatItCannotRecord(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	seedRepository(t, s)
	r, err := s.CreateRun(ctx, completeRun())
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if _, err := s.TransitionRun(ctx, r.ID, []RunStatus{RunPending}, RunRunning); err != nil {
		t.Fatalf("pending to running: %v", err)
	}
	for _, outside := range []PullRequestEnd{"", "open", "MERGED"} {
		_, err := s.TransitionRunOnPullRequest(ctx, r.ID, []RunStatus{RunRunning}, RunPassed, outside)
		if !errors.Is(err, ErrUnknownPullRequestEnd) {
			t.Fatalf("an end of %q was answered %v, want ErrUnknownPullRequestEnd", outside, err)
		}
	}
	// Anchored: a run that is not where the caller said is refused, and left
	// where it is with no end written.
	_, err = s.TransitionRunOnPullRequest(ctx, r.ID, []RunStatus{RunHeld}, RunPassed, PullRequestMerged)
	var refused *RunStatusError
	if !errors.As(err, &refused) || !errors.Is(err, ErrRunStatus) {
		t.Fatalf("moving a running run out of held: %v", err)
	}
	after, err := s.Run(ctx, r.ID)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if after.Status != RunRunning || after.PullRequestEnd.IsKnown() {
		t.Fatalf("a refused transition changed the run: %+v", after)
	}
}

// A run already in the status it is being moved to is left as it stands, end
// included, which is what makes a retry after a commit the caller did not see
// report what that commit established rather than rewriting it.
func TestTransitionRunOnPullRequestToTheStatusTheRunAlreadyHoldsWritesNothing(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	seedRepository(t, s)
	r, err := s.CreateRun(ctx, completeRun())
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if _, err := s.TransitionRun(ctx, r.ID, []RunStatus{RunPending}, RunRunning); err != nil {
		t.Fatalf("pending to running: %v", err)
	}
	first, err := s.TransitionRunOnPullRequest(ctx, r.ID, []RunStatus{RunRunning}, RunPassed, PullRequestMerged)
	if err != nil {
		t.Fatalf("running to passed on a merged pull request: %v", err)
	}
	// A different end, to a status the run already holds: the first write
	// stands.
	again, err := s.TransitionRunOnPullRequest(ctx, r.ID, []RunStatus{RunRunning}, RunPassed, PullRequestClosed)
	if err != nil {
		t.Fatalf("passed to passed: %v", err)
	}
	if end, _ := again.PullRequestEnd.Get(); end != PullRequestMerged || !again.UpdatedAt.Equal(first.UpdatedAt) {
		t.Fatalf("the repeated transition rewrote the run: %+v, first %+v", again, first)
	}
}

// A run recorded before the column existed reads back with no end rather than
// with a fabricated one, which is the promise Optional exists to keep, and the
// migrated database then takes the new transition.
func TestThePullRequestEndMigrationReachesAnOlderDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")

	older, err := openPool(ctx, poolDSN(path, true), true)
	if err != nil {
		t.Fatalf("open the older database: %v", err)
	}
	if err := migrate(ctx, older, schemaBefore(t, runPullRequestEndVersion)); err != nil {
		t.Fatalf("migrate to the older schema: %v", err)
	}
	now := encodeTime(nowUTC())
	if _, err := older.ExecContext(ctx, `
		INSERT INTO repository (id, working_path, upstream_url, fork_url, default_branch, created_at, updated_at)
		VALUES (?, ?, ?, NULL, ?, ?, ?)`,
		"repo-1", "/checkouts/one", "https://example.test/one.git", "main", now, now); err != nil {
		t.Fatalf("write a repository against the older schema: %v", err)
	}
	if _, err := older.ExecContext(ctx, `
		INSERT INTO run (id, repository_id, branch, submitted_head, base, status,
			intent, intent_source, build_version, build_revision, build_modified,
			build_go, config_digest, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"run-1", "repo-1", "topic", "aaaa", "bbbb", string(RunHeld),
		"validate", "push", "v0.1.0", "0123456789abcdef", 0, "go1.25.0", "cfg-1",
		now, now); err != nil {
		t.Fatalf("write a run against the older schema: %v", err)
	}
	if err := older.Close(); err != nil {
		t.Fatalf("close the older database: %v", err)
	}

	s := openStoreAt(t, path)
	got, err := s.Run(ctx, "run-1")
	if err != nil {
		t.Fatalf("the run written against the older schema did not survive: %v", err)
	}
	if got.PullRequestEnd.IsKnown() {
		t.Fatalf("a run recorded before the column existed reports a pull request end: %v", got.PullRequestEnd)
	}
	ended, err := s.TransitionRunOnPullRequest(ctx, "run-1", []RunStatus{RunHeld}, RunPassed, PullRequestMerged)
	if err != nil {
		t.Fatalf("TransitionRunOnPullRequest after the migration: %v", err)
	}
	if end, known := ended.PullRequestEnd.Get(); !known || end != PullRequestMerged {
		t.Fatalf("the migrated run records the end %v, want merged", ended.PullRequestEnd)
	}
}
