package service

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/graph"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
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
	principles.Cite(t, principles.P6)
	requireSweepClearsHere(t, "the sweep of a finished run's copy")

	for _, ending := range []string{"completes", "fails", "is cancelled"} {
		t.Run(ending, func(t *testing.T) {
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
		s.sweepFinished(t.Context(), store.Run{ID: "run-" + string(status), RepositoryID: "subject", Status: status})
	}
	if len(dirs) != 0 {
		t.Fatalf("sweeps were asked for rows that are not final: %v", dirs)
	}
	if log := serviceLog(t, h); !strings.Contains(log, "run run-unheard-of is not swept") && !strings.Contains(log, "copy of run run-unheard-of is not swept") {
		t.Fatalf("the refusal of an unrecognized status was not logged:\n%s", log)
	}
	s.sweepFinished(t.Context(), store.Run{ID: "run-done", RepositoryID: "subject", Status: store.RunPassed})
	if want := h.Worktree("subject", "run-done"); len(dirs) != 1 || dirs[0] != want {
		t.Fatalf("a final row asked %v, want one sweep of %s", dirs, want)
	}
	if log := serviceLog(t, h); !strings.Contains(log, "run run-done is passed and this platform has no process sweep") {
		t.Fatalf("an unsupported sweep of a final run's copy was not reported:\n%s", log)
	}
}
