package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
)

// PRD section 11, "Recovering an investigation": the report survives
// cleanup, every decision it surfaced is registered as a durable hold
// before the task can be treated as complete, and cleanup verifies that
// registration. This file is the storing half; cleanup.go's verifyReport is
// the verifying half.
//
// The report is a store row with an evidence copy. The row is the source:
// store.StoreTaskReport writes it in one transaction with the holds derived
// here, so a stored report whose decisions were never registered cannot
// exist through the product, and P8 resolves a fact from a record rather
// than from a file. The copy under home.TaskReport is for a person to open
// and nothing reads it.
//
// The report is parsed with findings.ParseReport, so P3 applies: a finding
// whose action is missing, empty or unrecognized is an ask and becomes a
// hold. A finding whose action is fix is recorded as evidence in the stored
// report and reaches no fixer: PRD section 5 says a report is evidence, not
// authorization, nothing here selects on findings.Fixable, and the Cleaner
// holds no route to an agent, which a test walks its type to hold. The hold
// a finding becomes keeps the worker's words as its subject and detail,
// because PRD principle P13 makes a report evidence to read and never text
// to relay: the row holds the evidence, and the surface that shows a person
// the hold translates it (#110) rather than printing it.
//
// The worker's words are kept with the credentials in them removed, and
// StoreReport is where that happens, because it is the one point every
// filing passes before anything of the report is stored: internal/service's
// fileReport reaches the store only through it, and no other production code
// calls store.StoreTaskReport. internal/redact decides what a credential is,
// and this file only decides where the call is made. The removal runs inside
// the parse, through findings.ParseReportThrough, after the report is decoded
// and before it is normalized, so every hold's subject and detail, every
// derived finding identifier and so every hold key, and the parsed report are
// derived from the redacted text. The body and the evidence copy are the filed
// text with the same remover run over it whole, and a refusal's text passes it
// too, so a refusal quoting the filed report quotes it redacted.
//
// Redacting the filed text first and then parsing it would be shorter and
// wrong: the remover takes an Authorization value to the end of its line,
// which in a report written on one line takes the closing quote and braces
// with it, and the report would then be refused, or an earlier object in the
// text read in its place.
//
// What that does not cover is named rather than implied. A report stored
// before this removal ran is kept as it was filed, because a stored report is
// written once and never revised. A body redacted whole is evidence for a
// person to read and nothing parses it again, so where an Authorization value
// stood it may no longer be valid JSON. A credential in a shape
// internal/redact does not recognize is stored as written, which is that
// package's stated gap. And the text a worker sends travels to the service
// whole before it reaches this point; this removes it from what is stored and
// answered, not from the request in flight.

// ErrReportUnreadable matches a report findings.ParseReport refused.
// Nothing is stored and no hold is registered, because a report this
// package cannot read is one whose decisions it cannot register.
var ErrReportUnreadable = errors.New("tasks: the report could not be read")

// ErrNotInvestigation reports a report filed for a task that is not an
// investigation task. A delivery task's deliverable is a change and its
// decisions are its run's holds; a report filed for one is refused rather
// than stored as evidence of nothing.
var ErrNotInvestigation = errors.New("tasks: only an investigation task files a report")

// StoredReport is what StoreReport recorded.
type StoredReport struct {
	// Record is the row as stored.
	Record store.TaskReport
	// Report is the report as parsed, normalized and validated.
	Report findings.Report
	// Holds are the holds registered from the report's ask findings, in the
	// report's order, as stored. A hold already registered under its key is
	// the existing one.
	Holds []store.Hold
	// Event is the store.ReportReadyEventKind entry the store appended to
	// the task's history in the same transaction as Record. It claims
	// Reported at internal/taskstate's event tier; whether that claim is
	// the answer is the resolver's precedence, which a live or dead
	// terminal outranks.
	Event store.TaskEvent
	// Evidence is the path of the evidence copy.
	Evidence string
}

