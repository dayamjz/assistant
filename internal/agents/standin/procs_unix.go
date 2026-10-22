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
// is read whole. A list that cannot be read is an error and never an empty
// list; a single process whose command line cannot be read, one that has
// exited or one of another user's, is passed over.
func ProcessesOf(binary string) ([]Process, error) {
	return processesWhere(func(pid int) bool {
		executable, _, err := commandLine(pid)
		return err == nil && executable == binary
	})
}

// carrying lists every process started with OwnerVar set to value in its
// environment (startEnvironment), whatever its executable, leaving out the
// same processes ProcessesOf does. It is how the owner sweep and
// leftAfterTests find a terminal server a test started, and the processes in
// its panes, which run another program and are in no group or session of the
// test's. A process whose environment cannot be read, one that has exited
// or one of another user's, is passed over, as ProcessesOf passes one over,
// and so is one whose environment reads as empty: on macOS the read finds
// none for some programs, an Apple-signed /bin/sleep among them, whatever
// they were started with.
func carrying(value string) ([]Process, error) {
	want := OwnerVar + "=" + value
	return processesWhere(func(pid int) bool {
		env, err := startEnvironment(pid)
		return err == nil && slices.Contains(env, want)
	})
}

// processesWhere lists every process match accepts, other than this
// process, its ancestors and every owner sweep, each with its identity and its
// arguments read from the kernel. A list that cannot be read is an error and
// never an empty list.
func processesWhere(match func(pid int) bool) ([]Process, error) {
	exclude := map[int]bool{}
	for pid := os.Getpid(); pid > 1 && !exclude[pid]; {
		exclude[pid] = true
		lineage, found, err := agents.ReadLineage(pid)
		if err != nil || !found {
			break
		}
		pid = lineage.Parent
	}
	pids, err := processIDs()
	if err != nil {
		return nil, fmt.Errorf("listing processes: %w", err)
	}
	var found []Process
	for _, pid := range pids {
		if exclude[pid] || !match(pid) {
			continue
		}
		_, args, err := commandLine(pid)
		if err != nil || isSweep(args) {
			continue
		}
		id, present, err := agents.Identify(pid)
		if err != nil || !present {
			continue
		}
		found = append(found, Process{Identity: id, Args: args})
	}
	return found, nil
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
// the package fails. A process list that cannot be read fails the package
// too, since a check that read nothing would pass.
//
// A process exiting can start another (a shell's EXIT trap), which the
// listing it was in does not hold, so when everything listed has exited the
// check lists again, and it passes only once a listing comes back empty.
// After that, nothing this check can see is left to start a process:
// RunOwned has this binary start nothing more first (stopStarting), and a
// process this check can see is in every listing whose reads of it succeed
// until it has exited, which is after any fork of its own has returned. A listing that still finds a
// process after sweepRounds of them fails the package, naming and ending
// what it found, so the check ends.
//
// What it does not see: a process running another executable that was
// started with an environment without OwnerVar, unless it is a tmux the
// wrapper ran (ownProcesses) or a process in that tmux's panes, or before
// ownership was recorded, or whose environment the read cannot find
// (carrying), which on macOS is every process of Apple's /bin/sh, /bin/bash,
// /bin/zsh and /bin/sleep; what such a process starts once the check has
// listed for the last time; a process passed over in the listing that came
// back empty because a read of it failed there (processesWhere), and what
// it starts; and anything at all once this binary is gone, so the binary
// AtTerminal ran again outliving a killed outer one is watchOuter's to end,
// and a terminal server outliving a killed owner the owner sweep's, and no
// check in that run reports either. A failed check ends what it names; what
// that ending starts, it leaves to the sweep.
func leftAfterTests() bool {
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: could not resolve this binary to check for processes the tests left running: %v\n", err)
		return false
	}
	for listing := 1; ; listing++ {
		listed, err := leftRunning(self)
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
// (ProcessesOf), and every process carrying this process's owner identity
// (carrying) when ownership was recorded, each once.
func leftRunning(binary string) ([]Process, error) {
	listed, err := ProcessesOf(binary)
	if err != nil {
		return nil, err
	}
	value, _ := owned()
	if value == "" {
		return listed, nil
	}
	marked, err := carrying(value)
	if err != nil {
		return nil, err
	}
	for _, p := range marked {
		if !slices.ContainsFunc(listed, func(q Process) bool { return q.Identity == p.Identity }) {
			listed = append(listed, p)
		}
	}
	return listed, nil
}
