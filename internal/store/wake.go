package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Wake is one row of PRD section 8's durable wake queue: an actionable wake
// the supervisor loop wrote, actionable until it is acknowledged and kept
// afterwards. A drain hands a row out and leaves it queued; only the
// acknowledgement closes it. The kind is a word internal/supervisor's table gives a meaning
// to, and the marker is the component the loop composed to keep one
// detection from being queued twice; this package records both and reads
// neither.
type Wake struct {
	// ID is the row's identifier, which the store assigns in the order it
	// accepted the rows, and which an acknowledgement names.
	ID int64 `json:"id"`
	// TaskID names the task the wake is about.
	TaskID string `json:"task_id"`
	// Kind is what the wake is, in the supervisor's vocabulary.
	Kind string `json:"kind"`
	// Marker is the detector's idempotency component for this wake.
	Marker string `json:"marker"`
	// CreatedAt is when the row was appended.
	CreatedAt time.Time `json:"created_at"`
	// HandledAt is when the wake was acknowledged, and unknown while it is
	// still queued.
	HandledAt Optional[time.Time] `json:"handled_at"`
	// AcknowledgedBy is who acknowledged it, and unknown while it is still
	// queued.
	AcknowledgedBy Optional[Acknowledger] `json:"acknowledged_by"`
	// HandedOutAt is when the latest drain handed the row out, and unknown
	// while no drain has. A row handed out is still queued.
	HandedOutAt Optional[time.Time] `json:"handed_out_at"`
	// HandedOutTo is who the latest drain handed the row to, and unknown
	// while no drain has. It is kept off the wire for AcknowledgedBy's
	// reason, which internal/machine's Wakes states: an Acknowledger has no
	// text form outside this package, so the record is where it is read.
	HandedOutTo Optional[Acknowledger] `json:"-"`
	// HandedOutUnder is the attachment the latest drain handed the row out
	// under, as the drain's caller composed it, and unknown while no drain
	// has. This package compares it and never interprets it; it is kept off
	// the wire because it is the service's own bookkeeping.
	HandedOutUnder Optional[string] `json:"-"`
	// Handouts is how many drains have handed the row out, so a reader can
	// tell a wake handed out again from one handed out for the first time.
	// It is unknown for a row no drain has handed out, and for a row
	// acknowledged before handouts were recorded, which a drain of that
	// build may have taken.
	Handouts Optional[int64] `json:"handouts"`
}

// Queued reports whether the wake has not been acknowledged, handed out or
// not.
func (w Wake) Queued() bool { return !w.HandledAt.IsKnown() }

// HandedOut reports whether a drain has handed the wake out and it has not
// been acknowledged since.
func (w Wake) HandedOut() bool { return w.Queued() && w.HandedOutAt.IsKnown() }

// WakeRequest is the wake a loop asks to append: the task, the kind, and the
// marker it composed.
//
// The uniqueness the queue enforces is over the three together, so a
// detection replayed after a restart, with the same marker, is one row. What
// keeps a legitimate second surface from reading as a replay is a component
// of the marker that advances on every surface, and composing it is the
// loop's: for a terminal kind the capture digest with the change epoch the
// markers carry and, for an escalation of the same stall, the stall
// repetition count; for an event kind the event's sequence; for a hold kind
// the hold's key; for a terminal gone the session reference with which of
// the three terminal-gone cases it was and, for each surface after the
// first of the same silence, the instant of that surface; this package
// checks only that the marker is non-empty. A marker composed without that component drops a
// second surface as a replay while the first row is queued and is refused
// with ErrWakeHandled once it is acknowledged, which is where the defect
// surfaces.
type WakeRequest struct {
	// TaskID names the task.
	TaskID string
	// Kind is what the wake is, in the supervisor's vocabulary. It is
	// recorded as handed over and checked only for being non-empty: what the
	// word means is internal/supervisor's table, and this package is not a
	// second owner of it.
	Kind string
	// Marker is the detector's idempotency component.
	Marker string
}

// WakeDisposition is what AppendWakeWithMarkers did with the wake. The zero
// value is neither outcome, so an unfilled result reads as no answer.
type WakeDisposition int

