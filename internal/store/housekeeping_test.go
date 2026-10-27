package store

import (
	"context"
	"errors"
	"strings"
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
		By: ResolvedByMachineInterface(), Relaunches: func(string) bool { return false }, Ended: func(RunStatus) bool { return true }, Delivered: relayed}); err != nil {
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

func ended(st RunStatus) bool { return st == RunPassed || st == RunFailed || st == RunTerminated }

// PRD section 6's retirement is written only while nothing moves the pass,
// decided in its own transaction: a run that has not ended, a run newer
// than the one the retirement was decided against, and a relaunch since
// all refuse it with nothing written; and once written it is the one
// record, naming the hold the answer closed, never rewritten (P6).
func TestARetirementIsWrittenOnceAndOnlyWhileNothingMovesThePass(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P6)
	ctx := context.Background()
	s := openStore(t)
	seedRepository(t, s)
	attempt, run := promotedPassWithRun(t, s, "taskhkaaaaca", "housekeeping/one", RunRunning)
	retire := registerRetireHold(t, s, "taskhkaaaaca")
	answer := Retirement{Task: "taskhkaaaaca", Attempt: attempt.Sequence, Run: run.ID, Hold: retire, By: ResolvedByMachineInterface(), Relaunches: func(string) bool { return false },
		Ended: ended, Delivered: relayed}

	if _, err := s.RetireHousekeepingPass(ctx, answer); !errors.Is(err, ErrPassMoves) {
		t.Fatalf("retiring while run %s runs = %v, want ErrPassMoves", run.ID, err)
	}
	if _, err := s.TransitionRun(ctx, run.ID, []RunStatus{RunRunning}, RunPassed); err != nil {
		t.Fatal(err)
	}
	for _, moved := range []Retirement{
		func() Retirement { r := answer; r.Attempt = 0; return r }(),
		func() Retirement { r := answer; r.Run = "run-older"; return r }(),
	} {
		if _, err := s.RetireHousekeepingPass(ctx, moved); !errors.Is(err, ErrPassMoves) {
			t.Fatalf("retiring against %+v = %v, want ErrPassMoves", moved, err)
		}
	}
	if h, err := s.Hold(ctx, retire); err != nil || !h.Open() {
		t.Fatalf("a refused retirement left the hold %+v, %v; want it open", h, err)
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

// An answered retirement closes its hold retire and every other open hold
// that would relaunch the worker stop, under the one resolver, and leaves a
// hold that does not relaunch it open; a retirement hold already answered
// is refused with nothing written.
func TestAnAnsweredRetirementAnswersStopOnTheHoldsThatWouldRelaunch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	seedRepository(t, s)
	attempt, run := promotedPassWithRun(t, s, "taskhkaaaada", "housekeeping/one", RunTerminated)
	retire := registerRetireHold(t, s, "taskhkaaaada")
	for _, key := range []string{"task/taskhkaaaada/stopped/1", "task/taskhkaaaada/relaunch-bound/1", "task/taskhkaaaada/finding/1"} {
		if _, err := s.RegisterHold(ctx, Hold{Key: key, TaskID: Known("taskhkaaaada"), Subject: "s", Detail: "d"}); err != nil {
			t.Fatal(err)
		}
	}
	relaunches := func(key string) bool {
		return key == "task/taskhkaaaada/stopped/1" || key == "task/taskhkaaaada/relaunch-bound/1"
	}
	answer := Retirement{Task: "taskhkaaaada", Attempt: attempt.Sequence, Run: run.ID, Hold: retire, Note: "retire it",
		By: ResolvedByMachineInterface(), Relaunches: relaunches, Ended: ended, Delivered: relayed}
	retired, err := s.RetireHousekeepingPass(ctx, answer)
	if err != nil || retired.Record.Hold != retire || len(retired.Closed) != 3 {
		t.Fatalf("the answered retirement = %+v, %v, want the record naming %s and three holds closed", retired, err, retire)
	}
	if retired.Closed[0].Key != retire || retired.Closed[0].Resolution != Known(AnswerRetire) {
		t.Fatalf("the retirement hold was closed %+v, want %q", retired.Closed[0], AnswerRetire)
	}
	for _, h := range retired.Closed[1:] {
		if h.Resolution != Known(AnswerStop) || h.ResolvedBy != retired.Closed[0].ResolvedBy {
			t.Fatalf("hold %s was closed %v by %v, want %q by the retire answer's resolver", h.Key, h.Resolution, h.ResolvedBy, AnswerStop)
		}
	}
	if finding, err := s.Hold(ctx, "task/taskhkaaaada/finding/1"); err != nil || !finding.Open() {
		t.Fatalf("the report's own hold reads %+v, %v; want it open, since it relaunches nothing", finding, err)
	}

	attempt, run = promotedPassWithRun(t, s, "taskhkaaaadb", "housekeeping/two", RunFailed)
	stale := registerRetireHold(t, s, "taskhkaaaadb")
	if _, err := s.ResolveHold(ctx, stale, "keep", ResolvedByMachineInterface()); err != nil {
		t.Fatal(err)
	}
	answer = Retirement{Task: "taskhkaaaadb", Attempt: attempt.Sequence, Run: run.ID, Hold: stale, By: ResolvedByMachineInterface(),
		Relaunches: relaunches, Ended: ended, Delivered: relayed}
	if _, err := s.RetireHousekeepingPass(ctx, answer); !errors.Is(err, ErrHoldResolved) {
		t.Fatalf("a retire on a hold answered keep = %v, want ErrHoldResolved", err)
	}
	if snap, err := s.TaskSnapshot(ctx, "taskhkaaaadb"); err != nil || snap.Retirement.IsKnown() {
		t.Fatalf("the refused retirement wrote %+v, %v", snap.Retirement, err)
	}

	// An answer decided against the newest run while that run has not
	// ended is refused, whatever its anchors say.
	// A store of its own, since the pass above is still open.
	s = openStore(t)
	seedRepository(t, s)
	attempt, run = promotedPassWithRun(t, s, "taskhkaaaadc", "housekeeping/three", RunRunning)
	unended := registerRetireHold(t, s, "taskhkaaaadc")
	answer = Retirement{Task: "taskhkaaaadc", Attempt: attempt.Sequence, Run: run.ID, Hold: unended, By: ResolvedByMachineInterface(),
		Relaunches: relaunches, Ended: ended, Delivered: relayed}
	if _, err := s.RetireHousekeepingPass(ctx, answer); !errors.Is(err, ErrPassMoves) {
		t.Fatalf("a retire while run %s runs = %v, want ErrPassMoves", run.ID, err)
	}
	// A newer run that started and ended since the decision moved the pass
	// too, though it has ended: the anchor names the run decided against.
	if _, err := s.TransitionRun(ctx, run.ID, []RunStatus{RunRunning}, RunFailed); err != nil {
		t.Fatal(err)
	}
	tick()
	if _, err := s.CreateRun(ctx, Run{ID: "run-newer", RepositoryID: "repo-1", Branch: "housekeeping/three", SubmittedHead: "aaaa",
		Base: "bbbb", Intent: "validate", IntentSource: "task", Build: testBuild(), ConfigDigest: "cfg-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TransitionRun(ctx, "run-newer", []RunStatus{RunPending}, RunTerminated); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RetireHousekeepingPass(ctx, answer); !errors.Is(err, ErrPassMoves) || !strings.Contains(err.Error(), "run-newer") {
		t.Fatalf("a retire decided before run-newer = %v, want ErrPassMoves naming it", err)
	}
}
