package feed_test

import (
	"os"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/supervisor/feed"
)

// instance is the identity the tests' service goes by, in the shape
// internal/service mints.
const instance = "0123456789abcdef"

// createdHome is a home that exists on disk with its layout made.
func createdHome(t *testing.T) *home.Home {
	t.Helper()
	root, err := standin.TempDir("feed")
	if err != nil {
		t.Fatalf("making a root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	h, err := home.Open(root)
	if err != nil {
		t.Fatalf("opening the home: %v", err)
	}
	if err := h.Create(); err != nil {
		t.Fatalf("creating the home: %v", err)
	}
	return h
}

// withDatabase opens the home's database the way a service does, so it exists
// in the mode the read-only open requires, and leaves it closed.
func withDatabase(t *testing.T, h *home.Home, fill func(s *store.Store)) {
	t.Helper()
	s, err := store.Open(t.Context(), h.Database(), store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatalf("opening the database: %v", err)
	}
	if fill != nil {
		fill(s)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("closing the database: %v", err)
	}
}

// oneTaskInFlight plants one task, which resolves as queued, in a fresh
// database: the smallest fleet with work in flight.
func oneTaskInFlight(t *testing.T, h *home.Home) {
	t.Helper()
	withDatabase(t, h, func(s *store.Store) {
		if _, err := s.CreateTask(t.Context(), store.Task{
			ID: "abcdefghij23", Shape: store.TaskInvestigation, Project: "one",
			Mode: "headless", WorktreePath: "/worktrees/one",
		}, store.Disposition{}); err != nil {
			t.Fatalf("creating a task: %v", err)
		}
	})
}

// writeBeacon writes one beacon under id at written, through the real writer.
func writeBeacon(t *testing.T, h *home.Home, id supervisor.Identity, written time.Time) supervisor.Beacon {
	t.Helper()
	w, err := feed.OpenWriter(h.Beacon())
	if err != nil {
		t.Fatalf("opening the beacon: %v", err)
	}
	defer func() { _ = w.Close() }()
	b, err := w.Write(id, written)
	if err != nil {
		t.Fatalf("writing the beacon: %v", err)
	}
	return b
}

// writeInstance publishes id as the home's instance file.
func writeInstance(t *testing.T, h *home.Home, id string) {
	t.Helper()
	if err := h.WriteInstance(id); err != nil {
		t.Fatalf("writing the instance file: %v", err)
	}
}
