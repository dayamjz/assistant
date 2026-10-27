package service

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/config"
	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/store"
)

// P6: a quota failure replaces the worker only after stopping it and proving
// custody of the same copy. An uncommitted file survives the handoff, and the
// second attempt records Cursor rather than attributing its work to Claude.
func TestQuotaWorkerHandoffPreservesUncommittedWork(t *testing.T) {
	principles.Cite(t, principles.P6)
	const unestablished = "that a quota handoff relaunches the backup in the same copy"
	requireSweepClearsHere(t, unestablished)
	primary := standin.New(t, standin.Script{Worker: &standin.WorkerScript{Exchanges: []standin.Exchange{{Contains: "work", Recorded: []string{`{"type":"result","is_error":true,"result":"quota exceeded"}`}}}}})
	backup := standin.NewCursor(t, standin.Script{})
	sub := newLaunchSubject(t, primary)
	s := sub.service
	s.catalog = agents.NewCatalog(fixedRunner{runner: primary.Runner()}, fixedRunner{runner: backup.Runner()})
	s.cfg.Roles = config.Roles{config.RoleWorker: {Agent: "claude", Fallback: &config.Role{Agent: "cursor"}}}
	s.terminals = heldTerminals{s}
	s.stopCtx, s.stopCancel = context.WithCancel(t.Context())
	t.Cleanup(func() { s.stopCancel(); s.stopWork(); s.work.Wait() })
	d, err := s.dispatchTask(t.Context(), sub.task)
	if err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(sub.copy, "uncommitted.txt")
	if err = os.WriteFile(sentinel, []byte("keep this work"), 0600); err != nil {
		t.Fatal(err)
	}
	// The backup's launch is held at the moment its reference is recorded,
	// before its hold is settled, until a reader is waiting on that
	// settlement, so the loop below reads the service in the order #538 lost
	// on CI rather than wherever the poll happens to land. The first
	// recording is the primary's, which this goroutine makes.
	release := make(chan struct{})
	var letGo sync.Once
	var recordings atomic.Int32
	s.afterSessionRecorded = func(string, string) {
		if recordings.Add(1) == 1 {
			return
		}
		select {
		case <-release:
		case <-s.stopCtx.Done():
		}
	}
	s.awaitingSessionRecord = func(string) { letGo.Do(func() { close(release) }) }
	t.Cleanup(func() { letGo.Do(func() { close(release) }) })
	first, err := s.launchTask(t.Context(), d)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		attempts, err := s.store.TaskAttempts(t.Context(), sub.task)
		if err != nil {
			t.Fatal(err)
		}
		if len(attempts) == 2 && attempts[1].SessionRef.IsKnown() {
			adapter, _ := attempts[1].Adapter.Get()
			if adapter != "cursor" {
				t.Fatalf("backup attributed to %s", adapter)
			}
			// launchTask holds a session before it records the reference,
			// so a record naming the backup's session is a session held;
			// sessionOf answers it once the hold is settled.
			_, current, held := s.sessionOf(sub.task)
			if !held || current == nil {
				t.Fatalf("attempt %d records session %v and this service holds no session for task %s",
					attempts[1].Sequence, attempts[1].SessionRef, sub.task)
			}
			if recorded, _ := attempts[1].SessionRef.Get(); current.Reference().String() != recorded {
				t.Fatalf("attempt %d records session %s and this service holds %s", attempts[1].Sequence, recorded, current.Reference())
			}
			if current.Reference() == first.Reference() {
				t.Fatal("worker was not replaced")
			}
			break
		}
		if time.Now().After(deadline) {
			f, has, _ := s.store.TaskFallback(t.Context(), sub.task)
			fatalUnlessSweepsStoppedClearing(t, unestablished, "no backup attempt: attempts=%+v fallback=%+v has=%v", attempts, f, has)
		}
		time.Sleep(20 * time.Millisecond)
	}
	bytes, err := os.ReadFile(sentinel)
	if err != nil || string(bytes) != "keep this work" {
		t.Fatalf("uncommitted work changed: %q %v", bytes, err)
	}
	f, has, err := s.store.TaskFallback(t.Context(), sub.task)
	if err != nil || !has || f.Reason != "quota" {
		t.Fatalf("handoff record %+v %v %v", f, has, err)
	}
}

