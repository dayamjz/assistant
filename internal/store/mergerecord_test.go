package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

// mergeRecordVersion is the shipped migration the older-database test below
// is named for, pinned on the terms runFixerSessionVersion is.
const mergeRecordVersion = 13

// The two ends this package defines are recognized, each renders the one
// reason PRD section 5 fixes for it, and nothing else is a member: the zero
// value in particular is not, so a transition asked to record "no end" as an
// end is refused rather than writing an empty word.
func TestPullRequestEndsAreAClosedSet(t *testing.T) {
	t.Parallel()
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

// runningRun creates a run and moves it to running.
func runningRun(t *testing.T, s *Store) Run {
	t.Helper()
	ctx := context.Background()
	seedRepository(t, s)
	r, err := s.CreateRun(ctx, completeRun())
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	running, err := s.TransitionRun(ctx, r.ID, []RunStatus{RunPending}, RunRunning)
	if err != nil {
		t.Fatalf("pending to running: %v", err)
	}
	return running
}

var closedRead = PullRequestRead{PullRequest: 7, Head: "0123abc", End: PullRequestClosed}

// A run its pull request ended moves and writes its merge record in one
// transaction, and the record is the one read of what ended it. The control
// is TransitionRun on the same run: an ordinary move leaves no record, so a
// record read back after the other transition is that transition's write.
func TestTransitionRunOnPullRequestWritesTheMergeRecordWithTheStatus(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	running := runningRun(t, s)
	if _, found, err := s.MergeRecord(ctx, running.ID); err != nil || found {
		t.Fatalf("an ordinary move left a merge record: %v, %v", found, err)
	}

	ended, err := s.TransitionRunOnPullRequest(ctx, running.ID, []RunStatus{RunRunning, RunHeld}, RunFailed, closedRead, HoldClosure{})
	if err != nil {
		t.Fatalf("running to failed on a closed pull request: %v", err)
	}
	if ended.Status != RunFailed {
		t.Fatalf("the run came back %s, want failed", ended.Status)
	}
	record, found, err := s.MergeRecord(ctx, running.ID)
	if err != nil || !found {
		t.Fatalf("the merge record was not written: %v, %v", found, err)
	}
	if record.Run != running.ID || record.PullRequest != 7 || record.Head != "0123abc" || record.End != PullRequestClosed {
		t.Fatalf("the merge record is %+v, want the read that ended the run", record)
	}
	if !record.RecordedAt.Equal(ended.UpdatedAt) {
		t.Fatalf("the record was stamped %s and the move %s; the two are one transaction", record.RecordedAt, ended.UpdatedAt)
	}
}

// A merge record is written once: the table refuses to update or delete a
// row, so no later write can make the record say other than what the read
// saw. The write itself is the control, because the refusal would otherwise
// be indistinguishable from a table nothing can write.
func TestAMergeRecordIsWrittenOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	running := runningRun(t, s)
	if _, err := s.TransitionRunOnPullRequest(ctx, running.ID, []RunStatus{RunRunning}, RunPassed,
		PullRequestRead{PullRequest: 7, Head: "0123abc", End: PullRequestMerged}, HoldClosure{}); err != nil {
		t.Fatalf("running to passed on a merged pull request: %v", err)
	}
	for _, verb := range []string{
		`UPDATE merge_record SET pull_request_end = 'closed' WHERE run = ?`,
		`DELETE FROM merge_record WHERE run = ?`,
	} {
		err := s.inTx(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, verb, running.ID)
			return err
		})
		if err == nil {
			t.Fatalf("%q was accepted on a merge record", verb)
		}
	}
	record, found, err := s.MergeRecord(ctx, running.ID)
	if err != nil || !found || record.End != PullRequestMerged {
		t.Fatalf("the merge record after the refused writes is %+v, %v, %v; want the one written", record, found, err)
	}
}

// The transition is anchored on TransitionRun's terms and refuses what it
// cannot record before it touches the run, so the record cannot come to hold
// a status its merge record does not account for or a record the read would
// refuse.
func TestTransitionRunOnPullRequestRefusesWhatItCannotRecord(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	running := runningRun(t, s)
	for _, outside := range []PullRequestEnd{"", "open", "MERGED"} {
		read := PullRequestRead{PullRequest: 7, Head: "0123abc", End: outside}
		_, err := s.TransitionRunOnPullRequest(ctx, running.ID, []RunStatus{RunRunning}, RunPassed, read, HoldClosure{})
		if !errors.Is(err, ErrUnknownPullRequestEnd) {
			t.Fatalf("an end of %q was answered %v, want ErrUnknownPullRequestEnd", outside, err)
		}
	}
	if _, err := s.TransitionRunOnPullRequest(ctx, running.ID, []RunStatus{RunRunning}, RunPassed,
		PullRequestRead{PullRequest: 0, End: PullRequestMerged}, HoldClosure{}); err == nil {
		t.Fatal("a read of pull request 0 was recorded")
	}
	// Anchored: a run that is not where the caller said is refused, and left
	// where it is with no record written.
	_, err := s.TransitionRunOnPullRequest(ctx, running.ID, []RunStatus{RunHeld}, RunPassed,
		PullRequestRead{PullRequest: 7, End: PullRequestMerged}, HoldClosure{})
	var refused *RunStatusError
	if !errors.As(err, &refused) || !errors.Is(err, ErrRunStatus) {
		t.Fatalf("moving a running run out of held: %v", err)
	}
	after, err := s.Run(ctx, running.ID)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, found, _ := s.MergeRecord(ctx, running.ID); after.Status != RunRunning || found {
		t.Fatalf("a refused transition changed the run: %+v, record %v", after, found)
	}
}

