package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// NothingToDeliverAnswer is the one answer of P12's content check a
// nothing-to-deliver record is written on: every examined commit present by
// content in the default-branch commit read. It is stored so the record
// names the answer it rests on, and the table refuses any other.
const NothingToDeliverAnswer = "present"

// NothingToDeliver is PRD section 8's nothing-to-deliver record: the
// program's own record that a run ended on section 5's empty-diff short
// circuit, of a branch its push anchor found absent where it would push,
// with every commit of the run's own found present by content in the
// default-branch commit its rebase stage read at its fresh fetch. It is
// written once, by the transaction that moves the run to passed, and never
// updated; the table is sealed against both, and against deletion while the
// run stands.
//
// It records what the run's position said and decides nothing. A task's
// Landed is derived from it, in internal/taskstate's terminal tier beside
// the merge record, and neither the run row nor the task row stores the
// fact again, which is why Store.NothingToDeliver is the one read. Whether a
// position carries the facts the record needs is internal/stages', which
// owns how the rebase stage recorded them.
type NothingToDeliver struct {
	// Run is the run that ended on the short circuit.
	Run string `json:"run"`
	// DefaultCommit is the default-branch commit the rebase stage read at
	// its fresh fetch, which the content check was made against.
	DefaultCommit string `json:"default_commit"`
	// Submitted is the commit the run was submitted with, which is the run
	// row's SubmittedHead; the write refuses any other.
	Submitted string `json:"submitted"`
	// Examined is the commits of the run's own the content check examined.
	// It is never empty: a branch with no commit of its own is no answer
	// and writes no record.
	Examined []string `json:"examined"`
	// Answer is the content check's answer, NothingToDeliverAnswer.
	Answer string `json:"answer"`
	// Checkpoint is the sequence of the checkpoint entry in the run's
	// history these facts were read from.
	Checkpoint int `json:"checkpoint"`
	// RecordedAt is when the row was written, which is when the run moved.
	RecordedAt time.Time `json:"recorded_at"`
}

// ErrNothingNotOfRun is returned by TransitionRunOnNothingToDeliver for a
// record that does not describe the run it is written for: a submitted
// commit other than the run row's, or a checkpoint entry the run's history
// does not hold.
var ErrNothingNotOfRun = errors.New("store: the nothing-to-deliver record does not describe the run it is written for")

