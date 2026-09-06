package runs_test

import (
	"context"
	"errors"
	"testing"

	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/runs"
	"github.com/dayamjz/assistant/internal/store"
)

// transitions is every exported move, named, so a test can drive each of them
// against every status a run can be in.
var transitions = []struct {
	name string
	call func(svc *runs.Service, ctx context.Context, id string) (store.Run, error)
	to   store.RunStatus
	// from is every status this move is legal out of, written here rather than
	// read from the package so the table under test is not its own witness.
	from []store.RunStatus
}{
	{"Start", (*runs.Service).Start, store.RunRunning,
		[]store.RunStatus{store.RunPending}},
	{"Hold", (*runs.Service).Hold, store.RunHeld,
		[]store.RunStatus{store.RunRunning}},
	{"Release", (*runs.Service).Release, store.RunRunning,
		[]store.RunStatus{store.RunHeld}},
	{"Pass", (*runs.Service).Pass, store.RunPassed,
		[]store.RunStatus{store.RunRunning}},
	{"Fail", (*runs.Service).Fail, store.RunFailed,
		[]store.RunStatus{store.RunRunning}},
	{"Terminate", (*runs.Service).Terminate, store.RunTerminated,
		[]store.RunStatus{store.RunPending, store.RunRunning, store.RunHeld}},
}

// Every move against every status a run can be in. A move is either one the
// table names, the status the run already holds, or refused: there is no
// fourth answer, and in particular no move out of a terminal status.
func TestEveryMoveAgainstEveryStatus(t *testing.T) {
	// No case here reaches the agent, so one stand-in serves the whole table.
	agent := resolution(standin.New(t, standin.Script{}))
	for _, move := range transitions {
		for _, from := range store.RunStatuses() {
			t.Run(move.name+" from "+from.String(), func(t *testing.T) {
				ctx := t.Context()
				s, _ := openStore(t)
				svc := service(t, s, agent, false)
				run := seedRun(t, s, svc, "run-1")
				park(t, svc, run.ID, from)

				after, err := move.call(svc, ctx, run.ID)
				legal := from == move.to || contains(move.from, from)
				if legal && err != nil {
					t.Fatalf("%s from %s was refused: %v", move.name, from, err)
				}
				if !legal {
					if !errors.Is(err, store.ErrRunStatus) {
						t.Fatalf("%s from %s answered %v, want a refusal", move.name, from, err)
					}
					stood, readErr := s.Run(ctx, run.ID)
					if readErr != nil {
						t.Fatalf("Run: %v", readErr)
					}
					if stood.Status != from {
						t.Fatalf("the refused move left the run %s, want %s", stood.Status, from)
					}
					return
				}
				if after.Status != move.to {
					t.Fatalf("%s from %s left the run %s, want %s", move.name, from, after.Status, move.to)
				}
			})
		}
	}
}

