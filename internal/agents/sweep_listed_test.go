//go:build linux || darwin

package agents_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/principles"
)

// realRefusal is a real unreadable working directory on this platform,
// as agents.RealRefusal finds one. The test skips, saying so, where none is,
// because a refusal it wrote itself would be a shape it stated rather than
// one the reader produced.
func realRefusal(t *testing.T) agents.Refusal {
	t.Helper()
	refusal, ok := agents.RealRefusal(t.Context())
	if !ok {
		t.Skip("no process's working directory is unreadable to this test here, so there is no real refusal to carry over and the classification of a refused process is not established through the listing")
	}
	return refusal
}

// TestAnUnreadableProcessOfThisUserInTheCopyKeepsTheSweepFromClearing
// establishes P6 on the sweep's answer: a process of this test's own user,
// standing in the copy, whose working directory the listing is refused, is
// a fact the sweep cannot verify, so the sweep refuses to clear rather than
// reporting a count beside a clear answer. The process is real and detached
// in the copy, every read the listing makes of it is real but the one
// refused, and that refusal is carried over from a real refused read. It is
// never signalled. The control is the same sweep with the read allowed,
// which finds the process, ends it and clears. The refusal half runs on
// every host; the control's clear is asserted only where an empty copy's
// sweep clears, and is otherwise recorded as a limit by hostLimit.
func TestAnUnreadableProcessOfThisUserInTheCopyKeepsTheSweepFromClearing(t *testing.T) {
	principles.Cite(t, principles.P6)
	requiresSweep(t)
	refusal := realRefusal(t)
	blockers := hostBlockers(t)
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
	if r.Clear() || !blocks(r, d.id) || r.Unreadable < 1 || mentions(r, d.id) {
		t.Fatalf("the sweep refused %s's working directory and did %s (%+v), want it not clear with that process blocking and never signalled", d.id, r, r)
	}
	requirePresent(t, d, "a process whose working directory the sweep was refused")

	control, err := sweeper.Sweep(ctx, copyDir)
	if err != nil || !slices.Contains(control.Ended, d.id) {
		t.Fatalf("the same sweep with the read allowed did %s, %v, want %s ended", control, err, d.id)
	}
	requireClearUnlessHostBlocks(t, control, blockers, "the same sweep with the read allowed")
	waitEnded(t, d, "the control sweep")
	hostLimit(t, blockers, "that the control sweep, with the read allowed, clears")
}

