//go:build linux || darwin

package standin

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
)

// Process is a process running a given executable: its identity and its
// arguments, argv[0] first, as the kernel holds them, one string per
// argument whatever the arguments contain.
type Process struct {
	agents.Identity
	Args []string
}

// String names the process and its command line, for a failure.
func (p Process) String() string {
	return fmt.Sprintf("process %d (%s)", p.PID, strings.Join(p.Args, " "))
}

// ProcessesOf lists every process whose executable is binary, other than
// this process, its ancestors, which include the test binary AtTerminal ran
// this one from, and every owner sweep (isSweep): this process's own, and
// one an outer test binary's RunOwned started, which is a process of the
// same binary when this one is the outer's tests' and ends with the outer
// rather than with anything this one's tests did. The executable
// and the arguments are read per process from the kernel (commandLine),
// never split out of a listing, so a path or an argument with a space in it
// is read whole. The list is a settled reading of the process table
// (processesWhere), so a process that exits while the table is read makes
// it read again rather than being passed over, unless the listing had
// already read it whole and refused it. A list that cannot be read,
// or that no reading settles for, is an error and never an empty list; a
// process of another user's (foreignProcess) whose command line cannot be
// read is passed over, and one of this user's is read again (processesWhere).
func ProcessesOf(binary string) ([]Process, error) {
	return processesWhere(kernel{}, ofBinary(binary))
}

// ProcessesMatching lists every process whose arguments, argv[0] first, match
// accepts, whatever its executable, leaving out the same processes
// ProcessesOf does, from a settled reading of the process table
// (processesWhere). Its arguments are read from the kernel as ProcessesOf
// reads them, so no program is started to list them. A process of another
// user's whose command line cannot be read is passed over, one of this
// user's is read again, and a list that cannot be read is an error and never
// an empty list.
func ProcessesMatching(match func(args []string) bool) ([]Process, error) {
	return processesWhere(kernel{}, matching(match))
}

// matching is ProcessesMatching's match: a process whose arguments, argv[0]
// first, match accepts.
func matching(match func(args []string) bool) func(processTable, int) (bool, error) {
	return func(table processTable, pid int) (bool, error) {
		_, args, err := table.commandLine(pid)
		if err != nil {
			return false, err
		}
		return match(args), nil
	}
}

// ofBinary is ProcessesOf's match: a process whose executable is binary.
func ofBinary(binary string) func(processTable, int) (bool, error) {
	return func(table processTable, pid int) (bool, error) {
		executable, _, err := table.commandLine(pid)
		if err != nil {
			return false, err
		}
		return executable == binary, nil
	}
}

// carrying lists every process started with OwnerVar set to value in its
// environment (startEnvironment), whatever its executable, leaving out the
// same processes ProcessesOf does. It is how the owner sweep and
// leftAfterTests find a terminal server a test started, and the processes in
// its panes, which run another program and are in no group or session of the
// test's. It is a settled reading, as ProcessesOf's is. A process of
// another user's whose environment cannot be read is not read as carrying
// the identity, one of this user's is read again (processesWhere), and one
// whose environment reads as empty is not read as carrying it. On some
// macOS machines the read finds none for some programs, whatever they were
// started with: on the one this was written on, every
// process of Apple's /bin/sh, /bin/bash, /bin/zsh and /bin/sleep, read with
// a probe of kern.procargs2; CI's macOS runner, by contrast, read the
// environment of /bin/sh and /bin/sleep processes the tests started.
func carrying(value string) ([]Process, error) {
	return processesWhere(kernel{}, carries(value))
}

// carries is carrying's match: a process started with OwnerVar set to value.
func carries(value string) func(processTable, int) (bool, error) {
	want := OwnerVar + "=" + value
	return func(table processTable, pid int) (bool, error) {
		env, err := table.environment(pid)
		if err != nil {
			return false, err
		}
		return slices.Contains(env, want), nil
	}
}

// processTable is the kernel's process table, one method per read the
// listings make of it, so a test can have a real process exit between two
// of those reads; kernel is the real one, and a test's table passes every
// read through to it.
type processTable interface {
	ids() ([]int, error)
	identify(pid int) (agents.Identity, bool, error)
	commandLine(pid int) (string, []string, error)
	environment(pid int) ([]string, error)
	foreign(pid int) (bool, error)
	isExiting(pid int) (bool, error)
}

