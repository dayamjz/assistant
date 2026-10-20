package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// ReportReadyEventKind is the kind of the event StoreTaskReport appends in
// the transaction that stores a task's report, so the history entry that
// says a report is ready and the stored report are one write and cannot
// disagree (P8). Its detail is empty: the report is the task_report row, and
// the history names that it exists rather than carrying a second copy. As
// with OpeningEventKind, the store records the word and internal/taskstate's
// table is what gives it a meaning.
const ReportReadyEventKind = "report-ready"

// TaskReport is PRD section 11's stored report: what an investigation task's
// worker filed, kept here so it survives the copy's cleanup. It is written
// once, by StoreTaskReport, in the same transaction as the holds registered
// from it and the ReportReadyEventKind entry that records it in the task's
// history, and never revised.
//
// Body is the worker's text as it arrived and Report is the same text as the
// writer parsed it; both are evidence on PRD principle P13's terms, never
// text to relay, and this package reads neither: Report is opaque bytes here
// for the reason a finding set is, that internal/findings owns the
// vocabulary. HoldKeys is the one fact this package does hold about the
// report's contents, the keys of the holds the writer registered from it,
// so the rule PRD section 11 has cleanup verify, that every decision the
// report surfaced has a hold row, is answerable from this row and the hold
// table alone.
type TaskReport struct {
	// TaskID names the task.
	TaskID string `json:"task_id"`
	// Body is the worker's report as it arrived.
	Body string `json:"body"`
	// Report is the parsed report, encoded by the writer. It is opaque here.
	Report []byte `json:"report"`
	// HoldKeys is the keys of the holds the writer registered from the
	// report, in the order it named them. An empty list is a known fact: a
	// report that surfaced no decision.
	HoldKeys []string `json:"hold_keys"`
	// RecordedAt is when the row was written.
	RecordedAt time.Time `json:"recorded_at"`
}

// StoreTaskReport writes a task's report, registers the holds the writer
// derived from it, and appends a ReportReadyEventKind entry to the task's
// history, in one transaction, and returns the report as stored and the
// entry as appended.
// The holds are registered on RegisterHold's terms, so a key already
// registered is left as it stands, resolved or not; every hold must name
// the report's task and nothing else is checked about it, because what a
// hold says is the writer's. The report's HoldKeys is written from the holds
// handed in, in their order, so a stored report whose decisions were never
// registered cannot exist through this package: a failure to register any
// of them rolls the report back with it.
//
// It refuses a task that already has a report with ErrTaskReportStored and
// leaves the first standing, with nothing appended, a task that does not
// exist with ErrNotFound, wrapped, an empty body, an empty parsed report, and
// a hold naming no key, no subject, or another task. RecordedAt is assigned
// here.
//
// The entry is appended after whatever the history already holds, anchored
// to nothing: a report is filed once per task, which the refusal above
// enforces, so there is no decision an anchor would protect.
func (s *Store) StoreTaskReport(ctx context.Context, r TaskReport, holds []Hold) (TaskReport, TaskEvent, error) {
	if strings.TrimSpace(r.TaskID) == "" {
		return TaskReport{}, TaskEvent{}, fmt.Errorf("store: a task report names no task")
	}
	if strings.TrimSpace(r.Body) == "" {
		return TaskReport{}, TaskEvent{}, fmt.Errorf("store: task %s: the report body is empty", r.TaskID)
	}
	if len(r.Report) == 0 {
		return TaskReport{}, TaskEvent{}, fmt.Errorf("store: task %s: the parsed report is empty", r.TaskID)
	}
	keys := make([]string, 0, len(holds))
	for _, h := range holds {
		switch {
		case strings.TrimSpace(h.Key) == "":
			return TaskReport{}, TaskEvent{}, fmt.Errorf("store: task %s: a hold has no key", r.TaskID)
		case strings.TrimSpace(h.Subject) == "":
			return TaskReport{}, TaskEvent{}, fmt.Errorf("store: task %s: hold %s has no subject", r.TaskID, h.Key)
		case h.TaskID.Or("") != r.TaskID:
			return TaskReport{}, TaskEvent{}, fmt.Errorf("store: task %s: hold %s names task %q rather than this one", r.TaskID, h.Key, h.TaskID.Or(""))
		}
		keys = append(keys, h.Key)
	}
	encodedKeys, err := json.Marshal(keys)
	if err != nil {
		return TaskReport{}, TaskEvent{}, fmt.Errorf("store: task %s: encoding the hold keys: %w", r.TaskID, err)
	}
	now := encodeTime(nowUTC())
	var event TaskEvent
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		if err := taskExists(ctx, tx, r.TaskID); err != nil {
			return err
		}
		var one int
		switch err := tx.QueryRowContext(ctx, `SELECT 1 FROM task_report WHERE task_id = ?`, r.TaskID).Scan(&one); {
		case err == nil:
			return ErrTaskReportStored
		case !isNoRows(err):
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO task_report (task_id, body, report, hold_keys, recorded_at) VALUES (?, ?, ?, ?, ?)`,
			r.TaskID, r.Body, string(r.Report), string(encodedKeys), now); err != nil {
			return err
		}
		for _, h := range holds {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO hold (key, run_id, task_id, subject, detail, opened_at, resolution, resolved_at, resolved_by)
				VALUES (?, ?, ?, ?, ?, ?, NULL, NULL, NULL)
				ON CONFLICT(key) DO NOTHING`,
				h.Key, h.RunID, h.TaskID, h.Subject, h.Detail, now); err != nil {
				return fmt.Errorf("registering hold %s: %w", h.Key, err)
			}
		}
		event, err = appendTaskEventTx(ctx, tx, r.TaskID, ReportReadyEventKind, "", Unknown[int64]())
		return err
	})
	if err != nil {
		return TaskReport{}, TaskEvent{}, fmt.Errorf("store: storing the report of task %s: %w", r.TaskID, err)
	}
	stored, err := s.TaskReport(ctx, r.TaskID)
	if err != nil {
		return TaskReport{}, TaskEvent{}, err
	}
	return stored, event, nil
}

