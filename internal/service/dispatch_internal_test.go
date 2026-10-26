package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/gate"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/redact"
	"github.com/dayamjz/assistant/internal/store"
	"github.com/dayamjz/assistant/internal/taskstate"
	"github.com/dayamjz/assistant/internal/vcs"
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
	return newDispatchSubjectAs(t, "")
}

// newDispatchSubjectAs is newDispatchSubject over a task intake recorded
// with role, so a launch resolves that role rather than the worker role.
func newDispatchSubjectAs(t *testing.T, role string) *dispatchSubject {
	t.Helper()
	return newDispatchSubjectOf(t, func(id, copyPath string) (store.Task, store.Disposition) {
		return store.Task{
			ID: id, Shape: store.TaskDelivery, Project: "one", Mode: "interactive", WorktreePath: copyPath,
			RepositoryID: store.Known("subject"), Branch: store.Known("task/one"), Role: role,
		}, store.Disposition{}
	})
}

// newDispatchSubjectOf is newDispatchSubject over the task record recorded
// returns for the identifier and copy path it is handed, with the
// disposition it returns, in place of the delivery task.
func newDispatchSubjectOf(t *testing.T, recorded func(id, copyPath string) (store.Task, store.Disposition)) *dispatchSubject {
	t.Helper()
	ctx := t.Context()
	h, err := home.Open(t.TempDir())
	if err != nil {
		t.Fatalf("opening the home: %v", err)
	}
	if err := h.Create(); err != nil {
		t.Fatalf("creating the home: %v", err)
	}
	seedDatabase(t, h.Database())
	records, err := store.Open(ctx, h.Database(), store.WithRedactor(redact.New()))
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	t.Cleanup(func() { _ = records.Close() })
	id, copyPath := plantLaunchableOf(t, h, records, recorded)
	log, err := h.OpenLog(0)
	if err != nil {
		t.Fatalf("opening the log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	s := &Service{home: h, store: records, log: log}
	return &dispatchSubject{service: s, records: records, task: id, copy: copyPath}
}

// plantLaunchable records in a home what a dispatch at open needs to cut a
// copy and launch a worker: a primary checkout with an upstream, its
// repository record and gate, and one delivery task resolving Dispatched
// with the instructions file intake writes. It returns the task and its
// copy path. Automatic git maintenance is turned off in the subject here
// and globally by TestMain for the whole process, so no background git
// process holds a repository a test is about to remove.
func plantLaunchable(t *testing.T, h *home.Home, records *store.Store) (id, copyPath string) {
	t.Helper()
	return plantLaunchableAs(t, h, records, "")
}

// plantLaunchableAs is plantLaunchable with the task recorded under role.
func plantLaunchableAs(t *testing.T, h *home.Home, records *store.Store, role string) (id, copyPath string) {
	t.Helper()
	return plantLaunchableOf(t, h, records, func(id, copyPath string) (store.Task, store.Disposition) {
		return store.Task{
			ID: id, Shape: store.TaskDelivery, Project: "one", Mode: "interactive", WorktreePath: copyPath,
			RepositoryID: store.Known("subject"), Branch: store.Known("task/one"), Role: role,
		}, store.Disposition{}
	})
}

// plantLaunchableOf is plantLaunchable with the task record recorded
// returns for the identifier and copy path it is handed, recorded with the
// disposition it returns.
func plantLaunchableOf(t *testing.T, h *home.Home, records *store.Store,
	recorded func(id, copyPath string) (store.Task, store.Disposition)) (id, copyPath string) {
	t.Helper()
	ctx := t.Context()
	primary, upstream, _ := heldSubject(t)
	for _, dir := range []string{primary, upstream} {
		heldGit(t, dir, "config", "gc.auto", "0")
		heldGit(t, dir, "config", "maintenance.auto", "false")
	}
	_, err := records.UpsertRepository(ctx, store.Repository{
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
	id, err = home.NewTaskID()
	if err != nil {
		t.Fatal(err)
	}
	copyPath, err = h.TaskCopy(id)
	if err != nil {
		t.Fatal(err)
	}
	task, disposition := recorded(id, copyPath)
	if _, err := records.CreateTask(ctx, task, disposition); err != nil {
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
	return id, copyPath
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
	t.Parallel()
	principles.Cite(t, principles.P6)
	subject := newDispatchSubject(t)
	ctx := t.Context()
	arrived := newRendezvous(2)
	subject.service.betweenAttemptReadAndClaim = arrived.Arrive

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer arrived.Leave()
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

// Two dispatchers of one task each read the repository's trusted copy
// before the first claim (placeable), and neither's read can be undone by
// the other's (#552). The first to fetch is held between its fetch and its
// resolve until the other has fetched, resolved and read, has removed what
// its own read left in the gate, and has reached its claim; only then does
// the held one resolve what it fetched. While both reads shared one
// reference named for the task, the second's removal deleted the first's
// fetch, and the first was refused as though the trusted copy could not be
// read: P7's refusal, reached for a reason that is not the repository's,
// beside a claim that was never contested. Now the store's anchor alone
// decides between them, and no dispatch reference is left in the gate.
func TestADispatchersTrustedReadIsItsOwn(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P7)
	subject := newDispatchSubject(t)
	ctx := t.Context()
	var fetched atomic.Int32
	reached := make(chan struct{})
	var reachOnce sync.Once
	reach := func() { reachOnce.Do(func() { close(reached) }) }
	subject.service.betweenTrustedFetchAndResolve = func(string) {
		if fetched.Add(1) == 1 {
			<-reached
		}
	}
	subject.service.betweenAttemptReadAndClaim = reach

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer reach()
			_, errs[i] = subject.service.dispatchClaimed(ctx, subject.task)
		}()
	}
	wg.Wait()
	if n := fetched.Load(); n != 2 {
		t.Fatalf("the dispatchers fetched the trusted copy %d times, want once each: the ordering this test holds was never reached", n)
	}
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
	if left := dispatchReadRefs(t, subject); len(left) != 0 {
		t.Fatalf("the dispatches left %v in the gate, want every read's reference removed once read", left)
	}
}

// Two dispatchers of one task both read no attempt, and the one that loses
// the first claim never touches the copy the winner cuts (#562). The first
// to fetch the trusted copy is held there until the other has claimed
// attempt 1, cut the copy and proved it; only then does the held one go on,
// with the winner's copy standing at the path. While a dispatcher swept any
// copy it found standing before its claim, the loser swept the winner's:
// in CI it caught the winner's clone still running there and failed for a
// reason other than the claim, and a sweep that clears ends whatever it
// finds, a winner's worker among it. Now a first claim sweeps nothing, so
// the loser goes straight to the claim and the anchor refuses it. The sweep
// asked is the real one, recorded on the way through, so what the test
// counts is what a dispatch asks of agents.Sweeper.
func TestALosingDispatcherNeverSweepsTheWinnersCopy(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P6)
	subject := newDispatchSubject(t)
	ctx := t.Context()
	var mu sync.Mutex
	var swept []string
	real := agents.NewSweeper(subject.service.home, reapGrace, nil).Sweep
	subject.service.sweepCopy = func(ctx context.Context, dir string) (agents.SweepResult, error) {
		mu.Lock()
		swept = append(swept, dir)
		mu.Unlock()
		return real(ctx, dir)
	}
	var fetched atomic.Int32
	returned := make(chan struct{})
	var returnOnce sync.Once
	subject.service.betweenTrustedFetchAndResolve = func(string) {
		if fetched.Add(1) == 1 {
			<-returned
		}
	}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer returnOnce.Do(func() { close(returned) })
			_, errs[i] = subject.service.dispatchClaimed(ctx, subject.task)
		}()
	}
	wg.Wait()
	if n := fetched.Load(); n != 2 {
		t.Fatalf("the dispatchers fetched the trusted copy %d times, want once each: the ordering this test holds was never reached", n)
	}
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
	if len(swept) != 0 {
		t.Fatalf("a first claim asked for sweeps of %v, want none: the loser swept the copy the winner cut", swept)
	}
	if _, err := os.Stat(filepath.Join(subject.copy, ".git")); err != nil {
		t.Fatalf("the winner's clone is not standing: %v", err)
	}
}

