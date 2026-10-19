package storetest_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/store/storetest"
)

func TestMain(m *testing.M) {
	code := m.Run()
	storetest.Remove()
	os.Exit(code)
}

// A seeded path holds a database the real store.Open accepts and writes to,
// which is the whole of what a seeding helper promises its caller.
func TestASeededDatabaseIsOneStoreOpenAccepts(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "state.db")
	storetest.Seed(t, path)
	records, err := store.Open(context.Background(), path, store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatalf("opening the seeded database: %v", err)
	}
	t.Cleanup(func() { _ = records.Close() })
	if _, err := records.UpsertRepository(context.Background(), store.Repository{
		ID: "repo-1", WorkingPath: "/checkouts/one", UpstreamURL: "https://example.test/one.git", DefaultBranch: "main",
	}); err != nil {
		t.Fatalf("writing to the seeded database: %v", err)
	}
}

// A path that already holds a database is left as it is, so a test reopening
// a home reads what that home recorded rather than an empty template.
func TestSeedLeavesADatabaseThatIsAlreadyThere(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "state.db")
	records, err := store.Open(context.Background(), path, store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatalf("creating the database: %v", err)
	}
	if _, err := records.UpsertRepository(context.Background(), store.Repository{
		ID: "repo-1", WorkingPath: "/checkouts/one", UpstreamURL: "https://example.test/one.git", DefaultBranch: "main",
	}); err != nil {
		t.Fatalf("recording a repository: %v", err)
	}
	if err := records.Close(); err != nil {
		t.Fatalf("closing: %v", err)
	}

	storetest.Seed(t, path)

	reopened, err := store.Open(context.Background(), path, store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatalf("reopening: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if _, err := reopened.Repository(context.Background(), "repo-1"); err != nil {
		t.Fatalf("the recorded repository did not survive seeding: %v", err)
	}
}
