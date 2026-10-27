//go:build linux || darwin

package standin

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/agents"
)

// A process a test has a shell hold until it is released is this binary
// started with heldFlag, never a shell loop: #432 found 23 loops waiting on
// a release file left running with parent 1 on macOS, and #440 found the
// same loop in internal/cli. A loop is /bin/sh, whose start environment
// some macOS machines do not show (carrying), so there neither the owner
// sweep nor the check RunOwned runs could see it, and nothing ended it once
// its test binary was killed, timed out, or failed before the release. The
// held process is started through env with the owner identity assigned in
// its environment (HeldWait), so its arguments are only this package's
// flags; it ends with the owner that variable names (WatchOwner, which a
// consuming TestMain arms before Main), and it is a process of this binary,
// which the check lists by executable.
//
// The property HeldWait needs is that the held process ends with an owner
// that will look for it: the test binary whose RunOwned check follows the
// test that started it. So the owner it names is this process, as
// RunOwned recorded it, and it refuses unless RunOwned's run is running in
// this process (requireOwned). An inherited OwnerVar is no proof of that:
// a test binary another package's tests started inherits it without
// running RunOwned, and a held process naming that outer owner would
// outlive the inner binary unreported by any check of its own.
//
// A test that has to end a held process itself asks it to record its own
// identity (HeldWaitIdentified) and reads that back (ReadHeldIdentity), so
// what it signals is the process the held process read itself to be, start
// time included, and never a bare identifier read later, which may by then
// name another process.
//
// What it does not cover: whatever the shell runs after the held process
// returns is the shell's and not this binary's, so a command released into
// runs under no owner watch of its own and ends only when it ends; and the
// shell that started the held process waits on it as /bin/sh, invisible to
// the check, until the held process exits.

// heldFlag is the argument that makes this binary the held process, followed
// by the file whose existence releases it. heldIdentityFlag, as an optional
// second argument, names the file the held process records its identity in
// before it waits.
const (
	heldFlag         = "--standin-held="
	heldIdentityFlag = "--standin-held-identity="
)

// heldMain makes this process, started with heldFlag FILE as its first
// argument and optionally heldIdentityFlag PATH as its second, the held
// process: it records its identity at PATH when given one, then exits 0
// once FILE exists, and otherwise runs until it is killed or its owner is
// gone, either of which is a nonzero exit. Without OwnerVar, or when it
// cannot record an identity it was asked for, it refuses at once with exit
// 2. It returns at once in every process not started for that.
func heldMain(args []string) {
	if len(args) != 1 && len(args) != 2 {
		return
	}
	file, ok := strings.CutPrefix(args[0], heldFlag)
	if !ok {
		return
	}
	identity := ""
	if len(args) == 2 {
		if identity, ok = strings.CutPrefix(args[1], heldIdentityFlag); !ok || identity == "" {
			return
		}
	}
	if file == "" || os.Getenv(OwnerVar) == "" {
		fmt.Fprintf(os.Stderr, "standin: a held process needs a release file and %s, so nothing would end this one with its test\n", OwnerVar)
		os.Exit(2)
	}
	if identity != "" {
		if err := recordHeldIdentity(identity); err != nil {
			fmt.Fprintf(os.Stderr, "standin: a held process could not record its identity: %v\n", err)
			os.Exit(2)
		}
	}
	for {
		if _, err := os.Stat(file); err == nil {
			os.Exit(0)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// recordHeldIdentity writes this process's identity, as agents.Identify
// reads it, to path, whole or not at all.
func recordHeldIdentity(path string) error {
	id, found, err := agents.Identify(os.Getpid())
	if err != nil {
		return err
	}
	if !found || id.Start == 0 {
		return errors.New("this process's start time could not be read")
	}
	line := strconv.Itoa(id.PID) + " " + strconv.FormatUint(id.Start, 10) + "\n"
	return writeWhole(path, []byte(line))
}

// HeldWait is the shell command that runs this binary as the held process
// until file exists, given the owner identity explicitly so that no
// environment a worker or a shell builds can drop it, and that exits
// nonzero when the held process was ended rather than released, so a shell
// that goes on with && after it goes no further. It fails tb unless
// RunOwned's run is running in this process, whatever OwnerVar holds. The
// held process only ends with its owner when the package's TestMain calls
// WatchOwner and then Main, which every RunOwned package's does.
func HeldWait(tb testing.TB, file string) string {
	tb.Helper()
	return heldCommand(tb, heldFlag+file)
}

// HeldWaitIdentified is HeldWait for a held process that first records its
// own identity, process identifier and start time, at identity, which
// ReadHeldIdentity reads. A held process that cannot record it exits 2
// before it waits, so a shell going on with && goes no further.
func HeldWaitIdentified(tb testing.TB, file, identity string) string {
	tb.Helper()
	return heldCommand(tb, heldFlag+file, heldIdentityFlag+identity)
}

// heldCommand is the command line HeldWait and HeldWaitIdentified build.
func heldCommand(tb testing.TB, args ...string) string {
	tb.Helper()
	owner := requireOwned(tb, "a held process")
	self, err := os.Executable()
	if err != nil {
		tb.Fatal(err)
	}
	words := []string{"env", OwnerVar + "=" + shellWord(owner), shellWord(self)}
	for _, a := range args {
		words = append(words, shellWord(a))
	}
	return strings.Join(words, " ")
}

// ReadHeldIdentity reads the identity a held process HeldWaitIdentified
// started recorded at path. It returns an error when nothing is recorded
// there or the record does not hold an identifier and a start time, and
// never a partial identity: a caller that gets an error has nothing it may
// signal. A Process built from what it returns signals only while that
// identifier still has that start time (Process.End), so a held process
// that exited and whose identifier was reused is not signalled.
func ReadHeldIdentity(path string) (agents.Identity, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return agents.Identity{}, err
	}
	fields := strings.Fields(string(raw))
	if len(fields) != 2 {
		return agents.Identity{}, fmt.Errorf("%s holds %q, not an identifier and a start time", path, raw)
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil || pid <= 0 {
		return agents.Identity{}, fmt.Errorf("%s holds %q, whose identifier is not a process's", path, raw)
	}
	start, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil || start == 0 {
		return agents.Identity{}, fmt.Errorf("%s holds %q, with no start time", path, raw)
	}
	return agents.Identity{PID: pid, Start: start}, nil
}

// shellWord renders s as one POSIX shell word.
func shellWord(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
