package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// TestDumpExportsTheRecordsTheResolverReads is PRD section 9's dump: one
// JSON document of every task snapshot, the task wakes and the kept work,
// redacted on the way out, from a read-only open that asks the service
// nothing. P13 is cited because the answer is an evidence document.
//
// Fail-before against the unchanged tree: assistant --json dump exited usage
// with "dump" is not a command (recorded in REPORT.md).
func TestDumpExportsTheRecordsTheResolverReads(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P13)
	h := newHome(t)
	records := openStore(t, h)
	t.Cleanup(func() { _ = records.Close() })

	const taskID = "dumptaskaaaa"
	fleetTask(t, records, taskID, "the parser", [2]string{"working", "on https://deploy:hunter2-dump-secret@example.com/o/r.git"})
	if _, err := records.RegisterHold(t.Context(), store.Hold{
		Key: "task/" + taskID + "/ask", TaskID: store.Known(taskID),
		Subject: "whether to go on", Detail: "Bearer dumpBearerTok0123456789",
	}); err != nil {
		t.Fatalf("registering a hold: %v", err)
	}
	inc, err := records.BeginIncarnation(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := records.AppendWakeWithMarkers(t.Context(), inc, store.WakeRequest{
		TaskID: taskID, Kind: "hold-opened", Marker: "dump-wake",
	}, store.MarkerAdvance{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := records.RecordKeptWork(t.Context(), store.KeptCopy{
		TaskID: taskID, CopyPath: "/worktrees/" + taskID, Holds: []string{"aaaa"},
		Unestablished: "could not compare",
		Reason:        "still needed", Detail: "kept for https://x:keptDumpSecret99@example.com/p.git",
	}); err != nil {
		t.Fatalf("recording kept work: %v", err)
	}

	got := run(t, h, h.Root(), "--json", "dump")
	if got.code != machine.ExitOK {
		t.Fatalf("assistant --json dump exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	var dumped machine.Dump
	if err := json.Unmarshal([]byte(got.stdout), &dumped); err != nil {
		t.Fatalf("the dump is not one document: %v\n%s", err, got.stdout)
	}
	if len(dumped.Tasks) != 1 || dumped.Tasks[0].Task.ID != taskID {
		t.Fatalf("dump tasks = %+v, want %s", dumped.Tasks, taskID)
	}
	if len(dumped.Wakes) != 1 || dumped.Wakes[0].Marker != "dump-wake" {
		t.Fatalf("dump wakes = %+v, want dump-wake", dumped.Wakes)
	}
	if len(dumped.KeptWork) != 1 || dumped.KeptWork[0].CopyPath != "/worktrees/"+taskID {
		t.Fatalf("dump kept work = %+v, want the kept copy", dumped.KeptWork)
	}
	for _, secret := range []string{"hunter2-dump-secret", "dumpBearerTok0123456789", "keptDumpSecret99"} {
		if strings.Contains(got.stdout, secret) {
			t.Errorf("the dump carries the credential %q:\n%s", secret, got.stdout)
		}
	}
	if !strings.Contains(got.stdout, redact.Marker) {
		t.Errorf("the dump carries no redaction marker, so scrubbing did not run:\n%s", got.stdout)
	}

	narrow := run(t, h, h.Root(), "--json", "dump", taskID)
	if narrow.code != machine.ExitOK {
		t.Fatalf("assistant --json dump %s exited %s:\n%s%s", taskID, narrow.code, narrow.stdout, narrow.stderr)
	}
	var one machine.Dump
	if err := json.Unmarshal([]byte(narrow.stdout), &one); err != nil {
		t.Fatalf("the narrowed dump is not one document: %v\n%s", err, narrow.stdout)
	}
	if len(one.Tasks) != 1 || len(one.Wakes) != 1 || len(one.KeptWork) != 1 {
		t.Fatalf("narrowed dump = tasks %d wakes %d kept %d, want one of each",
			len(one.Tasks), len(one.Wakes), len(one.KeptWork))
	}

	unknown := run(t, h, h.Root(), "--json", "dump", "nosuchtaskxx")
	if unknown.code != machine.ExitUsage {
		t.Fatalf("dump of an unknown task exited %s, want usage:\n%s%s", unknown.code, unknown.stdout, unknown.stderr)
	}
}

// TestDumpRoundTripsThroughTheResolverToTheSameTasksOutput is the acceptance
// criterion: the dump of a home, fed through taskstate.Resolve, answers the
// same state assistant --json tasks reports from those records (P8).
//
// Live inputs (a live terminal, an advancing run) are not in the dump, so the
// comparison is over records-derived states: Held here, with no worker.
func TestDumpRoundTripsThroughTheResolverToTheSameTasksOutput(t *testing.T) {
	t.Parallel()
	requiresIdentifiedPeer(t)
	principles.Cite(t, principles.P8)
	h := newHome(t)
	subject := newSubject(t)
	records := openStore(t, h)
	t.Cleanup(func() { _ = records.Close() })

	fleetTask(t, records, "roundtriptaa", "the question", [2]string{"working", "still on it"})
	if _, err := records.RegisterHold(t.Context(), store.Hold{
		Key: "task/roundtriptaa/ask", TaskID: store.Known("roundtriptaa"), Subject: "whether to go on",
	}); err != nil {
		t.Fatal(err)
	}
	fleetTask(t, records, "roundtriptab", "the audit", [2]string{store.ReportReadyEventKind, "filed"})
	if err := records.Close(); err != nil {
		t.Fatal(err)
	}

	serve(t, h)
	if got := run(t, h, subject, "init"); got.code != machine.ExitOK {
		t.Fatalf("assistant init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}

	dumped := run(t, h, h.Root(), "--json", "dump")
	if dumped.code != machine.ExitOK {
		t.Fatalf("assistant --json dump exited %s:\n%s%s", dumped.code, dumped.stdout, dumped.stderr)
	}
	var document machine.Dump
	if err := json.Unmarshal([]byte(dumped.stdout), &document); err != nil {
		t.Fatalf("the dump is not one document: %v\n%s", err, dumped.stdout)
	}

	fromDump := map[string]taskstate.State{}
	for _, snap := range document.Tasks {
		fromDump[snap.Task.ID] = taskstate.Resolve(taskstate.InputsOf(snap)).State
	}

	listed := run(t, h, subject, "--json", "tasks")
	if listed.code != machine.ExitOK {
		t.Fatalf("assistant --json tasks exited %s:\n%s%s", listed.code, listed.stdout, listed.stderr)
	}
	var tasks machine.Tasks
	if err := json.Unmarshal([]byte(listed.stdout), &tasks); err != nil {
		t.Fatalf("the tasks answer is not one document: %v\n%s", err, listed.stdout)
	}
	fromTasks := map[string]taskstate.State{}
	for _, task := range tasks.Tasks {
		fromTasks[task.Record.ID] = task.State.State
	}

	if len(fromDump) == 0 {
		t.Fatal("the dump carried no tasks")
	}
	for id, want := range fromDump {
		if got := fromTasks[id]; got != want {
			t.Errorf("task %s: dump resolves %s, assistant tasks reports %s", id, want, got)
		}
	}
	for id := range fromTasks {
		if _, ok := fromDump[id]; !ok {
			t.Errorf("assistant tasks names %s, which the dump did not carry", id)
		}
	}
}

// TestDumpCreatesNoHomeAndAsksTheServiceNothing holds the stuck-home
// promise: dump opens the database read-only, creates nothing, and never
// starts the service. A --home that has never been opened stays absent.
// Under an existing root with a database already seeded, Create's layout
// directories stay absent (Create would write them; dump must not).
func TestDumpCreatesNoHomeAndAsksTheServiceNothing(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "never-created")
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("%s should be absent before the command: %v", root, err)
	}
	absent := runArgs(t, t.TempDir(), "--home", root, "--json", "dump")
	if absent.code == machine.ExitOK {
		t.Fatalf("assistant --json dump under a never-created home exited OK:\n%s%s", absent.stdout, absent.stderr)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("dump created %s; the verb creates no home", root)
	}

	h := newHome(t)
	records := openStore(t, h)
	if err := records.Close(); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"worktrees", "evidence", "tasks", "logs", "terminals", "domains"} {
		layout := filepath.Join(h.Root(), dir)
		if _, err := os.Stat(layout); !os.IsNotExist(err) {
			t.Fatalf("%s should be absent before the command so Create is locked: %v", layout, err)
		}
	}
	// No serve: the verb must answer without the service.
	got := run(t, h, h.Root(), "--json", "dump")
	if got.code != machine.ExitOK {
		t.Fatalf("assistant --json dump with the service down exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	var document machine.Dump
	if err := json.Unmarshal([]byte(got.stdout), &document); err != nil {
		t.Fatalf("the dump is not one document: %v\n%s", err, got.stdout)
	}
	if len(document.Tasks) != 0 || len(document.Wakes) != 0 || len(document.KeptWork) != 0 {
		t.Errorf("empty home dump = %+v, want empty slices", document)
	}
	for _, dir := range []string{"worktrees", "evidence", "tasks", "logs", "terminals", "domains"} {
		layout := filepath.Join(h.Root(), dir)
		if _, err := os.Stat(layout); !os.IsNotExist(err) {
			t.Fatalf("dump created %s; the verb creates no home layout", layout)
		}
	}
}
