package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dayamjz/assistant/internal/vcs"

	// The embedded database is sqlite through a pure Go driver, so `go test
	// -race` and the three platforms CI builds on need no C toolchain and no
	// cgo. That is the reason for the dependency; nothing here uses the
	// driver's own API, only database/sql.
	_ "modernc.org/sqlite"
)

// driverName is the name modernc.org/sqlite registers with database/sql.
const driverName = "sqlite"

// busyTimeout is how long a connection waits for the database lock before
// giving up. Writes are serialized by this package rather than by contention,
// so this covers another process holding the same home's file, which the
// service lock in PRD section 8 already makes unusual.
const busyTimeout = 5 * time.Second

// Store is the durable record of everything the gate does. It owns the schema,
// its migrations, and every SQL statement in this product: PRD section 8 gives
// the store typed accessors and says no caller writes SQL.
//
// A Store is safe for concurrent use.
type Store struct {
	path   string
	redact vcs.Redactor

	// write carries every statement that changes a row. It is limited to one
	// connection, so writers queue in the pool instead of racing for the
	// database lock, and a read-modify-write done inside one transaction on it
	// cannot interleave with another. read carries queries only.
	write *sql.DB
	read  *sql.DB

	closeOnce sync.Once
	closeErr  error

	mu     sync.RWMutex
	closed bool
}

type options struct {
	redact vcs.Redactor
}

// Option configures Open.
type Option func(*options)

// WithRedactor supplies the credential remover this package persists the
// repository URL columns through. It is required: PRD section 8 gives
// credential removal one owner, and this package refuses to be a second one, so
// Open without it fails with ErrNoRedactor.
func WithRedactor(r vcs.Redactor) Option {
	return func(o *options) { o.redact = r }
}

// Open opens or creates the database at path and brings it up to the current
// schema. The file is created if it does not exist; its parent directory is
// not, because PRD section 8 makes the home layout somebody else's to build.
//
// Open refuses rather than degrades. Without a redactor it fails with
// ErrNoRedactor; against a database migrated by a newer build it fails with
// ErrSchemaAhead; against one whose recorded migrations differ from this
// build's it fails with ErrSchemaChanged; and when a connection does not come
// back carrying the settings this package's stated guarantees rest on, it fails
// with ErrSettingNotApplied rather than running on a configuration that is not
// the one described.
func Open(ctx context.Context, path string, opts ...Option) (*Store, error) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	if o.redact == nil {
		return nil, ErrNoRedactor
	}
	if err := probeRedactor(o.redact); err != nil {
		return nil, err
	}
	if path == "" {
		return nil, fmt.Errorf("store: no database path given")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("store: resolving %q: %w", path, err)
	}

	write, err := openPool(ctx, poolDSN(abs, true), true)
	if err != nil {
		return nil, fmt.Errorf("store: opening %s: %w", abs, err)
	}
	read, err := openPool(ctx, poolDSN(abs, false), false)
	if err != nil {
		_ = write.Close()
		return nil, fmt.Errorf("store: opening %s: %w", abs, err)
	}
	s := &Store{path: abs, redact: o.redact, write: write, read: read}

	if err := migrate(ctx, write, schema); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

// poolDSN is the connection string for one pool. The writer pool takes the
// database lock when its transaction begins rather than when it first writes,
// so a read-modify-write inside a transaction never has to be retried.
func poolDSN(path string, writer bool) string {
	dsn := "file:" + url.PathEscape(path) +
		"?_pragma=busy_timeout(" + fmt.Sprint(busyTimeout.Milliseconds()) + ")" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=foreign_keys(1)" +
		"&_pragma=synchronous(NORMAL)"
	if writer {
		dsn += "&_txlock=immediate"
	}
	return dsn
}

