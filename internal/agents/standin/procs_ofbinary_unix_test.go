//go:build linux || darwin

package standin

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/principles"
)

// ofBinaryWant is the executable the scripted listings below list.
const ofBinaryWant = "/want/binary-722"

// scriptedPass is what a scriptedTable answers for one pass of a listing,
// that is between one ids call and the next: the numbers the table names,
// the executable the snapshot reads at each (absent: the read fails), and
// the running process at each (absent: not running). A running process is
// a real one (liveProcess), so its identity's Presence is the kernel's, and
// exe is the executable commandLine reads of it.
type scriptedPass struct {
	pids    []int
	exe     map[int]string
	running map[int]agents.Identity
}

// scriptedTable plays passes, one per ids call, repeating the last, so a
// listing's passes and readings meet a schedule a test states.
type scriptedTable struct {
	kernel
	passes []scriptedPass
	exe    map[agents.Identity]string
	calls  int
	at     scriptedPass
}

func (s *scriptedTable) ids() ([]int, error) {
	s.at = s.passes[min(s.calls, len(s.passes)-1)]
	s.calls++
	return s.at.pids, nil
}

func (s *scriptedTable) executable(pid int) (string, error) {
	if path, ok := s.at.exe[pid]; ok {
		return path, nil
	}
	return "", errors.New("no executable read")
}

func (s *scriptedTable) identify(pid int) (agents.Identity, bool, error) {
	id, ok := s.at.running[pid]
	return id, ok, nil
}

func (s *scriptedTable) commandLine(pid int) (string, []string, error) {
	id, ok := s.at.running[pid]
	if !ok {
		return "", nil, errors.New("not running")
	}
	return s.exe[id], []string{s.exe[id], "arg"}, nil
}

func (s *scriptedTable) foreign(int) (bool, error)   { return false, nil }
func (s *scriptedTable) isExiting(int) (bool, error) { return false, nil }

// liveProcess starts a real process that runs until the test ends and
// returns its identity.
func liveProcess(t *testing.T) agents.Identity {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	id, found, err := agents.Identify(cmd.Process.Pid)
	if err != nil || !found {
		t.Fatalf("identifying process %d: %v, %v", cmd.Process.Pid, found, err)
	}
	return id
}

// listsOrUnsettles fails t when a listing returned successfully without
// want, the empty success a caller cleaning up would act on.
func listsOrUnsettles(t *testing.T, listed []Process, err error, want agents.Identity) {
	t.Helper()
	var unsettled *unsettledError
	if err != nil && !errors.As(err, &unsettled) {
		t.Fatalf("listing: %v", err)
	}
	if err == nil && !slices.ContainsFunc(listed, func(p Process) bool { return p.Identity == want }) {
		t.Fatalf("the listing settled as %v without %v, which runs binary", listed, want)
	}
}

// The executable the snapshot reads is the one ofBinary compares, the first
// thing commandLine reads, for a process started through a symbolic link
// as well as one started by its own path.
func TestTheSnapshotReadsTheExecutableOfBinaryCompares(t *testing.T) {
	t.Parallel()
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "linked-sleep")
	if err := os.Symlink(sleep, link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{sleep, link} {
		cmd := exec.Command(path, "60")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
		snapped, err := executable(cmd.Process.Pid)
		if err != nil {
			t.Fatalf("reading the executable of %s: %v", path, err)
		}
		compared, _, err := commandLine(cmd.Process.Pid)
		if err != nil {
			t.Fatalf("reading the command line of %s: %v", path, err)
		}
		if snapped == "" || snapped != compared {
			t.Errorf("started as %s, the snapshot read %q and ofBinary compares %q", path, snapped, compared)
		}
	}
}

// A process that exits unread, whose snapshot named another executable,
// settles through (c), and the listing lists the live process of binary
// beside it (P15, #722). The schedule is 64 passes in a row that each name a
// new such exit, then a quiet one: the follow-ups take them all in one
// reading, where a listing without (c) spends a reading on each and none of
// its 64 settles. That is the whole of what (c) adds: a follow-up pass
// settles on what a next reading would, so (c) gives a listing more passes
// in which one can.
func TestAnExitTheSnapshotRefusedSettlesBesideALiveProcessOfBinary(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	match := liveProcess(t)
	gone := newGoneNumbers(80_001, match.PID)
	var passes []scriptedPass
	for range settleReadings {
		pid := gone.take()
		passes = append(passes, scriptedPass{
			pids:    []int{match.PID, pid},
			exe:     map[int]string{match.PID: ofBinaryWant, pid: "/bin/sh"},
			running: map[int]agents.Identity{match.PID: match},
		})
	}
	passes = append(passes, scriptedPass{
		pids:    []int{match.PID},
		exe:     map[int]string{match.PID: ofBinaryWant},
		running: map[int]agents.Identity{match.PID: match},
	})
	exe := map[agents.Identity]string{match: ofBinaryWant}

	listed, err := processesOf(&scriptedTable{passes: passes, exe: exe}, ofBinaryWant)
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	if len(listed) != 1 || listed[0].Identity != match {
		t.Fatalf("the listing is %v, want only %v", listed, match)
	}
	_, err = processesWhere(&scriptedTable{passes: passes, exe: exe}, ofBinary(ofBinaryWant))
	var unsettled *unsettledError
	if !errors.As(err, &unsettled) {
		t.Fatalf("without (c) the listing returned %v, want no reading settled", err)
	}
}