// A copy that stands at the task's path without this dispatch having cut it
// or asked the sweep of it is refused as not isolated, recorded on the
// attempt the dispatch claimed, and left as it stands (P6): at a first
// claim, which sweeps nothing because no attempt has cut a copy, and at a
// retry whose check before its claim found nothing standing. The planted
// copy is cut the way a dispatch cuts one and passes the assertion, so the
// refusal is this guard's and not the assertion's, and the untracked file
// in it stands for work someone left there.
func TestACopyNoDispatchCutOrSweptIsRefused(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P6)
	for _, c := range []struct {
		name  string
		retry bool
	}{{name: "first claim"}, {name: "retry", retry: true}} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			subject := newDispatchSubject(t)
			ctx := t.Context()
			var swept atomic.Int32
			subject.service.sweepCopy = func(context.Context, string) (agents.SweepResult, error) {
				swept.Add(1)
				return agents.SweepResult{}, errors.New("no sweep is asked of a copy this dispatch did not find before its claim")
			}
			want := int64(1)
			if c.retry {
				// An attempt that crashed before it cut anything: the retry's
				// check finds no copy, and one appears before its claim.
				if _, err := subject.records.BeginFirstTaskAttempt(ctx, subject.task, store.OnThisMachine()); err != nil {
					t.Fatal(err)
				}
				want = 2
				subject.service.betweenAttemptReadAndClaim = func() { plantTaskCopy(t, subject) }
			} else {
				plantTaskCopy(t, subject)
			}
			_, err := subject.service.dispatchClaimed(ctx, subject.task)
			var refused *isolationRefused
			if !errors.As(err, &refused) || refused.Attempt != want || refused.Reason != taskstate.RefusedNotIsolated {
				t.Fatalf("the dispatch returned %v, want attempt %d refused %s", err, want, taskstate.RefusedNotIsolated)
			}
			if !strings.Contains(refused.Cause.Error(), "neither cut it nor swept it") {
				t.Fatalf("the refusal's cause is %v, want this guard's and not the assertion's", refused.Cause)
			}
			if n := swept.Load(); n != 0 {
				t.Fatalf("the dispatch asked %d sweeps, want none", n)
			}
			if bytes, err := os.ReadFile(filepath.Join(subject.copy, "kept.txt")); err != nil || string(bytes) != "work\n" {
				t.Fatalf("the standing copy's work is %q (%v), want it as it was left", bytes, err)
			}
		})
	}
}

