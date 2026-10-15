package session_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/session"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/tasks"
	"github.com/dayamjz/assistant/internal/vcs"
)

// waitFor bounds every poll in this package. The worker answers on its own
// schedule, and a test synchronizes on what the handle's Capture shows
// rather than on a file-polled record (#124).
const waitFor = 30 * time.Second

// rawGit runs git directly, on the terms internal/tasks' isolation tests
// state: the subject a proof is asserted over is built with git itself, not
// with the package under validation. It runs under the configuration
// subject wrote, with automatic maintenance off (#119).
func rawGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
}

// subject is a home with a task whose delivery copy has been asserted
// isolated: the proof Create takes, and the identifiers around it.
type subject struct {
	home      *home.Home
	id        string
	isolation tasks.Isolation
}

// newSubject builds a home, a gate carrying a primary checkout's main
// branch, a delivery clone at the task's copy path on a branch of its own,
// and asserts the clone isolated through tasks.AssertIsolated, so the
// Isolation a test hands Create is a real proof and not a value written
// from nothing.
func newSubject(t *testing.T) *subject {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(cfg, []byte("[user]\n\tname = Test\n\temail = test@example.invalid\n"+
		"[init]\n\tdefaultBranch = main\n[gc]\n\tauto = 0\n[maintenance]\n\tauto = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")

	// The root is short rather than the test's own directory, which carries
	// the test's name: the tmux provider puts each worker's socket under it,
	// and a local socket path is bounded.
	root := standin.SocketDir(t)
	h, err := home.Open(root)
	if err != nil {
		t.Fatalf("home.Open: %v", err)
	}
	if err := h.Create(); err != nil {
		t.Fatalf("home.Create: %v", err)
	}
	primary := filepath.Join(t.TempDir(), "primary")
	if err := os.Mkdir(primary, 0o700); err != nil {
		t.Fatal(err)
	}
	rawGit(t, primary, "init", "-q")
	if err := os.WriteFile(filepath.Join(primary, "README"), []byte("subject\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rawGit(t, primary, "add", "README")
	rawGit(t, primary, "commit", "-q", "-m", "first")
	gatePath := filepath.Join(h.Root(), "repos", "subject.git")
	if err := os.MkdirAll(filepath.Dir(gatePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := vcs.InitBare(context.Background(), gatePath); err != nil {
		t.Fatalf("InitBare: %v", err)
	}
	rawGit(t, primary, "push", "-q", gatePath, "main:main")

	id, err := home.NewTaskID()
	if err != nil {
		t.Fatal(err)
	}
	copyPath, err := h.TaskCopy(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(copyPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := vcs.Clone(context.Background(), gatePath, copyPath, gate.RemoteName); err != nil {
		t.Fatalf("Clone: %v", err)
	}
	rawGit(t, copyPath, "switch", "-q", "-c", "topic")

	want, _, err := tasks.Expect(h, id, store.TaskDelivery, gatePath, primary, "topic")
	if err != nil {
		t.Fatalf("Expect: %v", err)
	}
	obs, err := vcs.Observe(context.Background(), copyPath)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	proof, err := tasks.AssertIsolated(obs, vcs.StripRedirecting(os.Environ()), want)
	if err != nil {
		t.Fatalf("AssertIsolated: %v", err)
	}
	return &subject{home: h, id: id, isolation: proof}
}

// spec is a Create spec for the subject's task.
func (s *subject) spec(instructions string) session.Spec {
	return session.Spec{TaskID: s.id, Isolation: s.isolation, Instructions: instructions}
}

// workerScript is the stand-in script most tests here run: a greeting, a
// reply to a status line, and an exchange that ends the worker.
func workerScript() standin.Script {
	return standin.Script{Worker: &standin.WorkerScript{
		Greeting: "worker ready",
		Exchanges: []standin.Exchange{
			{Contains: "status", Reply: "still working on it"},
			{Contains: "finish", Reply: "all done", Ends: true},
		},
	}}
}

// provider is the reference provider over a stand-in worker adapter, with a
// short termination grace so a Close in a test costs little.
func provider(t *testing.T, h *home.Home, script standin.Script) (*session.Pipe, *standin.Agent) {
	t.Helper()
	agent := standin.NewWorker(t, script, agents.WithTerminationGrace(2*time.Second))
	return session.NewPipe(h, agent.Runner()), agent
}

// create is Create or a fatal failure, with the session closed at cleanup.
func create(t *testing.T, p *session.Pipe, spec session.Spec) *session.Session {
	t.Helper()
	s, err := p.Create(context.Background(), spec)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { _ = p.Close(s) })
	return s
}

// waitCapture polls Capture until its text carries want, and returns the
// capture that did.
func waitCapture(t *testing.T, p session.Provider, s *session.Session, want string) session.Capture {
	t.Helper()
	deadline := time.Now().Add(waitFor)
	for {
		got, err := p.Capture(s)
		if err != nil {
			t.Fatalf("Capture: %v", err)
		}
		if strings.Contains(got.Text, want) {
			return got
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the capture never carried %q; it is:\n%s", want, got.Text)
		}
		time.Sleep(time.Millisecond)
	}
}

// waitProbe polls Probe until it answers with reason, and returns that
// observation. A process ends on the operating system's schedule, so a test
// that killed one waits for the provider to see it rather than asserting at
// once.
func waitProbe(t *testing.T, p session.Provider, ref string, reason session.Reason) session.Observation {
	t.Helper()
	deadline := time.Now().Add(waitFor)
	for {
		got := p.Probe(context.Background(), ref)
		if got.Reason == reason {
			return got
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("the probe never answered %s; it answers %+v", reason, got)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// expect asserts an observation's reason and that its liveness is the
// reason's, read off the one table.
func expect(t *testing.T, got session.Observation, reason session.Reason) {
	t.Helper()
	if got.Reason != reason {
		t.Fatalf("probe answered %+v, want reason %s", got, reason)
	}
	if got.Liveness != reason.Liveness() {
		t.Fatalf("probe answered liveness %s with reason %s, whose liveness is %s", got.Liveness, got.Reason, reason.Liveness())
	}
}
