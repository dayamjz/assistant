package runs

import (
	"errors"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/vcs"
)

// A finished run has no next fix round, so the index that makes one run's
// fixer role one object stops holding it. Nothing outside this package can see
// that: Fixer already refuses a finished run whether or not the entry is
// there, so what this asserts is the bookkeeping and not a behaviour, and it
// is asserted here because that is where the index is.
func TestEndingARunDropsItsFixerRoleFromTheIndex(t *testing.T) {
	ctx := t.Context()
	svc, db := internalService(t)
	run := internalRun(t, db, svc, "run-1")

	if _, err := svc.Fixer(ctx, run.ID); err != nil {
		t.Fatalf("Fixer: %v", err)
	}
	svc.mu.Lock()
	held := len(svc.fixers)
	svc.mu.Unlock()
	if held != 1 {
		t.Fatalf("the index holds %d fixer roles after one was handed out, want 1", held)
	}

	if _, err := svc.Terminate(ctx, run.ID); err != nil {
		t.Fatalf("Terminate: %v", err)
	}
	svc.mu.Lock()
	held = len(svc.fixers)
	svc.mu.Unlock()
	if held != 0 {
		t.Fatalf("the index still holds %d fixer roles after the run ended", held)
	}
}

// A run ended by something that did not go through this service is refused the
// next time its role is asked for, and that refusal is also where the index
// stops holding it. The refusal alone would pass without the drop, because the
// status is read on every hand-out; the index is what this asserts.
func TestARunEndedOutOfBandDropsItsFixerRoleOnTheNextHandOut(t *testing.T) {
	ctx := t.Context()
	svc, db := internalService(t)
	run := internalRun(t, db, svc, "run-1")

	if _, err := svc.Fixer(ctx, run.ID); err != nil {
		t.Fatalf("Fixer: %v", err)
	}
	// Ending the run without the service, which is the bypass its documented
	// ownership leaves open, so nothing here evicts on the move.
	if _, err := db.TransitionRun(ctx, run.ID, []store.RunStatus{store.RunPending}, store.RunTerminated); err != nil {
		t.Fatalf("TransitionRun: %v", err)
	}
	svc.mu.Lock()
	held := len(svc.fixers)
	svc.mu.Unlock()
	if held != 1 {
		t.Fatalf("the index holds %d fixer roles after an out-of-band ending, want the 1 handed out", held)
	}

	if _, err := svc.Fixer(ctx, run.ID); !errors.Is(err, ErrRunEnded) {
		t.Fatalf("Fixer answered %v, want ErrRunEnded", err)
	}
	svc.mu.Lock()
	held = len(svc.fixers)
	svc.mu.Unlock()
	if held != 0 {
		t.Fatalf("the index still holds %d fixer roles after refusing the ended run", held)
	}
}

// A move that does not end the run leaves the index alone, so the assertion
// above is about the run ending rather than about any transition at all.
func TestAMoveThatDoesNotEndARunKeepsItsFixerRole(t *testing.T) {
	ctx := t.Context()
	svc, db := internalService(t)
	run := internalRun(t, db, svc, "run-1")

	handed, err := svc.Fixer(ctx, run.ID)
	if err != nil {
		t.Fatalf("Fixer: %v", err)
	}
	if _, err := svc.Start(ctx, run.ID); err != nil {
		t.Fatalf("Start: %v", err)
	}
	svc.mu.Lock()
	still := svc.fixers[run.ID]
	svc.mu.Unlock()
	if still != handed {
		t.Fatal("a run that is only starting lost the fixer role it was handed")
	}
}

// internalService builds a service over a real store and the stand-in agent.
// No test here reaches the agent, but the service is built the way a caller
// builds one rather than around a runner written for the occasion.
func internalService(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	userinfo := regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.\-]*://)([^/@\s]+)@`)
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "state.db"),
		store.WithRedactor(vcs.RedactorFunc(func(s string) string {
			return userinfo.ReplaceAllString(s, "${1}REDACTED@")
		})))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	runner := standin.New(t, standin.Script{}).Runner()
	svc, err := New(Options{Store: db, Agent: agents.Resolution{
		Runner: runner, Name: runner.Name(), Capabilities: runner.Capabilities(),
	}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return svc, db
}

func internalRun(t *testing.T, db *store.Store, svc *Service, id string) store.Run {
	t.Helper()
	ctx := t.Context()
	if _, err := db.UpsertRepository(ctx, store.Repository{
		ID: "repo-1", WorkingPath: "/checkouts/one",
		UpstreamURL: "https://example.test/one.git", DefaultBranch: "main",
	}); err != nil {
		t.Fatalf("UpsertRepository: %v", err)
	}
	r, err := svc.Create(ctx, store.Run{
		ID: id, RepositoryID: "repo-1", Branch: "topic",
		SubmittedHead: "aaaa", Base: "bbbb", Intent: "validate", IntentSource: "push",
		Build:        store.Build{Version: "v0.1.0", Revision: "0123456789abcdef", Go: "go1.25.0"},
		ConfigDigest: "cfg-1",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return r
}
