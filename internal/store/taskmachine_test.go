package store

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/principles"
)

// seedTaskOnBranch creates a delivery task of repo-1 on branch, anchored to
// observed, the highest Row of that branch's tasks the caller saw.
func seedTaskOnBranch(t *testing.T, s *Store, id, branch string, observed int64) Task {
	t.Helper()
	task, err := s.CreateTask(context.Background(), Task{
		ID: id, Shape: TaskDelivery, Project: "one", Mode: "interactive", WorktreePath: "/tasks/" + id + "/copy",
		RepositoryID: Known("repo-1"), Branch: Known(branch),
	}, Disposition{Observed: observed})
	if err != nil {
		t.Fatalf("CreateTask %s: %v", id, err)
	}
	return task
}

// machineOf reads a task's machine back through the accessor.
func machineOf(t *testing.T, s *Store, id string) string {
	t.Helper()
	task, err := s.Task(context.Background(), id)
	if err != nil {
		t.Fatalf("Task(%s): %v", id, err)
	}
	return task.Machine
}

// attemptsOf reads a task's attempts back through the accessor.
func attemptsOf(t *testing.T, s *Store, id string) []TaskAttempt {
	t.Helper()
	attempts, err := s.TaskAttempts(context.Background(), id)
	if err != nil {
		t.Fatalf("TaskAttempts(%s): %v", id, err)
	}
	return attempts
}