// What a process (c) settled starts after the pass that named it is read on
// a follow-up (#722 design round 1, finding 2): the parent's snapshot named
// another executable, it execs binary, starts a child and exits before it
// is identified; the child is listed, never left out of an empty success.
func TestAFollowUpListsWhatAProcessSettledThroughCStarted(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	child := liveProcess(t)
	parent := newGoneNumbers(80_001, child.PID).take()
	table := &scriptedTable{exe: map[agents.Identity]string{child: ofBinaryWant}, passes: []scriptedPass{
		{pids: []int{parent}, exe: map[int]string{parent: "/bin/sh"}},
		{pids: []int{child.PID}, exe: map[int]string{child.PID: ofBinaryWant}, running: map[int]agents.Identity{child.PID: child}},
	}}
	listed, err := processesOf(table, ofBinaryWant)
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	listsOrUnsettles(t, listed, err, child)
}

// A follow-up identifies again every number it names, so a number (c)
// settled that a process of binary has taken since is read as that process
// (#722 design round 2, finding 1).
func TestAFollowUpReadsTheProcessNowAtANumberCSettled(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	reuse := liveProcess(t)
	table := &scriptedTable{exe: map[agents.Identity]string{reuse: ofBinaryWant}, passes: []scriptedPass{
		{pids: []int{reuse.PID}, exe: map[int]string{reuse.PID: "/bin/sh"}},
		{pids: []int{reuse.PID}, exe: map[int]string{reuse.PID: ofBinaryWant}, running: map[int]agents.Identity{reuse.PID: reuse}},
	}}
	listed, err := processesOf(table, ofBinaryWant)
	listsOrUnsettles(t, listed, err, reuse)
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
}

// A follow-up identifies again a number whose process an earlier pass read
// whole and refused, so a process of binary that took the number since is
// read as itself (#722 design round 3, finding 1).
func TestAFollowUpReadsTheProcessNowAtANumberItRefusedLive(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	refused := liveProcess(t)
	// reuse is another process at the same number: the same PID, another
	// start time. The kernel reads its Presence as replaced, so the listing
	// unsettles on it, and it must not settle without it.
	reuse := refused
	reuse.Start++
	other := newGoneNumbers(80_001, refused.PID).take()
	table := &scriptedTable{
		exe: map[agents.Identity]string{refused: "/bin/sh", reuse: ofBinaryWant},
		passes: []scriptedPass{
			{pids: []int{refused.PID, other}, exe: map[int]string{refused.PID: "/bin/sh", other: "/bin/sh"}, running: map[int]agents.Identity{refused.PID: refused}},
			{pids: []int{refused.PID}, exe: map[int]string{refused.PID: ofBinaryWant}, running: map[int]agents.Identity{refused.PID: reuse}},
		},
	}
	listed, err := processesOf(table, ofBinaryWant)
	listsOrUnsettles(t, listed, err, reuse)
}

// A pass that settles a new process through (c) keeps the reading going,
// even when the number was named before (#722 design round 4): Q is read
// whole and refused, then starts a child of binary after the next pass's
// table and exits before it is identified; the follow-up after that names
// the child.
func TestALateCOnANumberAlreadyNamedKeepsTheReadingGoing(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	q, child := liveProcess(t), liveProcess(t)
	shell := newGoneNumbers(80_001, q.PID, child.PID).take()
	table := &scriptedTable{
		exe: map[agents.Identity]string{q: "/bin/sh", child: ofBinaryWant},
		passes: []scriptedPass{
			{pids: []int{shell}, exe: map[int]string{shell: "/bin/sh"}},
			{pids: []int{shell, q.PID}, exe: map[int]string{q.PID: "/bin/sh"}, running: map[int]agents.Identity{q.PID: q}},
			{pids: []int{shell, q.PID}, exe: map[int]string{q.PID: "/bin/sh"}},
			{pids: []int{shell, q.PID, child.PID}, exe: map[int]string{child.PID: ofBinaryWant}, running: map[int]agents.Identity{child.PID: child}},
		},
	}
	listed, err := processesOf(table, ofBinaryWant)
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	listsOrUnsettles(t, listed, err, child)
}

// A number already read as not running in the reading, read so again while
// this pass's snapshot names binary at it, unsettles the reading (#722
// design round 5): a process of binary held it since, and may have started
// another.
func TestANumberStoppedEarlierWhoseSnapshotNamesBinaryUnsettles(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	gone := newGoneNumbers(80_001)
	var passes []scriptedPass
	for range settleReadings {
		pid := gone.take()
		passes = append(passes,
			scriptedPass{pids: []int{pid}, exe: map[int]string{pid: "/bin/sh"}},
			scriptedPass{pids: []int{pid}, exe: map[int]string{pid: ofBinaryWant}})
	}
	_, err := processesOf(&scriptedTable{passes: passes}, ofBinaryWant)
	var unsettled *unsettledError
	if !errors.As(err, &unsettled) {
		t.Fatalf("listing = %v, want no reading settled", err)
	}
}

