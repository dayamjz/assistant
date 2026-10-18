package store

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"time"
)

// PullRequestEnd says which state of a run's pull request ended the run, when
// one did. PRD section 5's "Waiting on checks" has two: a pull request read as
// merged completes the run without validating it, and one read as closed
// without merging fails it. It is the vocabulary of the merge record, the one
// row PRD section 8's data model has the code-host read that ends a run write,
// and nothing else stores it: the run's own row carries the status the record
// goes with and not the end again.
//
// It is the record's vocabulary and not the code host's. internal/forge says
// where a pull request stands, in its own closed set, and the one place that
// reads that set decides whether what it read is one of these two; this type
// holds only what a run can be ended by, so a value here is never "open".
//
// The zero value names no end. It is not a member of the set: a transition
// asked to record it refuses, so the table never holds an empty word standing
// in for nothing.
type PullRequestEnd string

const (
	// PullRequestMerged is the run's pull request was read as merged. The run
	// is complete and not newly validated: its checks were not read to a
	// verdict, and the rule that only a no_ci declaration makes an empty
	// check list green was never applied.
	PullRequestMerged PullRequestEnd = "merged"
	// PullRequestClosed is the run's pull request was read as closed without
	// being merged, which ends the run as failed.
	PullRequestClosed PullRequestEnd = "closed"
)

// pullRequestEnds is the closed set, in the order PullRequestEnds reports.
var pullRequestEnds = []PullRequestEnd{PullRequestMerged, PullRequestClosed}

// PullRequestEnds returns the closed set. The result is a copy, so a caller
// cannot add to the set by writing to it.
func PullRequestEnds() []PullRequestEnd { return slices.Clone(pullRequestEnds) }

// Recognized reports whether e is one of the two ends this package defines.
func (e PullRequestEnd) Recognized() bool { return slices.Contains(pullRequestEnds, e) }

// String renders the end as it is stored.
func (e PullRequestEnd) String() string { return string(e) }

// Reason is the one sentence a record gives for a run this end ended, in the
// words PRD section 5 fixes for it: a run failed by its pull request closing
// is recorded with the reason "pull request closed without merging". It is
// rendered from the stored end rather than stored beside it, so the record and
// the sentence cannot disagree. An end outside the set renders as nothing.
func (e PullRequestEnd) Reason() string {
	switch e {
	case PullRequestMerged:
		return "pull request merged"
	case PullRequestClosed:
		return "pull request closed without merging"
	default:
		return ""
	}
}

// PullRequestRead is what the code-host read that ends a run reported: the
// pull request it read, the head it stood at, and which end its state was. It
// is the input to TransitionRunOnPullRequest, and MergeRecord is what that
// write leaves behind.
type PullRequestRead struct {
	// PullRequest is the pull request's number on the code host.
	PullRequest int
	// Head is the commit the pull request's head stood at when it was read,
	// as the provider reported it. It may be empty when the provider did not
	// say; the record then carries that it did not.
	Head string
	// End is which state ended the run.
	End PullRequestEnd
}

// MergeRecord is PRD section 8's merge record: the program's own record that
// the code-host read which ended a run saw the run's pull request merged, or,
// on the same row and in the same vocabulary, closed without a merge. It is
// written once, by the transaction that moves the run's status, and never
// updated or deleted; the table is sealed against both.
//
// The code host owns the merge. This records that it was seen: which pull
// request, the head it was read at, which end, and when. The run's passed on
// a merge is derived from this row, and so is a task's Landed where that is
// resolved; neither stores the fact again, which is why the run's own row has
// no column for it and Store.MergeRecord is the one read.
type MergeRecord struct {
	// Run is the run the read ended.
	Run string `json:"run"`
	// PullRequest is the pull request's number on the code host.
	PullRequest int `json:"pull_request"`
	// Head is the commit the pull request's head stood at when it was read.
	Head string `json:"head"`
	// End is which state ended the run.
	End PullRequestEnd `json:"end"`
	// RecordedAt is when the row was written, which is when the run moved.
	RecordedAt time.Time `json:"recorded_at"`
}

