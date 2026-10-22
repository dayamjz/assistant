package feed_test

import (
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/supervisor/feed"
)

// PRD principle P10, for the queue's third shape: a kept-work wake about an
// entry no task names, unacknowledged, keeps work in flight in a home whose
// every task says nothing is, so the stop guard blocks a home whose lock no
// live loop holds; once the coordinator acknowledges it, nothing is in
// flight and the same home is allowed. The fleet here has no task at all,
// so only that wake can be what the guard counts.
func TestAnUnacknowledgedKeptWorkWakeKeepsWorkInFlight(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P10)
	h := createdHome(t)
	var wake int64
	withDatabase(t, h, func(s *store.Store) {
		ctx := t.Context()
		if _, err := s.UpsertRepository(ctx, store.Repository{ID: "subject", WorkingPath: t.TempDir(), UpstreamURL: "/up.git", DefaultBranch: "main"}); err != nil {
			t.Fatal(err)
		}
		build, err := store.CurrentBuild()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.CreateRun(ctx, store.Run{ID: "run-pushed", RepositoryID: "subject", Branch: "work", SubmittedHead: "aaaa",
			Base: "bbbb", Intent: "validate", IntentSource: "push", ConfigDigest: "cfg", Build: build}); err != nil {
			t.Fatal(err)
		}
		entry, _, err := s.RecordKeptWork(ctx, store.KeptCopy{RunID: "run-pushed", CopyPath: "/worktrees/run-pushed",
			Holds: []string{"aaaa"}, Reason: "unproven", Against: "dddd"})
		if err != nil {
			t.Fatal(err)
		}
		inc, err := s.BeginIncarnation(ctx)
		if err != nil {
			t.Fatal(err)
		}
		appended, err := s.AppendKeptWorkWake(ctx, inc, store.KeptWorkWakeRequest{KeptWork: entry.ID, Kind: "kept-work", Marker: "kept-work:1"})
		if err != nil {
			t.Fatal(err)
		}
		wake = appended.Wake.ID
	})
	fresh := supervisor.FreshnessFor(10 * time.Second)
	writeInstance(t, h, instance)
	writeBeacon(t, h, supervisor.Identity{Instance: instance, Epoch: 1}, time.Now())
	if got := feed.Guard(t.Context(), h, fresh, time.Now()); got.Reason != supervisor.StopLockFree {
		t.Fatalf("with the kept-work wake queued and no loop holding the lock the guard answered %s, want lock-free", got.Reason)
	}
	withDatabase(t, h, func(s *store.Store) {
		if err := s.AcknowledgeAll(t.Context(), nil, nil, []int64{wake}, store.AcknowledgedByCoordinator()); err != nil {
			t.Fatal(err)
		}
	})
	if got := feed.Guard(t.Context(), h, fresh, time.Now()); got.Reason != supervisor.StopNothingInFlight {
		t.Fatalf("with the kept-work wake acknowledged the guard answered %s, want nothing-in-flight", got.Reason)
	}
}
