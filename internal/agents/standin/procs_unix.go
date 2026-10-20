//go:build linux || darwin

package standin

import (
	"fmt"
	"os"
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
// this process and its ancestors, which include the test binary AtTerminal
// ran this one from. The executable and the arguments are read per process
// from the kernel (commandLine), never split out of a listing, so a path or
// an argument with a space in it is read whole. A list that cannot be read is
// an error and never an empty list; a single process whose command line
// cannot be read, one that has exited or one of another user's, is passed
// over.
func ProcessesOf(binary string) ([]Process, error) {
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
		if exclude[pid] {
			continue
		}
		executable, args, err := commandLine(pid)
		if err != nil || executable != binary {
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
// running grace after it was listed, which is LeftAfterTests' question asked
// of a subset, so a test can ask it of the processes it started while other
// tests' processes run.
func Stayed(binary string, match func(Process) bool, grace time.Duration) ([]Process, error) {
	listed, err := ProcessesOf(binary)
	if err != nil {
		return nil, err
	}
	var left []Process
	for _, p := range listed {
		if match(p) && !p.GoneWithin(grace) {
			left = append(left, p)
		}
	}
	return left, nil
}

// LeftAfterTests is the check a package's TestMain runs once m.Run has
// returned, and its result decides the package's status with the tests'
// own:
//
//	code := m.Run()
//	if !standin.LeftAfterTests() && code == 0 {
//		code = 1
//	}
//
// Every process still running this binary is then one the tests started and
// did not end: a service, a stand-in agent or hook, or a test binary
// AtTerminal ran again on behalf of a test that started this binary as an
// outer one. It gives each EndGrace to finish exiting, then names every one
// still running on standard error, ends it, and reports false, so the
// package fails and leaves nothing behind. A process list that cannot be
// read fails the package too, since a check that read nothing would pass.
//
// What it does not see: a process running another executable, such as a git
// or a terminal server, and anything at all once this binary is gone, so the
// binary AtTerminal ran again outliving a killed outer one is watchOuter's to
// end and no check in that run reports it.
func LeftAfterTests() bool {
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: could not resolve this binary to check for processes the tests left running: %v\n", err)
		return false
	}
	left, err := Stayed(self, func(Process) bool { return true }, EndGrace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: could not check for processes the tests left running: %v\n", err)
		return false
	}
	if len(left) == 0 {
		return true
	}
	for _, p := range left {
		fmt.Fprintf(os.Stderr, "FAIL: %s was started by this package's tests and was still running %v after they finished\n", p, EndGrace)
	}
	for _, p := range left {
		p.End()
	}
	return false
}
