//go:build unix

package agents_test

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/home"
	"github.com/dayamjz/assistant/internal/principles"
)

// sweepGrace is the grace the tests give the polite signal. The stubborn
// processes ignore it, so the forceful step is what ends them.
const sweepGrace = 300 * time.Millisecond

// sweepEnded bounds how long a test waits for the descriptor a swept process
// held to close. Sweep returns after it has seen the process gone, so this
// is slack for the close to be observed, not for the signal to land.
const sweepEnded = 10 * time.Second

// requiresSweep skips a test on a platform with no sweep, saying what that
// leaves unestablished: nothing about which processes a sweep ends is
// checked there. TestAnUnsupportedSweepCannotReadAsEmpty runs everywhere and
// holds the unsupported answer itself.
func requiresSweep(t *testing.T) {
	t.Helper()
	requiresIdentityRead(t)
	h, _ := sweepHome(t, sweepBase(t), "probe")
	copyDir := makeTaskCopy(t, h)
	if _, err := agents.NewSweeper(h, sweepGrace, nil).Sweep(t.Context(), copyDir); errors.Is(err, agents.ErrSweepUnsupported) {
		t.Skipf("%s has no process working-directory listing, so which processes a sweep ends is not established here: %v", runtime.GOOS, err)
	}
}

// sweepBase is a fresh temporary directory for the test, resolved, so the
// homes a test opens under it share one parent.
func sweepBase(t *testing.T) string {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return base
}

// sweepHome creates the home name under base and returns it with its root.
func sweepHome(t *testing.T, base, name string) (*home.Home, string) {
	t.Helper()
	h, err := home.Open(filepath.Join(base, name))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Create(); err != nil {
		t.Fatal(err)
	}
	return h, h.Root()
}

// makeTaskCopy creates a task copy directory under h for a fresh identifier.
func makeTaskCopy(t *testing.T, h *home.Home) string {
	t.Helper()
	id, err := home.NewTaskID()
	if err != nil {
		t.Fatal(err)
	}
	return makeCopyOf(t, h, id)
}