// A task's machine is written once, at dispatch, and kept by relaunch and
// promotion. PRD principle P14 is what the test claims, through PRD section
// 8's task record: "the machine it runs on, written once at dispatch and kept
// by promotion and relaunch, empty for this machine ... the one place a
// task's machine is stored". Intake cannot state one, a dispatch that names
// nowhere or a name the record would not hold is refused with nothing
// written, a second dispatch is refused by its anchor, and every later write
// is refused by the accessors and, directly, by the schema. The positive
// control is the same direct write on a task not yet dispatched, which the
// schema admits, so the refusals after dispatch are the seal and not a
// statement that could never succeed.
func TestATasksMachineIsWrittenOnceAtDispatch(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P14)
	ctx := context.Background()
	s := openStore(t)

	t.Run("intake states no machine", func(t *testing.T) {
		_, err := s.CreateTask(ctx, Task{ID: "intakemachin", Shape: TaskDelivery, Project: "one", Mode: "interactive",
			WorktreePath: "/tasks/intakemachin/copy", Machine: "mini"}, Disposition{})
		if !errors.Is(err, ErrMachineAtIntake) {
			t.Fatalf("CreateTask with a machine = %v, want ErrMachineAtIntake", err)
		}
		err = rawWrite(ctx, s, `INSERT INTO task (id, shape, project, mode, worktree_path, created_at, machine)
			VALUES ('intakeraw000', 'delivery', 'one', 'interactive', '/tasks/intakeraw000/copy', ?, 'mini')`, encodeTime(nowUTC()))
		if err == nil || !strings.Contains(err.Error(), "never at intake") {
			t.Fatalf("a direct insert with a machine = %v, want the schema's refusal", err)
		}
		if _, err := s.Task(ctx, "intakeraw000"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("the refused insert left a task: %v", err)
		}
	})

	task := seedTask(t, s, "remoteaaaaaa")
	if task.Machine != "" {
		t.Fatalf("an undispatched task reads machine %q", task.Machine)
	}

	t.Run("a dispatch naming nowhere or an unholdable name records nothing", func(t *testing.T) {
		for name, at := range map[string]Placement{
			"the zero placement":       {},
			"an empty host":            OnRemoteHost(""),
			"a host with a space":      OnRemoteHost("mini two"),
			"a host with a newline":    OnRemoteHost("mini\n"),
			"a control character":      OnRemoteHost("mini\x01"),
			"bytes that are not UTF-8": OnRemoteHost("mini\xff"),
		} {
			if _, err := s.BeginFirstTaskAttempt(ctx, task.ID, at); err == nil {
				t.Errorf("%s: dispatched", name)
			}
		}
		if got := attemptsOf(t, s, task.ID); len(got) != 0 {
			t.Fatalf("a refused dispatch left attempts %+v", got)
		}
		if got := machineOf(t, s, task.ID); got != "" {
			t.Fatalf("a refused dispatch left machine %q", got)
		}
	})

	t.Run("the positive control: the schema admits the column before any attempt", func(t *testing.T) {
		control := seedTask(t, s, "controlaaaaa")
		if err := rawWrite(ctx, s, `UPDATE task SET machine = 'mini' WHERE id = ?`, control.ID); err != nil {
			t.Fatalf("a direct write before dispatch was refused: %v", err)
		}
		if got := machineOf(t, s, control.ID); got != "mini" {
			t.Fatalf("the direct write reads back %q", got)
		}
	})

	first, err := s.BeginFirstTaskAttempt(ctx, task.ID, OnRemoteHost("mini"))
	if err != nil || first.Sequence != 1 {
		t.Fatalf("BeginFirstTaskAttempt = %+v, %v; want attempt 1", first, err)
	}
	if got := machineOf(t, s, task.ID); got != "mini" {
		t.Fatalf("the dispatched task reads machine %q, want mini", got)
	}

	t.Run("a second dispatch is refused by its anchor and moves nothing", func(t *testing.T) {
		_, err := s.BeginFirstTaskAttempt(ctx, task.ID, OnRemoteHost("other"))
		var moved *TaskMovedError
		if !errors.As(err, &moved) || moved.Stands != 1 || moved.Expected != 0 {
			t.Fatalf("a second dispatch = %v, want a *TaskMovedError standing at 1", err)
		}
		if _, err := s.BeginFirstTaskAttempt(ctx, task.ID, OnThisMachine()); !errors.Is(err, ErrTaskMoved) {
			t.Fatalf("a second dispatch onto this machine = %v, want ErrTaskMoved", err)
		}
		if _, err := s.BeginTaskAttempt(ctx, task.ID, 0); !errors.Is(err, ErrFirstAttemptIsDispatch) {
			t.Fatalf("BeginTaskAttempt anchored to no attempt = %v, want ErrFirstAttemptIsDispatch", err)
		}
		if got := machineOf(t, s, task.ID); got != "mini" {
			t.Fatalf("after the refused dispatches the machine reads %q", got)
		}
	})

	t.Run("a relaunch keeps it", func(t *testing.T) {
		second, err := s.BeginTaskAttempt(ctx, task.ID, first.Sequence)
		if err != nil || second.Sequence != 2 {
			t.Fatalf("BeginTaskAttempt = %+v, %v; want attempt 2", second, err)
		}
		if got := machineOf(t, s, task.ID); got != "mini" {
			t.Fatalf("after the relaunch the machine reads %q", got)
		}
	})

	t.Run("the schema refuses every later write of it", func(t *testing.T) {
		for _, value := range []any{"other", nil, ""} {
			err := rawWrite(ctx, s, `UPDATE task SET machine = ? WHERE id = ?`, value, task.ID)
			if err == nil || !strings.Contains(err.Error(), "written once") {
				t.Errorf("a direct rewrite to %v = %v, want the schema's refusal", value, err)
			}
		}
		local := seedTask(t, s, "localaaaaaaa")
		if _, err := s.BeginFirstTaskAttempt(ctx, local.ID, OnThisMachine()); err != nil {
			t.Fatalf("BeginFirstTaskAttempt on this machine: %v", err)
		}
		if got := machineOf(t, s, local.ID); got != "" {
			t.Fatalf("a task dispatched here reads machine %q", got)
		}
		err := rawWrite(ctx, s, `UPDATE task SET machine = 'mini' WHERE id = ?`, local.ID)
		if err == nil || !strings.Contains(err.Error(), "written once") {
			t.Fatalf("placing a task dispatched here somewhere else = %v, want the schema's refusal", err)
		}
		if got := machineOf(t, s, task.ID); got != "mini" {
			t.Fatalf("after the refused rewrites the machine reads %q", got)
		}
	})

	t.Run("a promotion keeps it", func(t *testing.T) {
		inv := seedInvestigation(t, s, "investigatea")
		if _, err := s.BeginFirstTaskAttempt(ctx, inv.ID, OnRemoteHost("mini")); err != nil {
			t.Fatalf("BeginFirstTaskAttempt: %v", err)
		}
		promoted, _, err := s.TransitionTaskShape(ctx, inv.ID, TaskInvestigation, TaskDelivery, "repo-1", "topic",
			config.DeliveryGated, "Deliver it.", 2, "promoted", ResolvedByMachineInterface())
		if err != nil {
			t.Fatalf("TransitionTaskShape: %v", err)
		}
		if promoted.Shape != TaskDelivery || promoted.Machine != "mini" {
			t.Fatalf("the promoted task reads %+v, want a delivery task still on mini", promoted)
		}
		if got := machineOf(t, s, inv.ID); got != "mini" {
			t.Fatalf("after the promotion the machine reads %q", got)
		}
	})
}