// A run already in the status it is being moved to is left as it stands,
// record included, which is what makes a retry after a commit the caller did
// not see report what that commit established rather than writing a second
// record the table would refuse.
func TestTransitionRunOnPullRequestToTheStatusTheRunAlreadyHoldsWritesNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	running := runningRun(t, s)
	merged := PullRequestRead{PullRequest: 7, Head: "0123abc", End: PullRequestMerged}
	first, err := s.TransitionRunOnPullRequest(ctx, running.ID, []RunStatus{RunRunning}, RunPassed, merged, HoldClosure{})
	if err != nil {
		t.Fatalf("running to passed on a merged pull request: %v", err)
	}
	again, err := s.TransitionRunOnPullRequest(ctx, running.ID, []RunStatus{RunRunning}, RunPassed,
		PullRequestRead{PullRequest: 8, Head: "ffff", End: PullRequestMerged}, HoldClosure{})
	if err != nil {
		t.Fatalf("passed to passed: %v", err)
	}
	record, _, _ := s.MergeRecord(ctx, running.ID)
	if record.PullRequest != 7 || !again.UpdatedAt.Equal(first.UpdatedAt) {
		t.Fatalf("the repeated transition rewrote the run or its record: %+v, %+v", again, record)
	}
}

// The one closure the program makes is written with the move: the hold is
// registered if it was not, resolved as the closure says, and the run moved,
// in one transaction. A move the run's status refuses rolls the resolution
// back with it, so no hold ever reads as resolved by the program on a run the
// program did not move; and a hold already resolved is left as it stands
// while the move still happens, which is the repair for a record left that
// way by any other path.
func TestAHoldClosureIsWrittenWithTheMoveOrNotAtAll(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	running := runningRun(t, s)
	held, err := s.TransitionRun(ctx, running.ID, []RunStatus{RunRunning}, RunHeld)
	if err != nil {
		t.Fatalf("running to held: %v", err)
	}
	closing := HoldClosure{
		Hold:       Hold{Key: "run/" + held.ID + "/checks-timeout", RunID: Known(held.ID), Subject: "checks_timeout elapsed"},
		Resolution: "pull request 7 read as merged",
		By:         ResolvedByProgram(),
	}
	merged := PullRequestRead{PullRequest: 7, Head: "0123abc", End: PullRequestMerged}

	// Refused: the run is not where the caller said, and the hold is left
	// unregistered rather than registered and resolved.
	_, err = s.TransitionRunOnPullRequest(ctx, held.ID, []RunStatus{RunRunning}, RunPassed, merged, closing)
	if !errors.Is(err, ErrRunStatus) {
		t.Fatalf("moving a held run out of running: %v", err)
	}
	if _, err := s.Hold(ctx, closing.Hold.Key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a refused move left the hold behind: %v", err)
	}

	// Refused on the closure's own terms, before the run is touched.
	for name, bad := range map[string]HoldClosure{
		"no resolution": {Hold: closing.Hold, Resolution: " ", By: ResolvedByProgram()},
		"no resolver":   {Hold: closing.Hold, Resolution: "x"},
		"no subject":    {Hold: Hold{Key: "k"}, Resolution: "x", By: ResolvedByProgram()},
	} {
		if _, err := s.TransitionRunOnPullRequest(ctx, held.ID, []RunStatus{RunHeld}, RunPassed, merged, bad); err == nil {
			t.Fatalf("a closure with %s was written", name)
		}
	}
	if stood, _ := s.Run(ctx, held.ID); stood.Status != RunHeld {
		t.Fatalf("a refused closure moved the run to %s", stood.Status)
	}

	// Written: hold and move together.
	passed, err := s.TransitionRunOnPullRequest(ctx, held.ID, []RunStatus{RunHeld}, RunPassed, merged, closing)
	if err != nil {
		t.Fatalf("held to passed with the closure: %v", err)
	}
	hold, err := s.Hold(ctx, closing.Hold.Key)
	if err != nil {
		t.Fatalf("the closed hold: %v", err)
	}
	by, _ := hold.ResolvedBy.Get()
	resolution, _ := hold.Resolution.Get()
	if hold.Open() || by != ResolvedByProgram() || resolution != closing.Resolution || passed.Status != RunPassed {
		t.Fatalf("the closure left hold %+v and run %s", hold, passed.Status)
	}

	// Repair: a hold already resolved, with the run still held, is left as
	// it stands and the move still happens.
	other, err := s.CreateRun(ctx, func() Run { r := completeRun(); r.ID = "run-2"; return r }())
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	for _, to := range []RunStatus{RunRunning, RunHeld} {
		if _, err := s.TransitionRun(ctx, other.ID, RunStatuses(), to); err != nil {
			t.Fatalf("to %s: %v", to, err)
		}
	}
	pre := closing
	pre.Hold.Key = "run/" + other.ID + "/checks-timeout"
	if _, err := s.RegisterHold(ctx, pre.Hold); err != nil {
		t.Fatalf("RegisterHold: %v", err)
	}
	if _, err := s.ResolveHold(ctx, pre.Hold.Key, "earlier", ResolvedByProgram()); err != nil {
		t.Fatalf("ResolveHold: %v", err)
	}
	moved, err := s.TransitionRunOnPullRequest(ctx, other.ID, []RunStatus{RunHeld}, RunPassed, merged, pre)
	if err != nil {
		t.Fatalf("the repair move: %v", err)
	}
	repaired, _ := s.Hold(ctx, pre.Hold.Key)
	if earlier, _ := repaired.Resolution.Get(); earlier != "earlier" || moved.Status != RunPassed {
		t.Fatalf("the repair rewrote the hold to %+v or left the run %s", repaired, moved.Status)
	}
}

