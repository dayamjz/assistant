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
// what was asked and what the real sweep answered it.
type sweepsAsked struct {
	mu      sync.Mutex
	dirs    []string
	answers []sweepAnswer
	sweeper *agents.Sweeper
}

// sweepAnswer is what the real sweep answered one ask.
type sweepAnswer struct {
	r   agents.SweepResult
	err error
}

func (a *sweepsAsked) sweep(ctx context.Context, dir string) (agents.SweepResult, error) {
	r, err := a.sweeper.Sweep(ctx, dir)
	a.mu.Lock()
	a.dirs = append(a.dirs, dir)
	a.answers = append(a.answers, sweepAnswer{r: r, err: err})
	a.mu.Unlock()
	return r, err
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

// last is what the real sweep answered the latest ask of dir, and fails the
// test when none was asked or that sweep returned an error.
func (a *sweepsAsked) last(t *testing.T, dir string) agents.SweepResult {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	for i := len(a.dirs) - 1; i >= 0; i-- {
		if a.dirs[i] == dir {
			if err := a.answers[i].err; err != nil {
				t.Fatalf("the sweep the service asked of %s failed: %v", dir, err)
			}
			return a.answers[i].r
		}
	}
	t.Fatalf("no sweep was asked of %s; asked %v", dir, a.dirs)
	return agents.SweepResult{}
}

// ending is what the real sweep answered the ask of dir that ended id, and
// fails the test when no sweep the service asked of dir ended it.
func (a *sweepsAsked) ending(t *testing.T, dir string, id agents.Identity) agents.SweepResult {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	for i, d := range a.dirs {
		if d == dir && a.answers[i].err == nil && slices.Contains(a.answers[i].r.Ended, id) {
			return a.answers[i].r
		}
	}
	t.Fatalf("no sweep the service asked of %s ended %s", dir, id)
	return agents.SweepResult{}
}

// unendedBefore is placed less what was ended by the sweeps of dir asked
// before the latest one, which is what the latest could still find there:
// a move can be swept twice, on runsweep.go's terms.
func (a *sweepsAsked) unendedBefore(dir string, placed []agents.Identity) []agents.Identity {
	a.mu.Lock()
	defer a.mu.Unlock()
	latest := -1
	for i, d := range a.dirs {
		if d == dir {
			latest = i
		}
	}
	return slices.DeleteFunc(slices.Clone(placed), func(id agents.Identity) bool {
		for i := range latest {
			if a.dirs[i] == dir && slices.Contains(a.answers[i].r.Ended, id) {
				return true
			}
		}
		return false
	})
}

// sweepAttempts is how many sweeps of one copy in a row untilClear lets be
// refused by processes the test did not place there before it reports the
// host instead.
const sweepAttempts = 5

// untilClear reads the sweep the service last asked of dir, the first, and
// returns it with the first sweep from there on that cleared, which must
// have ended exactly what was still placed in dir when it ran. each is
// called with every sweep read, first included, so the caller can hold
// what the service recorded of it. A sweep that did not clear is held to
// refusedByOthers, with placed, the processes the test left in dir, less
// any an earlier sweep of dir ended, for the first sweep, and with nothing
// for every later one, since the first ended them. A refusal that rule rejects is the copy's and fails the test at
// once. One it accepts is P6's refusal working on a process the test did
// not place in dir (#449 saw tmux's setgid utempter helper, started by
// another test, be one), which is logged, and again is called, which must
// have the service ask exactly one more sweep of dir. After sweepAttempts
// accepted refusals in a row the failure goes through
// fatalUnlessSweepsStoppedClearing, which skips, or fails under
// standin.RequireSweepClearVar, when an empty copy elsewhere is refused too
// and fails otherwise: a host that refuses every sweep is reported as the
// host's, and a run of short-lived blockers longer than sweepAttempts
// fails loudly rather than passing unchecked.
//
// What it cannot tell: a process whose working directory the sweep could
// not read is one nothing can place outside dir, so accepting a refusal
// rests on dir holding nothing but what the test placed there, all of it
// ended, which refusedByOthers checks of the sweep's own listing, and on
// the copy being one no process of the service's own was started in, which
// the caller's setup arranges. Where the blocking process stood is read by
// nothing here.
func (a *sweepsAsked) untilClear(t *testing.T, unestablished, dir string, placed []agents.Identity,
	each func(agents.SweepResult), again func()) (first, cleared agents.SweepResult) {
	t.Helper()
	first = a.last(t, dir)
	placed = a.unendedBefore(dir, placed)
	r := first
	for attempt := 1; ; attempt++ {
		each(r)
		if r.Clear() {
			if err := endedExactly(r, placed); err != nil {
				t.Fatalf("the sweep the service asked of %s cleared, and %v (%s)", dir, err, r)
			}
			return first, r
		}
		if err := refusedByOthers(r, dir, placed); err != nil {
			t.Fatalf("the sweep the service asked of %s did not clear, and the refusal is the copy's: %v (%s)", dir, err, r)
		}
		if attempt == sweepAttempts {
			fatalUnlessSweepsStoppedClearing(t, unestablished,
				"%d sweeps of %s in a row were refused by processes the test did not place there, the last %s", attempt, dir, r)
		}
		t.Logf("the sweep the service asked of %s was refused by processes the test did not place there, as P6 requires (%s); asking again", dir, r)
		placed = nil
		before := a.of(dir)
		again()
		if n := a.of(dir); n != before+1 {
			t.Fatalf("asking again asked %d sweep(s) of %s, want one", n-before, dir)
		}
		r = a.last(t, dir)
	}
}

// The ways refusedByOthers rejects a sweep, one per clause, so its positive
// control can show each clause is reached.
var (
	errSweepCleared    = errors.New("the sweep cleared, so there is no refusal to attribute")
	errSweepOtherDir   = errors.New("the sweep is not of the copy")
	errSweepSurvivor   = errors.New("a process under the copy survived the sweep")
	errSweepUnverified = errors.New("the sweep left processes undecided")
	errSweepFoundOther = errors.New("the sweep found processes under the copy the test did not place there")
	errSweepPlacedHeld = errors.New("a process the test placed in the copy is among those that blocked the sweep")
	errSweepPlacedLeft = errors.New("the processes the sweep ended are not exactly the ones the test placed in the copy")
)

// refusedByOthers is nil when r, a sweep of dir, did not clear only because
// of processes whose working directory it could not read, none of which is
// one of placed, the processes the test left in dir, and when everything
// the sweep found under dir is exactly placed, all of it ended. Otherwise
// it wraps the error of the first clause r breaks. It reads nothing but r
// and decides nothing from a blocker's name: a blocker is matched against
// placed by identity alone.
func refusedByOthers(r agents.SweepResult, dir string, placed []agents.Identity) error {
	switch {
	case r.Clear():
		return errSweepCleared
	case r.Dir != dir:
		return fmt.Errorf("%w: it is of %q, not %s", errSweepOtherDir, r.Dir, dir)
	case len(r.Survived) != 0:
		return fmt.Errorf("%w: %v", errSweepSurvivor, r.Survived)
	case len(r.Unverified) != 0:
		return fmt.Errorf("%w: %v", errSweepUnverified, r.Unverified)
	case len(r.Exited)+len(r.Replaced) != 0:
		return fmt.Errorf("%w: exited %v, replaced %v", errSweepFoundOther, r.Exited, r.Replaced)
	}
	for _, b := range r.UnreadableBlocking {
		if slices.Contains(placed, b.Identity) {
			return fmt.Errorf("%w: %s", errSweepPlacedHeld, b)
		}
	}
	return endedExactly(r, placed)
}

// endedExactly is nil when the processes r ended are exactly placed, and
// otherwise wraps errSweepPlacedLeft naming both.
func endedExactly(r agents.SweepResult, placed []agents.Identity) error {
	order := func(x, y agents.Identity) int { return x.PID - y.PID }
	ended, want := slices.Clone(r.Ended), slices.Clone(placed)
	slices.SortFunc(ended, order)
	slices.SortFunc(want, order)
	if !slices.Equal(ended, want) {
		return fmt.Errorf("%w: ended %v, placed %v", errSweepPlacedLeft, ended, want)
	}
	return nil
}

// refusalRuleHolds is refusedByOthers' positive control, over a real sweep
// r of dir in which the service ended placed, one process. The base is r
// with its blockers removed, which is a sweep that cleared, and each
// counterfeit is that base with one change a real sweep can make: so the
// rule meets shapes of the real listing, starting from the real one, and
// not values written from nothing. foreign and other are two distinct
// processes the test started outside every copy, standing for processes
// the test did not place in dir: foreign is the one that blocks, and other
// the one a counterfeit has the sweep find under dir. A real listing
// classifies each process once, readable or refused, so no identity is in
// two of a result's sets, and every counterfeit is held to that by
// oneSetEach before the rule meets it. The rule must accept the base with
// foreign blocking, and reject the base itself and every counterfeit, each
// for the clause it is built to reach.
func refusalRuleHolds(t *testing.T, r agents.SweepResult, dir string, placed []agents.Identity, foreign, other agents.Identity) {
	t.Helper()
	if len(placed) != 1 {
		t.Fatalf("the control needs one placed process, got %v", placed)
	}
	if foreign.PID == other.PID || slices.ContainsFunc(placed, func(id agents.Identity) bool { return id.PID == foreign.PID || id.PID == other.PID }) {
		t.Fatalf("the control needs distinct processes, got placed %v, foreign %s, other %s", placed, foreign, other)
	}
	base := r
	base.UnreadableBlocking = nil
	if !base.Clear() {
		t.Fatalf("the sweep %s with its blockers removed does not clear, so the control has no base", r)
	}
	if err := refusedByOthers(base, dir, placed); !errors.Is(err, errSweepCleared) {
		t.Fatalf("a sweep that cleared is read as %v, want %v", err, errSweepCleared)
	}
	blocked := func(change func(*agents.SweepResult)) agents.SweepResult {
		m := base
		m.Ended = slices.Clone(base.Ended)
		m.UnreadableBlocking = []agents.Blocker{{Identity: foreign}}
		change(&m)
		if err := oneSetEach(m); err != nil {
			t.Fatalf("a counterfeit is a shape the real sweep cannot return: %v (%s)", err, m)
		}
		return m
	}
	if err := refusedByOthers(blocked(func(*agents.SweepResult) {}), dir, placed); err != nil {
		t.Fatalf("a sweep blocked only by a process outside the copy is read as the copy's: %v", err)
	}
	for _, c := range []struct {
		name   string
		change func(*agents.SweepResult)
		want   error
	}{
		{"of another copy", func(m *agents.SweepResult) { m.Dir += "-other" }, errSweepOtherDir},
		{"the placed process survived", func(m *agents.SweepResult) { m.Ended, m.Survived = nil, placed }, errSweepSurvivor},
		{"the placed process undecided", func(m *agents.SweepResult) { m.Ended, m.Unverified = nil, []int{placed[0].PID} }, errSweepUnverified},
		{"another process under the copy", func(m *agents.SweepResult) { m.Exited = []agents.Identity{other} }, errSweepFoundOther},
		{"the placed process blocked", func(m *agents.SweepResult) {
			m.Ended, m.UnreadableBlocking = nil, []agents.Blocker{{Identity: placed[0]}}
		}, errSweepPlacedHeld},
		{"the placed process not ended", func(m *agents.SweepResult) { m.Ended = nil }, errSweepPlacedLeft},
		{"another process ended", func(m *agents.SweepResult) { m.Ended = append(m.Ended, other) }, errSweepPlacedLeft},
	} {
		if err := refusedByOthers(blocked(c.change), dir, placed); !errors.Is(err, c.want) {
			t.Errorf("the counterfeit %q is read as %v, want %v", c.name, err, c.want)
		}
	}
}

// oneSetEach is nil when no process identifier is in two of r's sets of
// processes, which is how both platform listings return a result: each
// process is classified once, through either its readable working
// directory or its refused one, and a listed process then lands in exactly
// one of Ended, Exited, Replaced, Survived and Unverified.
func oneSetEach(r agents.SweepResult) error {
	seen := map[int]string{}
	note := func(set string, pid int) error {
		if was, ok := seen[pid]; ok {
			return fmt.Errorf("process %d is in both %s and %s", pid, was, set)
		}
		seen[pid] = set
		return nil
	}
	for set, ids := range map[string][]agents.Identity{"Ended": r.Ended, "Exited": r.Exited, "Replaced": r.Replaced, "Survived": r.Survived} {
		for _, id := range ids {
			if err := note(set, id.PID); err != nil {
				return err
			}
		}
	}
	for _, pid := range r.Unverified {
		if err := note("Unverified", pid); err != nil {
			return err
		}
	}
	for _, b := range r.UnreadableBlocking {
		if err := note("UnreadableBlocking", b.PID); err != nil {
			return err
		}
	}
	return nil
}

// recordsSweep fails the test unless got is what sweepFinished records, on
// occasion, of a sweep that answered r without an error: cleared with what
// it ended when r cleared, and otherwise not cleared, with what it ended
// and what it left counted.
func recordsSweep(t *testing.T, got store.RunCopySweep, r agents.SweepResult, occasion store.SweepOccasion) {
	t.Helper()
	outcome, ended, remaining := store.SweepCleared, store.Known(len(r.Ended)), store.Known(0)
	if !r.Clear() {
		outcome, remaining = store.SweepNotCleared, store.Known(len(r.Survived)+len(r.Unverified)+len(r.UnreadableBlocking))
	}
	if got.Outcome != outcome || got.Occasion != occasion || got.Ended != ended || got.Remaining != remaining {
		t.Fatalf("the sweep is recorded as %+v, want %s %s with %v ended and %v remaining, for the sweep %s",
			got, outcome, occasion, ended, remaining, r)
	}
}

// leftProcess is a process leaveRunning started: its identity, and a
// channel closed once it has exited.
type leftProcess struct {
	id     agents.Identity
	exited <-chan struct{}
}

// leaveRunning starts a process in dir in a session of its own, so neither
// an invocation's process group nor this test's reaches it: the detached
// descendant PRD section 8 says group termination does not end.
func leaveRunning(t *testing.T, dir string) leftProcess {
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
	id, found, err := agents.Identify(left.Process.Pid)
	if err != nil || !found {
		t.Fatalf("reading the identity of the process left in %s: found %v, %v", dir, found, err)
	}
	return leftProcess{id: id, exited: exited}
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
// or a process that could never have shown one. Each sweep's record is held
// to what the real sweep answered, and a sweep refused only by processes
// the test did not place in the copy is asked again on untilClear's terms,
// because whether some other process's working directory is readable is
// the host's and not the copy's (#449); refusalRuleHolds is that rule's
// positive control. Skips where the existing sweep tests skip, with the
// same reasons.
func TestARunsCopyIsSweptOnceTheRunIsFinalAndNeverBefore(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P6)
	const unestablished = "the sweep of a finished run's copy"
	requireSweepClearsHere(t, unestablished)

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
			left := leaveRunning(t, copyPath)
			exited := left.exited
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
			// What each sweep came to is recorded on the run, as the move's
			// own sweep, and the run's status answer carries the latest. A
			// sweep refused by a process this test did not place in the
			// copy is P6 working, and the move's sweep is asked again
			// until one clears, on untilClear's terms.
			recorded := func(r agents.SweepResult) {
				sweep, err := s.store.RunCopySweep(ctx, record.ID)
				if err != nil {
					t.Fatalf("the sweep of the run's copy was not recorded: %v", err)
				}
				recordsSweep(t, sweep, r, store.SweptAtFinish)
			}
			asked.untilClear(t, unestablished, copyPath, []agents.Identity{left.id}, recorded,
				func() { s.sweepFinished(ctx, final, store.SweptAtFinish) })
			refusalRuleHolds(t, asked.ending(t, copyPath, left.id), copyPath, []agents.Identity{left.id}, leaveRunning(t, t.TempDir()).id, leaveRunning(t, t.TempDir()).id)
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
// its copy at recovery and what it ended, which its status answer carries,
// in the structured rendering as well, while the held run has no sweep
// asked or recorded and its answer carries none. The process ending under
// the same recovery that leaves the other alone is the positive control.
// Each recovery's record is held to what the real sweep answered it. A
// recovery whose sweep was refused only by processes the test did not
// place in the copy, which #449 saw tmux's setgid utempter helper do, is
// P6's refusal working: the service is opened again, on untilClear's
// terms, until a recovery's sweep clears, and that one ended nothing,
// since the first ended the process. refusalRuleHolds is the positive
// control for the rule that accepts such a refusal.
func TestRecoverySweepsAFinishedRunsStandingCopyAndNeverAnActiveRunsCopy(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P6)
	const unestablished = "the sweep at recovery of a finished run's copy"
	requireSweepClearsHere(t, unestablished)

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
	stillRunning(t, finishedLeft.exited, "no service had asked a sweep of the finished run's copy")

	if err := s.Close(); err != nil {
		t.Fatalf("closing the first service: %v", err)
	}
	build, err := store.CurrentBuild()
	if err != nil {
		t.Fatal(err)
	}
	runner := standin.New(t, standin.Script{}).Runner()
	asked := &sweepsAsked{sweeper: agents.NewSweeper(s.home, reapGrace, nil)}
	var reopened *Service
	reopen := func() {
		if reopened != nil {
			if err := reopened.Close(); err != nil {
				t.Fatalf("closing the reopened service: %v", err)
			}
		}
		next, err := Open(ctx, Options{
			Home:      s.home,
			NewStages: stages.All,
			NewFixer:  stages.Fix,
			Build:     build,
			Catalog:   agents.NewCatalog(fixedRunner{runner: runner}, cursorFactory{runner: runner}),
			LockWait:  -1,
			sweepCopy: asked.sweep,
		})
		if err != nil {
			t.Fatalf("reopening the service on the same home: %v", err)
		}
		reopened = next
	}
	reopen()
	t.Cleanup(func() { _ = reopened.Close() })

	ended(t, finishedLeft.exited, "recovery found the finished run's copy standing")
	stillRunning(t, activeLeft.exited, "recovery ran with the run held")
	// A sweep refused by a process this test did not place in the copy is
	// P6 working, and the service is opened again, whose recovery sweeps
	// the standing copy again, until one clears, on untilClear's terms.
	var sweep store.RunCopySweep
	_, cleared := asked.untilClear(t, unestablished, finishedCopy, []agents.Identity{finishedLeft.id},
		func(r agents.SweepResult) {
			var err error
			if sweep, err = reopened.store.RunCopySweep(ctx, finished.ID); err != nil {
				t.Fatalf("the finished run's sweep was not recorded: %v", err)
			}
			recordsSweep(t, sweep, r, store.SweptAtRecovery)
			if n := asked.of(activeCopy); n != 0 {
				t.Fatalf("recovery asked %d sweep(s) of the held run's copy", n)
			}
			stillRunning(t, activeLeft.exited, "recovery ran with the run held")
		}, reopen)
	if !cleared.Clear() {
		t.Fatalf("untilClear returned a sweep that did not clear: %s", cleared)
	}
	refusalRuleHolds(t, asked.ending(t, finishedCopy, finishedLeft.id), finishedCopy, []agents.Identity{finishedLeft.id}, leaveRunning(t, t.TempDir()).id, leaveRunning(t, t.TempDir()).id)
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
