package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/agents/standin"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/stages"
	"github.com/dayamjz/assistant/internal/store"
)

// sweepsAsked records every directory the service asks a sweep of, and
// answers with the real sweep over the service's home, so a test reads both
// what was asked and what the real sweep did about it.
type sweepsAsked struct {
	mu      sync.Mutex
	dirs    []string
	sweeper *agents.Sweeper
}

func (a *sweepsAsked) sweep(ctx context.Context, dir string) (agents.SweepResult, error) {
	a.mu.Lock()
	a.dirs = append(a.dirs, dir)
	a.mu.Unlock()
	return a.sweeper.Sweep(ctx, dir)
}

// of counts the sweeps asked of dir.
func (a *sweepsAsked) of(dir string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for _, d := range a.dirs {
		if d == dir {
			n++
		}
	}
	return n
}

// leaveRunning starts a process in dir in a session of its own, so neither
// an invocation's process group nor this test's reaches it: the detached
// descendant PRD section 8 says group termination does not end. It returns
// a channel closed once the process has exited.
func leaveRunning(t *testing.T, dir string) <-chan struct{} {
	t.Helper()
	left := exec.Command("sleep", "60")
	left.Dir = dir
	detach(left)
	if err := left.Start(); err != nil {
		t.Skipf("no sleep to leave running in the copy, so the sweep of a finished run's copy is not established here: %v", err)
	}
	exited := make(chan struct{})
	go func() { _ = left.Wait(); close(exited) }()
	t.Cleanup(func() {
		_ = left.Process.Kill()
		<-exited
	})
	return exited
}

// stillRunning fails the test when the process behind exited has gone, after
// giving a sweep that was wrongly asked time to have ended it.
func stillRunning(t *testing.T, exited <-chan struct{}, while string) {
	t.Helper()
	select {
	case <-exited:
		t.Fatalf("the process left in the copy was ended while %s", while)
	case <-time.After(200 * time.Millisecond):
	}
}

// ended fails the test unless the process behind exited goes.
func ended(t *testing.T, exited <-chan struct{}, after string) {
	t.Helper()
	select {
	case <-exited:
	case <-time.After(10 * time.Second):
		t.Fatalf("the process left in the copy is still running after %s", after)
	}
}

