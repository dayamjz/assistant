package cli_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/machine/vocabtest"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
)

// With the service down and work in flight, status computes PRD section 5's
// pull guard from the home's files and lock and warns, in internal/machine's
// vocabulary and nothing else: the warning is held to the derived forbidden
// set here as well as in machine's own test, because this is where it
// reaches a person. With nothing in flight there is no warning, so the
// warning rests on the in-flight read and not on the service being down.
func TestStatusWarnsWhenWorkIsInFlightAndNothingWatchesIt(t *testing.T) {
	t.Parallel()
	vocabtest.Control(t)
	h := newHome(t)
	subject := newSubject(t)
	records := openStore(t, h)
	if _, err := records.CreateTask(t.Context(), store.Task{
		ID: "abcdefghij23", Shape: store.TaskInvestigation, Project: "one",
		Mode: "headless", WorktreePath: "/worktrees/one",
	}, store.Disposition{}); err != nil {
		t.Fatalf("creating a task: %v", err)
	}
	if err := records.Close(); err != nil {
		t.Fatal(err)
	}

	got := run(t, h, subject, "--json", "status")
	if got.code != machine.ExitOK {
		t.Fatalf("status exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	var status machine.Status
	if err := json.Unmarshal([]byte(got.stdout), &status); err != nil {
		t.Fatalf("decoding: %v\n%s", err, got.stdout)
	}
	if status.Service.Running {
		t.Fatal("the service is reported running, so the guard was not computed with it down")
	}
	if status.Supervision == nil || status.Supervision.Warning == "" {
		t.Fatalf("status carries no warning with a task in flight and no service: %+v", status.Supervision)
	}
	if status.Supervision.Reason != supervisor.StopLockFree {
		t.Fatalf("the guard's reason is %s, want lock-free with no service", status.Supervision.Reason)
	}
	if status.Supervision.Warning != machine.PhraseServiceDown {
		t.Fatalf("with no service the warning is %q, want the one whose next decision is starting the service", status.Supervision.Warning)
	}
	vocabtest.Assert(t, status.Supervision.Warning)

	plain := run(t, h, subject, "status")
	if !strings.Contains(plain.stdout, status.Supervision.Warning) {
		t.Fatalf("the human rendering does not carry the warning:\n%s", plain.stdout)
	}

	// Nothing in flight: no warning, and the guard still answered.
	empty := newHome(t)
	openStore(t, empty).Close()
	got = run(t, empty, subject, "--json", "status")
	var idle machine.Status
	if err := json.Unmarshal([]byte(got.stdout), &idle); err != nil {
		t.Fatalf("decoding: %v\n%s", err, got.stdout)
	}
	if idle.Supervision == nil || idle.Supervision.Warning != "" || idle.Supervision.Reason != supervisor.StopNothingInFlight {
		t.Fatalf("an empty fleet reports %+v, want nothing in flight and no warning", idle.Supervision)
	}
}
