package agents_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
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

// The Codex adapter refuses a subordinate coordinator before anything
// starts, with ErrGrantUnenforced, both at the launch-refusal ask and at
// OpenSubordinate, and the stand-in records no call; the same spec through
// OpenWorker starts, the control that the refusal is the grant's.
func TestCodexRefusesASubordinateItCannotHoldToItsGrant(t *testing.T) {
	principles.Cite(t, principles.P4)
	agent := standin.NewCodex(t, standin.Script{Worker: &standin.WorkerScript{}})
	spec := agents.WorkerSpec{Dir: t.TempDir(), Instructions: "Coordinate the domain.", Route: "echo routed"}
	if _, err := agents.OpenSubordinate(t.Context(), agent.Runner(), spec); !errors.Is(err, agents.ErrGrantUnenforced) {
		t.Fatalf("OpenSubordinate on Codex = %v, want ErrGrantUnenforced", err)
	}
	if calls := agent.Calls(); len(calls) != 0 {
		t.Fatalf("the refused subordinate started %d processes", len(calls))
	}
	worker, err := agents.OpenWorker(t.Context(), agent.Runner(), spec)
	if err != nil {
		t.Fatalf("the control worker: %v", err)
	}
	worker.Close()
}