// kernel is the processTable the kernel answers.
type kernel struct{}

func (kernel) ids() ([]int, error) { return processIDs() }
func (kernel) identify(pid int) (agents.Identity, bool, error) {
	return agents.Identify(pid)
}
func (kernel) commandLine(pid int) (string, []string, error) { return commandLine(pid) }
func (kernel) environment(pid int) ([]string, error)         { return startEnvironment(pid) }
func (kernel) foreign(pid int) (bool, error)                 { return foreignProcess(pid) }
func (kernel) isExiting(pid int) (bool, error)               { return exitingProcess(pid) }

// settleReadings bounds how many times processesWhere reads the process
// table looking for a settled reading (processesWhere), so a listing ends
// even when processes keep exiting mid-reading on a busy machine.
const settleReadings = 64

// processesWhere lists every process match accepts, other than this
// process, its ancestors and every owner sweep, each with its identity and its
// arguments read from the kernel. A list that cannot be read is an error and
// never an empty list.
//
// The list is a settled reading of the process table: one in which every
// process the table named was either read whole while it ran (its identity,
// then what match reads, then its command line, then its identity again,
// still the same running process), or is another user's and could not be
// read, or was read as not running in the reading before too, which an
// exited process nobody has reaped yet is, and which starts nothing. A
// process that exits between two of those reads may have started another
// the table read before its birth does not name, so a reading in which one
// did is not settled. Nor is one in which a read of this user's running
// process failed: that process was not read whole, and passing it over
// would settle a list without it, as a shell read just after it was started
// once was (#549). The one exception is a process whose read failed that
// the table reads as having begun to exit (isExiting: on Linux the kernel's
// exiting flag in /proc/<pid>/stat; on Darwin it reports false), which
// starts nothing either and so counts as one read as not running. Whether a process whose read failed is another user's is
// read from the process table (foreignProcess), never assumed from the
// failure. In each
// case processesWhere reads the table again, reusing what it read of a
// process it already read whole, until a reading settles or settleReadings
// have run, which is an error naming what left the last reading unsettled.
//
// One exit does not unsettle a reading: that of a process the reading
// before read as running and as one this listing had read whole and match
// refused. What a refused process starts once a reading has begun is read
// only by a later reading whose table names it, whether the refused process
// goes on running or exits, so its exit tells the listing nothing its
// running would not. Every other process's exit does unsettle the reading
// it is first read in. Without this a reading settles only when no process
// it names exits during it, and on CI, where other packages' tests start
// and end processes beside the listing, none of 64 readings did (#569).
//
// So every process of this user's running when a settled reading began was
// read whole, as one match accepts or refuses, and a process that match
// accepted, or that was never read whole, and that exited during an earlier
// reading after starting another has that other read whole in a later one.
//
// When no reading settles, the error is an unsettledError naming the
// process number that kept the most readings in a row from settling,
// through the last, with what it kept the last of them from settling with:
// a process of this user's whose read fails in every reading is named with
// that read's error, however many other processes exit during each reading
// on a busy machine, since each of those unsettles fewer (#561).
//
// What that does not cover: a process of another user's whose command line
// cannot be read is passed over, and so is what it starts unless that is
// read whole itself; a process of this user's whose reads keep failing
// fails every listing, which is loud rather than silent; a process match
// refuses can start one match would accept once a reading has begun, which
// that reading does not list, whether the refused process goes on running
// or exits, and which carrying's match refuses only for a process not read
// as carrying the identity (carrying); a process read whole is not read
// again within a listing, so one that execs another program after it was
// read keeps what it was read as; a refused process's exit is told from
// another's by the process number the reading before read it under, so
// another process given that number, started and ended between the two
// readings, would be taken for it; and processes the listing never read
// whole that keep exiting, one in every reading, still keep every reading
// from settling.
func processesWhere(table processTable, match func(processTable, int) (bool, error)) ([]Process, error) {
	exclude := map[int]bool{}
	for pid := os.Getpid(); pid > 1 && !exclude[pid]; {
		exclude[pid] = true
		lineage, found, err := agents.ReadLineage(pid)
		if err != nil || !found {
			break
		}
		pid = lineage.Parent
	}
	// read is every process read whole so far, with what it was read as:
	// a process match accepts, or nil for one it refuses or passes over;
	// refused is those of them read whole that match refused.
	read := map[agents.Identity]*Process{}
	refused := map[agents.Identity]bool{}
	// stopped is the process numbers the last reading read as not running,
	// and wasRefused those it read as a running process in refused.
	stopped, wasRefused := map[int]bool{}, map[int]bool{}
	// causes is what kept the last reading from settling, one per process
	// number in the order the table named them, and standing is, for each
	// of those numbers, how many readings in a row through the last it kept
	// from settling.
	var causes []unsettledCause
	standing := map[int]int{}
	for range settleReadings {
		pids, err := table.ids()
		if err != nil {
			return nil, fmt.Errorf("listing processes: %w", err)
		}
		var found []Process
		causes = nil
		unsettle := func(pid int, err error) {
			causes = append(causes, unsettledCause{pid: pid, err: err})
		}
		nowStopped, nowRefused := map[int]bool{}, map[int]bool{}
		for _, pid := range pids {
			if exclude[pid] {
				continue
			}
			id, running, err := table.identify(pid)
			if err != nil {
				unsettle(pid, fmt.Errorf("process %d could not be identified: %w", pid, err))
				continue
			}
			if !running {
				nowStopped[pid] = true
				if !stopped[pid] && !wasRefused[pid] {
					unsettle(pid, fmt.Errorf("process %d read as not running for the first time", pid))
				}
				continue
			}
			if p, ok := read[id]; ok {
				if p != nil {
					found = append(found, *p)
				}
				if refused[id] {
					nowRefused[pid] = true
				}
				continue
			}
			matched, merr := match(table, pid)
			_, args, cerr := table.commandLine(pid)
			// The match reads the same record commandLine does, so when
			// both fail alike the error is told once.
			failed := errors.Join(merr, cerr)
			if merr != nil && errors.Is(merr, cerr) {
				failed = merr
			}
			foreign, ferr := false, error(nil)
			if failed != nil {
				foreign, ferr = table.foreign(pid)
			}
			if presence, err := id.Presence(); err != nil || presence != agents.PresencePresent {
				unsettle(pid, fmt.Errorf("%v exited before it had been read whole", id))
				continue
			}
			if failed != nil && ferr == nil && !foreign {
				// A process of this user's that has begun to exit starts
				// nothing, as one that has exited does, so it is read as
				// not running: CI's ubuntu runner met one whose exe link,
				// gone with its address space, could not be read in any of
				// the readings (run 40211320712).
				if exiting, err := table.isExiting(pid); err == nil && exiting {
					nowStopped[pid] = true
					if !stopped[pid] && !wasRefused[pid] {
						unsettle(pid, fmt.Errorf("process %d read as exiting for the first time, its read failing: %w", pid, failed))
					}
					continue
				}
			}
			switch {
			case failed != nil && ferr != nil:
				unsettle(pid, fmt.Errorf("process %d could not be read (%w), nor its user: %w", pid, failed, ferr))
				continue
			case failed != nil && !foreign:
				unsettle(pid, fmt.Errorf("process %d is this user's and running, and could not be read: %w", pid, failed))
				continue
			}
			if failed != nil || !matched || isSweep(args) {
				read[id] = nil
				if failed == nil && !matched {
					refused[id] = true
					nowRefused[pid] = true
				}
				continue
			}
			p := Process{Identity: id, Args: args}
			read[id] = &p
			found = append(found, p)
		}
		if len(causes) == 0 {
			return found, nil
		}
		stopped, wasRefused = nowStopped, nowRefused
		nowStanding := make(map[int]int, len(causes))
		for _, c := range causes {
			nowStanding[c.pid] = standing[c.pid] + 1
		}
		standing = nowStanding
	}
	return nil, longestStanding(causes, standing)
}

