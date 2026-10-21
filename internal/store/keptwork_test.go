package store

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// A kept work entry is written once per copy and the commits it holds: the
// same copy holding the same commits is the entry already written, and the
// same copy holding other commits is a new one. The project and the branch
// are read off the owner's row, and the detail passes the redactor.
func TestKeptWorkIsWrittenOncePerCopyAndWhatItHolds(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)

	first, wrote, err := s.RecordKeptWork(ctx, KeptCopy{RunID: run.ID, CopyPath: "/home/worktrees/repo-1/run-1",
		Holds: []string{"aaaa"}, Reason: "unproven", Detail: "fetched https://user:secret@example.test/one.git"})
	if err != nil || !wrote {
		t.Fatalf("RecordKeptWork = %v, wrote %v; want a new entry", err, wrote)
	}
	if first.RepositoryID.Or("") != "repo-1" || first.Branch.Or("") != "topic" || first.TaskID.IsKnown() || first.RunID.Or("") != run.ID {
		t.Fatalf("entry = %+v; want the run's repository and branch read off its row", first)
	}
	if strings.Contains(first.Detail, "secret") {
		t.Fatalf("detail %q kept a credential", first.Detail)
	}
	if first.Answered() || first.SurfacedAt.IsKnown() {
		t.Fatalf("a new entry reads answered or surfaced: %+v", first)
	}

	again, wrote, err := s.RecordKeptWork(ctx, KeptCopy{RunID: run.ID, CopyPath: "/home/worktrees/repo-1/run-1",
		Holds: []string{"aaaa"}, Reason: "unproven", Detail: "asked again"})
	if err != nil || wrote || again.ID != first.ID || again.Detail != first.Detail {
		t.Fatalf("the same copy and commits again = %+v, wrote %v, %v; want the first entry unchanged", again, wrote, err)
	}
	moved, wrote, err := s.RecordKeptWork(ctx, KeptCopy{RunID: run.ID, CopyPath: "/home/worktrees/repo-1/run-1",
		Holds: []string{"cccc"}, Reason: "copy-dirty"})
	if err != nil || !wrote || moved.ID == first.ID {
		t.Fatalf("the same copy holding other commits = %+v, wrote %v, %v; want a second entry", moved, wrote, err)
	}
	unanswered, err := s.OpenKeptWork(ctx)
	if err != nil || len(unanswered) != 2 || unanswered[0].ID != first.ID || !slices.Equal(unanswered[0].Holds, []string{"aaaa"}) {
		t.Fatalf("OpenKeptWork = %+v, %v; want both, oldest first", unanswered, err)
	}

	for name, k := range map[string]KeptCopy{
		"no owner":      {CopyPath: "/c", Reason: "unproven"},
		"two owners":    {TaskID: "t", RunID: run.ID, CopyPath: "/c", Reason: "unproven"},
		"no copy":       {RunID: run.ID, Reason: "unproven"},
		"no reason":     {RunID: run.ID, CopyPath: "/c"},
		"missing owner": {TaskID: "nobody", CopyPath: "/c", Reason: "unproven"},
	} {
		if _, _, err := s.RecordKeptWork(ctx, k); err == nil {
			t.Errorf("%s: RecordKeptWork wrote an entry", name)
		}
	}
	if _, _, err := s.RecordKeptWork(ctx, KeptCopy{TaskID: "nobody", CopyPath: "/c", Reason: "unproven"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("an owner that is not there = %v; want ErrNotFound", err)
	}
}

