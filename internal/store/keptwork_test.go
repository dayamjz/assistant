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
	unanswered, err := s.UnansweredKeptWork(ctx)
	if err != nil || len(unanswered) != 2 || unanswered[0].ID != first.ID || !slices.Equal(unanswered[0].Holds, []string{"aaaa"}) {
		t.Fatalf("UnansweredKeptWork = %+v, %v; want both, oldest first", unanswered, err)
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
	if unanswered, err := s.UnansweredKeptWork(ctx); err != nil || len(unanswered) != 0 {
		t.Fatalf("UnansweredKeptWork = %+v, %v; want none once answered", unanswered, err)
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
