//go:build unix

package service_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dayamjz/assistant/internal/gate"
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

// The same bound reaches the git a gate's hook makes the service run: with
// git_deadline set to its floor, an admission whose resolution of the gate's
// working copy meets a git that never exits is refused once that deadline
// passes, not at the ten-minute default, and the git is ended with its group.
// The working copy is resolved through internal/gate, which opens it with the
// options the service hands it; a gate that opened it with its own defaults
// would leave a hook's push waiting on a bound the operator did not set. The
// refusal's wording is the gate's: its ownership rule reads a working copy it
// could not open as holding nothing, so the refusal says the gate is unbound
// rather than naming the deadline, and this asserts the refusal and its time,
// not its words.
func TestTheOperatorsGitDeadlineReachesTheGitAGateAdmissionRuns(t *testing.T) {
	principles.Cite(t, principles.P15)
	requiresIdentifiedPeer(t)
	const configured = time.Minute
	h := newHome(t)
	subject := newSubject(t)
	recordRepository(t, h, subject)
	gateID := func() string {
		records := openRecords(t, h)
		defer func() { _ = records.Close() }()
		binding, err := records.GateBinding(t.Context(), subject)
		if err != nil {
			t.Fatalf("reading the gate binding: %v", err)
		}
		return binding.GateID
	}()
	head := git(t, subject, "rev-parse", "HEAD")
	writeOperatorConfig(t, h.ConfigFile(), "git_deadline: "+configured.String()+"\n")
	marker := filepath.Join(t.TempDir(), "hang")
	recorded := service.HangOnPathForTest(t, "git", marker)
	withService(t, h, func(running serviceUnderTest) {
		if err := os.WriteFile(marker, nil, 0o600); err != nil {
			t.Fatalf("arming the stand-in: %v", err)
		}
		// Well short of the default, so an admission the configured deadline
		// did not reach fails here rather than at the default.
		limit := configured + time.Minute
		ctx, cancel := context.WithTimeout(t.Context(), limit)
		defer cancel()
		var admission machine.Admission
		start := time.Now()
		err := running.client.Call(ctx, ipc.MethodGateAdmit, machine.GateRequest{
			Gate: gateID, Updates: []gate.RefUpdate{{Old: head, New: head, Ref: "refs/heads/work"}},
		}, &admission)
		elapsed := time.Since(start)
		if ctx.Err() != nil {
			t.Fatalf("the admission had not answered after %s; the operator's %s did not bound the git it waited on", elapsed, configured)
		}
		if err == nil {
			t.Fatalf("the admission was granted (%+v) with every git wedged; want it refused", admission)
		}
		if elapsed < configured {
			t.Errorf("the admission answered after %s, before the operator's %s", elapsed, configured)
		}
	})
	pids := recorded()
	if len(pids) == 0 {
		t.Fatal("the stand-in recorded no waiting invocation, so the admission never met the wedged git")
	}
	service.RequireEndedForTest(t, pids)
}