// The two dispositions.
const (
	// WakeAppended is a new row written.
	WakeAppended WakeDisposition = iota + 1
	// WakeAlreadyQueued is a row with the same task, kind and marker already
	// queued and not yet acknowledged, which is one wake: a detection the
	// loop replayed, after a restart or a pass that repeated itself. Nothing
	// was appended, and the markers were advanced all the same.
	WakeAlreadyQueued
)

// String names the disposition for a report.
func (d WakeDisposition) String() string {
	switch d {
	case WakeAppended:
		return "appended"
	case WakeAlreadyQueued:
		return "already-queued"
	default:
		return "undecided"
	}
}

// WakeAppend is AppendWakeWithMarkers's answer: what it did, the row that
// stands for the wake, and the markers as they stand after the write.
type WakeAppend struct {
	// Disposition is whether the row was appended or already queued.
	Disposition WakeDisposition
	// Wake is the row, the new one or the one already queued.
	Wake Wake
	// Markers is the task's marker row after the advance.
	Markers WakeMarkers
}

// WakeMarkers is what the supervisor loop had observed about one task when it
// last passed over it: PRD section 5's detector markers, one row per task,
// rewritten in place. It is the comparison base for the loop's next pass and
// not the task's state; nothing resolves a state from it.
type WakeMarkers struct {
	// TaskID names the task.
	TaskID string `json:"task_id"`
	// CaptureDigest is the digest of the terminal's normalized screen at the
	// last capture, and unknown when the last pass could not capture it.
	CaptureDigest Optional[string] `json:"capture_digest"`
	// ChangeEpoch counts the passes whose capture digest differed from the
	// one recorded before, with no row and an unknown digest counting as one
	// absence. The store derives it in the transaction that records the new
	// digest, so it is never handed over: a loop composing a terminal kind's
	// marker reads it from here, and a terminal that returns to a screen it
	// showed before carries a higher epoch than it did then.
	ChangeEpoch int64 `json:"change_epoch"`
	// EventSequence is the highest task event sequence the loop had seen.
	EventSequence int64 `json:"event_sequence"`
	// Surfaced is whether the terminal-ish wait on the current screen has
	// already surfaced once, which PRD section 5 absorbs its redraws on.
	Surfaced bool `json:"surfaced"`
	// WindowStart is when the window the loop is measuring began, the
	// escalation window or a declared wait's recheck window, and unknown
	// while no window is open.
	WindowStart Optional[time.Time] `json:"window_start"`
	// StallRepetitions is how many times the same unchanged screen has
	// surfaced, PRD section 11's repeated stall, which the next escalation of
	// the same stall carries in its marker.
	StallRepetitions int64 `json:"stall_repetitions"`
	// UnobservedSince is when the terminal first could not be observed, and
	// unknown while it is observed.
	UnobservedSince Optional[time.Time] `json:"unobserved_since"`
	// GoneSurfacedAt is when the terminal that could not be observed last
	// surfaced as gone, which the escalation window between two of those
	// surfaces is measured from. It is unknown while the terminal is
	// observed and before the first surface of a silence, and it is
	// written in the transaction that appends the wake it records.
	GoneSurfacedAt Optional[time.Time] `json:"gone_surfaced_at"`
	// UpdatedAt is when the row was last written.
	UpdatedAt time.Time `json:"updated_at"`
}

// MarkerAdvance is what a pass of the loop hands over to become the task's
// markers: every column of WakeMarkers the loop decides. ChangeEpoch is not
// among them, because the store derives it from the digest, and UpdatedAt is
// assigned at the write.
type MarkerAdvance struct {
	CaptureDigest    Optional[string]
	EventSequence    int64
	Surfaced         bool
	WindowStart      Optional[time.Time]
	StallRepetitions int64
	UnobservedSince  Optional[time.Time]
	GoneSurfacedAt   Optional[time.Time]
}

