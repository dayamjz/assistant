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

// A checkout stands for one repository record. The refusing side of that rule
// is a sentinel a caller branches on rather than a driver's constraint text,
// and a refused upsert writes nothing.
func TestUpsertRepositoryRefusesATakenWorkingPath(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)

	first, err := s.UpsertRepository(ctx, Repository{
		ID: "repo-1", WorkingPath: "/checkouts/one",
		UpstreamURL: "https://example.test/one.git", DefaultBranch: "main",
	})
	if err != nil {
		t.Fatalf("UpsertRepository: %v", err)
	}

	_, err = s.UpsertRepository(ctx, Repository{
		ID: "repo-2", WorkingPath: "/checkouts/one",
		UpstreamURL: "https://example.test/two.git", DefaultBranch: "main",
	})
	if !errors.Is(err, ErrWorkingPathTaken) {
		t.Fatalf("a second identifier claimed a checkout that was already taken: %v", err)
	}
	if !strings.Contains(err.Error(), "/checkouts/one") || !strings.Contains(err.Error(), "repo-1") {
		t.Fatalf("the refusal does not name the path and the record that holds it: %v", err)
	}

	// The refusal left nothing of its own behind and did not disturb the
	// record that holds the path.
	if _, err := s.Repository(ctx, "repo-2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the refused upsert left a row behind: %v", err)
	}
	all, err := s.Repositories(ctx)
	if err != nil {
		t.Fatalf("Repositories: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("the refused upsert made %d repositories, want 1", len(all))
	}
	held, err := s.Repository(ctx, "repo-1")
	if err != nil {
		t.Fatalf("Repository: %v", err)
	}
	if held.UpstreamURL != first.UpstreamURL || !held.UpdatedAt.Equal(first.UpdatedAt) {
		t.Fatalf("the refused upsert changed the repository holding the path: %+v then %+v", first, held)
	}

	// The accepting side, twice over: the identifier that holds the path can
	// name it again, which is the ordinary update, and another identifier with
	// its own checkout is written, so the refusal is about the path rather
	// than about arriving second.
	again, err := s.UpsertRepository(ctx, Repository{
		ID: "repo-1", WorkingPath: "/checkouts/one",
		UpstreamURL: "https://example.test/one.git", DefaultBranch: "trunk",
	})
	if err != nil {
		t.Fatalf("UpsertRepository refused the identifier that holds the path: %v", err)
	}
	if again.DefaultBranch != "trunk" {
		t.Fatalf("the update did not take: %+v", again)
	}
	if !again.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("the update changed CreatedAt from %s to %s", first.CreatedAt, again.CreatedAt)
	}
	if _, err := s.UpsertRepository(ctx, Repository{
		ID: "repo-2", WorkingPath: "/checkouts/two",
		UpstreamURL: "https://example.test/two.git", DefaultBranch: "main",
	}); err != nil {
		t.Fatalf("UpsertRepository refused a repository with a checkout of its own: %v", err)
	}
}

// The lookup by working path is the one every caller asks, so it has to answer
// the record for that path, tell a path it does not hold apart from a failure,
// and give back the same redacted form every other accessor does.
func TestRepositoryAtAnswersByWorkingPath(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)

	stored, err := s.UpsertRepository(ctx, Repository{
		ID:            "repo-1",
		WorkingPath:   "/checkouts/one",
		UpstreamURL:   credentialed,
		DefaultBranch: "main",
	})
	if err != nil {
		t.Fatalf("UpsertRepository: %v", err)
	}
	if _, err := s.UpsertRepository(ctx, Repository{
		ID:            "repo-2",
		WorkingPath:   "/checkouts/two",
		UpstreamURL:   "https://example.test/two.git",
		DefaultBranch: "trunk",
	}); err != nil {
		t.Fatalf("UpsertRepository: %v", err)
	}

	found, err := s.RepositoryAt(ctx, "/checkouts/one")
	if err != nil {
		t.Fatalf("RepositoryAt: %v", err)
	}
	if found.ID != stored.ID || found.DefaultBranch != "main" {
		t.Fatalf("RepositoryAt(/checkouts/one) answered %+v, want repo-1 on main", found)
	}
	if found.UpstreamURL != stored.UpstreamURL {
		t.Fatalf("RepositoryAt gave %q, want the stored redacted form %q", found.UpstreamURL, stored.UpstreamURL)
	}

	// A path no record holds is not there rather than a failure to read, which
	// is the difference a caller branches on.
	if _, err := s.RepositoryAt(ctx, "/checkouts/three"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("RepositoryAt for a path with no record gave %v, want ErrNotFound", err)
	}
}
