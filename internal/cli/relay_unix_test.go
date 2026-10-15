//go:build unix

package cli_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// notedReport is a report whose one finding is a note, so it registers no
// hold and the investigation that files it reads Reported at once.
const notedReport = `{"summary": "The parser handles empty input.", "findings": [` +
	`{"severity": "info", "action": "note", "description": "Empty input returns an empty tree."}]}`

// routedInvestigation creates an investigation through the coordinator's
// client, optionally queued behind overlaps, and returns its identifier.
func routedInvestigation(t *testing.T, h *home.Home, subject, repository, project, overlaps string) machine.TaskCreated {
	t.Helper()
	args := []string{"create", "--shape", "investigation", "--project", project,
		"--instructions", "Look into how the parser handles empty input.", "--repository", repository}
	if overlaps != "" {
		args = append(args, "--overlaps", overlaps)
	}
	var created machine.TaskCreated
	coordinate(t, h, subject, &created, args...)
	return created
}

// workerCallIn is the stand-in's record of the worker started in task id's
// copy, once it has read its instructions, so a test running several
// workers reads the instructions of the one it means.
func workerCallIn(t *testing.T, h *home.Home, agent *standin.Agent, id string) standin.Call {
	t.Helper()
	copyPath, err := h.TaskCopy(id)
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(copyPath)
	if err != nil {
		t.Fatal(err)
	}
	var found standin.Call
	waitFor(t, "the worker of task "+id+" reading its instructions", func() bool {
		for _, c := range agent.Calls() {
			dir, err := filepath.EvalSymlinks(c.Dir)
			if c.Worker && err == nil && dir == want && len(c.Lines) > 0 {
				found = c
				return true
			}
		}
		return false
	})
	return found
}

// queuedUnlaunched holds a task to Queued from its history with no attempt
// begun: nothing has launched a worker for it.
func queuedUnlaunched(t *testing.T, h *home.Home, subject, id string) {
	t.Helper()
	got := taskOf(t, h, subject, id)
	if got.State.State != taskstate.Queued || got.State.Source != taskstate.SourceEvent {
		t.Fatalf("task %s reads %s from %s, want queued from its history", id, got.State.State, got.State.Source)
	}
	if n := len(attemptsOf(t, h, id)); n != 0 {
		t.Fatalf("task %s, still queued, has %d attempts", id, n)
	}
}