func (a MarkerAdvance) validate(taskID string) error {
	if strings.TrimSpace(taskID) == "" {
		return errors.New("store: a marker advance names no task")
	}
	if a.EventSequence < 0 {
		return fmt.Errorf("store: task %s: an event sequence of %d is not a position", taskID, a.EventSequence)
	}
	if a.StallRepetitions < 0 {
		return fmt.Errorf("store: task %s: a stall repetition count of %d is not a count", taskID, a.StallRepetitions)
	}
	return nil
}

func (w WakeRequest) validate() error {
	if strings.TrimSpace(w.TaskID) == "" {
		return errors.New("store: a wake names no task")
	}
	if strings.TrimSpace(w.Kind) == "" {
		return fmt.Errorf("store: task %s: a wake has no kind", w.TaskID)
	}
	if strings.TrimSpace(w.Marker) == "" {
		return fmt.Errorf("store: task %s: a %s wake has no marker", w.TaskID, w.Kind)
	}
	return nil
}

// AppendWakeWithMarkers appends an actionable wake and advances the task's
// markers in one transaction, which is PRD section 5's "written in the same
// transaction as the detector markers it advances" as a shape rather than a
// rule: there is no accessor that appends a wake without the markers, so no
// marker can move past a surfaced wake that was not recorded. The anchor
// AppendTaskEventAfter takes is not taken here, because one loop per service
// writes a task's markers and a pass that lost a race to itself has nothing
// to decide again. What is taken is the incarnation: the write is refused
// with ErrStaleIncarnation, markers included, when inc is not the current
// one, decided inside the same transaction, so a loop the watchdog replaced
// writes nothing however late it unwedges.
//
// A row with the same task, kind and marker that is still queued is the same
// wake, replayed: the answer is WakeAlreadyQueued, the markers are advanced
// all the same, and nothing is appended. The same key on a row already
// acknowledged is refused with ErrWakeHandled, and the whole transaction with
// it, the markers included, because a marker that did not advance for a new
// surface is a defect in the loop and not a wake to drop. A task that does
// not exist is ErrNotFound, wrapped, and a request or an advance that is
// malformed is refused before anything is read.
func (s *Store) AppendWakeWithMarkers(ctx context.Context, inc Incarnation, w WakeRequest, advance MarkerAdvance) (WakeAppend, error) {
	if err := w.validate(); err != nil {
		return WakeAppend{}, err
	}
	if err := advance.validate(w.TaskID); err != nil {
		return WakeAppend{}, err
	}
	var out WakeAppend
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if err := fenceIncarnation(ctx, tx, inc); err != nil {
			return err
		}
		if err := taskExists(ctx, tx, w.TaskID); err != nil {
			return err
		}
		markers, err := advanceMarkers(ctx, tx, w.TaskID, advance)
		if err != nil {
			return err
		}
		out.Markers = markers
		existing, err := wakeByKey(ctx, tx, w)
		switch {
		case err == nil && existing.Queued():
			out.Disposition = WakeAlreadyQueued
			out.Wake = existing
			return nil
		case err == nil:
			return fmt.Errorf("%w: wake %d", ErrWakeHandled, existing.ID)
		case !isNoRows(err):
			return err
		}
		result, err := tx.ExecContext(ctx, `
			INSERT INTO wake (task_id, kind, marker, created_at, handled_at, acknowledged_by)
			VALUES (?, ?, ?, ?, NULL, NULL)`,
			w.TaskID, w.Kind, w.Marker, encodeTime(nowUTC()))
		if err != nil {
			return err
		}
		id, err := result.LastInsertId()
		if err != nil {
			return err
		}
		appended, err := scanWake(tx.QueryRowContext(ctx, wakeColumns+` FROM wake WHERE id = ?`, id))
		if err != nil {
			return err
		}
		out.Disposition = WakeAppended
		out.Wake = appended
		return nil
	})
	if err != nil {
		return WakeAppend{}, fmt.Errorf("store: appending a %s wake for task %s: %w", w.Kind, w.TaskID, err)
	}
	return out, nil
}