// PRD section 8's process lifetime rule, P6's side of it: once a run reaches
// a final state its isolated copy is swept, and a copy whose run is still
// active is never swept. A run is begun through the real service, so its
// copy is real and its segment is inside a stage body, and a process is left
// running in the copy in a session of its own, which no process group the
// service signals reaches. While the run is running, held and running again
// no sweep is asked of its copy and the process runs on; once the run
// completes through settle, fails, or is cancelled through the protocol, a
// sweep is asked of exactly that copy and the process is gone. The same
// recorder seeing a sweep on each ending, and the same process ending under
// it, is the positive control that the absence before it is not a recorder
// or a process that could never have shown one. Skips where the existing
// sweep tests skip, with the same reasons.
func TestARunsCopyIsSweptOnceTheRunIsFinalAndNeverBefore(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P6)
	requireSweepClearsHere(t, "the sweep of a finished run's copy")

	for _, ending := range []string{"completes", "fails", "is cancelled"} {
		t.Run(ending, func(t *testing.T) {
			t.Parallel()
			held := newHeldService(t)
			s := held.service
			asked := &sweepsAsked{sweeper: agents.NewSweeper(s.home, reapGrace, nil)}
			s.sweepCopy = asked.sweep
			record := held.begin(t)
			ctx := t.Context()
			copyPath := s.home.Worktree(record.RepositoryID, record.ID)
			if _, err := os.Stat(copyPath); err != nil {
				t.Fatalf("the run's copy is not standing at %s: %v", copyPath, err)
			}
			if _, err := asked.sweeper.Sweep(ctx, copyPath); errors.Is(err, agents.ErrSweepUnsupported) {
				t.Skipf("%s has no sweep, so the sweep of a finished run's copy is not established here: %v", runtime.GOOS, err)
			}
			// The probe above is the test's own and not one the service
			// asked, so the recorder starts empty here.
			exited := leaveRunning(t, copyPath)
			stillRunning(t, exited, "the run was running")
			if n := asked.of(copyPath); n != 0 {
				t.Fatalf("a sweep was asked %d time(s) of the copy of a running run", n)
			}

			built, err := s.driverFor(ctx)
			if err != nil {
				t.Fatal(err)
			}
			switch ending {
			case "is cancelled":
				if _, err := s.cancel(ctx, machine.CancelRequest{Run: record.ID}); err != nil {
					t.Fatalf("cancelling the run: %v", err)
				}
				held.awaitSegment(t)
			default:
				held.let()
				held.awaitSegment(t)
				if got := held.status(t, record.ID); got != store.RunHeld {
					t.Fatalf("the run is %s after its stage asked, want held", got)
				}
				stillRunning(t, exited, "the run was held")
				if _, err := built.runs.Release(ctx, record.ID); err != nil {
					t.Fatalf("releasing the run: %v", err)
				}
				stillRunning(t, exited, "the run was running again")
				if n := asked.of(copyPath); n != 0 {
					t.Fatalf("a sweep was asked %d time(s) of the copy of a run that is held or running", n)
				}
				if ending == "completes" {
					if err := s.settle(ctx, record.ID, graph.Result{Status: graph.StatusCompleted}); err != nil {
						t.Fatalf("settling the run as completed: %v", err)
					}
				} else if _, err := built.runs.Fail(ctx, record.ID); err != nil {
					t.Fatalf("failing the run: %v", err)
				}
			}

			final, err := s.store.Run(ctx, record.ID)
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]store.RunStatus{"completes": store.RunPassed, "fails": store.RunFailed, "is cancelled": store.RunTerminated}[ending]
			if final.Status != want {
				t.Fatalf("the run is %s, want %s", final.Status, want)
			}
			ended(t, exited, "the run "+ending)
			if n := asked.of(copyPath); n == 0 {
				t.Fatalf("no sweep was asked of the copy of a run that %s; asked %v", ending, asked.dirs)
			}
			// What the sweep came to is recorded on the run, as the move's
			// own sweep, and the run's status answer carries it.
			sweep, err := s.store.RunCopySweep(ctx, record.ID)
			if err != nil {
				t.Fatalf("the sweep of the run's copy was not recorded: %v", err)
			}
			if sweep.Outcome != store.SweepCleared || sweep.Occasion != store.SweptAtFinish || sweep.Ended.Or(0) < 1 {
				t.Fatalf("the sweep is recorded as %+v, want cleared at finish with the process ended", sweep)
			}
			if answer, err := s.view(ctx, record.ID); err != nil || answer.CopySweep == nil || answer.CopySweep.Outcome != store.SweepCleared {
				t.Fatalf("the run's status answer carries the sweep %+v (%v)", answer.CopySweep, err)
			}
			log := serviceLog(t, s.home)
			if !strings.Contains(log, "run "+record.ID+" is "+string(want)+" and its isolated copy was swept: ") ||
				!strings.Contains(log, "1 ended") {
				t.Fatalf("the service log does not say the copy was swept and one process ended:\n%s", log)
			}
		})
	}
}

// A row that is not final is refused by the service's own reading as well,
// whoever hands it over: sweepFinished asks no sweep for a run whose status
// is active or one this build does not recognize, and says so in the log.
// The positive control is the same call with a final row, which asks one.
func TestASweepIsNotAskedForARowThatIsNotFinal(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P6)
	s, _, h := cleanupService(t)
	var dirs []string
	s.sweepCopy = func(_ context.Context, dir string) (agents.SweepResult, error) {
		dirs = append(dirs, dir)
		return agents.SweepResult{}, agents.ErrSweepUnsupported
	}
	for _, status := range append(slices.DeleteFunc(store.RunStatuses(), func(st store.RunStatus) bool {
		return st == store.RunPassed || st == store.RunFailed || st == store.RunTerminated
	}), store.RunStatus("unheard-of")) {
		s.sweepFinished(t.Context(), store.Run{ID: "run-" + string(status), RepositoryID: "subject", Status: status}, store.SweptAtFinish)
	}
	if len(dirs) != 0 {
		t.Fatalf("sweeps were asked for rows that are not final: %v", dirs)
	}
	if log := serviceLog(t, h); !strings.Contains(log, "run run-unheard-of is not swept") && !strings.Contains(log, "copy of run run-unheard-of is not swept") {
		t.Fatalf("the refusal of an unrecognized status was not logged:\n%s", log)
	}
	s.sweepFinished(t.Context(), store.Run{ID: "run-done", RepositoryID: "subject", Status: store.RunPassed}, store.SweptAtFinish)
	if want := h.Worktree("subject", "run-done"); len(dirs) != 1 || dirs[0] != want {
		t.Fatalf("a final row asked %v, want one sweep of %s", dirs, want)
	}
	if log := serviceLog(t, h); !strings.Contains(log, "run run-done is passed and this platform has no process sweep") {
		t.Fatalf("an unsupported sweep of a final run's copy was not reported:\n%s", log)
	}
}

