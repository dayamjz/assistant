package service

import (
	"context"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/findings"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/pipeline"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/stages"
	"github.com/dayamjz/assistant/internal/store"
)

// A run ended through the protocol while a segment is inside a stage body is
// not picked up again, and this drives the whole path from the ending to the
// decision rather than either end of it.
//
// The two ends are checked apart elsewhere, and neither of those says they are
// wired together: a segment's release reports the ending, and an ending maps to
// a disposition nothing continues. Between them sits advance, which reads the
// one into the other, and carryOn, which dispatches on it. Either silently
// disconnected puts a cancelled run back to walking stage bodies.
//
// The second case is the one that discriminates, and the first is why it has
// to. cancel moves the record before it answers, so by the time an end-to-end
// test can look, attach's own reconcile refuses to resume a terminated run
// whatever carryOn decided - which is a second line of defence, not this one,
// and it makes the ordinary path pass with the guard removed. What the guard
// answers for is the window where the ending is recorded and the record has
// not moved yet, which is exactly the interleaving round one found. Holding the
// record still is how that window is made deterministic instead of raced.
//
// P6 is cited because a continuation there would execute nodes past a position
// the run was ended at, and the run's checkpoint history is what would then
// record work nobody asked for.
func TestARunEndedThroughTheProtocolIsNotCarriedOn(t *testing.T) {
	principles.Cite(t, principles.P6)

	t.Run("the record has moved by the time the segment returns", func(t *testing.T) {
		held := newHeldService(t)
		record := held.begin(t)

		if _, err := held.service.cancel(t.Context(), machine.CancelRequest{Run: record.ID}); err != nil {
			t.Fatalf("ending the run: %v", err)
		}
		held.let()
		held.awaitSegment(t)

		if got := held.status(t, record.ID); got != store.RunTerminated {
			t.Fatalf("the ended run is recorded as %s, want terminated", got)
		}
		held.assertNotResumed(t, record.ID)
	})

	t.Run("the ending is recorded before the record moves", func(t *testing.T) {
		held := newHeldService(t)
		record := held.begin(t)

		// The slot carries the ending and the record still says running,
		// which is what a continuation reading that record would resume from.
		// The ending is written here and never finished, so the record stays
		// where it is for the whole of the check below.
		held.service.endRun(record.ID)
		held.let()
		held.awaitSegment(t)

		if got := held.status(t, record.ID); got != store.RunRunning {
			t.Fatalf("the record moved to %s; this case is about the window before it does", got)
		}
		held.assertNotResumed(t, record.ID)
	})
}

// heldService is a service whose first stage body stays inside itself until
// the test lets it out, so a segment certainly holds the run's slot when the
// ending arrives rather than being caught there.
type heldService struct {
	service *Service
	inside  chan struct{}
	release chan struct{}
	let     func()
	// entries counts every entry into the stage body, including one a
	// continuation would make.
	entries atomic.Int64
	// returned is closed when the segment that begins the run has returned,
	// which is after its own carryOn has decided.
	returned chan struct{}
}

// begin starts a run and returns its record once the segment advancing it is
// inside the stage body.
func (h *heldService) begin(t *testing.T) store.Run {
	t.Helper()
	record, err := h.service.create(t.Context(), run{
		repository: "subject",
		branch:     "main",
		head:       "0000000000000000000000000000000000000000",
		intent:     "held open for the length of an ending",
		source:     intentSourceSupplied,
		supplied:   true,
	})
	if err != nil {
		t.Fatalf("recording a run: %v", err)
	}
	go func() {
		defer close(h.returned)
		// The context is the service's own, so nothing about this goroutine
		// going away ends the segment: what ends it is the ending under test.
		_, _ = h.service.begin(context.WithoutCancel(t.Context()), record, pipeline.Start{
			Branch:         record.Branch,
			Base:           "main",
			Submitted:      record.SubmittedHead,
			Intent:         record.Intent,
			IntentSupplied: true,
		})
	}()
	select {
	case <-h.inside:
	case <-time.After(30 * time.Second):
		t.Fatal("the run never reached the stage body")
	}
	return record
}

