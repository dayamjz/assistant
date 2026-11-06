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
// printed the background shell's number.
const outsideLoop = `stop=$1; shift; end=$(( $(date +%s) + 120 ))
( while [ ! -e "$stop" ] && [ "$(date +%s)" -lt "$end" ]; do "$@" & wait $!; done ) </dev/null >/dev/null 2>&1 &
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
	table := &endsOutsideEachReading{parent: startOutside(t, nil, "/bin/sleep", "60")}
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

// recordedAt is a processTable that passes every read through to the
// kernel, except that each reading's records hold staging at its own number
// in place of what the kernel recorded there, and the table names that
// number when the kernel did not.
type recordedAt struct {
	kernel
	staging  tableRecord
	readings int
}

func (r *recordedAt) ids() ([]int, map[int]tableRecord, error) {
	r.readings++
	pids, records, err := r.kernel.ids()
	if err != nil {
		return nil, nil, err
	}
	if !slices.Contains(pids, r.staging.PID) {
		pids = append(pids, r.staging.PID)
	}
	records[r.staging.PID] = r.staging
	return pids, records, nil
}

// A process the table recorded at a number, and that exited during the
// reading while another was given its number, is seen to exit (P15, #722):
// a reading whose record of a process of this binary is not the process
// Identify then reads at that number is unsettled, since the recorded one
// exited unread and may have started another. Here the table holds, at a
// live sleep's number, a record of a process of this binary that has ended;
// both are real records, read by readTable from the kernel, and only their
// pairing at one number is staged, which is the shape a number given to
// another process between the table's read and Identify's makes. Every
// reading meets it. Before, Identify's reading of the sleep was taken as
// the process the table named, the sleep was read whole and refused, and
// the listing settled. The control is that the sleep runs throughout, under
// a start time other than the record's.
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
	if err != nil || !running || id.Start == ended.Start {
		t.Fatalf("the sleep reads %v, %v, %v beside the ended record %v, so the two are not told apart", id, running, err, ended.Identity)
	}
	staged := ended
	staged.PID = newcomer.Process.Pid
	table := &recordedAt{staging: staged}
	_, err = processesOfBinary(table, self)
	var unsettled *unsettledError
	if !errors.As(err, &unsettled) || unsettled.PID != newcomer.Process.Pid || unsettled.Readings != settleReadings {
		t.Fatalf("a listing whose record at %d is an ended process of this binary = %v after %d reading(s); want every reading unsettled naming %d", newcomer.Process.Pid, err, table.readings, newcomer.Process.Pid)
	}
	if presence, err := id.Presence(); err != nil || presence != agents.PresencePresent {
		t.Fatalf("the sleep %v was not running once the listing returned (%v, %v), so this shows nothing", id, presence, err)
	}
}

// refusedNumberRecordedAgain is a processTable that passes every read
// through to the kernel and orders real processes' lives around its reads
// of the table: each reading starts a sleep, which the listing reads whole
// and refuses, and ends and reaps the one the reading before started before
// the kernel's table is read; that reading's records then hold, at the
// ended sleep's number, staging, as if another process had been given the
// number and recorded there. The first reading names gone instead, a number
// with no record that identify reads as not running, so it is unsettled.
type refusedNumberRecordedAgain struct {
	kernel
	t                *testing.T
	staging          tableRecord
	gone             int
	current          *exec.Cmd
	readings, staged int
}

func (r *refusedNumberRecordedAgain) ids() ([]int, map[int]tableRecord, error) {
	r.readings++
	previous := r.current
	r.current = startSleep(r.t)
	if previous != nil {
		_ = previous.Process.Kill()
		_ = previous.Wait()
	}
	pids, records, err := r.kernel.ids()
	if err != nil {
		return nil, nil, err
	}
	if previous == nil {
		return append(pids, r.gone), records, nil
	}
	number := previous.Process.Pid
	if !slices.Contains(pids, number) {
		pids = append(pids, number)
	}
	staged := r.staging
	staged.PID = number
	records[number] = staged
	r.staged++
	return pids, records, nil
}

func (r *refusedNumberRecordedAgain) identify(pid int) (agents.Identity, bool, error) {
	if pid == r.gone {
		return agents.Identity{}, false, nil
	}
	return r.kernel.identify(pid)
}

// A process number that a reading read as a refused process does not pass
// over another process the next reading records at that number (P15, #722):
// a refused process's exit is told by its record's identity, not its
// number, so one of this binary's given the number between two readings,
// that starts another and exits before it is identified, unsettles the
// reading it is recorded in. Here each reading's refused sleep is ended
// before the next reading, and the next reading's record at its number is
// one of a process of this binary that has ended, a real record read by
// readTable; only the pairing is staged, the shape the number's reuse makes.
// Before, the number was taken for the refused sleep, its exit passed over
// as a refused process's, and the listing settled on the second reading.
// The control is that every reading after the first met the staged record.
func TestARefusedProcessNumberRecordedForAnotherIsNotPassedOver(t *testing.T) {
	t.Parallel()
	principles.Cite(t, principles.P15)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	table := &refusedNumberRecordedAgain{t: t, staging: endedBinaryRecord(t)}
	table.gone = newGoneNumbers(80_001).take()
	_, err = processesOfBinary(table, self)
	if table.staged != table.readings-1 {
		t.Fatalf("%d of the %d readings after the first met the staged record, so this shows nothing", table.staged, table.readings-1)
	}
	var unsettled *unsettledError
	if !errors.As(err, &unsettled) || table.readings != settleReadings {
		t.Fatalf("a listing whose refused process numbers are recorded for an ended process of this binary = %v after %d reading(s); want all %d unsettled", err, table.readings, settleReadings)
	}
}
