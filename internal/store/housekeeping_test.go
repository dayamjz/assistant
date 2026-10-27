package store

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/principles"
)

// relayed is the record tier these tests stand in for: an investigation is
// Complete once its findings relayed record is written, which is the one
// record a pass that was never promoted can reach that tier by. It reads
// the snapshot field internal/taskstate reads for the same answer.
func relayed(s TaskSnapshot) bool { return s.Relay.IsKnown() }

// createPass records a housekeeping pass of repo-1 with relayed as the tier.
func createPass(s *Store, id string) (Task, error) {
	return s.CreateTask(context.Background(), Task{
		ID: id, Shape: TaskInvestigation, Project: "one", Mode: "interactive", WorktreePath: "/tasks/" + id + "/copy",
		RepositoryID: Known("repo-1"), Role: config.RoleHousekeeping, Housekeeping: true,
	}, Disposition{Delivered: relayed})
}

func mustPass(t *testing.T, s *Store, id string) Task {
	t.Helper()
	task, err := createPass(s, id)
	if err != nil {
		t.Fatalf("creating pass %s: %v", id, err)
	}
	if !task.Housekeeping || task.Role != config.RoleHousekeeping {
		t.Fatalf("pass %s reads back %+v, want the mark and the housekeeping role", id, task)
	}
	return task
}

// relay writes the findings relayed record of a task, which closes a pass.
func relay(t *testing.T, s *Store, id string) {
	t.Helper()
	if _, _, err := s.StoreTaskReport(context.Background(), TaskReport{TaskID: id, Body: "nothing drifted",
		Report: []byte(`{"summary":"nothing drifted"}`)}, nil); err != nil {
		t.Fatalf("StoreTaskReport %s: %v", id, err)
	}
	history := historyOf(t, s, id)
	if _, _, err := s.RecordTaskRelay(context.Background(), id, history[len(history)-1].Sequence); err != nil {
		t.Fatalf("RecordTaskRelay %s: %v", id, err)
	}
}

// PRD section 6's one-at-a-time rule: a repository has at most one open
// pass, the refusal names it, and a pass in the terminal tier or retired no
// longer counts. Two creations at once leave exactly one task, because the
// check and the insert are one immediate transaction on the one writer.
func TestARepositoryHasAtMostOneOpenHousekeepingPass(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	seedRepository(t, s)
	first := mustPass(t, s, "taskhkaaaaaa")

	_, err := createPass(s, "taskhkaaaaab")
	var open *PassOpenError
	if !errors.As(err, &open) || !errors.Is(err, ErrPassOpen) || open.Pass != first.ID || open.Repository != "repo-1" {
		t.Fatalf("a second pass while %s is open = %v, want a *PassOpenError naming it", first.ID, err)
	}
	if _, err := s.Task(ctx, "taskhkaaaaab"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the refused pass was recorded: %v", err)
	}

	relay(t, s, first.ID)
	second := mustPass(t, s, "taskhkaaaaac")

	// A retired pass releases the claim too, though it resolves nothing.
	if _, err := s.RetireHousekeepingPass(ctx, Retirement{Task: second.ID, Hold: registerRetireHold(t, s, second.ID),
		By: ResolvedByMachineInterface(), Delivered: relayed}); err != nil {
		t.Fatalf("retiring %s: %v", second.ID, err)
	}

	var wg sync.WaitGroup
	results := make([]error, 2)
	for i, id := range []string{"taskhkaaaaad", "taskhkaaaaae"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, results[i] = createPass(s, id)
		}()
	}
	wg.Wait()
	created := 0
	for _, err := range results {
		switch {
		case err == nil:
			created++
		case !errors.Is(err, ErrPassOpen):
			t.Fatalf("a concurrent creation failed for another reason: %v", err)
		}
	}
	if created != 1 {
		t.Fatalf("two concurrent creations created %d passes, want exactly one: %v", created, results)
	}
}

