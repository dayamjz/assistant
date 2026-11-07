//go:build darwin

package standin

import (
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

// readingOf is one reading of a process table that holds just the processes
// named and nothing else: the kernel's own record of each of live, every
// record in staged at its own number in place of the kernel's, and a number
// list naming exactly the records the reading holds, in the order the
// kernel's table names them. That is how readTable builds the two, from the
// one kern.proc.all read, and every fake in this file that stages a record
// answers through this.
//
// The one liberty the rows below take is the table's size, and the number
// list and the record map name the same processes, so no process is hidden
// from the identify loop while its record stays in reach of the checks that
// read records. A reading whose list was a subset of its records would be
// the shape readTable cannot make, and is the finding that brought this
// helper (#804's round 2, and the third of that class in this file).
//
// The machine's whole table cannot serve these rows. On this part's rule any
// process outside this tree that exits unread unsettles a reading, which is
// #722 itself, so over the whole table a reading is unsettled whatever a row
// stages and no row could tell its staged cause from the machine's. That is
// measured, not assumed: with the whole table named, deleting the in-reading
// reuse check left TestAListingSeesARecordedProcessExitWhileItsNumberIsReused
// passing, because the machine's own churn had unsettled the first reading.
// Part 2 is what makes a listing settle under that churn, and its own rows
// name the whole table.
func readingOf(t *testing.T, live []int, staged ...tableRecord) ([]int, map[int]tableRecord, error) {
	t.Helper()
	order, all, err := readTable()
	if err != nil {
		return nil, nil, err
	}
	records := make(map[int]tableRecord, len(live)+len(staged))
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
