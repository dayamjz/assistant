package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// RecordNotificationEvent writes eventID once and reports whether this call
// wrote it. A second call leaves the first row and reports false. The row
// is the fact that the notification gateway applied the identifier; it says
// nothing about a task, a hold, or a merge. An empty identifier is refused
// and nothing is written.
func (s *Store) RecordNotificationEvent(ctx context.Context, eventID string) (bool, error) {
	if strings.TrimSpace(eventID) == "" {
		return false, fmt.Errorf("store: a notification event has no identifier")
	}
	var fresh bool
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`INSERT OR IGNORE INTO notification_event (event_id, recorded_at) VALUES (?, ?)`,
			eventID, encodeTime(nowUTC()))
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		fresh = n == 1
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("store: recording notification event %s: %w", eventID, err)
	}
	return fresh, nil
}

// NotificationEventSeen reports whether eventID has been recorded. A missing
// row is false, not an error.
func (s *Store) NotificationEventSeen(ctx context.Context, eventID string) (bool, error) {
	if err := s.live(); err != nil {
		return false, err
	}
	var one int
	err := s.read.QueryRowContext(ctx, `SELECT 1 FROM notification_event WHERE event_id = ?`, eventID).Scan(&one)
	if isNoRows(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("store: reading notification event %s: %w", eventID, err)
	}
	return true, nil
}
