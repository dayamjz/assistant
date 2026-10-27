package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/service"
	"github.com/dayamjz/assistant/internal/stages"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// passOnBranch records a housekeeping pass of the subject promoted onto
// branch, which makes a run of that branch the pass's.
func passOnBranch(t *testing.T, h *home.Home, branch string) {
	t.Helper()
	ctx := t.Context()
	records, err := store.Open(ctx, h.Database(), store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = records.Close() }()
	const id = "taskhkrunaaa"
	if _, err := records.CreateTask(ctx, store.Task{ID: id, Shape: store.TaskInvestigation, Project: "one", WorktreePath: "/w",
		RepositoryID: store.Known("subject"), Role: config.RoleHousekeeping, Housekeeping: true},
		store.Disposition{Delivered: taskstate.Delivered}); err != nil {
		t.Fatal(err)
	}
	events, err := records.TaskEvents(ctx, id, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := records.TransitionTaskShape(ctx, id, store.TaskInvestigation, store.TaskDelivery, "subject", branch,
		config.DeliveryGated, "Correct the drift.", events[len(events)-1].Sequence, "promoted", store.ResolvedByMachineInterface(), nil); err != nil {
		t.Fatal(err)
	}
	// The pass's attempt is what attributes a run of the branch to it.
	if _, err := records.BeginFirstTaskAttempt(ctx, id, store.OnThisMachine()); err != nil {
		t.Fatal(err)
	}
}

// taskOnBranchAfterThePass records an ordinary delivery task of the subject
// on branch, with its first attempt begun after the pass's, so a run of the
// branch from then on is that task's and not the pass's.
func taskOnBranchAfterThePass(t *testing.T, h *home.Home, branch string) {
	t.Helper()
	ctx := t.Context()
	records, err := store.Open(ctx, h.Database(), store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = records.Close() }()
	const id = "taskhkrunbbb"
	// The attempt's record is ordered after the pass's by its time, which
	// is the attribution's order; the pause keeps the two records' times
	// apart, as the store's own tests do, and races nothing.
	time.Sleep(25 * time.Millisecond)
	pass, err := records.Task(ctx, "taskhkrunaaa")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := records.CreateTask(ctx, store.Task{ID: id, Shape: store.TaskDelivery, Project: "one", WorktreePath: "/w2",
		RepositoryID: store.Known("subject"), Branch: store.Known(branch)}, store.Disposition{Observed: pass.Row}); err != nil {
		t.Fatal(err)
	}
	if _, err := records.BeginFirstTaskAttempt(ctx, id, store.OnThisMachine()); err != nil {
		t.Fatal(err)
	}
}

// passKeyOfAStartedRun starts a run of the subject's branch and returns
// what its state says of pipeline.KeyHousekeepingPass, read by the intent
// stage's body, which declares it.
func passKeyOfAStartedRun(t *testing.T, h *home.Home, subject string) bool {
	t.Helper()
	seen := make(chan bool, 1)
	o := options(t, h)
	o.NewStages = func(deps stages.StageDeps) pipeline.Stages {
		nine := stages.All(deps)
		nine.Intent = pipeline.Implementation{
			Reads: []pipeline.Key{pipeline.KeyHousekeepingPass},
			NewBody: func() pipeline.Body {
				return func(_ context.Context, in pipeline.Input) (pipeline.Output, error) {
					v, err := in.State.Get(pipeline.KeyHousekeepingPass)
					if err != nil {
						return pipeline.Output{}, err
					}
					flag, _ := v.Bool()
					select {
					case seen <- flag:
					default:
					}
					return pipeline.Output{Report: findings.Report{Summary: "read the run's inputs",
						Findings: []findings.Finding{{ID: "stop", Action: findings.ActionAsk, Description: "stop here"}}}}, nil
				}
			},
		}
		return nine
	}
	running, err := service.Open(t.Context(), o)
	if err != nil {
		t.Fatalf("opening the service: %v", err)
	}
	served := make(chan error, 1)
	go func() { served <- running.Serve(context.Background()) }()
	t.Cleanup(func() {
		_ = running.Close()
		if err := <-served; err != nil {
			t.Errorf("serving: %v", err)
		}
	})
	var run machine.Run
	if err := dial(t, running).Call(t.Context(), ipc.MethodRunStart, machine.StartRequest{
		Working: machine.Working{WorkingPath: subject},
	}, &run); err != nil {
		t.Fatalf("starting the run: %v", err)
	}
	select {
	case flag := <-seen:
		return flag
	default:
		t.Fatal("the intent stage never read the run's inputs")
		return false
	}
}

// PRD section 6's housekeeping: a run of a pass's branch begins with
// pipeline.KeyHousekeepingPass set, decided from the task records by its
// repository and branch, so the review stage holds it to the pass's scope.
// The run here is one a person starts, not the pass's own push, which is the
// key's stated reach: every run of the branch, whoever started it; a run of a branch no pass works
// on begins with it unset, which is the control that the key is read from
// the records and not set on every run.
func TestARunOfAPassBranchCarriesThePassKey(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	pass := newHome(t)
	subject := newSubject(t)
	recordRepository(t, pass, subject)
	passOnBranch(t, pass, "work")
	if !passKeyOfAStartedRun(t, pass, subject) {
		t.Fatal("a run of the pass's branch began without the pass key")
	}

	plain := newHome(t)
	other := newSubject(t)
	recordRepository(t, plain, other)
	if passKeyOfAStartedRun(t, plain, other) {
		t.Fatal("a run of a branch no pass works on began with the pass key")
	}

	// The key is the run's task's, not the branch's: once an ordinary task
	// has taken a branch a pass used, its runs are that task's and carry no
	// pass key, so an ordinary delivery is not held to the pass's scope.
	reused := newHome(t)
	third := newSubject(t)
	recordRepository(t, reused, third)
	passOnBranch(t, reused, "work")
	taskOnBranchAfterThePass(t, reused, "work")
	if passKeyOfAStartedRun(t, reused, third) {
		t.Fatal("a run of a branch an ordinary task took after a pass began with the pass key")
	}
}
