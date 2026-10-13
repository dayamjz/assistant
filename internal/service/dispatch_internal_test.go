package service

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
)

// dispatchSubject is a hand-built service over a home whose store records a
// primary checkout, its gate, and one delivery task resolving Dispatched,
// with the instructions file intake writes. It is built by hand, on the
// terms stranded_internal_test.go builds one, because what these tests reach
// is the dispatch below the in-memory slot, which no protocol method drives.
type dispatchSubject struct {
	service *Service
	records *store.Store
	task    string
	copy    string
}

func newDispatchSubject(t *testing.T) *dispatchSubject {
	t.Helper()
	ctx := t.Context()
	h, err := home.Open(t.TempDir())
	if err != nil {
		t.Fatalf("opening the home: %v", err)
	}
	if err := h.Create(); err != nil {
		t.Fatalf("creating the home: %v", err)
	}
	records, err := store.Open(ctx, h.Database(), store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	t.Cleanup(func() { _ = records.Close() })
	primary, upstream, _ := heldSubject(t)
	for _, dir := range []string{primary, upstream} {
		heldGit(t, dir, "config", "gc.auto", "0")
		heldGit(t, dir, "config", "maintenance.auto", "false")
	}
	cfg := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(cfg, []byte("[gc]\n\tauto = 0\n[maintenance]\n\tauto = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", cfg)
	repository, err := records.UpsertRepository(ctx, store.Repository{
		ID: "subject", WorkingPath: primary, UpstreamURL: upstream, DefaultBranch: "main",
	})
	if err != nil {
		t.Fatalf("recording the repository: %v", err)
	}
	command, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gate.Initialize(ctx, gate.Spec{Home: h.Root(), WorkingPath: primary, Command: command},
		gate.WithIndex(records)); err != nil {
		t.Fatalf("initializing the gate: %v", err)
	}
	id, err := home.NewTaskID()
	if err != nil {
		t.Fatal(err)
	}
	copyPath, err := h.TaskCopy(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := records.CreateTask(ctx, store.Task{
		ID: id, Shape: store.TaskDelivery, Project: "one", Mode: "interactive", WorktreePath: copyPath,
		RepositoryID: store.Known(repository.ID), Branch: store.Known("task/one"),
	}, store.Disposition{}); err != nil {
		t.Fatalf("recording the task: %v", err)
	}
	instructions, err := h.TaskInstructions(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(instructions), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(instructions, []byte("work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	log, err := h.OpenLog(0)
	if err != nil {
		t.Fatalf("opening the log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	s := &Service{home: h, store: records, log: log}
	return &dispatchSubject{service: s, records: records, task: id, copy: copyPath}
}

// Two dispatchers of one Dispatched task, below the in-memory slot, both
// read no attempt and both claim the first. The store's anchor decides it:
// one begins attempt 1 and cuts the clone, the other is refused with
// ErrTaskMoved before it reads the primary or touches the path, and the
// records show one attempt, one primary snapshot and one copy. Both are held
// between their read and their claim, so the test is of the anchor and not
// of which goroutine happened to run first. Run under the race detector,
// which make test does.
func TestConcurrentDispatchOfOneTaskClaimsOneAttempt(t *testing.T) {
	principles.Cite(t, principles.P6)
	subject := newDispatchSubject(t)
	ctx := t.Context()
	var arrived sync.WaitGroup
	arrived.Add(2)
	subject.service.betweenAttemptReadAndClaim = func() {
		arrived.Done()
		arrived.Wait()
	}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = subject.service.dispatchClaimed(ctx, subject.task)
		}()
	}
	wg.Wait()
	won, lost := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			won++
		case errors.Is(err, store.ErrTaskMoved):
			lost++
		default:
			t.Fatalf("a dispatcher failed for a reason other than the claim: %v", err)
		}
	}
	if won != 1 || lost != 1 {
		t.Fatalf("%d dispatchers won and %d lost the claim, want one of each: %v", won, lost, errs)
	}
	attempts, err := subject.records.TaskAttempts(ctx, subject.task)
	if err != nil || len(attempts) != 1 {
		t.Fatalf("the task has attempts %+v (%v), want exactly one", attempts, err)
	}
	snapshots, err := subject.records.PrimarySnapshots(ctx, subject.task)
	if err != nil || len(snapshots) != 1 {
		t.Fatalf("the task has primary snapshots %+v (%v), want exactly one", snapshots, err)
	}
	if _, err := os.Stat(filepath.Join(subject.copy, ".git")); err != nil {
		t.Fatalf("the winner's clone is not standing: %v", err)
	}
}

// A dispatch that reaches proven ends with the proof in the service's slot
// for the task and nowhere else, and nothing launches: the slot is what #91's
// launch seam will take. A second dispatch of the same task through the slot
// is refused there, so one service begins one attempt per task it proved,
// and the slot is this service's memory: a service that holds none for the
// task reads it as not dispatched here.
func TestADispatchEndsWithTheProofInTheServicesSlot(t *testing.T) {
	subject := newDispatchSubject(t)
	ctx := t.Context()
	if _, held := subject.service.isolationOf(subject.task); held {
		t.Fatal("the service holds a slot for a task nothing dispatched")
	}
	proof, err := subject.service.dispatchTask(ctx, subject.task)
	if err != nil {
		t.Fatalf("dispatching: %v", err)
	}
	if !proof.Proven() {
		t.Fatal("the dispatch returned a value AssertIsolated did not produce")
	}
	held, ok := subject.service.isolationOf(subject.task)
	if !ok || !held.Proven() || held.Copy != proof.Copy || held.Branch != "task/one" {
		t.Fatalf("the slot holds %+v, %v; want the proof the dispatch returned", held, ok)
	}
	if _, err := subject.service.dispatchTask(ctx, subject.task); err == nil {
		t.Fatal("a second dispatch of a task this service proved began another attempt")
	}
	attempts, err := subject.records.TaskAttempts(ctx, subject.task)
	if err != nil || len(attempts) != 1 || attempts[0].SessionRef.IsKnown() {
		t.Fatalf("the task has attempts %+v (%v), want one with no session, because nothing launched", attempts, err)
	}
	// The proof is in memory and not in any record: the task row carries
	// the copy's snapshot and the attempt, and neither says proven.
	task, err := subject.records.Task(ctx, subject.task)
	if err != nil {
		t.Fatal(err)
	}
	if !task.StartSnapshot.IsKnown() {
		t.Fatal("the proven dispatch recorded no start snapshot")
	}
}