// reportRedactor is the remover a filed report passes through before any of
// it is stored. It is a package variable rather than a dependency the Cleaner
// is handed, so no Cleaner can store a report without it.
var reportRedactor = redact.New()

// redactedError is a refusal whose text has passed reportRedactor, so a
// refusal quoting the filed report quotes it redacted. It still unwraps to
// the refusal, so a caller asking which one it was is answered.
type redactedError struct{ err error }

func (e redactedError) Error() string { return reportRedactor.Redact(e.err.Error()) }
func (e redactedError) Unwrap() error { return e.err }

// HoldKey is the key the hold for one of a task's report findings is
// registered under. It is derived from the finding's identifier, which
// internal/findings derives from the finding's content, so storing the same
// report twice names the same holds, which is what makes registration
// idempotent under store.RegisterHold.
func HoldKey(taskID, findingID string) string { return "task/" + taskID + "/finding/" + findingID }

// StoreReport parses raw as the task's report, stores it with the holds its
// ask findings become and the history entry that records it, writes the
// evidence copy, and returns what it recorded. The task must be an investigation task; a report the parser
// refuses is ErrReportUnreadable, wrapped, and a task that already has one
// is store.ErrTaskReportStored, wrapped, with the first report standing.
// The report's text, in what it stores, writes and returns and in a refusal,
// passes internal/redact first, on the terms this file's opening comment
// states.
//
// The evidence copy is written after the row, so a crash between the two
// leaves a stored report with no copy; the row is the source and nothing
// reads the copy, so the next person to open the task directory is the one
// who notices. The reverse cannot happen.
func (c *Cleaner) StoreReport(ctx context.Context, taskID, raw string) (StoredReport, error) {
	task, err := c.store.Task(ctx, taskID)
	if err != nil {
		return StoredReport{}, err
	}
	if task.Shape != store.TaskInvestigation {
		return StoredReport{}, fmt.Errorf("%w: task %s is a %s task", ErrNotInvestigation, taskID, task.Shape)
	}
	report, err := findings.ParseReportThrough(raw, reportRedactor.Redact)
	if err != nil {
		return StoredReport{}, fmt.Errorf("%w: %w", ErrReportUnreadable, redactedError{err})
	}
	raw = reportRedactor.Redact(raw)
	encoded, err := json.Marshal(report)
	if err != nil {
		return StoredReport{}, fmt.Errorf("tasks: encoding the report of task %s: %w", taskID, err)
	}
	var holds []store.Hold
	for _, f := range report.Held() {
		detail := f.Description
		if !f.Location.Empty() {
			detail = f.Location.String() + ": " + detail
		}
		holds = append(holds, store.Hold{
			Key:     HoldKey(taskID, f.ID),
			TaskID:  store.Known(taskID),
			Subject: f.Description,
			Detail:  detail,
		})
	}
	record, event, err := c.store.StoreTaskReport(ctx, store.TaskReport{TaskID: taskID, Body: raw, Report: encoded}, holds)
	if err != nil {
		return StoredReport{}, err
	}
	stored := StoredReport{Record: record, Report: report, Event: event}
	for _, h := range holds {
		got, err := c.store.Hold(ctx, h.Key)
		if err != nil {
			return StoredReport{}, err
		}
		stored.Holds = append(stored.Holds, got)
	}
	stored.Evidence, err = c.home.TaskReport(taskID)
	if err != nil {
		return StoredReport{}, err
	}
	if err := os.MkdirAll(filepath.Dir(stored.Evidence), 0o700); err != nil {
		return StoredReport{}, fmt.Errorf("tasks: creating the directory of task %s: %w", taskID, err)
	}
	if err := os.WriteFile(stored.Evidence, []byte(raw), 0o600); err != nil {
		return StoredReport{}, fmt.Errorf("tasks: writing the evidence copy of the report of task %s: %w", taskID, err)
	}
	return stored, nil
}
