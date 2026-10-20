//go:build unix

package agents_test

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

// helperPoliteSignals are the signals the stand-in agent ignores in its
// stubborn modes, so that only the forceful step of the escalation ends it.
func helperPoliteSignals() []os.Signal {
	return []os.Signal{syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP}
}

// helperSelfKill ends the stand-in agent with a signal, so it reports no exit
// status of its own. That is what an agent the system kills looks like, and
// exiting instead would make this mode prove nothing, so the exit below is a
// loud failure rather than a fallback.
func helperSelfKill() {
	_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
	time.Sleep(2 * time.Second)
	os.Exit(8)
}

// helperDetach is the "detach" mode: a child in the "hold" mode, stubborn
// when this process was asked to be, started in a new session in the
// directory helperDirVar names and given this process's descriptor 3 as its
// own, so the test learns the child has ended when the last copy of that
// descriptor closes. The child's identifier is printed after Start, which
// returns once the child is running the helper.
func helperDetach() {
	self, err := os.Executable()
	if err != nil {
		os.Exit(3)
	}
	child := exec.Command(self)
	child.Env = []string{helperModeVar + "=hold", helperStderrVar + "=" + os.Getenv(helperStderrVar)}
	child.Dir = os.Getenv(helperDirVar)
	child.ExtraFiles = []*os.File{os.NewFile(3, "held")}
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := child.Start(); err != nil {
		os.Exit(4)
	}
	fmt.Print(strconv.Itoa(child.Process.Pid))
}

// processGroup is a process group led by an anchor this test started and
// does not reap until the group has been signalled. A command joined to it
// is not the group's leader, so the group's identifier is the anchor's
// process identifier, and that identifier cannot be given to another
// process while the anchor is running or unreaped: the group can be
// signalled at any point up to end, whether or not the command joined to it
// has already exited and been reaped. A group led by the command itself
// would have no such point once exec.Cmd had reaped it.
type processGroup struct {
	anchor *exec.Cmd
	ended  bool
}

// startGroup starts the anchor, in a process group of its own.
func startGroup() (*processGroup, error) {
	anchor := exec.Command("/bin/sleep", "3600")
	anchor.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := anchor.Start(); err != nil {
		return nil, fmt.Errorf("starting the process group's anchor: %w", err)
	}
	return &processGroup{anchor: anchor}, nil
}

// join starts cmd in the group and has its Cancel kill the whole group, so
// a context ending reaches what cmd started in the group and not only cmd.
func (g *processGroup) join(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: g.anchor.Process.Pid}
	cmd.Cancel = g.kill
}

// kill sends SIGKILL to every process in the group, the anchor included.
func (g *processGroup) kill() error {
	return syscall.Kill(-g.anchor.Process.Pid, syscall.SIGKILL)
}

// end kills the group and then reaps the anchor, which is the order that
// keeps the signal on this group. It is safe to call more than once. What
// it does not reach is a process that left the group, by starting a group
// or a session of its own.
func (g *processGroup) end() {
	if g.ended {
		return
	}
	g.ended = true
	_ = g.kill()
	_ = g.anchor.Wait()
}
