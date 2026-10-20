//go:build linux || darwin

package standin

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/dayamjz/assistant/internal/agents"
)

// atTerminalVar marks a test binary AtTerminal has already run again on a
// terminal of its own, so a binary that still reads no foreground there
// fails rather than running itself a third time, and carries the identity of
// the outer binary that ran it, in encodeOwner's spelling, which is the
// owner the binary run again watches.
const atTerminalVar = "ASSISTANT_TEST_AT_TERMINAL"

// exitNotAtTerminal is a test binary that could not be given a terminal it is
// in the foreground of, and so could not send a change the way a shell at
// a terminal does.
const exitNotAtTerminal = 12

// AtTerminal gives this test binary a controlling terminal it is in the
// foreground of, when it has none, and returns when it has one. A package
// whose tests push to a gate from the test's own process calls it in
// TestMain, after the stand-in verbs and before the tests run:
//
//	func TestMain(m *testing.M) {
//		standin.Main()
//		standin.AtTerminal()
//		os.Exit(m.Run())
//	}
//
// The gate admits a push from a peer placed as a task's worker or as a
// shell at a terminal, which internal/service decides by walking the
// pushing process's parent chain for a process in the foreground of its
// controlling terminal, and refuses one placed as neither. A test's push
// is a person's push, made by a git the test binary started, so its chain
// reaches the test binary; from a developer's shell that binary stands in
// the terminal's foreground already, and this returns having read that.
// On a CI runner, under an editor's test runner, or under an agent's tool
// there is no terminal on the chain at all, and every such push would be
// refused for the chain's shape rather than for anything the test is
// about. So this runs the binary again, with the same arguments and
// streams, as the session leader of a pseudo-terminal of its own, which is
// what a shell at a terminal gives a job: the kernel puts the leader's
// group in the terminal's foreground, and the push's chain then reaches a
// process the walk places as a shell. The terminal is the real mechanism
// read by the real reader, and nothing here states a placement.
//
// The outer process does nothing but wait: it keeps the terminal's master
// side open, because closing it would hang the terminal up under the tests,
// relays what the tests write to the terminal to its own standard error,
// forwards an interrupt, a termination, a hangup and the quit signal go
// test sends on a timeout, and exits with the tests' status. The tests'
// standard output is the outer's own, so go test reads it as it would.
//
// The binary run again verifies the terminal it was given before it runs a
// test and exits exitNotAtTerminal when it still reads no foreground, naming
// what it read, rather than running the suite on a terminal that places
// nothing.
//
// The binary run again ends with the outer one by whatever path the outer
// ends. A normal exit and a forwarded signal follow from the outer waiting on
// it. A killed outer does not: the binary run again leads a session of its
// own, so a kill of the outer's group misses it. Two things reach it then,
// and both end its whole process group, the tests' stand-ins, hooks and gits
// with it, through one ending, watchOuter's. The terminal's master side
// closes with the outer and the kernel hangs the terminal up, which a binary
// run again with the hangup signal handled normally reads at once; it
// catches that signal, and the termination and interrupt signals the outer
// forwards, and kills its group rather than exiting alone, which ended it in
// #416's review leaving a child that ignores the hangup running with parent
// 1. A test binary started under nohup, or under a tool that starts its
// commands that way, has the hangup ignored, so does the binary run again,
// and the hangup ends nothing, as #414 found; so the outer hands the binary
// run again its own identity, the binary run again watches it with
// WatchOwner's watch, polling every OwnerPoll, and kills its group once the
// outer is gone. The residual window is OwnerPoll after the outer is gone
// where the hangup is ignored, plus a read of the outer's identity that
// fails, which counts as the outer running and so ends nothing. What the
// tests started outside that group is not reached by the kill: a service the
// product launched in a session of its own ends through its own owner watch
// where the package calls RunOwned and WatchOwner, and anything else the
// package's tests start that way is theirs to end. A binary run again that
// is stopped by a signal runs no watch, and one ended by the quit signal, by
// SIGKILL, or by a panic, a test timeout among them, ends alone, as
// watchOuter states. On a platform internal/ipc identifies no peer on the
// gate refuses every push before any placement, and AtTerminal returns at
// once.
//
// What this does not establish: a test that pushes from a process it put in
// a session of its own, with no terminal, is placed as nobody's whatever
// this did, which is how a test of the gate's refusal makes its pusher.
func AtTerminal() {
	outer := os.Getenv(atTerminalVar)
	if outer != "" {
		if err := watchOuter(outer); err != nil {
			fmt.Fprintf(os.Stderr, "standin: this test binary was run again on a terminal of its own, but its outer binary's "+
				"identity %s: %v, so nothing would end it with the outer\n", atTerminalVar, err)
			os.Exit(exitNotAtTerminal)
		}
	}
	self, found, err := agents.ReadLineage(os.Getpid())
	if err == nil && found && self.Foreground() {
		return
	}
	if outer != "" {
		fmt.Fprintf(os.Stderr, "standin: this test binary was run again on a terminal of its own and reads group %d, "+
			"terminal foreground group %d (found %v, error %v), so it is not in the foreground of a terminal and "+
			"cannot push a change the way a shell at a terminal does\n",
			self.Group, self.TerminalGroup, found, err)
		os.Exit(exitNotAtTerminal)
	}
	executable, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "standin: this test binary cannot resolve its own path to run itself at a terminal:", err)
		os.Exit(exitNotAtTerminal)
	}
	master, slave, err := openTerminal()
	if err != nil {
		fmt.Fprintln(os.Stderr, "standin: no pseudo-terminal could be opened for the tests to run at:", err)
		os.Exit(exitNotAtTerminal)
	}
	defer func() { _ = master.Close() }()
	cmd := exec.Command(executable, os.Args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	owner, err := selfOwner()
	if err != nil {
		fmt.Fprintln(os.Stderr, "standin: this test binary cannot hand the tests at a terminal an owner to end with:", err)
		os.Exit(exitNotAtTerminal)
	}
	cmd.Env = append(os.Environ(), atTerminalVar+"="+owner)
	// The slave is the child's descriptor 3, which Setctty names: the child
	// starts a session of its own and takes that terminal as its controlling
	// terminal, and the kernel gives the terminal's foreground to the
	// child's group.
	cmd.ExtraFiles = []*os.File{slave}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 3}
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "standin: the tests could not be started at a terminal:", err)
		os.Exit(exitNotAtTerminal)
	}
	_ = slave.Close()
	go func() { _, _ = io.Copy(os.Stderr, master) }()
	forwarded := make(chan os.Signal, 8)
	signal.Notify(forwarded, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case sig := <-forwarded:
				_ = cmd.Process.Signal(sig)
			case <-done:
				return
			}
		}
	}()
	err = cmd.Wait()
	close(done)
	signal.Stop(forwarded)
	if err == nil {
		os.Exit(0)
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() >= 0 {
		os.Exit(exit.ExitCode())
	}
	fmt.Fprintln(os.Stderr, "standin: the tests at the terminal ended without a status:", err)
	os.Exit(exitNotAtTerminal)
}
