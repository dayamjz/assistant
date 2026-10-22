//go:build linux || darwin

package standin

import (
	"errors"
	"os/exec"
	"syscall"
	"time"
)

// StartCopied calls start, which builds and starts a command whose binary
// the calling test has just written, and calls it again while the start is
// refused with ETXTBSY. On Linux a write descriptor to the copy can be held
// for a moment by a child that another parallel test forked while the copy
// was open (Go issue 22315), and exec refuses a binary open for writing. The
// retry is bounded at five seconds; any other error, or ETXTBSY past the
// bound, is returned unchanged, with the command of the last call.
//
// StartCopied starts nothing itself: start is the calling test's, so the
// program is started from test code, which internal/procgroup/guard leaves
// to the owner watch and RunOwned's check (its package comment says why).
//
// A test runs a copy of its own binary when the copy's RunOwned check must
// list the copy's processes alone: ProcessesOf matches by executable, so two
// owners started from the same file in parallel each read the other as a
// process its tests left, and end it.
func StartCopied(start func() (*exec.Cmd, error)) (*exec.Cmd, error) {
	deadline := time.Now().Add(5 * time.Second)
	for {
		cmd, err := start()
		if err == nil || !errors.Is(err, syscall.ETXTBSY) || !time.Now().Before(deadline) {
			return cmd, err
		}
		time.Sleep(20 * time.Millisecond)
	}
}