// TestAnUnreadableAncestorDoesNotKeepTheSweepFromClearing holds the first
// carve-out: a refused process on this process's parent chain is counted
// and does not keep an otherwise empty sweep from clearing. The refused
// process is this test's real parent, which the parent chain finds by
// identity. Where the environment itself refuses a link, as a container's
// init can be refused to a service running as its user, that refusal is
// read for real alongside. That the refused parent is counted and not
// blocking is asserted on every host; that the sweep then clears is
// asserted only where an empty copy's sweep clears.
func TestAnUnreadableAncestorDoesNotKeepTheSweepFromClearing(t *testing.T) {
	requiresSweep(t)
	refusal := realRefusal(t)
	blockers := hostBlockers(t)
	parent, present, err := agents.Identify(os.Getppid())
	if err != nil || !present || !slices.Contains(agents.Ancestry(), parent) {
		t.Fatalf("this test's parent %d reads %+v, %v, %v, and the parent chain is %v; want it found there", os.Getppid(), parent, present, err, agents.Ancestry())
	}
	h, _ := sweepHome(t, sweepBase(t), "home")
	copyDir := makeTaskCopy(t, h)
	r, err := agents.SweepRefusing(t.Context(), agents.NewSweeper(h, sweepGrace, nil), copyDir, func(pid int) bool { return pid == parent.PID }, refusal)
	if err != nil || r.Unreadable < 1 || blocks(r, parent) {
		t.Fatalf("the sweep refused the working directory of this test's parent %s and did %s, %v (%+v), want the refusal counted and not blocking", parent, r, err, r)
	}
	requireClearUnlessHostBlocks(t, r, blockers, "the sweep with this test's parent refused")
	hostLimit(t, blockers, "that a sweep whose only refused process in reach is an ancestor clears")
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
// that, and otherwise the first such process the process table lists, where the
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
	pids, err := agents.ProcessIDs()
	if err != nil {
		t.Fatal(err)
	}
	for _, pid := range pids {
		if pid <= 0 || pid == os.Getpid() {
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

// blockerFor is the entry r lists in UnreadableBlocking for id, if any.
func blockerFor(r agents.SweepResult, id agents.Identity) (agents.Blocker, bool) {
	i := slices.IndexFunc(r.UnreadableBlocking, func(b agents.Blocker) bool { return b.Identity == id })
	if i < 0 {
		return agents.Blocker{}, false
	}
	return r.UnreadableBlocking[i], true
}

// blocks reports whether r lists id in UnreadableBlocking.
func blocks(r agents.SweepResult, id agents.Identity) bool {
	_, ok := blockerFor(r, id)
	return ok
}

// namedHelper copies the test binary to a file called name in a directory
// of the test's, so a process started from it runs a file of that name.
func namedHelper(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(helperBinary(t))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, raw, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestARefusalNamesItsBlockersAndTheNameDecidesNothing establishes that a
// sweep that refuses to clear says which processes refused it: the blocker
// entry for a real process the test started in the copy, made unreadable
// the way TestAnUnreadableProcessOfThisUserInTheCopyKeepsTheSweepFromClearing
// makes one, carries that process's identifier and start time as Identify
// reads them, this test's user among its user identifiers, and the name of
// the file the test ran it from, and the result's String carries the entry.
//
// The positive control is that the name decides nothing. Two processes run
// from files of one name and a third from a file of another name: the two
// with different names in the swept copy, both refused, are both blocking;
// the one sharing the first's name, standing readable in another copy, is
// neither blocking nor mentioned. With the reads allowed, the sweep ends
// both in the copy, whatever their names, and leaves the namesake running.
// Whether the control's sweep then clears is asserted only where an empty
// copy's sweep clears, and otherwise recorded by hostLimit.
func TestARefusalNamesItsBlockersAndTheNameDecidesNothing(t *testing.T) {
	requiresSweep(t)
	refusal := realRefusal(t)
	blockers := hostBlockers(t)
	ctx := t.Context()
	h, _ := sweepHome(t, sweepBase(t), "home")
	copyDir := makeTaskCopy(t, h)
	elsewhere := makeTaskCopy(t, h)
	const name, otherName = "sweepnamed", "sweepother"
	named, other := namedHelper(t, name), namedHelper(t, otherName)
	inCopy := startDetachedAs(t, named, copyDir, false)
	namesake := startDetachedAs(t, named, elsewhere, false)
	renamed := startDetachedAs(t, other, copyDir, false)

	sweeper := agents.NewSweeper(h, sweepGrace, nil)
	refused := func(pid int) bool { return pid == inCopy.id.PID || pid == renamed.id.PID }
	r, err := agents.SweepRefusing(ctx, sweeper, copyDir, refused, refusal)
	if err != nil {
		t.Fatalf("sweeping %s: %v", copyDir, err)
	}
	if r.Clear() {
		t.Fatalf("the sweep refused two processes in %s and cleared: %s", copyDir, r)
	}
	t.Logf("the refusal reads: %s", r)
	for _, c := range []struct {
		d    detached
		name string
	}{{inCopy, name}, {renamed, otherName}} {
		b, ok := blockerFor(r, c.d.id)
		if !ok {
			t.Fatalf("the sweep refused %s's working directory and did %s, want it blocking", c.d.id, r)
		}
		if b.PID != c.d.id.PID || b.Start != c.d.id.Start || b.NameErr != nil || b.Name != c.name || !slices.Contains(b.UIDs, os.Geteuid()) {
			t.Fatalf("the blocker for %s reads %+v, want its identity, a user identifier of %d, and the name %q", c.d.id, b, os.Geteuid(), c.name)
		}
		if !strings.Contains(r.String(), b.String()) || !strings.Contains(b.String(), "pid "+strconv.Itoa(c.d.id.PID)) || !strings.Contains(b.String(), strconv.Quote(c.name)) {
			t.Fatalf("the sweep reads %q and the blocker %q, want the result to name the blocker by pid and name", r, b)
		}
		if mentions(r, c.d.id) {
			t.Fatalf("the sweep signalled or decided about a refused process %s: %+v", c.d.id, r)
		}
		requirePresent(t, c.d, "a refused process after the sweep")
	}
	if blocks(r, namesake.id) || mentions(r, namesake.id) {
		t.Fatalf("the sweep of %s reads %s, and names %s, which shares a blocker's name and stands readable in another copy", copyDir, r, namesake.id)
	}

	control, err := sweeper.Sweep(ctx, copyDir)
	if err != nil || !slices.Contains(control.Ended, inCopy.id) || !slices.Contains(control.Ended, renamed.id) || mentions(control, namesake.id) {
		t.Fatalf("the same sweep with the reads allowed did %s, %v, want %s and %s ended and %s untouched", control, err, inCopy.id, renamed.id, namesake.id)
	}
	waitEnded(t, inCopy, "the control sweep")
	waitEnded(t, renamed, "the control sweep")
	requirePresent(t, namesake, "the namesake in another copy after the control sweep")
	requireClearUnlessHostBlocks(t, control, blockers, "the same sweep with the reads allowed")
	hostLimit(t, blockers, "that the control sweep, with the reads allowed, clears")
}

// TestARefusalListsAtMostTenBlockersAndCountsTheRest holds the cap on what a
// refusal prints: with more real processes refused in the copy than
// agents.BlockersShown, the rendering names exactly that many, each as its
// entry renders, and counts the rest.
func TestARefusalListsAtMostTenBlockersAndCountsTheRest(t *testing.T) {
	requiresSweep(t)
	refusal := realRefusal(t)
	h, _ := sweepHome(t, sweepBase(t), "home")
	copyDir := makeTaskCopy(t, h)
	refuse := map[int]bool{}
	for range agents.BlockersShown + 1 {
		refuse[startDetached(t, copyDir, false).id.PID] = true
	}
	r, err := agents.SweepRefusing(t.Context(), agents.NewSweeper(h, sweepGrace, nil), copyDir, func(pid int) bool { return refuse[pid] }, refusal)
	if err != nil || len(r.UnreadableBlocking) <= agents.BlockersShown {
		t.Fatalf("sweeping %s with %d processes refused did %s, %v, want more than %d blocking", copyDir, len(refuse), r, err, agents.BlockersShown)
	}
	text := agents.DescribeBlockers(r.UnreadableBlocking)
	want := make([]string, 0, agents.BlockersShown+1)
	for _, b := range r.UnreadableBlocking[:agents.BlockersShown] {
		want = append(want, b.String())
	}
	want = append(want, "and "+strconv.Itoa(len(r.UnreadableBlocking)-agents.BlockersShown)+" more")
	if got := strings.Join(want, "; "); text != got {
		t.Fatalf("the blockers render as %q, want %q", text, got)
	}
}

// TestABlockerWhoseNameCannotBeReadIsStillReported holds that a failed name
// read is recorded and decides nothing: the entry for a real identity
// carries the name, and the entry for a mutation of it, a start time the
// process does not have, which is what a reused identifier reads as, keeps
// the identity and user identifiers it was given, records the name as
// unreadable, and says so when rendered. So does the entry for the real
// identity once the process has ended.
func TestABlockerWhoseNameCannotBeReadIsStillReported(t *testing.T) {
	requiresSweep(t)
	h, _ := sweepHome(t, sweepBase(t), "home")
	const name = "sweepnamed"
	d := startDetachedAs(t, namedHelper(t, name), makeTaskCopy(t, h), false)
	uids := []int{os.Getuid(), os.Geteuid()}

	if b := agents.BlockerOf(d.id, uids); b.NameErr != nil || b.Name != name || b.Identity != d.id {
		t.Fatalf("the blocker for the real identity %s reads %+v, want the name %q", d.id, b, name)
	}
	stale := agents.Identity{PID: d.id.PID, Start: d.id.Start + 1}
	b := agents.BlockerOf(stale, uids)
	if !errors.Is(b.NameErr, agents.ErrNameReplaced) || b.Name != "" || b.Identity != stale || !slices.Equal(b.UIDs, uids) || !strings.Contains(b.String(), "name unreadable") {
		t.Fatalf("the blocker for a stale identity reads %+v (%s), want the identity kept and the name unreadable because the identifier names another process", b, b)
	}

	if err := syscall.Kill(d.id.PID, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	waitEnded(t, d, "the process killed before its name is read")
	if b := agents.BlockerOf(d.id, uids); b.NameErr == nil || b.Name != "" || b.Identity != d.id || !strings.Contains(b.String(), "name unreadable") {
		t.Fatalf("the blocker for an ended process reads %+v (%s), want the name unreadable", b, b)
	}
}
