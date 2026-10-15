package service

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
)

// databaseTemplate is one database this build's store.Open created and
// migrated, made once per test process and copied wherever a test is about
// to open a database that does not exist yet.
//
// It exists for time and for nothing else. Creating a database applies every
// migration and compares the schema before and after each one, which costs
// over a second under the race detector against tens of milliseconds for
// opening a database already at this build's schema, and most tests here
// begin by creating one. The copy is what store.Open itself wrote, so no
// test is handed a shape the real mechanism cannot produce; and the open
// that follows the copy still checks the recorded migrations against this
// build's and refuses a database that disagrees, so a wrong template fails
// the test that opens it rather than passing unnoticed. Creating a database
// from nothing is still exercised: internal/store's own tests do it, and so
// does every test here whose service opens a home nobody seeded.
var databaseTemplate struct {
	once sync.Once
	dir  string
	path string
	err  error
}

// templateDatabase returns the template's path, making it on first use.
func templateDatabase() (string, error) {
	databaseTemplate.once.Do(func() {
		dir, err := os.MkdirTemp("", "dbtemplate")
		if err != nil {
			databaseTemplate.err = err
			return
		}
		databaseTemplate.dir = dir
		path := filepath.Join(dir, "state.db")
		records, err := store.Open(context.Background(), path, store.WithRedactor(redact.New()))
		if err != nil {
			databaseTemplate.err = err
			return
		}
		if err := records.Close(); err != nil {
			databaseTemplate.err = err
			return
		}
		databaseTemplate.path = path
	})
	return databaseTemplate.path, databaseTemplate.err
}

// removeDatabaseTemplate removes the template, for TestMain once every test
// has run.
func removeDatabaseTemplate() {
	if databaseTemplate.dir != "" {
		_ = os.RemoveAll(databaseTemplate.dir)
	}
}

// seedDatabase copies the template to path when nothing is there yet, with
// any write-ahead log the closed template left beside it, so the store.Open
// that follows finds a database at this build's schema. A path that already
// holds a database is left alone: a test reopening a home reads what is
// there.
func seedDatabase(tb testing.TB, path string) {
	tb.Helper()
	if _, err := os.Stat(path); err == nil {
		return
	} else if !errors.Is(err, fs.ErrNotExist) {
		tb.Fatalf("reading %s before seeding it: %v", path, err)
	}
	template, err := templateDatabase()
	if err != nil {
		tb.Fatalf("making the database template: %v", err)
	}
	for _, suffix := range []string{"", "-wal"} {
		raw, err := os.ReadFile(template + suffix)
		if errors.Is(err, fs.ErrNotExist) && suffix != "" {
			continue
		}
		if err != nil {
			tb.Fatalf("reading the database template: %v", err)
		}
		info, err := os.Stat(template + suffix)
		if err != nil {
			tb.Fatalf("reading the database template's mode: %v", err)
		}
		if err := os.WriteFile(path+suffix, raw, info.Mode().Perm()); err != nil {
			tb.Fatalf("seeding %s: %v", path+suffix, err)
		}
	}
}