// Two dispatchers that both found no attempt record one dispatch between
// them, and the machine is the winner's, never the loser's and never a mix:
// the anchor and the write are one transaction. The race detector is what
// makes the concurrency claim checkable.
func TestRacingDispatchesRecordOneMachine(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := openStore(t)
	task := seedTask(t, s, "racedaaaaaaa")

	hosts := []string{"mini", "macbook", "studio", "air"}
	won := make([]bool, len(hosts))
	errs := make([]error, len(hosts))
	var wg sync.WaitGroup
	for i, host := range hosts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.BeginFirstTaskAttempt(ctx, task.ID, OnRemoteHost(host))
			won[i], errs[i] = err == nil, err
		}()
	}
	wg.Wait()

	winner := ""
	for i, host := range hosts {
		switch {
		case won[i] && winner != "":
			t.Fatalf("%s and %s both dispatched the task", winner, host)
		case won[i]:
			winner = host
		case !errors.Is(errs[i], ErrTaskMoved):
			t.Fatalf("the loser on %s = %v, want ErrTaskMoved", host, errs[i])
		}
	}
	if winner == "" {
		t.Fatal("no dispatcher won")
	}
	if got := machineOf(t, s, task.ID); got != winner {
		t.Fatalf("the task reads machine %q, and %s won the dispatch", got, winner)
	}
	if got := attemptsOf(t, s, task.ID); len(got) != 1 {
		t.Fatalf("the race left %d attempts", len(got))
	}
}