// A move to the status the run already holds establishes nothing new, so it
// writes nothing and is not an error. That is what makes a transition safe to
// repeat after a failure that had already committed.
func TestAMoveToTheStatusTheRunAlreadyHoldsIsNotAnError(t *testing.T) {
	ctx := t.Context()
	s, _ := openStore(t)
	svc := service(t, s, resolution(standin.New(t, standin.Script{})), false)
	run := seedRun(t, s, svc, "run-1")

	first, err := svc.Start(ctx, run.ID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	again, err := svc.Start(ctx, run.ID)
	if err != nil {
		t.Fatalf("Start again: %v", err)
	}
	if again.Status != store.RunRunning {
		t.Fatalf("the repeated Start left the run %s", again.Status)
	}
	if !again.UpdatedAt.Equal(first.UpdatedAt) {
		t.Errorf("the repeated Start moved updated_at from %s to %s", first.UpdatedAt, again.UpdatedAt)
	}
}

// A run held and released is running again, which is the path a person's
// answer takes a run down. It is spelled out because the table above proves
// each move alone and not that they compose into a life.
func TestARunHeldAndReleasedRunsAgainAndCanStillPass(t *testing.T) {
	ctx := t.Context()
	s, _ := openStore(t)
	svc := service(t, s, resolution(standin.New(t, standin.Script{})), false)
	run := seedRun(t, s, svc, "run-1")

	for _, step := range []struct {
		name string
		call func() (store.Run, error)
		want store.RunStatus
	}{
		{"Start", func() (store.Run, error) { return svc.Start(ctx, run.ID) }, store.RunRunning},
		{"Hold", func() (store.Run, error) { return svc.Hold(ctx, run.ID) }, store.RunHeld},
		{"Release", func() (store.Run, error) { return svc.Release(ctx, run.ID) }, store.RunRunning},
		{"Pass", func() (store.Run, error) { return svc.Pass(ctx, run.ID) }, store.RunPassed},
	} {
		after, err := step.call()
		if err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		if after.Status != step.want {
			t.Fatalf("%s left the run %s, want %s", step.name, after.Status, step.want)
		}
	}
}

// A run begins pending and with no session. Both refusals are about a run
// handed over partway through a life it has not started.
func TestCreateRefusesARunThatIsAlreadyUnderway(t *testing.T) {
	ctx := t.Context()
	s, _ := openStore(t)
	svc := service(t, s, resolution(standin.New(t, standin.Script{})), true)
	if _, err := s.UpsertRepository(ctx, store.Repository{
		ID: "repo-1", WorkingPath: "/checkouts/one",
		UpstreamURL: "https://example.test/one.git", DefaultBranch: "main",
	}); err != nil {
		t.Fatalf("UpsertRepository: %v", err)
	}
	base := store.Run{
		ID: "run-1", RepositoryID: "repo-1", Branch: "topic",
		SubmittedHead: "aaaa", Base: "bbbb", Intent: "validate", IntentSource: "push",
		Build:        store.Build{Version: "v0.1.0", Revision: "0123456789abcdef", Go: "go1.25.0"},
		ConfigDigest: "cfg-1",
	}
	for _, refused := range []struct {
		name   string
		mutate func(store.Run) store.Run
	}{
		{"already running", func(r store.Run) store.Run { r.Status = store.RunRunning; return r }},
		{"already finished", func(r store.Run) store.Run { r.Status = store.RunPassed; return r }},
		{"carrying a session", func(r store.Run) store.Run {
			r.FixerSession = store.Known("sess-1")
			return r
		}},
	} {
		t.Run(refused.name, func(t *testing.T) {
			if _, err := svc.Create(ctx, refused.mutate(base)); !errors.Is(err, runs.ErrRunNotNew) {
				t.Fatalf("Create answered %v, want ErrRunNotNew", err)
			}
			if _, err := s.Run(ctx, base.ID); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("the refused run was recorded anyway: %v", err)
			}
		})
	}

	created, err := svc.Create(ctx, base)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Status != store.RunPending {
		t.Errorf("a created run is %s, want pending", created.Status)
	}
	if created.FixerSession.IsKnown() {
		t.Errorf("a created run carries the session %q", created.FixerSession)
	}
}

// park moves a run from pending to the status a case needs it in, using the
// service's own moves, so no test reaches around the table to set one up.
func park(t *testing.T, svc *runs.Service, id string, status store.RunStatus) {
	t.Helper()
	ctx := t.Context()
	var err error
	switch status {
	case store.RunPending:
		return
	case store.RunRunning:
		_, err = svc.Start(ctx, id)
	case store.RunHeld:
		if _, err = svc.Start(ctx, id); err == nil {
			_, err = svc.Hold(ctx, id)
		}
	case store.RunPassed:
		if _, err = svc.Start(ctx, id); err == nil {
			_, err = svc.Pass(ctx, id)
		}
	case store.RunFailed:
		if _, err = svc.Start(ctx, id); err == nil {
			_, err = svc.Fail(ctx, id)
		}
	case store.RunTerminated:
		_, err = svc.Terminate(ctx, id)
	default:
		t.Fatalf("no way to park a run in %q", status)
	}
	if err != nil {
		t.Fatalf("parking run %s in %s: %v", id, status, err)
	}
}

func contains(statuses []store.RunStatus, status store.RunStatus) bool {
	for _, s := range statuses {
		if s == status {
			return true
		}
	}
	return false
}
