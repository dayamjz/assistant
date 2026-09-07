//go:build unix

package cli

import (
	"os/exec"
	"syscall"
)

// detach puts the launched service in a session of its own, so it is not in
// the terminal's foreground process group and does not receive the signals
// that terminal sends. A service that stopped because the shell that started
// it was interrupted would not be a background service.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
