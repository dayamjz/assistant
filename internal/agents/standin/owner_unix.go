//go:build linux || darwin

package standin

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
)

// A process a test starts can outlive the test binary that started it: a
// service the product launches in a session of its own, by design, and the
// test binary AtTerminal runs again as the leader of a session of its own.
// Neither macOS nor Linux ends a process when its owner dies (Linux's
// parent-death signal reaches only an immediate parent, and a restart's
// successor is not the child of the test binary that owns it), so this file
// is the one owner watch both rest on: an owned process carries its owner's
// identity, polls for it, and ends once it is gone. It also holds the check a
// package runs once its tests have returned, LeftAfterTests, which fails the
// package naming every process of its binary still running.

// OwnerVar names the environment variable carrying the identity of the test
// binary that owns a process this binary runs as. OwnProcesses sets it in the
// process that runs the tests, so whatever the tests start inherits it,
// including a service the product launches and the successor a restart
// launches, and WatchOwner reads it.
const OwnerVar = "ASSISTANT_TEST_OWNER"

// OwnerPoll is how often an owned process looks for its owner, and
// OwnerGrace how long WatchOwner gives a process's own graceful exit, on
// SIGTERM, before it kills its process group.
const (
	OwnerPoll  = 250 * time.Millisecond
	OwnerGrace = 10 * time.Second
)

// EndGrace is how long Process.End and LeftAfterTests give a process of this
// binary to exit on SIGTERM before they kill it.
const EndGrace = 10 * time.Second

// encodeOwner is the one spelling of an owner's identity in an environment
// variable, which parseOwner reads.
func encodeOwner(id agents.Identity) string {
	return strconv.Itoa(id.PID) + ":" + strconv.FormatUint(id.Start, 10)
}

// parseOwner reads an identity encodeOwner wrote, and refuses anything else.
func parseOwner(value string) (agents.Identity, error) {
	pid, start, ok := strings.Cut(value, ":")
	var owner agents.Identity
	var perr, serr error
	owner.PID, perr = strconv.Atoi(pid)
	owner.Start, serr = strconv.ParseUint(start, 10, 64)
	if !ok || perr != nil || serr != nil || owner.PID <= 0 {
		return agents.Identity{}, fmt.Errorf("%q is not a process identity", value)
	}
	return owner, nil
}

// selfOwner is this process's identity, encoded for an owned process.
func selfOwner() (string, error) {
	self, found, err := agents.Identify(os.Getpid())
	if err != nil || !found {
		return "", fmt.Errorf("reading this test binary's own identity (found %v): %w", found, err)
	}
	return encodeOwner(self), nil
}

// awaitGone returns once owner is read as no longer running, polling every
// OwnerPoll. A read that fails counts as the owner still running, so a
// process is never ended on an identity it could not read.
func awaitGone(owner agents.Identity) {
	for {
		time.Sleep(OwnerPoll)
		if presence, err := owner.Presence(); err == nil && presence != agents.PresencePresent {
			return
		}
	}
}

// OwnProcesses records this process as the owner of every process the tests
// start. A package calls it in TestMain after AtTerminal, so the owner is the
// binary that runs the tests.
func OwnProcesses() error {
	value, err := selfOwner()
	if err != nil {
		return err
	}
	return os.Setenv(OwnerVar, value)
}

// WatchOwner has this process end once the test binary named by OwnerVar is
// gone, and does nothing when the variable is unset; a value that does not
// parse ends this process at once with a message, since nothing would end it
// with its owner. A package whose tests start this binary as a lasting
// process calls it first thing in TestMain. The process is first sent
// SIGTERM, which a service's own shutdown handles, and then, OwnerGrace
// later, SIGKILL, to its whole group when it leads one, which a launched
// service does, since the launch gives it a session of its own.
//
// What it does not cover: an owner read as present that is gone is noticed
// within OwnerPoll, so a process outlives its owner by up to that plus
// OwnerGrace; a process stopped by a signal runs no watcher; and what this
// process started in a session or group of its own, a terminal server for
// one, is reached by neither signal and is left to the service's own
// shutdown.
func WatchOwner() {
	value := os.Getenv(OwnerVar)
	if value == "" {
		return
	}
	owner, err := parseOwner(value)
	if err != nil {
		fmt.Fprintf(os.Stderr, "standin: the test binary's owner %s: %v, so nothing would end this process with it\n", OwnerVar, err)
		os.Exit(1)
	}
	go func() {
		awaitGone(owner)
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
		time.Sleep(OwnerGrace)
		if group, err := syscall.Getpgid(0); err == nil && group == os.Getpid() {
			_ = syscall.Kill(-group, syscall.SIGKILL)
		}
		_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
	}()
}

// watchOuter has the test binary AtTerminal ran again end, with its whole
// process group, once the outer binary named by value is gone. It is
// WatchOwner's watch with a different ending: the binary run again is a
// test binary with nobody left to report to, not a service with a shutdown
// of its own to run, so it kills its group at once rather than asking
// first, and everything the tests started in that group goes with it.
func watchOuter(value string) error {
	outer, err := parseOwner(value)
	if err != nil {
		return err
	}
	go func() {
		awaitGone(outer)
		if group, err := syscall.Getpgid(0); err == nil && group == os.Getpid() {
			_ = syscall.Kill(-group, syscall.SIGKILL)
		}
		_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
	}()
	return nil
}
