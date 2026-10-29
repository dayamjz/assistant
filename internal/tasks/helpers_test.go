package tasks

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/store/storetest"
	"github.com/dayamjz/assistant/internal/taskstate"
	"github.com/dayamjz/assistant/internal/vcs"
)

const (
	repo   = "repo-1"
	branch = "topic"
)

// fixture is a queue over a fresh store and home, with the knobs a test turns.
type fixture struct {
	home  *home.Home
	store *store.Store
	queue *Queue
	// inGate is what BranchInGate answers, per branch.
	inGate map[string]bool
	// posture is what the posture reader answers for the project, and
	// postureErr, when set, fails it instead, as an unreadable trusted copy
	// does. postureAsked counts the reads; it is atomic because two intakes
	// under the exclusion test read the posture from two goroutines.
	posture      config.Delivery
	postureErr   error
	postureAsked atomic.Int32
	// exclusion is the per-branch exclusion the queue takes. It is a keyed
	// mutex in the shape of internal/service's branch gate, so a test here
	// establishes what the queue does under one without opening a service.
	exclusion *keyedExclusion
	// scope is what the housekeeping scope reader answers, the trusted
	// housekeeping.paths, and scopeErr, when set, fails it instead.
	scope    config.PatternSet
	scopeErr error
}

// keyedExclusion is one mutex per repository and branch.
type keyedExclusion struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
	// taken counts how many holds were asked for, so a test can tell the
	// queue asked for one.
	taken int
	// arrived is closed once expect callers have asked for a hold, so a test
	// can keep the first caller between its read and its write until the
	// second is queued at the exclusion.
	expect  int
	arrived chan struct{}
}

func (k *keyedExclusion) hold(_ context.Context, repository, branch string) (func(), error) {
	k.mu.Lock()
	key := repository + "\x00" + branch
	m, ok := k.locks[key]
	if !ok {
		m = &sync.Mutex{}
		k.locks[key] = m
	}
	k.taken++
	if k.arrived != nil && k.taken == k.expect {
		close(k.arrived)
	}
	k.mu.Unlock()
	m.Lock()
	return m.Unlock, nil
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	h, err := home.Open(root)
	if err != nil {
		t.Fatalf("home.Open: %v", err)
	}
	if err := h.Create(); err != nil {
		t.Fatalf("home.Create: %v", err)
	}
	// Seeded for time alone, as internal/store/storetest's comment says.
	storetest.Seed(t, filepath.Join(root, "state.db"))
	s, err := store.Open(ctx, filepath.Join(root, "state.db"), store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err := s.UpsertRepository(ctx, store.Repository{
		ID: repo, WorkingPath: filepath.Join(root, "checkout"), UpstreamURL: "https://example.test/one.git", DefaultBranch: "main",
	}); err != nil {
		t.Fatalf("UpsertRepository: %v", err)
	}
	f := &fixture{home: h, store: s, inGate: map[string]bool{}, exclusion: &keyedExclusion{locks: map[string]*sync.Mutex{}},
		posture: config.DeliveryGated, scope: config.Defaults().HousekeepingPaths}
	f.queue, err = New(Deps{
		Home: h, Store: s, Reader: taskstate.NewReader(s, nil, nil),
		Exclude: f.exclusion.hold,
		BranchInGate: func(_ context.Context, r store.Repository, b string) (bool, error) {
			return f.inGate[r.ID+"\x00"+b], nil
		},
		GatePath: func(_ context.Context, r store.Repository) (string, error) {
			return filepath.Join(root, "repos", r.ID+".git"), nil
		},
		Posture: func(context.Context, store.Repository, string) (config.Delivery, error) {
			f.postureAsked.Add(1)
			return f.posture, f.postureErr
		},
		Housekeeping: func(context.Context, store.Repository, string) (config.PatternSet, error) {
			return f.scope, f.scopeErr
		},
		GitDeadline: vcs.DefaultInvocationDeadline,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return f
}

// delivery is a well-formed delivery request on the fixture's branch.
func delivery(overlaps string) Request {
	return Request{Shape: "delivery", Project: "one", Instructions: "do the thing", Mode: "interactive",
		Repository: repo, Branch: branch, Overlaps: overlaps}
}

// create is Create or a fatal failure.
func (f *fixture) create(t *testing.T, req Request) Created {
	t.Helper()
	got, err := f.queue.Create(context.Background(), req)
	if err != nil {
		t.Fatalf("Create(%+v): %v", req, err)
	}
	return got
}

// resolve reads a task's state the way every caller does: through the
// resolver, never off the history.
func (f *fixture) resolve(t *testing.T, id string) taskstate.Resolution {
	t.Helper()
	got, err := taskstate.NewReader(f.store, nil, nil).Task(context.Background(), id)
	if err != nil {
		t.Fatalf("resolving %s: %v", id, err)
	}
	return got
}

// beginAttempt records the first attempt of a task, which is what makes a run
// of its branch its own.
func (f *fixture) beginAttempt(t *testing.T, id string) {
	t.Helper()
	if _, err := f.store.BeginFirstTaskAttempt(context.Background(), id, store.OnThisMachine()); err != nil {
		t.Fatalf("BeginTaskAttempt: %v", err)
	}
}

// runOn records a run of the fixture's branch and moves it to the status
// given, through the moves a real run takes.
func (f *fixture) runOn(t *testing.T, id string, to store.RunStatus) store.Run {
	t.Helper()
	ctx := context.Background()
	run, err := f.store.CreateRun(ctx, store.Run{
		ID: id, RepositoryID: repo, Branch: branch, SubmittedHead: "aaaa", Base: "bbbb",
		Intent: "validate", IntentSource: "push", ConfigDigest: "cfg",
		Build: store.Build{Version: "v0", Revision: "0123456789abcdef", Go: "go1.25.0"},
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if to == store.RunPending {
		return run
	}
	if run, err = f.store.TransitionRun(ctx, id, []store.RunStatus{store.RunPending}, store.RunRunning); err != nil {
		t.Fatalf("TransitionRun running: %v", err)
	}
	if to == store.RunRunning {
		return run
	}
	if run, err = f.store.TransitionRun(ctx, id, []store.RunStatus{store.RunRunning}, to); err != nil {
		t.Fatalf("TransitionRun %s: %v", to, err)
	}
	return run
}

// dispatchedEvents counts a task's dispatched entries, reading its whole
// history: this is accounting over what was written, not a reading of state.
func (f *fixture) dispatchedEvents(t *testing.T, id string) int {
	t.Helper()
	events, err := f.store.TaskEvents(context.Background(), id, 0, 0)
	if err != nil {
		t.Fatalf("TaskEvents: %v", err)
	}
	n := 0
	for _, e := range events {
		if e.Kind == store.DispatchedEventKind {
			n++
		}
	}
	return n
}

// instructionsOf reads the instructions file intake wrote.
func (f *fixture) instructionsOf(t *testing.T, id string) string {
	t.Helper()
	path, err := f.home.TaskInstructions(id)
	if err != nil {
		t.Fatalf("TaskInstructions: %v", err)
	}
	text, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the instructions: %v", err)
	}
	return string(text)
}

// barrier holds callers until n of them have arrived.
func barrier(n int) func() {
	var wg sync.WaitGroup
	wg.Add(n)
	return func() {
		wg.Done()
		wg.Wait()
	}
}

// race runs fn on two goroutines at once and returns both results.
func race(fn func() error) [2]error {
	var wg sync.WaitGroup
	var out [2]error
	start := make(chan struct{})
	for i := range out {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			out[i] = fn()
		}()
	}
	close(start)
	wg.Wait()
	return out
}
