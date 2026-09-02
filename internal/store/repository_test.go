package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

const credentialed = "https://alice:s3cr3t@example.test/one.git"

func TestUpsertRepositoryStoresRedactedURLs(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)

	stored, err := s.UpsertRepository(ctx, Repository{
		ID:            "repo-1",
		WorkingPath:   "/checkouts/one",
		UpstreamURL:   credentialed,
		ForkURL:       Known("ssh://bob:hunter2@fork.test/one.git"),
		DefaultBranch: "main",
	})
	if err != nil {
		t.Fatalf("UpsertRepository: %v", err)
	}

	fork, _ := stored.ForkURL.Get()
	for name, got := range map[string]string{"upstream": stored.UpstreamURL, "fork": fork} {
		if strings.Contains(got, "s3cr3t") || strings.Contains(got, "hunter2") {
			t.Fatalf("the %s URL came back with its credential intact: %s", name, got)
		}
		if !strings.Contains(got, "example.test") && !strings.Contains(got, "fork.test") {
			t.Fatalf("the %s URL lost its host: %s", name, got)
		}
	}

	// What the accessor returns is what the database holds, so a second reader
	// sees the same redacted form rather than the caller's original.
	reread, err := s.Repository(ctx, "repo-1")
	if err != nil {
		t.Fatalf("Repository: %v", err)
	}
	if reread.UpstreamURL != stored.UpstreamURL {
		t.Fatalf("re-reading gave %q, want %q", reread.UpstreamURL, stored.UpstreamURL)
	}
	if strings.Contains(reread.UpstreamURL, "s3cr3t") {
		t.Fatalf("the stored upstream URL carries a credential: %s", reread.UpstreamURL)
	}
}

// The refusing side of the same rule. A redactor that does nothing is what a
// caller has when its redactor was never wired up or was replaced by something
// inert, and the store has to refuse to open rather than quietly hold
// passwords.
func TestOpenRefusesAnInertRedactor(t *testing.T) {
	_, err := Open(context.Background(), t.TempDir()+"/state.db", WithRedactor(inertRedactor()))
	if !errors.Is(err, ErrRedactorInert) {
		t.Fatalf("Open accepted a redactor that removes nothing: %v", err)
	}
}

// A probe that only ever passes is not a probe. A redactor that removes the
// credential opens, which is what the rest of these tests rely on.
func TestOpenAcceptsAWorkingRedactor(t *testing.T) {
	s, err := Open(context.Background(), t.TempDir()+"/state.db", WithRedactor(workingRedactor()))
	if err != nil {
		t.Fatalf("Open refused a working redactor: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestOpenRequiresARedactor(t *testing.T) {
	_, err := Open(context.Background(), t.TempDir()+"/state.db")
	if !errors.Is(err, ErrNoRedactor) {
		t.Fatalf("Open succeeded without a redactor: %v", err)
	}
}

func TestUpsertRepositoryUpdatesInPlace(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)

	first, err := s.UpsertRepository(ctx, Repository{
		ID: "repo-1", WorkingPath: "/checkouts/one",
		UpstreamURL: "https://example.test/one.git", DefaultBranch: "main",
	})
	if err != nil {
		t.Fatalf("UpsertRepository: %v", err)
	}
	if first.ForkURL.IsKnown() {
		t.Fatalf("a repository with no fork reported one: %v", first.ForkURL)
	}

	second, err := s.UpsertRepository(ctx, Repository{
		ID: "repo-1", WorkingPath: "/checkouts/one",
		UpstreamURL: "https://example.test/one.git",
		ForkURL:     Known("https://example.test/fork.git"), DefaultBranch: "trunk",
	})
	if err != nil {
		t.Fatalf("UpsertRepository: %v", err)
	}
	if second.DefaultBranch != "trunk" {
		t.Fatalf("the update did not take: %+v", second)
	}
	if !second.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("the update changed CreatedAt from %s to %s", first.CreatedAt, second.CreatedAt)
	}
	all, err := s.Repositories(ctx)
	if err != nil {
		t.Fatalf("Repositories: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("upserting the same identifier made %d rows", len(all))
	}
}

func TestRepositoryRefusesIncompleteRecords(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	complete := Repository{ID: "repo-1", WorkingPath: "/checkouts/one",
		UpstreamURL: "https://example.test/one.git", DefaultBranch: "main"}

	cases := map[string]func(Repository) Repository{
		"no identifier":    func(r Repository) Repository { r.ID = " "; return r },
		"no working path":  func(r Repository) Repository { r.WorkingPath = ""; return r },
		"no branch":        func(r Repository) Repository { r.DefaultBranch = ""; return r },
		"no upstream":      func(r Repository) Repository { r.UpstreamURL = ""; return r },
		"empty fork given": func(r Repository) Repository { r.ForkURL = Known(""); return r },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := s.UpsertRepository(ctx, mutate(complete)); err == nil {
				t.Fatal("UpsertRepository accepted an incomplete repository")
			}
		})
	}
	if _, err := s.UpsertRepository(ctx, complete); err != nil {
		t.Fatalf("UpsertRepository refused a complete repository: %v", err)
	}
}