// TaskReport returns a task's stored report, or ErrNotFound when none has
// been stored, which is the state of every task whose worker has not filed
// one.
func (s *Store) TaskReport(ctx context.Context, taskID string) (TaskReport, error) {
	if err := s.live(); err != nil {
		return TaskReport{}, err
	}
	var r TaskReport
	var report, keys, recorded string
	err := s.read.QueryRowContext(ctx, `SELECT task_id, body, report, hold_keys, recorded_at FROM task_report WHERE task_id = ?`,
		taskID).Scan(&r.TaskID, &r.Body, &report, &keys, &recorded)
	if err != nil {
		return TaskReport{}, fmt.Errorf("store: reading the report of task %s: %w", taskID, errNoRows(err))
	}
	r.Report = []byte(report)
	if err := json.Unmarshal([]byte(keys), &r.HoldKeys); err != nil {
		return TaskReport{}, fmt.Errorf("store: reading the report of task %s: column hold_keys: %w", taskID, err)
	}
	if r.HoldKeys == nil {
		r.HoldKeys = []string{}
	}
	if r.RecordedAt, err = decodeTime(recorded); err != nil {
		return TaskReport{}, fmt.Errorf("store: reading the report of task %s: %w", taskID, err)
	}
	return r, nil
}

// ForgetHoldForTest deletes one hold row. It is a counterfeit builder and
// nothing else: no production path deletes a hold, because PRD section 8
// closes one only by an explicit resolution, and StoreTaskReport registers a
// report's holds in the transaction that stores it, so a stored report with
// a decision no hold row names cannot be produced through this package. The
// rule PRD section 11 has cleanup verify therefore has a refusal case nothing
// here can reach, and internal/tasks builds that case the way internal/journey
// builds a counterfeit, by mutating a real observation: the real stored
// report with one of its hold rows deleted here, with the unmutated state as
// the positive control.
//
// It takes a testing.TB and refuses a nil one, so a production path does
// not reach it by accident; that does not stop a deliberate caller that
// builds a testing.T of its own. A key with no row is reported as
// ErrNotFound.
func (s *Store) ForgetHoldForTest(ctx context.Context, tb testing.TB, key string) error {
	if tb == nil {
		return fmt.Errorf("store: ForgetHoldForTest is a test's counterfeit builder and takes the test")
	}
	tb.Helper()
	return s.inTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `DELETE FROM hold WHERE key = ?`, key)
		if err != nil {
			return fmt.Errorf("store: forgetting hold %s: %w", key, err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("store: forgetting hold %s: %w", key, err)
		}
		if affected == 0 {
			return fmt.Errorf("store: forgetting hold %s: %w", key, ErrNotFound)
		}
		return nil
	})
}