// A sweep that cannot prove the copy empty must not turn a quota handoff
// into a hold. The backup was not asked. A hold would resolve the task as
// held, and the next start would leave it there instead of trying the backup
// once a sweep can clear.
func TestABlockedSweepLeavesAQuotaHandoffRetryable(t *testing.T) {
	principles.Cite(t, principles.P6)
	primary := standin.New(t, standin.Script{Worker: &standin.WorkerScript{Exchanges: []standin.Exchange{{Contains: "work", Recorded: []string{`{"type":"result","is_error":true,"result":"quota exceeded"}`}}}}})
	backup := standin.NewCursor(t, standin.Script{})
	sub := newLaunchSubject(t, primary)
	s := sub.service
	s.catalog = agents.NewCatalog(fixedRunner{runner: primary.Runner()}, fixedRunner{runner: backup.Runner()})
	s.cfg.Roles = config.Roles{config.RoleWorker: {Agent: "claude", Fallback: &config.Role{Agent: "cursor"}}}
	s.terminals = heldTerminals{s}
	s.stopCtx, s.stopCancel = context.WithCancel(t.Context())
	t.Cleanup(func() { s.stopCancel(); s.stopWork(); s.work.Wait() })
	d, err := s.dispatchTask(t.Context(), sub.task)
	if err != nil {
		t.Fatal(err)
	}
	s.sweepCopy = func(context.Context, string) (agents.SweepResult, error) {
		return agents.SweepResult{}, nil
	}
	if _, err = s.launchTask(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		f, has, readErr := s.store.TaskFallback(t.Context(), sub.task)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if has && f.Reason == "quota" {
			if _, _, held := s.sessionOf(sub.task); !held {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("the quota handoff was not recorded")
		}
		time.Sleep(20 * time.Millisecond)
	}
	hold, err := s.store.Hold(t.Context(), workerFallbackHoldKey(sub.task, d.attempt))
	if err == nil && hold.Open() {
		t.Fatalf("a sweep that did not clear opened %s", hold.Key)
	}
	attempts, err := s.store.TaskAttempts(t.Context(), sub.task)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 {
		t.Fatalf("attempts = %d, want the primary only", len(attempts))
	}
	if len(backup.Calls()) != 0 {
		t.Fatal("the backup was started without a sweep that cleared")
	}
}

func TestMissingWorkerSelectsConfiguredBackupBeforeLaunch(t *testing.T) {
	backup := standin.NewCursor(t, standin.Script{})
	sub := newLaunchSubject(t, backup)
	s := sub.service
	missing := agents.ClaudeFactory(agents.WithBinary(filepath.Join(t.TempDir(), "missing-claude")))
	s.catalog = agents.NewCatalog(missing, fixedRunner{runner: backup.Runner()})
	s.cfg.Roles = config.Roles{config.RoleWorker: {Agent: "claude", Fallback: &config.Role{Agent: "cursor"}}}
	d, err := s.dispatchTask(t.Context(), sub.task)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.launchTask(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	attempts, err := s.store.TaskAttempts(t.Context(), sub.task)
	if err != nil {
		t.Fatal(err)
	}
	adapter, _ := attempts[0].Adapter.Get()
	if adapter != "cursor" {
		t.Fatalf("selected %q", adapter)
	}
	f, has, err := s.store.TaskFallback(t.Context(), sub.task)
	if err != nil || !has || f != (store.TaskFallback{Primary: "claude", Backup: "cursor", Reason: "unavailable", Attempt: d.attempt}) {
		t.Fatalf("selection %+v %v %v", f, has, err)
	}
}

func TestCursorBackupWithTmuxIsRefusedBeforeAnyWorkerStarts(t *testing.T) {
	primary := standin.New(t, standin.Script{})
	backup := standin.NewCursor(t, standin.Script{})
	sub := newLaunchSubject(t, primary)
	s := sub.service
	s.catalog = agents.NewCatalog(fixedRunner{runner: primary.Runner()}, fixedRunner{runner: backup.Runner()})
	s.cfg.SessionProvider = config.SessionProviderTmux
	s.cfg.Roles = config.Roles{config.RoleWorker: {Agent: "claude", Fallback: &config.Role{Agent: "cursor"}}}
	d, err := s.dispatchTask(t.Context(), sub.task)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.launchTask(t.Context(), d); !errors.Is(err, agents.ErrUndeclaredCapability) {
		t.Fatalf("unsupported backup: %v", err)
	}
	if len(primary.Calls()) != 0 || len(backup.Calls()) != 0 {
		t.Fatal("an invalid backup configuration started a worker")
	}
	if _, has, err := s.store.TaskFallback(t.Context(), sub.task); err != nil || has {
		t.Fatalf("invalid backup was sealed: %v %v", has, err)
	}
}

// A worker backup whose context window its adapter has not declared is
// refused before any worker starts, as any backup it cannot run is: the
// primary is not started on the strength of a backup that could never take
// over, and nothing is sealed. Codex declares no window, and a window with no
// model is one no adapter can express.
func TestABackupWithAnUndeclaredContextWindowIsRefusedBeforeAnyWorkerStarts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		backup config.Role
		window string
	}{
		{"codex at standard", config.Role{Agent: agents.CodexName, Provider: "openai", Type: "gpt", Version: "6-astra", ContextWindow: config.ContextStandard}, "context standard"},
		{"a window with no model", config.Role{Agent: agents.CodexName, ContextWindow: config.Context1M}, "context 1m"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			primary := standin.New(t, standin.Script{})
			backup := standin.NewCodex(t, standin.Script{})
			sub := newLaunchSubjectAs(t, primary, "pages")
			s := sub.service
			s.catalog = agents.NewCatalog(fixedRunner{runner: primary.Runner()}, fixedRunner{runner: backup.Runner()})
			s.cfg.Roles = config.Roles{"pages": {Agent: agents.ClaudeName, Fallback: &tc.backup}}
			d, err := s.dispatchTask(t.Context(), sub.task)
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.launchTask(t.Context(), d)
			var refused *routeRefused
			if !errors.As(err, &refused) || !errors.Is(err, agents.ErrUndeclaredModel) {
				t.Fatalf("the launch answered %v, want a *routeRefused over ErrUndeclaredModel for the backup's window", err)
			}
			// PRD section 10: the refusal names the role, the triple, and
			// the window, on the backup's path as on the primary's.
			if detail := routeRefusedDetail(t, sub); !strings.Contains(detail, "the pages role") || !strings.Contains(detail, tc.window) {
				t.Fatalf("route-refused detail %q does not name the pages role and %s", detail, tc.window)
			}
			if len(primary.Calls()) != 0 || len(backup.Calls()) != 0 {
				t.Fatal("a backup whose window cannot be expressed let a worker start")
			}
			if _, has, err := s.store.TaskFallback(t.Context(), sub.task); err != nil || has {
				t.Fatalf("the refused backup was sealed: %v %v", has, err)
			}
		})
	}
}

