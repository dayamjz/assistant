//go:build darwin

package standin

import (
	"errors"
	"os"
	"os/exec"
	"slices"
	"testing"

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
// number and recorded there. The first reading, which has no sleep before
// it, starts a second sleep beside its own and ends that one once the
// kernel's table is read, so the reading meets a real unread exit and is
// unsettled.
type refusedNumberRecordedAgain struct {
	kernel
	t                *testing.T
	staging          tableRecord
	current          *exec.Cmd
	readings, staged int
}

func (r *refusedNumberRecordedAgain) ids() ([]int, map[int]tableRecord, error) {
	r.readings++
	previous := r.current
	r.current = startSleep(r.t)
	var exiting *exec.Cmd
	if previous == nil {
		exiting = startSleep(r.t)
	}
	if previous != nil {
		_ = previous.Process.Kill()
		_ = previous.Wait()
	}
	pids, records, err := r.kernel.ids()
	if err != nil {
		return nil, nil, err
	}
	if previous == nil {
		_ = exiting.Process.Kill()
		_ = exiting.Wait()
		return pids, records, nil
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
	_, err = processesOfBinary(table, self)
	if table.staged != table.readings-1 {
		t.Fatalf("%d of the %d readings after the first met the staged record, so this shows nothing", table.staged, table.readings-1)
	}
	var unsettled *unsettledError
	if !errors.As(err, &unsettled) || table.readings != settleReadings {
		t.Fatalf("a listing whose refused process numbers are recorded for an ended process of this binary = %v after %d reading(s); want all %d unsettled", err, table.readings, settleReadings)
	}
}
