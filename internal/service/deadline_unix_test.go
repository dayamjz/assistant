//go:build unix

package service_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/ipc"
	"github.com/dayamjz/assistant/internal/machine"
	"github.com/dayamjz/assistant/internal/principles"
	"github.com/dayamjz/assistant/internal/service"
)

// PRD section 8's process model and P15 for git, end to end: with
// git_deadline set to its floor in the operator's file, a status request
// whose read of the working copy meets a git that never exits answers once
// that deadline passes, not at the ten-minute default, and the git is ended
// with its group. The request carries no deadline of its own, so the bound
// that ends it is the one the service built its handle with.
func TestTheOperatorsGitDeadlineEndsAGitThatNeverExits(t *testing.T) {
	principles.Cite(t, principles.P15)
	const configured = time.Minute
	h := newHome(t)
	writeOperatorConfig(t, h.ConfigFile(), "git_deadline: "+configured.String()+"\n")
	marker := filepath.Join(t.TempDir(), "hang")
	recorded := service.HangOnPathForTest(t, "git", marker)
	working := t.TempDir()
	withService(t, h, func(running serviceUnderTest) {
		if err := os.WriteFile(marker, nil, 0o600); err != nil {
			t.Fatalf("arming the stand-in: %v", err)
		}
		// Well short of the default, so a request the configured deadline
		// did not reach fails here rather than at the default.
		limit := configured + time.Minute
		ctx, cancel := context.WithTimeout(t.Context(), limit)
		defer cancel()
		var status machine.Status
		start := time.Now()
		err := running.client.Call(ctx, ipc.MethodStatus, machine.StatusRequest{
			Working: machine.Working{WorkingPath: working},
		}, &status)
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("status after %s: %v; the operator's %s did not bound the git it waited on", elapsed, err, configured)
		}
		if elapsed < configured {
			t.Errorf("status answered after %s, before the operator's %s", elapsed, configured)
		}
		if status.Branch == nil || !strings.Contains(status.Branch.Detail, context.DeadlineExceeded.Error()) {
			t.Errorf("status reported the branch as %+v; want the read refused at its deadline", status.Branch)
		}
	})
	pids := recorded()
	if len(pids) != 1 {
		t.Fatalf("the stand-in recorded %d waiting invocations, want the one read", len(pids))
	}
	service.RequireEndedForTest(t, pids)
}