// openPool builds one connection pool from dsn and closes it again rather than
// return one whose connection does not report the settings requiredSettings
// asks for. A single pool holds one connection, so its writers queue rather
// than contend.
func openPool(ctx context.Context, dsn string, single bool) (*sql.DB, error) {
	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, err
	}
	if single {
		db.SetMaxOpenConns(1)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := verifySettings(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// setting is one connection setting this package asks for and then reads back.
type setting struct {
	// name is the pragma, which is also how the refusal names it.
	name string
	// want is the value the store was opened asking for, compared to what the
	// connection reports without regard to case.
	want string
}

// requiredSettings are the settings the guarantees in the package comment rest
// on. They are asked for in the DSN and read back from the connection, because
// a setting that is asked for and not applied leaves this package describing a
// database it does not have.
func requiredSettings() []setting {
	return []setting{
		{name: "journal_mode", want: "wal"},
		{name: "synchronous", want: "1"},
		{name: "busy_timeout", want: strconv.FormatInt(busyTimeout.Milliseconds(), 10)},
		{name: "foreign_keys", want: "1"},
	}
}

// verifySettings reads each required setting back from db and returns
// ErrSettingNotApplied naming the first that is not the value asked for.
//
// What it buys this package is that the connection it drew reports the
// configuration the package comment describes, rather than that configuration
// being assumed from the fact that opening returned no error. Its scope is that
// one connection: journal_mode is a property of the database file, so reading
// it says which mode the file is in, while busy_timeout, foreign_keys, and
// synchronous belong to a connection, and a pool that opens another one later
// is outside what this establishes.
//
// The pragma name is concatenated into the statement. It never comes from a
// caller: it is one of the in-package literals above.
func verifySettings(ctx context.Context, db *sql.DB) error {
	for _, s := range requiredSettings() {
		var got string
		if err := db.QueryRowContext(ctx, "PRAGMA "+s.name).Scan(&got); err != nil {
			return fmt.Errorf("store: reading %s back: %w", s.name, err)
		}
		if !strings.EqualFold(got, s.want) {
			return fmt.Errorf("%w: %s was opened as %s and reports %s",
				ErrSettingNotApplied, s.name, s.want, got)
		}
	}
	return nil
}

// redactorProbe is a URL of the shape PRD section 8 requires stored without its
// credential, and probeSecret is the part that may not survive redaction.
const (
	redactorProbe = "https://probe-user:probe-secret@redactor.probe.invalid/probe.git"
	probeSecret   = "probe-secret"
)

// probeRedactor refuses a redactor that leaves the probe's credential intact.
//
// It establishes one thing: the redactor that will run on the repository URL
// columns is not inert. That is the failure a caller cannot see for themselves,
// because a redactor that was never wired up produces a store that looks
// completely normal and quietly holds passwords.
//
// It does not establish that the redactor removes every credential. One probe
// of one shape cannot, and deciding which shapes count belongs to the redactor,
// which P14 makes the single owner of the question.
func probeRedactor(r vcs.Redactor) error {
	if strings.Contains(r.Redact(redactorProbe), probeSecret) {
		return ErrRedactorInert
	}
	return nil
}

// Path is the absolute path of the database file.
func (s *Store) Path() string { return s.path }

// Close releases both connection pools. It is safe to call more than once, and
// every accessor called afterwards returns ErrClosed.
func (s *Store) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		if err := s.write.Close(); err != nil {
			s.closeErr = err
		}
		if err := s.read.Close(); err != nil && s.closeErr == nil {
			s.closeErr = err
		}
	})
	return s.closeErr
}

// live reports ErrClosed once Close has run, so an accessor fails with this
// package's own condition rather than with whatever the driver says about a
// closed pool.
func (s *Store) live() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return ErrClosed
	}
	return nil
}

// inTx runs fn inside one write transaction. Every accessor that changes a row
// goes through it, so a read-modify-write is atomic against another writer
// without any accessor having to remember that.
func (s *Store) inTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	if err := s.live(); err != nil {
		return err
	}
	tx, err := s.write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// timeLayout is how instants are stored. Text in UTC with nanosecond precision
// sorts lexicographically in the same order it sorts chronologically, and it
// does not depend on a driver's mapping of a date type.
const timeLayout = "2006-01-02T15:04:05.000000000Z07:00"

func nowUTC() time.Time { return time.Now().UTC() }

func encodeTime(t time.Time) string { return t.UTC().Format(timeLayout) }

func decodeTime(s string) (time.Time, error) {
	t, err := time.Parse(timeLayout, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("unparsable timestamp %q: %w", s, err)
	}
	return t.UTC(), nil
}