// unsettledCause is one process that kept a reading of the process table
// from settling (processesWhere), and why.
type unsettledCause struct {
	pid int
	err error
}

// unsettledError is processesWhere's error when no reading of the process
// table settled within settleReadings. It names, of the processes that kept
// the last reading from settling, the one whose number kept the most
// readings in a row through the last from settling, and the first the
// table named of those that tie.
type unsettledError struct {
	// PID is the process number named.
	PID int
	// Readings is how many readings in a row, through the last, PID kept
	// from settling: settleReadings when it kept every one of them.
	Readings int
	// Others is how many other processes kept the last reading from
	// settling.
	Others int
	// Err is why PID kept the last reading from settling.
	Err error
}

// Error names the process, why it kept the last reading from settling, how
// many readings in a row it did, and how many others kept the last one from
// settling too.
func (e *unsettledError) Error() string {
	others := ""
	switch {
	case e.Others == 1:
		others = ", and 1 other process kept the last from settling too"
	case e.Others > 1:
		others = fmt.Sprintf(", and %d other processes kept the last from settling too", e.Others)
	}
	return fmt.Sprintf("listing processes: no reading of the process table settled in %d; %v, which kept each of the last %d from settling%s",
		settleReadings, e.Err, e.Readings, others)
}

// Unwrap is why the named process kept the last reading from settling.
func (e *unsettledError) Unwrap() error { return e.Err }