// The answer is the entry's one revision and is made once, and the seals
// refuse every other write: a second answer through the accessor and
// directly, a rewrite of what the entry holds, a surfaced instant moved
// back, and a delete while the owner stands. Each seal is shown refusing
// against the real schema rather than assumed.
func TestKeptWorkIsAnsweredOnceAndOtherwiseSealed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	seedTask(t, s, "taskkeptwork")
	entry, _, err := s.RecordKeptWork(ctx, KeptCopy{TaskID: "taskkeptwork", CopyPath: "/home/tasks/taskkeptwork/copy",
		Holds: []string{"aaaa", "bbbb"}, Reason: "unproven", Detail: "none of the three proofs could be established"})
	if err != nil {
		t.Fatal(err)
	}
	if entry.RepositoryID.IsKnown() || entry.Branch.IsKnown() {
		t.Fatalf("a task naming no repository wrote one: %+v", entry)
	}

	at := time.Now().UTC()
	if err := s.MarkKeptWorkSurfaced(ctx, entry.ID, at); err != nil {
		t.Fatalf("MarkKeptWorkSurfaced: %v", err)
	}
	if err := s.MarkKeptWorkSurfaced(ctx, entry.ID, at.Add(-time.Minute)); err == nil {
		t.Fatal("when the entry surfaced moved back")
	}
	if err := s.MarkKeptWorkSurfaced(ctx, 999, at); !errors.Is(err, ErrNotFound) {
		t.Fatalf("surfacing an unknown entry = %v; want ErrNotFound", err)
	}

	if _, err := s.AnswerKeptWork(ctx, entry.ID, "keep", ResolvedByMachineInterface()); !errors.Is(err, ErrUnknownKeptAnswer) {
		t.Fatalf("an answer outside the set = %v; want ErrUnknownKeptAnswer", err)
	}
	if _, err := s.AnswerKeptWork(ctx, entry.ID, KeptDiscard, Resolver{}); !errors.Is(err, ErrNoResolver) {
		t.Fatalf("an answer from nobody = %v; want ErrNoResolver", err)
	}
	answered, err := s.AnswerKeptWork(ctx, entry.ID, KeptDiscard, ResolvedByMachineInterface())
	if err != nil {
		t.Fatalf("AnswerKeptWork: %v", err)
	}
	if answered.Answer.Or("") != KeptDiscard || answered.AnsweredBy.Or(Resolver{}) != ResolvedByMachineInterface() || !answered.AnsweredAt.IsKnown() {
		t.Fatalf("answered = %+v; want discard by the machine interface, timed", answered)
	}
	if _, err := s.AnswerKeptWork(ctx, entry.ID, KeptLand, ResolvedByMachineInterface()); !errors.Is(err, ErrKeptWorkAnswered) {
		t.Fatalf("a second answer = %v; want ErrKeptWorkAnswered", err)
	}
	if got, err := s.KeptWorkEntry(ctx, entry.ID); err != nil || got.Answer.Or("") != KeptDiscard {
		t.Fatalf("after a refused second answer the entry reads %+v, %v; want the first answer", got, err)
	}
	if unanswered, err := s.OpenKeptWork(ctx); err != nil || len(unanswered) != 0 {
		t.Fatalf("OpenKeptWork = %+v, %v; want none once answered", unanswered, err)
	}

	for name, statement := range map[string]string{
		"a second answer":   `UPDATE kept_work SET answer = 'land' WHERE id = ?`,
		"a rewritten holds": `UPDATE kept_work SET holds = '[]' WHERE id = ?`,
		"a moved owner":     `UPDATE kept_work SET task_id = NULL WHERE id = ?`,
		"a delete":          `DELETE FROM kept_work WHERE id = ?`,
	} {
		if _, err := s.write.ExecContext(ctx, statement, entry.ID); err == nil || !strings.Contains(err.Error(), "kept work entry") {
			t.Errorf("%s was not refused by the seal: %v", name, err)
		}
	}
}

