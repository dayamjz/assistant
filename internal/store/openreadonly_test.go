package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// A read-only open reads what a writer recorded and writes nothing: the
// queued wakes a writer appended are drained from it, every write is
// ErrReadOnly, and the file is untouched by the open. The read needs no
// redactor, because nothing it can do writes the columns the redactor is
// for.
func TestOpenReadOnlyReadsTheRecordAndRefusesEveryWrite(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	w := openStoreAt(t, path)
	task := seedTask(t, w, "taskaaaaaaab").ID
	appended, err := w.AppendWakeWithMarkers(ctx, incarnationOf(t, w), WakeRequest{TaskID: task, Kind: "hold-opened", Marker: "hold-1"},
		MarkerAdvance{EventSequence: 1})
	if err != nil {
		t.Fatalf("AppendWakeWithMarkers: %v", err)
	}

	r, err := OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	queued, err := r.QueuedWakes(ctx, 10)
	if err != nil || len(queued) != 1 || queued[0].ID != appended.Wake.ID {
		t.Fatalf("the read-only read of the queue = %+v (%v), want the writer's wake %d", queued, err, appended.Wake.ID)
	}
	counts, err := r.UnacknowledgedWakes(ctx)
	if err != nil || counts[task] != 1 {
		t.Fatalf("the read-only count = %v (%v), want {%s: 1}", counts, err, task)
	}
	if _, err := r.Task(ctx, task); err != nil {
		t.Fatalf("a read-only Task read: %v", err)
	}

	if err := r.AcknowledgeWakes(ctx, []int64{appended.Wake.ID}, AcknowledgedByProgram()); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("a read-only acknowledgement = %v, want ErrReadOnly", err)
	}
	if _, err := r.DrainWakes(ctx, 10, WakeHandout{To: AcknowledgedByProgram(), Under: "u"}); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("a read-only drain = %v, want ErrReadOnly, because a drain acknowledges", err)
	}
	if _, err := r.AppendWakeWithMarkers(ctx, incarnationOf(t, w), WakeRequest{TaskID: task, Kind: "hold-opened", Marker: "hold-2"},
		MarkerAdvance{}); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("a read-only append = %v, want ErrReadOnly", err)
	}
	if _, err := r.AppendTaskEvent(ctx, task, "note", "x"); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("a read-only event append = %v, want ErrReadOnly", err)
	}
	// The writer still sees the row queued: nothing the reader did reached
	// the file.
	if left, err := w.QueuedWakes(ctx, 10); err != nil || len(left) != 1 {
		t.Fatalf("the writer's read after the read-only session = %+v (%v)", left, err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := r.QueuedWakes(ctx, 10); !errors.Is(err, ErrClosed) {
		t.Fatalf("a read after Close = %v, want ErrClosed", err)
	}
}

// A database that cannot be read as this build's is the typed result, an
// *UnreadableError matching ErrUnreadable with the cause beneath it, and
// never a store: a file that is not there, one that is not a database, one
// a writer never migrated, and one ahead of this build. A read-only open
// creates no file in any of those cases. An empty path is the caller's
// mistake and is refused as something other than unreadable.
func TestOpenReadOnlyReportsAnUnreadableDatabaseAsTheTypedResult(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()

	missing := filepath.Join(dir, "missing.db")
	assertUnreadable(t, missing, os.ErrNotExist)
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the read-only open created %s", missing)
	}

	garbage := filepath.Join(dir, "garbage.db")
	if err := os.WriteFile(garbage, []byte("this is not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertUnreadable(t, garbage, nil)

	// A database a writer opened under a shorter migration list: this
	// build's accessors would select from tables it lacks.
	behind := filepath.Join(dir, "behind.db")
	short := openStoreAt(t, behind)
	if err := short.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := OpenReadOnly(ctx, behind)
	if err != nil {
		t.Fatalf("a fully migrated database is unreadable: %v", err)
	}
	_ = db.Close()
	// Take a migration off the record: the schema this build has is then
	// ahead of what the file says, which is the behind case.
	behindDB := openStoreAt(t, behind)
	if _, err := behindDB.write.ExecContext(ctx, `DELETE FROM schema_migration WHERE version = ?`, len(schema)); err != nil {
		t.Fatal(err)
	}
	if err := behindDB.Close(); err != nil {
		t.Fatal(err)
	}
	assertUnreadable(t, behind, ErrSchemaBehind)

	ahead := filepath.Join(dir, "ahead.db")
	aheadDB := openStoreAt(t, ahead)
	if _, err := aheadDB.write.ExecContext(ctx,
		`INSERT INTO schema_migration (version, name, statements, digest, applied_at) VALUES (?, 'future', 1, 'x', 'now')`,
		len(schema)+1); err != nil {
		t.Fatal(err)
	}
	if err := aheadDB.Close(); err != nil {
		t.Fatal(err)
	}
	assertUnreadable(t, ahead, ErrSchemaAhead)

	if _, err := OpenReadOnly(ctx, ""); err == nil || errors.Is(err, ErrUnreadable) {
		t.Fatalf("an empty path = %v, want a refusal that is not ErrUnreadable", err)
	}
}

func assertUnreadable(t *testing.T, path string, cause error) {
	t.Helper()
	s, err := OpenReadOnly(context.Background(), path)
	if s != nil {
		_ = s.Close()
		t.Fatalf("OpenReadOnly(%s) returned a store", path)
	}
	var unreadable *UnreadableError
	if !errors.As(err, &unreadable) || !errors.Is(err, ErrUnreadable) {
		t.Fatalf("OpenReadOnly(%s) = %v, want *UnreadableError", path, err)
	}
	if unreadable.Path != path {
		t.Fatalf("the error names %q, want %q", unreadable.Path, path)
	}
	if cause != nil && !errors.Is(err, cause) {
		t.Fatalf("OpenReadOnly(%s) = %v, want the cause %v beneath it", path, err, cause)
	}
}
