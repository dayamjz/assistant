//go:build darwin

package standin

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/procgroup"
)

// startHeldBinary starts this binary as a process that runs until something
// ends it (tmuxPaneArg), and ends it with the test.
func startHeldBinary(t *testing.T) *exec.Cmd {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	held := exec.Command(self, "-test.run=^$", tmuxPaneArg)
	if err := held.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Process.Kill(); _ = held.Wait() })
	return held
}

// startSleep starts a /bin/sleep child of this process, and ends it with the
// test.
func startSleep(t *testing.T) *exec.Cmd {
	t.Helper()
	sleeper := exec.Command("/bin/sleep", "60")
	if err := sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sleeper.Process.Kill(); _ = sleeper.Wait() })
	return sleeper
}

// endedBinaryRecord is a record the kernel's process table held of a process
// of this binary that has since ended and been reaped: readTable read it
// from the kernel while the process ran, and the control is that it names
// the process as agents.Identify did then.
func endedBinaryRecord(t *testing.T) tableRecord {
	t.Helper()
	held := startHeldBinary(t)
	_, records, err := readTable()
	if err != nil {
		t.Fatal(err)
	}
	record, ok := records[held.Process.Pid]
	id, running, ierr := agents.Identify(held.Process.Pid)
	if !ok || ierr != nil || !running || record.Identity != id {
		t.Fatalf("the table's record of process %d is %+v (held %v); Identify read %v, %v, %v", held.Process.Pid, record, ok, id, running, ierr)
	}
	_ = held.Process.Kill()
	_ = held.Wait()
	return record
}

