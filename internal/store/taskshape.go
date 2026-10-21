package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/dayamjz/assistant/internal/config"
)

// PromotedEventKind is the kind of the event TransitionTaskShape appends in
// the transaction that flips a task's shape: PRD section 5's promotion, an
// investigation task becoming a delivery task in place. As with
// OpeningEventKind, the store records the word and internal/taskstate's
// table is what gives it a meaning.
const PromotedEventKind = "promoted"

// ErrShapeTransition is returned by TransitionTaskShape for a move it does
// not make: anything but TaskInvestigation to TaskDelivery. PRD section 5
// gives promotion one direction, so the reverse is refused before anything
// is read rather than recorded as a shape change nobody defined.
var ErrShapeTransition = errors.New("store: the only shape transition is investigation to delivery")

// TransitionTaskShape flips a task from one shape to the other and gives it
// the repository, the branch, the delivery posture and the delivery intent its
// new shape works under, in one transaction with the event that records the
// move: the task row's shape, repository_id, branch, delivery and
// delivery_intent are written together, and an event of kind
// PromotedEventKind with the given detail is appended, so a task is never
// read as a delivery task naming no branch or as an investigation task on
// one, and a task promoted through it is never read as a delivery task with
// no posture or no intent. It returns the task as stored and the event
// appended. The one move it makes is from TaskInvestigation to TaskDelivery;
// any other pair is ErrShapeTransition, an empty repository, branch or intent
// is refused before anything is read, and so is a posture outside
// config.Delivery's closed set, which is ErrUnknownDelivery. The intent is
// stored as given, on doc.go's terms for every column but the repository
// URLs.
//
// The intent is Task.DeliveryIntent, and the triggers of the migration
// promotedIntentMigration names hold it to this one write on the posture's
// terms: an insert naming it is refused, and so is an update unless the row
// had none, is given text that is not blank, and moves from investigation to
// delivery in the same statement.
//
// The posture is the one write of the delivery column after CreateTask's,
// and the schema holds it to that: migration 27's trigger refuses an update
// of the column unless the row had no posture, is given a known one, and
// moves from investigation to delivery in the same statement. CreateTask
// records no posture on an investigation task, so the write lands on an
// empty column; a second write of it is refused, here by the shape check
// below before the trigger is reached.
//
// Three facts are checked in the transaction, on the single writer
// connection, and nothing is written unless all three hold. The task's shape
// must still be from: a task that already moved is ErrTaskShapeMoved,
// which is what makes a second promotion of one task refuse. The task must
// have no cleanup record: a task with one is refused with
// ErrCleanupRecorded, because RecordTaskCleanup claims the shape the cleanup
// was decided over in the transaction that writes that record, and the two
// writes read each other's row under the same serialization, so a promotion
// and a cleanup of one task cannot interleave: one of the two is refused,
// and which is decided by the order the writer connection ran them in. And
// anchor must be the highest event sequence the caller had read, on
// AppendTaskEventAfter's terms: the state a promotion is decided from is
// resolved rather than stored, so the caller anchors the flip to the history
// its resolution read, and a history that moved since is a *TaskMovedError
// wrapping ErrTaskMoved naming where it stands. A refused caller reads again
// and decides again (P6).
//
// A task that does not exist is ErrNotFound, wrapped. The repository is
// written as given and not checked against the task's current repository_id:
// whether the copy was cut from that repository is a fact about the copy,
// which internal/tasks' Promote answers for, and the store records what it
// was asked to. Its one writer is that function.
//
// by is who promoted, which the copy's open kept work entries, if any, are
// answered carry-on as (PRD section 6: "the promotion records the entry's
// answer as carry on, which closes it"), naming the promoted task as the
// task carrying them on, in this same transaction; a zero Resolver is
// ErrNoResolver and nothing is written.
func (s *Store) TransitionTaskShape(ctx context.Context, id string, from, to TaskShape, repository, branch string,
	delivery config.Delivery, intent string, anchor int64, detail string, by Resolver) (Task, TaskEvent, error) {
	if strings.TrimSpace(id) == "" {
		return Task{}, TaskEvent{}, fmt.Errorf("store: a shape transition names no task")
	}
	who, err := resolverText(by)
	if err != nil {
		return Task{}, TaskEvent{}, fmt.Errorf("store: task %s: a promotion records who promoted: %w", id, err)
	}
	if from != TaskInvestigation || to != TaskDelivery {
		return Task{}, TaskEvent{}, fmt.Errorf("store: task %s: %w, not %q to %q", id, ErrShapeTransition, from, to)
	}
	if strings.TrimSpace(repository) == "" {
		return Task{}, TaskEvent{}, fmt.Errorf("store: task %s: a delivery task names a repository", id)
	}
	if strings.TrimSpace(branch) == "" {
		return Task{}, TaskEvent{}, fmt.Errorf("store: task %s: a delivery task names a branch", id)
	}
	if !delivery.Recognized() {
		return Task{}, TaskEvent{}, fmt.Errorf("store: task %s: %w: %q", id, ErrUnknownDelivery, delivery)
	}
	if strings.TrimSpace(intent) == "" {
		return Task{}, TaskEvent{}, fmt.Errorf("store: task %s: a promotion records what the delivery is to do", id)
	}

	now := nowUTC()
	var sequence int64
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		var stands string
		if err := tx.QueryRowContext(ctx, `SELECT shape FROM task WHERE id = ?`, id).Scan(&stands); err != nil {
			return errNoRows(err)
		}
		if TaskShape(stands) != from {
			return fmt.Errorf("%w: the transition is from %q and the task is %q", ErrTaskShapeMoved, from, stands)
		}
		var one int
		switch err := tx.QueryRowContext(ctx, `SELECT 1 FROM task_cleanup WHERE task_id = ?`, id).Scan(&one); {
		case err == nil:
			return ErrCleanupRecorded
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		var tip int64
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(sequence), 0) FROM task_event WHERE task_id = ?`, id).Scan(&tip); err != nil {
			return err
		}
		if tip != anchor {
			return &TaskMovedError{Task: id, Anchor: AnchoredEvent, Expected: anchor, Stands: tip}
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE task SET shape = ?, repository_id = ?, branch = ?, delivery = ?, delivery_intent = ? WHERE id = ?`,
			string(to), repository, branch, string(delivery), intent, id); err != nil {
			return err
		}
		sequence = tip + 1
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO task_event (task_id, sequence, kind, detail, at) VALUES (?, ?, ?, ?, ?)`,
			id, sequence, PromotedEventKind, detail, encodeTime(now)); err != nil {
			return err
		}
		// PRD section 6: a promotion of a task whose copy has an open kept
		// work entry records the entry's answer as carry on, naming the
		// promoted task itself as the one carrying it on, since the worker
		// continues in that same copy, and the entry closes with the
		// promotion and never without it. An entry on another copy is not
		// touched. The task is named with the answer and the copy's
		// ownership marked after, as the carry-on seals have it.
		if _, err := tx.ExecContext(ctx, `UPDATE kept_work SET answer = ?, answered_by = ?, answered_at = ?, carried_on_task = ?
			WHERE task_id = ? AND answer IS NULL AND closure IS NULL`,
			string(KeptCarryOn), who, encodeTime(now), id, id); err != nil {
			return fmt.Errorf("recording the copy's kept work as carried on by the promotion: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE kept_work SET carried_on_at = ?
			WHERE task_id = ? AND carried_on_task = ? AND carried_on_at IS NULL`, encodeTime(now), id, id); err != nil {
			return fmt.Errorf("recording the copy's kept work as carried on by the promotion: %w", err)
		}
		return nil
	})
	if err != nil {
		var moved *TaskMovedError
		if errors.As(err, &moved) {
			return Task{}, TaskEvent{}, err
		}
		return Task{}, TaskEvent{}, fmt.Errorf("store: promoting task %s: %w", id, err)
	}
	task, err := s.Task(ctx, id)
	if err != nil {
		return Task{}, TaskEvent{}, err
	}
	return task, TaskEvent{TaskID: id, Sequence: sequence, Kind: PromotedEventKind, Detail: detail, At: now}, nil
}