// HoldClosure is a hold to close in the same transaction as the run's move,
// for the one closure PRD section 8 lets the program make: the hold the
// checks stage raised when checks_timeout elapsed, closed by the service on
// the pull request's state. The zero value closes nothing.
//
// The hold is registered if it is not there, on RegisterHold's terms, and then
// resolved on ResolveHold's. A hold already carrying a resolution is left as
// it stands rather than refusing the transaction: the run's move is the fact
// the transaction exists for, and a resolution already written is what a
// transaction that committed the resolution and nothing after it would have
// left, were the two ever separate. Every other refusal rolls the whole
// transaction back, so no row ever says the program resolved a hold on a run
// the program did not move.
type HoldClosure struct {
	// Hold is the hold to register and resolve, with at least a key and a
	// subject.
	Hold Hold
	// Resolution is what was decided, which ResolveHold refuses empty.
	Resolution string
	// Note is free text recorded with the resolution about what was
	// decided. Empty leaves ResolutionNote unknown. It is not who decided:
	// that is By. Non-empty text is redacted before it is stored.
	Note string
	// By is who decided, which ResolveHold refuses as the zero Resolver.
	By Resolver
}

// MergeRecord returns the run's merge record and whether there is one. A run
// its pull request did not end has none, and that is an answer rather than a
// failure; every other failure to read is an error.
func (s *Store) MergeRecord(ctx context.Context, run string) (MergeRecord, bool, error) {
	if err := s.live(); err != nil {
		return MergeRecord{}, false, err
	}
	row := s.read.QueryRowContext(ctx, mergeRecordColumns+` FROM merge_record WHERE run = ?`, run)
	record, err := scanMergeRecord(row)
	if isNoRows(err) {
		return MergeRecord{}, false, nil
	}
	if err != nil {
		return MergeRecord{}, false, fmt.Errorf("store: reading the merge record of run %s: %w", run, err)
	}
	return record, true, nil
}