// A remote worker's session is recorded with the herdr server hosting it,
// PRD section 8's "written with the session reference, the identity of the
// herdr server hosting that worker", in one write and once; a session of a
// task here carries none. The session reference itself names no machine:
// the attempt reaches its machine through its task. Each refusal leaves the
// attempt as it was, and the schema holds the same rules against a direct
// write; its positive control is the one write it admits.
func TestARemoteSessionIsRecordedWithItsHerdrServer(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P14)
	ctx := context.Background()
	s := openStore(t)
	remote := seedTask(t, s, "remotesessio")
	if _, err := s.BeginFirstTaskAttempt(ctx, remote.ID, OnRemoteHost("mini")); err != nil {
		t.Fatal(err)
	}
	local := seedTask(t, s, "localsession")
	if _, err := s.BeginFirstTaskAttempt(ctx, local.ID, OnThisMachine()); err != nil {
		t.Fatal(err)
	}
	server := SessionHost{PID: 4242, Start: 1_700_000_000}
	sessionless := func(t *testing.T, id string) {
		t.Helper()
		got := attemptsOf(t, s, id)
		if last := got[len(got)-1]; last.SessionRef.IsKnown() || last.Host.Known() {
			t.Fatalf("a refused session left %+v", last)
		}
	}

	t.Run("a remote session without its server, or a local one with one, records nothing", func(t *testing.T) {
		if err := s.SetAttemptSession(ctx, remote.ID, 1, "herdr:remotesessio", SessionHost{}); !errors.Is(err, ErrRemoteSessionWithoutHost) {
			t.Fatalf("a remote session with no host = %v, want ErrRemoteSessionWithoutHost", err)
		}
		sessionless(t, remote.ID)
		if err := s.SetAttemptSession(ctx, local.ID, 1, "pane-local", server); !errors.Is(err, ErrSessionHostOnThisMachine) {
			t.Fatalf("a local session with a host = %v, want ErrSessionHostOnThisMachine", err)
		}
		sessionless(t, local.ID)
		if err := s.SetAttemptSession(ctx, remote.ID, 1, "herdr:remotesessio", SessionHost{PID: 1, Start: math.MaxInt64 + 1}); err == nil {
			t.Fatal("a start time the record cannot hold was accepted")
		}
		sessionless(t, remote.ID)
	})

	t.Run("the schema refuses the same shapes written directly", func(t *testing.T) {
		for name, write := range map[string]string{
			"a remote session with no host": `UPDATE task_attempt SET session_ref = 'x' WHERE task_id = 'remotesessio'`,
			"a local session with a host":   `UPDATE task_attempt SET session_ref = 'x', host_pid = 1, host_start = 1 WHERE task_id = 'localsession'`,
			"a host with no session":        `UPDATE task_attempt SET host_pid = 1, host_start = 1 WHERE task_id = 'remotesessio'`,
			"half a host":                   `UPDATE task_attempt SET session_ref = 'x', host_pid = 1 WHERE task_id = 'remotesessio'`,
		} {
			if err := rawWrite(ctx, s, write); err == nil {
				t.Errorf("%s: written", name)
			}
		}
		sessionless(t, remote.ID)
		sessionless(t, local.ID)
	})

	if err := s.SetAttemptSession(ctx, remote.ID, 1, "herdr:remotesessio", server); err != nil {
		t.Fatalf("SetAttemptSession with its server: %v", err)
	}
	if err := s.SetAttemptSession(ctx, local.ID, 1, "pane-local", SessionHost{}); err != nil {
		t.Fatalf("SetAttemptSession here: %v", err)
	}
	if got := attemptsOf(t, s, remote.ID)[0]; got.SessionRef.Or("") != "herdr:remotesessio" || got.Host != server {
		t.Fatalf("the remote attempt reads %+v, want its session and server", got)
	}
	if got := attemptsOf(t, s, local.ID)[0]; got.SessionRef.Or("") != "pane-local" || got.Host.Known() {
		t.Fatalf("the local attempt reads %+v, want its session and no host", got)
	}

	t.Run("both are recorded once", func(t *testing.T) {
		if err := s.SetAttemptSession(ctx, remote.ID, 1, "herdr:again", SessionHost{PID: 7, Start: 7}); !errors.Is(err, ErrAttemptSessionRecorded) {
			t.Fatalf("a second session = %v, want ErrAttemptSessionRecorded", err)
		}
		for _, write := range []string{
			`UPDATE task_attempt SET host_pid = 7, host_start = 7 WHERE task_id = 'remotesessio'`,
			`UPDATE task_attempt SET session_ref = 'other' WHERE task_id = 'remotesessio'`,
			`UPDATE task_attempt SET session_ref = 'other' WHERE task_id = 'localsession'`,
		} {
			if err := rawWrite(ctx, s, write); err == nil {
				t.Errorf("%s: written", write)
			}
		}
		if got := attemptsOf(t, s, remote.ID)[0]; got.SessionRef.Or("") != "herdr:remotesessio" || got.Host != server {
			t.Fatalf("after the refused rewrites the remote attempt reads %+v", got)
		}
	})

	t.Run("the positive control: the schema admits a remote session written whole", func(t *testing.T) {
		if _, err := s.BeginTaskAttempt(ctx, remote.ID, 1); err != nil {
			t.Fatal(err)
		}
		if err := rawWrite(ctx, s, `UPDATE task_attempt SET session_ref = 'herdr:two', host_pid = 99, host_start = 5
			WHERE task_id = 'remotesessio' AND sequence = 2`); err != nil {
			t.Fatalf("the one shape the schema admits was refused: %v", err)
		}
		if got := attemptsOf(t, s, remote.ID)[1]; got.Host != (SessionHost{PID: 99, Start: 5}) {
			t.Fatalf("the relaunch's attempt reads %+v, want its own server", got)
		}
	})
}

