package service_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/service"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
)

func TestChildMergeAndParentGateAreRefused(t *testing.T) {
	principles.Cite(t, principles.P14)
	parent, sub := startSubordinate(t, store.RecoveryNone)
	ctx := t.Context()
	err := sub.Child().AttemptDelegatedMerge(ctx)
	if !errors.Is(err, service.ErrDelegationMerge) {
		t.Fatalf("child merge = %v, want %v", err, service.ErrDelegationMerge)
	}
	parentErr := parent.service.AttemptDelegatedMerge(ctx)
	if errors.Is(parentErr, service.ErrDelegationMerge) {
		t.Fatal("the parent was refused as a subordinate")
	}
	if err := sub.Child().DeliverDelegated(ctx, sub.Tasks()[0]); !errors.Is(err, service.ErrDeliveryNotOwned) {
		t.Fatalf("child delivery = %v, want %v", err, service.ErrDeliveryNotOwned)
	}

	command := testCommand(t)
	if err := sub.Child().ClaimGate(ctx, parent.branch, command); !errors.Is(err, store.ErrParentGate) {
		t.Fatalf("claiming the parent's branch = %v, want %v", err, store.ErrParentGate)
	}
	if remotes := git(t, parent.branch, "remote"); strings.Contains(remotes, "assistant") {
		t.Fatalf("the parent's branch gained an assistant remote:\n%s", remotes)
	}
	if err := sub.Child().ClaimGate(ctx, parent.own, command); err != nil {
		t.Fatalf("claiming the child's own copy: %v", err)
	}
	repos := filepath.Join(parent.childHome.Root(), "repos")
	entries, err := os.ReadDir(repos)
	if err != nil || len(entries) == 0 {
		t.Fatalf("the child's gate is not under its own home (%s): %v", repos, err)
	}
	if _, err := os.Stat(filepath.Join(parent.home.Root(), "repos")); err == nil {
		t.Fatal("the parent home gained a gate repository")
	}
}

func TestParentRestartDoesNotDuplicateADelegatedTask(t *testing.T) {
	principles.Cite(t, principles.P14, principles.P6)
	parent, sub := startSubordinate(t, store.RecoveryNone)
	ctx := t.Context()
	task := sub.Tasks()[0]
	copy := sub.Copy()
	if err := parent.service.DeliverDelegated(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := sub.Close(); err != nil {
		t.Fatal(err)
	}
	closeService(t, parent.service, parent.served)
	parent.service = nil

	restarted, err := service.Open(ctx, options(t, parent.home))
	if err != nil {
		requiresLocalSocket(t, err)
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- restarted.Serve(context.Background()) }()
	t.Cleanup(func() { closeService(t, restarted, served) })

	tasks, err := restarted.TasksForTest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].ID != task {
		t.Fatalf("tasks after restart = %+v, want one %s", tasks, task)
	}
	attempts, err := restarted.AttemptsForTest(ctx, task)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 {
		t.Fatalf("attempts after restart = %d, want 1", len(attempts))
	}
	if err := restarted.DeliverDelegated(ctx, task); !errors.Is(err, store.ErrDelegationDelivered) {
		t.Fatalf("second delivery = %v, want %v", err, store.ErrDelegationDelivered)
	}
	if err := restarted.RelaunchForTest(ctx, task); !errors.Is(err, service.ErrDelegationOwns) {
		t.Fatalf("relaunch after restart = %v, want %v", err, service.ErrDelegationOwns)
	}
	attempts, err = restarted.AttemptsForTest(ctx, task)
	if err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 {
		t.Fatalf("attempts after the refused relaunch = %d, want 1", len(attempts))
	}
	if _, err := os.ReadFile(filepath.Join(copy, "kept.txt")); err != nil {
		t.Fatalf("the copy was not kept: %v", err)
	}
}