// An entry closes without an answer only when its owner's copy is marked
// removed on a proof, in that transaction: the open entry is closed as
// KeptRemovedOnProof with the proof, it is no longer listed, an answer to it
// is refused, and the closure cannot be written again or reopened. An entry
// a person already answered keeps its answer, and recording a proof alone,
// with no removal, closes nothing.
func TestKeptWorkClosesOnlyWhenItsCopyIsRemovedOnAProof(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)
	open, _, err := s.RecordKeptWork(ctx, KeptCopy{RunID: run.ID, CopyPath: "/w/run-1", Holds: []string{"aaaa"}, Reason: "unproven"})
	if err != nil {
		t.Fatal(err)
	}
	answered, _, err := s.RecordKeptWork(ctx, KeptCopy{RunID: run.ID, CopyPath: "/w/run-1", Holds: []string{"bbbb"}, Reason: "copy-dirty"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AnswerKeptWork(ctx, answered.ID, KeptCarryOn, ResolvedByMachineInterface()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordRunCleanup(ctx, RunCleanup{Run: run.ID, Proof: Known(CleanupProofRemoteBranch), Examined: []string{"aaaa"}}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.KeptWorkEntry(ctx, open.ID); err != nil || !got.Open() {
		t.Fatalf("a proof recorded with no removal closed the entry: %+v, %v", got, err)
	}
	if err := s.MarkRunCleanupRemoved(ctx, run.ID, time.Now()); err != nil {
		t.Fatalf("MarkRunCleanupRemoved: %v", err)
	}
	closed, err := s.KeptWorkEntry(ctx, open.ID)
	if err != nil || closed.Closure.Or("") != KeptRemovedOnProof || closed.ClosedProof.Or("") != CleanupProofRemoteBranch ||
		!closed.ClosedAt.IsKnown() || closed.Answered() || closed.Open() {
		t.Fatalf("after the removal the entry reads %+v, %v; want it closed as removed on the remote-branch proof", closed, err)
	}
	if kept, err := s.KeptWorkEntry(ctx, answered.ID); err != nil || kept.Answer.Or("") != KeptCarryOn || kept.Closure.IsKnown() {
		t.Fatalf("the answered entry reads %+v, %v; want its answer kept and no closure", kept, err)
	}
	if listed, err := s.OpenKeptWork(ctx); err != nil || len(listed) != 0 {
		t.Fatalf("OpenKeptWork = %+v, %v; want none", listed, err)
	}
	if _, err := s.AnswerKeptWork(ctx, open.ID, KeptDiscard, ResolvedByMachineInterface()); !errors.Is(err, ErrKeptWorkClosed) {
		t.Fatalf("answering a closed entry = %v; want ErrKeptWorkClosed", err)
	}
	for name, statement := range map[string]string{
		"a second closure":   `UPDATE kept_work SET closed_at = '2000-01-01T00:00:00.000000000Z' WHERE id = ?`,
		"a reopening":        `UPDATE kept_work SET closure = NULL, closed_proof = NULL, closed_at = NULL WHERE id = ?`,
		"an answer after it": `UPDATE kept_work SET answer = 'land', answered_by = 'person', answered_at = closed_at WHERE id = ?`,
	} {
		if _, err := s.write.ExecContext(ctx, statement, open.ID); err == nil || !strings.Contains(err.Error(), "kept work entry") {
			t.Errorf("%s was not refused by the seal: %v", name, err)
		}
	}
	if _, err := s.write.ExecContext(ctx, `UPDATE kept_work SET closure = 'removed-on-proof', closed_proof = 'remote-branch', closed_at = answered_at WHERE id = ?`,
		answered.ID); err == nil || !strings.Contains(err.Error(), "kept work entry") {
		t.Errorf("closing an answered entry was not refused by the seal: %v", err)
	}
}

// A repository whose run's copy is open kept work is not forgotten: the
// preflight and the removal both refuse with a *KeptWorkOpenError naming the
// entry's copy, its owner and what it holds, and nothing is removed. Once
// the copy is removed on a proof the entry is closed and the repository is
// forgotten with it.
func TestARepositoryWithOpenKeptWorkIsNotForgotten(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)
	if _, err := s.TransitionRun(ctx, run.ID, []RunStatus{RunPending}, RunRunning); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TransitionRun(ctx, run.ID, []RunStatus{RunRunning}, RunFailed); err != nil {
		t.Fatal(err)
	}
	entry, _, err := s.RecordKeptWork(ctx, KeptCopy{RunID: run.ID, CopyPath: "/home/worktrees/repo-1/run-1",
		Holds: []string{"aaaa"}, Reason: "unproven"})
	if err != nil {
		t.Fatal(err)
	}
	for name, ask := range map[string]func() error{
		"RepositoryRemovable": func() error { return s.RepositoryRemovable(ctx, run.RepositoryID) },
		"ForgetRepository":    func() error { _, err := s.ForgetRepository(ctx, run.RepositoryID); return err },
	} {
		err := ask()
		var open *KeptWorkOpenError
		if !errors.As(err, &open) || !errors.Is(err, ErrKeptWorkOpen) || len(open.Entries) != 1 || open.Entries[0].ID != entry.ID {
			t.Fatalf("%s = %v, want a KeptWorkOpenError naming entry %d", name, err, entry.ID)
		}
		for _, want := range []string{entry.CopyPath, "run " + run.ID, "aaaa", "#411 part 2", "discard"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("%s's refusal %q does not name %q", name, err, want)
			}
		}
	}
	if _, err := s.Run(ctx, run.ID); err != nil {
		t.Fatalf("the refused removal took the run: %v", err)
	}
	if got, err := s.KeptWorkEntry(ctx, entry.ID); err != nil || !got.Open() {
		t.Fatalf("the refused removal touched the entry: %+v, %v", got, err)
	}

	if _, err := s.RecordRunCleanup(ctx, RunCleanup{Run: run.ID, Proof: Known(CleanupProofRemoteBranch), Examined: []string{"aaaa"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkRunCleanupRemoved(ctx, run.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.RepositoryRemovable(ctx, run.RepositoryID); err != nil {
		t.Fatalf("RepositoryRemovable once the entry closed = %v", err)
	}
	if _, err := s.ForgetRepository(ctx, run.RepositoryID); err != nil {
		t.Fatalf("ForgetRepository once the entry closed = %v", err)
	}
	if _, err := s.KeptWorkEntry(ctx, entry.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the closed entry survived the forgetting: %v", err)
	}
}

// A kept work entry and eject's reserved preflight have exactly one loser
// in either order. An entry committed first refuses the reservation with the
// typed refusal naming it. A reservation committed first refuses the entry
// with ErrRepositoryBeingRemoved and writes nothing, and once the
// reservation is released the same entry is recorded. A reserved repository
// is forgotten with its reservation.
func TestAKeptWorkEntryAndARemovalReservationHaveExactlyOneLoser(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	finished := func(s *Store) Run {
		run := seedRun(t, s)
		if _, err := s.TransitionRun(ctx, run.ID, []RunStatus{RunPending}, RunRunning); err != nil {
			t.Fatal(err)
		}
		if _, err := s.TransitionRun(ctx, run.ID, []RunStatus{RunRunning}, RunFailed); err != nil {
			t.Fatal(err)
		}
		return run
	}
	kept := func(run Run) KeptCopy {
		return KeptCopy{RunID: run.ID, CopyPath: "/w/" + run.ID, Holds: []string{"aaaa"}, Reason: "unproven"}
	}

	entryFirst := openStore(t)
	run := finished(entryFirst)
	if _, _, err := entryFirst.RecordKeptWork(ctx, kept(run)); err != nil {
		t.Fatal(err)
	}
	var open *KeptWorkOpenError
	if err := entryFirst.ReserveRepositoryRemoval(ctx, run.RepositoryID); !errors.As(err, &open) {
		t.Fatalf("reserving the removal after the entry = %v, want a KeptWorkOpenError", err)
	}

	reservedFirst := openStore(t)
	run = finished(reservedFirst)
	if err := reservedFirst.ReserveRepositoryRemoval(ctx, run.RepositoryID); err != nil {
		t.Fatalf("ReserveRepositoryRemoval: %v", err)
	}
	if _, _, err := reservedFirst.RecordKeptWork(ctx, kept(run)); !errors.Is(err, ErrRepositoryBeingRemoved) {
		t.Fatalf("recording kept work after the reservation = %v, want ErrRepositoryBeingRemoved", err)
	}
	if listed, err := reservedFirst.OpenKeptWork(ctx); err != nil || len(listed) != 0 {
		t.Fatalf("the refused entry was written: %+v, %v", listed, err)
	}
	if err := reservedFirst.ReleaseRepositoryRemoval(ctx, run.RepositoryID); err != nil {
		t.Fatal(err)
	}
	if _, wrote, err := reservedFirst.RecordKeptWork(ctx, kept(run)); err != nil || !wrote {
		t.Fatalf("recording kept work once the reservation was released = %v, wrote %v", err, wrote)
	}

	forgotten := openStore(t)
	run = finished(forgotten)
	if err := forgotten.ReserveRepositoryRemoval(ctx, run.RepositoryID); err != nil {
		t.Fatal(err)
	}
	if _, err := forgotten.ForgetRepository(ctx, run.RepositoryID); err != nil {
		t.Fatalf("forgetting a reserved repository = %v", err)
	}
	if n := tableRows(t, forgotten, "repository_removal"); n != 0 {
		t.Fatalf("the reservation outlived its repository: %d rows", n)
	}
}