// A kept work entry's machine is its owner's, read off the owner task's row
// on every read and stored on no kept work row, PRD section 8's "a kept-work
// row names the owner it outlived, and its machine is that owner's". A run's
// copy is this machine's.
func TestKeptWorkReadsItsMachineOffItsOwner(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P14)
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)
	task := seedTaskOnBranch(t, s, "keptremoteaa", "topic", 0)
	if _, err := s.BeginFirstTaskAttempt(ctx, task.ID, OnRemoteHost("mini")); err != nil {
		t.Fatal(err)
	}
	ofTask, _, err := s.RecordKeptWork(ctx, KeptCopy{TaskID: task.ID, CopyPath: task.WorktreePath,
		Holds: []string{"aaaa"}, Reason: "unproven", Against: "dddd"})
	if err != nil {
		t.Fatalf("RecordKeptWork: %v", err)
	}
	ofRun, _, err := s.RecordKeptWork(ctx, KeptCopy{RunID: run.ID, CopyPath: "/home/worktrees/repo-1/run-1",
		Holds: []string{"bbbb"}, Reason: "unproven", Against: "dddd"})
	if err != nil {
		t.Fatalf("RecordKeptWork: %v", err)
	}
	if ofTask.Machine != "mini" || ofRun.Machine != "" {
		t.Fatalf("the written entries read machines %q and %q, want mini and this machine", ofTask.Machine, ofRun.Machine)
	}
	byID, err := s.KeptWorkEntry(ctx, ofTask.ID)
	if err != nil || byID.Machine != "mini" {
		t.Fatalf("KeptWorkEntry = %+v, %v; want mini", byID, err)
	}
	byCopy, err := s.KeptWorkOf(ctx, task.WorktreePath, []string{"aaaa"})
	if err != nil || byCopy.Machine != "mini" {
		t.Fatalf("KeptWorkOf = %+v, %v; want mini", byCopy, err)
	}
	open, err := s.OpenKeptWork(ctx)
	if err != nil || len(open) != 2 || open[0].Machine != "mini" || open[1].Machine != "" {
		t.Fatalf("OpenKeptWork = %+v, %v; want the task's entry on mini and the run's here", open, err)
	}
	if answered := discardKept(t, s, ofTask.ID); answered.Machine != "mini" {
		t.Fatalf("the answered entry = %+v; want mini", answered)
	}
}

