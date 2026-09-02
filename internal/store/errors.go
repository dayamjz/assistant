package store

import (
	"errors"
	"fmt"
)

// ErrNotFound is returned by every accessor that reads one record by its
// identifier when no such record exists. It is a typed result a caller handles,
// not a condition to log and continue past.
var ErrNotFound = errors.New("store: record not found")

// ErrNoRedactor is returned by Open when no [vcs.Redactor] was supplied. PRD
// section 8 gives credential removal one owner, so this package refuses to open
// rather than invent a second implementation of it.
var ErrNoRedactor = errors.New("store: no redactor supplied, and this package does not implement one")

// ErrRedactorInert is returned by Open when the supplied redactor leaves the
// credential in a probe URL of the shape PRD section 8 requires removed. It is
// what a caller gets when its redactor was never wired up, or was replaced by
// something that does nothing. See the package comment for what the probe does
// and does not establish.
var ErrRedactorInert = errors.New("store: the supplied redactor left a credential in the probe URL untouched")

// ErrBuildIdentityMissing is returned by CreateRun when the run carries no
// usable build identity. PRD section 8 requires a surprising result to be
// traceable to the exact build and configuration that produced it, so a run
// that cannot name either is refused at creation rather than recorded and
// discovered later.
var ErrBuildIdentityMissing = errors.New("store: run has no build identity")

// ErrConfigDigestMissing is returned by CreateRun when the run names no
// configuration. It is the other half of what ErrBuildIdentityMissing covers.
var ErrConfigDigestMissing = errors.New("store: run has no configuration digest")

// ErrNoResolution is returned by ResolveHold when no resolution text was given.
// PRD section 8 says a hold is closed only by an explicit resolution, and an
// empty one is not explicit.
var ErrNoResolution = errors.New("store: a hold is closed only by an explicit resolution")

// ErrClosed is returned by every accessor called after Close.
var ErrClosed = errors.New("store: database is closed")

// MigrationError names the migration that failed and what went wrong. The
// database is unchanged by the failed migration: each migration is applied in
// one transaction together with the row recording it.
type MigrationError struct {
	// Version is the migration's version number.
	Version int
	// Name is the migration's name.
	Name string
	// Err is the underlying failure.
	Err error
}

// Error implements error.
func (e *MigrationError) Error() string {
	return fmt.Sprintf("store: migration %d (%s): %v", e.Version, e.Name, e.Err)
}

// Unwrap returns the underlying failure.
func (e *MigrationError) Unwrap() error { return e.Err }

// ErrNotAdditive is the failure a migration carries when it would change the
// shape of a table that already exists in a way old rows cannot survive. See
// the package comment for the rule and why it is checked against the database
// rather than against the migration text.
var ErrNotAdditive = errors.New("store: migration is not additive")

// ErrSchemaAhead is returned by Open when the database has been migrated by a
// newer build than this one. Running an older binary against it would read
// columns it does not know about and write rows a newer one would misread, so
// opening refuses rather than proceeding on a schema it cannot account for.
var ErrSchemaAhead = errors.New("store: database schema is newer than this build")

// ErrSchemaChanged is returned by Open when a migration already recorded in the
// database does not match the one in this build's list. A shipped migration has
// already run on somebody's machine and will not run again there, so editing
// one silently gives two databases different shapes under the same version
// number. A schema change is a new migration.
//
// What is compared is the migration's name, its statement count, and a digest
// of its statement text, so an edit made in place is caught rather than only a
// renamed or resized migration. The one row this cannot speak for is a
// migration recorded before this build's predecessor wrote digests: its digest
// is unknown, and for it the comparison is name and count alone.
var ErrSchemaChanged = errors.New("store: a recorded migration differs from this build's copy")

// ErrHoldResolved is returned by ResolveHold when the hold already carries a
// resolution. The second decision on a closed hold is a new decision and
// belongs to a new key, so this is a typed result a caller branches on rather
// than a failure of the write.
var ErrHoldResolved = errors.New("store: hold is already resolved")

// ErrMigrationsMalformed is returned when the migration list itself is wrong:
// versions that do not start at one or do not increase by one. It is a
// programming error in this package rather than a condition of the database.
var ErrMigrationsMalformed = errors.New("store: migration list is malformed")
