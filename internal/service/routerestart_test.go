package service_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/service"
	"github.com/dayamjz/assistant/internal/session"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
)

// fallbackWorkerOptions is options over a worker role whose configured
// backup is the Cursor adapter reading the stand-in's ACP worker, and whose
// primary, Claude, either has no executable here or, when primaryRuns, is
// the stand-in's Claude worker. The roles table is the operator's own
// configuration file, which is where a service reads it.
func fallbackWorkerOptions(t *testing.T, h *home.Home, primaryRuns bool) (service.Options, *standin.Agent) {
	t.Helper()
	backup := standin.NewCursor(t, standin.Script{})
	var claude *standin.Agent
	primary := agents.ClaudeFactory(agents.WithBinary(filepath.Join(t.TempDir(), "no-claude-here")))
	if primaryRuns {
		claude = standin.New(t, standin.Script{Worker: &standin.WorkerScript{}})
		primary = fixedFactory{runner: claude.Runner()}
	}
	o := options(t, h)
	o.Catalog = agents.NewCatalog(primary, fixedFactory{runner: backup.Runner()})
	return o, claude
}

// A task launched on its role's configured backup is reconciled across a
// restart on the same backup and for the same reason. The first service
// finds the primary cannot start, seals the selection, and launches the
// backup, recording adapter cursor, reason fallback-unavailable, and no
// skipped entry, because the backup is a pin of its own; its Close ends the
// worker. The second service can start the primary, so a launch that routed
// afresh would record claude by override; its reconciliation finds the
// terminal Dead and relaunches in place, and the new attempt reads the
// sealed selection instead, so it records the same adapter and reason and
// the primary is never started.
func TestARestartRelaunchesAFallbackWorkerOnTheSameAdapterForTheSameReason(t *testing.T) {
	t.Parallel()
	service.RequireSweepClearsHere(t, "a restart relaunching a worker launched on its backup")
	h := newHome(t)
	if err := os.WriteFile(h.ConfigFile(),
		[]byte(`{"roles": {"worker": {"agent": "claude", "fallback": {"agent": "cursor"}}}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	subject := newSubject(t)
	quietMaintenance(t, subject)
	repository := recordRepository(t, h, subject)
	id := recordDispatchedTask(t, h, repository, store.TaskDelivery, "task/one")

	var first session.Reference
	var ended session.Provider
	o, _ := fallbackWorkerOptions(t, h, false)
	withServiceOptions(t, o, func(running serviceUnderTest) {
		provider, launched := launchedSession(t, running.service, id)
		ended, first = provider, launched.Reference()
	})
	homeIsFree(t, h)
	if probe := ended.Probe(t.Context(), first.String()); probe.Liveness != taskstate.Dead {
		t.Fatalf("after the first service closed the backup worker probes %+v, want Dead", probe)
	}

	var second session.Reference
	o, claude := fallbackWorkerOptions(t, h, true)
	withServiceOptions(t, o, func(running serviceUnderTest) {
		_, launched := waitSession(t, running.service, id)
		second = launched.Reference()
	})
	if calls := claude.Calls(); len(calls) != 0 {
		t.Fatalf("the relaunch started the primary the sealed selection replaced: %s", calls)
	}
	if first == second {
		t.Fatalf("the relaunch reused the first attempt's reference %s", first)
	}

	records := openRecords(t, h)
	defer func() { _ = records.Close() }()
	attempts, err := records.TaskAttempts(t.Context(), id)
	if err != nil || len(attempts) != 2 {
		t.Fatalf("the task has attempts %+v (%v), want the launch's and the relaunch's", attempts, err)
	}
	for i, a := range attempts {
		if adapter, ok := a.Adapter.Get(); !ok || adapter != agents.CursorName {
			t.Errorf("attempt %d adapter = %q (known=%v), want %s", i+1, adapter, ok, agents.CursorName)
		}
		if reason, ok := a.RouteReason.Get(); !ok || reason != agents.ReasonFallbackUnavailable {
			t.Errorf("attempt %d route reason = %q (known=%v), want %s", i+1, reason, ok, agents.ReasonFallbackUnavailable)
		}
		if skipped, ok := a.RouteSkipped.Get(); !ok || len(skipped) != 0 {
			t.Errorf("attempt %d route skipped = %v (known=%v), want the known empty list of a pinned backup", i+1, skipped, ok)
		}
	}
	if attempts[0].SessionRef.Or("") != first.String() || attempts[1].SessionRef.Or("") != second.String() {
		t.Fatalf("the attempts carry %+v and %+v, want %s then %s", attempts[0].SessionRef, attempts[1].SessionRef, first, second)
	}
	selected, has, err := records.TaskFallback(t.Context(), id)
	if err != nil || !has || selected.Reason != string(agents.FailureUnavailable) || selected.Attempt != 1 {
		t.Fatalf("the sealed selection is %+v (has=%v, %v), want the first attempt's unavailable handoff", selected, has, err)
	}
}