// Carry on gives the new task the kept copy's machine rather than placing
// it, PRD section 8: the dispatch names the entry and never a machine, and
// the machine is read off the owner the entry outlived. An entry the task
// cannot be carrying on is refused with nothing written: one not answered
// carry on, the task's own, one of another branch, or one another task
// carried on. A valid placement is still held while the project has an open
// entry, on keptworkhold.go's terms. The finished task keeps its machine.
func TestCarryOnGivesTheNewTaskTheKeptCopysMachine(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P14)
	ctx := context.Background()
	s := openStore(t)
	run := seedRun(t, s)
	finished := seedTaskOnBranch(t, s, "finishedaaaa", "topic", 0)
	if _, err := s.BeginFirstTaskAttempt(ctx, finished.ID, OnRemoteHost("mini")); err != nil {
		t.Fatal(err)
	}
	kept := func(holds string) KeptWork {
		t.Helper()
		k, _, err := s.RecordKeptWork(ctx, KeptCopy{TaskID: finished.ID, CopyPath: finished.WorktreePath,
			Holds: []string{holds}, Reason: "unproven", Against: "dddd"})
		if err != nil {
			t.Fatalf("RecordKeptWork: %v", err)
		}
		return k
	}
	landed := kept("cccc")
	landKept(t, s, landed.ID)
	newTask := seedTaskOnBranch(t, s, "carriedonaaa", "topic", finished.Row)
	carried := carryOnKept(t, s, kept("eeee").ID, newTask.ID)
	// Kept after the carry on, so its mark left it open.
	open := kept("aaaa")
	if !open.Open() {
		t.Fatalf("the entry kept after the carry on reads %+v, want it open", open)
	}
	otherBranch := seedTaskOnBranch(t, s, "otherbrancha", "elsewhere", 0)
	secondCarrier := seedTaskOnBranch(t, s, "secondcarrie", "topic", newTask.Row)

	t.Run("an entry the task is not carrying on records nothing", func(t *testing.T) {
		for name, try := range map[string]struct {
			task  string
			entry int64
			want  error
		}{
			"an open entry":                    {newTask.ID, open.ID, ErrNotCarriedOn},
			"an entry answered land":           {newTask.ID, landed.ID, ErrNotCarriedOn},
			"another branch's entry":           {otherBranch.ID, carried.ID, ErrNotCarriedOn},
			"an entry another task carried on": {secondCarrier.ID, carried.ID, ErrNotCarriedOn},
			"an entry that is not there":       {newTask.ID, carried.ID + 100, ErrNotFound},
		} {
			if _, err := s.BeginFirstTaskAttempt(ctx, try.task, CarriedOnFrom(try.entry)); !errors.Is(err, try.want) {
				t.Errorf("%s = %v, want %v", name, err, try.want)
			}
			if got := attemptsOf(t, s, try.task); len(got) != 0 || machineOf(t, s, try.task) != "" {
				t.Errorf("%s: left attempts %+v and machine %q", name, got, machineOf(t, s, try.task))
			}
		}
		own := seedTaskOnBranch(t, s, "ownentryaaaa", "own", 0)
		ownEntry, _, err := s.RecordKeptWork(ctx, KeptCopy{TaskID: own.ID, CopyPath: own.WorktreePath,
			Holds: []string{"ffff"}, Reason: "unproven", Against: "dddd"})
		if err != nil {
			t.Fatal(err)
		}
		carryOnKept(t, s, ownEntry.ID, own.ID)
		if _, err := s.BeginFirstTaskAttempt(ctx, own.ID, CarriedOnFrom(ownEntry.ID)); !errors.Is(err, ErrNotCarriedOn) {
			t.Errorf("the task's own entry = %v, want ErrNotCarriedOn", err)
		}
	})

	t.Run("an entry that names no project is nobody's to carry on", func(t *testing.T) {
		// Two investigations naming no repository, of different projects:
		// their unknown repositories and branches are no evidence that they
		// share either, so the second may not take the first's machine.
		investigation := func(id, project string) Task {
			t.Helper()
			task, err := s.CreateTask(ctx, Task{ID: id, Shape: TaskInvestigation, Project: project, Mode: "interactive",
				WorktreePath: "/tasks/" + id + "/copy"}, Disposition{})
			if err != nil {
				t.Fatalf("CreateTask %s: %v", id, err)
			}
			return task
		}
		first := investigation("projectoneaa", "project-one")
		if _, err := s.BeginFirstTaskAttempt(ctx, first.ID, OnRemoteHost("studio")); err != nil {
			t.Fatal(err)
		}
		unnamed, _, err := s.RecordKeptWork(ctx, KeptCopy{TaskID: first.ID, CopyPath: first.WorktreePath,
			Holds: []string{"abab"}, Reason: "unproven", Against: "dddd"})
		if err != nil {
			t.Fatal(err)
		}
		if _, known := unnamed.RepositoryID.Get(); known {
			t.Fatalf("the entry reads project %q, want none: the case below would test nothing", unnamed.RepositoryID.Or(""))
		}
		// The carry on names the investigation of its project, so for that
		// one only the missing project can refuse.
		ofItsProject := investigation("projectonebb", "project-one")
		carryOnKept(t, s, unnamed.ID, ofItsProject.ID)
		for name, task := range map[string]Task{
			"another project's investigation": investigation("projecttwoaa", "project-two"),
			"an investigation of its project": ofItsProject,
			"a task naming a repository":      seedTaskOnBranch(t, s, "namedrepoaaa", "named", 0),
		} {
			if _, err := s.BeginFirstTaskAttempt(ctx, task.ID, CarriedOnFrom(unnamed.ID)); !errors.Is(err, ErrNotCarriedOn) {
				t.Errorf("%s carrying on an entry of no project = %v, want ErrNotCarriedOn", name, err)
			}
			if got := attemptsOf(t, s, task.ID); len(got) != 0 || machineOf(t, s, task.ID) != "" {
				t.Errorf("%s: left attempts %+v and machine %q", name, got, machineOf(t, s, task.ID))
			}
		}
	})

	if _, err := s.BeginFirstTaskAttempt(ctx, newTask.ID, CarriedOnFrom(carried.ID)); !errors.Is(err, ErrDispatchHeld) {
		t.Fatalf("carrying on while the project has an open entry = %v, want ErrDispatchHeld", err)
	}
	if got := attemptsOf(t, s, newTask.ID); len(got) != 0 || machineOf(t, s, newTask.ID) != "" {
		t.Fatalf("the held dispatch left attempts %+v and machine %q", got, machineOf(t, s, newTask.ID))
	}
	discardKept(t, s, open.ID)
	if _, err := s.BeginFirstTaskAttempt(ctx, newTask.ID, CarriedOnFrom(carried.ID)); err != nil {
		t.Fatalf("BeginFirstTaskAttempt carrying on: %v", err)
	}
	if got := machineOf(t, s, newTask.ID); got != "mini" {
		t.Fatalf("the carried-on task reads machine %q, want the kept copy's, mini", got)
	}
	if got := machineOf(t, s, finished.ID); got != "mini" {
		t.Fatalf("the finished task reads machine %q after the carry on", got)
	}
	if _, err := s.BeginTaskAttempt(ctx, newTask.ID, 1); err != nil || machineOf(t, s, newTask.ID) != "mini" {
		t.Fatalf("relaunching the carried-on task = %v, machine %q; want it kept", err, machineOf(t, s, newTask.ID))
	}

	t.Run("a run's copy is this machine's", func(t *testing.T) {
		ofRun, _, err := s.RecordKeptWork(ctx, KeptCopy{RunID: run.ID, CopyPath: "/home/worktrees/repo-1/run-1",
			Holds: []string{"9999"}, Reason: "unproven", Against: "dddd"})
		if err != nil {
			t.Fatal(err)
		}
		fromRun := seedTaskOnBranch(t, s, "fromrunaaaaa", "topic", secondCarrier.Row)
		carryOnKept(t, s, ofRun.ID, fromRun.ID)
		if _, err := s.BeginFirstTaskAttempt(ctx, fromRun.ID, CarriedOnFrom(ofRun.ID)); err != nil {
			t.Fatalf("carrying a run's copy on: %v", err)
		}
		if got := machineOf(t, s, fromRun.ID); got != "" {
			t.Fatalf("a task carrying a run's copy on reads machine %q, want this machine", got)
		}
	})
}

