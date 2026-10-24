package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/procgroup"
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
	applyFix(t, s, built, inv)

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

func TestReviewFallsBackWithoutInheritingThePrimaryModel(t *testing.T) {
	primary := standin.New(t, standin.Script{Steps: []standin.Step{{Times: standin.Always, Reply: standin.Failed("quota exceeded")}}})
	backup := standin.NewCursor(t, standin.Script{Steps: []standin.Step{{Times: standin.Always, Reply: standin.Text("backup review")}}})
	s := roleService(t, primary.Runner())
	s.catalog = agents.NewCatalog(fixedRunner{runner: primary.Runner()}, fixedRunner{runner: backup.Runner()})
	s.cfg.Roles = config.Roles{config.RoleReview: {Agent: "claude", Provider: "anthropic", Type: "opus", Version: "5.5", Effort: "high", ContextWindow: config.Context1M, Fallback: &config.Role{Agent: "cursor"}}}
	built, err := s.driverFor(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	inv := agents.Invocation{Prompt: "review", Shape: agents.ShapeText, Dir: t.TempDir()}
	result, err := built.stages.Run(t.Context(), agents.PurposeReview, inv)
	if err != nil || result.Text != "backup review" {
		t.Fatalf("review: %q %v", result.Text, err)
	}
	if tried := primary.Calls(); len(tried) != 1 || tried[0].Model() != "claude-opus-5-5[1m]" {
		t.Fatalf("the primary was tried as %v, want once at claude-opus-5-5[1m]", tried)
	}
	calls := backup.Calls()
	if len(calls) != 1 || calls[0].Model() != "" || flagValue(calls[0].Args, "--effort") != "" || calls[0].Session() != "" {
		t.Fatalf("backup inherited primary settings or session: %v", calls)
	}
}

// A review role and a fix role that name a context window start the model at
// that window: the review on Opus 5.5 at 1m reaches the command line as
// claude-opus-5-5[1m], and the fixer on Sonnet 5 at standard as the plain
// claude-sonnet-5. Each launch records the window it was asked for, and the
// lint launch, whose purpose no role names, records it as not named.
func TestReviewAndFixRolesStartTheContextWindowTheyName(t *testing.T) {
	ctx := t.Context()
	rec := &roleRecorder{}
	agent := standin.New(t, standin.Script{Steps: []standin.Step{{
		Times: standin.Always,
		Reply: standin.Text("done"),
	}}}, agents.WithRecorder(rec))
	s := roleService(t, agent.Runner())
	s.cfg.Roles = config.Roles{
		config.RoleReview: {Agent: agents.ClaudeName, Provider: "anthropic", Type: "opus", Version: "5.5", ContextWindow: config.Context1M},
		config.RoleFix:    {Agent: agents.ClaudeName, Provider: "anthropic", Type: "sonnet", Version: "5", ContextWindow: config.ContextStandard},
	}
	built, err := s.driverFor(ctx)
	if err != nil {
		t.Fatalf("driverFor: %v", err)
	}
	inv := agents.Invocation{Prompt: "look", Shape: agents.ShapeText, Dir: t.TempDir()}
	for _, purpose := range []agents.Purpose{agents.PurposeReview, agents.PurposeLint} {
		if _, err := built.stages.Run(ctx, purpose, inv); err != nil {
			t.Fatalf("%s: %v", purpose, err)
		}
	}
	applyFix(t, s, built, inv)

	calls := agent.Calls()
	if len(calls) != 3 || calls[0].Model() != "claude-opus-5-5[1m]" || calls[1].Model() != "" || calls[2].Model() != "claude-sonnet-5" {
		t.Fatalf("the stand-in saw %v, want review at claude-opus-5-5[1m], lint at no model, and fix at claude-sonnet-5", calls)
	}
	got := rec.snapshot()
	if len(got) != 3 ||
		got[0].ContextWindow != config.Context1M || got[0].Model != "claude-opus-5-5[1m]" ||
		got[1].ContextWindow != "" ||
		got[2].ContextWindow != config.ContextStandard || got[2].Model != "claude-sonnet-5" {
		t.Fatalf("recorded launches = %+v, want review at 1m, lint not named, and fix at standard", got)
	}
}

// applyFix starts the fix role the way a run's fix stage does: through the
// run's Fixer, over a run recorded against a repository at inv.Dir.
func applyFix(t *testing.T, s *Service, built *driver, inv agents.Invocation) {
	t.Helper()
	ctx := t.Context()
	if _, err := s.store.UpsertRepository(ctx, store.Repository{
		ID: "repo-1", WorkingPath: inv.Dir, UpstreamURL: "https://example.test/one.git", DefaultBranch: "main",
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
}

// A role whose context window its adapter has not declared on that model is
// refused before anything starts, naming the role, and nothing is started at
// the standard window in its place. Haiku 4.5 is a cell Claude declares no
// 1m window on, and Codex declares no window at all.
func TestAnUndeclaredContextWindowIsRefusedBeforeLaunch(t *testing.T) {
	ctx := t.Context()
	for _, tc := range []struct {
		name string
		role config.Role
	}{
		{"claude haiku at 1m", config.Role{Agent: agents.ClaudeName, Provider: "anthropic", Type: "haiku", Version: "4.5", ContextWindow: config.Context1M}},
		{"codex at standard", config.Role{Agent: agents.CodexName, Provider: "openai", Type: "gpt", Version: "6-astra", ContextWindow: config.ContextStandard}},
		{"a window with no model", config.Role{Agent: agents.ClaudeName, ContextWindow: config.Context1M}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			agent := standin.New(t, standin.Script{Steps: []standin.Step{{Times: standin.Always, Reply: standin.Text("done")}}})
			s := roleService(t, agent.Runner())
			s.cfg.Roles = config.Roles{config.RoleReview: tc.role}
			_, err := s.driverFor(ctx)
			if !errors.Is(err, agents.ErrUndeclaredModel) || !strings.Contains(err.Error(), "review") {
				t.Fatalf("driverFor = %v, want ErrUndeclaredModel naming the review role", err)
			}
			if calls := agent.Calls(); len(calls) != 0 {
				t.Fatalf("an agent was started in the refused launch's place: %v", calls)
			}
		})
	}
}

func TestFallbackFixerRecordsAndRestoresTheActualSessionOwner(t *testing.T) {
	primary := standin.New(t, standin.Script{Steps: []standin.Step{{Times: standin.Always, Reply: standin.Failed("quota exceeded")}}})
	backup := standin.NewCursor(t, standin.Script{Steps: []standin.Step{{Times: standin.Always, Reply: standin.Text("fixed")}}})
	s := roleService(t, primary.Runner())
	s.catalog = agents.NewCatalog(fixedRunner{runner: primary.Runner()}, fixedRunner{runner: backup.Runner()})
	s.cfg.SessionReuse = true
	s.cfg.Roles = config.Roles{config.RoleFix: {Agent: "claude", Fallback: &config.Role{Agent: "cursor"}}}
	built, err := s.driverFor(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if _, err = s.store.UpsertRepository(t.Context(), store.Repository{ID: "fallback-repo", WorkingPath: dir, UpstreamURL: "https://example.test/fallback.git", DefaultBranch: "main"}); err != nil {
		t.Fatal(err)
	}
	build, err := store.CurrentBuild()
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.store.CreateRun(t.Context(), store.Run{ID: "fallback-run", RepositoryID: "fallback-repo", Branch: "topic", SubmittedHead: "abc", Intent: "work", IntentSource: "push", Build: build, ConfigDigest: "cfg"})
	if err != nil {
		t.Fatal(err)
	}
	fixer, err := built.runs.Fixer(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	inv := agents.Invocation{Prompt: "fix", Shape: agents.ShapeText, Dir: dir}
	if _, err = fixer.Apply(t.Context(), inv); err != nil {
		t.Fatal(err)
	}
	saved, err := s.store.Run(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.FixerAgent.Or("") != "cursor" || !saved.FixerSession.IsKnown() {
		t.Fatalf("stored owner: %+v", saved)
	}
	// Rebuild the service driver, which reconstructs its runs service from
	// durable state rather than using the existing in-memory Fixer.
	s.built = nil
	rebuilt, err := s.driverFor(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	again, err := rebuilt.runs.Fixer(t.Context(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = again.Apply(t.Context(), inv); err != nil {
		t.Fatal(err)
	}
	calls := backup.Calls()
	if len(primary.Calls()) != 1 || len(calls) != 2 || calls[1].Session() == "" {
		t.Fatalf("session was not restored on backup: primary=%v backup=%v", primary.Calls(), calls)
	}
}

type argumentFactory struct {
	inner agents.Factory
	args  []string
}

func (f argumentFactory) Name() string                  { return f.inner.Name() }
func (f argumentFactory) AutoEligible() bool            { return false }
func (f argumentFactory) Declares() agents.Capabilities { return f.inner.Declares() }
func (f argumentFactory) New(ctx context.Context, args []string) (agents.Runner, error) {
	return f.inner.New(ctx, append(args, f.args...))
}

func TestAReviewPrimaryIsRetriedAfterItsExecutableReturns(t *testing.T) {
	primary := standin.New(t, standin.Script{Steps: []standin.Step{{Times: standin.Always, Reply: standin.Text("primary")}}})
	backup := standin.NewCursor(t, standin.Script{Steps: []standin.Step{{Times: standin.Always, Reply: standin.Text("backup")}}})
	path := filepath.Join(t.TempDir(), "claude-later")
	s := roleService(t, backup.Runner())
	s.catalog = agents.NewCatalog(argumentFactory{inner: agents.ClaudeFactory(agents.WithBinary(path)), args: primary.Arguments()}, fixedRunner{runner: backup.Runner()})
	s.cfg.Roles = config.Roles{config.RoleReview: {Agent: "claude", Fallback: &config.Role{Agent: "cursor"}}}
	built, err := s.driverFor(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	inv := agents.Invocation{Prompt: "review", Shape: agents.ShapeText, Dir: t.TempDir()}
	result, err := built.stages.Run(t.Context(), agents.PurposeReview, inv)
	if err != nil || result.Text != "backup" {
		t.Fatalf("missing primary: %q %v", result.Text, err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bytes, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	if err = procgroup.WriteProgram(path, bytes, 0700); err != nil {
		t.Fatal(err)
	}
	result, err = built.stages.Run(t.Context(), agents.PurposeReview, inv)
	if err != nil || result.Text != "primary" {
		t.Fatalf("recovered primary: %q %v", result.Text, err)
	}
	if len(primary.Calls()) != 1 || len(backup.Calls()) != 1 {
		t.Fatal("recovered primary was not selected")
	}
}

// A review role that names a context window and no agent is refused, naming
// the role, rather than run on the agent list at whatever window that agent
// starts: a window belongs to one process, as an effort does.
func TestAContextWindowOnARoleWithNoAgentIsRefused(t *testing.T) {
	agent := standin.New(t, standin.Script{Steps: []standin.Step{{Times: standin.Always, Reply: standin.Text("done")}}})
	s := roleService(t, agent.Runner())
	s.cfg.Roles = config.Roles{config.RoleReview: {ContextWindow: config.Context1M}}
	if _, err := s.driverFor(t.Context()); err == nil || !strings.Contains(err.Error(), "review") || !strings.Contains(err.Error(), "context window") {
		t.Fatalf("driverFor = %v, want a refusal naming the review role and its context window", err)
	}
	if calls := agent.Calls(); len(calls) != 0 {
		t.Fatalf("an agent was started for the refused role: %v", calls)
	}
}
