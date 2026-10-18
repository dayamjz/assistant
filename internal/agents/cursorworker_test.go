package agents_test

import (
	"context"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
)

func TestCursorACPWorkerPreservesMessagesAndProcess(t *testing.T) {
	agent := standin.NewCursor(t, standin.Script{Worker: &standin.WorkerScript{Exchanges: []standin.Exchange{{Contains: "second", Reply: "second response"}}}})
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
	worker.Close()
	calls := agent.Calls()
	if len(calls) != 1 || !slices.Equal(calls[0].Lines, []string{first, second}) {
		t.Fatalf("messages: %v", calls)
	}
	if group <= 0 || worker.Group() != group {
		t.Fatal("worker process changed between messages")
	}
}

func TestInstalledCursorACPWorker(t *testing.T) {
	if os.Getenv("ASSISTANT_CURSOR_WORKER_LIVE") != "1" {
		t.Skip("set ASSISTANT_CURSOR_WORKER_LIVE=1 to probe the installed CLI and model")
	}
	binary, err := exec.LookPath("cursor-agent")
	if err != nil {
		t.Fatal(err)
	}
	factory := agents.CursorFactory(agents.WithCursorBinary(binary))
	runner, err := factory.New(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	worker, err := agents.OpenWorker(ctx, runner, agents.WorkerSpec{Dir: t.TempDir(), Instructions: "Reply with exactly ACP_READY. Do not use tools."})
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		if strings.Contains(worker.Capture().Text, "ACP_READY") {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("installed Cursor did not answer: %s", worker.Capture().Text)
		case <-worker.Done():
			t.Fatalf("installed Cursor exited: %s", worker.Capture().Text)
		case <-tick.C:
		}
	}
}

func TestCursorACPPermissionRepliesAreAppliedOnTheWire(t *testing.T) {
	for _, test := range []struct{ command, want string }{
		{"git --version", "git version"},
		{"echo should-not-run", "permission refused"},
		{"git --version; echo should-not-run", "permission refused"},
	} {
		t.Run(test.command, func(t *testing.T) {
			agent := standin.NewCursor(t, standin.Script{Worker: &standin.WorkerScript{Exchanges: []standin.Exchange{{Contains: "run ", Run: true}}}})
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