// occurrences returns every table and column of the database that holds
// value anywhere in its text, with how many rows, as "table.column=count".
// It reads the schema the database itself reports, so a column a later
// migration adds is scanned without being named here.
func occurrences(t *testing.T, s *Store, value string) []string {
	t.Helper()
	ctx := context.Background()
	rows, err := s.read.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, name)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if len(tables) == 0 {
		t.Fatal("the database reports no tables, so the scan would look at nothing")
	}
	var found []string
	for _, table := range tables {
		cols, err := s.read.QueryContext(ctx, `SELECT name FROM pragma_table_info(?)`, table)
		if err != nil {
			t.Fatal(err)
		}
		var columns []string
		for cols.Next() {
			var name string
			if err := cols.Scan(&name); err != nil {
				t.Fatal(err)
			}
			columns = append(columns, name)
		}
		if err := cols.Close(); err != nil {
			t.Fatal(err)
		}
		for _, column := range columns {
			var n int
			query := fmt.Sprintf(`SELECT count(*) FROM %q WHERE instr(CAST(%q AS TEXT), ?) > 0`, table, column)
			if err := s.read.QueryRowContext(ctx, query, value).Scan(&n); err != nil {
				t.Fatalf("scanning %s.%s: %v", table, column, err)
			}
			if n > 0 {
				found = append(found, fmt.Sprintf("%s.%s=%d", table, column, n))
			}
		}
	}
	sort.Strings(found)
	return found
}

