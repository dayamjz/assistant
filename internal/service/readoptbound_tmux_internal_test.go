//go:build unix

package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net"
	"os/exec"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/session"
	"github.com/dayamjz/assistant/internal/store"
)

// unansweredWorker records a task whose latest attempt names a worker that
// is present and a tmux server that never answers: the pane is a process
// this test started, and the socket the reference names is one this test
// listens on and never accepts from, so a tmux client connects and waits,
// as it does on a server that has stopped.
func unansweredWorker(t *testing.T, h *home.Home, records *store.Store, adapter string) {
	t.Helper()
	ctx := t.Context()
	pane := exec.Command("sleep", "60")
	if err := pane.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pane.Process.Kill(); _ = pane.Wait() })
	identity, found, err := agents.Identify(pane.Process.Pid)
	if err != nil || !found {
		t.Fatalf("reading the identity of process %d: %v (found %t)", pane.Process.Pid, err, found)
	}
	name := make([]byte, 8)
	if _, err := rand.Read(name); err != nil {
		t.Fatal(err)
	}
	terminal := hex.EncodeToString(name)
	listener, err := net.Listen("unix", h.TerminalSocket(terminal))
	if err != nil {
		t.Fatalf("listening on the terminal socket: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	id, err := home.NewTaskID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := records.CreateTask(ctx, store.Task{
		ID: id, Shape: store.TaskDelivery, Project: "one", Mode: "interactive", WorktreePath: "/w/" + id,
		RepositoryID: store.Known("subject"), Branch: store.Known("task/" + id),
	}, store.Disposition{}); err != nil {
		t.Fatalf("creating the task: %v", err)
	}
	if _, err := records.BeginFirstTaskAttempt(ctx, id, store.OnThisMachine()); err != nil {
		t.Fatalf("beginning the attempt of %s: %v", id, err)
	}
	if err := records.SetAttemptRoute(ctx, id, 1, adapter, "test", store.AttemptLaunch{}); err != nil {
		t.Fatalf("recording the route of %s: %v", id, err)
	}
	ref := session.Reference{Home: session.HomeMarker(h), Task: id, PID: identity.PID, Start: identity.Start, Terminal: terminal}
	if err := records.SetAttemptSession(ctx, id, 1, ref.String(), store.SessionHost{}); err != nil {
		t.Fatalf("recording the session of %s: %v", id, err)
	}
}

// Re-adoption waits on servers that do not answer once for them all, however
// many there are: twelve workers whose servers never answer, more than any
// batch a cap on how many are asked at once would take in one round, are
// asked together under one readoptBound, so the pass is held back by that
// bound and not by a multiple of it. None is held, since none verifies.
func TestReadoptionWaitsOnceForEveryServerThatDoesNotAnswer(t *testing.T) {
	standin.Tmux(t)
	h, records := loopHomeConfigured(t, loopConfig)
	defer func() { _ = records.Close() }()
	if _, err := records.UpsertRepository(t.Context(), store.Repository{
		ID: "subject", WorkingPath: t.TempDir(), UpstreamURL: "https://example.invalid/subject.git", DefaultBranch: "main",
	}); err != nil {
		t.Fatal(err)
	}
	runner := workerStandIn(t).Runner()
	const workers = 12
	for range workers {
		unansweredWorker(t, h, records, runner.Name())
	}
	log, err := h.OpenLog(0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	s := &Service{home: h, store: records, log: log, cfg: config.Defaults(), registry: newRegistry(),
		catalog: agents.NewCatalog(fixedRunner{runner: runner}), launched: make(map[string]launchedWorker)}
	bound, cancel := context.WithTimeout(t.Context(), reconcileProbeBound)
	defer cancel()
	began := time.Now()
	s.readoptWorkers(t.Context(), bound)
	took := time.Since(began)
	t.Logf("re-adoption behind %d servers that do not answer took %v", workers, took)
	if took >= 2*readoptBound {
		t.Fatalf("re-adoption behind %d servers that do not answer took %v, want under %v: it waited on them in rounds",
			workers, took, 2*readoptBound)
	}
	if len(s.launched) != 0 {
		t.Fatalf("re-adoption held %d workers whose servers never answered", len(s.launched))
	}
}
