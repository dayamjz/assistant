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
// fails rather than running itself a third time.
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
// nothing. On a platform internal/ipc identifies no peer on the gate
// refuses every push before any placement, and AtTerminal returns at once.
//
// What this does not establish: a test that pushes from a process it put in
// a session of its own, with no terminal, is placed as nobody's whatever
// this did, which is how a test of the gate's refusal makes its pusher.
func AtTerminal() {
	self, found, err := agents.ReadLineage(os.Getpid())
	if err == nil && found && self.Foreground() {
		return
	}
	if os.Getenv(atTerminalVar) != "" {
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
	cmd.Env = append(os.Environ(), atTerminalVar+"=1")
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
