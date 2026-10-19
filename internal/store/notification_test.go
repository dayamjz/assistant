package store

import (
	"context"
	"testing"
)

func TestANotificationEventIsWrittenOnceAndSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	path := t.TempDir() + "/state.db"
	s := openStoreAt(t, path)
	fresh, err := s.RecordNotificationEvent(ctx, "evt-1")
	if err != nil || !fresh {
		t.Fatalf("RecordNotificationEvent = %v, %v, want a new row", fresh, err)
	}
	again, err := s.RecordNotificationEvent(ctx, "evt-1")
	if err != nil || again {
		t.Fatalf("a second RecordNotificationEvent = %v, %v, want the first row kept", again, err)
	}
	if seen, err := s.NotificationEventSeen(ctx, "evt-1"); err != nil || !seen {
		t.Fatalf("Seen = %v, %v", seen, err)
	}
	if seen, err := s.NotificationEventSeen(ctx, "evt-2"); err != nil || seen {
		t.Fatalf("Seen of an unrecorded id = %v, %v", seen, err)
	}
	if _, err := s.RecordNotificationEvent(ctx, "  "); err == nil {
		t.Fatal("an empty identifier was recorded")
	}
	for name, statement := range map[string]string{
		"revising": `UPDATE notification_event SET event_id = 'other' WHERE event_id = ?`,
		"removing": `DELETE FROM notification_event WHERE event_id = ?`,
	} {
		if err := rawWrite(ctx, s, statement, "evt-1"); err == nil {
			t.Fatalf("%s a notification event was accepted", name)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened := openStoreAt(t, path)
	seen, err := reopened.NotificationEventSeen(ctx, "evt-1")
	if err != nil || !seen {
		t.Fatalf("after reopen Seen = %v, %v", seen, err)
	}
	fresh, err = reopened.RecordNotificationEvent(ctx, "evt-1")
	if err != nil || fresh {
		t.Fatalf("recording the same id after reopen = %v, %v, want the row kept", fresh, err)
	}
}

func TestTheNotificationEventMigrationIsAdditive(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	var added migration
	for _, m := range schema {
		if m.name == notificationEventMigration {
			added = m
		}
	}
	if added.version == 0 {
		t.Fatalf("the shipped schema has no migration named %q", notificationEventMigration)
	}
	run := seedRun(t, s)
	for _, statement := range []string{
		`DROP TRIGGER IF EXISTS notification_event_is_written_once`,
		`DROP TRIGGER IF EXISTS notification_event_is_not_removed`,
		`DROP TABLE notification_event`,
	} {
		if _, err := s.write.ExecContext(ctx, statement); err != nil {
			t.Fatalf("rolling the schema back: %v", err)
		}
	}
	if _, err := s.write.ExecContext(ctx, `DELETE FROM schema_migration WHERE version = ?`, added.version); err != nil {
		t.Fatalf("forgetting the migration: %v", err)
	}
	if err := migrate(ctx, s.write, schema); err != nil {
		t.Fatalf("applying the shipped migration over the old rows: %v", err)
	}
	read, err := s.Run(ctx, run.ID)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if read.Status != run.Status || read.Branch != run.Branch {
		t.Fatalf("the earlier run reads back %+v, want it as it stood", read)
	}
	if _, err := s.RecordNotificationEvent(ctx, "evt-1"); err != nil {
		t.Fatalf("recording after the migration: %v", err)
	}
	if err := rawWrite(ctx, s, `DELETE FROM notification_event WHERE event_id = ?`, "evt-1"); err == nil {
		t.Fatal("the reapplied trigger let the event be removed")
	}
}
