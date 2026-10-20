package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"
)

// NotificationOutcome is how the operator's notify command ended for one
// wake. The set is closed. It says what the command did and nothing about
// whether anyone read what it was sent.
type NotificationOutcome string

const (
	// NotificationSent is a command that exited with status zero.
	NotificationSent NotificationOutcome = "sent"
	// NotificationFailed is a command that did not: it exited non-zero,
	// could not be started, or was ended before it exited, by its bound or
	// by the service closing. The detail says which.
	NotificationFailed NotificationOutcome = "failed"
)

var notificationOutcomes = []NotificationOutcome{NotificationSent, NotificationFailed}

// Recognized reports whether o is one of the outcomes this package defines.
func (o NotificationOutcome) Recognized() bool { return slices.Contains(notificationOutcomes, o) }

// ErrNotificationClaimed is what ClaimNotification refuses for a wake that
// already has a notification record, settled or not: the first claim
// stands, which is what keeps a wake from being sent twice.
var ErrNotificationClaimed = errors.New("store: the wake already has a notification record")

// ErrNotificationNotOffered is what ClaimNotification refuses for a wake
// that is no longer queued: an acknowledged wake has been acted on, and it
// is not offered to the notify command.
var ErrNotificationNotOffered = errors.New("store: an acknowledged wake is not offered for notification")

// ErrNotificationSettled is what SettleNotification refuses for a record
// already settled. The first outcome stands.
var ErrNotificationSettled = errors.New("store: the notification is already settled")

// ErrUnknownNotification is what SettleNotification refuses, and a read
// returns, for a settlement outside the closed set or whose exit status
// does not match its outcome.
var ErrUnknownNotification = errors.New("store: notification record is outside the closed set")

// Notification is the record of one wake offered to the operator's notify
// command, keyed by the wake. It is claimed before the command runs and
// settled once after it ends, and the claim and the settlement are the
// only two writes it takes; the table is sealed against every other. A
// record claimed and not settled is a command whose end was not recorded,
// a service that crashed while it ran among them, and it is not offered
// again: delivery is at most once.
type Notification struct {
	// WakeID names the wake.
	WakeID int64 `json:"wake_id"`
	// ClaimedAt is when the claim was written, just before the command ran.
	ClaimedAt time.Time `json:"claimed_at"`
	// Outcome is how the command ended, unknown until it is settled.
	Outcome Optional[NotificationOutcome] `json:"outcome"`
	// ExitCode is the command's own exit status, known when it reported
	// one; zero for every sent record.
	ExitCode Optional[int] `json:"exit_code"`
	// Detail is the redacted end of the command's output, or why it did not
	// exit on its own, as evidence; nothing reads it to decide. Unknown
	// until settled.
	Detail Optional[string] `json:"detail"`
	// SettledAt is when the settlement was written, unknown until it is.
	SettledAt Optional[time.Time] `json:"settled_at"`
}

// Settled reports whether the record carries an outcome.
func (n Notification) Settled() bool { return n.Outcome.IsKnown() }

// NotificationSettlement is how one command ended, as SettleNotification
// records it.
type NotificationSettlement struct {
	// Outcome is how it ended.
	Outcome NotificationOutcome
	// ExitCode is its own exit status, unknown when it reported none. A sent
	// settlement must carry zero.
	ExitCode Optional[int]
	// Detail is free text about how it ended. It passes through the
	// redactor Open was given before it is stored.
	Detail string
}

func (n NotificationSettlement) valid() error {
	if !n.Outcome.Recognized() {
		return fmt.Errorf("%w: outcome %q", ErrUnknownNotification, n.Outcome)
	}
	if code, ok := n.ExitCode.Get(); n.Outcome == NotificationSent && (!ok || code != 0) {
		return fmt.Errorf("%w: a sent notification with exit status %v", ErrUnknownNotification, n.ExitCode)
	}
	if code, ok := n.ExitCode.Get(); n.Outcome == NotificationFailed && ok && code == 0 {
		return fmt.Errorf("%w: a failed notification with exit status 0", ErrUnknownNotification)
	}
	return nil
}