// TransitionRunOnPullRequest is TransitionRun for a run its pull request's
// state ended: it moves the run to a status on TransitionRun's terms and
// writes the run's merge record from read, in one transaction, so a reader
// never finds a run that moved with no record of what ended it or a record on
// a run that did not move. With a non-zero closing it also closes the hold the
// run was waiting on, in the same transaction, on HoldClosure's terms.
//
// It refuses an end outside the closed set, and the zero end, with
// ErrUnknownPullRequestEnd, and a pull request number below one, before it
// touches the run. A run already in the status it is being moved to is left as
// it stands, record included, on TransitionRun's terms: nothing is written and
// no error is returned, so a caller retrying after a commit it did not see
// reports what that commit established rather than writing a second record,
// which the table would refuse in any case.
//
// What a status and an end together mean is not decided here. internal/runs
// says which statuses a pull request's state may end a run in and which end
// goes with which; this package records the pair it is handed.
func (s *Store) TransitionRunOnPullRequest(ctx context.Context, id string, from []RunStatus, to RunStatus,
	read PullRequestRead, closing HoldClosure) (Run, error) {
	if !read.End.Recognized() {
		return Run{}, fmt.Errorf("store: run %s: %w: %q", id, ErrUnknownPullRequestEnd, read.End)
	}
	if read.PullRequest < 1 {
		return Run{}, fmt.Errorf("store: run %s: pull request %d is not a positive number", id, read.PullRequest)
	}
	if closing.Hold.Key != "" {
		if strings.TrimSpace(closing.Hold.Subject) == "" {
			return Run{}, fmt.Errorf("store: hold %s has no subject", closing.Hold.Key)
		}
		if strings.TrimSpace(closing.Resolution) == "" {
			return Run{}, fmt.Errorf("%w: hold %s", ErrNoResolution, closing.Hold.Key)
		}
		if _, err := resolverText(closing.By); err != nil {
			return Run{}, fmt.Errorf("store: resolving hold %s: %w", closing.Hold.Key, err)
		}
	}
	return s.transitionRun(ctx, id, from, to, func(tx *sql.Tx, now string) error {
		if closing.Hold.Key != "" {
			if err := s.closeHold(ctx, tx, closing, now); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO merge_record (run, pull_request, head, pull_request_end, recorded_at)
			VALUES (?, ?, ?, ?, ?)`,
			id, read.PullRequest, read.Head, string(read.End), now); err != nil {
			return fmt.Errorf("store: recording what ended run %s: %w", id, err)
		}
		return nil
	})
}

// closeHold registers and resolves a hold inside a transaction, on
// HoldClosure's terms. Non-empty note text passes through the redactor Open
// was given before it is stored.
func (s *Store) closeHold(ctx context.Context, tx *sql.Tx, closing HoldClosure, now string) error {
	h := closing.Hold
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO hold (key, run_id, task_id, subject, detail, opened_at, resolution, resolved_at, resolved_by)
		VALUES (?, ?, ?, ?, ?, ?, NULL, NULL, NULL)
		ON CONFLICT(key) DO NOTHING`,
		h.Key, h.RunID, h.TaskID, h.Subject, h.Detail, now); err != nil {
		return fmt.Errorf("store: registering hold %s: %w", h.Key, err)
	}
	resolver, err := resolverText(closing.By)
	if err != nil {
		return fmt.Errorf("store: resolving hold %s: %w", h.Key, err)
	}
	var note any
	if strings.TrimSpace(closing.Note) != "" {
		note = s.redact.Redact(closing.Note)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE hold SET resolution = ?, resolved_at = ?, resolved_by = ?, resolution_note = ? WHERE key = ? AND resolution IS NULL`,
		closing.Resolution, now, resolver, note, h.Key); err != nil {
		return fmt.Errorf("store: resolving hold %s: %w", h.Key, err)
	}
	return nil
}

const mergeRecordColumns = `SELECT run, pull_request, head, pull_request_end, recorded_at`

func scanMergeRecord(sc scanner) (MergeRecord, error) {
	var r MergeRecord
	var end, recorded string
	if err := sc.Scan(&r.Run, &r.PullRequest, &r.Head, &end, &recorded); err != nil {
		return MergeRecord{}, err
	}
	r.End = PullRequestEnd(end)
	if !r.End.Recognized() {
		// A row outside the set was not written by this package's accessor,
		// and reading it back as some other end, or as no record, would turn
		// a row nobody can account for into a fact about how the run ended.
		return MergeRecord{}, fmt.Errorf("column pull_request_end: %w: %q", ErrUnknownPullRequestEnd, end)
	}
	var err error
	if r.RecordedAt, err = decodeTime(recorded); err != nil {
		return MergeRecord{}, err
	}
	return r, nil
}

// RecordMergeOfPassedRun writes the merge record of a run that had already
// passed when its pull request was read as merged: the code-host read that
// follows a merge the program made under the autonomy flag, PRD section 6,
// which writes the record "as it does for a merge you made", and the read of
// a merge a person made after the run passed. The run's status
// does not move, because a run that passed has its verdict; what the record
// adds is the end the run's passed is then read with, which is why it is a
// row beside the run and the run row gains nothing.
//
// It refuses an end other than PullRequestMerged with
// ErrUnknownPullRequestEnd, because a closed pull request does not fail a run
// that already passed, and a pull request number below one, before anything
// is read. In the transaction that writes the row it refuses a run that is
// not passed with a *RunStatusError and a run that already has a merge record
// with ErrMergeRecorded, so the record is written once whichever read got
// there first.
func (s *Store) RecordMergeOfPassedRun(ctx context.Context, id string, read PullRequestRead) (MergeRecord, error) {
	if read.End != PullRequestMerged {
		return MergeRecord{}, fmt.Errorf("store: run %s: %w: %q is not a merge", id, ErrUnknownPullRequestEnd, read.End)
	}
	if read.PullRequest < 1 {
		return MergeRecord{}, fmt.Errorf("store: run %s: pull request %d is not a positive number", id, read.PullRequest)
	}
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		current, err := scanRun(tx.QueryRowContext(ctx, runColumns+` FROM run WHERE id = ?`, id))
		if err != nil {
			return fmt.Errorf("store: reading run %s: %w", id, errNoRows(err))
		}
		if current.Status != RunPassed {
			return &RunStatusError{Run: id, Expected: []RunStatus{RunPassed}, Actual: current.Status, To: RunPassed}
		}
		var one int
		switch err := tx.QueryRowContext(ctx, `SELECT 1 FROM merge_record WHERE run = ?`, id).Scan(&one); {
		case err == nil:
			return fmt.Errorf("%w: run %s", ErrMergeRecorded, id)
		case !isNoRows(err):
			return err
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO merge_record (run, pull_request, head, pull_request_end, recorded_at)
			VALUES (?, ?, ?, ?, ?)`,
			id, read.PullRequest, read.Head, string(read.End), encodeTime(nowUTC()))
		return err
	})
	if err != nil {
		return MergeRecord{}, err
	}
	record, _, err := s.MergeRecord(ctx, id)
	return record, err
}
