package service_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// queuedBehindABlocker records, where a coordinator's intake would have, a
// blocker task on the subject's work branch with its first attempt begun, and
// a successor queued behind it. Nothing in this build creates a task through
// the service, so the records are written through internal/store.
func queuedBehindABlocker(t *testing.T, h *home.Home, repository store.Repository) (blocker, successor string) {
	t.Helper()
	records := openRecords(t, h)
	defer func() { _ = records.Close() }()
	ctx := t.Context()
	b, err := records.CreateTask(ctx, store.Task{
		ID: "blockeraaaaa", Shape: store.TaskDelivery, Project: "one", Mode: "interactive", WorktreePath: "/w/blocker",
		RepositoryID: store.Known(repository.ID), Branch: store.Known("work"),
	}, store.Disposition{})
	if err != nil {
		t.Fatalf("recording the blocker: %v", err)
	}
	if _, err := records.BeginTaskAttempt(ctx, b.ID, 0); err != nil {
		t.Fatalf("beginning the blocker's attempt: %v", err)
	}
	s, err := records.CreateTask(ctx, store.Task{
		ID: "successoraaa", Shape: store.TaskDelivery, Project: "one", Mode: "interactive", WorktreePath: "/w/successor",
		RepositoryID: store.Known(repository.ID), Branch: store.Known("work"),
	}, store.Disposition{Blocker: store.Known(b.ID), Observed: b.Row})
	if err != nil {
		t.Fatalf("recording the successor: %v", err)
	}
	return b.ID, s.ID
}

// taskState reads a task's resolved state off the service.
func taskState(t *testing.T, client *ipc.Client, id string) taskstate.Resolution {
	t.Helper()
	var task machine.Task
	if err := client.Call(t.Context(), ipc.MethodTaskGet, machine.TaskRequest{Task: id}, &task); err != nil {
		t.Fatalf("reading task %s: %v", id, err)
	}
	return task.State
}

// A run that finishes releases the task queued behind the task the run was
// attributed to. The blocker's run is started through the service and ended
// through it, which moves the blocker to Failed; the successor, which was
// Queued while the run stood, is Dispatched once the run has settled, and
// nothing but the service's own release wrote that.
func TestASettledRunReleasesTheTaskQueuedBehindIt(t *testing.T) {
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	blocker, successor := queuedBehindABlocker(t, h, repository)

	withService(t, h, func(running serviceUnderTest) {
		if got := taskState(t, running.client, successor); got.State != taskstate.Queued {
			t.Fatalf("before any run the successor resolves %s", got.State)
		}
		started := startRun(t, running.client, subject)
		holdingAt(t, started, holdingStage(t, 0))
		if got := taskState(t, running.client, blocker); got.State != taskstate.Held || got.Source != taskstate.SourceRun {
			t.Fatalf("with its run holding the blocker resolves %s from %s", got.State, got.Source)
		}
		if got := taskState(t, running.client, successor); got.State != taskstate.Queued {
			t.Fatalf("with the blocker's run holding the successor resolves %s, want queued", got.State)
		}

		var ended machine.Run
		if err := running.client.Call(t.Context(), ipc.MethodRunCancel,
			machine.CancelRequest{Run: started.Record.ID}, &ended); err != nil {
			t.Fatalf("ending the run: %v", err)
		}
		if got := taskState(t, running.client, blocker); got.State != taskstate.Failed {
			t.Fatalf("with its run ended the blocker resolves %s, want failed", got.State)
		}
		got := taskState(t, running.client, successor)
		if got.State != taskstate.Dispatched || got.Source != taskstate.SourceEvent {
			t.Fatalf("after the blocker cleared the successor resolves %s from %s, want dispatched from its release",
				got.State, got.Source)
		}
	})
}