// TransitionRunOnNothingToDeliver is TransitionRun for a run that ended on
// the empty-diff short circuit with nothing left to deliver: it moves the run
// to a status on TransitionRun's terms and writes the run's
// nothing-to-deliver record from r, in one transaction, so a reader never
// finds a run that ended this way without its record or a record on a run
// that did not move. r.Run and r.RecordedAt are the transaction's.
//
// It refuses a record missing a commit, naming one with white space in it,
// examining no commit, carrying an answer other than NothingToDeliverAnswer,
// or naming no checkpoint, before it touches the run. Inside the
// transaction it refuses, with ErrNothingNotOfRun, a submitted commit other
// than the run row's and a checkpoint sequence the run's history does not
// hold. A run already in the status it is being moved to is left as it
// stands, record included, on TransitionRun's terms, so a caller retrying
// after a commit it did not see writes nothing twice.
//
// Which status goes with the record is not decided here: internal/runs says
// which move writes it.
func (s *Store) TransitionRunOnNothingToDeliver(ctx context.Context, id string, from []RunStatus, to RunStatus,
	r NothingToDeliver) (Run, error) {
	for name, v := range map[string]string{"default-branch": r.DefaultCommit, "submitted": r.Submitted} {
		if v == "" || strings.ContainsAny(v, " \t\r\n") {
			return Run{}, fmt.Errorf("store: run %s: a nothing-to-deliver record names %q as its %s commit", id, v, name)
		}
	}
	if len(r.Examined) == 0 {
		return Run{}, fmt.Errorf("store: run %s: a nothing-to-deliver record examined no commit", id)
	}
	for _, c := range r.Examined {
		if c == "" || strings.ContainsAny(c, " \t\r\n") {
			return Run{}, fmt.Errorf("store: run %s: a nothing-to-deliver record names %q as an examined commit", id, c)
		}
	}
	if r.Answer != NothingToDeliverAnswer {
		return Run{}, fmt.Errorf("store: run %s: a nothing-to-deliver record is written on the answer %q, not %q",
			id, NothingToDeliverAnswer, r.Answer)
	}
	if r.Checkpoint < 1 {
		return Run{}, fmt.Errorf("store: run %s: a nothing-to-deliver record names checkpoint %d", id, r.Checkpoint)
	}
	examined, err := json.Marshal(r.Examined)
	if err != nil {
		return Run{}, fmt.Errorf("store: run %s: encoding the examined commits: %w", id, err)
	}
	return s.transitionRun(ctx, id, from, to, func(tx *sql.Tx, now string) error {
		var submitted string
		if err := tx.QueryRowContext(ctx, `SELECT submitted_head FROM run WHERE id = ?`, id).Scan(&submitted); err != nil {
			return fmt.Errorf("store: reading run %s: %w", id, errNoRows(err))
		}
		if submitted != r.Submitted {
			return fmt.Errorf("%w: run %s was submitted %s and the record names %s", ErrNothingNotOfRun, id, submitted, r.Submitted)
		}
		var one int
		switch err := tx.QueryRowContext(ctx, `SELECT 1 FROM graph_checkpoint WHERE run = ? AND seq = ?`,
			id, r.Checkpoint).Scan(&one); {
		case isNoRows(err):
			return fmt.Errorf("%w: run %s's history holds no checkpoint %d", ErrNothingNotOfRun, id, r.Checkpoint)
		case err != nil:
			return fmt.Errorf("store: reading checkpoint %d of run %s: %w", r.Checkpoint, id, err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO nothing_to_deliver (run, default_commit, submitted, examined, answer, checkpoint, recorded_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			id, r.DefaultCommit, r.Submitted, string(examined), r.Answer, r.Checkpoint, now); err != nil {
			return fmt.Errorf("store: recording that run %s left nothing to deliver: %w", id, err)
		}
		return nil
	})
}

// NothingToDeliver returns the run's nothing-to-deliver record and whether
// there is one. A run that wrote none has none, which is an answer rather
// than a failure; every other failure to read is an error.
func (s *Store) NothingToDeliver(ctx context.Context, run string) (NothingToDeliver, bool, error) {
	if err := s.live(); err != nil {
		return NothingToDeliver{}, false, err
	}
	record, err := nothingToDeliverOf(ctx, s.read, run)
	if isNoRows(err) {
		return NothingToDeliver{}, false, nil
	}
	if err != nil {
		return NothingToDeliver{}, false, fmt.Errorf("store: reading the nothing-to-deliver record of run %s: %w", run, err)
	}
	return record, true, nil
}

// nothingToDeliverOf reads one run's record through q, which is the reader
// pool or a snapshot's transaction, and returns the no-rows error as it is.
func nothingToDeliverOf(ctx context.Context, q querier, run string) (NothingToDeliver, error) {
	var r NothingToDeliver
	var examined, recorded string
	err := q.QueryRowContext(ctx, `
		SELECT run, default_commit, submitted, examined, answer, checkpoint, recorded_at
		FROM nothing_to_deliver WHERE run = ?`, run).
		Scan(&r.Run, &r.DefaultCommit, &r.Submitted, &examined, &r.Answer, &r.Checkpoint, &recorded)
	if err != nil {
		return NothingToDeliver{}, err
	}
	if err := json.Unmarshal([]byte(examined), &r.Examined); err != nil {
		return NothingToDeliver{}, fmt.Errorf("column examined: %w", err)
	}
	if r.RecordedAt, err = decodeTime(recorded); err != nil {
		return NothingToDeliver{}, err
	}
	return r, nil
}