// A task's machine is stored in one place, the task's own row, through a
// whole life: dispatch, a session and its server, a relaunch with its own,
// promotion, kept work, and a carry on, whose new task records the machine
// on its own row as its own fact. PRD principle P14 is what the test claims:
// every table and column the database holds is scanned for the machine's
// name, so a copy in any row, in a column of its own or inside another's
// text, is found. The scan's positive control is the same name written into
// a task event's detail, which it must report.
func TestTheMachineIsStoredInTheTaskRowAndNowhereElse(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P14)
	ctx := context.Background()
	s := openStore(t)
	seedRun(t, s)
	const host = "machine-7f3a9c"

	inv := seedInvestigation(t, s, "lifetimeaaaa")
	if _, err := s.BeginFirstTaskAttempt(ctx, inv.ID, OnRemoteHost(host)); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAttemptSession(ctx, inv.ID, 1, "herdr:lifetime-1", SessionHost{PID: 501, Start: 11}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginTaskAttempt(ctx, inv.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAttemptSession(ctx, inv.ID, 2, "herdr:lifetime-2", SessionHost{PID: 502, Start: 12}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.TransitionTaskShape(ctx, inv.ID, TaskInvestigation, TaskDelivery, "repo-1", "topic",
		config.DeliveryGated, "Deliver it.", 2, "promoted", ResolvedByMachineInterface()); err != nil {
		t.Fatal(err)
	}
	promoted, err := s.Task(ctx, inv.ID)
	if err != nil {
		t.Fatal(err)
	}
	entry, _, err := s.RecordKeptWork(ctx, KeptCopy{TaskID: inv.ID, CopyPath: inv.WorktreePath,
		Holds: []string{"aaaa"}, Reason: "unproven", Against: "dddd"})
	if err != nil {
		t.Fatal(err)
	}
	next := seedTaskOnBranch(t, s, "carriedonbbb", "topic", promoted.Row)
	carryOnKept(t, s, entry.ID, next.ID)
	if _, err := s.BeginFirstTaskAttempt(ctx, next.ID, CarriedOnFrom(entry.ID)); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAttemptSession(ctx, next.ID, 1, "herdr:carried-1", SessionHost{PID: 503, Start: 13}); err != nil {
		t.Fatal(err)
	}

	if got, want := occurrences(t, s, host), []string{"task.machine=2"}; strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("the machine's name is held at %v, want only %v: the promoted task's row and the carried-on task's", got, want)
	}
	for _, id := range []string{inv.ID, next.ID} {
		if got := machineOf(t, s, id); got != host {
			t.Fatalf("task %s reads machine %q, want %q", id, got, host)
		}
	}

	t.Run("the positive control: a copy anywhere else is found", func(t *testing.T) {
		if _, err := s.AppendTaskEvent(ctx, next.ID, "note", "running on "+host); err != nil {
			t.Fatal(err)
		}
		got := strings.Join(occurrences(t, s, host), " ")
		if !strings.Contains(got, "task_event.detail=1") {
			t.Fatalf("the scan found %q and missed the copy in a task event", got)
		}
	})
}
