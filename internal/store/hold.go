package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/dayamjz/assistant/internal/vcs"
)

// Hold is a decision waiting on a person, keyed by a stable key so that
// registering the same decision twice is one hold rather than two.
//
// PRD section 8 says a hold is closed only by an explicit resolution and never
// by the surrounding work finishing. This package keeps that literally: the
// only accessor that writes a resolution is ResolveHold, and it refuses an
// empty one. Nothing about a run's status touches a hold.
//
// The same section requires the resolution to record who made it, so
// ResolveHold takes a Resolver as well as an answer and refuses a resolution
// that names nobody. See Resolver for what the set is and for why the value
// meaning a person decided cannot arrive from a wire.
type Hold struct {
	// Key is the stable identifier of the decision. Registering the same key
	// again is a no-op.
	Key string
	// RunID names the run the decision belongs to, when it belongs to one.
	RunID Optional[string]
	// TaskID names the task the decision belongs to, when it belongs to one.
	TaskID Optional[string]
	// Subject is what the decision is about.
	Subject string
	// Detail is what the person needs in order to make it.
	Detail string
	// OpenedAt is when the hold was first registered.
	OpenedAt time.Time
	// Resolution is what was decided. It is unknown while the hold is open,
	// which is what distinguishes an open hold from one resolved with an empty
	// answer, a state ResolveHold refuses to create.
	Resolution Optional[string]
	// ResolvedAt is when it was decided, unknown while the hold is open.
	ResolvedAt Optional[time.Time]
	// ResolvedBy is who decided, from the closed set Resolver defines. It is
	// unknown while the hold is open, and unknown for a hold resolved before
	// the column existed, which is a different fact from a resolution nobody
	// is recorded for: ResolveHold refuses to create one of those.
	ResolvedBy Optional[Resolver]
	// ResolutionNote is free text recorded with the resolution about what
	// was decided, stored redacted. It is unknown while the hold is open,
	// when no note was given, and for a hold resolved before the column
	// existed. It is not who decided: that is ResolvedBy.
	ResolutionNote Optional[string] `json:"resolution_note,omitempty"`
}

// Open reports whether the hold is still waiting on a person.
func (h Hold) Open() bool { return !h.Resolution.IsKnown() }

// RegisterHold registers a decision and returns the hold as stored.
// Registering a key that already exists for the same decision, naming the
// same task and run, returns the existing hold unchanged, resolved or not,
// because the key is what says these are the same decision. A key already
// registered for another task or run, or as a housekeeping pass's
// retirement hold, is a different decision and is refused with a
// *HoldKeyTakenError (ErrHoldKeyTaken), writing nothing: a genuinely
// different decision gets a different key.
func (s *Store) RegisterHold(ctx context.Context, h Hold) (Hold, error) {
	if err := validateHold(h); err != nil {
		return Hold{}, err
	}
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		_, err := insertHold(ctx, tx, h)
		return err
	})
	if err != nil {
		return Hold{}, fmt.Errorf("store: registering hold %s: %w", h.Key, err)
	}
	return s.Hold(ctx, h.Key)
}

// ResolveHold closes a hold with an explicit resolution, recorded as made by
// by, and returns the hold as stored. It refuses an empty resolution with
// ErrNoResolution and a resolution that names nobody with ErrNoResolver, and it
// refuses with ErrHoldResolved to overwrite a resolution that is already there:
// the second decision on a closed hold is a new decision and belongs to a new
// key. A hold that does not exist is ErrNotFound.
//
// Who resolved is recorded and is not consulted. Every member of the set closes
// a hold on the same terms, this accessor has no branch on the value, and a
// resolution one member may make is a resolution every member may make. PRD
// section 8 requires that: the record exists so a history can say who made each
// call, and a resolution that became refused because of what it would say would
// be this record gating rather than recording.
func (s *Store) ResolveHold(ctx context.Context, key, resolution string, by Resolver) (Hold, error) {
	return s.ResolveHoldNoted(ctx, key, resolution, "", by)
}

// ResolveHoldNoted is ResolveHold with free text recorded beside the
// resolution. An empty note leaves ResolutionNote unknown. The note is not
// who decided: that remains by. Non-empty text passes through the redactor
// Open was given before it is stored.
func (s *Store) ResolveHoldNoted(ctx context.Context, key, resolution, note string, by Resolver) (Hold, error) {
	return s.resolveHold(ctx, nil, key, resolution, note, by)
}

// ResolveHoldUnder is ResolveHoldNoted fenced by the supervisor loop's
// incarnation, on AppendWakeWithMarkers's terms: the resolution is refused
// with ErrStaleIncarnation, nothing written, when inc is not the current
// incarnation, decided inside the same transaction. It is for a resolution
// the loop makes on what a pass read, so a loop the watchdog replaced cannot
// close a hold on a reading it took before it was replaced.
func (s *Store) ResolveHoldUnder(ctx context.Context, inc Incarnation, key, resolution, note string, by Resolver) (Hold, error) {
	return s.resolveHold(ctx, &inc, key, resolution, note, by)
}