// An exit whose snapshot could not be read refuses nothing, and one whose
// snapshot named binary is not settled through (c): both unsettle every
// reading, as an unread exit does in any listing.
func TestAnExitTheSnapshotDidNotRefuseUnsettles(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	for name, exe := range map[string]func(pid int) map[int]string{
		"unread": func(int) map[int]string { return nil },
		"binary": func(pid int) map[int]string { return map[int]string{pid: ofBinaryWant} },
	} {
		gone := newGoneNumbers(80_001)
		var passes []scriptedPass
		for range settleReadings {
			pid := gone.take()
			passes = append(passes, scriptedPass{pids: []int{pid}, exe: exe(pid)})
		}
		table := &scriptedTable{passes: passes}
		_, err := processesOf(table, ofBinaryWant)
		var unsettled *unsettledError
		if !errors.As(err, &unsettled) || table.calls != settleReadings {
			t.Errorf("%s: listing = %v after %d passes, want every one of %d readings unsettled", name, err, table.calls, settleReadings)
		}
	}
}

// A running process is read whole whatever its snapshot named: one that
// execs binary after the snapshot is listed as a process of binary.
func TestARunningProcessIsReadWholeWhateverItsSnapshotNamed(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	execed := liveProcess(t)
	table := &scriptedTable{exe: map[agents.Identity]string{execed: ofBinaryWant}, passes: []scriptedPass{
		{pids: []int{execed.PID}, exe: map[int]string{execed.PID: "/bin/sh"}, running: map[int]agents.Identity{execed.PID: execed}},
	}}
	listed, err := processesOf(table, ofBinaryWant)
	if err != nil || len(listed) != 1 || listed[0].Identity != execed {
		t.Fatalf("the listing is %v, %v, want %v", listed, err, execed)
	}
}

// Follow-ups that never go quiet unsettle the reading, naming a process that
// kept them going: a new number in every follow-up exhausts followUpPasses.
func TestFollowUpsThatNeverGoQuietUnsettleTheReading(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	gone := newGoneNumbers(80_001)
	var passes []scriptedPass
	for range settleReadings * (followUpPasses + 1) {
		pid := gone.take()
		passes = append(passes, scriptedPass{pids: []int{pid}, exe: map[int]string{pid: "/bin/sh"}})
	}
	_, err := processesOf(&scriptedTable{passes: passes}, ofBinaryWant)
	var unsettled *unsettledError
	if !errors.As(err, &unsettled) || unsettled.PID == 0 {
		t.Fatalf("listing = %v, want unsettled naming a process that kept the follow-ups going", err)
	}
}

// (c) is an ofBinary listing's alone: leftRunning with an owner value lists
// by executable or identity, and an unread exit the snapshot would refuse
// still unsettles every reading there.
func TestLeftRunningWithAnOwnerValueDoesNotSettleThroughC(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	gone := newGoneNumbers(80_001)
	var passes []scriptedPass
	for range settleReadings {
		pid := gone.take()
		passes = append(passes, scriptedPass{pids: []int{pid}, exe: map[int]string{pid: "/bin/sh"}})
	}
	table := &scriptedTable{passes: passes}
	_, err := leftRunning(table, ofBinaryWant, "owner")
	var unsettled *unsettledError
	if !errors.As(err, &unsettled) || table.calls != settleReadings {
		t.Fatalf("listing = %v after %d passes, want every one of %d readings unsettled", err, table.calls, settleReadings)
	}
	listed, err := leftRunning(&scriptedTable{passes: append(passes[:1:1], scriptedPass{})}, ofBinaryWant, "")
	if err != nil || len(listed) != 0 {
		t.Fatalf("with no owner value, leftRunning = %v, %v, want the ofBinary listing's empty settle", listed, err)
	}
}

// The accepted gap (Linear AGE-508): a number already read as not running
// in the reading, taken by a process of binary that starts a child after
// the pass's table and exits before it is identified, is never read when
// the snapshot missed it or read the number's earlier process. The reading
// settles empty while the child runs. This test states the gap so a change
// that closes it shows here; it is not a requirement.
func TestAMatchingProcessTheSnapshotNeverReadIsTheAcceptedGap(t *testing.T) {
	t.Parallel()
	child := liveProcess(t)
	pid := newGoneNumbers(80_001, child.PID).take()
	for name, second := range map[string]map[int]string{
		"no snapshot":                    nil,
		"the earlier process's snapshot": {pid: "/bin/sh"},
	} {
		table := &scriptedTable{passes: []scriptedPass{
			{pids: []int{pid}, exe: map[int]string{pid: "/bin/sh"}},
			{pids: []int{pid}, exe: second},
		}}
		listed, err := processesOf(table, ofBinaryWant)
		if err != nil || len(listed) != 0 {
			t.Errorf("%s: the listing is %v, %v; the accepted gap settles empty here, so a change closed it: update the doc comment and this test", name, listed, err)
		}
	}
}
