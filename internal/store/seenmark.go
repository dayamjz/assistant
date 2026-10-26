package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// SeenKind is which of the three landed records a seen mark names. PRD
// section 8's seen mark is one row per merge or cleanup record a person was
// shown and marked seen, keyed the way that record is keyed, and a
// nothing-to-deliver record is shown and marked seen on the same terms, so
// the kind is what says which key it is: a merge record and a
// nothing-to-deliver record are keyed by their run and a task's cleanup
// record by its task. The set is closed.
type SeenKind string

const (
	// SeenMerge names a merge record, by the run it ended.
	SeenMerge SeenKind = "merge"
	// SeenCleanup names a task's cleanup record, by the task.
	SeenCleanup SeenKind = "cleanup"
	// SeenNothingToDeliver names a nothing-to-deliver record, by the run
	// that wrote it.
	SeenNothingToDeliver SeenKind = "nothing-to-deliver"
)

var seenKinds = []SeenKind{SeenMerge, SeenCleanup, SeenNothingToDeliver}

// SeenKinds returns the closed set, in the order above. The result is a copy.
func SeenKinds() []SeenKind { return slices.Clone(seenKinds) }

// Recognized reports whether k is one of the kinds this package defines.
func (k SeenKind) Recognized() bool { return slices.Contains(seenKinds, k) }

// SeenRecord names one landed record the way that record is keyed: a merge
// record and a nothing-to-deliver record by their run, a task's cleanup
// record by its task. It is what a
// catch-up read shows a person and what a mark names back.
type SeenRecord struct {
	// Kind is which record it is.
	Kind SeenKind `json:"kind"`
	// Key is that record's own key: the run for a merge record or a
	// nothing-to-deliver record, the task for a cleanup record.
	Key string `json:"key"`
}

// SeenMark is one row of PRD section 8's seen mark: a landed record a person
// was shown and marked seen, and when the mark was made. It is appended,
// written once per record and never revised or removed, and the database
// holds that with triggers. MarkedAt is when the row was written and is
// never what decides whether a record is seen: a record is seen when it has
// a row, so nothing reads the mark as a time.
type SeenMark struct {
	SeenRecord
	// MarkedAt is when the row was written.
	MarkedAt time.Time `json:"marked_at"`
}

// MarkSeen appends a seen mark for each named record, in one transaction,
// and returns the marks as they stand. It is the one writer of the seen mark,
// and it writes only what it is named: nothing here reads which records
// exist and marks them, so a record recorded after the read a caller is
// marking from is not marked by it.
//
// Each record is checked before anything is written. A kind outside the
// closed set is refused with ErrUnknownSeenKind, and a record that is not a
// landed record - no merge record for the run, one that reads closed rather
// than merged, no nothing-to-deliver record for the run, no cleanup record for
// the task, or one whose removal is not recorded yet - with ErrNotLanded, each as a *SeenRecordError naming it, and
// then the whole batch writes nothing. An empty batch is refused. A record
// already marked keeps its first row, and its mark is returned as it was
// first written, so a mark retried after a commit the caller did not see
// changes nothing.
func (s *Store) MarkSeen(ctx context.Context, records []SeenRecord) ([]SeenMark, error) {
	if len(records) == 0 {
		return nil, errors.New("store: a seen mark names no records")
	}
	now := encodeTime(nowUTC())
	out := make([]SeenMark, 0, len(records))
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		for _, r := range records {
			if err := landed(ctx, tx, r); err != nil {
				return err
			}
		}
		for _, r := range records {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO seen_mark (kind, key, marked_at) VALUES (?, ?, ?) ON CONFLICT (kind, key) DO NOTHING`,
				string(r.Kind), r.Key, now); err != nil {
				return fmt.Errorf("store: marking %s %s seen: %w", r.Kind, r.Key, err)
			}
			var marked string
			if err := tx.QueryRowContext(ctx, `SELECT marked_at FROM seen_mark WHERE kind = ? AND key = ?`,
				string(r.Kind), r.Key).Scan(&marked); err != nil {
				return fmt.Errorf("store: reading the seen mark of %s %s: %w", r.Kind, r.Key, err)
			}
			at, err := decodeTime(marked)
			if err != nil {
				return fmt.Errorf("store: reading the seen mark of %s %s: %w", r.Kind, r.Key, err)
			}
			out = append(out, SeenMark{SeenRecord: r, MarkedAt: at})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// landed checks, inside the marking transaction, that r names a landed
// record on the terms MarkSeen states.
func landed(ctx context.Context, tx *sql.Tx, r SeenRecord) error {
	if !r.Kind.Recognized() {
		return &SeenRecordError{Record: r, Reason: ErrUnknownSeenKind}
	}
	if strings.TrimSpace(r.Key) == "" {
		return &SeenRecordError{Record: r, Reason: ErrNotLanded}
	}
	switch r.Kind {
	case SeenMerge:
		var end string
		err := tx.QueryRowContext(ctx, `SELECT pull_request_end FROM merge_record WHERE run = ?`, r.Key).Scan(&end)
		switch {
		case isNoRows(err):
			return &SeenRecordError{Record: r, Reason: ErrNotLanded}
		case err != nil:
			return fmt.Errorf("store: reading the merge record of run %s: %w", r.Key, err)
		case PullRequestEnd(end) != PullRequestMerged:
			return &SeenRecordError{Record: r, Reason: ErrNotLanded}
		}
	case SeenNothingToDeliver:
		var one int
		switch err := tx.QueryRowContext(ctx, `SELECT 1 FROM nothing_to_deliver WHERE run = ?`, r.Key).Scan(&one); {
		case isNoRows(err):
			return &SeenRecordError{Record: r, Reason: ErrNotLanded}
		case err != nil:
			return fmt.Errorf("store: reading the nothing-to-deliver record of run %s: %w", r.Key, err)
		}
	case SeenCleanup:
		var removed Optional[string]
		err := tx.QueryRowContext(ctx, `SELECT removed_at FROM task_cleanup WHERE task_id = ?`, r.Key).Scan(&removed)
		switch {
		case isNoRows(err):
			return &SeenRecordError{Record: r, Reason: ErrNotLanded}
		case err != nil:
			return fmt.Errorf("store: reading the cleanup record of task %s: %w", r.Key, err)
		case !removed.IsKnown():
			return &SeenRecordError{Record: r, Reason: ErrNotLanded}
		}
	}
	return nil
}

// SeenMarks returns every row of the seen mark, in the order they were
// written. It is what PRD section 9's catch-up reads to decide which landed
// records a person has not been shown, and section 8 says nothing else reads
// it.
func (s *Store) SeenMarks(ctx context.Context) ([]SeenMark, error) {
	if err := s.live(); err != nil {
		return nil, err
	}
	rows, err := s.read.QueryContext(ctx, `SELECT kind, key, marked_at FROM seen_mark ORDER BY rowid`)
	if err != nil {
		return nil, fmt.Errorf("store: reading the seen mark: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []SeenMark
	for rows.Next() {
		var kind, key, marked string
		if err := rows.Scan(&kind, &key, &marked); err != nil {
			return nil, fmt.Errorf("store: reading the seen mark: %w", err)
		}
		at, err := decodeTime(marked)
		if err != nil {
			return nil, fmt.Errorf("store: reading the seen mark: %w", err)
		}
		out = append(out, SeenMark{SeenRecord: SeenRecord{Kind: SeenKind(kind), Key: key}, MarkedAt: at})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: reading the seen mark: %w", err)
	}
	return out, nil
}