// resolveHold is the one resolution write, fenced when inc is not nil.
func (s *Store) resolveHold(ctx context.Context, inc *Incarnation, key, resolution, note string, by Resolver) (Hold, error) {
	if strings.TrimSpace(resolution) == "" {
		return Hold{}, fmt.Errorf("%w: hold %s", ErrNoResolution, key)
	}
	resolver, err := resolverText(by)
	if err != nil {
		return Hold{}, fmt.Errorf("store: resolving hold %s: %w", key, err)
	}
	var noteCol any
	if strings.TrimSpace(note) != "" {
		noteCol = s.redact.Redact(note)
	}
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		if inc != nil {
			if err := fenceIncarnation(ctx, tx, *inc); err != nil {
				return err
			}
		}
		result, err := tx.ExecContext(ctx,
			`UPDATE hold SET resolution = ?, resolved_at = ?, resolved_by = ?, resolution_note = ? WHERE key = ? AND resolution IS NULL`,
			resolution, encodeTime(nowUTC()), resolver, noteCol, key)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected == 1 {
			return nil
		}
		var existing Optional[string]
		switch err := tx.QueryRowContext(ctx, `SELECT resolution FROM hold WHERE key = ?`, key).Scan(&existing); {
		case isNoRows(err):
			return ErrNotFound
		case err != nil:
			return err
		}
		return fmt.Errorf("%w with %s", ErrHoldResolved, existing)
	})
	if err != nil {
		return Hold{}, fmt.Errorf("store: resolving hold %s: %w", key, err)
	}
	return s.Hold(ctx, key)
}

// RecordHoldResolution registers a hold and resolves it in one transaction, on
// HoldClosure's terms, and returns the hold as stored. It is for a decision
// the recording caller answered itself, so the hold row and its resolution
// appear together: a reader never finds the hold open with nobody waiting on
// it, and never finds a resolution with no hold behind it.
//
// It refuses a hold with no key or no subject, an empty resolution with
// ErrNoResolution, and a resolution naming nobody with ErrNoResolver, before
// anything is written. A key already resolved is left as it stands, which is
// what makes recording one decision twice one record rather than a refusal:
// the key is what says these are the same decision. A key another decision
// holds (another task or run, or a housekeeping pass's retirement hold) is
// refused with a *HoldKeyTakenError, and nothing is registered or resolved.
func (s *Store) RecordHoldResolution(ctx context.Context, closing HoldClosure) (Hold, error) {
	key := closing.Hold.Key
	if strings.TrimSpace(key) == "" {
		return Hold{}, fmt.Errorf("store: hold has no key")
	}
	if strings.TrimSpace(closing.Hold.Subject) == "" {
		return Hold{}, fmt.Errorf("store: hold %s has no subject", key)
	}
	if strings.TrimSpace(closing.Resolution) == "" {
		return Hold{}, fmt.Errorf("%w: hold %s", ErrNoResolution, key)
	}
	if _, err := resolverText(closing.By); err != nil {
		return Hold{}, fmt.Errorf("store: resolving hold %s: %w", key, err)
	}
	if err := s.inTx(ctx, func(tx *sql.Tx) error {
		return s.closeHold(ctx, tx, closing, encodeTime(nowUTC()))
	}); err != nil {
		return Hold{}, err
	}
	return s.Hold(ctx, key)
}

// Hold returns the hold with the given key, or ErrNotFound.
func (s *Store) Hold(ctx context.Context, key string) (Hold, error) {
	if err := s.live(); err != nil {
		return Hold{}, err
	}
	row := s.read.QueryRowContext(ctx, holdColumns+` FROM hold WHERE key = ?`, key)
	h, err := scanHold(row)
	if err != nil {
		return Hold{}, fmt.Errorf("store: reading hold %s: %w", key, errNoRows(err))
	}
	return h, nil
}

