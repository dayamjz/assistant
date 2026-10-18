package service

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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
	requireSweepClearsHere(t, "that a quota handoff relaunches the backup in the same copy")
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
	sentinel := filepath.Join(sub.copy, "uncommitted.txt")
	if err = os.WriteFile(sentinel, []byte("keep this work"), 0600); err != nil {
		t.Fatal(err)
	}
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
			_, current, _ := s.sessionOf(sub.task)
			if current.Reference() == first.Reference() {
				t.Fatal("worker was not replaced")
			}
			break
		}
		if time.Now().After(deadline) {
			f, has, _ := s.store.TaskFallback(t.Context(), sub.task)
			t.Fatalf("no backup attempt: attempts=%+v fallback=%+v has=%v", attempts, f, has)
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
	requireSweepClearsHere(t, "that a backup retry reclaims the standing copy")
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
			t.Fatal("backup failure raised no open hold")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// An explicit retry while the backup is still unavailable must produce a
	// new visible decision, rather than only logging the failed launch.
	s.cfg.RelaunchAttempts = 5
	firstRetry, retryErr := s.respondTask(t.Context(), ipc.Request{}, machine.TaskRespondRequest{Task: sub.task, Hold: key, Answer: machine.AnswerRetry})
	if retryErr != nil || firstRetry.Effect != machine.AnswerRedispatched {
		t.Fatalf("unavailable retry: %+v %v", firstRetry, retryErr)
	}
	key = workerFallbackHoldKey(sub.task, 3)
	deadline = time.Now().Add(15 * time.Second)
	for {
		hold, readErr := s.store.Hold(t.Context(), key)
		if readErr == nil && hold.Open() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("repeated backup failure raised no new hold")
		}
		time.Sleep(10 * time.Millisecond)
	}
	s.catalog = agents.NewCatalog(fixedRunner{runner: primary.Runner()}, fixedRunner{runner: backup.Runner()})
	answered, err := s.respondTask(t.Context(), ipc.Request{}, machine.TaskRespondRequest{Task: sub.task, Hold: key, Answer: machine.AnswerRetry})
	if err != nil || answered.Effect != machine.AnswerRedispatched {
		t.Fatalf("retry: %+v %v", answered, err)
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
			t.Fatalf("retry did not start backup: %+v", attempts)
		}
		time.Sleep(10 * time.Millisecond)
	}
	bytes, err := os.ReadFile(sentinel)
	if err != nil || string(bytes) != "preserved" {
		t.Fatalf("work lost on retry: %q %v", bytes, err)
	}
}
