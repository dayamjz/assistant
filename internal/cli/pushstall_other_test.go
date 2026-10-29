//go:build !linux && !darwin

package cli_test

import (
	"errors"
	"os/exec"
)

// giveUp kills git push alone here. Every test that pushes into a gate skips
// on a platform with no local socket peer credentials, which is every
// platform this file builds for, so no push reaches a receive command or a
// hook here for it to list or end, and its verdict is never that the push
// ended.
func (p *boundedPush) giveUp() gaveUp {
	_ = p.cmd.Process.Kill()
	<-p.done
	return gaveUp{
		said:     "the push's processes are not listed on this platform; git push alone was killed\n",
		notEnded: errors.New("nothing the push started is read on this platform, so it is not shown ended"),
	}
}

// inOwnGroup leaves cmd as it is here, since giveUp reads no group.
func inOwnGroup(*exec.Cmd) {}

// spawnerMain returns at once here: no test on this platform parks the
// spawner in a push, for the reason giveUp gives.
func spawnerMain() {}

// leaverMain returns at once here, for the reason spawnerMain does.
func leaverMain() {}