// OpenHolds returns every hold still waiting on a person, oldest first.
func (s *Store) OpenHolds(ctx context.Context) ([]Hold, error) {
	if err := s.live(); err != nil {
		return nil, err
	}
	rows, err := s.read.QueryContext(ctx,
		holdColumns+` FROM hold WHERE resolution IS NULL ORDER BY opened_at, key`)
	if err != nil {
		return nil, fmt.Errorf("store: listing open holds: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Hold
	for rows.Next() {
		h, err := scanHold(rows)
		if err != nil {
			return nil, fmt.Errorf("store: listing open holds: %w", err)
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: listing open holds: %w", err)
	}
	return out, nil
}

const holdColumns = `SELECT key, run_id, task_id, subject, detail, opened_at, resolution, resolved_at, resolved_by, resolution_note`

func scanHold(sc scanner) (Hold, error) {
	var h Hold
	var opened string
	var resolvedAt, resolvedBy, resolutionNote Optional[string]
	if err := sc.Scan(&h.Key, &h.RunID, &h.TaskID, &h.Subject, &h.Detail, &opened, &h.Resolution, &resolvedAt, &resolvedBy, &resolutionNote); err != nil {
		return Hold{}, err
	}
	var err error
	if h.OpenedAt, err = decodeTime(opened); err != nil {
		return Hold{}, err
	}
	if h.ResolvedAt, err = optionalTimeValue(resolvedAt, "resolved_at"); err != nil {
		return Hold{}, err
	}
	if h.ResolvedBy, err = resolverValue(resolvedBy, "resolved_by"); err != nil {
		return Hold{}, err
	}
	h.ResolutionNote = resolutionNote
	return h, nil
}

func validateHold(h Hold) error {
	if strings.TrimSpace(h.Key) == "" {
		return fmt.Errorf("store: hold has no key")
	}
	if strings.TrimSpace(h.Subject) == "" {
		return fmt.Errorf("store: hold %s has no subject", h.Key)
	}
	return nil
}

// redactExistingHoldResolutionNotes runs the supplied redactor over every
// resolved hold's note before the seal migration installs its trigger. Rows
// written when only some paths redacted can still carry credentials in
// resolution_note; this is the one upgrade pass that brings them in line with
// what ResolveHoldNoted and HoldClosure store today.
func redactExistingHoldResolutionNotes(ctx context.Context, tx *sql.Tx, r vcs.Redactor) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT key, resolution_note FROM hold
		WHERE resolution IS NOT NULL AND resolution_note IS NOT NULL`)
	if err != nil {
		return fmt.Errorf("store: listing hold notes to redact: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var key, note string
		if err := rows.Scan(&key, &note); err != nil {
			return fmt.Errorf("store: reading a hold note to redact: %w", err)
		}
		redacted := r.Redact(note)
		if redacted == note {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE hold SET resolution_note = ? WHERE key = ?`, redacted, key); err != nil {
			return fmt.Errorf("store: redacting hold %s resolution note: %w", key, err)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: listing hold notes to redact: %w", err)
	}
	return nil
}

// insertHold writes h in the caller's transaction and reports whether it
// created the row. A key already registered is never passed over silently:
// a row of the same decision, naming the same task and run and not marked
// as a retirement hold, is RegisterHold's re-registration and answers false
// with no error, so a caller that must create the hold reads false as a
// refusal; any other row under the key is a different decision, refused
// with a *HoldKeyTakenError and nothing written.
func insertHold(ctx context.Context, tx *sql.Tx, h Hold) (bool, error) {
	result, err := tx.ExecContext(ctx, `INSERT INTO hold (key,run_id,task_id,subject,detail,opened_at,resolution,resolved_at,resolved_by) VALUES(?,?,?,?,?,?,NULL,NULL,NULL) ON CONFLICT(key) DO NOTHING`, h.Key, h.RunID, h.TaskID, h.Subject, h.Detail, encodeTime(nowUTC()))
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if affected == 1 {
		return true, nil
	}
	var run, task Optional[string]
	var marked Optional[int64]
	if err := tx.QueryRowContext(ctx, `SELECT run_id, task_id, housekeeping_retirement FROM hold WHERE key = ?`, h.Key).
		Scan(&run, &task, &marked); err != nil {
		return false, fmt.Errorf("reading the hold already registered under key %s: %w", h.Key, err)
	}
	switch {
	case run != h.RunID || task != h.TaskID:
		return false, &HoldKeyTakenError{Key: h.Key, Reason: fmt.Sprintf("it is the hold of task %q and run %q, not of task %q and run %q",
			task.Or(""), run.Or(""), h.TaskID.Or(""), h.RunID.Or(""))}
	case marked.Or(0) == 1:
		return false, &HoldKeyTakenError{Key: h.Key, Reason: "it is a housekeeping pass's retirement hold"}
	}
	return false, nil
}

// RegisterTaskAttemptHold records a decision only while the task is still on
// the attempt observed. It refuses a stale handoff instead of holding a newer
// worker that somebody else already started. A key already registered is
// answered on RegisterHold's terms.
func (s *Store) RegisterTaskAttemptHold(ctx context.Context, id string, attempt int64, h Hold) (Hold, error) {
	if err := validateHold(h); err != nil {
		return Hold{}, err
	}
	if h.RunID.IsKnown() {
		return Hold{}, fmt.Errorf("store: a task-attempt hold cannot name a run")
	}
	h.TaskID = Known(id)
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if err := checkLatestTaskAttempt(ctx, tx, id, attempt); err != nil {
			return err
		}
		_, err := insertHold(ctx, tx, h)
		return err
	})
	if err != nil {
		return Hold{}, err
	}
	return s.Hold(ctx, h.Key)
}