// A database from before the table existed gains it empty, so every run
// recorded then reads as having no merge record rather than a fabricated one,
// and the migrated database then takes the transition.
func TestTheMergeRecordMigrationReachesAnOlderDatabase(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")

	older, err := openPool(ctx, poolDSN(path, true), true)
	if err != nil {
		t.Fatalf("open the older database: %v", err)
	}
	if err := migrate(ctx, older, schemaBefore(t, mergeRecordVersion)); err != nil {
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
	if _, found, err := s.MergeRecord(ctx, "run-1"); err != nil || found {
		t.Fatalf("a run recorded before the table existed reads a merge record: %v, %v", found, err)
	}
	ended, err := s.TransitionRunOnPullRequest(ctx, "run-1", []RunStatus{RunHeld}, RunPassed,
		PullRequestRead{PullRequest: 7, Head: "0123abc", End: PullRequestMerged}, HoldClosure{})
	if err != nil {
		t.Fatalf("TransitionRunOnPullRequest after the migration: %v", err)
	}
	if record, found, _ := s.MergeRecord(ctx, "run-1"); !found || record.End != PullRequestMerged || ended.Status != RunPassed {
		t.Fatalf("the migrated run records %+v, %v and stands %s", record, found, ended.Status)
	}
}

// The closure is written in the transaction that writes the merge record,
// and not in one of its own before or after it: a merge record already in
// the table for the held run makes the record's INSERT fail on the primary
// key after the hold has been registered and resolved inside the
// transaction, and the whole of it rolls back, leaving the run held and no
// hold row at all. The refused-status cases cannot show this, because they
// refuse before any hold write; a closure written in a second transaction
// would pass them and fail here.
func TestTheHoldClosureRollsBackWithARefusedMergeRecord(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	running := runningRun(t, s)
	held, err := s.TransitionRun(ctx, running.ID, []RunStatus{RunRunning}, RunHeld)
	if err != nil {
		t.Fatalf("running to held: %v", err)
	}
	// Seeded by hand: no accessor writes a record for a run that has not
	// moved, which is the property this test leans on.
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO merge_record (run, pull_request, head, pull_request_end, recorded_at)
			VALUES (?, ?, ?, ?, ?)`, held.ID, 7, "0123abc", string(PullRequestMerged), encodeTime(nowUTC()))
		return err
	})
	if err != nil {
		t.Fatalf("seeding a merge record: %v", err)
	}
	closing := HoldClosure{
		Hold:       Hold{Key: "run/" + held.ID + "/checks-timeout", RunID: Known(held.ID), Subject: "checks_timeout elapsed"},
		Resolution: "pull request 7 read as merged",
		By:         ResolvedByProgram(),
	}
	_, err = s.TransitionRunOnPullRequest(ctx, held.ID, []RunStatus{RunHeld}, RunPassed,
		PullRequestRead{PullRequest: 7, Head: "0123abc", End: PullRequestMerged}, closing)
	if err == nil {
		t.Fatal("a second merge record for the run was written")
	}
	stood, err := s.Run(ctx, held.ID)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if stood.Status != RunHeld {
		t.Fatalf("the refused transaction moved the run to %s", stood.Status)
	}
	if _, err := s.Hold(ctx, closing.Hold.Key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the hold survived the refused transaction: %v; the closure was written outside it", err)
	}
}
