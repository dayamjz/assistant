// Package storetest hands a test a database this build's store.Open already
// created, for a test that is about to open a home's database where none is
// yet.
//
// It exists for time and for nothing else. Creating a database applies every
// migration and compares the schema before and after each one, which costs
// over a second under the race detector against tens of milliseconds for
// opening a database already at this build's schema, and most tests that
// drive a home begin by creating one. The template is what store.Open itself
// wrote, so no test is handed a shape the real mechanism cannot produce; and
// the store.Open that follows the copy still checks the recorded migrations
// against this build's and refuses a database that disagrees, so a wrong
// template fails the test that opens it rather than passing unnoticed. The
// redactor is not part of what is copied, because store.Open keeps it in
// memory and writes none of it to the file.
//
// Seeding is a choice a test makes at a helper, never something every home
// gets: creating a database from nothing stays exercised by internal/store's
// own tests and by every home a seeding helper does not reach.
//
// Its consumers are internal/service's tests and internal/cli's, which both
// open homes by the hundred; one copy is why it is a package. internal/store's
// own tests cannot import it, since it imports that package, so they keep the
// same arrangement in their helpers_test.go.
package storetest

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
)

// template is one database store.Open created and migrated, made once per
// test process on first use.
var template struct {
	once sync.Once
	dir  string
	path string
	err  error
}

// templatePath returns the template's path, making it on first use.
func templatePath() (string, error) {
	template.once.Do(func() {
		dir, err := standin.TempDir("dbtemplate")
		if err != nil {
			template.err = err
			return
		}
		template.dir = dir
		path := filepath.Join(dir, "state.db")
		records, err := store.Open(context.Background(), path, store.WithRedactor(redact.New()))
		if err != nil {
			template.err = err
			return
		}
		if err := records.Close(); err != nil {
			template.err = err
			return
		}
		template.path = path
	})
	return template.path, template.err
}

// Seed copies the template to path when nothing is there yet, with any
// write-ahead log the closed template left beside it, so the store.Open that
// follows finds a database at this build's schema. A path that already holds
// a database is left alone: a test reopening a home reads what is there. A
// path that cannot be read, or a template that cannot be made, fails the test.
func Seed(tb testing.TB, path string) {
	tb.Helper()
	if _, err := os.Stat(path); err == nil {
		return
	} else if !errors.Is(err, fs.ErrNotExist) {
		tb.Fatalf("reading %s before seeding it: %v", path, err)
	}
	source, err := templatePath()
	if err != nil {
		tb.Fatalf("making the database template: %v", err)
	}
	for _, suffix := range []string{"", "-wal"} {
		raw, err := os.ReadFile(source + suffix)
		if errors.Is(err, fs.ErrNotExist) && suffix != "" {
			continue
		}
		if err != nil {
			tb.Fatalf("reading the database template: %v", err)
		}
		info, err := os.Stat(source + suffix)
		if err != nil {
			tb.Fatalf("reading the database template's mode: %v", err)
		}
		if err := os.WriteFile(path+suffix, raw, info.Mode().Perm()); err != nil {
			tb.Fatalf("seeding %s: %v", path+suffix, err)
		}
	}
}

// Remove deletes the template, for a TestMain once every test has run. It is
// safe to call when no test made one.
func Remove() {
	if template.dir != "" {
		_ = os.RemoveAll(template.dir)
	}
}
