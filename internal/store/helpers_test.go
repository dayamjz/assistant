package store

import (
	"context"
	"path/filepath"
	"regexp"
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

// openStore opens a fresh store in a temporary directory.
func openStore(t *testing.T, opts ...Option) *Store {
	t.Helper()
	return openStoreAt(t, filepath.Join(t.TempDir(), "state.db"), opts...)
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
	}, "queued", "intake")
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return task
}