// longestStanding is the unsettledError for causes, the last reading's, in
// the order the table named them, given standing, how many readings in a
// row through the last each process number in causes kept from settling.
func longestStanding(causes []unsettledCause, standing map[int]int) *unsettledError {
	named := causes[0]
	for _, c := range causes[1:] {
		if standing[c.pid] > standing[named.pid] {
			named = c
		}
	}
	return &unsettledError{PID: named.pid, Readings: standing[named.pid], Others: len(causes) - 1, Err: named.err}
}

// isSweep reports whether args, argv[0] first, are an owner sweep's, which
// startSweep starts with sweepFlag as its first argument. No test starts a
// sweep: its owner's RunOwned does, and ends it itself once its check passes
// (retireSweep); otherwise it outlives the tests by design and ends once its
// owner is gone and it has swept. So no check counts one as left behind;
// what that does not cover is a sweep that never ends, whose owner's
// identity reads as present forever (sweep), which no check reports.
func isSweep(args []string) bool {
	return len(args) > 1 && strings.HasPrefix(args[1], sweepFlag)
}

// End sends p SIGTERM and waits up to EndGrace for it to go, then sends
// SIGKILL to its group when it leads one and to it otherwise. Before each
// signal the identity is checked to still name the running process, which
// narrows the chance of signalling a later process given the same number to
// the moment between that check and the signal; it does not close it. It
// reports whether p had to be killed.
func (p Process) End() bool {
	if !p.signal(syscall.SIGTERM) {
		return false
	}
	return p.killUnlessGone()
}

// signal sends p sig once its identity has been read to still name the
// running process, End's check before each signal, and reports whether it
// read so and sent sig; a process read as gone, or whose identity could not
// be read, is sent nothing.
func (p Process) signal(sig syscall.Signal) bool {
	if presence, err := p.Presence(); err != nil || presence != agents.PresencePresent {
		return false
	}
	_ = syscall.Kill(p.PID, sig)
	return true
}

// killUnlessGone is the rest of End once p has been sent SIGTERM: it waits
// up to EndGrace for p to go, then sends SIGKILL to its group when it leads
// one and to it otherwise, after the same check of its identity, and
// reports whether p had to be killed.
func (p Process) killUnlessGone() bool {
	if p.GoneWithin(EndGrace) {
		return false
	}
	if presence, err := p.Presence(); err != nil || presence != agents.PresencePresent {
		return false
	}
	if group, err := syscall.Getpgid(p.PID); err == nil && group == p.PID {
		_ = syscall.Kill(-group, syscall.SIGKILL)
	} else {
		_ = syscall.Kill(p.PID, syscall.SIGKILL)
	}
	p.GoneWithin(EndGrace)
	return true
}

