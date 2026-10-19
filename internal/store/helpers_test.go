package store

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"testing"

	"github.com/dayamjz/assistant/internal/vcs"
)

// seamUserinfo is the shape the redaction seam in internal/vcs covers: the
// userinfo of a URL that carries a scheme. Tests supply a redactor of that
// shape rather than reaching into another package's unexported one.
var seamUserinfo = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.\-]*://)([^/@\s]+)@`)

// workingRedactor stands in for the credential remover a real caller supplies.
func workingRedactor() vcs.Redactor {
	return vcs.RedactorFunc(func(s string) string {
		return seamUserinfo.ReplaceAllString(s, "${1}REDACTED@")
	})
}

// inertRedactor returns its input unchanged. It is what a caller would have if
// its redactor were misconfigured, never wired up, or silently broken, and it
// is how the tests reach the probe in Open.
func inertRedactor() vcs.Redactor {
	return vcs.RedactorFunc(func(s string) string { return s })
}

// openStore opens a fresh store in a temporary directory, from a copy of the
// package's template database rather than from nothing. It is for every test
// whose subject is what a store at this build's schema does, which is nearly
// all of them; a test whose subject is what Open does to a file that is not
// there yet calls createStore or openStoreAt instead.
func openStore(t *testing.T, opts ...Option) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	seedDatabase(t, path)
	return openStoreAt(t, path, opts...)
}

// createStore opens a store at a path where nothing is yet, so Open creates
// the file and applies every migration to it. It is what openStore was before
// the template, kept for the tests whose subject is that creation.
func createStore(t *testing.T, opts ...Option) *Store {
	t.Helper()
	return openStoreAt(t, filepath.Join(t.TempDir(), "state.db"), opts...)
}

// databaseTemplate is one database Open created and migrated, made once per
// test process and copied wherever openStore is about to open one.
//
// It exists for time and for nothing else. Creating a database applies every
// migration and compares the schema before and after each one, which costs
// about a second under the race detector against tens of milliseconds for
// opening a database already at this build's schema, and most tests here
// begin by creating one. The copy is what Open itself wrote, so no test is
// handed a shape the real mechanism cannot produce; and the Open that follows
// the copy still checks the recorded migrations against this build's and
// refuses a database that disagrees, so a wrong template fails the test that
// opens it rather than passing unnoticed. Creating a database from nothing is
// still exercised: by the template itself, by createStore, and by every test
// that opens a path of its own through openStoreAt. Open keeps the redactor
// in memory and writes nothing of it to the file, so a test that opens the
// copy with a redactor of its own is held to that one. Packages outside
// this one copy the template internal/store/storetest owns rather than
// building their own; internal/service's seed_internal_test.go delegates to
// it.
var databaseTemplate struct {
	once sync.Once
	dir  string
	path string
	err  error
}

// templateDatabase returns the template's path, making it on first use.
func templateDatabase() (string, error) {
	databaseTemplate.once.Do(func() {
		dir, err := os.MkdirTemp("", "storetemplate")
		if err != nil {
			databaseTemplate.err = err
			return
		}
		databaseTemplate.dir = dir
		path := filepath.Join(dir, "state.db")
		records, err := Open(context.Background(), path, WithRedactor(workingRedactor()))
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

// seedDatabase copies the template to path, with any write-ahead log the
// closed template left beside it, so the Open that follows finds a database
// at this build's schema. It refuses a path that already holds something,
// because its one caller passes a path in a directory it has just made.
func seedDatabase(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("seeding %s, which is already there or cannot be read: %v", path, err)
	}
	template, err := templateDatabase()
	if err != nil {
		t.Fatalf("making the database template: %v", err)
	}
	for _, suffix := range []string{"", "-wal"} {
		raw, err := os.ReadFile(template + suffix)
		if errors.Is(err, fs.ErrNotExist) && suffix != "" {
			continue
		}
		if err != nil {
			t.Fatalf("reading the database template: %v", err)
		}
		if err := os.WriteFile(path+suffix, raw, 0o600); err != nil {
			t.Fatalf("seeding %s: %v", path+suffix, err)
		}
	}
}

// TestMain removes the template once every test has run.
func TestMain(m *testing.M) {
	code := m.Run()
	if databaseTemplate.dir != "" {
		_ = os.RemoveAll(databaseTemplate.dir)
	}
	os.Exit(code)
}

func openStoreAt(t *testing.T, path string, opts ...Option) *Store {
	t.Helper()
	if len(opts) == 0 {
		opts = []Option{WithRedactor(workingRedactor())}
	}
	s, err := Open(context.Background(), path, opts...)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// testBuild is a build identity that validates.
func testBuild() Build {
	return Build{Version: "v0.1.0", Revision: "0123456789abcdef", Modified: false, Go: "go1.25.0"}
}

// seedRun creates a repository and a run on it, and returns the run.
func seedRun(t *testing.T, s *Store) Run {
	t.Helper()
	ctx := context.Background()
	if _, err := s.UpsertRepository(ctx, Repository{
		ID:            "repo-1",
		WorkingPath:   "/checkouts/one",
		UpstreamURL:   "https://example.test/one.git",
		DefaultBranch: "main",
	}); err != nil {
		t.Fatalf("UpsertRepository: %v", err)
	}
	run, err := s.CreateRun(ctx, Run{
		ID:            "run-1",
		RepositoryID:  "repo-1",
		Branch:        "topic",
		SubmittedHead: "aaaa",
		Base:          "bbbb",
		Intent:        "validate",
		IntentSource:  "push",
		Build:         testBuild(),
		ConfigDigest:  "cfg-1",
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	return run
}

// seedTask creates a task and returns it.
func seedTask(t *testing.T, s *Store, id string) Task {
	t.Helper()
	task, err := s.CreateTask(context.Background(), Task{
		ID:           id,
		Shape:        TaskDelivery,
		Project:      "one",
		Mode:         "interactive",
		WorktreePath: "/worktrees/" + id,
	}, Disposition{})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return task
}

// incarnationOf begins a supervisor incarnation on s and returns it, which
// is what a loop write needs to pass the fence. Each call begins the next
// one, so a value from an earlier call is stale from then on; a test that
// needs to show the fence firing keeps the earlier value on purpose.
func incarnationOf(t *testing.T, s *Store) Incarnation {
	t.Helper()
	inc, err := s.BeginIncarnation(context.Background())
	if err != nil {
		t.Fatalf("BeginIncarnation: %v", err)
	}
	return inc
}