// #266: filing an investigation's report releases what is queued behind it,
// from the place the report becomes a record.
//
// Two investigations run on the stand-in worker, the shipped Claude Code
// adapter's, and a third and a fourth are each queued behind one of them by
// the coordinator's overlap claim. The first files its report through the
// real worker report route, as its instructions say, with only a note, so it
// reads Reported from the stored report at once. The task queued behind it
// is then released and its worker launched: an attempt with a session is
// recorded for it. Neither of the moments that released a queued task before
// this change happened: the home holds no run, so no run finished, and the
// test opens one service before anything is created and opens none after,
// so no service start released it. The control is the task queued behind the second
// investigation, which files nothing: it still reads Queued from its history
// with no attempt, so the release followed the stored report and not the
// passing of time or another task's launch (P8).
func TestFilingAReportReleasesTheTaskQueuedBehindIt(t *testing.T) {
	principles.Cite(t, principles.P8)
	requiresIdentifiedPeer(t)
	quietGit(t)
	h := newHome(t)
	subject := newSubject(t)
	if err := h.Create(); err != nil {
		t.Fatal(err)
	}
	agent, route := serveRouted(t, h)
	if got := run(t, h, subject, "init"); got.code != machine.ExitOK {
		t.Fatalf("assistant init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	repository := repositoryOf(t, h, subject)

	reporting := routedInvestigation(t, h, subject, repository, "report", "").Record.ID
	waitSessions(t, h, reporting, 1)
	silent := routedInvestigation(t, h, subject, repository, "silent", "").Record.ID
	waitSessions(t, h, silent, 1)
	behind := routedInvestigation(t, h, subject, repository, "behind", reporting)
	control := routedInvestigation(t, h, subject, repository, "control", silent)
	for _, c := range []machine.TaskCreated{behind, control} {
		if c.Cause != "claimed" || !c.Record.BlockerTask.IsKnown() {
			t.Fatalf("task %s was recorded %s with blocker %v, want queued on the overlap claim", c.Record.ID, c.Cause, c.Record.BlockerTask)
		}
		queuedUnlaunched(t, h, subject, c.Record.ID)
	}

	commands := routeCommands(t, workerCallIn(t, h, agent, reporting).Lines[0], route)
	fileReport(t, h, subject, reporting, commands["report"], notedReport)
	if got := taskOf(t, h, subject, reporting); got.State.State != taskstate.Reported || got.State.Source != taskstate.SourceReport {
		t.Fatalf("the reporting investigation reads %s from %s, want reported from its report", got.State.State, got.State.Source)
	}

	waitSessions(t, h, behind.Record.ID, 1)
	if got := taskOf(t, h, subject, behind.Record.ID); got.State.State == taskstate.Queued {
		t.Fatalf("the released task still reads queued, from %s", got.State.Source)
	}
	queuedUnlaunched(t, h, subject, control.Record.ID)

	records := openStore(t, h)
	runs, err := records.RunsForRepository(t.Context(), repository)
	_ = records.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("the home holds %d runs, so a run finishing could have released the task: %+v", len(runs), runs)
	}
}

// #278: the coordinator records that it relayed a reported investigation's
// findings, and the record moves the investigation to Complete through the
// terminal tier.
//
// An investigation on the stand-in worker files its report through the
// worker route and reads Reported from the stored report. The coordinator's
// client records the relay, and the task reads Complete from the record
// (SourceRecord, Basis.Relayed), not from the findings-relayed entry beside
// it (P8). A relayed investigation is owed no worker and its copy is owed
// the cleanup decision from that record, so the service ends the worker it
// holds and removes the copy on the scratch proof (P12), and the task still
// reads Complete.
//
// The refusals are the controls, each leaving the history as it was: a
// delivery task, which has no findings to relay; an investigation that has
// filed no report; the same relay from inside the reporting worker's
// process group, which no worker may make; and a second relay of the task
// already Complete. The successful relay from this test's process is the
// positive control for all four.
func TestARelayCompletesAReportedInvestigationAndNothingElse(t *testing.T) {
	principles.Cite(t, principles.P8)
	principles.Cite(t, principles.P12)
	requiresIdentifiedPeer(t)
	requireSweepClears(t)
	quietGit(t)
	h := newHome(t)
	subject := newSubject(t)
	if err := h.Create(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.ConfigFile(), []byte(`{"supervision_beacon_interval": "1s"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	agent, route := serveRouted(t, h)
	if got := run(t, h, subject, "init"); got.code != machine.ExitOK {
		t.Fatalf("assistant init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	repository := repositoryOf(t, h, subject)

	inquiry := routedInvestigation(t, h, subject, repository, "inquiry", "").Record.ID
	waitSessions(t, h, inquiry, 1)
	silent := routedInvestigation(t, h, subject, repository, "silent", "").Record.ID
	waitSessions(t, h, silent, 1)
	var delivery machine.TaskCreated
	coordinate(t, h, subject, &delivery, "create", "--shape", "delivery", "--project", "change",
		"--instructions", "Make the change.", "--repository", repository, "--branch", "task/change")
	waitSessions(t, h, delivery.Record.ID, 1)

	relayRefused := func(id string) {
		t.Helper()
		before := historyKinds(t, h, id)
		refusedAs(t, run(t, h, subject, "--json", "coordinator", "relayed", id), ipc.CodeRefused, machine.ExitFailure)
		if got := taskOf(t, h, subject, id); got.State.State == taskstate.Complete || got.State.Basis.Relayed {
			t.Fatalf("a refused relay left task %s reading %s (relayed %t)", id, got.State.State, got.State.Basis.Relayed)
		}
		if after := historyKinds(t, h, id); after[store.FindingsRelayedEventKind] != before[store.FindingsRelayedEventKind] {
			t.Fatalf("a refused relay grew task %s's history from %v to %v", id, before, after)
		}
	}
	relayRefused(delivery.Record.ID)
	relayRefused(silent)

	commands := routeCommands(t, workerCallIn(t, h, agent, inquiry).Lines[0], route)
	fileReport(t, h, subject, inquiry, commands["report"], notedReport)
	if got := taskOf(t, h, subject, inquiry); got.State.State != taskstate.Reported || got.State.Source != taskstate.SourceReport {
		t.Fatalf("the investigation reads %s from %s after its report, want reported from the report", got.State.State, got.State.Source)
	}

	// From inside the worker's group the relay is refused as a worker's.
	refusedAs(t, coordinateAs(t, workerGroup(t, h, inquiry), h, subject, "relayed", inquiry), ipc.CodeWorker, machine.ExitFailure)
	if got := taskOf(t, h, subject, inquiry); got.State.State != taskstate.Reported {
		t.Fatalf("a worker's refused relay moved the investigation to %s", got.State.State)
	}

	var relayed machine.TaskRelayed
	coordinate(t, h, subject, &relayed, "relayed", inquiry)
	if relayed.Record.TaskID != inquiry || relayed.Event.Kind != store.FindingsRelayedEventKind || relayed.Record.Event != relayed.Event.Sequence {
		t.Fatalf("the relay answered %+v, want the record naming the entry it appended", relayed)
	}
	got := taskOf(t, h, subject, inquiry)
	if got.State.State != taskstate.Complete || got.State.Source != taskstate.SourceRecord || !got.State.Basis.Relayed ||
		got.State.Basis.EventKind != store.FindingsRelayedEventKind {
		t.Fatalf("the relayed investigation reads %s from %s with %+v, want complete from the record", got.State.State, got.State.Source, got.State.Basis)
	}
	relayRefused(delivery.Record.ID)
	before := historyKinds(t, h, inquiry)
	refusedAs(t, run(t, h, subject, "--json", "coordinator", "relayed", inquiry), ipc.CodeRefused, machine.ExitFailure)
	if after := historyKinds(t, h, inquiry); after[store.FindingsRelayedEventKind] != 1 || before[store.FindingsRelayedEventKind] != 1 {
		t.Fatalf("the relay is recorded %d times after a second was asked, want once", after[store.FindingsRelayedEventKind])
	}

	// The copy goes on the record's terms once the worker is ended.
	t.Cleanup(func() {
		if t.Failed() {
			text, _ := os.ReadFile(h.ServiceLog())
			t.Logf("the service log:\n%s", text)
		}
	})
	copyPath, err := h.TaskCopy(inquiry)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the relayed investigation's copy removed", func() bool { return taskOf(t, h, subject, inquiry).State.Basis.CleanupRemoved })
	if _, err := os.Lstat(copyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the task records its copy removed and %s still stands: %v", copyPath, err)
	}
	if got := taskOf(t, h, subject, inquiry); got.State.State != taskstate.Complete || got.State.Source != taskstate.SourceRecord {
		t.Fatalf("with its copy removed the relayed investigation reads %s from %s, want complete from the record", got.State.State, got.State.Source)
	}
}