// GoneWithin reports whether p is no longer running within wait.
func (p Process) GoneWithin(wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for {
		if presence, err := p.Presence(); err == nil && presence != agents.PresencePresent {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Stayed is every process of binary that match accepts and that is still
// running grace after it was listed, which is the question leftAfterTests
// asks of each of its listings, asked once of a subset, so a test can ask it
// of the processes it started while other tests' processes run.
func Stayed(binary string, match func(Process) bool, grace time.Duration) ([]Process, error) {
	listed, err := ProcessesOf(binary)
	if err != nil {
		return nil, err
	}
	return stayed(slices.DeleteFunc(listed, func(p Process) bool { return !match(p) }), grace), nil
}

// stayed is every process in ps still running grace after the call, each
// given the same grace at once rather than one after another.
func stayed(ps []Process, grace time.Duration) []Process {
	deadline := time.Now().Add(grace)
	var left []Process
	for _, p := range ps {
		if !p.GoneWithin(time.Until(deadline)) {
			left = append(left, p)
		}
	}
	return left
}

// leftAfterTests is the check RunOwned runs once the tests have returned,
// and its result decides the package's status with the tests' own. Every
// process still running this binary is then one the tests started and did
// not end: a service, a stand-in agent or hook, or a test binary AtTerminal
// ran again on behalf of a test that started this binary as an outer one. So
// is every process started with this process's owner identity in its
// environment, whatever it runs, a terminal server and what runs in its
// panes among them. It gives each EndGrace to finish exiting, then names
// every one still running on standard error, ends it, and reports false, so
// the package fails. A process list that cannot be read, or for which no
// reading of the process table settles (processesWhere), fails the package
// too, since a check that read nothing would pass.
//
// A process exiting can start another (a shell's EXIT trap), which the
// listing it was in does not hold, so when everything listed has exited the
// check lists again, and it passes only once a listing comes back empty.
// After that, nothing this check can see is left to start a process:
// RunOwned has this binary start nothing more first (stopStarting), and
// each listing is one settled reading of the process table (leftRunning),
// so a process of this binary, or one carrying the owner identity, that
// starts another and exits while the table is read makes the listing read
// it again, and the process it started is listed. A
// listing that still finds a process after sweepRounds of them fails the
// package, naming and ending what it found, so the check ends.
//
// What it does not see: a process running another executable that was
// started with an environment without OwnerVar, unless it is a tmux the
// wrapper ran (ownProcesses) or a process in that tmux's panes, or before
// ownership was recorded, or whose environment the read cannot find, which
// on some macOS machines is a whole program's (carrying); what such a
// process starts once the check's last listing has begun, whether it goes
// on running or exits (processesWhere); a process of
// another user's whose command line cannot be read (processesWhere), and
// what it starts; and anything at all
// once this binary is gone, so the binary AtTerminal ran again outliving a
// killed outer one is watchOuter's to end, and a terminal server outliving
// a killed owner the owner sweep's, and no check in that run reports
// either. A failed check ends what it names as one set (endAll), so each
// process it names has been sent its own SIGTERM before any child of its it
// names is sent one; what that ending starts all the same, within what
// endAll does not cover, it leaves to the sweep.
func leftAfterTests() bool {
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: could not resolve this binary to check for processes the tests left running: %v\n", err)
		return false
	}
	for listing := 1; ; listing++ {
		value, _ := owned()
		listed, err := leftRunning(kernel{}, self, value)
		if err != nil {
			fmt.Fprintf(os.Stderr, "FAIL: could not check for processes the tests left running: %v\n", err)
			return false
		}
		if len(listed) == 0 {
			return true
		}
		if listing == sweepRounds {
			for _, p := range listed {
				fmt.Fprintf(os.Stderr, "FAIL: %s was started by a process this package's tests left, and processes were still being started %d listings after the tests finished\n", p, sweepRounds)
			}
			endAll(listed)
			return false
		}
		if left := stayed(listed, EndGrace); len(left) > 0 {
			for _, p := range left {
				fmt.Fprintf(os.Stderr, "FAIL: %s was started by this package's tests and was still running %v after they finished\n", p, EndGrace)
			}
			endAll(left)
			return false
		}
	}
}

// leftRunning is one listing of leftAfterTests: every process of binary
// (ProcessesOf), and every process carrying the owner identity value
// (carrying) when it is not empty, from one settled reading of the process
// table (processesWhere), so no process is missed for having been started
// between the reading that would list it by its executable and the one
// that would list it by its environment.
func leftRunning(table processTable, binary, value string) ([]Process, error) {
	byBinary := ofBinary(binary)
	if value == "" {
		return processesWhere(table, byBinary)
	}
	byIdentity := carries(value)
	return processesWhere(table, func(table processTable, pid int) (bool, error) {
		executes, berr := byBinary(table, pid)
		if berr == nil && executes {
			return true, nil
		}
		carries, ierr := byIdentity(table, pid)
		if ierr == nil && carries {
			return true, nil
		}
		return false, errors.Join(berr, ierr)
	})
}
