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