// finishedRun records a run of repository "subject" and moves it straight to
// terminated through the store, around internal/runs, so no OnFinish hears
// it: the record a service that stopped between an ending's commit and its
// sweep leaves behind.
func finishedRun(t *testing.T, records *store.Store, id, head string) store.Run {
	t.Helper()
	ctx := t.Context()
	if _, err := records.Repository(ctx, "subject"); errors.Is(err, store.ErrNotFound) {
		if _, err := records.UpsertRepository(ctx, store.Repository{
			ID: "subject", WorkingPath: "/checkouts/subject", UpstreamURL: "https://example.test/subject.git", DefaultBranch: "main",
		}); err != nil {
			t.Fatalf("recording the repository: %v", err)
		}
	}
	build, err := store.CurrentBuild()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := records.CreateRun(ctx, store.Run{
		ID: id, RepositoryID: "subject", Branch: "finished-" + id, SubmittedHead: head, Base: head,
		Intent: "finished before its copy was swept", IntentSource: "push", Build: build, ConfigDigest: "cfg",
	}); err != nil {
		t.Fatalf("recording run %s: %v", id, err)
	}
	finished, err := records.TransitionRun(ctx, id, []store.RunStatus{store.RunPending}, store.RunTerminated)
	if err != nil {
		t.Fatalf("finishing run %s around internal/runs: %v", id, err)
	}
	return finished
}

// PRD section 8's process lifetime rule at recovery, P6's side of it: a run
// that was already finished when the service opened, and whose isolated copy
// still stands, has that copy swept by the opening service's recovery, and
// a run that is still active, here one held on a person's answer, keeps
// every process in its copy. A run is begun and held through the real
// service, so its copy is real, and a second run is recorded finished
// around internal/runs, so no ending swept it, which is the service that
// stopped between the commit and the sweep. A process is left running in
// each copy in a session of its own. The service is closed and a second
// service opened on the same home: the finished run's process is gone, the
// held run's runs on, and the finished run's record says the sweep cleared
// its copy at recovery and ended one process, which its status answer
// carries, in the structured rendering as well, while the held run has no
// sweep recorded and its answer carries none. The process ending under the
// same recovery that leaves the other alone is the positive control.
func TestRecoverySweepsAFinishedRunsStandingCopyAndNeverAnActiveRunsCopy(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P6)
	requireSweepClearsHere(t, "the sweep at recovery of a finished run's copy")

	held := newHeldService(t)
	s := held.service
	ctx := t.Context()
	active := held.begin(t)
	held.let()
	held.awaitSegment(t)
	if got := held.status(t, active.ID); got != store.RunHeld {
		t.Fatalf("the run is %s after its stage asked, want held", got)
	}
	activeCopy := s.home.Worktree(active.RepositoryID, active.ID)
	if _, err := os.Stat(activeCopy); err != nil {
		t.Fatalf("the held run's copy is not standing at %s: %v", activeCopy, err)
	}
	if _, err := agents.NewSweeper(s.home, reapGrace, nil).Sweep(ctx, activeCopy); errors.Is(err, agents.ErrSweepUnsupported) {
		t.Skipf("%s has no sweep, so the sweep at recovery of a finished run's copy is not established here: %v", runtime.GOOS, err)
	}
	finished := finishedRun(t, s.store, "finishedaaaa", held.head)
	finishedCopy := s.home.Worktree(finished.RepositoryID, finished.ID)
	if err := os.MkdirAll(finishedCopy, 0o700); err != nil {
		t.Fatal(err)
	}
	activeLeft := leaveRunning(t, activeCopy)
	finishedLeft := leaveRunning(t, finishedCopy)
	stillRunning(t, finishedLeft, "no service had asked a sweep of the finished run's copy")

	if err := s.Close(); err != nil {
		t.Fatalf("closing the first service: %v", err)
	}
	build, err := store.CurrentBuild()
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, Options{
		Home:      s.home,
		NewStages: stages.All,
		NewFixer:  stages.Fix,
		Build:     build,
		Catalog:   agents.NewCatalog(fixedRunner{runner: standin.New(t, standin.Script{}).Runner()}),
		LockWait:  -1,
	})
	if err != nil {
		t.Fatalf("reopening the service on the same home: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })

	ended(t, finishedLeft, "recovery found the finished run's copy standing")
	stillRunning(t, activeLeft, "recovery ran with the run held")
	sweep, err := reopened.store.RunCopySweep(ctx, finished.ID)
	if err != nil {
		t.Fatalf("the finished run's sweep was not recorded: %v", err)
	}
	if sweep.Outcome != store.SweepCleared || sweep.Occasion != store.SweptAtRecovery ||
		sweep.Ended != store.Known(1) || sweep.Remaining != store.Known(0) {
		t.Fatalf("the finished run's sweep is recorded as %+v, want cleared at recovery with one ended", sweep)
	}
	if _, err := reopened.store.RunCopySweep(ctx, active.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the held run has a sweep recorded (%v), and an active run's copy is never swept", err)
	}

	answer, err := reopened.view(ctx, finished.ID)
	if err != nil {
		t.Fatal(err)
	}
	if answer.CopySweep == nil || *answer.CopySweep != sweep {
		t.Fatalf("the finished run's status answer carries the sweep %+v, want %+v", answer.CopySweep, sweep)
	}
	encoded, err := json.Marshal(answer)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		CopySweep *store.RunCopySweep `json:"copy_sweep"`
	}
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	if document.CopySweep == nil || document.CopySweep.Outcome != store.SweepCleared || document.CopySweep.Occasion != store.SweptAtRecovery {
		t.Fatalf("the structured status answer carries the sweep %+v:\n%s", document.CopySweep, encoded)
	}
	if answer, err := reopened.view(ctx, active.ID); err != nil || answer.CopySweep != nil {
		t.Fatalf("the held run's status answer carries a sweep %+v (%v)", answer.CopySweep, err)
	}
}

