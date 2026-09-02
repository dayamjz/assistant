//go:build windows

package agents

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

// ctrlBreakEvent is CTRL_BREAK_EVENT, the console control event that can be
// addressed at a process group. It is spelled out here rather than taken from
// syscall so this file states what it sends.
const ctrlBreakEvent = 1

// createNewProcessGroup is CREATE_NEW_PROCESS_GROUP. The agent becomes the
// root of a new process group, which is what makes a console control event
// addressable at the tree rather than at one process.
const createNewProcessGroup = 0x00000200

// kernel32 and procGenerateConsoleCtrlEvent reach the console control event
// this file sends. It is not exposed by the standard syscall package on this
// platform, and loading it here keeps this package free of a dependency added
// for one call.
var (
	kernel32                     = syscall.NewLazyDLL("kernel32.dll")
	procGenerateConsoleCtrlEvent = kernel32.NewProc("GenerateConsoleCtrlEvent")
)

// setProcessGroup puts the agent at the root of a new process group.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
}

// terminateTree ends the process tree the invocation started, escalating from
// a console control event addressed at the group to a forceful tree kill after
// grace.
//
// What this platform gives up is worth stating rather than implying away.
// There is no cheap way here to ask whether anything is left in the group, so
// termination happens only while the leader is known to be running. A
// descendant that outlives a leader which exited on its own is not ended here;
// the identity-based sweep seam on Runner is the answer to that, on every
// platform.
func terminateTree(proc *os.Process, grace time.Duration, leaderRunning bool, stopped <-chan struct{}) {
	if proc == nil || !leaderRunning {
		return
	}
	pid := proc.Pid
	_ = sendCtrlBreak(pid)
	if waitStopped(stopped, grace) {
		return
	}
	// taskkill /T ends the tree rather than the one process, which is what
	// this call is for; falling back to killing the leader alone is strictly
	// less than that and is said so here rather than reported as success.
	if err := exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(pid)).Run(); err != nil {
		_ = proc.Kill()
	}
}

// sendCtrlBreak addresses a console control event at the group rooted at pid.
func sendCtrlBreak(pid int) error {
	r, _, err := procGenerateConsoleCtrlEvent.Call(uintptr(ctrlBreakEvent), uintptr(pid))
	if r == 0 {
		return err
	}
	return nil
}

// waitStopped reports whether the process finished within grace. A nil channel
// means the caller has no signal to offer, so the full grace period elapses.
func waitStopped(stopped <-chan struct{}, grace time.Duration) bool {
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-stopped:
		return true
	case <-timer.C:
		return false
	}
}