func TestASealedSelectionDoesNotPermitASecondLiveWorker(t *testing.T) {
	primary := standin.New(t, standin.Script{})
	backup := standin.NewCursor(t, standin.Script{})
	sub := newLaunchSubject(t, primary)
	s := sub.service
	s.catalog = agents.NewCatalog(fixedRunner{runner: primary.Runner()}, fixedRunner{runner: backup.Runner()})
	s.cfg.Roles = config.Roles{config.RoleWorker: {Agent: "claude", Fallback: &config.Role{Agent: "cursor"}}}
	s.terminals = heldTerminals{s}
	d, err := s.dispatchTask(t.Context(), sub.task)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.launchTask(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	if err = s.store.RecordTaskFallback(t.Context(), sub.task, store.TaskFallback{Primary: "claude", Backup: "cursor", Reason: "quota", Attempt: d.attempt}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.relaunchTask(t.Context(), sub.task); !errors.Is(err, errRelaunchNotDue) {
		t.Fatalf("a live primary allowed relaunch: %v", err)
	}
	if len(backup.Calls()) != 0 {
		t.Fatal("backup started beside primary")
	}
}

func TestFailedBackupRaisesAHoldAndRetryPreservesTheCopy(t *testing.T) {
	const unestablished = "that a backup retry reclaims the standing copy"
	requireSweepClearsHere(t, unestablished)
	primary := standin.New(t, standin.Script{Worker: &standin.WorkerScript{Exchanges: []standin.Exchange{{Contains: "work", Recorded: []string{`{"type":"result","is_error":true,"result":"quota exceeded"}`}}}}})
	backup := standin.NewCursor(t, standin.Script{})
	sub := newLaunchSubject(t, primary)
	s := sub.service
	binary, err := exec.LookPath("true")
	if err != nil {
		t.Fatal(err)
	}
	// A binary that exits before ACP initialization models an unavailable
	// backup through the real process/protocol path, without a typed fake.
	unavailable := agents.CursorFactory(agents.WithCursorBinary(binary))
	s.catalog = agents.NewCatalog(fixedRunner{runner: primary.Runner()}, unavailable)
	s.cfg.Roles = config.Roles{config.RoleWorker: {Agent: "claude", Fallback: &config.Role{Agent: "cursor"}}}
	s.terminals = heldTerminals{s}
	s.stopCtx, s.stopCancel = context.WithCancel(t.Context())
	t.Cleanup(func() { s.stopCancel(); s.stopWork(); s.work.Wait() })
	d, err := s.dispatchTask(t.Context(), sub.task)
	if err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(sub.copy, "uncommitted.txt")
	if err = os.WriteFile(sentinel, []byte("preserved"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.launchTask(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	key := workerFallbackHoldKey(sub.task, 2)
	deadline := time.Now().Add(15 * time.Second)
	for {
		hold, err := s.store.Hold(t.Context(), key)
		if err == nil && hold.Open() {
			break
		}
		if time.Now().After(deadline) {
			fatalUnlessSweepsStoppedClearing(t, unestablished, "backup failure raised no open hold")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// An explicit retry while the backup is still unavailable must produce a
	// new visible decision, rather than only logging the failed launch.
	s.cfg.RelaunchAttempts = 5
	firstRetry, retryErr := s.respondTask(t.Context(), ipc.Request{}, machine.TaskRespondRequest{Task: sub.task, Hold: key, Answer: machine.AnswerRetry})
	if retryErr != nil || firstRetry.Effect != machine.AnswerRedispatched {
		fatalUnlessSweepsStoppedClearing(t, unestablished, "unavailable retry: %+v %v", firstRetry, retryErr)
	}
	key = workerFallbackHoldKey(sub.task, 3)
	deadline = time.Now().Add(15 * time.Second)
	for {
		hold, readErr := s.store.Hold(t.Context(), key)
		if readErr == nil && hold.Open() {
			break
		}
		if time.Now().After(deadline) {
			fatalUnlessSweepsStoppedClearing(t, unestablished, "repeated backup failure raised no new hold")
		}
		time.Sleep(10 * time.Millisecond)
	}
	s.catalog = agents.NewCatalog(fixedRunner{runner: primary.Runner()}, fixedRunner{runner: backup.Runner()})
	answered, err := s.respondTask(t.Context(), ipc.Request{}, machine.TaskRespondRequest{Task: sub.task, Hold: key, Answer: machine.AnswerRetry})
	if err != nil || answered.Effect != machine.AnswerRedispatched {
		fatalUnlessSweepsStoppedClearing(t, unestablished, "retry: %+v %v", answered, err)
	}
	deadline = time.Now().Add(15 * time.Second)
	for {
		attempts, err := s.store.TaskAttempts(t.Context(), sub.task)
		if err != nil {
			t.Fatal(err)
		}
		if len(attempts) == 4 && attempts[3].SessionRef.IsKnown() {
			break
		}
		if time.Now().After(deadline) {
			fatalUnlessSweepsStoppedClearing(t, unestablished, "retry did not start backup: %+v", attempts)
		}
		time.Sleep(10 * time.Millisecond)
	}
	bytes, err := os.ReadFile(sentinel)
	if err != nil || string(bytes) != "preserved" {
		t.Fatalf("work lost on retry: %q %v", bytes, err)
	}
}
