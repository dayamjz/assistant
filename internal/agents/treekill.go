package agents

import (
	"context"
	"os/exec"
	"time"

	"github.com/dayamjz/assistant/internal/procgroup"
)

// treeKillDeadline bounds one run of the program that ends a process tree
// forcefully, taskkill on Windows. That program is short-lived: it signals
// and exits, so a run that has not ended by then is one that will not, and
// the caller falls back to ending the leader alone, as it does for any
// other failure of it.
const treeKillDeadline = 10 * time.Second

// treeKillGrace is the grace procgroup.Run is given for that program;
// procgroup.Run says each wait it bounds.
const treeKillGrace = time.Second

// runTreeKill runs cmd, the program that ends a process tree, built with
// exec.Command and not yet started, through procgroup.Run under deadline,
// which every caller passes as treeKillDeadline and a test passes shorter,
// which is PRD section 8's terms for an invocation: a
// deadline, and a process group ended when the call returns or times out.
// It returns procgroup.Run's answer, so an expiry is an error the caller
// reads as the program's failure.
//
// It is the one start of that program, so it lives beside no platform:
// only Windows calls it today, and its tests run it on the platforms CI
// runs. What procgroup.Run does not do on Windows, group the call and bound
// the wait for a killed program, is procgroup's package comment's to state.
func runTreeKill(cmd *exec.Cmd, deadline time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()
	return procgroup.Run(ctx, cmd, treeKillGrace)
}