// What a sweep of a finished run's copy came to is recorded on the run, one
// outcome per way the sweep can answer, with the path that asked it: no copy
// standing, a path standing as something that is not the copy, a listing
// that failed, and a platform with no sweep. The first two are the real
// sweep's answers over the filesystem as each case leaves it; the last two
// are the errors the real sweep wraps, put in front of the service the way
// the existing sweep tests do. Each record is read back from the store, and
// a later sweep replaces the one before it, so the record is the latest
// sweep. A cleared sweep's record is established by the recovery test and
// the one above it, which sweep a real copy with a process in it; a sweep
// that lists and does not clear is not reached here, because no real sweep
// in a test can be made to leave a process it may signal running.
func TestASweepsOutcomeIsRecordedOnTheRun(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P6)
	s, records, h := cleanupService(t)
	listingFailed := errors.New("the process table could not be read")
	real := agents.NewSweeper(h, reapGrace, nil).Sweep
	for i, c := range []struct {
		name     string
		occasion store.SweepOccasion
		arrange  func(t *testing.T, dir string)
		sweep    func(context.Context, string) (agents.SweepResult, error)
		want     store.SweepOutcome
	}{
		{name: "nothing standing", occasion: store.SweptAtFinish, sweep: real, want: store.SweepNothingStanding},
		{name: "a path that is not the copy", occasion: store.SweptAtRecovery, sweep: real, want: store.SweepFailed,
			arrange: func(t *testing.T, dir string) {
				if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), dir); err != nil {
					t.Skipf("no symbolic link to stand at the copy path: %v", err)
				}
			}},
		{name: "a listing that failed", occasion: store.SweptAtFinish, want: store.SweepFailed,
			sweep: func(_ context.Context, dir string) (agents.SweepResult, error) {
				return agents.SweepResult{}, fmt.Errorf("agents: listing processes under %s: %w", dir, listingFailed)
			}},
		{name: "no sweep on this platform", occasion: store.SweptAtRecovery, sweep: sweepUnsupported, want: store.SweepUnsupported},
	} {
		t.Run(c.name, func(t *testing.T) {
			run := finishedRun(t, records, fmt.Sprintf("outcome%05d", i), "aaaa")
			if c.arrange != nil {
				c.arrange(t, h.Worktree(run.RepositoryID, run.ID))
			}
			s.sweepCopy = c.sweep
			s.sweepFinished(t.Context(), run, c.occasion)
			got, err := records.RunCopySweep(t.Context(), run.ID)
			if err != nil {
				t.Fatalf("no sweep was recorded: %v", err)
			}
			if got.Outcome != c.want || got.Occasion != c.occasion || got.Ended.IsKnown() || got.Remaining.IsKnown() {
				t.Fatalf("the sweep is recorded as %+v, want %s %s with no counts", got, c.want, c.occasion)
			}
			if c.want == store.SweepFailed && got.Detail == "" {
				t.Fatalf("a failed sweep records no account of why: %+v", got)
			}
			// The next sweep of the same run replaces this one.
			s.sweepCopy = sweepUnsupported
			s.sweepFinished(t.Context(), run, store.SweptAtFinish)
			if again, err := records.RunCopySweep(t.Context(), run.ID); err != nil ||
				again.Outcome != store.SweepUnsupported || again.Occasion != store.SweptAtFinish {
				t.Fatalf("a later sweep left the record %+v (%v), want it replaced", again, err)
			}
		})
	}
}
