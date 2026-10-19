package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
)

// ceilingService is a service whose configuration is the operator's document
// doc, resolved the way Open resolves it, over a seeded store.
func ceilingService(t *testing.T, doc string) (*Service, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	_, cfg, _, err := resolveConfig(path)
	if err != nil {
		t.Fatalf("resolveConfig: %v", err)
	}
	database := filepath.Join(dir, "state.db")
	seedDatabase(t, database)
	st, err := store.Open(t.Context(), database, store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return &Service{store: st, cfg: cfg, registry: newRegistry()}, st
}

func seedSpend(t *testing.T, st *store.Store, started time.Time, tokens store.Optional[int64]) {
	t.Helper()
	if _, err := st.AppendAgentInvocation(t.Context(), store.AgentInvocation{
		Purpose: "review", Agent: "claude", SessionUse: "none",
		Started: started, InputTokens: tokens,
	}); err != nil {
		t.Fatalf("seeding: %v", err)
	}
}

// countingRunner counts how many times the inner run is reached.
type countingRunner struct {
	mu    sync.Mutex
	calls int
}

func (r *countingRunner) Name() string                      { return "counting" }
func (r *countingRunner) Capabilities() agents.Capabilities { return agents.Capabilities{} }
func (r *countingRunner) Run(context.Context, agents.Purpose, agents.Invocation) (agents.Result, error) {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	return agents.Result{}, nil
}

func (r *countingRunner) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

type countingSession struct{ *countingRunner }

func (r countingSession) Fixer(context.Context, string) (agents.Fixer, error) {
	return countingFixer(r), nil
}

type countingFixer struct{ *countingRunner }

func (f countingFixer) Apply(ctx context.Context, inv agents.Invocation) (agents.Result, error) {
	return f.Run(ctx, agents.PurposeFix, inv)
}

func (f countingFixer) Reference() string { return "counting-session" }

func ceilingOf(t *testing.T, err error) *machine.UsageCeilingError {
	t.Helper()
	var got *machine.UsageCeilingError
	if !errors.As(err, &got) {
		t.Fatalf("error %v, want a usage ceiling refusal", err)
	}
	return got
}

const ceilingDoc = "usage_ceiling:\n  window: 24h\n  input_tokens: 100\n"

// Recorded usage under the ceiling, and usage equal to it, starts the next
// invocation and the next fix round.
func TestAStartUnderTheUsageCeilingProceeds(t *testing.T) {
	t.Parallel()
	s, st := ceilingService(t, ceilingDoc)
	seedSpend(t, st, time.Now().UTC(), store.Known[int64](40))
	inner := &countingRunner{}
	wrapped := s.containRunner(countingSession{inner})
	if _, err := wrapped.Run(t.Context(), agents.PurposeReview, agents.Invocation{}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	fixer, err := wrapped.(agents.SessionRunner).Fixer(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixer.Apply(t.Context(), agents.Invocation{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if inner.count() != 2 {
		t.Fatalf("the inner runner was reached %d times, want both starts", inner.count())
	}

	equal, history := ceilingService(t, ceilingDoc)
	seedSpend(t, history, time.Now().UTC(), store.Known[int64](100))
	atCeiling := &countingRunner{}
	if _, err := equal.containRunner(atCeiling).Run(t.Context(), agents.PurposeReview, agents.Invocation{}); err != nil {
		t.Fatalf("a total equal to the ceiling: %v", err)
	}
	if atCeiling.count() != 1 {
		t.Fatal("a total equal to the ceiling did not start")
	}
}

// Recorded usage over the ceiling refuses the next start and the next fix
// round, and neither reaches the inner runner or the containment registry.
func TestAStartOverTheUsageCeilingIsRefused(t *testing.T) {
	t.Parallel()
	s, st := ceilingService(t, ceilingDoc)
	seedSpend(t, st, time.Now().UTC(), store.Known[int64](101))
	inner := &countingRunner{}
	wrapped := s.containRunner(countingSession{inner})
	_, runErr := wrapped.Run(t.Context(), agents.PurposeReview, agents.Invocation{})
	got := ceilingOf(t, runErr)
	if n, ok := got.Total.Get(); !ok || n != 101 || got.Ceiling != 100 || got.Unreported != 0 {
		t.Fatalf("Run refusal %+v, want 101 over 100", got)
	}
	fixer, err := wrapped.(agents.SessionRunner).Fixer(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixer.Apply(t.Context(), agents.Invocation{}); ceilingOf(t, err) == nil {
		t.Fatal("Apply was not refused")
	}
	if inner.count() != 0 {
		t.Fatalf("the inner runner was reached %d times", inner.count())
	}
	if !s.registry.empty() {
		t.Fatal("a refused start registered a process group")
	}
}

// A call that started outside the window is not part of the total. The same
// spend refuses a start when the window reaches it.
func TestTheUsageCeilingWindowBoundsTheHistory(t *testing.T) {
	t.Parallel()
	started := time.Now().UTC().Add(-2 * time.Hour)
	narrow, st := ceilingService(t, "usage_ceiling:\n  window: 1h\n  input_tokens: 50\n")
	seedSpend(t, st, started, store.Known[int64](100))
	inner := &countingRunner{}
	if _, err := narrow.containRunner(inner).Run(t.Context(), agents.PurposeReview, agents.Invocation{}); err != nil {
		t.Fatalf("a call outside the window refused the start: %v", err)
	}
	wide, other := ceilingService(t, "usage_ceiling:\n  window: 24h\n  input_tokens: 50\n")
	seedSpend(t, other, started, store.Known[int64](100))
	if _, err := wide.containRunner(&countingRunner{}).Run(t.Context(), agents.PurposeReview, agents.Invocation{}); ceilingOf(t, err).Ceiling != 50 {
		t.Fatalf("the same call inside a longer window answered %v", err)
	}
	if inner.count() != 1 {
		t.Fatalf("the start outside the short window was reached %d times", inner.count())
	}
}

// The default, and an explicit zero, do not refuse, however large the history.
func TestAZeroUsageCeilingDoesNotRefuse(t *testing.T) {
	t.Parallel()
	for _, doc := range []string{"", "usage_ceiling:\n  input_tokens: 0\n"} {
		s, st := ceilingService(t, doc)
		seedSpend(t, st, time.Now().UTC(), store.Known[int64](1_000_000))
		inner := &countingRunner{}
		if _, err := s.containRunner(inner).Run(t.Context(), agents.PurposeReview, agents.Invocation{}); err != nil {
			t.Fatalf("document %q: %v", doc, err)
		}
		if inner.count() != 1 {
			t.Fatalf("document %q reached the runner %d times", doc, inner.count())
		}
		if s.cfg.UsageCeiling.InputTokens != 0 {
			t.Fatalf("document %q resolved a ceiling of %d", doc, s.cfg.UsageCeiling.InputTokens)
		}
	}
}

// A call that did not report input tokens leaves the total unestablished, and
// a positive ceiling refuses rather than treating the missing count as zero.
func TestUnreportedInputTokensRefuseAStart(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P6)
	s, st := ceilingService(t, ceilingDoc)
	seedSpend(t, st, time.Now().UTC(), store.Known[int64](10))
	seedSpend(t, st, time.Now().UTC(), store.Optional[int64]{})
	inner := &countingRunner{}
	_, err := s.containRunner(inner).Run(t.Context(), agents.PurposeReview, agents.Invocation{})
	got := ceilingOf(t, err)
	if got.Unreported != 1 || got.Calls != 2 {
		t.Fatalf("refusal %+v, want one of two calls unreported", got)
	}
	if inner.count() != 0 {
		t.Fatal("the start ran")
	}
}

// A positive ceiling with no history to read refuses. A cancelled context is
// that cancellation, not a ceiling refusal. A window the usage view cannot
// read refuses before a start, which is the check at admit time.
func TestAStartIsRefusedWhenTheCeilingCannotBeCompared(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P6)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(ceilingDoc), 0o600); err != nil {
		t.Fatal(err)
	}
	_, cfg, _, err := resolveConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	closed := &countingRunner{}
	s := &Service{cfg: cfg, registry: newRegistry()}
	_, err = s.containRunner(closed).Run(t.Context(), agents.PurposeReview, agents.Invocation{})
	if !errors.Is(ceilingOf(t, err).Read, errNoInvocationHistory) {
		t.Fatalf("nil store: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	s, _ = ceilingService(t, ceilingDoc)
	cancelled := &countingRunner{}
	_, err = s.containRunner(cancelled).Run(ctx, agents.PurposeReview, agents.Invocation{})
	var ceiling *machine.UsageCeilingError
	if !errors.Is(err, context.Canceled) || errors.As(err, &ceiling) {
		t.Fatalf("cancelled context: %v", err)
	}

	s, _ = ceilingService(t, ceilingDoc)
	s.cfg.UsageCeiling.Window = "soon"
	unreadable := &countingRunner{}
	_, err = s.containRunner(unreadable).Run(t.Context(), agents.PurposeReview, agents.Invocation{})
	if !errors.Is(ceilingOf(t, err).Read, machine.ErrUsageWindow) {
		t.Fatalf("unreadable window: %v", err)
	}
	if closed.count() != 0 || cancelled.count() != 0 || unreadable.count() != 0 {
		t.Fatal("a start the ceiling could not compare still ran")
	}
}

// A start that passed the check keeps running when a later start is refused.
// The first call's context is not cancelled, and the refused call does not
// reach the runner.
func TestAnInFlightInvocationIsNotCancelledWhenALaterStartIsRefused(t *testing.T) {
	t.Parallel()
	s, st := ceilingService(t, ceilingDoc)
	seedSpend(t, st, time.Now().UTC(), store.Known[int64](40))

	release := make(chan struct{})
	var stop sync.Once
	defer stop.Do(func() { close(release) })
	entered := make(chan struct{})
	inner := &blockingRunner{entered: entered, release: release}
	wrapped := s.containRunner(inner)

	first, firstCancel := context.WithCancel(context.Background())
	defer firstCancel()
	done := make(chan error, 1)
	go func() {
		_, err := wrapped.Run(first, agents.PurposeReview, agents.Invocation{})
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the invocation did not start")
	}

	seedSpend(t, st, time.Now().UTC(), store.Known[int64](70))
	_, err := wrapped.Run(context.Background(), agents.PurposeReview, agents.Invocation{})
	if ceilingOf(t, err).Ceiling != 100 {
		t.Fatalf("the later start answered %v", err)
	}
	if first.Err() != nil {
		t.Fatal("the in-flight invocation's context was cancelled")
	}
	if inner.count() != 1 {
		t.Fatalf("the runner was reached %d times, want only the in-flight call", inner.count())
	}
	stop.Do(func() { close(release) })
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the in-flight invocation ended with %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the in-flight invocation did not finish")
	}
	if inner.count() != 1 {
		t.Fatalf("the runner was reached %d times after the in-flight call returned", inner.count())
	}
}

// blockingRunner blocks its first call until release is closed.
type blockingRunner struct {
	countingRunner
	entered chan struct{}
	release chan struct{}
}

func (r *blockingRunner) Run(ctx context.Context, purpose agents.Purpose, inv agents.Invocation) (agents.Result, error) {
	r.mu.Lock()
	r.calls++
	n := r.calls
	r.mu.Unlock()
	if n == 1 {
		close(r.entered)
		select {
		case <-r.release:
		case <-ctx.Done():
			return agents.Result{}, ctx.Err()
		}
		if err := ctx.Err(); err != nil {
			return agents.Result{}, err
		}
	}
	return agents.Result{}, nil
}

// A refusal is the error the start returns, and it is written to the stage
// log of the run and stage the context names. It is not a row of the history.
func TestAUsageCeilingRefusalIsWrittenToTheStageLog(t *testing.T) {
	t.Parallel()
	s, st := ceilingService(t, ceilingDoc)
	seedSpend(t, st, time.Now().UTC(), store.Known[int64](150))
	h, err := home.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s.home = h
	ctx := withServing(withAdvancing(t.Context(), "run-1"), "review")
	_, err = s.containRunner(&countingRunner{}).Run(ctx, agents.PurposeReview, agents.Invocation{})
	if ceilingOf(t, err) == nil {
		t.Fatal(err)
	}
	text, readErr := os.ReadFile(h.StageLog("run-1", "review"))
	if readErr != nil {
		t.Fatalf("reading the stage log: %v", readErr)
	}
	logged := string(text)
	for _, part := range []string{"usage ceiling", "150", "over the ceiling", "100"} {
		if !strings.Contains(logged, part) {
			t.Fatalf("stage log %q does not contain %q", logged, part)
		}
	}
	rows, err := st.AgentInvocations(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("the history has %d rows, want the seeded one and not the refused start", len(rows))
	}
}