func TestDroppedDelegationIsUnobservedAndKeepsTheCopy(t *testing.T) {
	principles.Cite(t, principles.P6)
	parent, sub := startSubordinate(t, store.RecoveryRelaunchIfDead)
	ctx := t.Context()
	const secret = "Bearer s3cret-token https://user:s3cret-token@example.test/x.git"
	if err := os.WriteFile(filepath.Join(sub.Copy(), "secret.txt"), []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	before := sub.Observe(ctx)
	if !before.Connected || before.Liveness != taskstate.Unobserved {
		t.Fatalf("before the drop: %+v", before)
	}
	foreign := exec.Command(testCommand(t), callVerb, sub.Child().Socket(), "health", "{}")
	out, err := foreign.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "refused") {
		t.Fatalf("foreign peer health = %v\n%s", err, out)
	}
	sub.Drop()
	after := sub.Observe(ctx)
	if after.Connected || after.Liveness != taskstate.Unobserved {
		t.Fatalf("after the drop: %+v", after)
	}
	if _, err := os.ReadFile(filepath.Join(sub.Copy(), "secret.txt")); err != nil {
		t.Fatalf("the copy was removed when the connection dropped: %v", err)
	}
	if _, err := os.ReadFile(filepath.Join(sub.Copy(), "kept.txt")); err != nil {
		t.Fatalf("the marker was removed: %v", err)
	}
	for _, path := range []string{parent.home.ServiceLog(), parent.childHome.ServiceLog()} {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "s3cret-token") {
			t.Fatalf("the service log %s contains a secret", path)
		}
	}
}

// delegationFixture is a parent service and the paths the subordinate tests share.
type delegationFixture struct {
	service   *service.Service
	served    chan error
	home      *home.Home
	childHome *home.Home
	branch    string
	own       string
}

func startSubordinate(t *testing.T, recovery store.DelegationRecovery) (*delegationFixture, *service.Subordinate) {
	t.Helper()
	if os.Getuid() < 0 {
		t.Skip("this platform reports no user identity")
	}
	parentHome := newHome(t)
	childHome := newHome(t)
	writeMergeConfig(t, parentHome)
	writeMergeConfig(t, childHome)
	running, err := service.Open(t.Context(), options(t, parentHome))
	if err != nil {
		requiresLocalSocket(t, err)
		t.Fatalf("opening the parent: %v", err)
	}
	served := make(chan error, 1)
	go func() { served <- running.Serve(context.Background()) }()
	fix := &delegationFixture{
		service: running, served: served, home: parentHome, childHome: childHome,
		branch: gitRepo(t), own: gitRepo(t),
	}
	id, err := home.NewTaskID()
	if err != nil {
		t.Fatal(err)
	}
	sub, err := running.StartSubordinate(t.Context(), service.SubordinateSpec{
		Home: childHome, Tasks: []string{id}, ParentBranches: []string{fix.branch}, Recovery: recovery,
	})
	if err != nil {
		closeService(t, running, served)
		t.Fatalf("starting the subordinate: %v", err)
	}
	t.Cleanup(func() {
		if err := sub.Close(); err != nil {
			t.Errorf("closing the subordinate: %v", err)
		}
		if fix.service != nil {
			closeService(t, fix.service, fix.served)
		}
	})
	return fix, sub
}

func closeService(t *testing.T, running *service.Service, served chan error) {
	t.Helper()
	if err := running.Close(); err != nil {
		t.Errorf("closing the service: %v", err)
	}
	if err := <-served; err != nil {
		t.Errorf("serving: %v", err)
	}
}

func writeMergeConfig(t *testing.T, h *home.Home) {
	t.Helper()
	if err := h.Create(); err != nil {
		t.Fatal(err)
	}
	body := "autonomy:\n  merge:\n    - owner/name\n"
	if err := os.WriteFile(h.ConfigFile(), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func gitRepo(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "g")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	git(t, dir, "init", "--quiet", "-b", "main", ".")
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "--quiet", "-m", "first")
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}
