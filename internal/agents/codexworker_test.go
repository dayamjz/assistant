package agents_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/principles"
)

func TestCodexAppServerWorkerPreservesMessagesAndProcess(t *testing.T) {
	principles.Cite(t, principles.P4)
	agent := standin.NewCodex(t, standin.Script{Worker: &standin.WorkerScript{Exchanges: []standin.Exchange{{Contains: "second", Reply: "second response"}}}})
	first := "initial instructions\nwith quotes \" and Unicode é"
	worker, err := agents.OpenWorker(t.Context(), agent.Runner(), agents.WorkerSpec{Dir: t.TempDir(), Instructions: first})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	group := worker.Group()
	second := "second message\nwith more than one line"
	if err = worker.Send(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(worker.Capture().Text, "second response") {
		if time.Now().After(deadline) {
			t.Fatal("no response to second prompt")
		}
		time.Sleep(5 * time.Millisecond)
	}
	calls := agent.Calls()
	if len(calls) != 1 || !slices.Equal(calls[0].Lines, []string{first, second}) {
		t.Fatalf("messages: %v", calls)
	}
	if !slices.Contains(calls[0].Args, "app-server") || slices.Contains(calls[0].Args, "exec") || slices.Contains(calls[0].Args, "resume") {
		t.Fatalf("the worker command is %v, want app-server and not a review or fixer resume", calls[0].Args)
	}
	if group <= 0 || worker.Group() != group {
		t.Fatal("worker process changed between messages")
	}
	transcript := agents.ReadTranscript(worker.Capture().Text, agents.OutputStreamJSON, false)
	var saw bool
	for _, event := range transcript.Events {
		if event.Kind == agents.TranscriptUnrecognized {
			t.Fatalf("a worker reply was unrecognized: %+v", event)
		}
		if event.Text == "second response" {
			saw = true
		}
	}
	if !saw {
		t.Fatalf("the transcript of %q has no second response", worker.Capture().Text)
	}
}

func TestCodexWorkerJSONReplyStaysText(t *testing.T) {
	const reply = `{"ok":true}`
	agent := standin.NewCodex(t, standin.Script{Worker: &standin.WorkerScript{Exchanges: []standin.Exchange{{Contains: "json", Reply: reply}}}})
	worker, err := agents.OpenWorker(t.Context(), agent.Runner(), agents.WorkerSpec{Dir: t.TempDir(), Instructions: "initial"})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	if err = worker.Send(t.Context(), "json please"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(worker.Capture().Text, `ok`) {
		if time.Now().After(deadline) {
			t.Fatalf("the JSON reply never arrived: %s", worker.Capture().Text)
		}
		time.Sleep(5 * time.Millisecond)
	}
	transcript := agents.ReadTranscript(worker.Capture().Text, agents.OutputStreamJSON, false)
	var saw bool
	for _, event := range transcript.Events {
		if event.Kind == agents.TranscriptUnrecognized {
			t.Fatalf("the JSON reply was unrecognized: %+v\n%s", event, worker.Capture().Text)
		}
		if event.Kind == agents.TranscriptText && event.Text == reply {
			saw = true
		}
	}
	if !saw {
		t.Fatalf("the transcript of %q did not keep the JSON reply as text", worker.Capture().Text)
	}
}

func TestCodexAppServerPermissionRepliesAreAppliedOnTheWire(t *testing.T) {
	for _, test := range []struct{ command, want string }{
		{"git --version", "git version"},
		{"echo should-not-run", "permission refused"},
		{"git --version; echo should-not-run", "permission refused"},
	} {
		t.Run(test.command, func(t *testing.T) {
			agent := standin.NewCodex(t, standin.Script{Worker: &standin.WorkerScript{Exchanges: []standin.Exchange{{Contains: "run ", Run: true}}}})
			worker, err := agents.OpenWorker(t.Context(), agent.Runner(), agents.WorkerSpec{Dir: t.TempDir(), Instructions: "initial"})
			if err != nil {
				t.Fatal(err)
			}
			defer worker.Close()
			if err = worker.Send(t.Context(), "run "+test.command); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(3 * time.Second)
			for !strings.Contains(worker.Capture().Text, test.want) {
				if time.Now().After(deadline) {
					t.Fatalf("permission response never arrived: %s", worker.Capture().Text)
				}
				time.Sleep(5 * time.Millisecond)
			}
		})
	}
}

func TestACodexPinRefusesTerminalWorkers(t *testing.T) {
	factory := agents.CodexFactory(agents.WithCodexBinary(helperBinary(t)))
	_, err := agents.Route(t.Context(), agents.NewCatalog(factory), []string{agents.CodexName}, agents.NeedForWorker(true), agents.CodexName)
	var missing *agents.CapabilityError
	if !errors.As(err, &missing) || missing.Capability != agents.CapabilityTerminalWorkers || missing.Agent != agents.CodexName {
		t.Fatalf("Route answered %v, want a *CapabilityError naming %s and %s", err, agents.CodexName, agents.CapabilityTerminalWorkers)
	}
}

func TestInstalledCodexAppServerWorker(t *testing.T) {
	if os.Getenv("ASSISTANT_CODEX_WORKER_LIVE") != "1" {
		t.Skip("set ASSISTANT_CODEX_WORKER_LIVE=1 to probe the installed CLI and model")
	}
	binary, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	runner, err := agents.CodexFactory(agents.WithCodexBinary(binary)).New(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	worker, err := agents.OpenWorker(ctx, runner, agents.WorkerSpec{Dir: t.TempDir(), Instructions: "Reply with exactly CODEX_READY. Do not use tools."})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		if strings.Contains(worker.Capture().Text, "CODEX_READY") {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("installed Codex did not answer: %s", worker.Capture().Text)
		case <-worker.Done():
			t.Fatalf("installed Codex exited: %s", worker.Capture().Text)
		case <-tick.C:
		}
	}
}

// TestInstalledCodexWorkerGitRunsUnderItsGrant is the live probe that
// recorded testdata/codex-app-server-approvals-0.166.0.jsonl, there with
// an escalated echo first, run against the installed CLI and model: one
// turn asks for an escalated touch, a git commit and an escalated git push
// to a bare repository beside the copy, so each comes to the adapter as a
// command approval. The push reaching the repository is the adapter's
// accept of git read inside the CLI's shell line, and the touched file not
// existing is its decline of anything else; a model that runs the touch
// without asking, which the sandbox would allow, fails it the same way.
// It skips unless ASSISTANT_CODEX_WORKER_LIVE=1, because it spends a model
// turn.
func TestInstalledCodexWorkerGitRunsUnderItsGrant(t *testing.T) {
	if os.Getenv("ASSISTANT_CODEX_WORKER_LIVE") != "1" {
		t.Skip("set ASSISTANT_CODEX_WORKER_LIVE=1 to probe the installed CLI and model")
	}
	binary, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	bare, work := filepath.Join(root, "gate.git"), filepath.Join(root, "work")
	liveGit(t, root, "init", "--quiet", "--bare", bare)
	liveGit(t, root, "init", "--quiet", work)
	for _, setting := range [][]string{{"user.name", "probe"}, {"user.email", "probe@example.invalid"}, {"gc.auto", "0"}, {"maintenance.auto", "false"}} {
		liveGit(t, work, "config", setting[0], setting[1])
	}
	liveGit(t, work, "commit", "--quiet", "--allow-empty", "-m", "probe")
	marker := filepath.Join(root, "declined-marker")
	liveGit(t, work, "remote", "add", "origin", bare)
	runner, err := agents.CodexFactory(agents.WithCodexBinary(binary)).New(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 240*time.Second)
	defer cancel()
	instructions := "This is a protocol probe. Use the shell tool for exactly these commands, in this order, each as its own tool call:\n" +
		"1. touch " + marker + " (with sandbox_permissions set to require_escalated)\n" +
		"2. git commit --allow-empty -m probe-two (in the normal sandbox, no escalation)\n" +
		"3. git push origin HEAD:refs/heads/probe (with sandbox_permissions set to require_escalated)\n" +
		"If a command is declined, do not retry it. If command 2 fails in the sandbox, rerun it once with require_escalated. " +
		"Run nothing else. When done, reply with exactly PROBE_DONE."
	worker, err := agents.OpenWorker(ctx, runner, agents.WorkerSpec{Dir: work, Instructions: instructions, Effort: "low"})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for !strings.Contains(worker.Capture().Text, "PROBE_DONE") {
		select {
		case <-ctx.Done():
			t.Fatalf("installed Codex did not finish the probe: %s", worker.Capture().Text)
		case <-worker.Done():
			t.Fatalf("installed Codex exited: %s", worker.Capture().Text)
		case <-tick.C:
		}
	}
	if pushed := liveGit(t, bare, "for-each-ref", "--format=%(refname)"); !strings.Contains(pushed, "refs/heads/probe") {
		t.Fatalf("the accepted git push did not reach the bare repository: %q\n%s", pushed, worker.Capture().Text)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the touch the grant does not allow ran (%v): %s", err, worker.Capture().Text)
	}
}

// liveGit runs git for the live probe's own setup, with automatic
// maintenance off so no background git outlives the test (#119) and an
// identity stated, since a runner may have none.
func liveGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "gc.auto=0", "-c", "maintenance.auto=false",
		"-c", "user.name=probe", "-c", "user.email=probe@example.invalid"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// The Codex adapter starts a subordinate coordinator under GrantSubordinate
// the same way it starts a worker, with the app-server thread under
// approvalPolicy untrusted so every command asks before it runs. A grant
// it has no translation for is still refused with ErrGrantUnenforced before
// anything starts.
func TestCodexStartsASubordinateUnderItsGrant(t *testing.T) {
	principles.Cite(t, principles.P4)
	agent := standin.NewCodex(t, standin.Script{Worker: &standin.WorkerScript{}})
	spec := agents.WorkerSpec{Dir: t.TempDir(), Instructions: "Coordinate the domain.", Route: "echo routed"}
	sub, err := agents.OpenSubordinate(t.Context(), agent.Runner(), spec)
	if err != nil {
		t.Fatalf("OpenSubordinate on Codex: %v", err)
	}
	sub.Close()
	if calls := agent.Calls(); len(calls) != 1 {
		t.Fatalf("the subordinate started %d processes, want 1", len(calls))
	}
}

// A Codex subordinate accepts only its route and declines git: the stand-in
// asks each Run exchange as Codex CLI 0.166.0 asked a command approval, and
// the adapter's GrantSubordinate answers accept only for the route.
func TestACodexSubordinateHoldsApprovalsToItsGrant(t *testing.T) {
	route := "echo routed"
	agent := standin.NewCodex(t, standin.Script{Worker: &standin.WorkerScript{
		Exchanges: []standin.Exchange{{Contains: "run ", Run: true}},
	}})
	sub, err := agents.OpenSubordinate(t.Context(), agent.Runner(), agents.WorkerSpec{
		Dir: t.TempDir(), Instructions: "Coordinate.", Route: route,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	for _, test := range []struct{ command, want string }{
		{"git status", "permission refused"},
		{route, "routed"},
	} {
		if err := sub.Send(t.Context(), "run "+test.command); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(3 * time.Second)
		for !strings.Contains(sub.Capture().Text, test.want) {
			if time.Now().After(deadline) {
				t.Fatalf("after %q the capture never held %q: %s", test.command, test.want, sub.Capture().Text)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
}
