package service

import (
	"strings"
	"sync"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
)

// roleRecorder is the adapter's own account of an invocation, attached with
// agents.WithRecorder. The stand-in's Calls are the wire; this is what the
// adapter recorded from that invocation.
type roleRecorder struct {
	mu      sync.Mutex
	records []agents.Record
}

func (r *roleRecorder) RecordInvocation(rec agents.Record) {
	r.mu.Lock()
	r.records = append(r.records, rec)
	r.mu.Unlock()
}

func (r *roleRecorder) snapshot() []agents.Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]agents.Record, len(r.records))
	copy(out, r.records)
	return out
}

func roleService(t *testing.T, runner agents.Runner) *Service {
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
	cfg.Agent = []string{runner.Name()}
	return &Service{
		home:     h,
		store:    records,
		log:      log,
		cfg:      cfg,
		catalog:  agents.NewCatalog(fixedRunner{runner: runner}),
		registry: newRegistry(),
	}
}

func flagValue(args []string, name string) string {
	for i, arg := range args {
		if arg == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// A review role and a fix role name the agent, the model, and the effort
// those launches start. The stand-in sees the model and the effort on the
// command line, and another stage keeps the service agent with neither.
func TestReviewAndFixRolesStartTheAgentTheyName(t *testing.T) {
	ctx := t.Context()
	rec := &roleRecorder{}
	agent := standin.New(t, standin.Script{Steps: []standin.Step{{
		Times: standin.Always,
		Reply: standin.Text("done"),
	}}}, agents.WithRecorder(rec))
	s := roleService(t, agent.Runner())
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

	calls := agent.Calls()
	if len(calls) != 3 {
		t.Fatalf("the stand-in saw %d launches, want review, lint, and fix: %v", len(calls), calls)
	}
	if calls[0].Model() != "claude-opus-5-5" || flagValue(calls[0].Args, "--effort") != "high" {
		t.Fatalf("review command line = %q, want --model claude-opus-5-5 and --effort high", calls[0].Args)
	}
	if calls[1].Model() != "" || flagValue(calls[1].Args, "--effort") != "" {
		t.Fatalf("lint command line = %q, want no model and no effort", calls[1].Args)
	}
	if calls[2].Model() != "claude-sonnet-5" || flagValue(calls[2].Args, "--effort") != "low" {
		t.Fatalf("fix command line = %q, want --model claude-sonnet-5 and --effort low", calls[2].Args)
	}
	got := rec.snapshot()
	if len(got) != 3 ||
		got[0].Role != "review" || got[0].Provider != "anthropic" || got[0].Type != "opus" || got[0].Version != "5.5" || got[0].Effort != "high" ||
		got[1].Role != "" || got[1].Provider != "" || got[1].Effort != "" ||
		got[2].Role != "fix" || got[2].Provider != "anthropic" || got[2].Type != "sonnet" || got[2].Version != "5" || got[2].Effort != "low" {
		t.Fatalf("recorded launches = %+v", got)
	}
}

// A review role that names an agent this build cannot start refuses the
// driver, and the refusal is not cached, so a role that can start is what
// the next run resolves.
func TestAReviewRoleThatCannotStartRefusesTheDriver(t *testing.T) {
	ctx := t.Context()
	agent := standin.New(t, standin.Script{})
	s := roleService(t, agent.Runner())
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

// A worker role that names auto is a pin of that word, not the fallback
// list. An adapter that is runnable is not started in its place.
func TestAWorkerRoleNamedAutoDoesNotFallThrough(t *testing.T) {
	ctx := t.Context()
	agent := standin.New(t, standin.Script{})
	s := roleService(t, agent.Runner())
	s.cfg.Roles = config.Roles{config.RoleWorker: {Agent: "auto"}}
	if _, _, err := s.routeWorker(ctx, ""); err == nil {
		t.Fatal("a worker role named auto resolved an agent")
	}
	if len(agent.Calls()) != 0 {
		t.Fatalf("the stand-in was started: %v", agent.Calls())
	}
}
