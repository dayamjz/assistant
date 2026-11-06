//go:build darwin

package standin

import (
	"os"
	"os/exec"
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
// kernel, and names one number alone, staging.PID, with the kernel's
// records, except that the first reading's record at that number is
// staging, in place of what the kernel recorded there.
type recordedAt struct {
	kernel
	staging  tableRecord
	readings int
}

func (r *recordedAt) ids() ([]int, map[int]tableRecord, error) {
	r.readings++
	_, records, err := r.kernel.ids()
	if err != nil {
		return nil, nil, err
	}
	if r.readings == 1 {
		records[r.staging.PID] = r.staging
	}
	return []int{r.staging.PID}, records, nil
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
// is staged. The listing reads again, and the second reading, the kernel's,
// settles. Before, Identify's reading of the sleep was taken as the process
// the table named and the first reading settled. The controls are that the
// recorded process started before the sleep and that the sleep runs
// throughout.
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
	staged := ended
	staged.PID = newcomer.Process.Pid
	table := &recordedAt{staging: staged}
	if _, err := processesOfBinary(table, self); err != nil || table.readings != 2 {
		t.Fatalf("a listing whose first record at %d is an ended process of this binary = %v after %d reading(s); want the first unsettled and the second settled", newcomer.Process.Pid, err, table.readings)
	}
	if presence, err := id.Presence(); err != nil || presence != agents.PresencePresent {
		t.Fatalf("the sleep %v was not running once the listing returned (%v, %v), so this shows nothing", id, presence, err)
	}
}

// refusedNumberRecordedAgain is a processTable that passes every read
// through to the kernel, names only the processes it orders, and orders
// real processes' lives around its reads of the table, with the kernel's
// records. The first reading names a sleep, refused, which the listing reads
// whole, and an exiting sleep, which it ends once the kernel's table is read,
// so that reading meets a real unread exit and is unsettled. Before the
// second reading it ends and reaps the refused sleep, starts a process of
// this binary, reads its record, and ends and reaps it too; the second
// reading names the refused sleep's number alone, with that record staged
// at it, as if the process had been given the number and recorded there
// and then exited before it was identified. Later readings name nothing.
type refusedNumberRecordedAgain struct {
	kernel
	t                *testing.T
	refused          *exec.Cmd
	readings, staged int
	stagedStart      uint64
	refusedStart     uint64
}

func (r *refusedNumberRecordedAgain) ids() ([]int, map[int]tableRecord, error) {
	r.readings++
	switch r.readings {
	case 1:
		r.refused = startSleep(r.t)
		exiting := startSleep(r.t)
		_, records, err := r.kernel.ids()
		_ = exiting.Process.Kill()
		_ = exiting.Wait()
		r.refusedStart = records[r.refused.Process.Pid].Start
		return []int{r.refused.Process.Pid, exiting.Process.Pid}, records, err
	case 2:
		_ = r.refused.Process.Kill()
		_ = r.refused.Wait()
		staging := endedBinaryRecord(r.t)
		_, records, err := r.kernel.ids()
		if err != nil {
			return nil, nil, err
		}
		number := r.refused.Process.Pid
		staging.PID = number
		records[number] = staging
		r.staged++
		r.stagedStart = staging.Start
		return []int{number}, records, nil
	default:
		_, records, err := r.kernel.ids()
		return nil, records, err
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
// the number's reuse makes. The listing reads a third time, which names
// nothing and settles. Before, the number was taken for the refused sleep,
// its exit passed over as a refused process's, and the listing settled on
// the second reading. The controls are that the second reading met the
// staged record and that it records a start after the refused sleep's.
// Not parallel: it starts and ends real processes around its readings,
// which would keep other tests' listings from settling.
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
