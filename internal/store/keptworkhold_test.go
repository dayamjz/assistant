package store

import (
	"context"
	"errors"
	"testing"

	"github.com/dayamjz/assistant/internal/principles"
)

// keptHoldTask creates a delivery task on one repository's branch, with no
// attempt.
func keptHoldTask(t *testing.T, s *Store, id, repo, branch string) {
	t.Helper()
	if _, err := s.CreateTask(context.Background(), Task{
		ID: id, Shape: TaskDelivery, Project: repo, Mode: "interactive", WorktreePath: "/worktrees/" + id,
		RepositoryID: Known(repo), Branch: Known(branch),
	}, Disposition{Observed: newestOnBranch(t, s, repo, branch)}); err != nil {
		t.Fatalf("CreateTask %s: %v", id, err)
	}
}

// While a project has an open kept work entry, a new task in it is held at
// its dispatch claim and nowhere else: BeginDispatchAttempt refuses it with
// the typed refusal naming the entry and writes no attempt, KeptWorkHolding
// reads the same entry, and the relaunch claim is not held. A task in
// another project, a task launched before, and a task naming no project are
// not held, and an entry naming no project holds nothing. Once the entry is
// answered the same task dispatches, read from the record again and from
// nothing cached (P8).
func TestOpenKeptWorkHoldsANewTasksDispatchInItsProjectOnly(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	principles.Cite(t, principles.P8)
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s) // repo-1, branch topic
	if _, err := s.UpsertRepository(ctx, Repository{ID: "repo-2", WorkingPath: "/checkouts/two",
		UpstreamURL: "https://example.test/two.git", DefaultBranch: "main"}); err != nil {
		t.Fatal(err)
	}
	keptHoldTask(t, s, "taskheldnewa", "repo-1", "one")
	keptHoldTask(t, s, "taskrelaunch", "repo-1", "two")
	keptHoldTask(t, s, "tasklaunched", "repo-1", "three")
	keptHoldTask(t, s, "taskelsewher", "repo-2", "one")
	seedTask(t, s, "tasknoprojct")
	a := beginAttempt(t, s, "tasklaunched", 0)
	if err := s.SetAttemptSession(ctx, "tasklaunched", a.Sequence, "ref-1"); err != nil {
		t.Fatal(err)
	}

	// An entry naming no project holds nothing anywhere.
	if _, _, err := s.RecordKeptWork(ctx, KeptCopy{TaskID: "tasknoprojct", CopyPath: "/tasks/tasknoprojct/copy",
		Holds: []string{"cccc"}, Reason: "unproven"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"taskheldnewa", "taskelsewher", "tasknoprojct"} {
		if got, err := s.KeptWorkHolding(ctx, id); err != nil || len(got) != 0 {
			t.Fatalf("with only a project-less entry, KeptWorkHolding(%s) = %+v, %v; want none", id, got, err)
		}
	}

	entry, _, err := s.RecordKeptWork(ctx, KeptCopy{RunID: run.ID, CopyPath: "/worktrees/repo-1/run-1",
		Holds: []string{"aaaa"}, Reason: "unproven"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.KeptWorkHolding(ctx, "taskheldnewa")
	if err != nil || len(got) != 1 || got[0].ID != entry.ID {
		t.Fatalf("KeptWorkHolding(held) = %+v, %v; want the run's entry", got, err)
	}
	_, err = s.BeginDispatchAttempt(ctx, "taskheldnewa", 0)
	var held *DispatchHeldError
	if !errors.As(err, &held) || !errors.Is(err, ErrDispatchHeld) || held.Task != "taskheldnewa" || held.Repository != "repo-1" ||
		len(held.Entries) != 1 || held.Entries[0].ID != entry.ID {
		t.Fatalf("BeginDispatchAttempt(held) = %v; want a *DispatchHeldError naming entry %d", err, entry.ID)
	}
	if attempts, err := s.TaskAttempts(ctx, "taskheldnewa"); err != nil || len(attempts) != 0 {
		t.Fatalf("a held dispatch wrote attempts %+v, %v", attempts, err)
	}
	for _, id := range []string{"tasklaunched", "taskelsewher", "tasknoprojct"} {
		if got, err := s.KeptWorkHolding(ctx, id); err != nil || len(got) != 0 {
			t.Errorf("KeptWorkHolding(%s) = %+v, %v; want none", id, got, err)
		}
	}
	if _, err := s.BeginDispatchAttempt(ctx, "taskelsewher", 0); err != nil {
		t.Errorf("a task in another project was refused: %v", err)
	}
	if _, err := s.BeginDispatchAttempt(ctx, "tasklaunched", a.Sequence); err != nil {
		t.Errorf("a task launched before was refused: %v", err)
	}
	if _, err := s.BeginTaskAttempt(ctx, "taskrelaunch", 0); err != nil {
		t.Errorf("the relaunch claim was held: %v", err)
	}
	if _, err := s.KeptWorkHolding(ctx, "tasknobodyaa"); !errors.Is(err, ErrNotFound) {
		t.Errorf("KeptWorkHolding(nobody) = %v; want ErrNotFound", err)
	}
	if kept, err := s.OpenKeptWorkOf(ctx, "repo-1"); err != nil || len(kept) != 1 || kept[0].ID != entry.ID {
		t.Errorf("OpenKeptWorkOf(repo-1) = %+v, %v; want the run's entry", kept, err)
	}

	if _, err := s.AnswerKeptWork(ctx, entry.ID, KeptLand, ResolvedByMachineInterface()); err != nil {
		t.Fatal(err)
	}
	if got, err := s.KeptWorkHolding(ctx, "taskheldnewa"); err != nil || len(got) != 0 {
		t.Fatalf("after the answer KeptWorkHolding(held) = %+v, %v; want none", got, err)
	}
	if _, err := s.BeginDispatchAttempt(ctx, "taskheldnewa", 0); err != nil {
		t.Fatalf("after the answer the dispatch was refused: %v", err)
	}
	if kept, err := s.OpenKeptWorkOf(ctx, "repo-1"); err != nil || len(kept) != 0 {
		t.Errorf("after the answer OpenKeptWorkOf(repo-1) = %+v, %v; want none", kept, err)
	}
}

// The launch claim asks the same hold in its own transaction: a held task's
// claim is refused with the typed refusal and writes nothing, and an unheld
// task's is written once and a second is refused. A claim alone is not work
// under way: a task whose launch was claimed and whose worker never started,
// with no session recorded, is held by an entry recorded afterwards at its
// next dispatch claim and its next launch claim, which is the sequence of a
// provider that refused the process after the claim. Only a recorded session
// exempts a task from then on.
func TestALaunchClaimWithNoStartedWorkerLeavesItsTaskHeld(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)
	keptHoldTask(t, s, "taskclaimeda", "repo-1", "one")
	keptHoldTask(t, s, "taskheldlnch", "repo-1", "two")
	keptHoldTask(t, s, "taskstarteda", "repo-1", "three")
	claimed := beginAttempt(t, s, "taskclaimeda", 0)
	held := beginAttempt(t, s, "taskheldlnch", 0)
	started := beginAttempt(t, s, "taskstarteda", 0)
	if err := s.ClaimAttemptLaunch(ctx, "taskclaimeda", claimed.Sequence); err != nil {
		t.Fatalf("an unheld launch claim: %v", err)
	}
	if err := s.ClaimAttemptLaunch(ctx, "taskclaimeda", claimed.Sequence); !errors.Is(err, ErrAttemptLaunchClaimed) {
		t.Fatalf("a second launch claim = %v; want ErrAttemptLaunchClaimed", err)
	}
	if err := s.ClaimAttemptLaunch(ctx, "taskclaimeda", 9); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a claim of an attempt that does not exist = %v; want ErrNotFound", err)
	}
	if err := s.ClaimAttemptLaunch(ctx, "taskstarteda", started.Sequence); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAttemptSession(ctx, "taskstarteda", started.Sequence, "ref-started"); err != nil {
		t.Fatal(err)
	}

	if _, _, err := s.RecordKeptWork(ctx, KeptCopy{RunID: run.ID, CopyPath: "/worktrees/repo-1/run-1",
		Holds: []string{"aaaa"}, Reason: "unproven"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ClaimAttemptLaunch(ctx, "taskheldlnch", held.Sequence); !errors.Is(err, ErrDispatchHeld) {
		t.Fatalf("a held task's launch claim = %v; want ErrDispatchHeld", err)
	}
	if err := s.ClaimAttemptLaunch(ctx, "taskheldlnch", held.Sequence); !errors.Is(err, ErrDispatchHeld) {
		t.Fatalf("the refused claim wrote something: a second ask = %v; want ErrDispatchHeld again", err)
	}

	// The claimed launch whose worker never started is held like a fresh task.
	if got, err := s.KeptWorkHolding(ctx, "taskclaimeda"); err != nil || len(got) != 1 {
		t.Fatalf("a task whose launch was claimed and never started is held by %+v, %v; want the entry", got, err)
	}
	if _, err := s.BeginDispatchAttempt(ctx, "taskclaimeda", claimed.Sequence); !errors.Is(err, ErrDispatchHeld) {
		t.Fatalf("its next dispatch = %v; want ErrDispatchHeld", err)
	}
	// A task whose worker started is work under way.
	if got, err := s.KeptWorkHolding(ctx, "taskstarteda"); err != nil || len(got) != 0 {
		t.Fatalf("a task whose worker started is held by %+v, %v; want nothing", got, err)
	}
	next, err := s.BeginDispatchAttempt(ctx, "taskstarteda", started.Sequence)
	if err != nil {
		t.Fatalf("the next attempt of a task whose worker started: %v", err)
	}
	if err := s.ClaimAttemptLaunch(ctx, "taskstarteda", next.Sequence); err != nil {
		t.Fatalf("the next launch of a task whose worker started: %v", err)
	}
}
