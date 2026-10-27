package feed_test

import (
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/supervisor"
	"github.com/dayamjz/assistant/internal/supervisor/feed"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// PRD principle P10, for the queue's fourth shape: an unacknowledged
// housekeeping wake about a repository keeps work in flight in a home whose
// every task says nothing is, so the stop guard blocks a home no live loop
// holds; once acknowledged, nothing is in flight. The fleet has no task, so
// only that wake can be what the guard counts.
func TestAnUnacknowledgedHousekeepingWakeKeepsWorkInFlight(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P10)
	h := createdHome(t)
	var wake int64
	withDatabase(t, h, func(s *store.Store) {
		ctx := t.Context()
		if _, err := s.UpsertRepository(ctx, store.Repository{ID: "subject", WorkingPath: t.TempDir(), UpstreamURL: "/up.git", DefaultBranch: "main"}); err != nil {
			t.Fatal(err)
		}
		inc, err := s.BeginIncarnation(ctx)
		if err != nil {
			t.Fatal(err)
		}
		appended, err := s.AppendHousekeepingWake(ctx, inc, store.HousekeepingWakeRequest{Repository: "subject",
			Kind: string(supervisor.KindHousekeepingDue), Marker: "due:none:1", Detail: "no pass was ever created"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		wake = appended.Wake.ID
	})
	fresh := supervisor.FreshnessFor(10 * time.Second)
	writeInstance(t, h, instance)
	writeBeacon(t, h, supervisor.Identity{Instance: instance, Epoch: 1}, time.Now())
	if got := feed.Guard(t.Context(), h, fresh, time.Now()); got.Reason != supervisor.StopLockFree {
		t.Fatalf("with the housekeeping wake queued and no loop holding the lock the guard answered %s, want lock-free", got.Reason)
	}
	withDatabase(t, h, func(s *store.Store) {
		if err := s.AcknowledgeAll(t.Context(), nil, nil, nil, []int64{wake}, store.AcknowledgedByCoordinator()); err != nil {
			t.Fatal(err)
		}
	})
	if got := feed.Guard(t.Context(), h, fresh, time.Now()); got.Reason != supervisor.StopNothingInFlight {
		t.Fatalf("with the housekeeping wake acknowledged the guard answered %s, want nothing-in-flight", got.Reason)
	}
}

// PRD principle P10 and section 6's housekeeping: a promoted pass that
// resolves Held from an open report decision while its run still validates
// is in flight whatever it resolves, with every wake acknowledged, because
// its run moves it, so the stop guard counts it; once the run has ended and
// the pass's latest attempt never had a session, nothing moves it and the
// same home is allowed. The positive control is that same Held task without
// the mark, which nothing keeps in flight.
func TestAPassSomethingMovesIsInFlightWhateverItResolves(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P10)
	h := createdHome(t)
	const pass = "taskhkfeedaa"
	withDatabase(t, h, func(s *store.Store) {
		ctx := t.Context()
		if _, err := s.UpsertRepository(ctx, store.Repository{ID: "subject", WorkingPath: t.TempDir(), UpstreamURL: "/up.git", DefaultBranch: "main"}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.CreateTask(ctx, store.Task{ID: pass, Shape: store.TaskInvestigation, Project: "one", WorktreePath: "/w",
			RepositoryID: store.Known("subject"), Role: config.RoleHousekeeping, Housekeeping: true},
			store.Disposition{Delivered: taskstate.Delivered}); err != nil {
			t.Fatal(err)
		}
		events, err := s.TaskEvents(ctx, pass, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.TransitionTaskShape(ctx, pass, store.TaskInvestigation, store.TaskDelivery, "subject", "housekeeping/one",
			config.DeliveryGated, "Correct the drift.", events[len(events)-1].Sequence, "promoted", store.ResolvedByMachineInterface(), nil); err != nil {
			t.Fatal(err)
		}
		if _, err := s.BeginFirstTaskAttempt(ctx, pass, store.OnThisMachine()); err != nil {
			t.Fatal(err)
		}
		time.Sleep(25 * time.Millisecond)
		build, err := store.CurrentBuild()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.CreateRun(ctx, store.Run{ID: "run-pass", RepositoryID: "subject", Branch: "housekeeping/one", SubmittedHead: "aaaa",
			Base: "main", Intent: "validate", IntentSource: "task", ConfigDigest: "cfg", Build: build}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.TransitionRun(ctx, "run-pass", []store.RunStatus{store.RunPending}, store.RunRunning); err != nil {
			t.Fatal(err)
		}
		if _, err := s.RegisterHold(ctx, store.Hold{Key: "task/" + pass + "/finding/f1", TaskID: store.Known(pass),
			Subject: "a report decision", Detail: "d"}); err != nil {
			t.Fatal(err)
		}
	})
	fresh := supervisor.FreshnessFor(10 * time.Second)
	writeInstance(t, h, instance)
	writeBeacon(t, h, supervisor.Identity{Instance: instance, Epoch: 1}, time.Now())
	if got := feed.Guard(t.Context(), h, fresh, time.Now()); got.Reason != supervisor.StopLockFree {
		t.Fatalf("with the pass's run validating the guard answered %s, want lock-free", got.Reason)
	}
	withDatabase(t, h, func(s *store.Store) {
		if _, err := s.TransitionRun(t.Context(), "run-pass", []store.RunStatus{store.RunRunning}, store.RunFailed); err != nil {
			t.Fatal(err)
		}
	})
	if got := feed.Guard(t.Context(), h, fresh, time.Now()); got.Reason != supervisor.StopNothingInFlight {
		t.Fatalf("with the pass's run ended and no session the guard answered %s, want nothing-in-flight", got.Reason)
	}
}
