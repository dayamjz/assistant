package agents_test

import (
	"errors"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"syscall"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/principles"
)

// realRefusal is the error a real read of a process's cwd link returned when
// it was refused, found by reading the links /proc lists until one is
// refused. The test skips, saying so, where no link is refused, because a
// refusal it wrote itself would be a shape it stated rather than one the
// reader produced.
func realRefusal(t *testing.T) *os.PathError {
	t.Helper()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		_, err := os.Readlink("/proc/" + entry.Name() + "/cwd")
		var refusal *os.PathError
		if errors.Is(err, os.ErrPermission) && errors.As(err, &refusal) {
			return refusal
		}
	}
	t.Skip("no process's working directory is refused to this test here, so there is no real refusal to carry over and the classification of a refused process is not established through the listing")
	return nil
}

// TestAnUnreadableProcessOfThisUserInTheCopyKeepsTheSweepFromClearing
// establishes P6 on the sweep's answer: a process of this test's own user,
// standing in the copy, whose working directory the listing is refused, is
// a fact the sweep cannot verify, so the sweep refuses to clear rather than
// reporting a count beside a clear answer. The process is real and detached
// in the copy, every read the listing makes of it is real but the one
// refused, and that refusal is carried over from a real refused read. It is
// never signalled. The control is the same sweep with the read allowed,
// which finds the process, ends it and clears.
func TestAnUnreadableProcessOfThisUserInTheCopyKeepsTheSweepFromClearing(t *testing.T) {
	principles.Cite(t, principles.P6)
	requiresSweep(t)
	refusal := realRefusal(t)
	ctx := t.Context()
	h, _ := sweepHome(t, sweepBase(t), "home")
	copyDir := makeTaskCopy(t, h)
	d := startDetached(t, copyDir, false)
	if slices.Contains(agents.Ancestry(), d.id) {
		t.Fatalf("the detached process %s is on this test's parent chain", d.id)
	}

	sweeper := agents.NewSweeper(h, sweepGrace, nil)
	r, err := agents.SweepRefusing(ctx, sweeper, copyDir, func(pid int) bool { return pid == d.id.PID }, refusal)
	if err != nil {
		t.Fatalf("sweeping %s: %v", copyDir, err)
	}
	if r.Clear() || !slices.Contains(r.UnreadableBlocking, d.id) || r.Unreadable < 1 || mentions(r, d.id) {
		t.Fatalf("the sweep refused %s's working directory and did %s (%+v), want it not clear with that process blocking and never signalled", d.id, r, r)
	}
	requirePresent(t, d, "a process whose working directory the sweep was refused")

	control, err := sweeper.Sweep(ctx, copyDir)
	if err != nil || !control.Clear() || !slices.Contains(control.Ended, d.id) {
		t.Fatalf("the same sweep with the read allowed did %s, %v, want it clear with %s ended", control, err, d.id)
	}
	waitEnded(t, d, "the control sweep")
}

// TestAnUnreadableAncestorDoesNotKeepTheSweepFromClearing holds the first
// carve-out: a refused process on this process's parent chain is counted
// and does not keep an otherwise empty sweep from clearing. The refused
// process is this test's real parent, which the parent chain finds by
// identity. Where the environment itself refuses a link, as a container's
// init can be refused to a service running as its user, that refusal is
// read for real alongside.
func TestAnUnreadableAncestorDoesNotKeepTheSweepFromClearing(t *testing.T) {
	requiresSweep(t)
	refusal := realRefusal(t)
	parent, present, err := agents.Identify(os.Getppid())
	if err != nil || !present || !slices.Contains(agents.Ancestry(), parent) {
		t.Fatalf("this test's parent %d reads %+v, %v, %v, and the parent chain is %v; want it found there", os.Getppid(), parent, present, err, agents.Ancestry())
	}
	h, _ := sweepHome(t, sweepBase(t), "home")
	copyDir := makeTaskCopy(t, h)
	r, err := agents.SweepRefusing(t.Context(), agents.NewSweeper(h, sweepGrace, nil), copyDir, func(pid int) bool { return pid == parent.PID }, refusal)
	if err != nil || !r.Clear() || r.Unreadable < 1 || slices.Contains(r.UnreadableBlocking, parent) {
		t.Fatalf("the sweep refused the working directory of this test's parent %s and did %s, %v (%+v), want it clear with the refusal counted", parent, r, err, r)
	}
}

// TestARefusedProcessIsSetAsideOnlyWhenItSharesNoUserAndRefusesASignal holds
// the second carve-out and both its halves, from real status entries: a
// process of this test's user blocks whatever the zero signal answered, and
// a process of another user blocks unless the signal was refused, which is
// the one answer syscall.Kill gives as syscall.EPERM. The real answer for
// the other user's process is checked against the same rule.
func TestARefusedProcessIsSetAsideOnlyWhenItSharesNoUserAndRefusesASignal(t *testing.T) {
	requiresSweep(t)
	h, _ := sweepHome(t, sweepBase(t), "home")
	mine := startDetached(t, makeTaskCopy(t, h), false)
	for _, signal := range []error{nil, syscall.EPERM} {
		if blocks, err := agents.ClassifyRefused(mine.id.PID, signal); err != nil || !blocks {
			t.Fatalf("a refused process of this test's user with the signal answering %v: blocks %v, %v, want it blocking", signal, blocks, err)
		}
	}

	foreign := foreignProcess(t)
	for _, c := range []struct {
		signal error
		want   bool
	}{{nil, true}, {syscall.EPERM, false}, {syscall.Kill(foreign, 0), !errors.Is(syscall.Kill(foreign, 0), syscall.EPERM)}} {
		if blocks, err := agents.ClassifyRefused(foreign, c.signal); err != nil || blocks != c.want {
			t.Fatalf("a refused process %d of another user with the signal answering %v: blocks %v, %v, want %v", foreign, c.signal, blocks, err, c.want)
		}
	}
}

// foreignProcess is a running process sharing no user with this test and
// not on its parent chain: started as another user where this test may do
// that, and otherwise the first such process /proc lists, where the
// set-aside half of the assertion then restates the selection and only the
// blocking half discriminates. It skips, saying
// so, where there is neither.
func foreignProcess(t *testing.T) int {
	t.Helper()
	if os.Geteuid() == 0 {
		cmd := exec.Command("sleep", "60")
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 65534, Gid: 65534}}
		if err := cmd.Start(); err == nil {
			t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
			return cmd.Process.Pid
		}
	}
	ancestors := agents.Ancestry()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		id, present, err := agents.Identify(pid)
		if err != nil || !present || slices.Contains(ancestors, id) {
			continue
		}
		// Set aside on a refused signal is, off the parent chain, exactly
		// sharing no user, so the selection is the rule's own reading of the
		// status entry; the nil-signal case is what it then checks.
		if blocks, err := agents.ClassifyRefused(pid, syscall.EPERM); err != nil || blocks {
			continue
		}
		return pid
	}
	t.Skip("no process of another user is running or can be started here, so the second carve-out is not established for one")
	return 0
}
