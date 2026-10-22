//go:build unix

package service

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/knowledge"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// claudeTmuxLaunchSubject is a launchSubject whose workers run in tmux through
// the shipped Claude adapter over this test binary as the stand-in.
func claudeTmuxLaunchSubject(t *testing.T) *launchSubject {
	t.Helper()
	standin.Tmux(t)
	t.Setenv(agents.ClaudeKeyVariable, "standin-placeholder-key-0123456789abcdefghij")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	runner, err := agents.ClaudeFactory(agents.WithBinary(self)).New(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	// A home under /tmp keeps the tmux socket path within internal/agents' limit,
	// which the launch measurement and a real start both need.
	root, err := standin.TempDir("kn")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	ctx := t.Context()
	h, err := home.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Create(); err != nil {
		t.Fatal(err)
	}
	seedDatabase(t, h.Database())
	records, err := store.Open(ctx, h.Database(), store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = records.Close() })
	id, copyPath := plantLaunchable(t, h, records)
	log, err := h.OpenLog(0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	s := &Service{home: h, store: records, log: log}
	s.catalog = agents.NewCatalog(fixedRunner{runner: runner})
	s.cfg = config.Defaults()
	s.cfg.SessionProvider = config.SessionProviderTmux
	s.cfg.Agent = []string{agents.ClaudeName}
	s.registry = newRegistry()
	sub := &launchSubject{dispatchSubject: &dispatchSubject{service: s, records: records, task: id, copy: copyPath}}
	t.Cleanup(func() {
		for _, err := range sub.service.closeSessions() {
			t.Errorf("closing the sessions: %v", err)
		}
	})
	if len(filepath.Join(h.TerminalSockets(), "0000000000000000")) > 103 {
		t.Fatalf("the home's terminal socket path is too long for tmux: %s", h.TerminalSockets())
	}
	return sub
}

// A knowledge document within the file bound can still refuse a tmux launch
// when the composed first turn exceeds the terminal launch bound internal/agents
// owns. The control is the pipe worker, which is not bounded that way and
// still launches.
func TestAKnowledgeDocumentTooLongForTerminalLaunchRefusesTheLaunch(t *testing.T) {
	ctx := t.Context()
	const large = 20400
	doc := strings.Repeat("a", large)

	t.Run("tmux_claude_refuses", func(t *testing.T) {
		sub := claudeTmuxLaunchSubject(t)
		curate(t, sub.dispatchSubject, doc, true)
		d, err := sub.service.dispatchTask(ctx, sub.task)
		if err != nil {
			t.Fatalf("dispatching: %v", err)
		}
		_, err = sub.service.launchTask(ctx, d)
		var refused *knowledgeRefused
		if !errors.As(err, &refused) || !errors.Is(err, knowledge.ErrLaunchTooLarge) {
			t.Fatalf("launching = %v, want a *knowledgeRefused over ErrLaunchTooLarge", err)
		}
		resolved, err := sub.service.tasks().Task(ctx, sub.task)
		if err != nil {
			t.Fatal(err)
		}
		if resolved.State != taskstate.Failed || resolved.Basis.EventKind != knowledgeRefusedKind {
			t.Fatalf("the task resolves %+v, want Failed from %s", resolved, knowledgeRefusedKind)
		}
		attempts, err := sub.records.TaskAttempts(ctx, sub.task)
		if err != nil || len(attempts) != 1 || attempts[0].SessionRef.IsKnown() || attempts[0].KnowledgeCommit.IsKnown() {
			t.Fatalf("attempts %+v (%v), want one with no session and no knowledge revision", attempts, err)
		}
	})

	t.Run("pipe_worker_still_launches", func(t *testing.T) {
		sub := newLaunchSubject(t, knowledgeWorker(t, "----- end "+knowledge.Document))
		curate(t, sub.dispatchSubject, doc, true)
		d, err := sub.service.dispatchTask(ctx, sub.task)
		if err != nil {
			t.Fatalf("dispatching: %v", err)
		}
		if _, err := sub.service.launchTask(ctx, d); err != nil {
			t.Fatalf("launching on a pipe worker: %v", err)
		}
	})
}