// A blocker that cleared while no service was serving the home releases its
// successor when the next service opens, before it serves: the records are
// written with the blocker's run already failed, and the first read of the
// successor finds it Dispatched.
func TestOpeningTheServiceReleasesATaskWhoseBlockerClearedMeanwhile(t *testing.T) {
	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	_, successor := queuedBehindABlocker(t, h, repository)
	func() {
		records := openRecords(t, h)
		defer func() { _ = records.Close() }()
		if _, err := records.CreateRun(t.Context(), store.Run{
			ID: "run-blocker", RepositoryID: repository.ID, Branch: "work", SubmittedHead: "aaaa", Base: "bbbb",
			Intent: "validate", IntentSource: "push", ConfigDigest: "cfg",
			Build: store.Build{Version: "v0", Revision: "0123456789abcdef", Go: "go1.25.0"},
		}); err != nil {
			t.Fatalf("recording the blocker's run: %v", err)
		}
		if _, err := records.TransitionRun(t.Context(), "run-blocker", []store.RunStatus{store.RunPending}, store.RunFailed); err != nil {
			t.Fatalf("failing the blocker's run: %v", err)
		}
	}()

	withService(t, h, func(running serviceUnderTest) {
		got := taskState(t, running.client, successor)
		if got.State != taskstate.Dispatched || got.Source != taskstate.SourceEvent {
			t.Fatalf("after open the successor resolves %s from %s, want dispatched from its release", got.State, got.Source)
		}
	})
}

// A push that supersedes the blocker's run does not release the successor.
// The branch's run is terminated and its replacement created under one
// exclusion, and no release runs between the two: the blocker resolves from
// the replacement run the moment it exists, so the successor stays Queued
// rather than being dispatched onto a branch whose worker is still pushing
// to it.
func TestAPushSupersedingTheBlockersRunDoesNotReleaseTheSuccessor(t *testing.T) {
	requiresIdentifiedPeer(t)
	h := newHome(t)
	subject := newSubject(t)
	repository := recordRepository(t, h, subject)
	blocker, successor := queuedBehindABlocker(t, h, repository)
	gateID := func() string {
		records := openRecords(t, h)
		defer func() { _ = records.Close() }()
		binding, err := records.GateBinding(t.Context(), subject)
		if err != nil {
			t.Fatalf("reading the gate binding: %v", err)
		}
		return binding.GateID
	}()
	first := git(t, subject, "rev-parse", "HEAD")
	if err := writeFile(filepath.Join(subject, "second.txt"), "a second version\n"); err != nil {
		t.Fatalf("writing the second version: %v", err)
	}
	git(t, subject, "add", "-A")
	git(t, subject, "commit", "--quiet", "-m", "the second version")
	second := git(t, subject, "rev-parse", "HEAD")

	withService(t, h, func(running serviceUnderTest) {
		push := func(old, head string) machine.Notification {
			var out machine.Notification
			if err := running.client.Call(t.Context(), ipc.MethodGateNotify, machine.GateRequest{
				Gate: gateID, Updates: []gate.RefUpdate{{Old: old, New: head, Ref: "refs/heads/work"}},
			}, &out); err != nil {
				t.Fatalf("notifying the push of %s: %v", head, err)
			}
			return out
		}
		one := push(strings.Repeat("0", 40), first)
		if len(one.Started) != 1 {
			t.Fatalf("the first push started %+v", one.Started)
		}
		if got := taskState(t, running.client, blocker); got.Source != taskstate.SourceRun {
			t.Fatalf("after the first push the blocker resolves %s from %s, want from its run", got.State, got.Source)
		}
		two := push(first, second)
		if len(two.Started) != 1 || two.Started[0].Superseded != one.Started[0].Run {
			t.Fatalf("the second push started %+v, want one run superseding %s", two.Started, one.Started[0].Run)
		}
		if got := taskState(t, running.client, blocker); got.Source != taskstate.SourceRun || got.Basis.RunID != two.Started[0].Run {
			t.Fatalf("after the second push the blocker resolves %s from %s on run %s, want the replacement run",
				got.State, got.Source, got.Basis.RunID)
		}
		got := taskState(t, running.client, successor)
		if got.State != taskstate.Queued || got.Source != taskstate.SourceEvent {
			t.Fatalf("after the superseding push the successor resolves %s from %s, want still queued", got.State, got.Source)
		}
	})
}
