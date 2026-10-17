package service

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
)

// recordingRunner keeps the invocation it was handed, so a test can see the
// model and effort a role launch stamped. It declares resumable sessions and
// carries the mechanism, which is what Resolve and Route hold an adapter to.
type recordingRunner struct {
	name string

	mu    sync.Mutex
	calls []recordedCall
}

type recordedCall struct {
	purpose agents.Purpose
	model   string
	effort  string
}

func (r *recordingRunner) Name() string { return r.name }

func (r *recordingRunner) Capabilities() agents.Capabilities {
	return agents.Declare(agents.CapabilityResumableSessions)
}

func (r *recordingRunner) Run(_ context.Context, purpose agents.Purpose, inv agents.Invocation) (agents.Result, error) {
	r.mu.Lock()
	r.calls = append(r.calls, recordedCall{purpose: purpose, model: inv.Model, effort: inv.Effort})
	r.mu.Unlock()
	return agents.Result{}, nil
}

func (r *recordingRunner) Fixer(context.Context, string) (agents.Fixer, error) {
	return &recordingFixer{runner: r}, nil
}

func (r *recordingRunner) snapshot() []recordedCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]recordedCall, len(r.calls))
	copy(out, r.calls)
	return out
}

type recordingFixer struct {
	runner *recordingRunner
}

func (f *recordingFixer) Apply(ctx context.Context, inv agents.Invocation) (agents.Result, error) {
	return f.runner.Run(ctx, agents.PurposeFix, inv)
}

func (f *recordingFixer) Reference() string { return "" }

func roleService(t *testing.T, scripted, claude *recordingRunner) *Service {
	t.Helper()
	ctx := t.Context()
	h, err := home.Open(t.TempDir())
	if err != nil {
		t.Fatalf("opening the home: %v", err)
	}
	if err := h.Create(); err != nil {
		t.Fatalf("creating the home: %v", err)
	}
	seedDatabase(t, h.Database())
	records, err := store.Open(ctx, h.Database(), store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	t.Cleanup(func() { _ = records.Close() })
	log, err := h.OpenLog(0)
	if err != nil {
		t.Fatalf("opening the log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	cfg := config.Defaults()
	cfg.Agent = []string{scripted.Name()}
	return &Service{
		home:     h,
		store:    records,
		log:      log,
		cfg:      cfg,
		catalog:  agents.NewCatalog(fixedRunner{runner: scripted}, fixedRunner{runner: claude}),
		registry: newRegistry(),
	}
}

// A review role and a fix role name the agent, the model, and the effort
// those launches start. Another stage keeps the service agent, with neither.
func TestReviewAndFixRolesStartTheAgentTheyName(t *testing.T) {
	ctx := t.Context()
	scripted := &recordingRunner{name: "scripted"}
	claude := &recordingRunner{name: agents.ClaudeName}
	s := roleService(t, scripted, claude)
	s.cfg.Roles = config.Roles{
		config.RoleReview: {
			Agent: agents.ClaudeName, Provider: "anthropic", Type: "opus", Version: "5.5", Effort: "high",
		},
		config.RoleFix: {
			Agent: agents.ClaudeName, Provider: "anthropic", Type: "sonnet", Version: "5", Effort: "low",
		},
	}
	built, err := s.driverFor(ctx)
	if err != nil {
		t.Fatalf("driverFor: %v", err)
	}
	dir := t.TempDir()
	inv := agents.Invocation{Prompt: "look", Shape: agents.ShapeText, Dir: dir}
	if _, err := built.stages.Run(ctx, agents.PurposeReview, inv); err != nil {
		t.Fatalf("review: %v", err)
	}
	if _, err := built.stages.Run(ctx, agents.PurposeLint, inv); err != nil {
		t.Fatalf("lint: %v", err)
	}
	if _, err := s.store.UpsertRepository(ctx, store.Repository{
		ID: "repo-1", WorkingPath: dir, UpstreamURL: "https://example.test/one.git", DefaultBranch: "main",
	}); err != nil {
		t.Fatalf("UpsertRepository: %v", err)
	}
	build, err := store.CurrentBuild()
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.store.CreateRun(ctx, store.Run{
		ID: "run-1", RepositoryID: "repo-1", Branch: "topic", SubmittedHead: "abc",
		Intent: "work", IntentSource: "push", Build: build, ConfigDigest: "cfg",
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	fixer, err := built.runs.Fixer(ctx, run.ID)
	if err != nil {
		t.Fatalf("Fixer: %v", err)
	}
	if _, err := fixer.Apply(ctx, inv); err != nil {
		t.Fatalf("fix: %v", err)
	}

	gotLint := scripted.snapshot()
	if len(gotLint) != 1 || gotLint[0].purpose != agents.PurposeLint || gotLint[0].model != "" || gotLint[0].effort != "" {
		t.Fatalf("lint launch = %+v, want the service agent with no model and no effort", gotLint)
	}
	got := claude.snapshot()
	if len(got) != 2 ||
		got[0].purpose != agents.PurposeReview || got[0].model != "claude-opus-5-5" || got[0].effort != "high" ||
		got[1].purpose != agents.PurposeFix || got[1].model != "claude-sonnet-5" || got[1].effort != "low" {
		t.Fatalf("role launches = %+v, want a review of claude-opus-5-5 at high and a fix of claude-sonnet-5 at low", got)
	}
}

// A review role that names an agent this build cannot start refuses the
// driver, and the refusal is not cached, so a role that can start is what
// the next run resolves.
func TestAReviewRoleThatCannotStartRefusesTheDriver(t *testing.T) {
	ctx := t.Context()
	scripted := &recordingRunner{name: "scripted"}
	claude := &recordingRunner{name: agents.ClaudeName}
	s := roleService(t, scripted, claude)
	s.cfg.Roles = config.Roles{config.RoleReview: {Agent: "missing"}}
	if _, err := s.driverFor(ctx); err == nil || !strings.Contains(err.Error(), "review") || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("driverFor = %v, want a refusal naming the review role and missing", err)
	}
	if _, err := s.driverFor(ctx); err == nil {
		t.Fatal("the refusal was cached")
	}
	s.cfg.Roles = config.Roles{
		config.RoleReview: {Agent: agents.ClaudeName, Provider: "openai", Type: "gpt", Version: "6-astra"},
	}
	if _, err := s.driverFor(ctx); err == nil || !strings.Contains(err.Error(), "review") {
		t.Fatalf("driverFor = %v, want the review role's undeclared translation refused", err)
	}
	s.cfg.Roles = nil
	if _, err := s.driverFor(ctx); err != nil {
		t.Fatalf("driverFor after the role was cleared: %v", err)
	}
}