// AdvanceWakeMarkers rewrites a task's markers with no wake, which is what an
// absorbed pass does: PRD section 5's "advance markers, no model turn". It
// derives the change epoch on WakeMarkers' terms and returns the row as
// written. A task that does not exist is ErrNotFound, wrapped, and an
// incarnation that is not the current one is ErrStaleIncarnation, decided in
// the same transaction, on AppendWakeWithMarkers's terms.
func (s *Store) AdvanceWakeMarkers(ctx context.Context, inc Incarnation, taskID string, advance MarkerAdvance) (WakeMarkers, error) {
	if err := advance.validate(taskID); err != nil {
		return WakeMarkers{}, err
	}
	var out WakeMarkers
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if err := fenceIncarnation(ctx, tx, inc); err != nil {
			return err
		}
		if err := taskExists(ctx, tx, taskID); err != nil {
			return err
		}
		var err error
		out, err = advanceMarkers(ctx, tx, taskID, advance)
		return err
	})
	if err != nil {
		return WakeMarkers{}, fmt.Errorf("store: advancing the markers of task %s: %w", taskID, err)
	}
	return out, nil
}

// WakeMarkers returns a task's markers and whether the loop has written any.
// A task no pass has reached has none, and that is an answer rather than a
// failure.
func (s *Store) WakeMarkers(ctx context.Context, taskID string) (WakeMarkers, bool, error) {
	if err := s.live(); err != nil {
		return WakeMarkers{}, false, err
	}
	m, err := wakeMarkersOf(ctx, s.read, taskID)
	if isNoRows(err) {
		return WakeMarkers{}, false, nil
	}
	if err != nil {
		return WakeMarkers{}, false, fmt.Errorf("store: reading the markers of task %s: %w", taskID, err)
	}
	return m, true, nil
}

