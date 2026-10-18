//go:build unix

package cli_test

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/cli"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/machine/vocabtest"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/service"
	"github.com/dayamjz/assistant/internal/stages"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// GAP-04: an investigation's filed report is visible on task.get, survives a
// service restart with its holds, and the person-facing line points at the
// structured answer rather than relaying the report (P13).
//
// The worker files through the real `assistant worker report` route. Before
// the service stops, task.get carries summary, findings, evidence paths and
// the evidence-file path; the plain tasks line names no finding text. After
// stop and reopen, the task still reads Held from the report's open decision,
// the same open holds, and the same report body.
func TestAnInvestigationsReportIsVisibleAndSurvivesRestart(t *testing.T) {
	principles.Cite(t, principles.P8)
	principles.Cite(t, principles.P13)
	requiresIdentifiedPeer(t)
	quietGit(t)
	const (
		summary = "The importer retries on a closed connection."
		ask     = "Should the old retry budget be kept?"
		note    = "Empty input returns an empty tree."
		evLabel = "retry log"
		evPath  = "notes/retry.log"
	)
	report := `{"summary": "` + summary + `", "findings": [` +
		`{"severity": "warning", "action": "ask", "description": "` + ask + `"}, ` +
		`{"severity": "info", "action": "note", "description": "` + note + `"}], ` +
		`"evidence": [{"label": "` + evLabel + `", "path": "` + evPath + `"}]}`

	h := newHome(t)
	subject := newSubject(t)
	if err := h.Create(); err != nil {
		t.Fatal(err)
	}
	agent, route, stop := serveRoutedStop(t, h)
	if got := run(t, h, subject, "init"); got.code != machine.ExitOK {
		t.Fatalf("assistant init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	repository := repositoryOf(t, h, subject)

	created := routedInvestigation(t, h, subject, repository, "importer", "")
	id := created.Record.ID
	waitSessions(t, h, id, 1)
	commands := routeCommands(t, workerCallIn(t, h, agent, id).Lines[0], route)
	fileReport(t, h, subject, id, commands["report"], report)

	before := taskOf(t, h, subject, id)
	if before.State.State != taskstate.Held || before.State.Source != taskstate.SourceHold || !before.State.Basis.ReportStored {
		t.Fatalf("after filing the task reads %+v, want Held from its report hold with a stored report", before.State)
	}
	requireInvestigationReport(t, before, id, h, summary, ask, note, evLabel, evPath)

	// While a report decision is open the hold tier outranks Reported, so
	// the person-facing line says how to answer and points at
	// assistant tasks --json for the identifier; it never relays the report.
	plainHeld := run(t, h, subject, "tasks", id)
	if plainHeld.code != machine.ExitOK {
		t.Fatalf("assistant tasks %s exited %s:\n%s%s", id, plainHeld.code, plainHeld.stdout, plainHeld.stderr)
	}
	if !strings.Contains(plainHeld.stdout, "assistant tasks --json") {
		t.Fatalf("the person-facing hold line does not point at the structured answer:\n%s", plainHeld.stdout)
	}
	for _, leaked := range []string{summary, ask, note, evPath} {
		if strings.Contains(plainHeld.stdout, leaked) {
			t.Fatalf("the person-facing line relays %q:\n%s", leaked, plainHeld.stdout)
		}
	}
	vocabtest.Assert(t, plainHeld.stdout)

	holdsBefore := openHoldsOf(t, h, id)
	if len(holdsBefore) != 1 || holdsBefore[0].Subject != ask {
		t.Fatalf("open holds before restart = %+v, want the one ask", holdsBefore)
	}
	bodyBefore := storedReportBody(t, h, id)

	stop()
	assertHomeFree(t, h)

	_, _, _ = serveRoutedStop(t, h)
	after := taskOf(t, h, subject, id)
	if after.State.State != taskstate.Held || after.State.Source != taskstate.SourceHold || !after.State.Basis.ReportStored {
		t.Fatalf("after reopen the task reads %+v, want Held from its report hold with a stored report", after.State)
	}
	requireInvestigationReport(t, after, id, h, summary, ask, note, evLabel, evPath)
	if after.Report.RecordedAt != before.Report.RecordedAt {
		t.Fatalf("recorded_at moved across restart: before %v after %v", before.Report.RecordedAt, after.Report.RecordedAt)
	}
	holdsAfter := openHoldsOf(t, h, id)
	if !sameHoldKeys(holdsBefore, holdsAfter) {
		t.Fatalf("holds after restart = %+v, want %+v", holdsAfter, holdsBefore)
	}
	if bodyAfter := storedReportBody(t, h, id); bodyAfter != bodyBefore {
		t.Fatalf("stored report body changed across restart:\nbefore: %s\nafter: %s", bodyBefore, bodyAfter)
	}

	// Once the report's decision is answered the task reads Reported and
	// the person-facing line points at --json for the report itself.
	answered := run(t, h, subject, "--json", "--task", id, "--answer", "approved")
	if answered.code != machine.ExitOK {
		t.Fatalf("answering the report hold exited %s:\n%s%s", answered.code, answered.stdout, answered.stderr)
	}
	reported := taskOf(t, h, subject, id)
	if reported.State.State != taskstate.Reported || reported.State.Source != taskstate.SourceReport {
		t.Fatalf("after the answer the task reads %+v, want Reported from its report", reported.State)
	}
	requireInvestigationReport(t, reported, id, h, summary, ask, note, evLabel, evPath)
	plainReported := run(t, h, subject, "tasks", id)
	if plainReported.code != machine.ExitOK {
		t.Fatalf("assistant tasks %s exited %s:\n%s%s", id, plainReported.code, plainReported.stdout, plainReported.stderr)
	}
	if !strings.Contains(plainReported.stdout, "assistant --json tasks") {
		t.Fatalf("the person-facing reported line does not point at the structured report:\n%s", plainReported.stdout)
	}
	for _, leaked := range []string{summary, ask, note, evPath} {
		if strings.Contains(plainReported.stdout, leaked) {
			t.Fatalf("the person-facing reported line relays %q:\n%s", leaked, plainReported.stdout)
		}
	}
	vocabtest.Assert(t, plainReported.stdout)
}

// GAP-04: a worker cannot promote through the coordinator client. The
// investigation files nothing; the refusal is decided from the caller's
// credentials before Promote runs, so no promoted entry is written.
func TestAnInvestigationWorkerCannotPromoteThroughTheCoordinatorClient(t *testing.T) {
	requiresIdentifiedPeer(t)
	quietGit(t)
	h := newHome(t)
	subject := newSubject(t)
	if err := h.Create(); err != nil {
		t.Fatal(err)
	}
	serveCoordinated(t, h)
	if got := run(t, h, subject, "init"); got.code != machine.ExitOK {
		t.Fatalf("assistant init exited %s:\n%s%s", got.code, got.stdout, got.stderr)
	}
	repository := repositoryOf(t, h, subject)

	var created machine.TaskCreated
	coordinate(t, h, subject, &created, "create", "--shape", "investigation", "--project", "look",
		"--instructions", "Look into the slow build.", "--repository", repository)
	id := created.Record.ID
	waitSessions(t, h, id, 1)
	group := workerGroup(t, h, id)

	got := coordinateAs(t, group, h, subject, "promote", id,
		"--repository", repository, "--branch", "task/promoted", "--intent", "Deliver the fix.")
	if got.code != machine.ExitFailure {
		t.Fatalf("promote from the investigation's worker exited %s, want failure:\n%s%s", got.code, got.stdout, got.stderr)
	}
	var failure machine.Failure
	if err := json.Unmarshal([]byte(got.stdout), &failure); err != nil {
		t.Fatalf("promote from the worker wrote no failure document: %v\n%s", err, got.stdout)
	}
	if failure.Code != string(ipc.CodeWorker) || !strings.Contains(failure.NextAction, "worker group") {
		t.Fatalf("promote from the worker was refused %+v, want the worker code", failure)
	}
	if n := historyKinds(t, h, id)["promoted"]; n != 0 {
		t.Fatalf("the refused self-promotion left %d promoted entries", n)
	}
	records := openStore(t, h)
	task, err := records.Task(t.Context(), id)
	_ = records.Close()
	if err != nil {
		t.Fatal(err)
	}
	if task.Shape != store.TaskInvestigation {
		t.Fatalf("after the refusal the task reads shape %s, want investigation", task.Shape)
	}
}

// serveRoutedStop is serveRouted with the stop function returned so a test
// can close the service and reopen it under a fresh Serve.
func serveRoutedStop(t *testing.T, h *home.Home) (*standin.Agent, string, func()) {
	t.Helper()
	agent := standin.New(t, standin.Script{
		Steps: []standin.Step{{Match: standin.MatchReview(), Times: standin.Always, Reply: standin.Reviewed("the change reads cleanly")}},
		Worker: &standin.WorkerScript{
			Exchanges: []standin.Exchange{
				{Contains: writeMarker, Write: true},
				{Contains: runMarker, Run: true},
				{Contains: endWorker, Reply: "worker finished", Ends: true},
			},
		},
	}, agents.WithTerminationGrace(2*time.Second))
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	route := cli.WorkerRoute(executable, h.Root())
	stop := serveOptions(t, service.Options{
		Home: h, NewStages: stages.All, NewFixer: stages.Fix,
		Catalog:     agentCatalog(agent.Runner()),
		WorkerRoute: route,
	})
	return agent, route, stop
}

func requireInvestigationReport(t *testing.T, task machine.Task, id string, h *home.Home, summary, ask, note, evLabel, evPath string) {
	t.Helper()
	if task.Report == nil {
		t.Fatal("task.get carries no report")
	}
	if task.Report.Summary != summary {
		t.Fatalf("report.summary = %q, want %q", task.Report.Summary, summary)
	}
	if len(task.Report.Findings) != 2 {
		t.Fatalf("report.findings = %+v, want the ask and the note", task.Report.Findings)
	}
	if task.Report.Findings[0].Description != ask || task.Report.Findings[0].Action != findings.ActionAsk {
		t.Fatalf("first finding = %+v, want the ask", task.Report.Findings[0])
	}
	if task.Report.Findings[1].Description != note || task.Report.Findings[1].Action != findings.ActionNote {
		t.Fatalf("second finding = %+v, want the note", task.Report.Findings[1])
	}
	if len(task.Report.Evidence) != 1 || task.Report.Evidence[0].Label != evLabel || task.Report.Evidence[0].Path != evPath {
		t.Fatalf("report.evidence = %+v, want %s at %s", task.Report.Evidence, evLabel, evPath)
	}
	wantFile, err := h.TaskReport(id)
	if err != nil {
		t.Fatal(err)
	}
	if task.Report.EvidenceFile != wantFile {
		t.Fatalf("report.evidence_file = %q, want %q", task.Report.EvidenceFile, wantFile)
	}
	if task.Report.RecordedAt.IsZero() {
		t.Fatal("report.recorded_at is zero")
	}
}

func storedReportBody(t *testing.T, h *home.Home, id string) string {
	t.Helper()
	records := openStore(t, h)
	defer func() { _ = records.Close() }()
	stored, err := records.TaskReport(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return stored.Body
}

func sameHoldKeys(a, b []store.Hold) bool {
	if len(a) != len(b) {
		return false
	}
	keys := func(holds []store.Hold) []string {
		out := make([]string, len(holds))
		for i, h := range holds {
			out[i] = h.Key + "\x00" + h.Subject + "\x00" + h.Detail
		}
		return out
	}
	return reflect.DeepEqual(keys(a), keys(b))
}

func assertHomeFree(t *testing.T, h *home.Home) {
	t.Helper()
	held, err := h.Acquire(t.Context(), 0)
	if err != nil {
		t.Fatalf("the home is still held, so the previous service did not close: %v", err)
	}
	if err := held.Release(); err != nil {
		t.Fatalf("releasing the home lock: %v", err)
	}
}
