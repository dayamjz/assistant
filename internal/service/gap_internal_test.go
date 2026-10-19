package service

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// namedRunner is a catalog entry over a runner a test already built. auto is
// the factory's own AutoEligible answer: Codex stays false, so a catalog that
// contains it does not make "auto" select it.
type namedRunner struct {
	runner agents.Runner
	auto   bool
}

func (n namedRunner) Name() string { return n.runner.Name() }

func (n namedRunner) AutoEligible() bool { return n.auto }

func (n namedRunner) New(context.Context, []string) (agents.Runner, error) { return n.runner, nil }

// subjectDir is the directory a Codex invocation runs in. When
// ASSISTANT_E2E_SUBJECT names a checkout, that tree is copied here and the
// invocation uses the copy. The variable is how a local run points at a
// planning workbook; the default is a directory this test creates, so CI
// does not depend on a path outside the module.
func subjectDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := os.Getenv("ASSISTANT_E2E_SUBJECT")
	if src == "" {
		if err := os.WriteFile(filepath.Join(dir, "marker.txt"), []byte("subject\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	cmd := exec.Command("cp", "-R", src+string(os.PathSeparator)+".", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("copying %s: %v\n%s", src, err, out)
	}
	return dir
}

// Codex and Claude execute the tasks each adapter declares, through the
// service's own resolution and launch. The operator list puts Codex first,
// so runs use Codex. Codex declares no worker, so the launch skips it and
// takes Claude, and records that selection. A review on the resolved runner
// carries no session; the fixer round does.
func TestCodexRunsAndClaudeWorkersThroughTheSameService(t *testing.T) {
	principles.Cite(t, principles.P4)
	ctx := t.Context()
	codex := standin.NewCodex(t, standin.Script{Steps: []standin.Step{{
		Times: standin.Always, Reply: standin.Text("the change looks fine"),
	}}})
	claude := standin.New(t, standin.Script{})
	subject := newDispatchSubject(t)
	subject.service.catalog = agents.NewCatalog(
		namedRunner{runner: codex.Runner(), auto: false},
		namedRunner{runner: claude.Runner(), auto: true},
	)
	subject.service.cfg = config.Defaults()
	subject.service.cfg.Agent = []string{agents.CodexName, agents.ClaudeName}
	subject.service.registry = newRegistry()
	t.Cleanup(func() {
		for _, err := range subject.service.closeSessions() {
			t.Errorf("closing the sessions: %v", err)
		}
	})

	driver, err := subject.service.driverFor(ctx)
	if err != nil {
		t.Fatalf("driverFor: %v", err)
	}
	if driver.agent.Name != agents.CodexName {
		t.Fatalf("resolved %s, want codex", driver.agent.Name)
	}
	dir := subjectDir(t)
	reviewed, err := driver.agent.Runner.Run(ctx, agents.PurposeReview, agents.Invocation{
		Prompt: "review the subject", Shape: agents.ShapeText, Dir: dir,
	})
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if reviewed.Text != "the change looks fine" || reviewed.Record.Session != agents.SessionNone ||
		reviewed.Record.Agent != agents.CodexName {
		t.Fatalf("review result = %+v text %q, want a sessionless codex review", reviewed.Record, reviewed.Text)
	}
	fixer, err := agents.OpenFixer(ctx, driver.agent.Runner, "")
	if err != nil {
		t.Fatalf("OpenFixer: %v", err)
	}
	fixed, err := fixer.Apply(ctx, agents.Invocation{
		Prompt: "fix the subject", Shape: agents.ShapeText, Dir: dir,
	})
	if err != nil {
		t.Fatalf("fix: %v", err)
	}
	if fixed.Record.Session != agents.SessionOpened {
		t.Fatalf("fix recorded session %s, want opened", fixed.Record.Session)
	}

	d, err := subject.service.dispatchTask(ctx, subject.task)
	if err != nil {
		t.Fatalf("dispatching: %v", err)
	}
	if _, err := subject.service.launchTask(ctx, d); err != nil {
		t.Fatalf("launching: %v", err)
	}
	attempts, err := subject.records.TaskAttempts(ctx, subject.task)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("attempts = %+v, %v", attempts, err)
	}
	adapter, ok := attempts[0].Adapter.Get()
	if !ok || adapter != agents.ClaudeName {
		t.Fatalf("adapter = %q known=%v, want claude", adapter, ok)
	}
	reason, ok := attempts[0].RouteReason.Get()
	if !ok || reason != agents.ReasonOrdered {
		t.Fatalf("reason = %q known=%v, want %s", reason, ok, agents.ReasonOrdered)
	}

	var review, fix *standin.Call
	codexCalls := codex.Calls()
	for i := range codexCalls {
		call := &codexCalls[i]
		if call.Worker {
			t.Fatalf("codex started a worker: %s", call)
		}
		switch call.Prompt {
		case "review the subject":
			review = call
		case "fix the subject":
			fix = call
		}
	}
	if review == nil || review.Session() != "" || !hasArg(review.Args, "--sandbox") || hasArg(review.Args, "--approve-for-me") {
		t.Fatalf("review call = %v, want a read-only one-shot", review)
	}
	if fix == nil || !hasArg(fix.Args, "--approve-for-me") || hasArg(fix.Args, "--sandbox") {
		t.Fatalf("fix call = %v, want the edit grant", fix)
	}
	var worker *standin.Call
	deadline := time.Now().Add(30 * time.Second)
	for worker == nil {
		claudeCalls := claude.Calls()
		for i := range claudeCalls {
			if claudeCalls[i].Worker {
				worker = &claudeCalls[i]
			}
		}
		if worker != nil || !time.Now().Before(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if worker == nil || !hasArg(worker.Args, "--allowedTools") || !hasArg(worker.Args, "Bash(git:*)") {
		t.Fatalf("worker call = %v, calls %s, want GrantWorker's tool list", worker, claude.Calls())
	}
	if hasArg(worker.Args, "--approve-for-me") || hasArg(worker.Args, "--dangerously-bypass-approvals-and-sandbox") {
		t.Fatalf("the worker grant was widened: %q", worker.Args)
	}
}

// An operator pin of Codex refuses the worker launch. The refusal is typed,
// the attempt records no route, and no process is started, so the grant is
// the one the adapter would have written for a task it declares.
func TestACodexOnlyPinRefusesWorkerLaunchWithoutStartingOne(t *testing.T) {
	ctx := t.Context()
	codex := standin.NewCodex(t, standin.Script{})
	subject := newDispatchSubject(t)
	subject.service.catalog = agents.NewCatalog(namedRunner{runner: codex.Runner(), auto: false})
	subject.service.cfg = config.Defaults()
	subject.service.cfg.Agent = []string{agents.CodexName}
	subject.service.registry = newRegistry()

	before := len(codex.Calls())
	d, err := subject.service.dispatchTask(ctx, subject.task)
	if err != nil {
		t.Fatalf("dispatching: %v", err)
	}
	_, err = subject.service.launchTask(ctx, d)
	var refused *routeRefused
	if !errors.As(err, &refused) {
		t.Fatalf("launch answered %v, want a *routeRefused", err)
	}
	var undeclared *agents.CapabilityError
	if !errors.As(err, &undeclared) || undeclared.Capability != agents.CapabilityWorkerSessions {
		t.Fatalf("cause = %v, want a *CapabilityError naming worker_sessions", err)
	}
	if len(codex.Calls()) != before {
		t.Fatalf("the refused launch started %d processes", len(codex.Calls())-before)
	}
	resolved, err := subject.service.tasks().Resolved(ctx, subject.task)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.State.State != taskstate.Failed || resolved.State.Basis.EventKind != routeRefusedKind {
		t.Fatalf("the task resolves %+v, want Failed from route-refused", resolved.State)
	}
	attempts, err := subject.records.TaskAttempts(ctx, subject.task)
	if err != nil || len(attempts) != 1 || attempts[0].Adapter.IsKnown() || attempts[0].RouteReason.IsKnown() ||
		attempts[0].SessionRef.IsKnown() {
		t.Fatalf("attempts = %+v (%v), want no route and no session", attempts, err)
	}
}

func hasArg(args []string, want string) bool {
	for _, a := range args {
		if a == want || strings.Contains(a, want) {
			return true
		}
	}
	return false
}