// The mark is the schema's to hold: a pass is an investigation of a
// repository under the housekeeping role, no other task carries that role,
// the mark never moves, and CreateTask takes the record tier only for a
// pass.
func TestTheHousekeepingMarkIsWrittenOnceAtIntake(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	seedRepository(t, s)
	pass := mustPass(t, s, "taskhkaaaaba")
	plain := seedInvestigation(t, s, "taskhkaaaabb")

	for _, bad := range []struct {
		name string
		task Task
		d    Disposition
	}{
		{"a pass with no tier", Task{ID: "taskhkaaaabc", Shape: TaskInvestigation, Project: "one", WorktreePath: "/c",
			RepositoryID: Known("repo-1"), Role: config.RoleHousekeeping, Housekeeping: true}, Disposition{}},
		{"a delivery pass", Task{ID: "taskhkaaaabd", Shape: TaskDelivery, Project: "one", WorktreePath: "/c",
			RepositoryID: Known("repo-1"), Branch: Known("b"), Role: config.RoleHousekeeping, Housekeeping: true},
			Disposition{Delivered: relayed}},
		{"a pass of no repository", Task{ID: "taskhkaaaabe", Shape: TaskInvestigation, Project: "one", WorktreePath: "/c",
			Role: config.RoleHousekeeping, Housekeeping: true}, Disposition{Delivered: relayed}},
		{"a tier for a task that is no pass", Task{ID: "taskhkaaaabf", Shape: TaskInvestigation, Project: "one", WorktreePath: "/c"},
			Disposition{Delivered: relayed}},
	} {
		if _, err := s.CreateTask(ctx, bad.task, bad.d); err == nil {
			t.Errorf("%s was created", bad.name)
		}
	}
	for _, refused := range []struct{ name, statement string }{
		{"the role on a task that is no pass", `INSERT INTO task (id, shape, project, mode, worktree_path, created_at, role)
			VALUES ('taskhkaaaabg', 'investigation', 'one', '', '/c', '2026-01-01T00:00:00Z', 'housekeeping')`},
		{"a pass with another role", `INSERT INTO task (id, shape, project, mode, worktree_path, created_at, repository_id, housekeeping)
			VALUES ('taskhkaaaabh', 'investigation', 'one', '', '/c', '2026-01-01T00:00:00Z', 'repo-1', 1)`},
		{"unmarking a pass", `UPDATE task SET housekeeping = NULL WHERE id = '` + pass.ID + `'`},
		{"marking a task after its intake", `UPDATE task SET housekeeping = 1 WHERE id = '` + plain.ID + `'`},
	} {
		if err := rawWrite(ctx, s, refused.statement); err == nil {
			t.Errorf("the schema accepted %s", refused.name)
		}
	}
	// The positive control: the mark is read back as written.
	if got, err := s.Task(ctx, plain.ID); err != nil || got.Housekeeping {
		t.Fatalf("an ordinary investigation reads %+v, %v, want no mark", got, err)
	}
}

// registerRetireHold registers a stand-in for the retirement hold on a task.
func registerRetireHold(t *testing.T, s *Store, task string) string {
	t.Helper()
	key := "task/" + task + "/housekeeping-retire/1"
	if _, err := s.RegisterHold(context.Background(), Hold{Key: key, TaskID: Known(task), Subject: "retire it?", Detail: "d"}); err != nil {
		t.Fatalf("RegisterHold: %v", err)
	}
	return key
}

// promotedPassWithRun promotes a pass of repo-1 onto branch, begins its
// first attempt and creates a run on the branch with the given status.
func promotedPassWithRun(t *testing.T, s *Store, id, branch string, status RunStatus) (TaskAttempt, Run) {
	t.Helper()
	ctx := context.Background()
	mustPass(t, s, id)
	history := historyOf(t, s, id)
	if _, _, err := s.TransitionTaskShape(ctx, id, TaskInvestigation, TaskDelivery, "repo-1", branch,
		config.DeliveryGated, "Correct the drift.", history[len(history)-1].Sequence, "promoted in the test", ResolvedByMachineInterface(), nil); err != nil {
		t.Fatalf("promoting %s: %v", id, err)
	}
	attempt := beginAttempt(t, s, id, 0)
	tick()
	run, err := s.CreateRun(ctx, Run{ID: "run-" + id, RepositoryID: "repo-1", Branch: branch, SubmittedHead: "aaaa", Base: "bbbb",
		Intent: "validate", IntentSource: "task", Build: testBuild(), ConfigDigest: "cfg-1"})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if status != RunPending {
		if run, err = s.TransitionRun(ctx, run.ID, []RunStatus{RunPending}, RunRunning); err != nil {
			t.Fatal(err)
		}
		if status != RunRunning {
			if run, err = s.TransitionRun(ctx, run.ID, []RunStatus{RunRunning}, status); err != nil {
				t.Fatal(err)
			}
		}
	}
	return attempt, run
}

// PRD section 6's retirement is one record, written once in its own
// transaction, naming the hold the answer closed, never rewritten, and a
// second retirement of the pass is refused as closed (P6).
func TestARetirementIsWrittenOnce(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P6)
	ctx := context.Background()
	s := openStore(t)
	seedRepository(t, s)
	_, run := promotedPassWithRun(t, s, "taskhkaaaaca", "housekeeping/one", RunRunning)
	retire := registerRetireHold(t, s, "taskhkaaaaca")
	answer := Retirement{Task: "taskhkaaaaca", Hold: retire, By: ResolvedByMachineInterface(), Delivered: relayed}
	if _, err := s.TransitionRun(ctx, run.ID, []RunStatus{RunRunning}, RunPassed); err != nil {
		t.Fatal(err)
	}
	retired, err := s.RetireHousekeepingPass(ctx, answer)
	if err != nil || retired.Record.Hold != retire || len(retired.Closed) != 1 || retired.Closed[0].Key != retire {
		t.Fatalf("the retirement = %+v, %v, want the record naming hold %s and that hold closed", retired, err, retire)
	}
	if _, err := s.RetireHousekeepingPass(ctx, answer); !errors.Is(err, ErrPassClosed) {
		t.Fatalf("a second retirement = %v, want ErrPassClosed", err)
	}
	if err := rawWrite(ctx, s, `UPDATE housekeeping_retirement SET recorded_at = '2000-01-01T00:00:00Z' WHERE task_id = 'taskhkaaaaca'`); err == nil {
		t.Fatal("the schema let a retirement be rewritten")
	}
	snap, err := s.TaskSnapshot(ctx, "taskhkaaaaca")
	if err != nil || snap.Retirement != Known(retired.Record) {
		t.Fatalf("the snapshot carries %+v, %v, want the record", snap.Retirement, err)
	}
}
