//go:build linux || darwin

package standin

import (
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
// it read again rather than being passed over. A list that cannot be read,
// or that no reading settles for, is an error and never an empty list; a
// process whose command line cannot be read while it runs, one of another
// user's, is passed over.
func ProcessesOf(binary string) ([]Process, error) {
	return processesWhere(kernel{}, ofBinary(binary))
}

// ofBinary is ProcessesOf's match: a process whose executable is binary.
func ofBinary(binary string) func(processTable, int) bool {
	return func(table processTable, pid int) bool {
		executable, _, err := table.commandLine(pid)
		return err == nil && executable == binary
	}
}

// carrying lists every process started with OwnerVar set to value in its
// environment (startEnvironment), whatever its executable, leaving out the
// same processes ProcessesOf does. It is how the owner sweep and
// leftAfterTests find a terminal server a test started, and the processes in
// its panes, which run another program and are in no group or session of the
// test's. It is a settled reading, as ProcessesOf's is. A process whose
// environment cannot be read while it runs, one of another user's, is not
// read as carrying the identity, and neither is one whose environment reads
// as empty. On some macOS machines the read finds none for some programs,
// whatever they were started with: on the one this was written on, every
// process of Apple's /bin/sh, /bin/bash, /bin/zsh and /bin/sleep, read with
// a probe of kern.procargs2; CI's macOS runner, by contrast, read the
// environment of /bin/sh and /bin/sleep processes the tests started.
func carrying(value string) ([]Process, error) {
	return processesWhere(kernel{}, carries(value))
}

// carries is carrying's match: a process started with OwnerVar set to value.
func carries(value string) func(processTable, int) bool {
	want := OwnerVar + "=" + value
	return func(table processTable, pid int) bool {
		env, err := table.environment(pid)
		return err == nil && slices.Contains(env, want)
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
}

// kernel is the processTable the kernel answers.
type kernel struct{}

func (kernel) ids() ([]int, error) { return processIDs() }
func (kernel) identify(pid int) (agents.Identity, bool, error) {
	return agents.Identify(pid)
}
func (kernel) commandLine(pid int) (string, []string, error) { return commandLine(pid) }
func (kernel) environment(pid int) ([]string, error)         { return startEnvironment(pid) }

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
// still the same running process) or read as not running in the reading
// before too, which an exited process nobody has reaped yet is, and which
// starts nothing. A process that exits between two of those reads may have
// started another the table read before its birth does not name, so a
// reading in which one did is not settled, and processesWhere reads the
// table again, reusing what it read of a process it already read whole,
// until a reading settles or settleReadings have run, which is an error.
// So every process running when a settled reading began was read whole, as
// one match accepts or refuses, and a process that exited during an earlier
// reading after starting another has that other read whole in a later one.
//
// What that does not cover: a process whose command line cannot be read
// while it runs, one of another user's, is passed over, and so is what it
// starts unless that is read whole itself; and a process match refuses can
// start one match would accept after the reading, which carrying's match
// refuses only for a process not read as carrying the identity (carrying).
func processesWhere(table processTable, match func(processTable, int) bool) ([]Process, error) {
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
	// a process match accepts, or nil for one it refuses or passes over.
	read := map[agents.Identity]*Process{}
	stopped := map[int]bool{}
	for range settleReadings {
		pids, err := table.ids()
		if err != nil {
			return nil, fmt.Errorf("listing processes: %w", err)
		}
		var found []Process
		settled := true
		nowStopped := map[int]bool{}
		for _, pid := range pids {
			if exclude[pid] {
				continue
			}
			id, running, err := table.identify(pid)
			if err != nil {
				settled = false
				continue
			}
			if !running {
				nowStopped[pid] = true
				if !stopped[pid] {
					settled = false
				}
				continue
			}
			if p, ok := read[id]; ok {
				if p != nil {
					found = append(found, *p)
				}
				continue
			}
			matched := match(table, pid)
			_, args, cerr := table.commandLine(pid)
			if presence, err := id.Presence(); err != nil || presence != agents.PresencePresent {
				settled = false
				continue
			}
			if !matched || cerr != nil || isSweep(args) {
				read[id] = nil
				continue
			}
			p := Process{Identity: id, Args: args}
			read[id] = &p
			found = append(found, p)
		}
		if settled {
			return found, nil
		}
		stopped = nowStopped
	}
	return nil, fmt.Errorf("listing processes: no reading of the process table settled in %d, since in each a process exited before it had been read whole", settleReadings)
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
	if presence, err := p.Presence(); err != nil || presence != agents.PresencePresent {
		return false
	}
	_ = syscall.Kill(p.PID, syscall.SIGTERM)
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
// so a process that starts another and exits while the table is read makes
// the listing read it again, and the process it started is listed. A
// listing that still finds a process after sweepRounds of them fails the
// package, naming and ending what it found, so the check ends.
//
// What it does not see: a process running another executable that was
// started with an environment without OwnerVar, unless it is a tmux the
// wrapper ran (ownProcesses) or a process in that tmux's panes, or before
// ownership was recorded, or whose environment the read cannot find, which
// on some macOS machines is a whole program's (carrying); what such a
// process starts once the check has listed for the last time; a process
// whose command line cannot be read while it runs (processesWhere), and
// what it starts; and anything at all
// once this binary is gone, so the binary AtTerminal ran again outliving a
// killed outer one is watchOuter's to end, and a terminal server outliving
// a killed owner the owner sweep's, and no check in that run reports
// either. A failed check ends what it names; what that ending starts, it
// leaves to the sweep.
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
	return processesWhere(table, func(table processTable, pid int) bool {
		return byBinary(table, pid) || byIdentity(table, pid)
	})
}