// plantTaskCopy cuts the subject task's copy the way a dispatch does, with
// no attempt claiming it, and leaves an untracked file in it.
func plantTaskCopy(t *testing.T, subject *dispatchSubject) {
	t.Helper()
	ctx := t.Context()
	record, err := subject.records.Task(ctx, subject.task)
	if err != nil {
		t.Fatal(err)
	}
	e, err := subject.service.expectationOf(ctx, record)
	if err != nil {
		t.Fatal(err)
	}
	base := heldGit(t, e.repository.WorkingPath, "rev-parse", "main")
	if err := subject.service.cutTaskCopy(ctx, record, e.repository.WorkingPath, e.copyPath, e.gatePath, base); err != nil {
		t.Fatalf("planting the copy: %v", err)
	}
	if err := os.WriteFile(filepath.Join(e.copyPath, "kept.txt"), []byte("work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// dispatchReadRefs lists the references under the dispatch reads' namespace
// in the gate repository of the subject's repository.
func dispatchReadRefs(t *testing.T, subject *dispatchSubject) []vcs.Ref {
	t.Helper()
	ctx := t.Context()
	repository, err := subject.records.Repository(ctx, "subject")
	if err != nil {
		t.Fatal(err)
	}
	bare, err := subject.service.gateRepositoryOf(ctx, repository, "the test")
	if err != nil {
		t.Fatal(err)
	}
	refs, err := bare.ListRefs(ctx, "refs/assistant/trusted/dispatch/")
	if err != nil {
		t.Fatalf("listing the gate's dispatch references: %v", err)
	}
	return refs
}

// A dispatch that reaches proven ends with the service's slot for the task
// taken and still unsettled, and the proof in that slot and nowhere else
// once the launch settles it (dispatched.settle), so the slot is held
// against a stop, a relaunch or a retirement from the claim until the
// launch's record is settled; the dispatch itself launches nothing: the
// launch is dispatchDue's next step over what the dispatch returned, and
// launch_internal_test.go drives it. A second dispatch of the same task
// through the slot is refused there, so one service begins one attempt per
// task it proved, and the slot is this service's memory: a service that
// holds none for the task reads it as not dispatched here.
func TestADispatchEndsWithTheProofInTheServicesSlot(t *testing.T) {
	t.Parallel()
	subject := newDispatchSubject(t)
	ctx := t.Context()
	if _, held := subject.service.isolationOf(subject.task); held {
		t.Fatal("the service holds a slot for a task nothing dispatched")
	}
	d, err := subject.service.dispatchTask(ctx, subject.task)
	if err != nil {
		t.Fatalf("dispatching: %v", err)
	}
	if !d.proof.Proven() {
		t.Fatal("the dispatch returned a value AssertIsolated did not produce")
	}
	if d.id != subject.task || d.attempt != 1 || d.anchor == 0 || d.instructions == "" {
		t.Fatalf("the dispatch returned %+v, want the task, attempt 1, the anchor and the instructions file", d)
	}
	if held, ok := subject.service.isolationOf(subject.task); !ok || held.Proven() {
		t.Fatalf("after the dispatch the slot holds %+v, %v; want it taken and not yet settled with the proof", held, ok)
	}
	if _, err := subject.service.claimSteer(subject.task); !errors.Is(err, errSteerInFlight) {
		t.Fatalf("a stop's claim on the slot before the launch settled it = %v, want %v", err, errSteerInFlight)
	}
	if d.settle == nil {
		t.Fatal("the dispatch returned no way for the launch to settle the slot")
	}
	d.settleSlot()
	held, ok := subject.service.isolationOf(subject.task)
	if !ok || !held.Proven() || held.Copy != d.proof.Copy || held.Branch != "task/one" {
		t.Fatalf("the settled slot holds %+v, %v; want the proof the dispatch returned", held, ok)
	}
	if release, err := subject.service.claimSteer(subject.task); err != nil {
		t.Fatalf("a stop's claim on the settled slot = %v, want it taken", err)
	} else {
		release()
	}
	if _, err := subject.service.dispatchTask(ctx, subject.task); err == nil {
		t.Fatal("a second dispatch of a task this service proved began another attempt")
	}
	attempts, err := subject.records.TaskAttempts(ctx, subject.task)
	if err != nil || len(attempts) != 1 || attempts[0].SessionRef.IsKnown() {
		t.Fatalf("the task has attempts %+v (%v), want one with no session, because the dispatch alone launches nothing", attempts, err)
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
