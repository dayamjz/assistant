package store

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

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
		By: ResolvedByMachineInterface(), Relaunches: func(string) bool { return false },
		Ended: func(RunStatus) bool { return true }, Delivered: relayed}); err != nil {
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
		config.DeliveryGated, "Correct the drift.", history[len(history)-1].Sequence, "promoted in the test"); err != nil {
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
// all refuse it with nothing written; on proof, an open hold that offers to
// relaunch the worker refuses it too; and once written it is the one
// record, never rewritten, that every later attempt is refused on (P6).
func TestARetirementIsWrittenOnceAndOnlyWhileNothingMovesThePass(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P6)
	ctx := context.Background()
	s := openStore(t)
	seedRepository(t, s)
	attempt, run := promotedPassWithRun(t, s, "taskhkaaaaca", "housekeeping/one", RunRunning)
	relaunchKey := "task/taskhkaaaaca/stopped/1"
	relaunches := func(key string) bool { return key == relaunchKey }
	onProof := Retirement{Task: "taskhkaaaaca", Attempt: attempt.Sequence, Run: run.ID, OnProof: true,
		Relaunches: relaunches, Ended: ended, Delivered: relayed}

	if _, err := s.RetireHousekeepingPass(ctx, onProof); !errors.Is(err, ErrPassMoves) {
		t.Fatalf("retiring while run %s runs = %v, want ErrPassMoves", run.ID, err)
	}
	if _, err := s.TransitionRun(ctx, run.ID, []RunStatus{RunRunning}, RunPassed); err != nil {
		t.Fatal(err)
	}
	for _, moved := range []Retirement{
		func() Retirement { r := onProof; r.Attempt = 0; return r }(),
		func() Retirement { r := onProof; r.Run = "run-older"; return r }(),
	} {
		if _, err := s.RetireHousekeepingPass(ctx, moved); !errors.Is(err, ErrPassMoves) {
			t.Fatalf("retiring against %+v = %v, want ErrPassMoves", moved, err)
		}
	}
	if _, err := s.RegisterHold(ctx, Hold{Key: relaunchKey, TaskID: Known("taskhkaaaaca"), Subject: "begin again?", Detail: "d"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RetireHousekeepingPass(ctx, onProof); !errors.Is(err, ErrPassMoves) {
		t.Fatalf("retiring on proof with a stop's hold open = %v, want ErrPassMoves", err)
	}
	if _, err := s.ResolveHold(ctx, relaunchKey, "stop", ResolvedByMachineInterface()); err != nil {
		t.Fatal(err)
	}
	retired, err := s.RetireHousekeepingPass(ctx, onProof)
	if err != nil || retired.Record.Run != Known(run.ID) || !retired.Record.OnProof() || len(retired.Closed) != 0 {
		t.Fatalf("the on-proof retirement = %+v, %v, want the record naming run %s and no hold closed", retired, err, run.ID)
	}
	if _, err := s.RetireHousekeepingPass(ctx, onProof); !errors.Is(err, ErrPassClosed) {
		t.Fatalf("a second retirement = %v, want ErrPassClosed", err)
	}
	if err := rawWrite(ctx, s, `UPDATE housekeeping_retirement SET run_id = NULL WHERE task_id = 'taskhkaaaaca'`); err == nil {
		t.Fatal("the schema let a retirement be rewritten")
	}
	snap, err := s.TaskSnapshot(ctx, "taskhkaaaaca")
	if err != nil || snap.Retirement != Known(retired.Record) {
		t.Fatalf("the snapshot carries %+v, %v, want the record", snap.Retirement, err)
	}
	if _, err := s.BeginTaskAttempt(ctx, "taskhkaaaaca", attempt.Sequence); !errors.Is(err, ErrPassRetired) {
		t.Fatalf("an attempt of the retired pass = %v, want ErrPassRetired", err)
	}
	// The positive control: the same attempt of a pass not retired begins.
	other, _ := promotedPassWithRun(t, s, "taskhkaaaacb", "housekeeping/two", RunPassed)
	if _, err := s.BeginTaskAttempt(ctx, "taskhkaaaacb", other.Sequence); err != nil {
		t.Fatalf("an attempt of a pass not retired = %v, want it begun", err)
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
	if err != nil || retired.Record.Hold != Known(retire) || len(retired.Closed) != 3 {
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

// PRD section 8's housekeeping-due wake is the queue's fourth shape: about a
// repository, appended once per marker under the incarnation fence with the
// due-check marker in its own transaction, drained, acknowledged and
// claimed for the private channel on the other shapes' terms, the first
// coordinator's alone, and it holds its repository's records until
// acknowledged.
func TestAHousekeepingWakeIsTheQueuesFourthShape(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P8)
	ctx := context.Background()
	s := openStore(t)
	seedRepository(t, s)
	inc := incarnationOf(t, s)
	request := HousekeepingWakeRequest{Repository: "repo-1", Kind: "housekeeping-due", Marker: "due:none:1", Detail: "no pass was ever created"}
	checked := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	first, err := s.AppendHousekeepingWake(ctx, inc, request, &HousekeepingCheck{Repository: "repo-1", CheckedAt: checked})
	if err != nil || first.Disposition != WakeAppended || first.Wake.Repository != "repo-1" || first.Wake.Pass.IsKnown() || !first.Wake.Queued() {
		t.Fatalf("the first append = %+v, %v", first, err)
	}
	marker, found, err := s.HousekeepingMarkerOf(ctx, "repo-1")
	if err != nil || !found || !marker.CheckedAt.Equal(checked) || marker.Pass != "" {
		t.Fatalf("the marker reads %+v, %v, %v, want it advanced with the wake", marker, found, err)
	}
	again, err := s.AppendHousekeepingWake(ctx, inc, request, nil)
	if err != nil || again.Disposition != WakeAlreadyQueued || again.Wake.ID != first.Wake.ID {
		t.Fatalf("a replay = %+v, %v, want the queued row", again, err)
	}
	stale := inc
	inc = incarnationOf(t, s)
	if _, err := s.AppendHousekeepingWake(ctx, stale, HousekeepingWakeRequest{Repository: "repo-1", Kind: "housekeeping-due",
		Marker: "due:none:2", Detail: "d"}, nil); !errors.Is(err, ErrStaleIncarnation) {
		t.Fatalf("a replaced incarnation's append = %v, want ErrStaleIncarnation", err)
	}
	if err := s.AdvanceHousekeepingMarker(ctx, stale, HousekeepingCheck{Repository: "repo-1", CheckedAt: checked.Add(time.Hour)}); !errors.Is(err, ErrStaleIncarnation) {
		t.Fatalf("a replaced incarnation's marker = %v, want ErrStaleIncarnation", err)
	}

	if n, err := s.OfferedHousekeepingWakes(ctx, nil, WakeRoute{Subordinate: "web"}); err != nil || n != 0 {
		t.Fatalf("a subordinate is offered %d, %v, want none", n, err)
	}
	if n, err := s.UnacknowledgedHousekeepingWakes(ctx); err != nil || n != 1 {
		t.Fatalf("unacknowledged = %d, %v, want 1", n, err)
	}
	if _, err := s.ForgetRepository(ctx, "repo-1"); !errors.Is(err, ErrHousekeepingWakeQueued) {
		t.Fatalf("forgetting the repository with its wake queued = %v, want ErrHousekeepingWakeQueued", err)
	}
	pending, err := s.PendingHousekeepingNotifications(ctx, 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending notifications = %+v, %v", pending, err)
	}
	if _, err := s.ClaimHousekeepingNotification(ctx, first.Wake.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ClaimHousekeepingNotification(ctx, first.Wake.ID); !errors.Is(err, ErrNotificationClaimed) {
		t.Fatalf("a second claim = %v, want ErrNotificationClaimed", err)
	}
	drained, err := s.DrainAll(ctx, 10, WakeHandout{To: AcknowledgedByCoordinator(), Under: "attach-1"})
	if err != nil || len(drained.HousekeepingWakes) != 1 || drained.HousekeepingWakes[0].ID != first.Wake.ID {
		t.Fatalf("the drain = %+v, %v, want the housekeeping row", drained, err)
	}
	if err := s.AcknowledgeAll(ctx, nil, nil, nil, []int64{first.Wake.ID}, AcknowledgedByCoordinator()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendHousekeepingWake(ctx, inc, request, nil); !errors.Is(err, ErrWakeHandled) {
		t.Fatalf("the marker raised again once acknowledged = %v, want ErrWakeHandled", err)
	}
	if removed, err := s.ForgetRepository(ctx, "repo-1"); err != nil || removed != 0 {
		t.Fatalf("forgetting the repository once its wake is acknowledged = %d, %v", removed, err)
	}
	if rows, err := s.HousekeepingWakes(ctx); err != nil || len(rows) != 0 {
		t.Fatalf("the repository's wakes outlived it: %+v, %v", rows, err)
	}
}

// RegisterHoldUnder, the loop's registration of a retirement hold, is fenced
// by the incarnation and anchored to the attempt it was decided against: a
// replaced incarnation and a task begun again since each register nothing,
// and the current incarnation at the latest attempt registers the hold.
func TestALoopRegisteredHoldIsFencedAndAnchored(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	seedRepository(t, s)
	mustPass(t, s, "taskhkaaaaea")
	stale := incarnationOf(t, s)
	inc := incarnationOf(t, s)
	h := Hold{Key: "task/taskhkaaaaea/housekeeping-retire/1/0/-", TaskID: Known("taskhkaaaaea"), Subject: "retire?", Detail: "d"}
	if _, err := s.RegisterHoldUnder(ctx, stale, 0, h); !errors.Is(err, ErrStaleIncarnation) {
		t.Fatalf("a replaced incarnation's registration = %v, want ErrStaleIncarnation", err)
	}
	beginAttempt(t, s, "taskhkaaaaea", 0)
	if _, err := s.RegisterHoldUnder(ctx, inc, 0, h); !errors.Is(err, ErrTaskMoved) {
		t.Fatalf("a registration decided before the pass was begun = %v, want ErrTaskMoved", err)
	}
	if _, err := s.Hold(ctx, h.Key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a refused registration wrote the hold: %v", err)
	}
	if got, err := s.RegisterHoldUnder(ctx, inc, 1, h); err != nil || !got.Open() {
		t.Fatalf("the current registration = %+v, %v, want the hold open", got, err)
	}
}

// CheckRetirement answers what RetireHousekeepingPass would refuse, writing
// nothing: a newer run than the one decided against moves the pass, and the
// same request against the newest run is answered nil without a record.
func TestCheckRetirementAnswersTheRefusalAndWritesNothing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	seedRepository(t, s)
	attempt, run := promotedPassWithRun(t, s, "taskhkaaaafa", "housekeeping/one", RunPassed)
	r := Retirement{Task: "taskhkaaaafa", Attempt: attempt.Sequence, Run: run.ID, OnProof: true,
		Relaunches: func(string) bool { return false }, Ended: ended, Delivered: relayed}
	if err := s.CheckRetirement(ctx, r); err != nil {
		t.Fatalf("checking a current proof = %v, want nil", err)
	}
	stale := r
	stale.Run = "run-older"
	if err := s.CheckRetirement(ctx, stale); !errors.Is(err, ErrPassMoves) {
		t.Fatalf("checking a stale proof = %v, want ErrPassMoves", err)
	}
	if snap, err := s.TaskSnapshot(ctx, "taskhkaaaafa"); err != nil || snap.Retirement.IsKnown() {
		t.Fatalf("a check wrote %+v (%v)", snap.Retirement, err)
	}
}

// CheckRetirement's answer is RetireHousekeepingPass's for an answered
// retirement too: a retirement hold already answered keep is refused by
// both, with ErrHoldResolved, before anything is written.
func TestCheckRetirementRefusesAnAnsweredHold(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	seedRepository(t, s)
	attempt, run := promotedPassWithRun(t, s, "taskhkaaaaga", "housekeeping/one", RunTerminated)
	hold := registerRetireHold(t, s, "taskhkaaaaga")
	r := Retirement{Task: "taskhkaaaaga", Attempt: attempt.Sequence, Run: run.ID, Hold: hold, By: ResolvedByMachineInterface(),
		Relaunches: func(string) bool { return false }, Ended: ended, Delivered: relayed}
	if err := s.CheckRetirement(ctx, r); err != nil {
		t.Fatalf("checking an open hold's retirement = %v, want nil", err)
	}
	if _, err := s.ResolveHold(ctx, hold, "keep", ResolvedByMachineInterface()); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckRetirement(ctx, r); !errors.Is(err, ErrHoldResolved) {
		t.Fatalf("checking a retirement whose hold was answered keep = %v, want ErrHoldResolved", err)
	}
	if _, err := s.RetireHousekeepingPass(ctx, r); !errors.Is(err, ErrHoldResolved) {
		t.Fatalf("the retirement itself = %v, want ErrHoldResolved", err)
	}
}

// A retired pass is offered no retirement hold, and a pass with one open is
// not retired on proof, whichever of the two the store sees first: the
// program never answers a hold that waits on a person, and a hold on a pass
// whose claim is released could only be refused.
func TestARetirementHoldAndAnOnProofRetirementExcludeEachOther(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	isRetireHold := func(key string) bool { return strings.Contains(key, "/housekeeping-retire/") }
	for _, holdFirst := range []bool{true, false} {
		s := openStore(t)
		seedRepository(t, s)
		attempt, run := promotedPassWithRun(t, s, "taskhkaaaaha", "housekeeping/one", RunPassed)
		inc := incarnationOf(t, s)
		h := Hold{Key: "task/taskhkaaaaha/housekeeping-retire/1/1/" + run.ID, TaskID: Known("taskhkaaaaha"),
			Subject: "retire?", Detail: "d"}
		onProof := Retirement{Task: "taskhkaaaaha", Attempt: attempt.Sequence, Run: run.ID, OnProof: true,
			Relaunches: isRetireHold, Ended: ended, Delivered: relayed}
		if holdFirst {
			if _, err := s.RegisterHoldUnder(ctx, inc, attempt.Sequence, h); err != nil {
				t.Fatal(err)
			}
			if _, err := s.RetireHousekeepingPass(ctx, onProof); !errors.Is(err, ErrPassMoves) {
				t.Fatalf("an on-proof retirement with the retirement hold open = %v, want ErrPassMoves", err)
			}
			continue
		}
		if _, err := s.RetireHousekeepingPass(ctx, onProof); err != nil {
			t.Fatal(err)
		}
		if _, err := s.RegisterHoldUnder(ctx, inc, attempt.Sequence, h); !errors.Is(err, ErrPassRetired) {
			t.Fatalf("a retirement hold for a retired pass = %v, want ErrPassRetired", err)
		}
	}
}
