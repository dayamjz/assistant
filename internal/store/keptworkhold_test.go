package store

import (
	"context"
	"errors"
	"testing"
	"time"

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
// its dispatch claim and nowhere else: BeginFirstTaskAttempt refuses it with
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
	if err := s.SetAttemptSession(ctx, "tasklaunched", a.Sequence, "ref-1", SessionHost{}); err != nil {
		t.Fatal(err)
	}
	relaunched := beginAttempt(t, s, "taskrelaunch", 0)

	// An entry naming no project holds nothing anywhere.
	if _, _, err := s.RecordKeptWork(ctx, KeptCopy{TaskID: "tasknoprojct", CopyPath: "/tasks/tasknoprojct/copy",
		Holds: []string{"cccc"}, Reason: "unproven", Against: "mmmm"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"taskheldnewa", "taskelsewher", "tasknoprojct"} {
		if got, err := s.KeptWorkHolding(ctx, id); err != nil || len(got) != 0 {
			t.Fatalf("with only a project-less entry, KeptWorkHolding(%s) = %+v, %v; want none", id, got, err)
		}
	}

	entry, _, err := s.RecordKeptWork(ctx, KeptCopy{RunID: run.ID, CopyPath: "/worktrees/repo-1/run-1",
		Holds: []string{"aaaa"}, Reason: "unproven", Against: "mmmm"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.KeptWorkHolding(ctx, "taskheldnewa")
	if err != nil || len(got) != 1 || got[0].ID != entry.ID {
		t.Fatalf("KeptWorkHolding(held) = %+v, %v; want the run's entry", got, err)
	}
	_, err = s.BeginFirstTaskAttempt(ctx, "taskheldnewa", OnThisMachine())
	var held *DispatchHeldError
	if !errors.As(err, &held) || !errors.Is(err, ErrDispatchHeld) || held.Task != "taskheldnewa" || held.Repository != "repo-1" ||
		len(held.Entries) != 1 || held.Entries[0].ID != entry.ID {
		t.Fatalf("BeginFirstTaskAttempt(held) = %v; want a *DispatchHeldError naming entry %d", err, entry.ID)
	}
	if attempts, err := s.TaskAttempts(ctx, "taskheldnewa"); err != nil || len(attempts) != 0 {
		t.Fatalf("a held dispatch wrote attempts %+v, %v", attempts, err)
	}
	for _, id := range []string{"tasklaunched", "taskelsewher", "tasknoprojct"} {
		if got, err := s.KeptWorkHolding(ctx, id); err != nil || len(got) != 0 {
			t.Errorf("KeptWorkHolding(%s) = %+v, %v; want none", id, got, err)
		}
	}
	if _, err := s.BeginFirstTaskAttempt(ctx, "taskelsewher", OnThisMachine()); err != nil {
		t.Errorf("a task in another project was refused: %v", err)
	}
	if _, err := s.BeginDispatchAttempt(ctx, "tasklaunched", a.Sequence); err != nil {
		t.Errorf("a task launched before was refused: %v", err)
	}
	if _, err := s.BeginTaskAttempt(ctx, "taskrelaunch", relaunched.Sequence); err != nil {
		t.Errorf("the relaunch claim was held: %v", err)
	}
	if _, err := s.KeptWorkHolding(ctx, "tasknobodyaa"); !errors.Is(err, ErrNotFound) {
		t.Errorf("KeptWorkHolding(nobody) = %v; want ErrNotFound", err)
	}
	if kept, err := s.OpenKeptWorkOf(ctx, "repo-1"); err != nil || len(kept) != 1 || kept[0].ID != entry.ID {
		t.Errorf("OpenKeptWorkOf(repo-1) = %+v, %v; want the run's entry", kept, err)
	}

	landKept(t, s, entry.ID)
	if got, err := s.KeptWorkHolding(ctx, "taskheldnewa"); err != nil || len(got) != 0 {
		t.Fatalf("after the answer KeptWorkHolding(held) = %+v, %v; want none", got, err)
	}
	if _, err := s.BeginFirstTaskAttempt(ctx, "taskheldnewa", OnThisMachine()); err != nil {
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
	if err := s.SetAttemptSession(ctx, "taskstarteda", started.Sequence, "ref-started", SessionHost{}); err != nil {
		t.Fatal(err)
	}

	if _, _, err := s.RecordKeptWork(ctx, KeptCopy{RunID: run.ID, CopyPath: "/worktrees/repo-1/run-1",
		Holds: []string{"aaaa"}, Reason: "unproven", Against: "mmmm"}); err != nil {
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

// A carry-on claim holds the task it names only while its entry is open,
// the final review of 3088420: a task's copy is kept and the entry
// claimed for a carry on naming a new task, whose row is written; the new
// task is held by the claim, which is the positive control. The cleanup of
// the old task then records a proof and marks its copy removed, which
// closes the entry with the claim never carried out, and the carry on's
// mark is refused as closed. The new task is held by nothing then and
// dispatches. Without the closure read, the closed entry held it forever,
// since no mark or withdrawal can ever be written on it again.
func TestAClosedEntrysUnfinishedCarryOnHoldsNothing(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	ctx := context.Background()
	s := openStore(t)
	seedRun(t, s)
	keptHoldTask(t, s, "taskoldcopya", "repo-1", "old")
	entry, _, err := s.RecordKeptWork(ctx, KeptCopy{TaskID: "taskoldcopya", CopyPath: "/worktrees/taskoldcopya",
		Holds: []string{"aaaa"}, Reason: "unproven", Against: "mmmm"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimKeptWorkCarryOn(ctx, entry.ID, "tasknewcopya", "the-kept-copy", ResolvedByPerson()); err != nil {
		t.Fatal(err)
	}
	keptHoldTask(t, s, "tasknewcopya", "repo-1", "old")
	if held, err := s.KeptWorkHolding(ctx, "tasknewcopya"); err != nil || len(held) != 1 || held[0].ID != entry.ID {
		t.Fatalf("while its carry on is claimed the new task is held by %+v, %v; want the entry", held, err)
	}

	if _, err := s.RecordTaskCleanup(ctx, TaskCleanup{TaskID: "taskoldcopya", Proof: CleanupProofMergedPullRequest,
		Examined: []string{"aaaa"}}, TaskDelivery); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkCleanupRemoved(ctx, "taskoldcopya", time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	closed, err := s.KeptWorkEntry(ctx, entry.ID)
	if err != nil || !closed.Closure.IsKnown() || closed.CarriedOnAt.IsKnown() || closed.CarriedOnTask.Or("") != "tasknewcopya" {
		t.Fatalf("after the removal on a proof the entry reads %+v, %v; want it closed with the claim not carried out", closed, err)
	}
	if _, err := s.MarkKeptWorkCarriedOn(ctx, entry.ID, "tasknewcopya"); !errors.Is(err, ErrKeptWorkClosed) {
		t.Fatalf("the carry on's mark on the closed entry = %v; want ErrKeptWorkClosed", err)
	}
	if held, err := s.KeptWorkHolding(ctx, "tasknewcopya"); err != nil || len(held) != 0 {
		t.Fatalf("after the entry closed the new task is held by %+v, %v; want nothing", held, err)
	}
	if _, err := s.BeginFirstTaskAttempt(ctx, "tasknewcopya", OnThisMachine()); err != nil {
		t.Fatalf("the new task's dispatch after the entry closed = %v; want it claimed", err)
	}
}