// awaitNotRunning waits up to five seconds for agents.Identify to read pid
// as not running, and reports whether it did.
func awaitNotRunning(pid int) bool {
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, running, err := agents.Identify(pid); err == nil && !running {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// outsideLoop is the shell startOutside runs: it runs its arguments again
// each time they end, until the file its first argument names exists or two
// minutes have passed, in the background of a shell that exits once it has
// printed the background shell's number. Its test forks nothing, so the
// background shell's only children are what it runs: a test that ended a
// command substitution's fork once ended the loop (CI, macOS).
const outsideLoop = `stop=$1; shift
( while [ ! -e "$stop" ] && [ "$SECONDS" -lt 120 ]; do "$@" & wait $!; done ) </dev/null >/dev/null 2>&1 &
echo $!`

// startOutside starts, outside this process's tree, a shell that runs
// command again each time it ends, with env added to this process's
// environment, and returns that shell's number. The shell is the second of
// two forks: the first exits once it has started it, so the kernel gives it
// to launchd, which is checked, and no process this one started is its
// ancestor. Both are in a process group of their own, which the cleanup
// kills whole after leaving the stop file the shell checks.
func startOutside(t *testing.T, env []string, command ...string) int {
	t.Helper()
	stop := filepath.Join(t.TempDir(), "stop")
	first := exec.Command("/bin/sh", append([]string{"-c", outsideLoop, "sh", stop}, command...)...)
	first.Env = append(os.Environ(), env...)
	first.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := first.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	group := first.Process.Pid
	t.Cleanup(func() {
		_ = os.WriteFile(stop, nil, 0o600)
		_ = syscall.Kill(-group, syscall.SIGKILL)
	})
	line, rerr := bufio.NewReader(out).ReadString('\n')
	_ = first.Wait()
	shell, err := strconv.Atoi(strings.TrimSpace(line))
	if rerr != nil || err != nil {
		t.Fatalf("the first shell printed %q (%v) for the second's number", line, rerr)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		lineage, found, err := agents.ReadLineage(shell)
		if err == nil && found && lineage.Parent == 1 {
			return shell
		}
		if !time.Now().Before(deadline) {
			t.Fatalf("shell %d's parent reads %d (%v, %v), not launchd, so it is not outside this tree", shell, lineage.Parent, found, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// endsOutsideEachReading is a processTable that passes every read through
// to the kernel and, in each reading, ends one of the real processes that
// reading's records name as a child of parent, under name when name is set,
// so every reading meets a process that exited unread, as one on a busy
// machine does. It waits for the kernel to name one it has not ended yet,
// which parent starts again each time one ends. It ends it once the kernel
// has named it and before the listing identifies it, or, with atRead, once
// the listing has read its command line once; renamed, when set, is the
// name the reading's record of it carries in place of the kernel's, the
// one a record read before the process's exec carries.
type endsOutsideEachReading struct {
	kernel
	parent          int
	name, renamed   string
	atRead          bool
	readings, ended int
	gone            map[agents.Identity]bool
	pick            int
}

func (e *endsOutsideEachReading) ids() ([]int, map[int]tableRecord, error) {
	e.readings++
	if e.gone == nil {
		e.gone = map[agents.Identity]bool{}
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		pids, records, err := e.kernel.ids()
		if err != nil {
			return nil, nil, err
		}
		for _, pid := range pids {
			record := records[pid]
			if record.parent != e.parent || e.gone[record.Identity] || (e.name != "" && record.name != e.name) {
				continue
			}
			if presence, err := record.Presence(); err != nil || presence != agents.PresencePresent {
				continue
			}
			e.gone[record.Identity] = true
			if e.renamed != "" {
				record.name = e.renamed
				records[pid] = record
			}
			if e.atRead {
				e.pick = pid
			} else {
				e.end(pid)
			}
			return pids, records, nil
		}
		if !time.Now().Before(deadline) {
			return pids, records, nil
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (e *endsOutsideEachReading) commandLine(pid int) (string, []string, error) {
	executable, args, err := e.kernel.commandLine(pid)
	if e.atRead && e.pick > 0 && pid == e.pick {
		e.pick = 0
		e.end(pid)
	}
	return executable, args, err
}

// end ends pid, never a number that signals a group, and counts it as
// ended once it reads as not running.
func (e *endsOutsideEachReading) end(pid int) {
	if pid <= 1 {
		return
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	if awaitNotRunning(pid) {
		e.ended++
	}
}

// A listing of this binary's processes settles while processes outside its
// tree exit unread in every reading (P15, #722): no record of theirs names
// the binary, and no ancestor's names it or is this process, so what they
// start is no process of the binary either, unless it execs the binary
// after the table was read, the gap processesWhere states. Here a real shell
// outside this tree, its parent launchd, runs one /bin/sleep after another,
// and the table ends one of them in every reading after the kernel named it
// and before the listing identifies it. Before, every one of the 64
// readings was unsettled by that exit and the listing failed, which is
// #722's failure. Not parallel, so the exits of other tests' children,
// which may be the binary's, do not keep it from settling. The controls are
// that every reading ended an outside process, and that a held process of
// the binary is listed.
func TestAListingOfABinarySettlesWhileProcessesOutsideItsTreeExitUnread(t *testing.T) {
	principles.Cite(t, principles.P15)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	held := startHeldBinary(t)
	table := &endsOutsideEachReading{parent: startOutside(t, nil, "/bin/sleep", "60"), name: "sleep"}
	listed, err := processesOfBinary(table, self)
	if table.ended != table.readings {
		t.Fatalf("%d of %d readings ended a process outside this tree, so this shows nothing", table.ended, table.readings)
	}
	if err != nil {
		t.Fatalf("a listing while a process outside this tree exits unread in each reading: %v", err)
	}
	if !slices.ContainsFunc(listed, func(p Process) bool { return p.PID == held.Process.Pid }) {
		t.Errorf("the listing is %v, without the held process %d of this binary", listed, held.Process.Pid)
	}
	t.Logf("settled in %d reading(s)", table.readings)
}

// readingOf is one reading of a process table that holds just the processes
// named: this process and its ancestors, the kernel's own record of each of
// live, every record in staged at its own number in place of the kernel's,
// and a number list naming exactly the records the reading holds, in the
// order the kernel's table names them. That is how readTable builds the two,
// from the one kern.proc.all read, and the two fakes below that stage a
// record answer through this.
//
// This process's own lineage is in every reading because the kernel's table
// always names it, and candidates.checkSelf reads this process's record to
// check what the listing predicts the binary is recorded as. A reading that
// left it out would be unsettled by that check rather than by what the row
// staged, and that is measured too: without the lineage, deleting the
// in-reading reuse check left
// TestAListingSeesARecordedProcessExitWhileItsNumberIsReused passing under
// this part's rule. The identify loop reads nothing of those numbers in any
// case, because ownLineage excludes them.
//
// The one liberty those two rows take is the table's size, and the number
// list and the record map name the same processes, so no process is hidden
// from the identify loop while its record stays in reach of the checks that
// read records. A reading whose list was a subset of its records would be
// the shape readTable cannot make, and is the finding that brought this
// helper (#804's round 2, and the third of that class in this file).
//
// The machine's whole table cannot serve those two rows, which are part 1's.
// On part 1's rule any process outside this tree that exits unread unsettles
// a reading, which is the failure of #722, so over the whole table a reading
// is unsettled whatever a row stages and neither row could tell its staged
// cause from the machine's. That is measured, not assumed: with the whole
// table named, deleting the in-reading reuse check left
// TestAListingSeesARecordedProcessExitWhileItsNumberIsReused passing,
// because the machine's own churn had unsettled the first reading. This
// part's own rows are what make a listing settle under that churn, and they
// name the whole table through endsOutsideEachReading.
func readingOf(t *testing.T, live []int, staged ...tableRecord) ([]int, map[int]tableRecord, error) {
	t.Helper()
	order, all, err := readTable()
	if err != nil {
		return nil, nil, err
	}
	records := make(map[int]tableRecord, len(live)+len(staged)+1)
	for pid := range ownLineage() {
		if record, ok := all[pid]; ok {
			records[pid] = record
		}
	}
	for _, pid := range live {
		record, ok := all[pid]
		if !ok {
			t.Fatalf("the kernel's table holds no record of process %d, so a reading cannot name it", pid)
		}
		records[pid] = record
	}
	for _, record := range staged {
		records[record.PID] = record
	}
	pids := make([]int, 0, len(records))
	for _, pid := range order {
		if _, ok := records[pid]; ok {
			pids = append(pids, pid)
		}
	}
	for _, record := range staged {
		if !slices.Contains(pids, record.PID) {
			pids = append(pids, record.PID)
		}
	}
	return pids, records, nil
}

// recordedAt is a processTable whose every reading names one live process,
// staging.PID, with the kernel's record of it, except that the first
// reading carries staging at that number in place of the kernel's record.
type recordedAt struct {
	kernel
	t        *testing.T
	staging  tableRecord
	readings int
}

func (r *recordedAt) ids() ([]int, map[int]tableRecord, error) {
	r.readings++
	if r.readings == 1 {
		return readingOf(r.t, nil, r.staging)
	}
	return readingOf(r.t, []int{r.staging.PID})
}

// A process the table recorded at a number, and that exited during the
// reading while another was given its number, is seen to exit (P15, #722):
// a reading whose record is not the process Identify then reads at that
// number is unsettled, since the recorded one exited unread and may have
// started another. Here the first reading holds, at a live sleep's number,
// the record of a process of this binary that ended before the sleep was
// started, which is what a table read before that process exited and the
// sleep was given its number would hold; both records are real ones read
// by readTable, and only their pairing at one number, in that one reading,
// is staged. The listing reads again, and the second reading, which carries
// the kernel's own record at that number, settles. Before, Identify's
// reading of the sleep was taken as the process the table named and the
// first reading settled, which is what deleting the in-reading reuse check
// gives again.
//
// Each reading names that one process, which is readingOf's liberty and the
// reason the reading counts here are exact rather than a floor. The controls
// are that the recorded process started before the sleep and that the sleep
// runs throughout.
func TestAListingSeesARecordedProcessExitWhileItsNumberIsReused(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ended := endedBinaryRecord(t)
	newcomer := startSleep(t)
	id, running, err := agents.Identify(newcomer.Process.Pid)
	if err != nil || !running || id.Start <= ended.Start {
		t.Fatalf("the sleep reads %v, %v, %v beside the ended record %v; want it started after the recorded process", id, running, err, ended.Identity)
	}
	staging := ended
	staging.PID = newcomer.Process.Pid
	table := &recordedAt{t: t, staging: staging}
	if _, err := processesOfBinary(table, self); err != nil || table.readings != 2 {
		t.Fatalf("a listing whose first record at %d is an ended process of this binary = %v after %d reading(s); want the first unsettled and the second settled", newcomer.Process.Pid, err, table.readings)
	}
	if presence, err := id.Presence(); err != nil || presence != agents.PresencePresent {
		t.Fatalf("the sleep %v was not running once the listing returned (%v, %v), so this shows nothing", id, presence, err)
	}
}

// refusedNumberRecordedAgain is a processTable that names, through
// readingOf, the processes it orders and nothing else, and orders real
// processes' lives around its reads of the table. Every reading names a
// sleep that runs throughout, held so that no reading names an empty table.
// The first reading names beside it a sleep the listing reads whole and
// refuses, and an exiting sleep, which it ends once the kernel's table is
// read, so that reading meets a real unread exit and is unsettled. Before
// the second reading it ends and reaps the refused sleep, starts a process
// of this binary, reads its record, and ends and reaps it too; the second
// reading carries that record at the refused sleep's number, as if the
// process had been given the number and recorded there and then exited
// before it was identified. The third reading names the held sleep alone,
// which it has read whole already, and settles.
type refusedNumberRecordedAgain struct {
	kernel
	t                *testing.T
	held, refused    *exec.Cmd
	readings, staged int
	stagedStart      uint64
	refusedStart     uint64
}

func (r *refusedNumberRecordedAgain) ids() ([]int, map[int]tableRecord, error) {
	r.readings++
	switch r.readings {
	case 1:
		r.held = startSleep(r.t)
		r.refused = startSleep(r.t)
		exiting := startSleep(r.t)
		pids, records, err := readingOf(r.t, []int{r.held.Process.Pid, r.refused.Process.Pid, exiting.Process.Pid})
		_ = exiting.Process.Kill()
		_ = exiting.Wait()
		if err != nil {
			return nil, nil, err
		}
		r.refusedStart = records[r.refused.Process.Pid].Start
		return pids, records, nil
	case 2:
		_ = r.refused.Process.Kill()
		_ = r.refused.Wait()
		staging := endedBinaryRecord(r.t)
		staging.PID = r.refused.Process.Pid
		r.staged++
		r.stagedStart = staging.Start
		return readingOf(r.t, []int{r.held.Process.Pid}, staging)
	default:
		return readingOf(r.t, []int{r.held.Process.Pid})
	}
}

// A process number that a reading read as a refused process does not pass
// over another process the next reading records at that number (P15, #722):
// a refused process's exit is told by its record's identity, not its
// number, so one of this binary's given the number between two readings,
// that starts another and exits before it is identified, unsettles the
// reading it is recorded in. Here the first reading reads a sleep whole and
// refuses it, and the second records at its number a process of this
// binary started after that sleep ended and itself ended since: a real
// record read by readTable, of which only the number is staged, the shape
// the number's reuse makes. The listing reads a third time, which names the
// held sleep it has read already, and settles. Before, the number was taken
// for the refused sleep, its exit passed over as a refused process's, and
// the listing settled on the second reading, which is what keying refused
// processes by number gives again.
//
// Each reading names the processes this row orders and nothing else, which
// is readingOf's liberty and the reason the reading counts here are exact.
// The controls are that the second reading met the staged record and that it
// records a start after the refused sleep's. Not parallel: it starts and
// ends real processes around its readings, which would keep other tests'
// listings from settling.
func TestARefusedProcessNumberRecordedForAnotherIsNotPassedOver(t *testing.T) {
	principles.Cite(t, principles.P15)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	table := &refusedNumberRecordedAgain{t: t}
	_, err = processesOfBinary(table, self)
	if table.staged != 1 || table.stagedStart <= table.refusedStart {
		t.Fatalf("the second reading met the staged record %d time(s), started at %d beside the refused sleep's %d, so this shows nothing", table.staged, table.stagedStart, table.refusedStart)
	}
	if err != nil || table.readings != 3 {
		t.Fatalf("a listing whose refused process number is recorded for an ended process of this binary = %v after %d reading(s); want the first two unsettled and the third settled", err, table.readings)
	}
}

// insideLoop is the shell startInside runs: outsideLoop's loop, run by the
// shell itself rather than in its background.
const insideLoop = `stop=$1; shift
while [ ! -e "$stop" ] && [ "$SECONDS" -lt 120 ]; do "$@" & wait $!; done`

// startInside starts, as a child of this process, a shell that runs command
// again each time it ends, as startOutside's does, and returns its number:
// what it runs is a grandchild of this process. It is in a process group of
// its own, which the cleanup kills whole after leaving the stop file.
func startInside(t *testing.T, command ...string) int {
	t.Helper()
	stop := filepath.Join(t.TempDir(), "stop")
	shell := exec.Command("/bin/sh", append([]string{"-c", insideLoop, "sh", stop}, command...)...)
	shell.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := shell.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.WriteFile(stop, nil, 0o600)
		_ = syscall.Kill(-shell.Process.Pid, syscall.SIGKILL)
		_ = shell.Wait()
	})
	return shell.Process.Pid
}

// copyOfBinary writes a copy of this binary named name in a directory of
// the test's, and returns its path.
func copyOfBinary(t *testing.T, name string) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	copied := filepath.Join(t.TempDir(), name)
	if err := procgroup.WriteProgram(copied, content, 0o700); err != nil {
		t.Fatal(err)
	}
	return copied
}

// startHeld starts the binary at path as a process that runs until
// something ends it (tmuxPaneArg), and ends it with the test.
func startHeld(t *testing.T, path string) *exec.Cmd {
	t.Helper()
	held, err := StartCopied(func() (*exec.Cmd, error) {
		cmd := exec.Command(path, "-test.run=^$", tmuxPaneArg)
		return cmd, cmd.Start()
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Process.Kill(); _ = held.Wait() })
	return held
}

// predictedName is recordedName of path, failing the test where it
// predicts none.
func predictedName(t *testing.T, path string) string {
	t.Helper()
	name, ok := recordedName(path)
	if !ok {
		t.Fatalf("no recorded name is predicted for %s", path)
	}
	return name
}

// everyReadingUnsettled fails the test unless err is the unsettledError of
// a listing whose every reading was unsettled.
func everyReadingUnsettled(t *testing.T, what string, err error, readings int) {
	t.Helper()
	var unsettled *unsettledError
	if !errors.As(err, &unsettled) || readings != settleReadings {
		t.Fatalf("%s = %v after %d reading(s); want all %d unsettled", what, err, readings, settleReadings)
	}
}

// A process of the binary outside this tree whose exit is unread unsettles
// the reading (P15, #722): its record names it by the binary's name, so it
// is a candidate whatever its parents are, and what it started may be the
// binary's. Here a shell outside this tree runs this binary again each time
// it ends, and the table ends it in every reading once the kernel has
// recorded it under the binary's name and before the listing identifies it.
// Without the name clause of the rule the listing settles on its first
// reading. The control is that every reading ended one.
func TestAnUnreadExitOfABinaryProcessOutsideTheTreeUnsettles(t *testing.T) {
	principles.Cite(t, principles.P15)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	shell := startOutside(t, []string{"STANDIN_TEST_HOLD_OPEN=1"}, self)
	table := &endsOutsideEachReading{parent: shell, name: predictedName(t, self)}
	_, err = processesOfBinary(table, self)
	if table.ended != table.readings {
		t.Fatalf("%d of %d readings ended a process of the binary outside this tree, so this shows nothing", table.ended, table.readings)
	}
	everyReadingUnsettled(t, "a listing while a process of the binary outside this tree exits unread in each reading", err, table.readings)
}

// A descendant of this process that has not exec'd the binary yet, and
// whose exit is unread, unsettles the reading (P15, #735 r1 finding 1): its
// record names another program, but its recorded parents reach this
// process, so it is a candidate, and it may have started the binary as
// finding 1's env did. Here this process's child shell runs one sleep after
// another, and the table ends one in every reading before the listing
// identifies it. The listing is of a copy of this binary, which this
// process does not run, so only the this-process clause of the rule makes
// the sleep a candidate; without it the listing settles on its first
// reading. The control is that every reading ended one.
func TestAnUnreadExitOfADescendantOfThisProcessUnsettles(t *testing.T) {
	principles.Cite(t, principles.P15)
	copied := copyOfBinary(t, "descendant.test")
	table := &endsOutsideEachReading{parent: startInside(t, "/bin/sleep", "60"), name: "sleep"}
	_, err := processesOfBinary(table, copied)
	if table.ended != table.readings {
		t.Fatalf("%d of %d readings ended a descendant of this process, so this shows nothing", table.ended, table.readings)
	}
	everyReadingUnsettled(t, "a listing while a descendant of this process exits unread in each reading", err, table.readings)
}

// A failed read of a running process that is no candidate still unsettles
// the reading (P15, #735 r1 finding 2): the record judges only exits, never
// a read that fails while the process runs. Here a sleep outside this tree,
// which the reading's records judge no candidate, has every read of its
// record fail with EIO (failsMidExec), and the listing fails naming it and
// the EIO in each of its readings. The control is that the sleep runs
// throughout.
func TestAFailedReadOfAProcessThatIsNoCandidateUnsettles(t *testing.T) {
	principles.Cite(t, principles.P15)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	shell := startOutside(t, nil, "/bin/sleep", "60")
	var sleeper agents.Identity
	deadline := time.Now().Add(10 * time.Second)
	for sleeper.PID == 0 {
		_, records, err := readTable()
		if err != nil {
			t.Fatal(err)
		}
		for pid, record := range records {
			if record.parent == shell && record.name == "sleep" {
				if newCandidates(self).could(records, pid) {
					t.Fatalf("the sleep %d outside this tree is judged a candidate, so this shows nothing", pid)
				}
				sleeper = record.Identity
			}
		}
		if sleeper.PID == 0 && !time.Now().Before(deadline) {
			t.Fatal("the shell outside this tree started no sleep")
		}
		time.Sleep(5 * time.Millisecond)
	}
	_, err = processesWhere(&failsMidExec{pid: sleeper.PID, always: true}, ofBinary(self), newCandidates(self))
	var unsettled *unsettledError
	if !errors.As(err, &unsettled) || unsettled.PID != sleeper.PID || unsettled.Readings != settleReadings || !errors.Is(err, syscall.EIO) {
		t.Fatalf("a listing beside the unreadable sleep %d = %v; want every reading unsettled naming it and its EIO", sleeper.PID, err)
	}
	if presence, err := sleeper.Presence(); err != nil || presence != agents.PresencePresent {
		t.Fatalf("the sleep %v was not running once the listing returned (%v, %v)", sleeper, presence, err)
	}
}

// A process that is no candidate by the table's record, but that the
// listing then read as running the binary, unsettles the reading when it
// exits before it was read whole (P15, #722): the record was read before it
// exec'd the binary, and the read after says what it is now. Here a shell
// outside this tree runs this binary again each time it ends; the table
// records each under the shell's own name, which a record read before the
// exec carries, and ends it once the listing has read its command line
// once. Without the clause that keeps that exit unsettling the listing
// settles on its first reading. The control is that every reading ended
// one.
func TestAnExitAfterAReadAsTheBinaryUnsettlesWhateverTheRecord(t *testing.T) {
	principles.Cite(t, principles.P15)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	shell := startOutside(t, []string{"STANDIN_TEST_HOLD_OPEN=1"}, self)
	record, found, err := readRecord(shell)
	if err != nil || !found {
		t.Fatalf("reading the outside shell's record: %v, %v", found, err)
	}
	table := &endsOutsideEachReading{parent: shell, name: predictedName(t, self), renamed: record.name, atRead: true}
	_, err = processesOfBinary(table, self)
	if table.ended != table.readings {
		t.Fatalf("%d of %d readings ended a process read as the binary, so this shows nothing", table.ended, table.readings)
	}
	everyReadingUnsettled(t, "a listing while a process read as the binary exits before it is read whole", err, table.readings)
}

// The name a copy of this binary is recorded under is the one recordedName
// predicts (P15, #722), run by a path whose name is longer than the table
// records and through a symbolic link of another name: a listing of either
// path lists the held process, keeps its candidates on, which it would turn
// off had the fresh record of the process it accepted carried another name,
// and the record the table holds is the predicted name.
func TestTheRecordedNameIsPredictedForALongNameAndALink(t *testing.T) {
	principles.Cite(t, principles.P15)
	long := copyOfBinary(t, "a-binary-named-past-the-table-limit.test")
	link := filepath.Join(t.TempDir(), "link.test")
	if err := os.Symlink(long, link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{long, link} {
		held := startHeld(t, path)
		judge := newCandidates(path)
		if judge == nil {
			t.Fatalf("no candidates for %s", path)
		}
		listed, err := processesWhere(kernel{}, ofBinary(path), judge)
		if err != nil || !slices.ContainsFunc(listed, func(p Process) bool { return p.PID == held.Process.Pid }) {
			t.Fatalf("a listing of %s = %v, %v; want the held process %d", path, listed, err, held.Process.Pid)
		}
		record, found, err := readRecord(held.Process.Pid)
		if err != nil || !found || record.name != judge.name || judge.off {
			t.Errorf("run by %s, the process is recorded as %q (%v, %v), predicted %q; candidates off: %v", path, record.name, found, err, judge.name, judge.off)
		}
	}
}

// A listing whose predicted name this process's own record contradicts
// judges every exit as a record-less listing does (P15, #722): with the
// prediction made wrong, outside churn a listing otherwise settles beside
// (TestAListingOfABinarySettlesWhileProcessesOutsideItsTreeExitUnread)
// keeps every reading unsettled.
// Nothing runs the binary but this process, so only the check of this
// process's record turns the candidates off; without it the listing
// settles. The control is that every reading ended an outside process.
func TestAWrongPredictionOfThisProcessesNameTurnsTheRecordsOff(t *testing.T) {
	principles.Cite(t, principles.P15)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	table := &endsOutsideEachReading{parent: startOutside(t, nil, "/bin/sleep", "60"), name: "sleep"}
	judge := newCandidates(self)
	judge.name = "not-" + judge.name
	_, err = processesWhere(table, ofBinary(self), judge)
	if table.ended != table.readings {
		t.Fatalf("%d of %d readings ended a process outside this tree, so this shows nothing", table.ended, table.readings)
	}
	everyReadingUnsettled(t, "a listing whose prediction this process's record contradicts", err, table.readings)
}

// A listing whose predicted name the fresh record of a process it accepted
// contradicts judges every exit as a record-less listing does (P15, #722):
// the listing is of a copy of this binary, which this process does not
// run, with one held process of the copy and the prediction made wrong, so
// only the check of the accepted process's record turns the candidates off,
// and the outside churn then keeps every reading unsettled; without it the
// listing settles. The control is that every reading ended an outside
// process.
func TestAWrongPredictionOfAnAcceptedProcessesNameTurnsTheRecordsOff(t *testing.T) {
	principles.Cite(t, principles.P15)
	copied := copyOfBinary(t, "accepted.test")
	startHeld(t, copied)
	table := &endsOutsideEachReading{parent: startOutside(t, nil, "/bin/sleep", "60"), name: "sleep"}
	judge := newCandidates(copied)
	judge.name = "not-" + judge.name
	_, err := processesWhere(table, ofBinary(copied), judge)
	if table.ended != table.readings {
		t.Fatalf("%d of %d readings ended a process outside this tree, so this shows nothing", table.ended, table.readings)
	}
	everyReadingUnsettled(t, "a listing whose prediction an accepted process's record contradicts", err, table.readings)
}

// recheckFails is a processTable that passes every read through to the
// kernel but three, all of pick, a live process of the binary outside this
// tree: each reading records it under renamed, the name a record read
// before its exec carries; every read of its command line fails with EIO;
// and identify, asked of it a second time in a reading, which is the check
// after the reads that it is still the process read, fails with EIO.
type recheckFails struct {
	kernel
	pick               int
	renamed            string
	asked              bool
	readings, rechecks int
}

func (r *recheckFails) ids() ([]int, map[int]tableRecord, error) {
	r.readings++
	r.asked = false
	pids, records, err := r.kernel.ids()
	if record, ok := records[r.pick]; ok {
		record.name = r.renamed
		records[r.pick] = record
	}
	return pids, records, err
}

func (r *recheckFails) commandLine(pid int) (string, []string, error) {
	if pid == r.pick {
		return "", nil, syscall.EIO
	}
	return r.kernel.commandLine(pid)
}

func (r *recheckFails) identify(pid int) (agents.Identity, bool, error) {
	if pid == r.pick {
		if r.asked {
			r.rechecks++
			return agents.Identity{}, false, syscall.EIO
		}
		r.asked = true
	}
	return r.kernel.identify(pid)
}

// A process whose identity cannot be read again once it was read is not
// taken for one that exited (P15, #722): a recheck that fails says nothing
// of whether the process runs, so it unsettles the reading whatever the
// record says. Here a live process of this binary outside this tree is
// recorded under its shell's name, as a record read before its exec is,
// so the record judges it no candidate; every read of its command line
// fails, and so does the recheck of its identity. Before, the failed
// recheck was read as the process gone, the exit of a process that is no
// candidate was passed over, and the listing settled without the live
// process. The controls are that every reading reached the recheck and
// that the process runs throughout.
func TestAFailedRecheckOfAProcessThatIsNoCandidateUnsettles(t *testing.T) {
	principles.Cite(t, principles.P15)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	shell := startOutside(t, []string{"STANDIN_TEST_HOLD_OPEN=1"}, self)
	name := predictedName(t, self)
	var pick agents.Identity
	var renamed string
	deadline := time.Now().Add(10 * time.Second)
	for pick.PID == 0 {
		_, records, err := readTable()
		if err != nil {
			t.Fatal(err)
		}
		for _, record := range records {
			if record.parent == shell && record.name == name {
				pick, renamed = record.Identity, records[shell].name
			}
		}
		if pick.PID == 0 && !time.Now().Before(deadline) {
			t.Fatal("the shell outside this tree started no process of the binary")
		}
		time.Sleep(5 * time.Millisecond)
	}
	table := &recheckFails{pick: pick.PID, renamed: renamed}
	_, err = processesOfBinary(table, self)
	if table.rechecks != table.readings {
		t.Fatalf("%d of %d readings rechecked process %d's identity, so this shows nothing", table.rechecks, table.readings, pick.PID)
	}
	var unsettled *unsettledError
	if !errors.As(err, &unsettled) || unsettled.PID != pick.PID || unsettled.Readings != settleReadings || !errors.Is(err, syscall.EIO) {
		t.Fatalf("a listing whose recheck of the live process %d fails = %v; want every reading unsettled naming it and the EIO", pick.PID, err)
	}
	if presence, err := pick.Presence(); err != nil || presence != agents.PresencePresent {
		t.Fatalf("process %v was not running once the listing returned (%v, %v)", pick, presence, err)
	}
}