// advanceMarkers writes the marker row inside a transaction, deriving the
// change epoch from the digest it replaces.
func advanceMarkers(ctx context.Context, tx *sql.Tx, taskID string, a MarkerAdvance) (WakeMarkers, error) {
	before, err := wakeMarkersOf(ctx, tx, taskID)
	epoch := int64(0)
	switch {
	case err == nil:
		epoch = before.ChangeEpoch
		if before.CaptureDigest != a.CaptureDigest {
			epoch++
		}
	case isNoRows(err):
		if a.CaptureDigest.IsKnown() {
			epoch = 1
		}
	default:
		return WakeMarkers{}, err
	}
	surfaced := 0
	if a.Surfaced {
		surfaced = 1
	}
	now := nowUTC()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO wake_marker (task_id, capture_digest, change_epoch, event_sequence, surfaced,
			window_start, stall_repetitions, unobserved_since, gone_surfaced_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(task_id) DO UPDATE SET
			capture_digest = excluded.capture_digest,
			change_epoch = excluded.change_epoch,
			event_sequence = excluded.event_sequence,
			surfaced = excluded.surfaced,
			window_start = excluded.window_start,
			stall_repetitions = excluded.stall_repetitions,
			unobserved_since = excluded.unobserved_since,
			gone_surfaced_at = excluded.gone_surfaced_at,
			updated_at = excluded.updated_at`,
		taskID, a.CaptureDigest, epoch, a.EventSequence, surfaced,
		optionalTimeText(a.WindowStart), a.StallRepetitions, optionalTimeText(a.UnobservedSince),
		optionalTimeText(a.GoneSurfacedAt), encodeTime(now),
	); err != nil {
		return WakeMarkers{}, err
	}
	return wakeMarkersOf(ctx, tx, taskID)
}

const wakeMarkerColumns = `SELECT task_id, capture_digest, change_epoch, event_sequence, surfaced,
	window_start, stall_repetitions, unobserved_since, gone_surfaced_at, updated_at`

func wakeMarkersOf(ctx context.Context, q querier, taskID string) (WakeMarkers, error) {
	var m WakeMarkers
	var surfaced int
	var window, unobserved, goneSurfaced Optional[string]
	var updated string
	if err := q.QueryRowContext(ctx, wakeMarkerColumns+` FROM wake_marker WHERE task_id = ?`, taskID).Scan(
		&m.TaskID, &m.CaptureDigest, &m.ChangeEpoch, &m.EventSequence, &surfaced,
		&window, &m.StallRepetitions, &unobserved, &goneSurfaced, &updated,
	); err != nil {
		return WakeMarkers{}, err
	}
	m.Surfaced = surfaced != 0
	var err error
	if m.WindowStart, err = optionalTimeValue(window, "window_start"); err != nil {
		return WakeMarkers{}, err
	}
	if m.UnobservedSince, err = optionalTimeValue(unobserved, "unobserved_since"); err != nil {
		return WakeMarkers{}, err
	}
	if m.GoneSurfacedAt, err = optionalTimeValue(goneSurfaced, "gone_surfaced_at"); err != nil {
		return WakeMarkers{}, err
	}
	if m.UpdatedAt, err = decodeTime(updated); err != nil {
		return WakeMarkers{}, err
	}
	return m, nil
}

// QueuedWakes returns the queued wakes, oldest first, at most limit of them,
// handed out or not, and hands out none: it is a read, for a caller that
// has to see what stands for the coordinator without handing it over, which
// is the program's own reconciliation and relaunch and a person's listing of
// the queue. A row it returns stays where it was. The limit must be
// positive; a batch with no bound is not a batch.
func (s *Store) QueuedWakes(ctx context.Context, limit int) ([]Wake, error) {
	if limit < 1 {
		return nil, fmt.Errorf("store: a limit of %d queued wakes is not a batch size", limit)
	}
	if err := s.live(); err != nil {
		return nil, err
	}
	rows, err := s.read.QueryContext(ctx, wakeColumns+` FROM wake WHERE handled_at IS NULL ORDER BY id LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: reading the queued wakes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out, err := collectWakes(rows)
	if err != nil {
		return nil, fmt.Errorf("store: reading the queued wakes: %w", err)
	}
	return out, nil
}

// WakeHandout is what a drain hands rows out as: who to, under which
// attachment, and which attachments are still attached.
//
// The attachments are the caller's: internal/service composes them from its
// own identity and the coordinator's wake stream, and this package compares
// them as text and never interprets one. What it promises is the comparison:
// a queued row last handed out under an attachment named in Held is not
// handed out again, and every other queued row is.
type WakeHandout struct {
	// To is who the rows are handed to. The zero Acknowledger is refused
	// with ErrNoAcknowledger.
	To Acknowledger
	// Under is the attachment the rows are handed out under, which a later
	// drain names in Held for as long as it is attached. It may not be
	// blank.
	Under string
	// Held are the attachments still attached. A row last handed out under
	// one of them is held for it and not offered; empty holds nothing, so
	// every queued row is offered.
	Held []string
}

func (h WakeHandout) validate() error {
	if _, err := acknowledgerText(h.To); err != nil {
		return err
	}
	if strings.TrimSpace(h.Under) == "" {
		return errors.New("store: a wake handout names no attachment it is made under")
	}
	return nil
}

// offeredClause is the condition a queued row is offered on, with its
// arguments: never handed out, or handed out under an attachment held does
// not name.
func offeredClause(held []string) (string, []any) {
	clause := `handled_at IS NULL`
	if len(held) == 0 {
		return clause, nil
	}
	marks := make([]string, len(held))
	args := make([]any, len(held))
	for i, h := range held {
		marks[i], args[i] = "?", h
	}
	return clause + ` AND (handed_out_under IS NULL OR handed_out_under NOT IN (` + strings.Join(marks, ",") + `))`, args
}

// OfferedWakes reports how many queued rows a drain made now with held as
// its WakeHandout.Held would offer, and hands out none. It is a read, for a
// wait that has to know whether a drain is worth its write.
func (s *Store) OfferedWakes(ctx context.Context, held []string) (int, error) {
	if err := s.live(); err != nil {
		return 0, err
	}
	clause, args := offeredClause(held)
	var n int
	if err := s.read.QueryRowContext(ctx, `SELECT count(*) FROM wake WHERE `+clause, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: counting the offered wakes: %w", err)
	}
	return n, nil
}

// DrainWakes is PRD section 8's drain as the owner amended it: draining
// hands a row out, and acknowledging closes it. It returns the queued rows
// on offer, oldest first, at most limit of them, and records on exactly
// those rows, in the one transaction that read them, that they were handed
// out to h.To under h.Under, now, and one handout more. It closes nothing:
// every row it returns is still queued, and only AcknowledgeWakes takes it
// off the queue.
//
// A row is on offer when it is queued and was never handed out, or was last
// handed out under an attachment h.Held does not name. So a row handed out
// is not handed out again for as long as the caller names its attachment as
// held, and is handed out again by the first drain that does not, which is
// how a coordinator that drained and then went away before acting gets the
// row back: delivery is at least once. When an attachment stops being held
// is the caller's to decide; nothing here reads a clock to decide it.
//
// The limit must be positive and the handout must name who and under what,
// all before anything is read; the zero Acknowledger is ErrNoAcknowledger. A
// handle opened read only refuses with ErrReadOnly, because a drain writes.
func (s *Store) DrainWakes(ctx context.Context, limit int, h WakeHandout) ([]Wake, error) {
	if limit < 1 {
		return nil, fmt.Errorf("store: a drain limit of %d is not a batch size", limit)
	}
	if err := h.validate(); err != nil {
		return nil, fmt.Errorf("store: draining wakes: %w", err)
	}
	to, _ := acknowledgerText(h.To)
	clause, args := offeredClause(h.Held)
	var out []Wake
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id FROM wake WHERE `+clause+` ORDER BY id LIMIT ?`, append(args, limit)...)
		if err != nil {
			return err
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}
		now := encodeTime(nowUTC())
		out = make([]Wake, 0, len(ids))
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx,
				`UPDATE wake SET handed_out_at = ?, handed_out_to = ?, handed_out_under = ?,
					handouts = coalesce(handouts, 0) + 1
				WHERE id = ? AND handled_at IS NULL`,
				now, to, h.Under, id); err != nil {
				return err
			}
			drained, err := scanWake(tx.QueryRowContext(ctx, wakeColumns+` FROM wake WHERE id = ?`, id))
			if err != nil {
				return err
			}
			out = append(out, drained)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("store: draining wakes: %w", err)
	}
	return out, nil
}

// TaskWakes returns every wake of a task, acknowledged or not, oldest first.
// It is the history of what surfaced for the task, closed rows included;
// which of them is still actionable is QueuedWakes' question.
func (s *Store) TaskWakes(ctx context.Context, taskID string) ([]Wake, error) {
	if err := s.live(); err != nil {
		return nil, err
	}
	rows, err := s.read.QueryContext(ctx, wakeColumns+` FROM wake WHERE task_id = ? ORDER BY id`, taskID)
	if err != nil {
		return nil, fmt.Errorf("store: listing the wakes of task %s: %w", taskID, err)
	}
	defer func() { _ = rows.Close() }()
	out, err := collectWakes(rows)
	if err != nil {
		return nil, fmt.Errorf("store: listing the wakes of task %s: %w", taskID, err)
	}
	return out, nil
}

// UnacknowledgedWakes returns how many queued wakes each task has, handed
// out or not, keyed by task identifier, with no entry for a task that has
// none. It is the queue's half of what internal/supervisor's in-flight table
// reads: a task with a row here is one the coordinator has not acknowledged
// acting on, whatever its state says, so a wake keeps its task in flight
// until it is acknowledged and not until it is drained. The count is taken
// as of one read; which tasks are in flight by state is not decided here.
func (s *Store) UnacknowledgedWakes(ctx context.Context) (map[string]int, error) {
	if err := s.live(); err != nil {
		return nil, err
	}
	rows, err := s.read.QueryContext(ctx, `SELECT task_id, count(*) FROM wake WHERE handled_at IS NULL GROUP BY task_id`)
	if err != nil {
		return nil, fmt.Errorf("store: counting queued wakes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make(map[string]int)
	for rows.Next() {
		var task string
		var n int
		if err := rows.Scan(&task, &n); err != nil {
			return nil, fmt.Errorf("store: counting queued wakes: %w", err)
		}
		out[task] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: counting queued wakes: %w", err)
	}
	return out, nil
}

// AcknowledgeWakes records that the named wakes were handled, by whom, in
// one transaction: every row is checked and then every row is written, so a
// batch that names an identifier no wake has, a *WakeIDError with
// ErrWakeUnknown, or a wake already acknowledged, a *WakeIDError with
// ErrWakeAcknowledged, writes nothing. A batch naming a subset of the
// queued rows leaves the rest queued. The zero Acknowledger is refused
// with ErrNoAcknowledger and an empty batch is refused, both before anything
// is read.
//
// The acknowledgement is what closes a row, and the last revision it takes:
// it takes the row out of QueuedWakes, DrainWakes and UnacknowledgedWakes,
// and no drain hands it out again. A row a drain handed out is still queued
// and is acknowledged here on the same terms as one nobody drained, which is
// how the coordinator says it acted on what a drain handed it; a row it
// learned of some other way, from a pushed notice or a task's history, is
// acknowledged the same way. It records who acknowledged and does not gate
// on it: every member of the set closes a row on the same terms.
func (s *Store) AcknowledgeWakes(ctx context.Context, ids []int64, by Acknowledger) error {
	if len(ids) == 0 {
		return errors.New("store: an acknowledgement names no wakes")
	}
	who, err := acknowledgerText(by)
	if err != nil {
		return fmt.Errorf("store: acknowledging wakes: %w", err)
	}
	now := encodeTime(nowUTC())
	return s.inTx(ctx, func(tx *sql.Tx) error {
		for _, id := range ids {
			var handled Optional[string]
			err := tx.QueryRowContext(ctx, `SELECT handled_at FROM wake WHERE id = ?`, id).Scan(&handled)
			switch {
			case isNoRows(err):
				return &WakeIDError{ID: id, Reason: ErrWakeUnknown}
			case err != nil:
				return fmt.Errorf("store: acknowledging wake %d: %w", id, err)
			case handled.IsKnown():
				return &WakeIDError{ID: id, Reason: ErrWakeAcknowledged}
			}
		}
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx,
				`UPDATE wake SET handled_at = ?, acknowledged_by = ? WHERE id = ? AND handled_at IS NULL`,
				now, who, id); err != nil {
				return fmt.Errorf("store: acknowledging wake %d: %w", id, err)
			}
		}
		return nil
	})
}

const wakeColumns = `SELECT id, task_id, kind, marker, created_at, handled_at, acknowledged_by,
	handed_out_at, handed_out_to, handed_out_under, handouts`

func wakeByKey(ctx context.Context, q querier, w WakeRequest) (Wake, error) {
	return scanWake(q.QueryRowContext(ctx, wakeColumns+` FROM wake WHERE task_id = ? AND kind = ? AND marker = ?`,
		w.TaskID, w.Kind, w.Marker))
}

func scanWake(sc scanner) (Wake, error) {
	var w Wake
	var created string
	var handled, by, handedAt, handedTo Optional[string]
	if err := sc.Scan(&w.ID, &w.TaskID, &w.Kind, &w.Marker, &created, &handled, &by,
		&handedAt, &handedTo, &w.HandedOutUnder, &w.Handouts); err != nil {
		return Wake{}, err
	}
	var err error
	if w.CreatedAt, err = decodeTime(created); err != nil {
		return Wake{}, err
	}
	if w.HandledAt, err = optionalTimeValue(handled, "handled_at"); err != nil {
		return Wake{}, err
	}
	if w.AcknowledgedBy, err = acknowledgerValue(by, "acknowledged_by"); err != nil {
		return Wake{}, err
	}
	if w.HandedOutAt, err = optionalTimeValue(handedAt, "handed_out_at"); err != nil {
		return Wake{}, err
	}
	if w.HandedOutTo, err = acknowledgerValue(handedTo, "handed_out_to"); err != nil {
		return Wake{}, err
	}
	return w, nil
}

func collectWakes(rows *sql.Rows) ([]Wake, error) {
	var out []Wake
	for rows.Next() {
		w, err := scanWake(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