// makeCopyOf creates the task copy directory of id under h.
func makeCopyOf(t *testing.T, h *home.Home, id string) string {
	t.Helper()
	dir, err := h.TaskCopy(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

// detached is one process the "detach" mode left running: its identity, and
// a channel closed when the descriptor it holds is closed, which is when it
// has exited.
type detached struct {
	id    agents.Identity
	ended <-chan struct{}
}

// startDetached runs the "detach" mode with dir as the child's working
// directory and returns the child it left behind: in a session of its own,
// its parent exited, so neither a process group nor a parent reaches it.
// The cleanup ends it by verified identity if a sweep did not.
func startDetached(t *testing.T, dir string, stubborn bool) detached {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(helperBinary(t))
	cmd.Env = []string{helperModeVar + "=detach", helperDirVar + "=" + dir}
	if stubborn {
		cmd.Env = append(cmd.Env, helperStderrVar+"=stubborn")
	}
	cmd.Dir = os.TempDir()
	cmd.ExtraFiles = []*os.File{w}
	out, err := cmd.Output()
	_ = w.Close()
	if err != nil {
		_ = r.Close()
		t.Fatalf("running the detaching stand-in: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		_ = r.Close()
		t.Fatalf("the detaching stand-in printed %q, want an identifier", out)
	}
	ended := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, r)
		_ = r.Close()
		close(ended)
	}()
	id, found, err := agents.Identify(pid)
	if err != nil || !found {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Fatalf("identifying the detached child %d: %+v, %v, %v", pid, id, found, err)
	}
	t.Cleanup(func() {
		if presence, err := id.Presence(); err == nil && presence == agents.PresencePresent {
			_ = syscall.Kill(id.PID, syscall.SIGKILL)
		}
		select {
		case <-ended:
		case <-time.After(sweepEnded):
			t.Errorf("the detached child %d did not end on cleanup", id.PID)
		}
	})
	return detached{id: id, ended: ended}
}

// waitEnded fails the test unless d's descriptor closes within sweepEnded.
func waitEnded(t *testing.T, d detached, why string) {
	t.Helper()
	select {
	case <-d.ended:
	case <-time.After(sweepEnded):
		t.Fatalf("%s: process %d is still holding its descriptor", why, d.id.PID)
	}
}

// requirePresent fails the test unless d still reads present.
func requirePresent(t *testing.T, d detached, why string) {
	t.Helper()
	if presence, err := d.id.Presence(); err != nil || presence != agents.PresencePresent {
		t.Fatalf("%s: process %d reads %s, %v, want present", why, d.id.PID, presence, err)
	}
}

// mentions reports whether r names id in any of its lists.
func mentions(r agents.SweepResult, id agents.Identity) bool {
	for _, list := range [][]agents.Identity{r.Ended, r.Exited, r.Replaced, r.Survived} {
		if slices.Contains(list, id) {
			return true
		}
	}
	return slices.Contains(r.Unverified, id.PID)
}

// TestASweepEndsADetachedDescendantUnderTheCopyAndNothingElse establishes
// the sweep's reach and its scope. Three processes are started the same way
// with the same command line, each in a session of its own with its parent
// gone, so no group signal and no parent reaches any of them: one whose
// working directory is below a task copy of this home, entered through a
// symbolic link so the two sides of the comparison are resolved alike; one
// in another task copy of the same home; and one in the copy of the same
// task identifier under a different home. Sweeping the first copy ends the
// first, which ignores the polite signal so the forceful step is exercised,
// and leaves the other two running and unmentioned.
//
// The positive control is the listing over the directory the three share:
// it finds all three, so their command line and their detachment are no
// reason they were left alone, and the directory is.
func TestASweepEndsADetachedDescendantUnderTheCopyAndNothingElse(t *testing.T) {
	requiresSweep(t)
	ctx := t.Context()
	base := sweepBase(t)
	h, root := sweepHome(t, base, "home")
	other, _ := sweepHome(t, base, "other")
	id, err := home.NewTaskID()
	if err != nil {
		t.Fatal(err)
	}
	target := makeCopyOf(t, h, id)
	neighbour := makeTaskCopy(t, h)
	elsewhere := makeCopyOf(t, other, id)
	below := filepath.Join(target, "build")
	if err := os.MkdirAll(below, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(filepath.Dir(root), "alias")
	if err := os.Symlink(below, alias); err != nil {
		t.Fatal(err)
	}

	inTarget := startDetached(t, alias, true)
	inNeighbour := startDetached(t, neighbour, false)
	inElsewhere := startDetached(t, elsewhere, false)

	shared := filepath.Dir(root)
	listed, err := agents.ListUnder(ctx, shared)
	if err != nil {
		t.Fatalf("listing under %s: %v", shared, err)
	}
	for _, d := range []detached{inTarget, inNeighbour, inElsewhere} {
		if !slices.Contains(listed, d.id) {
			t.Fatalf("the listing under the shared directory %s does not find %s, so leaving it alone would prove nothing: %v", shared, d.id, listed)
		}
	}

	r, err := agents.NewSweeper(h, sweepGrace, nil).Sweep(ctx, target)
	if err != nil {
		t.Fatalf("sweeping %s: %v", target, err)
	}
	if !r.Clear() || len(r.Ended) != 1 || r.Ended[0] != inTarget.id || r.Dir != target {
		t.Fatalf("the sweep of %s did %s (%+v), want a clear sweep that ended %s", target, r, r, inTarget.id)
	}
	waitEnded(t, inTarget, "the process under the swept copy was reported ended")
	for _, d := range []detached{inNeighbour, inElsewhere} {
		if mentions(r, d.id) {
			t.Fatalf("the sweep of %s mentions %s, which runs in another copy: %+v", target, d.id, r)
		}
		requirePresent(t, d, "a process in another copy after the sweep")
	}

	again, err := agents.NewSweeper(h, sweepGrace, nil).Sweep(ctx, target)
	if err != nil || !again.Clear() || len(again.Ended) != 0 {
		t.Fatalf("a second sweep of %s did %s, %v, want clear with nothing ended", target, again, err)
	}
}

// TestASweepRefusesADirectoryThatIsNotOneOfItsHomesCopies holds the scope at
// the entry: a copy of another home, the home's root, and a task copy path
// that is a symbolic link to a copy of this home are each refused with
// home.ErrNotACopy and the zero result, and the process standing in the
// other home's copy, which a sweep of that directory would end, is left
// running.
func TestASweepRefusesADirectoryThatIsNotOneOfItsHomesCopies(t *testing.T) {
	requiresSweep(t)
	ctx := t.Context()
	base := sweepBase(t)
	h, root := sweepHome(t, base, "home")
	other, _ := sweepHome(t, base, "other")
	elsewhere := makeTaskCopy(t, other)
	inElsewhere := startDetached(t, elsewhere, false)
	real := makeTaskCopy(t, h)
	linkID, err := home.NewTaskID()
	if err != nil {
		t.Fatal(err)
	}
	linked, err := h.TaskCopy(linkID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(linked), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, linked); err != nil {
		t.Fatal(err)
	}
	sweeper := agents.NewSweeper(h, sweepGrace, nil)
	for _, dir := range []string{elsewhere, root, linked} {
		r, err := sweeper.Sweep(ctx, dir)
		if !errors.Is(err, home.ErrNotACopy) || r.Clear() || len(r.Ended) != 0 {
			t.Fatalf("sweeping %s answered %s, %v, want the zero result and home.ErrNotACopy", dir, r, err)
		}
	}
	requirePresent(t, inElsewhere, "the process in another home's copy after a refused sweep")
	// The control: the other home's own sweep of the same directory ends it.
	r, err := agents.NewSweeper(other, sweepGrace, nil).Sweep(ctx, elsewhere)
	if err != nil || len(r.Ended) != 1 || r.Ended[0] != inElsewhere.id {
		t.Fatalf("the other home's sweep of its own copy did %s, %v, want %s ended", r, err, inElsewhere.id)
	}
	waitEnded(t, inElsewhere, "the other home's sweep of its own copy")
}

// TestASweepDoesNotEndAStaleIdentity establishes P6 on the sweep's kill
// path: an identity the listing produced whose identifier now names a
// process with another start time is a reused number, and the ending step
// reports it replaced and signals nothing; an identity with no start time is
// unverified and signals nothing. The counterfeit is a mutation of the real
// reading, since nothing can make the kernel reuse a live identifier on
// demand, and the control is the same ending step given the real identity,
// which ends the process.
func TestASweepDoesNotEndAStaleIdentity(t *testing.T) {
	principles.Cite(t, principles.P6)
	requiresSweep(t)
	ctx := t.Context()
	h, _ := sweepHome(t, sweepBase(t), "home")
	d := startDetached(t, makeTaskCopy(t, h), false)

	stale := agents.Identity{PID: d.id.PID, Start: d.id.Start + 1}
	r := agents.EndListed(ctx, []agents.Identity{stale}, sweepGrace)
	if len(r.Replaced) != 1 || r.Replaced[0] != stale || len(r.Ended) != 0 || len(r.Survived) != 0 {
		t.Fatalf("ending a stale identity did %+v, want it reported replaced and nothing ended", r)
	}
	requirePresent(t, d, "the process behind a stale identity")

	unread := agents.Identity{PID: d.id.PID}
	r = agents.EndListed(ctx, []agents.Identity{unread}, sweepGrace)
	if !slices.Equal(r.Unverified, []int{d.id.PID}) || len(r.Ended) != 0 {
		t.Fatalf("ending an identity with no start time did %+v, want it unverified and nothing ended", r)
	}
	requirePresent(t, d, "the process behind an identity with no start time")

	r = agents.EndListed(ctx, []agents.Identity{d.id}, sweepGrace)
	if len(r.Ended) != 1 || r.Ended[0] != d.id {
		t.Fatalf("ending the real identity did %+v, want it ended", r)
	}
	waitEnded(t, d, "the process behind the real identity")
}