// PendingNotifications returns the wakes still to be offered to the notify
// command: queued wakes with no notification record, oldest first, at most
// limit of them. It is a read and claims nothing. An acknowledged wake is
// never pending, so a home whose operator names a command for the first
// time is offered what is still in flight and not its whole history. The
// limit must be positive.
func (s *Store) PendingNotifications(ctx context.Context, limit int) ([]Wake, error) {
	if limit < 1 {
		return nil, fmt.Errorf("store: a limit of %d pending notifications is not a batch size", limit)
	}
	if err := s.live(); err != nil {
		return nil, err
	}
	rows, err := s.read.QueryContext(ctx, wakeColumns+` FROM wake
		WHERE handled_at IS NULL AND NOT EXISTS (SELECT 1 FROM notification n WHERE n.wake_id = wake.id)
		ORDER BY id LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: reading the pending notifications: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out, err := collectWakes(rows)
	if err != nil {
		return nil, fmt.Errorf("store: reading the pending notifications: %w", err)
	}
	return out, nil
}

// ClaimNotification writes the claim for a wake and returns the record as
// stored. The caller runs the command only after a claim it made succeeds,
// so a wake two callers both read as pending is run by one of them. It
// refuses, inside the transaction that writes, a wake that does not exist
// with ErrNotFound, one acknowledged with ErrNotificationNotOffered, and
// one already claimed with ErrNotificationClaimed, each wrapped.
func (s *Store) ClaimNotification(ctx context.Context, wakeID int64) (Notification, error) {
	now := encodeTime(nowUTC())
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var handled Optional[string]
		if err := tx.QueryRowContext(ctx, `SELECT handled_at FROM wake WHERE id = ?`, wakeID).Scan(&handled); err != nil {
			return errNoRows(err)
		}
		if handled.IsKnown() {
			return ErrNotificationNotOffered
		}
		var one int
		switch err := tx.QueryRowContext(ctx, `SELECT 1 FROM notification WHERE wake_id = ?`, wakeID).Scan(&one); {
		case err == nil:
			return ErrNotificationClaimed
		case !isNoRows(err):
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO notification (wake_id, claimed_at) VALUES (?, ?)`, wakeID, now)
		return err
	})
	if err != nil {
		return Notification{}, fmt.Errorf("store: claiming the notification of wake %d: %w", wakeID, err)
	}
	return s.Notification(ctx, wakeID)
}

// SettleNotification writes how the command ended onto a claimed record
// and returns the record as stored, with the detail redacted. It refuses a
// settlement outside the closed set with ErrUnknownNotification, a wake
// with no claim with ErrNotFound, and a record already settled with
// ErrNotificationSettled, each wrapped.
func (s *Store) SettleNotification(ctx context.Context, wakeID int64, n NotificationSettlement) (Notification, error) {
	if err := n.valid(); err != nil {
		return Notification{}, fmt.Errorf("store: settling the notification of wake %d: %w", wakeID, err)
	}
	detail := s.redact.Redact(n.Detail)
	now := encodeTime(nowUTC())
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var outcome Optional[string]
		if err := tx.QueryRowContext(ctx, `SELECT outcome FROM notification WHERE wake_id = ?`, wakeID).Scan(&outcome); err != nil {
			return errNoRows(err)
		}
		if outcome.IsKnown() {
			return ErrNotificationSettled
		}
		_, err := tx.ExecContext(ctx,
			`UPDATE notification SET outcome = ?, exit_code = ?, detail = ?, settled_at = ? WHERE wake_id = ? AND outcome IS NULL`,
			string(n.Outcome), optionalInt64(n.ExitCode), detail, now, wakeID)
		return err
	})
	if err != nil {
		return Notification{}, fmt.Errorf("store: settling the notification of wake %d: %w", wakeID, err)
	}
	return s.Notification(ctx, wakeID)
}

// Notification returns the notification record of a wake, or ErrNotFound,
// wrapped, when the wake has none, which is the ordinary state of a wake in
// a home that names no notify command. A stored outcome outside the closed
// set is ErrUnknownNotification, wrapped.
func (s *Store) Notification(ctx context.Context, wakeID int64) (Notification, error) {
	if err := s.live(); err != nil {
		return Notification{}, err
	}
	var n Notification
	var claimed string
	var outcome, settled Optional[string]
	var code Optional[int64]
	err := s.read.QueryRowContext(ctx,
		`SELECT wake_id, claimed_at, outcome, exit_code, detail, settled_at FROM notification WHERE wake_id = ?`, wakeID).
		Scan(&n.WakeID, &claimed, &outcome, &code, &n.Detail, &settled)
	if err != nil {
		return Notification{}, fmt.Errorf("store: reading the notification of wake %d: %w", wakeID, errNoRows(err))
	}
	if n.ClaimedAt, err = decodeTime(claimed); err != nil {
		return Notification{}, fmt.Errorf("store: reading the notification of wake %d: %w", wakeID, err)
	}
	if n.SettledAt, err = optionalTimeValue(settled, "settled_at"); err != nil {
		return Notification{}, fmt.Errorf("store: reading the notification of wake %d: %w", wakeID, err)
	}
	n.ExitCode = optionalIntValue(code)
	if o, ok := outcome.Get(); ok {
		n.Outcome = Known(NotificationOutcome(o))
		if err := (NotificationSettlement{Outcome: NotificationOutcome(o), ExitCode: n.ExitCode}).valid(); err != nil {
			return Notification{}, fmt.Errorf("store: reading the notification of wake %d: %w", wakeID, err)
		}
	}
	return n, nil
}