// awaitSegment waits for the segment that began the run to return, which is
// the point at which its carryOn has decided.
func (h *heldService) awaitSegment(t *testing.T) {
	t.Helper()
	select {
	case <-h.returned:
	case <-time.After(30 * time.Second):
		t.Fatal("the segment never returned after the stage body was let out")
	}
}

// status reads the run's recorded status straight from the store, without the
// reconciliation a read through the service would do.
func (h *heldService) status(t *testing.T, runID string) store.RunStatus {
	t.Helper()
	record, err := h.service.store.Run(t.Context(), runID)
	if err != nil {
		t.Fatalf("reading run %s: %v", runID, err)
	}
	return record.Status
}

// assertNotResumed fails if anything picked the run up after its segment
// ended. A continuation takes the run's slot and walks the graph from where it
// stood, so it reaches the stage body a second time; the body is released, so
// nothing holds one up, and the wait is what turns "not yet" into "not at all".
func (h *heldService) assertNotResumed(t *testing.T, runID string) {
	t.Helper()
	for range 25 {
		if h.entries.Load() > 1 {
			t.Fatalf("the stage body ran %d times: something carried on a run that was ended", h.entries.Load())
		}
		if h.service.isAdvancing(runID) {
			t.Fatal("a segment is advancing a run that was ended")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// newHeldService opens a service on a home of its own, with a repository
// recorded and a first stage body that waits to be let out.
func newHeldService(t *testing.T) *heldService {
	t.Helper()
	root, err := os.MkdirTemp("", "h")
	if err != nil {
		t.Fatalf("making a home root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	h, err := home.Open(root)
	if err != nil {
		t.Fatalf("opening the home: %v", err)
	}
	build, err := store.CurrentBuild()
	if err != nil {
		t.Fatalf("reading this build's identity: %v", err)
	}

	held := &heldService{
		inside:   make(chan struct{}),
		release:  make(chan struct{}),
		returned: make(chan struct{}),
	}
	var entered, released sync.Once
	held.let = func() { released.Do(func() { close(held.release) }) }
	served := stages.All()
	served.Intent = pipeline.Implementation{
		NewBody: func() pipeline.Body {
			return func(ctx context.Context, _ pipeline.Input) (pipeline.Output, error) {
				held.entries.Add(1)
				entered.Do(func() { close(held.inside) })
				select {
				case <-held.release:
				case <-ctx.Done():
					return pipeline.Output{}, ctx.Err()
				}
				return pipeline.Output{Report: findings.Report{
					Summary: "the stage was held open for the length of the ending",
					Findings: []findings.Finding{
						{ID: "stand-in", Action: findings.ActionAsk, Description: "a decision"},
					},
				}}, nil
			}
		},
	}

	running, err := Open(t.Context(), Options{
		Home:     h,
		Stages:   served,
		NewFixer: stages.PendingFixer,
		Build:    build,
		Catalog:  agents.NewCatalog(fixedRunner{runner: standin.New(t, standin.Script{}).Runner()}),
		LockWait: -1,
	})
	if err != nil {
		if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
			t.Skipf("%s has no local socket transport to serve this protocol over: %v", runtime.GOOS, err)
		}
		t.Fatalf("opening the service: %v", err)
	}
	held.service = running
	// Registered before the close below and so run before it: a failure while
	// the body is still held would otherwise leave the teardown waiting on a
	// stage nothing is going to release.
	t.Cleanup(func() {
		if err := running.Close(); err != nil {
			t.Errorf("closing the service: %v", err)
		}
	})
	t.Cleanup(held.let)

	if _, err := running.store.UpsertRepository(t.Context(), store.Repository{
		ID:            "subject",
		WorkingPath:   root,
		UpstreamURL:   "https://example.invalid/o/r.git",
		DefaultBranch: "main",
	}); err != nil {
		t.Fatalf("recording the repository: %v", err)
	}
	return held
}

// fixedRunner hands back a Runner somebody else built, so nothing here can
// produce an adapter the production path could not.
type fixedRunner struct{ runner agents.Runner }

func (f fixedRunner) Name() string { return f.runner.Name() }

func (f fixedRunner) New(context.Context, []string) (agents.Runner, error) { return f.runner, nil }
