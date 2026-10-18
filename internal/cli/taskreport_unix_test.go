//go:build unix

package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
)

// #260: a credential a worker writes into its investigation's report reaches
// neither the stored report, its evidence copy, the holds registered from
// it, nor any answer the surface gives that reads them.
//
// The worker is the shipped Claude Code adapter's, the stand-in in stream
// mode, and it files the report through the real `assistant worker report`
// command its instructions name, from a file, so the report reaches the
// service only through the route and never through the message that asked
// for it. The report is written on one line, the shape in which redacting
// the filed text before parsing it would cut an Authorization value to the
// end of the line, closing braces included, and refuse the report.
//
// The control is the report itself: its other text, and the marker
// internal/redact leaves where each credential stood, arrive in the stored
// report, the evidence copy, every hold and every answer, and the report
// registers holds, so an answer that dropped the report or a filing that
// never arrived cannot pass as one that removed the credentials.
func TestAWorkersReportIsStoredAndAnsweredWithoutItsCredentials(t *testing.T) {
	requiresIdentifiedPeer(t)
	quietGit(t)
	const (
		urlSecret    = "hunter2-url-password"
		bearerSecret = "s3cretBearerToken0123456789"
		visible      = "the importer logs its remote and its request headers"
		summary      = "The importer leaks what it connects with."
	)
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

	var created machine.TaskCreated
	coordinate(t, h, subject, &created, "create", "--shape", "investigation", "--project", "importer",
		"--instructions", "Look into what the importer logs.", "--repository", repository)
	id := created.Record.ID
	waitSessions(t, h, id, 1)
	commands := routeCommands(t, workerCall(t, agent, 1).Lines[0], route)
	command, _, ok := strings.Cut(commands["report"], " --file ")
	if !ok {
		t.Fatalf("the report command reads %q, with no --file to file from", commands["report"])
	}

	// An ask and a finding naming no action, both carrying both shapes, and
	// a summary carrying the URL, on one line.
	credentialed := visible + ": https://deploy:" + urlSecret + "@example.com/o/r.git and " +
		"Authorization: Bearer " + bearerSecret
	report := `{"summary": "` + summary + ` It clones https://deploy:` + urlSecret + `@example.com/o/r.git.", "findings": [` +
		`{"severity": "warning", "action": "ask", "description": "` + credentialed + `"}, ` +
		`{"severity": "warning", "description": "Bearer ` + bearerSecret + ` is in ` + visible + `"}, ` +
		`{"severity": "info", "action": "note", "description": "nothing else"}]}`
	filed := filepath.Join(t.TempDir(), "report.json")
	if err := os.WriteFile(filed, []byte(report), 0o600); err != nil {
		t.Fatal(err)
	}
	messageWorker(t, h, subject, id, runMarker+command+" --file '"+filed+"'")
	waitFor(t, "the report", func() bool {
		records := openStore(t, h)
		defer func() { _ = records.Close() }()
		_, err := records.TaskReport(t.Context(), id)
		return err == nil
	})

	requireRedacted := func(t *testing.T, what, text string, kept ...string) {
		t.Helper()
		for _, secret := range []string{urlSecret, bearerSecret} {
			if strings.Contains(text, secret) {
				t.Fatalf("%s carries the credential %q:\n%s", what, secret, text)
			}
		}
		for _, word := range kept {
			if !strings.Contains(text, word) {
				t.Fatalf("%s does not carry %q, so the report did not arrive as redacted text:\n%s", what, word, text)
			}
		}
	}
	marked := []string{visible, "https://" + redact.Marker + "@example.com", "Bearer " + redact.Marker}

	records := openStore(t, h)
	stored, err := records.TaskReport(t.Context(), id)
	_ = records.Close()
	if err != nil {
		t.Fatal(err)
	}
	requireRedacted(t, "the stored body", stored.Body, append(marked, summary)...)
	requireRedacted(t, "the stored parsed report", string(stored.Report), append(marked, summary)...)
	evidence, err := h.TaskReport(id)
	if err != nil {
		t.Fatal(err)
	}
	copied, err := os.ReadFile(evidence)
	if err != nil {
		t.Fatal(err)
	}
	requireRedacted(t, "the evidence copy", string(copied), append(marked, summary)...)

	holds := openHoldsOf(t, h, id)
	if len(holds) != 2 {
		t.Fatalf("the report registered %d open holds, want the ask and the finding with no action: %+v", len(holds), holds)
	}
	for _, hold := range holds {
		requireRedacted(t, "hold "+hold.Key, hold.Key+"\n"+hold.Subject+"\n"+hold.Detail, visible, redact.Marker)
	}

	task := run(t, h, subject, "--json", "tasks", id)
	if task.code != machine.ExitOK {
		t.Fatalf("reading the task exited %s:\n%s%s", task.code, task.stdout, task.stderr)
	}
	requireRedacted(t, "the task's answer", task.stdout)

	_, documents := catchUpOnce(t, h, subject)
	requireRedacted(t, "watch --once", strings.Join(documents, "\n"), marked...)

	for _, hold := range holds {
		answered := run(t, h, subject, "--json", "--task", id, "--hold", hold.Key, "--answer", "approved")
		if answered.code != machine.ExitOK {
			t.Fatalf("answering the task's hold exited %s:\n%s%s", answered.code, answered.stdout, answered.stderr)
		}
		requireRedacted(t, "the answer to a hold", answered.stdout, visible, redact.Marker)
	}
	if n := historyKinds(t, h, id)[store.ReportReadyEventKind]; n != 1 {
		t.Fatalf("the history records the report %d times, want once", n)
	}
}
